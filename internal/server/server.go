// Package server is Pokesearch's HTTP layer: JSON endpoints plus the embedded
// static frontend. Elasticsearch is only reached from this package.
package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"sync"
	"time"

	"github.com/elastic/go-elasticsearch/v8"

	"github.com/AndresThePerez/pokesearch/internal/esindex"
	"github.com/AndresThePerez/pokesearch/internal/search"
)

type Server struct {
	es           *elasticsearch.Client
	mux          *http.ServeMux
	logW         io.Writer
	now          func() time.Time
	setCatalogMu sync.Mutex
	setCatalog   []facetBucket
}

func New(es *elasticsearch.Client, static fs.FS, logW io.Writer, now func() time.Time) *Server {
	if now == nil {
		now = time.Now
	}
	s := &Server{es: es, mux: http.NewServeMux(), logW: logW, now: now}
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /api/search", s.handleSearch)
	s.mux.HandleFunc("GET /api/suggest", s.handleSuggest)
	s.mux.Handle("GET /", http.FileServerFS(static))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	res, err := s.es.Count(
		s.es.Count.WithContext(r.Context()),
		s.es.Count.WithIndex(esindex.IndexName),
	)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "error"})
		return
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusNotFound {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "docs": 0})
		return
	}
	if res.IsError() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "error"})
		return
	}

	var body struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "docs": body.Count})
}

type facetBucket struct {
	Value       string `json:"value"`
	Label       string `json:"label,omitempty"`
	ReleaseDate string `json:"release_date,omitempty"`
	Count       int    `json:"count"`
}

type searchResponse struct {
	Total   int                      `json:"total"`
	Page    int                      `json:"page"`
	Pages   int                      `json:"pages"`
	TookMs  int                      `json:"took_ms"`
	Results []json.RawMessage        `json:"results"`
	Facets  map[string][]facetBucket `json:"facets"`
	DSL     map[string]any           `json:"dsl,omitempty"`
}

type esFacetBucket struct {
	Key      string `json:"key"`
	DocCount int    `json:"doc_count"`
	Identity struct {
		Hits struct {
			Hits []struct {
				Source struct {
					SetName     string `json:"set_name"`
					ReleaseDate string `json:"release_date"`
				} `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	} `json:"identity"`
}

// esAggregation decodes both direct terms aggregations and filtered facet
// scopes, whose terms buckets sit under the "items" sub-aggregation.
type esAggregation struct {
	Buckets []esFacetBucket `json:"buckets"`
	Items   struct {
		Buckets []esFacetBucket `json:"buckets"`
	} `json:"items"`
}

func (a esAggregation) buckets() []esFacetBucket {
	if a.Buckets != nil {
		return a.Buckets
	}
	return a.Items.Buckets
}

type esSearchResponse struct {
	Took int `json:"took"`
	Hits struct {
		Total struct {
			Value int `json:"value"`
		} `json:"total"`
		Hits []struct {
			Source json.RawMessage `json:"_source"`
		} `json:"hits"`
	} `json:"hits"`
	Aggregations map[string]esAggregation `json:"aggregations"`
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	p := search.ParseParams(r.URL.Query())
	dsl := search.BuildQuery(p)
	entry := QueryLog{
		Time:     s.now().UTC().Format(time.RFC3339),
		Endpoint: "search",
		Params:   logParams(p),
		DSL:      dsl,
	}

	esr, err := s.searchES(r, dsl)
	if err != nil {
		entry.Status = http.StatusServiceUnavailable
		writeLog(s.logW, entry)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "elasticsearch unavailable"})
		return
	}

	var setCatalog []facetBucket
	if _, hasAggs := dsl["aggs"]; hasAggs {
		setCatalog, err = s.loadSetCatalog(r)
		if err != nil {
			entry.TookMs = esr.Took
			entry.Total = esr.Hits.Total.Value
			entry.Status = http.StatusServiceUnavailable
			writeLog(s.logW, entry)
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "elasticsearch unavailable"})
			return
		}
	}

	resp := searchResponse{
		Total:   esr.Hits.Total.Value,
		Page:    p.Page,
		Pages:   pagesFor(esr.Hits.Total.Value),
		TookMs:  esr.Took,
		Results: make([]json.RawMessage, 0, len(esr.Hits.Hits)),
		Facets: map[string][]facetBucket{
			"supertype":  {},
			"types":      {},
			"rarity":     {},
			"set_series": {},
			"sets":       {},
		},
	}
	for _, hit := range esr.Hits.Hits {
		resp.Results = append(resp.Results, hit.Source)
	}
	for _, name := range []string{"supertype", "types", "rarity", "set_series"} {
		agg := esr.Aggregations[name]
		esBuckets := agg.buckets()
		buckets := make([]facetBucket, 0, len(esBuckets))
		for _, b := range esBuckets {
			buckets = append(buckets, facetBucket{Value: b.Key, Count: b.DocCount})
		}
		resp.Facets[name] = buckets
	}
	if setCatalog != nil {
		resp.Facets["sets"] = mergeSetCatalog(setCatalog, esr.Aggregations["sets"])
	}
	if p.Debug {
		resp.DSL = dsl
	}

	entry.TookMs = esr.Took
	entry.Total = esr.Hits.Total.Value
	entry.Status = http.StatusOK
	writeLog(s.logW, entry)
	writeJSON(w, http.StatusOK, resp)
}

// loadSetCatalog lazily fetches the immutable set metadata once per app
// process. The standalone size:0 request is also eligible for ES's request
// cache, while hot search requests only calculate dynamic counts.
func (s *Server) loadSetCatalog(r *http.Request) ([]facetBucket, error) {
	s.setCatalogMu.Lock()
	defer s.setCatalogMu.Unlock()
	if s.setCatalog != nil {
		return s.setCatalog, nil
	}

	esr, err := s.searchES(r, search.BuildSetCatalogQuery())
	if err != nil {
		return nil, err
	}
	esBuckets := esr.Aggregations["set_catalog"].buckets()
	catalog := make([]facetBucket, 0, len(esBuckets))
	for _, b := range esBuckets {
		bucket := facetBucket{Value: b.Key}
		if len(b.Identity.Hits.Hits) > 0 {
			bucket.Label = b.Identity.Hits.Hits[0].Source.SetName
			bucket.ReleaseDate = b.Identity.Hits.Hits[0].Source.ReleaseDate
		}
		catalog = append(catalog, bucket)
	}
	s.setCatalog = catalog
	return s.setCatalog, nil
}

// mergeSetCatalog joins cached labels/releases with per-request dynamic
// counts: every catalog set stays visible, and non-matching sets read 0.
func mergeSetCatalog(catalog []facetBucket, dynamic esAggregation) []facetBucket {
	dynamicBuckets := dynamic.buckets()
	counts := make(map[string]int, len(dynamicBuckets))
	for _, b := range dynamicBuckets {
		counts[b.Key] = b.DocCount
	}
	buckets := make([]facetBucket, 0, len(catalog))
	for _, cached := range catalog {
		bucket := cached
		bucket.Count = counts[bucket.Value]
		buckets = append(buckets, bucket)
	}
	return buckets
}

func pagesFor(total int) int {
	capped := min(total, search.MaxPage*search.PageSize)
	if capped == 0 {
		return 0
	}
	return (capped + search.PageSize - 1) / search.PageSize
}

func (s *Server) searchES(r *http.Request, dsl map[string]any) (*esSearchResponse, error) {
	body, err := json.Marshal(dsl)
	if err != nil {
		return nil, err
	}
	res, err := s.es.Search(
		s.es.Search.WithContext(r.Context()),
		s.es.Search.WithIndex(esindex.IndexName),
		s.es.Search.WithBody(bytes.NewReader(body)),
	)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.IsError() {
		return nil, errors.New(res.Status())
	}

	var esr esSearchResponse
	if err := json.NewDecoder(res.Body).Decode(&esr); err != nil {
		return nil, err
	}
	return &esr, nil
}

type esSuggestResponse struct {
	Took    int `json:"took"`
	Suggest struct {
		Card []struct {
			Options []struct {
				Text string `json:"text"`
			} `json:"options"`
		} `json:"card"`
	} `json:"suggest"`
}

func (s *Server) handleSuggest(w http.ResponseWriter, r *http.Request) {
	p := search.ParseParams(r.URL.Query())
	if p.Q == "" {
		writeJSON(w, http.StatusOK, map[string]any{"suggestions": []string{}})
		return
	}

	dsl := search.BuildSuggest(p.Q, false)
	entry := QueryLog{
		Time:     s.now().UTC().Format(time.RFC3339),
		Endpoint: "suggest",
		Params:   map[string]any{"q": p.Q},
		DSL:      dsl,
	}

	names, took, err := s.suggestES(r, dsl)
	if err == nil && len(names) == 0 {
		dsl = search.BuildSuggest(p.Q, true)
		entry.DSL = dsl
		var fuzzyTook int
		names, fuzzyTook, err = s.suggestES(r, dsl)
		took += fuzzyTook
	}
	if err != nil {
		entry.Status = http.StatusServiceUnavailable
		writeLog(s.logW, entry)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "elasticsearch unavailable"})
		return
	}

	entry.TookMs = took
	entry.Total = len(names)
	entry.Status = http.StatusOK
	writeLog(s.logW, entry)
	writeJSON(w, http.StatusOK, map[string]any{"suggestions": names})
}

func (s *Server) suggestES(r *http.Request, dsl map[string]any) ([]string, int, error) {
	body, err := json.Marshal(dsl)
	if err != nil {
		return nil, 0, err
	}
	res, err := s.es.Search(
		s.es.Search.WithContext(r.Context()),
		s.es.Search.WithIndex(esindex.IndexName),
		s.es.Search.WithBody(bytes.NewReader(body)),
	)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	if res.IsError() {
		return nil, 0, errors.New(res.Status())
	}

	var esr esSuggestResponse
	if err := json.NewDecoder(res.Body).Decode(&esr); err != nil {
		return nil, 0, err
	}
	names := []string{}
	for _, group := range esr.Suggest.Card {
		for _, opt := range group.Options {
			names = append(names, opt.Text)
		}
	}
	return names, esr.Took, nil
}
