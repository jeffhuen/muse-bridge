// Command muse-bridge-go is a Go port of the muse-bridge Python daemon.
// It listens on localhost only and forwards /v1/* to the Model API,
// minting a key from the stored Meta identity. Subcommands:
//
//	muse-bridge-go [-port N] [-max-flights N]   run the daemon
//	muse-bridge-go login                        one-time Meta device login
//	muse-bridge-go -version                     print the build version
//
// The binary is portable: pure Go standard library, no cgo, no syscalls
// beyond signal handling, TCP sockets only. It cross-compiles to every
// platform Muse supports; see package.sh.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/jeffhuen/muse-bridge/muse-bridge-go/internal/auth"
	"github.com/jeffhuen/muse-bridge/muse-bridge-go/internal/config"
	"github.com/jeffhuen/muse-bridge/muse-bridge-go/internal/keys"
	"github.com/jeffhuen/muse-bridge/muse-bridge-go/internal/logfile"
	"github.com/jeffhuen/muse-bridge/muse-bridge-go/internal/proxy"
)

// version is stamped at release time: go build -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// interruptContext cancels on Ctrl-C / SIGTERM. Both names compile on
// every GOOS (verified by package.sh); where a signal never arrives
// (Windows services) the context simply never cancels.
func interruptContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func run() error {
	authClient := &http.Client{Timeout: config.AuthTimeout}
	if len(os.Args) > 1 && os.Args[1] == "login" {
		ctx, stop := interruptContext()
		defer stop()
		return auth.DoLogin(ctx, authClient)
	}
	port := flag.Int("port", config.DefaultPort, "localhost port to listen on")
	maxFlights := flag.Int("max-flights", config.DefaultMaxFlights, "max concurrent upstream requests")
	showVersion := flag.Bool("version", false, "print the build version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return nil
	}

	// Daemon logs go to a self-rotating file. Launchers must not
	// redirect stdout here or the two writers will corrupt it. The
	// login path above keeps the default stderr output.
	lf, err := logfile.Open(
		filepath.Join(config.BaseDir(), "bridge-go.log"),
		config.LogMaxBytes, config.LogBackups)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	log.SetOutput(lf)

	handler := proxy.New(
		keys.NewStore(authClient, config.KeyTTL),
		proxy.NewUpstreamClient(*maxFlights),
		config.UpstreamBase,
		*maxFlights,
		config.DebugOn(),
	)

	mux := http.NewServeMux()
	mux.Handle("/", handler)
	srv := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", *port),
		Handler:           mux,
		ReadHeaderTimeout: config.ReadHeaderTime,
		IdleTimeout:       config.IdleTime,
		// Deliberately no WriteTimeout: it is a wall clock on the whole
		// handler, so it would kill legitimate long streams.
	}
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	// Serve runs in the background so main can block on the signal and
	// drive Shutdown synchronously: calling Shutdown from a goroutine
	// would close the listener, return Serve early, and exit main
	// before in-flight streams drain.
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	log.Printf("muse-bridge-go %s listening on 127.0.0.1:%d", version, *port)

	ctx, stop := interruptContext()
	defer stop()
	select {
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), config.ShutdownTime)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-serveErr; err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}
