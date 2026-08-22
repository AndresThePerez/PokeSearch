// Command seed performs the one-shot ingestion: GitHub tarball → in-memory
// transform → bulk index → forcemerge. Nothing is written to disk; the
// server never seeds.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"

	"github.com/AndresThePerez/pokesearch/internal/esindex"
	"github.com/AndresThePerez/pokesearch/internal/tcg"
)

const chunkSize = 1000

// defaultTarballBase is the corpus source; the git ref is appended to it. It
// is a flag rather than a constant so run() can be pointed at a local fixture
// server and tested without reaching GitHub.
const defaultTarballBase = "https://codeload.github.com/AndresThePerez/pokemon-tcg-data/tar.gz/"

func main() {
	esURL := flag.String("es", "http://127.0.0.1:9200", "Elasticsearch URL")
	ref := flag.String("ref", "master", "pokemon-tcg-data git ref to ingest")
	force := flag.Bool("force", false, "delete and recreate a populated index")
	tarballBase := flag.String("tarball-base", defaultTarballBase,
		"base URL for the corpus tarball; the -ref value is appended to it")
	flag.Parse()
	if err := run(*esURL, *tarballBase, *ref, *force); err != nil {
		slog.Error("seed failed", "err", err)
		os.Exit(1)
	}
}

func run(esURL, tarballBase, ref string, force bool) error {
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
			slog.Info("index already populated — nothing to do (use -force to reseed)",
				"index", esindex.IndexName, "count", existing.Count)
			return nil
		}
		if err := do(es.Indices.Delete([]string{esindex.IndexName})); err != nil {
			return fmt.Errorf("delete index: %w", err)
		}
		if existing.Count == 0 {
			slog.Info("deleted empty index before seeding", "index", esindex.IndexName)
		} else {
			slog.Info("deleted existing index (-force)", "index", esindex.IndexName)
		}
	} else if res.StatusCode != http.StatusNotFound {
		return fmt.Errorf("inspect index: ES %s: %s", res.Status(), truncate(string(body), 500))
	}

	url := tarballBase + ref
	slog.Info("fetching corpus", "url", url)
	resp, err := fetchCorpus(url)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	archive, err := tcg.ParseArchive(resp.Body)
	if err != nil {
		return err
	}
	docs, err := archive.Docs()
	if err != nil {
		return err
	}
	slog.Info("parsed corpus", "sets", len(archive.Sets), "cards", len(docs))

	if err := do(es.Indices.Create(esindex.IndexName,
		es.Indices.Create.WithBody(strings.NewReader(esindex.Mapping)))); err != nil {
		return fmt.Errorf("create index: %w", err)
	}
	// Stamp provenance into the mapping so the index can say which corpus
	// snapshot it holds. GET /api/meta reads it straight back out.
	if err := do(es.Indices.PutMapping([]string{esindex.IndexName},
		strings.NewReader(metaBody(ref, time.Now())))); err != nil {
		return fmt.Errorf("stamp _meta: %w", err)
	}
	if err := do(es.Indices.PutSettings(strings.NewReader(refreshDisabled),
		es.Indices.PutSettings.WithIndex(esindex.IndexName))); err != nil {
		return fmt.Errorf("disable refresh: %w", err)
	}
	// From here the index is in load configuration. Every exit path has to put
	// refresh back or the index never becomes searchable on its own — an
	// abandoned seed would leave a silently stale index behind. The success
	// path restores explicitly so its failure is fatal; this defer is the net
	// under every other path, and restore() no-ops once it has run.
	restored := false
	restore := func() error {
		if restored {
			return nil
		}
		restored = true
		return do(es.Indices.PutSettings(strings.NewReader(refreshRestored),
			es.Indices.PutSettings.WithIndex(esindex.IndexName)))
	}
	defer func() {
		if err := restore(); err != nil {
			slog.Error("could not restore refresh_interval; the index will not refresh on its own",
				"index", esindex.IndexName, "err", err)
		}
	}()

	chunks := (len(docs) + chunkSize - 1) / chunkSize
	for start, chunk := 0, 0; start < len(docs); start, chunk = start+chunkSize, chunk+1 {
		// Encoded, sent and released one chunk at a time — see BulkBody.
		body, err := esindex.BulkBody(docs[start:min(start+chunkSize, len(docs))])
		if err != nil {
			return err
		}
		res, err := es.Bulk(bytes.NewReader(body), es.Bulk.WithIndex(esindex.IndexName))
		if err != nil {
			return fmt.Errorf("bulk chunk %d: %w", chunk, err)
		}
		raw, readErr := io.ReadAll(res.Body)
		res.Body.Close()
		if readErr != nil {
			return fmt.Errorf("bulk chunk %d: %w", chunk, readErr)
		}
		if res.IsError() {
			return fmt.Errorf("bulk chunk %d failed: ES %s: %s", chunk, res.Status(), truncate(string(raw), 500))
		}
		var result bulkResponse
		if err := unmarshal(raw, &result); err != nil {
			return fmt.Errorf("bulk chunk %d: %w", chunk, err)
		}
		if result.Errors {
			return fmt.Errorf("bulk chunk %d rejected documents: %s",
				chunk, strings.Join(result.failures(), "; "))
		}
		slog.Info("bulk indexed", "chunk", chunk+1, "chunks", chunks)
	}

	if err := restore(); err != nil {
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
	slog.Info("seeded", "cards", count.Count, "index", esindex.IndexName,
		"took", time.Since(start).Round(time.Millisecond).String(), "ref", ref)
	return nil
}

// The two index settings the load toggles. Named so the restore path and its
// tests cannot drift from the disable path.
const (
	refreshDisabled = `{"index":{"refresh_interval":"-1"}}`
	refreshRestored = `{"index":{"refresh_interval":"30s"}}`
)

// downloadAttempts bounds the corpus retry loop. Timeout and backoff are
// variables rather than constants only so tests can exercise the loop without
// sleeping for six seconds.
const downloadAttempts = 3

var (
	downloadTimeout = 2 * time.Minute
	downloadBackoff = func(attempt int) time.Duration {
		return time.Duration(attempt) * 2 * time.Second
	}
)

// fetchCorpus downloads the source tarball, retrying transport failures and
// 5xx responses — a seed is a long, rare, expensive operation and GitHub has
// bad minutes. A 4xx is returned immediately: a bad ref will not become a good
// one on the third try. The caller owns the returned body.
func fetchCorpus(url string) (*http.Response, error) {
	client := &http.Client{Timeout: downloadTimeout}
	var lastErr error
	for attempt := 1; attempt <= downloadAttempts; attempt++ {
		if attempt > 1 {
			time.Sleep(downloadBackoff(attempt - 1))
		}
		res, err := client.Get(url)
		if err != nil {
			lastErr = err
		} else if res.StatusCode >= http.StatusInternalServerError {
			lastErr = fmt.Errorf("HTTP %d for %s: %s", res.StatusCode, url, readSnippet(res.Body))
			res.Body.Close()
		} else if res.StatusCode != http.StatusOK {
			res.Body.Close()
			return nil, fmt.Errorf("HTTP %d for %s", res.StatusCode, url)
		} else {
			return res, nil
		}
		slog.Warn("corpus download failed", "attempt", attempt, "of", downloadAttempts, "err", lastErr)
	}
	return nil, lastErr
}

func readSnippet(r io.Reader) string {
	raw, _ := io.ReadAll(io.LimitReader(r, 500))
	return string(raw)
}

// bulkResponse decodes _bulk's per-item results. ES reports document-level
// rejections with HTTP 200 and errors:true, so the status line says nothing
// about whether the documents actually landed.
type bulkResponse struct {
	Errors bool `json:"errors"`
	Items  []map[string]struct {
		ID     string `json:"_id"`
		Status int    `json:"status"`
		Error  *struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		} `json:"error"`
	} `json:"items"`
}

// maxReportedFailures caps how many rejected documents an error names. One
// mapping mistake rejects a whole chunk; five examples diagnose it, a thousand
// only fill the terminal.
const maxReportedFailures = 5

// failures names the rejected documents, so a failed seed says which cards
// ES refused and why instead of "errors: true".
func (b bulkResponse) failures() []string {
	out := make([]string, 0, maxReportedFailures)
	for _, item := range b.Items {
		for _, result := range item {
			if result.Error == nil {
				continue
			}
			out = append(out, fmt.Sprintf("%s (status %d): %s: %s",
				result.ID, result.Status, result.Error.Type, result.Error.Reason))
			if len(out) == maxReportedFailures {
				return out
			}
		}
	}
	return out
}

// metaBody builds the index mapping's _meta block. The two keys are a contract
// with the server's /api/meta decoder — renaming one here breaks provenance
// reporting there, which is what cmd/seed's TestMetaBody guards.
func metaBody(ref string, at time.Time) string {
	return fmt.Sprintf(`{"_meta":{"seed_ref":%q,"seeded_at":%q}}`,
		ref, at.UTC().Format(time.RFC3339))
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
	// Drain so the connection can be reused; a read failure here is moot.
	_, _ = io.Copy(io.Discard, res.Body)
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
