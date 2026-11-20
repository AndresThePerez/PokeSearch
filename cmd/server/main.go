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
	"strconv"
	"syscall"
	"time"

	"github.com/elastic/go-elasticsearch/v8"

	"github.com/AndresThePerez/pokesearch/internal/server"
	"github.com/AndresThePerez/pokesearch/web"
)

// config is everything run() needs; main owns the environment, run owns the
// lifecycle. Keeping them apart is what makes the shutdown path testable.
type config struct {
	port        string
	esURL       string
	metrics     bool
	maxInFlight int
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

	maxInFlight, err := maxInFlightSetting(os.Getenv("MAX_IN_FLIGHT"))
	if err != nil {
		slog.Error("PokéSearch cannot start", "err", err)
		os.Exit(1)
	}
	cfg := config{
		port:        port,
		esURL:       envOr("ES_URL", "http://127.0.0.1:9200"),
		metrics:     os.Getenv("METRICS") == "1",
		maxInFlight: maxInFlight,
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
	// Zero is the field's unset value and leaves the server's default; the
	// environment never yields it, and a negative is refused here.
	maxInFlight := server.DefaultMaxInFlight
	if cfg.maxInFlight != 0 {
		if err := s.SetMaxInFlight(cfg.maxInFlight); err != nil {
			return err
		}
		maxInFlight = cfg.maxInFlight
	}
	if cfg.metrics {
		// Opt-in only: the production reverse proxy forwards whatever path it
		// is handed, so METRICS=1 belongs to local and CI runs, never the
		// server topology.
		s.EnableMetrics()
		slog.Info("expvar metrics enabled", "path", "/debug/vars")
	}
	srv := newHTTPServer(":"+cfg.port, s)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	slog.Info("PokéSearch listening", "port", cfg.port, "es", cfg.esURL, "max_in_flight", maxInFlight)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	}
}

// maxRequestHeaderBytes caps the request line and headers of one request.
// Go's default is 1 MB, which lets one connection make the process hold
// hundreds of times more than any real request is: a list parameter is
// copied several times while the query is built and again into the query
// log, so a long URL costs a multiple of its size in memory. 16 KiB leaves
// room for the longest request the page makes, a 256-character query with
// every byte percent-encoded (about 2.3 KB), a lookup of 100 ids of the
// pattern's full 128 characters (about 13 KB) and the few KB of headers a
// CDN and a browser add. Go reads up to 4096 bytes beyond this value, so a
// request head of up to 20,480 bytes, blank line included, is served; a
// larger one is answered 431 by net/http itself, before any handler runs.
// A lookup whose ids contain "?" and are sent percent-encoded can pass that.
const maxRequestHeaderBytes = 16 << 10

// newHTTPServer is the server run() serves with: the four timeouts that bound
// a slow client and the request head limit above.
func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		MaxHeaderBytes:    maxRequestHeaderBytes,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// esMaxRetries and esRetryBackoff bound what one failing Elasticsearch call
// can cost: a single retry after a short pause. A dead or refusing cluster
// then fails a request in a fraction of a second instead of several, and a
// struggling one is not handed a multiple of its load.
const (
	esMaxRetries   = 1
	esRetryBackoff = 100 * time.Millisecond
)

// newESConfig is the explicit client configuration: retries and backoff are
// stated rather than inherited, and the transport's idle-connection pool is
// widened because Go's default of 2 idle conns per host thrashes connections
// under ~50 concurrent searches (Courier perf mode) and distorts every
// latency number the observability rail reports.
//
// 503 is deliberately not retried: it is Elasticsearch saying it is
// overloaded or not ready, and an immediate second request only adds to
// that. RetryOnStatus must stay non-empty, since the client substitutes its
// own defaults (503 among them) for an empty list.
func newESConfig(esURL string) elasticsearch.Config {
	return elasticsearch.Config{
		Addresses:     []string{esURL},
		MaxRetries:    esMaxRetries,
		RetryOnStatus: []int{http.StatusBadGateway, http.StatusGatewayTimeout},
		// The client also waits after its final attempt, so only the pauses
		// before a real retry are non-zero.
		RetryBackoff: func(attempt int) time.Duration {
			if attempt > esMaxRetries {
				return 0
			}
			return esRetryBackoff
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

// maxInFlightSetting reads MAX_IN_FLIGHT: unset is the server's default, a
// positive whole number is taken as it is, and anything else (zero, a
// negative, a fraction, a number too large to hold, stray spaces) is an error
// that stops the server from starting. Falling back with a warning instead
// would let a typo in the compose file ship a cap nobody chose, and zero must
// never be read as "no cap".
func maxInFlightSetting(raw string) (int, error) {
	if raw == "" {
		return server.DefaultMaxInFlight, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("MAX_IN_FLIGHT must be a whole number of at least 1, got %q", raw)
	}
	return n, nil
}
