package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/AndresThePerez/pokesearch/internal/search"
)

// logTimeFormat is RFC3339 with milliseconds. Whole seconds cannot order two
// requests that landed in the same second, which is exactly the case where a
// query line has to be matched against its access line.
const logTimeFormat = "2006-01-02T15:04:05.000Z"

// logHandlerOptions makes slog's own timestamps match the QueryLog's: UTC at
// millisecond resolution. Two lines describing one request have to be
// directly comparable, and slog's default (local time, nanoseconds) is not.
func logHandlerOptions() *slog.HandlerOptions {
	return &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey && a.Value.Kind() == slog.KindTime {
				a.Value = slog.StringValue(a.Value.Time().UTC().Format(logTimeFormat))
			}
			return a
		},
	}
}

// QueryLog is one stdout line per search/suggest request that reaches ES.
type QueryLog struct {
	Time string `json:"time"`
	// RequestID ties this line to the request's access line and to the
	// X-Request-Id the client was handed.
	RequestID string         `json:"request_id"`
	Endpoint  string         `json:"endpoint"`
	Params    map[string]any `json:"params"`
	DSL       map[string]any `json:"dsl"`
	TookMs    int            `json:"took_ms"`
	Total     int            `json:"total"`
	Status    int            `json:"status"`
	// Error carries the server-side cause of a non-2xx — for a 503 that is the
	// truncated Elasticsearch error body, which is what makes a mapping error
	// distinguishable from a down cluster. Never sent to clients.
	Error string `json:"error,omitempty"`
}

// queryLog seeds the entry with what both endpoints share. The rest is filled
// in as the request progresses and written once, at the end.
func (s *Server) queryLog(r *http.Request, endpoint string) QueryLog {
	return QueryLog{
		Time:      s.now().UTC().Format(logTimeFormat),
		RequestID: requestID(r),
		Endpoint:  endpoint,
	}
}

// writeLog emits the replayable query line. A failure here is reported through
// slog rather than swallowed — a silently missing DSL line would undermine the
// one piece of observability this project leans on hardest.
func (s *Server) writeLog(entry QueryLog) {
	b, err := json.Marshal(entry)
	if err != nil {
		s.log.Error("querylog marshal failed", "endpoint", entry.Endpoint,
			"request_id", entry.RequestID, "err", err.Error())
		return
	}
	if _, err := s.logW.Write(append(b, '\n')); err != nil {
		s.log.Error("querylog write failed", "endpoint", entry.Endpoint,
			"request_id", entry.RequestID, "err", err.Error())
	}
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
	if p.SetID != "" {
		m["set"] = p.SetID
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
	if p.PageSize != search.PageSize {
		m["page_size"] = p.PageSize
	}
	return m
}
