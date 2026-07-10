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

	es, err := elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{esURL}})
	if err != nil {
		log.Fatalf("es client: %v", err)
	}
	s := server.New(es, web.Files, os.Stdout, time.Now)
	log.Printf("pokesearch listening on :%s (es %s)", port, esURL)
	log.Fatal(http.ListenAndServe(":"+port, s))
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
