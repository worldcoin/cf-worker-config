package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"
)

var (
	ready      atomic.Bool
	port       string
	healthPort string
	tickerTime int
	timeout    int
)

func main() {
	log.Logger = zerolog.New(os.Stdout).With().Timestamp().Logger()

	// Log configuration values loaded from environment
	log.Info().
		Str("healthPort", healthPort).
		Str("port", port).
		Msg("Configuration loaded")

	ready.Store(false)

	// Create HTTP servers
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		if ready.Load() {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ready"))
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("not ready"))
		}
	})
	healthMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	healthSrv := &http.Server{Addr: healthPort, Handler: healthMux}
	srv := &http.Server{Addr: port, Handler: mux}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	g, ctx := errgroup.WithContext(ctx)

	// Start servers and ticker
	g.Go(serve(healthSrv, "health"))
	g.Go(serve(srv, "app"))
	g.Go(ticker(ctx))

	// Handle graceful shutdown
	g.Go(func() error {
		<-ctx.Done()
		log.Info().Msg("Shutting down servers...")
		return shutdownAll(context.Background(), 5*time.Second, healthSrv, srv)
	})

	log.Info().Msg("Starting App")

	if err := g.Wait(); err != nil {
		log.Error().Err(err).Msg("App exited with error")
		os.Exit(1)
	}
	log.Info().Msg("App stopped gracefully")
}

// ticker returns a function that periodically runs business logic at fixed interval.
// It marks the application as ready after the first successful check and stops when ctx is cancelled.
// Parameters:
//
//	ctx: Context used to control cancellation of the ticker loop.
//
// Returns:
//
//	A function that runs the periodic business logic loop and returns an error if the context is cancelled.
func ticker(ctx context.Context) func() error {
	return func() error {
		ticker := time.NewTicker(time.Duration(tickerTime) * time.Second)
		defer ticker.Stop()

		// Run some code on startup
		log.Info().Msg("Performing initial code")

		// Mark as ready after first successful update
		ready.Store(true)
		log.Info().Msg("App is ready")

		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				businessLogic()
			}
		}
	}
}

// serve returns a function that starts the HTTP server and logs startup information.
// Returns nil if the server shuts down gracefully, otherwise returns an error.
func serve(srv *http.Server, name string) func() error {
	return func() error {
		log.Info().Str("name", name).Str("addr", srv.Addr).Msg("Starting server")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("%s server error: %w", name, err)
		}
		return nil
	}
}

// shutdownAll gracefully shuts down multiple HTTP servers concurrently with the given timeout.
// It starts a goroutine for each server to call Shutdown, and collects errors.
// Returns the first error encountered during shutdown, or nil if all servers shut down successfully.
func shutdownAll(ctx context.Context, timeout time.Duration, srvs ...*http.Server) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	errCh := make(chan error, len(srvs))
	for _, s := range srvs {
		go func(s *http.Server) {
			if s != nil {
				errCh <- s.Shutdown(ctx)
			} else {
				errCh <- nil
			}
		}(s)
	}

	var firstErr error
	for range len(srvs) {
		if err := <-errCh; err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
