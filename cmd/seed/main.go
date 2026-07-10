// Command seed performs the one-shot ingestion: GitHub tarball → in-memory
// transform → bulk index → forcemerge. Nothing is written to disk; the
// server never seeds.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"

	"github.com/AndresThePerez/pokesearch/internal/esindex"
	"github.com/AndresThePerez/pokesearch/internal/tcg"
)

const chunkSize = 1000

func main() {
	esURL := flag.String("es", "http://127.0.0.1:9200", "Elasticsearch URL")
	ref := flag.String("ref", "master", "pokemon-tcg-data git ref to ingest")
	force := flag.Bool("force", false, "delete and recreate a populated index")
	flag.Parse()
	if err := run(*esURL, *ref, *force); err != nil {
		log.Fatalf("seed: %v", err)
	}
}

func run(esURL, ref string, force bool) error {
	start := time.Now()
	es, err := elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{esURL}})
	if err != nil {
		return err
	}

	// Guard: leave a populated index alone unless -force. An existing empty
	// index is an interrupted seed, so recreate it instead of treating it as
	// complete. Count transport and HTTP errors are surfaced immediately.
	res, err := es.Count(es.Count.WithIndex(esindex.IndexName))
	if err != nil {
		return fmt.Errorf("inspect index: %w", err)
	}
	body, readErr := io.ReadAll(res.Body)
	res.Body.Close()
	if readErr != nil {
		return fmt.Errorf("inspect index body: %w", readErr)
	}
	if res.StatusCode == http.StatusOK {
		var existing struct {
			Count int `json:"count"`
		}
		if err := unmarshal(body, &existing); err != nil {
			return fmt.Errorf("inspect index: %w", err)
		}
		if existing.Count > 0 && !force {
			fmt.Printf("index %q already populated (count %d) — nothing to do (use -force to reseed)\n",
				esindex.IndexName, existing.Count)
			return nil
		}
		if err := do(es.Indices.Delete([]string{esindex.IndexName})); err != nil {
			return fmt.Errorf("delete index: %w", err)
		}
		if existing.Count == 0 {
			fmt.Printf("deleted empty index %q before seeding\n", esindex.IndexName)
		} else {
			fmt.Printf("deleted existing index %q (-force)\n", esindex.IndexName)
		}
	} else if res.StatusCode != http.StatusNotFound {
		return fmt.Errorf("inspect index: ES %s: %s", res.Status(), truncate(string(body), 500))
	}

	url := "https://codeload.github.com/AndresThePerez/pokemon-tcg-data/tar.gz/" + ref
	fmt.Printf("fetching %s\n", url)
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download: HTTP %d for %s", resp.StatusCode, url)
	}
	archive, err := tcg.ParseArchive(resp.Body)
	if err != nil {
		return err
	}
	docs, err := archive.Docs()
	if err != nil {
		return err
	}
	fmt.Printf("parsed %d sets, %d cards\n", len(archive.Sets), len(docs))

	if err := do(es.Indices.Create(esindex.IndexName,
		es.Indices.Create.WithBody(strings.NewReader(esindex.Mapping)))); err != nil {
		return fmt.Errorf("create index: %w", err)
	}
	if err := do(es.Indices.PutSettings(strings.NewReader(`{"index":{"refresh_interval":"-1"}}`),
		es.Indices.PutSettings.WithIndex(esindex.IndexName))); err != nil {
		return fmt.Errorf("disable refresh: %w", err)
	}

	bodies, err := esindex.BulkBodies(docs, chunkSize)
	if err != nil {
		return err
	}
	for i, body := range bodies {
		res, err := es.Bulk(strings.NewReader(string(body)), es.Bulk.WithIndex(esindex.IndexName))
		if err != nil {
			return fmt.Errorf("bulk chunk %d: %w", i, err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.IsError() || strings.Contains(string(raw), `"errors":true`) {
			return fmt.Errorf("bulk chunk %d failed: %s", i, truncate(string(raw), 500))
		}
		fmt.Printf("bulk %d/%d\n", i+1, len(bodies))
	}

	if err := do(es.Indices.PutSettings(strings.NewReader(`{"index":{"refresh_interval":"30s"}}`),
		es.Indices.PutSettings.WithIndex(esindex.IndexName))); err != nil {
		return fmt.Errorf("restore refresh: %w", err)
	}
	if err := do(es.Indices.Refresh(es.Indices.Refresh.WithIndex(esindex.IndexName))); err != nil {
		return fmt.Errorf("refresh: %w", err)
	}
	if err := do(es.Indices.Forcemerge(
		es.Indices.Forcemerge.WithIndex(esindex.IndexName),
		es.Indices.Forcemerge.WithMaxNumSegments(1))); err != nil {
		return fmt.Errorf("forcemerge: %w", err)
	}

	res, err = es.Count(es.Count.WithIndex(esindex.IndexName))
	if err != nil {
		return fmt.Errorf("count: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var count struct {
		Count int `json:"count"`
	}
	if err := unmarshal(raw, &count); err != nil {
		return err
	}
	if count.Count != len(docs) {
		return fmt.Errorf("count mismatch: indexed %d, _count says %d", len(docs), count.Count)
	}
	fmt.Printf("seeded %d cards into %q in %s (ref %s)\n",
		count.Count, esindex.IndexName, time.Since(start).Round(time.Millisecond), ref)
	return nil
}

// do drains an esapi call, returning an error when transport or HTTP failed.
func do(res *esapi.Response, err error) error {
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.IsError() {
		raw, _ := io.ReadAll(res.Body)
		return fmt.Errorf("ES %s: %s", res.Status(), truncate(string(raw), 500))
	}
	io.Copy(io.Discard, res.Body)
	return nil
}

func unmarshal(raw []byte, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("decode ES response %q: %w", truncate(string(raw), 200), err)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
