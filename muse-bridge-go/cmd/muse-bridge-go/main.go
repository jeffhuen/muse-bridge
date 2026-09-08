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
	"syscall"

	"github.com/jeffhuen/muse-bridge/muse-bridge-go/internal/auth"
	"github.com/jeffhuen/muse-bridge/muse-bridge-go/internal/config"
	"github.com/jeffhuen/muse-bridge/muse-bridge-go/internal/keys"
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

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "login" {
		client := &http.Client{Timeout: config.AuthTimeout}
		return auth.DoLogin(client)
	}
	port := flag.Int("port", config.DefaultPort, "localhost port to listen on")
	maxFlights := flag.Int("max-flights", config.DefaultMaxFlights, "max concurrent upstream requests")
	showVersion := flag.Bool("version", false, "print the build version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return nil
	}

	upstreamClient := &http.Client{Timeout: config.UpstreamTimeout} // keep-alives on by default
	authClient := &http.Client{Timeout: config.AuthTimeout}
	handler := proxy.New(
		keys.NewStore(authClient, config.KeyTTL),
		upstreamClient,
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
	// os.Interrupt arrives as Ctrl-C / SIGINT everywhere including
	// Windows; SIGTERM covers launchd/systemd stops on Unix. Both names
	// compile on every GOOS (verified by package.sh).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), config.ShutdownTime)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	log.Printf("muse-bridge-go %s listening on 127.0.0.1:%d", version, *port)
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}
