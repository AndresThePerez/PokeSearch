package server

import (
	"encoding/json"
	"io"

	"github.com/AndresThePerez/pokesearch/internal/search"
)

// QueryLog is one stdout line per search/suggest request that reaches ES.
type QueryLog struct {
	Time     string         `json:"time"`
	Endpoint string         `json:"endpoint"`
	Params   map[string]any `json:"params"`
	DSL      map[string]any `json:"dsl"`
	TookMs   int            `json:"took_ms"`
	Total    int            `json:"total"`
	Status   int            `json:"status"`
}

func writeLog(w io.Writer, entry QueryLog) {
	if w == nil {
		return
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return
	}
	_, _ = w.Write(append(b, '\n'))
}

// logParams renders only the set fields of Params using canonical values.
func logParams(p search.Params) map[string]any {
	m := map[string]any{"sort": p.Sort}
	if p.Q != "" {
		m["q"] = p.Q
	}
	if p.ID != "" {
		m["id"] = p.ID
	}
	if p.Supertype != "" {
		m["supertype"] = p.Supertype
	}
	if len(p.Types) > 0 {
		m["types"] = p.Types
	}
	if len(p.Rarity) > 0 {
		m["rarity"] = p.Rarity
	}
	if len(p.Series) > 0 {
		m["series"] = p.Series
	}
	if p.HPMin != nil {
		m["hp_min"] = *p.HPMin
	}
	if p.HPMax != nil {
		m["hp_max"] = *p.HPMax
	}
	if p.Order != "" {
		m["order"] = p.Order
	}
	if p.Page > 1 {
		m["page"] = p.Page
	}
	return m
}
