package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

// A refused /api/v1/cards lookup writes one small query-log line whatever the
// ids value is made of: no more than 2 KB, valid JSON and UTF-8, no field a
// value could forge, no DSL, the status and message of the 400, and the value
// itself cut to its first 32 characters with the marker (or kept whole when
// it is that short).
func TestV1RefusedIDsLogOneSmallLine(t *testing.T) {
	ids101 := make([]string, 101)
	for i := range ids101 {
		ids101[i] = fmt.Sprintf("a%03d", i)
	}
	for _, tc := range []struct {
		name, raw string
		message   string
	}{
		{"missing", "", "ids is required"},
		{"only spaces and commas", " , ,\t", ""},
		{"101 distinct ids", strings.Join(ids101, ","), "ids takes at most 100 distinct card ids"},
		{"an over-long id", strings.Repeat("a", 129), ""},
		{"quotes", "base1-4," + strings.Repeat(`"`, 6000), ""},
		{"backslashes", "base1-4," + strings.Repeat(`\`, 6000), ""},
		{"a forged line", "base1-4,\n{\"forged\":1}\n" + strings.Repeat("x", 40), ""},
		{"invalid bytes", "base1-4," + strings.Repeat("\xff", 6000), ""},
		{"escaped markup", strings.Repeat("<", 19000), ""},
		{"20 KB of one valid id, then a bad one", strings.Repeat("base1-4,", 2500) + "?!", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			s, logBuf := newTestServer(t, anyRouteES(&calls))
			req := "/api/v1/cards"
			if tc.raw != "" {
				req += "?ids=" + queryEscapeAll(tc.raw)
			}
			rec := get(t, s, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400: %.200s", rec.Code, rec.Body.String())
			}
			if calls.Load() != 0 {
				t.Errorf("a refused lookup reached Elasticsearch %d times", calls.Load())
			}
			body := decodeError(t, rec)
			if body.Error.Code != "invalid_param" || body.Error.Field != "ids" ||
				(tc.message != "" && body.Error.Message != tc.message) {
				t.Errorf("400 body = %+v", body.Error)
			}
			line := rawQueryLine(t, logBuf)
			if len(line) > maxRefusedLogLine || !utf8.ValidString(line) || strings.Contains(line, "\n") {
				t.Fatalf("query-log line of %d bytes (valid UTF-8 %v): %.300s", len(line), utf8.ValidString(line), line)
			}
			var lg map[string]any
			if err := json.Unmarshal([]byte(line), &lg); err != nil {
				t.Fatal(err)
			}
			if _, forged := lg["forged"]; forged {
				t.Errorf("a value forged a field: %s", line)
			}
			if raw, ok := lg["dsl"]; !ok || raw != nil {
				t.Errorf("dsl = %v, want null", raw)
			}
			if lg["status"] != float64(400) || lg["error"] != body.Error.Message || lg["endpoint"] != "v1_cards" {
				t.Errorf("line endpoint %v status %v error %v", lg["endpoint"], lg["status"], lg["error"])
			}
			p, _ := lg["params"].(map[string]any)
			got, _ := p["ids"].(string)
			// JSON writes each invalid byte as its own U+FFFD.
			want := string([]rune(tc.raw))
			if utf8.RuneCountInString(tc.raw) > 32 {
				kept, cut := strings.CutSuffix(got, cutLogMarker)
				if !cut || utf8.RuneCountInString(kept) != 32 || !strings.HasPrefix(want, kept) {
					t.Errorf("logged ids = %q, want the first 32 characters and the marker", got)
				}
			} else if got != want {
				t.Errorf("logged ids = %q, want %q as given", got, want)
			}
		})
	}
}

// An accepted lookup's line is not cut: the ids value is logged whole, as it
// always was, even when it is far longer than a refused line keeps.
func TestV1AcceptedIDsLogTheWholeValue(t *testing.T) {
	s, logBuf := newTestServer(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return esResponse(200, `{"docs":[]}`), nil
	}))
	raw := strings.Repeat("base1-4,", 40) + "base1-2"
	rec := get(t, s, "/api/v1/cards?ids="+raw)
	if rec.Code == http.StatusBadRequest {
		t.Fatalf("status %d: %.200s", rec.Code, rec.Body.String())
	}
	p, _ := queryLine(t, logBuf)["params"].(map[string]any)
	if p["ids"] != raw {
		t.Errorf("logged ids = %.80v, want the whole %d-character value", p["ids"], len(raw))
	}
}

// queryEscapeAll percent-encodes every byte, so any value, invalid UTF-8
// included, reaches the handler exactly as given.
func queryEscapeAll(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := range len(s) {
		b.WriteByte('%')
		b.WriteByte(hex[s[i]>>4])
		b.WriteByte(hex[s[i]&15])
	}
	return b.String()
}
