package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

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

const codeNotFound = "not_found"

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
