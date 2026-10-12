package server

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/elastic/go-elasticsearch/v8/esapi"

	"github.com/AndresThePerez/pokesearch/internal/esindex"
	"github.com/AndresThePerez/pokesearch/internal/search"
	"github.com/AndresThePerez/pokesearch/internal/tcg"
)

// The /api/v1/ surface is the versioned card API a second client builds on.
// It is additive: the /api/* endpoints the UI uses are untouched. Card ids
// reach Elasticsearch only inside an _mget body (search.BuildCardsMget), never
// in a URL path, because the ES client does not escape a document id it puts
// in a path and "ex10-?" would arrive as "ex10-".

const (
	codeNotFound     = "not_found"
	codeUnauthorized = "unauthorized"
)

// v1Seed is the corpus provenance every v1 answer carries. Both fields are
// always present and null when the index was seeded before provenance
// stamping, so the response shape never depends on the index's age.
type v1Seed struct {
	Ref      *string `json:"ref"`
	SeededAt *string `json:"seeded_at"`
}

type v1CardResponse struct {
	Data tcg.Card `json:"data"`
	Seed v1Seed   `json:"seed"`
}

type v1CardsResponse struct {
	Data    []tcg.Card `json:"data"`
	Missing []string   `json:"missing"`
	Seed    v1Seed     `json:"seed"`
}

type esMgetResponse struct {
	Docs []struct {
		ID     string          `json:"_id"`
		Found  bool            `json:"found"`
		Source *tcg.Card       `json:"_source"`
		Error  json.RawMessage `json:"error"`
	} `json:"docs"`
}

// handleV1Card answers GET /api/v1/cards/{id}. The mux has already unescaped
// the segment, so "%3F" is a literal "?" here and "%2F" a literal "/", which
// the pattern rejects. It is also registered at /api/v1/cards/{$}, so an empty
// id gets the 400 envelope rather than the static file server's plain 404.
func (s *Server) handleV1Card(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	entry := s.queryLog(r, "v1_card")
	entry.Params = map[string]any{"id": id}

	if !search.ValidCardID(id) {
		s.v1BadRequest(w, r, entry, "id", "id must match "+search.CardIDPattern)
		return
	}
	found, err := s.v1Lookup(r, &entry, []string{id})
	if err != nil {
		s.writeES503(w, r, entry, err)
		return
	}
	card, ok := found[id]
	if !ok {
		entry.Status = http.StatusNotFound
		s.writeLog(entry)
		s.writeError(w, r, http.StatusNotFound, codeNotFound, "id", "no card with id "+id)
		return
	}
	seed, err := s.v1Seed(r)
	if err != nil {
		s.writeES503(w, r, entry, err)
		return
	}
	entry.Status = s.writeV1(w, r, v1CardResponse{Data: card, Seed: seed})
	s.writeLog(entry)
}

// handleV1Cards answers GET /api/v1/cards?ids=a,b,c. A valid request is always
// a 200, even when nothing was found: "missing" is the answer for those ids.
func (s *Server) handleV1Cards(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("ids")
	entry := s.queryLog(r, "v1_cards")
	entry.Params = map[string]any{"ids": raw}

	ids, err := search.ParseCardIDs(raw)
	if err != nil {
		s.v1BadRequest(w, r, entry, "ids", err.Error())
		return
	}
	found, err := s.v1Lookup(r, &entry, ids)
	if err != nil {
		s.writeES503(w, r, entry, err)
		return
	}
	seed, err := s.v1Seed(r)
	if err != nil {
		s.writeES503(w, r, entry, err)
		return
	}
	resp := v1CardsResponse{Data: make([]tcg.Card, 0, len(found)), Missing: []string{}, Seed: seed}
	for _, id := range ids {
		if card, ok := found[id]; ok {
			resp.Data = append(resp.Data, card)
		} else {
			resp.Missing = append(resp.Missing, id)
		}
	}
	entry.Status = s.writeV1(w, r, resp)
	s.writeLog(entry)
}

func (s *Server) v1BadRequest(w http.ResponseWriter, r *http.Request, entry QueryLog, field, message string) {
	entry.Status = http.StatusBadRequest
	entry.Error = message
	s.writeLog(entry)
	s.writeError(w, r, http.StatusBadRequest, codeInvalidParam, field, message)
}

// v1Lookup runs the one _mget a v1 request makes and returns the found cards
// by id. A per-document error (an unseeded or closed index answers _mget with
// a 200 whose docs carry an error) is the cluster failing to answer, not a
// missing card, so it fails the request rather than reporting "not found".
func (s *Server) v1Lookup(r *http.Request, entry *QueryLog, ids []string) (map[string]tcg.Card, error) {
	dsl := search.BuildCardsMget(ids)
	entry.DSL = dsl
	body, err := json.Marshal(dsl)
	if err != nil {
		return nil, err
	}
	started := s.now()
	esr, err := esCall[esMgetResponse](s, r, func(ctx context.Context) (*esapi.Response, error) {
		return s.es.Mget(bytes.NewReader(body),
			s.es.Mget.WithContext(ctx),
			s.es.Mget.WithIndex(esindex.IndexName),
		)
	})
	entry.TookMs = int(s.now().Sub(started).Milliseconds())
	if err != nil {
		return nil, err
	}
	found := make(map[string]tcg.Card, len(esr.Docs))
	for _, doc := range esr.Docs {
		if len(doc.Error) > 0 && !bytes.Equal(doc.Error, []byte("null")) {
			return nil, fmt.Errorf("es _mget %s: %s", doc.ID, doc.Error)
		}
		if doc.Found && doc.Source != nil {
			found[doc.ID] = *doc.Source
		}
	}
	entry.Total = len(found)
	return found, nil
}

// v1Seed reads the cached seed provenance and spells absence as two nulls.
func (s *Server) v1Seed(r *http.Request) (v1Seed, error) {
	meta, err := s.loadSeedMeta(r)
	if err != nil {
		return v1Seed{}, err
	}
	var seed v1Seed
	if meta != nil {
		seed.Ref = nonEmpty(meta.Ref)
		seed.SeededAt = nonEmpty(meta.SeededAt)
	}
	return seed, nil
}

func nonEmpty(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// writeV1 sends a v1 200 with a validator, or the 304 a matching
// If-None-Match earns, and returns the status it wrote.
//
// The tag is weak because withGzip encodes the body after this handler has
// hashed it: one tag then covers both codings, which is exactly what a weak
// validator promises (RFC 9110 §8.8.3). The body carries no request_id (the
// X-Request-Id header does), so equal answers hash equal. no-cache makes every
// reuse a conditional request, and a reseed changes the body and the tag.
func (s *Server) writeV1(w http.ResponseWriter, r *http.Request, v any) int {
	var buf bytes.Buffer
	// The v1 shapes hold strings, ints and slices of them only, which
	// encoding/json cannot fail on — the same reasoning writeJSON relies on.
	_ = json.NewEncoder(&buf).Encode(v)
	sum := sha256.Sum256(buf.Bytes())
	opaque := `"` + hex.EncodeToString(sum[:])[:etagHexLen] + `"`

	h := w.Header()
	h.Set("ETag", "W/"+opaque)
	h.Set("Cache-Control", "no-cache")
	// etagMatches already drops a W/ prefix from each candidate, so comparing
	// against the opaque part is the weak comparison If-None-Match requires.
	if etagMatches(r.Header.Get("If-None-Match"), opaque) {
		w.WriteHeader(http.StatusNotModified)
		return http.StatusNotModified
	}
	h.Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
	return http.StatusOK
}

// v1Set is one entry of GET /api/v1/sets. card_count is how many cards of the
// set the index holds; total is the set's printed size, which can differ
// (secret rares, cards the dataset lacks).
type v1Set struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Series      string `json:"series"`
	Total       int    `json:"total"`
	ReleaseDate string `json:"release_date"`
	CardCount   int    `json:"card_count"`
}

type v1SetsResponse struct {
	Data []v1Set `json:"data"`
	Seed v1Seed  `json:"seed"`
}

type esV1SetsResponse struct {
	Aggregations map[string]struct {
		Buckets []struct {
			Key      string `json:"key"`
			DocCount int    `json:"doc_count"`
			Identity struct {
				Hits struct {
					Hits []struct {
						Source struct {
							SetName     string `json:"set_name"`
							SetSeries   string `json:"set_series"`
							SetTotal    int    `json:"set_total"`
							ReleaseDate string `json:"release_date"`
						} `json:"_source"`
					} `json:"hits"`
				} `json:"hits"`
			} `json:"identity"`
		} `json:"buckets"`
	} `json:"aggregations"`
}

// v1SetsCache holds the set list for the process lifetime: it only changes
// with a reseed, like the search catalog, but it is a separate cache over a
// separate request so neither can move the other.
type v1SetsCache struct {
	mu   sync.RWMutex
	sets []v1Set
}

// handleV1Sets returns the GET /api/v1/sets handler with its own cache, so
// every Server (and every test server) starts cold.
func (s *Server) handleV1Sets() http.HandlerFunc {
	cache := &v1SetsCache{}
	return func(w http.ResponseWriter, r *http.Request) {
		entry := s.queryLog(r, "v1_sets")
		entry.Params = map[string]any{}
		entry.DSL = search.BuildV1SetsQuery()

		sets, err := s.loadV1Sets(r, &entry, cache)
		if err != nil {
			s.writeES503(w, r, entry, err)
			return
		}
		seed, err := s.v1Seed(r)
		if err != nil {
			s.writeES503(w, r, entry, err)
			return
		}
		entry.Total = len(sets)
		entry.Status = s.writeV1(w, r, v1SetsResponse{Data: sets, Seed: seed})
		s.writeLog(entry)
	}
}

// loadV1Sets has loadSetCatalog's shape: the ES call runs outside the lock,
// and an empty list (an unseeded index) is served but never cached, so the
// first request after a seed heals it. The body carries no card id, so this
// is a plain _search; nothing reaches a URL path.
func (s *Server) loadV1Sets(r *http.Request, entry *QueryLog, cache *v1SetsCache) ([]v1Set, error) {
	cache.mu.RLock()
	cached := cache.sets
	cache.mu.RUnlock()
	if len(cached) > 0 {
		return cached, nil
	}

	started := s.now()
	esr, err := esQuery[esV1SetsResponse](s, r, entry.DSL)
	entry.TookMs = int(s.now().Sub(started).Milliseconds())
	if err != nil {
		return nil, err
	}
	buckets := esr.Aggregations[search.V1SetsAgg].Buckets
	sets := make([]v1Set, 0, len(buckets))
	for _, b := range buckets {
		set := v1Set{ID: b.Key, CardCount: b.DocCount}
		if hits := b.Identity.Hits.Hits; len(hits) > 0 {
			set.Name = hits[0].Source.SetName
			set.Series = hits[0].Source.SetSeries
			set.Total = hits[0].Source.SetTotal
			set.ReleaseDate = hits[0].Source.ReleaseDate
		}
		sets = append(sets, set)
	}
	// release_date is yyyy-MM-dd, so string order is date order; the id breaks
	// ties so two sets released on one day always list the same way.
	slices.SortFunc(sets, func(a, b v1Set) int {
		return cmp.Or(strings.Compare(a.ReleaseDate, b.ReleaseDate), strings.Compare(a.ID, b.ID))
	})
	if len(sets) == 0 {
		return sets, nil
	}

	cache.mu.Lock()
	if len(cache.sets) == 0 {
		cache.sets = sets
	}
	cached = cache.sets
	cache.mu.Unlock()
	return cached, nil
}

// The optional API key. It identifies a client; it does not gate the public
// demo: no header is anonymous access, exactly as before. A header that
// names no configured key is a 401, so a client with a stale or mistyped key
// finds out instead of silently losing whatever keyed access buys it later
// (the reason it exists: a future rate limiter can exempt keyed clients).
const (
	apiKeyHeader = "X-Api-Key"
	v1Prefix     = "/api/v1/"
	clientKeyed  = "keyed"
)

// apiKeys holds the SHA-256 of each configured key. Comparing digests keeps
// the comparison fixed-length, so neither a key's length nor its prefix leaks
// through timing, and the raw keys are not kept in memory past start-up.
type apiKeys [][sha256.Size]byte

// parseAPIKeys reads the comma list; whitespace around a member and empty
// members are not keys.
func parseAPIKeys(raw string) apiKeys {
	var keys apiKeys
	for part := range strings.SplitSeq(raw, ",") {
		if k := strings.TrimSpace(part); k != "" {
			keys = append(keys, sha256.Sum256([]byte(k)))
		}
	}
	return keys
}

// match compares against every configured key without stopping at a hit.
func (k apiKeys) match(presented string) bool {
	digest := sha256.Sum256([]byte(presented))
	found := 0
	for i := range k {
		found |= subtle.ConstantTimeCompare(digest[:], k[i][:])
	}
	return found == 1
}

// withAPIKey applies the key to /api/v1/* only; every other path ignores the
// header. Keys are read once, from the POKESEARCH_API_KEYS environment
// variable (a comma list), when the server is built.
// Neither the presented key nor a configured one is ever written to a log
// line or an error message.
func (s *Server) withAPIKey(next http.Handler) http.Handler {
	keys := parseAPIKeys(os.Getenv("POKESEARCH_API_KEYS"))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, v1Prefix) {
			next.ServeHTTP(w, r)
			return
		}
		presented := r.Header.Values(apiKeyHeader)
		if len(presented) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		// Two key headers are ambiguous, so they are not a keyed request.
		if len(presented) != 1 || !keys.match(presented[0]) {
			s.writeError(w, r, http.StatusUnauthorized, codeUnauthorized, "", "invalid API key")
			return
		}
		if c, ok := r.Context().Value(ctxClient{}).(*accessClient); ok {
			c.label = clientKeyed
		}
		next.ServeHTTP(w, r)
	})
}

type ctxClient struct{}

// accessClient is how a handler inside the observability middleware names the
// client on the request's access line. Only the label is recorded.
type accessClient struct{ label string }

func withAccessClient(ctx context.Context) (context.Context, *accessClient) {
	c := &accessClient{}
	return context.WithValue(ctx, ctxClient{}, c), c
}

func (c *accessClient) attrs() []slog.Attr {
	if c.label == "" {
		return nil
	}
	return []slog.Attr{slog.String("client", c.label)}
}
