package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// buildTarball writes a gzipped tar in memory. Deliberately duplicated from
// internal/tcg/tarball_test.go: a test helper is not importable across
// packages, and exporting one would put test-only API into internal/tcg.
func buildTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const seedSetsJSON = `[
  {"id":"base1","name":"Base","series":"Base","total":102,"releaseDate":"1999/01/09"},
  {"id":"ex11","name":"Delta Species","series":"EX","total":114,"releaseDate":"2005/10/31"}
]`

const seedBase1JSON = `[
  {"id":"base1-1","name":"Alakazam","supertype":"Pokémon","hp":"80","types":["Psychic"],"number":"1",
   "images":{"small":"https://images.pokemontcg.io/base1/1.png",
             "large":"https://images.pokemontcg.io/base1/1_hires.png"}},
  {"id":"base1-2","name":"Blastoise","supertype":"Pokémon","hp":"100","types":["Water"],"number":"2","images":{}},
  {"id":"base1-3","name":"Chansey","supertype":"Pokémon","hp":"120","types":["Colorless"],"number":"3","images":{}}
]`

const seedEx11JSON = `[
  {"id":"ex11-12","name":"Mewtwo δ","supertype":"Pokémon","hp":"70","types":["Fire","Metal"],"number":"12","images":{}},
  {"id":"ex11-13","name":"Rayquaza δ","supertype":"Pokémon","hp":"90","types":["Lightning"],"number":"13","images":{}}
]`

// seedDocCount is how many documents the fixture tarball transforms into — the
// number the seeder's final _count gate must agree with.
const seedDocCount = 5

func seedCorpus(t *testing.T) []byte {
	t.Helper()
	return buildTarball(t, map[string]string{
		"pokemon-tcg-data-ref/README.md":           "# ignored",
		"pokemon-tcg-data-ref/cards/en/base1.json": seedBase1JSON,
		"pokemon-tcg-data-ref/cards/en/ex11.json":  seedEx11JSON,
		"pokemon-tcg-data-ref/sets/en.json":        seedSetsJSON,
	})
}

// tarballServer serves the fixture corpus at GET /<ref>, the same shape as
// codeload.github.com. handler, when set, runs first and may answer instead.
type tarballServer struct {
	mu       sync.Mutex
	body     []byte
	attempts int
	paths    []string
	handler  func(attempt int, w http.ResponseWriter) bool // true = handled
}

func (s *tarballServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.attempts++
	attempt := s.attempts
	s.paths = append(s.paths, r.URL.Path)
	handler := s.handler
	s.mu.Unlock()

	if handler != nil && handler(attempt, w) {
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	_, _ = w.Write(s.body)
}

func (s *tarballServer) attemptCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts
}

func (s *tarballServer) requestedPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...)
}

// seedFake is a scripted Elasticsearch covering exactly the calls cmd/seed
// makes. Every response carries X-Elastic-Product, without which the v8 client
// refuses to talk to it.
type seedFake struct {
	mu sync.Mutex

	counts   []int // successive _count replies; -1 answers 404 (no such index)
	countIdx int

	bulkStatus int                                // non-zero overrides the _bulk status
	bulkBody   string                             // non-empty overrides the _bulk body
	settingsFn func(attempt int, body string) int // non-zero return overrides the status

	calls          []string // "METHOD /path", in order
	settingsBodies []string
	mappingBodies  []string
	bulkChunks     int
	bulkDocs       int
}

func (f *seedFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("X-Elastic-Product", "Elasticsearch")
	w.Header().Set("Content-Type", "application/json")

	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.mu.Unlock()

	switch {
	case strings.HasSuffix(r.URL.Path, "/_count"):
		n := f.nextCount()
		if n < 0 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"type":"index_not_found_exception"},"status":404}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"count":%d}`, n)

	case strings.HasSuffix(r.URL.Path, "/_mapping"):
		f.mu.Lock()
		f.mappingBodies = append(f.mappingBodies, string(body))
		f.mu.Unlock()
		_, _ = io.WriteString(w, `{"acknowledged":true}`)

	case strings.HasSuffix(r.URL.Path, "/_settings"):
		f.mu.Lock()
		f.settingsBodies = append(f.settingsBodies, string(body))
		attempt := len(f.settingsBodies)
		fn := f.settingsFn
		f.mu.Unlock()
		if fn != nil {
			if status := fn(attempt, string(body)); status != 0 {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":{"type":"illegal_argument_exception"}}`)
				return
			}
		}
		_, _ = io.WriteString(w, `{"acknowledged":true}`)

	case strings.HasSuffix(r.URL.Path, "/_bulk"):
		f.mu.Lock()
		f.bulkChunks++
		f.bulkDocs += bytes.Count(body, []byte(`{"index":`))
		status, reply := f.bulkStatus, f.bulkBody
		f.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
		}
		if reply == "" {
			reply = `{"took":1,"errors":false,"items":[]}`
		}
		_, _ = io.WriteString(w, reply)

	default: // create, delete, refresh, forcemerge
		_, _ = io.WriteString(w, `{"acknowledged":true}`)
	}
}

func (f *seedFake) nextCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.countIdx >= len(f.counts) {
		return 0
	}
	n := f.counts[f.countIdx]
	f.countIdx++
	return n
}

func (f *seedFake) snapshot() (calls, settings, mappings []string, chunks, docs int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...),
		append([]string(nil), f.settingsBodies...),
		append([]string(nil), f.mappingBodies...),
		f.bulkChunks, f.bulkDocs
}

// called reports whether any recorded call matches "METHOD /path" exactly.
func (f *seedFake) called(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == call {
			return true
		}
	}
	return false
}

// newSeedEnv wires a scripted ES and a fixture tarball server together and
// returns the two arguments run() needs to reach them.
func newSeedEnv(t *testing.T, fake *seedFake) (esURL, tarballBase string, tarball *tarballServer) {
	t.Helper()
	es := httptest.NewServer(fake)
	t.Cleanup(es.Close)

	tarball = &tarballServer{body: seedCorpus(t)}
	corpus := httptest.NewServer(tarball)
	t.Cleanup(corpus.Close)

	return es.URL, corpus.URL + "/", tarball
}
