// Command server is the single PokéSearch binary: JSON API plus embedded
// frontend. It never seeds and never touches GitHub.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/elastic/go-elasticsearch/v8"

	"github.com/AndresThePerez/pokesearch/internal/server"
	"github.com/AndresThePerez/pokesearch/web"
)

// config is everything run() needs; main owns the environment, run owns the
// lifecycle. Keeping them apart is what makes the shutdown path testable.
type config struct {
	port    string
	esURL   string
	metrics bool
}

func main() {
	ping := flag.Bool("ping", false, "GET /livez on the local port and exit (container healthcheck)")
	flag.Parse()

	port := envOr("PORT", "8080")
	if *ping {
		// The distroless image has no shell and no curl, so the binary is its
		// own healthcheck client.
		client := &http.Client{Timeout: 2 * time.Second}
		res, err := client.Get("http://127.0.0.1:" + port + "/livez")
		if err != nil {
			os.Exit(1)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		os.Exit(0)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := config{
		port:    port,
		esURL:   envOr("ES_URL", "http://127.0.0.1:9200"),
		metrics: os.Getenv("METRICS") == "1",
	}
	if err := run(ctx, cfg); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("PokéSearch stopped", "err", err)
		os.Exit(1)
	}
}

// run serves until ctx is cancelled, then drains in-flight requests within a
// bounded window. Returns nil (or http.ErrServerClosed) on a clean stop.
func run(ctx context.Context, cfg config) error {
	es, err := elasticsearch.NewClient(newESConfig(cfg.esURL))
	if err != nil {
		return fmt.Errorf("es client: %w", err)
	}

	s := server.New(es, web.Files, os.Stdout, time.Now)
	if cfg.metrics {
		// Opt-in only: the production reverse proxy forwards whatever path it
		// is handed, so METRICS=1 belongs to local and CI runs, never the
		// server topology.
		s.EnableMetrics()
		slog.Info("expvar metrics enabled", "path", "/debug/vars")
	}
	srv := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           s,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	slog.Info("PokéSearch listening", "port", cfg.port, "es", cfg.esURL)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	}
}

// newESConfig is the explicit client configuration: retries and backoff are
// stated rather than inherited, and the transport's idle-connection pool is
// widened because Go's default of 2 idle conns per host thrashes connections
// under ~50 concurrent searches (Courier perf mode) and distorts every
// latency number the observability rail reports.
func newESConfig(esURL string) elasticsearch.Config {
	return elasticsearch.Config{
		Addresses:     []string{esURL},
		MaxRetries:    3,
		RetryOnStatus: []int{502, 503, 504},
		RetryBackoff: func(attempt int) time.Duration {
			return time.Duration(attempt*attempt) * 100 * time.Millisecond
		},
		Transport: &http.Transport{MaxIdleConns: 100, MaxIdleConnsPerHost: 100},
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
