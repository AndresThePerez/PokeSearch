// Command server is the single Pokesearch binary: JSON API plus embedded
// frontend. It never seeds and never touches GitHub.
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/elastic/go-elasticsearch/v8"

	"github.com/AndresThePerez/pokesearch/internal/server"
	"github.com/AndresThePerez/pokesearch/web"
)

func main() {
	port := envOr("PORT", "8080")
	esURL := envOr("ES_URL", "http://127.0.0.1:9200")

	es, err := elasticsearch.NewClient(newESConfig(esURL))
	if err != nil {
		log.Fatalf("es client: %v", err)
	}
	s := server.New(es, web.Files, os.Stdout, time.Now)
	log.Printf("pokesearch listening on :%s (es %s)", port, esURL)
	log.Fatal(http.ListenAndServe(":"+port, s))
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
