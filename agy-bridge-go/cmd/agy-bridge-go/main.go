// Command agy-bridge-go runs the localhost reverse proxy bridging Google Antigravity
// to OpenAI-compatible clients like Codex CLI, OpenCode, and pi.
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

	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/auth"
	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/config"
	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/logfile"
	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/proxy"
	"github.com/jeffhuen/muse-bridge/agy-bridge-go/internal/upstream"
)

var version = "dev"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func interruptContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func run() error {
	port := flag.Int("port", config.DefaultPort, "localhost port to listen on")
	maxFlights := flag.Int("max-flights", config.DefaultMaxFlights, "max concurrent requests")
	defaultModel := flag.String("model", config.DefaultModel, "default model")
	showVersion := flag.Bool("version", false, "print build version and exit")
	debugFlag := flag.Bool("debug", config.DebugOn(), "enable verbose logging")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return nil
	}

	lf, err := logfile.Open(config.LogPath(), config.LogMaxBytes, config.LogBackups)
	if err != nil {
		return fmt.Errorf("open logfile: %w", err)
	}
	log.SetOutput(lf)

	tokenStore := auth.NewStore(nil)
	upstreamClient := upstream.NewClient(tokenStore, nil, config.UpstreamURL())
	handler := proxy.New(upstreamClient, *maxFlights, *debugFlag)


	mux := http.NewServeMux()
	mux.Handle("/", handler)

	srv := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", *port),
		Handler:           mux,
		ReadHeaderTimeout: config.ReadHeaderTime,
		IdleTimeout:       config.IdleTime,
	}

	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", srv.Addr, err)
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	log.Printf("agy-bridge-go %s listening on 127.0.0.1:%d (default model: %s)", version, *port, *defaultModel)

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
