package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stormHandler is a handler that behaves in one of the ways a real handler can
// end: it writes an answer, returns without writing, panics with a value,
// aborts with http.ErrAbortHandler, or panics after its status line went out.
// It counts how many requests are inside it at once.
type stormHandler struct {
	inside, most atomic.Int64
}

func (h *stormHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := h.inside.Add(1)
	defer h.inside.Add(-1)
	for {
		m := h.most.Load()
		if n <= m || h.most.CompareAndSwap(m, n) {
			break
		}
	}
	runtime.Gosched()
	switch r.URL.Query().Get("end") {
	case "write":
		_, _ = io.WriteString(w, "ok")
	case "silent":
	case "panic":
		panic("handler failed")
	case "abort":
		panic(http.ErrAbortHandler)
	case "late-panic":
		w.WriteHeader(http.StatusAccepted)
		panic("handler failed after its status line")
	}
}

var stormEnds = []string{"write", "silent", "panic", "abort", "late-panic"}

// serveSurvivingPanic runs one request through h the way net/http would,
// recovering the panic net/http itself would recover, and reports the status.
func serveSurvivingPanic(h http.Handler, r *http.Request) (status int) {
	rec := httptest.NewRecorder()
	defer func() {
		if recover() != nil {
			status = -1
		}
	}()
	h.ServeHTTP(rec, r)
	return rec.Code
}

// stormRequest is request i of a storm: a varying way to end, and every
// seventh one arriving with its context already cancelled.
func stormRequest(i int) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/search?end="+stormEnds[i%len(stormEnds)], nil)
	if i%7 == 0 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r = r.WithContext(ctx)
	}
	return r
}

// A storm of requests that end every way a handler can end never has more
// requests inside the handlers than the cap, and leaves no slot taken.
func TestInFlightCapHoldsUnderAStormOfEveryEnding(t *testing.T) {
	s := newTestServerLogging(t, noESCalls(t), io.Discard)
	const capacity = 4
	if err := s.SetMaxInFlight(capacity); err != nil {
		t.Fatal(err)
	}
	pool := s.inFlight.Load()
	inner := &stormHandler{}
	h := s.withInFlightCap(inner)

	var admitted, refused atomic.Int64
	var wg sync.WaitGroup
	for g := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 300 {
				if serveSurvivingPanic(h, stormRequest(g*300+i)) == http.StatusServiceUnavailable {
					refused.Add(1)
				} else {
					admitted.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	if got := inner.most.Load(); got > capacity {
		t.Errorf("%d requests were inside the handlers at once under a cap of %d", got, capacity)
	}
	if admitted.Load()+refused.Load() != 32*300 || admitted.Load() == 0 {
		t.Errorf("admitted %d, refused %d of %d", admitted.Load(), refused.Load(), 32*300)
	}
	if n := len(pool.slots); n != 0 {
		t.Errorf("%d slots still taken after every request returned", n)
	}
}

// Resizing the cap while requests come and go gives every slot back to the
// pool it was taken from: once the storm is over no pool has a slot taken,
// and the cap in force admits exactly its size again.
func TestInFlightCapResizedDuringAStormKeepsEverySlot(t *testing.T) {
	s := newTestServerLogging(t, noESCalls(t), io.Discard)
	h := s.withInFlightCap(&stormHandler{})

	stop := make(chan struct{})
	pools := []*inFlightSlots{s.inFlight.Load()}
	resized := make(chan struct{})
	go func() {
		defer close(resized)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := s.SetMaxInFlight([]int{1, 2, 3, 5, 8}[i%5]); err != nil {
				t.Error(err)
				return
			}
			pools = append(pools, s.inFlight.Load())
			runtime.Gosched()
		}
	}()
	var wg sync.WaitGroup
	for g := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 300 {
				serveSurvivingPanic(h, stormRequest(g*300+i))
			}
		}()
	}
	wg.Wait()
	close(stop)
	<-resized

	for i, p := range pools {
		if n := len(p.slots); n != 0 {
			t.Fatalf("pool %d of %d (size %d) keeps %d slots taken after the storm", i, len(pools), cap(p.slots), n)
		}
	}

	const final = 3
	if err := s.SetMaxInFlight(final); err != nil {
		t.Fatal(err)
	}
	assertAdmitsExactly(t, s, final)
}

// assertAdmitsExactly holds n requests inside a blocking handler behind the
// server's cap and checks that one more is refused, then that n are admitted
// again once they have all returned.
func assertAdmitsExactly(t *testing.T, s *Server, n int) {
	t.Helper()
	entered := make(chan struct{}, n+1)
	release := make(chan struct{})
	h := s.withInFlightCap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		entered <- struct{}{}
		<-release
	}))
	for round := range 2 {
		var wg sync.WaitGroup
		for range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				serveSurvivingPanic(h, httptest.NewRequest(http.MethodGet, "/api/search", nil))
			}()
		}
		for i := range n {
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatalf("round %d: only %d of %d requests were admitted under a cap of %d", round, i, n, n)
			}
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/search", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("round %d: request %d answered %d under a cap of %d, want 503", round, n+1, rec.Code, n)
		}
		for range n {
			release <- struct{}{}
		}
		wg.Wait()
	}
}

// A thousand requests one after another under a cap of one are all admitted,
// and while each is inside, a second one is refused: no slot is lost and none
// is gained, whatever way the first one ends.
func TestInFlightCapOfOneOverAThousandCycles(t *testing.T) {
	s := newTestServerLogging(t, noESCalls(t), io.Discard)
	if err := s.SetMaxInFlight(1); err != nil {
		t.Fatal(err)
	}
	var h http.Handler
	var nested int
	h = s.withInFlightCap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/search", nil))
		nested = rec.Code
		(&stormHandler{}).ServeHTTP(w, r)
	}))
	for i := range 1000 {
		nested = 0
		status := serveSurvivingPanic(h, stormRequest(i))
		if status == http.StatusServiceUnavailable {
			t.Fatalf("cycle %d: refused with nothing else in flight: a slot was lost", i)
		}
		if nested != http.StatusServiceUnavailable {
			t.Fatalf("cycle %d: a second request inside a cap of one answered %d: a slot was gained", i, nested)
		}
	}
	if n := len(s.inFlight.Load().slots); n != 0 {
		t.Errorf("%d slots taken after a thousand cycles", n)
	}
}

// probeRequest sends one raw request line on its own connection and reads the
// answer, so request targets a client library would rewrite reach the server
// exactly as written.
func probeRequest(t *testing.T, addr, method, target, host string) *http.Response {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, method+" "+target+" HTTP/1.1\r\nHost: "+host+"\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	res, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: method})
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(body))
	return res
}

// The probes are exempted by path. With the one slot of a cap of one taken,
// every spelling of a request target that might be read as a probe by the cap
// but routed elsewhere by the mux, or the reverse, either reaches a probe or
// is refused: nothing that is not a probe is served, and only /healthz calls
// Elasticsearch (its one _count).
func TestInFlightCapExemptsOnlyTheProbesWhateverTheSpelling(t *testing.T) {
	es := newHeldES()
	_, ts, _ := capServer(t, es, 1)
	wait := holdN(t, es, ts.Client(), ts.URL, 1)
	defer func() {
		es.release <- struct{}{}
		wait()
	}()
	addr := strings.TrimPrefix(ts.URL, "http://")

	for _, tc := range []struct {
		method, target, host string
		want                 int
		countCall            bool
	}{
		// The probes, however they are asked for.
		{"GET", "/livez", "x", 200, false},
		{"HEAD", "/livez", "x", 200, false},
		{"GET", "/healthz", "x", 200, true},
		{"HEAD", "/healthz", "x", 200, true},
		{"GET", "/livez?x=1", "x", 200, false},
		{"GET", "/healthz?" + strings.Repeat("q", 4000), "x", 200, true},
		{"GET", "http://example.com/livez", "example.com", 200, false},
		{"GET", "/livez", "example.com:80", 200, false},
		{"GET", "/liv%65z", "x", 200, false},
		{"GET", "/%68ealthz", "x", 200, true},
		// Not the probes: refused, never redirected or served by the mux.
		{"GET", "//healthz", "x", 503, false},
		{"GET", "/healthz/", "x", 503, false},
		{"GET", "/healthz/x", "x", 503, false},
		{"GET", "/livez/../api/search", "x", 503, false},
		{"GET", "/./livez", "x", 503, false},
		{"GET", "/LIVEZ", "x", 503, false},
		{"GET", "/Healthz", "x", 503, false},
		{"GET", "/livez#x", "x", 503, false},
		{"GET", "/healthz%2f..%2fapi/search", "x", 503, false},
		{"GET", "/api/search?path=/livez", "x", 503, false},
		{"GET", "http://example.com/api/search?q=/livez", "example.com", 503, false},
		// A probe path asked with a method it does not answer gets the mux's
		// own 405, which does no work and calls nothing.
		{"POST", "/livez", "x", 405, false},
		{"DELETE", "/healthz", "x", 405, false},
		{"CONNECT", "/livez", "x", 405, false},
		// net/http answers this itself; no handler of the server runs.
		{"OPTIONS", "*", "x", 200, false},
	} {
		before := es.calls.Load()
		res := probeRequest(t, addr, tc.method, tc.target, tc.host)
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != tc.want {
			t.Errorf("%s %.60s: status %d %.120q, want %d", tc.method, tc.target, res.StatusCode, body, tc.want)
			continue
		}
		if tc.want == 503 && !overloaded(res, body) && tc.method != "HEAD" {
			t.Errorf("%s %.60s: a 503 that is not the cap's refusal: %.120q", tc.method, tc.target, body)
		}
		wantCalls := int32(0)
		if tc.countCall {
			wantCalls = 1
		}
		if got := es.calls.Load() - before; got != wantCalls {
			t.Errorf("%s %.60s: %d Elasticsearch calls while the cap was full, want %d", tc.method, tc.target, got, wantCalls)
		}
	}
}

// errorEnum reads the closed list of error codes from the published schema.
func errorEnum(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("../../docs/api/schema/error.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties struct {
			Error struct {
				Properties struct {
					Code struct {
						Enum []string `json:"enum"`
					} `json:"code"`
				} `json:"properties"`
			} `json:"error"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	return schema.Properties.Error.Properties.Code.Enum
}

// assertSchemaShaped checks a refusal body the way error.json does: exactly
// the keys it allows, all it requires, and a code from its enum.
func assertSchemaShaped(t *testing.T, label string, body []byte, enum []string) {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatalf("%s: body %q is not JSON: %v", label, body, err)
	}
	if len(top) != 2 || top["error"] == nil || top["request_id"] == nil {
		t.Errorf("%s: top-level keys of %s, want exactly error and request_id", label, body)
	}
	var detail map[string]any
	if err := json.Unmarshal(top["error"], &detail); err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	code, _ := detail["code"].(string)
	msg, _ := detail["message"].(string)
	if len(detail) != 2 || msg == "" || !slices.Contains(enum, code) || code != "overloaded" {
		t.Errorf("%s: error %v, want only code overloaded (in %v) and a message", label, detail, enum)
	}
}

// The refusal is the same answer whatever the method, the path, the API key
// and the encoding offered: 503, Retry-After 1, the connection closed, never
// compressed, the envelope under /api/ and plain text elsewhere, nothing in
// it for a HEAD, and not a byte different between a good key and a bad one.
func TestInFlightRefusalIsOneAnswerWhateverIsAsked(t *testing.T) {
	good := throwawayKey(t)
	t.Setenv("POKESEARCH_API_KEYS", good)
	es := newHeldES()
	_, ts, logs := capServer(t, es, 1)
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	t.Cleanup(client.CloseIdleConnections)
	wait := holdN(t, es, ts.Client(), ts.URL, 1)
	defer func() {
		es.release <- struct{}{}
		wait()
	}()
	enum := errorEnum(t)
	longQuery := strings.Repeat("<", 20000)

	ask := func(method, path string, headers http.Header) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, ts.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header = headers
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %.60s: %v", method, path, err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res, body
	}

	type answer struct {
		header http.Header
		body   []byte
	}
	byKey := map[string]answer{}
	for i, tc := range []struct {
		method, path, key string
		api, gzip         bool
	}{
		{"GET", "/api/search?q=pikachu", "", true, false},
		{"GET", "/api/search?q=pikachu", "", true, true},
		{"HEAD", "/api/search?q=pikachu", "", true, true},
		{"DELETE", "/api/search", "", true, false},
		{"GET", "/api/", "", true, false},
		{"GET", "/api/nothing-here", "", true, false},
		{"GET", "/api/v1/cards/", "", true, false},
		{"GET", "/api/v1/cards?ids=base1-4", "none", true, false},
		{"GET", "/api/v1/cards?ids=base1-4", "good", true, false},
		{"GET", "/api/v1/cards?ids=base1-4", "bad", true, false},
		{"GET", "/api/v1/cards?ids=base1-4", "two", true, false},
		{"GET", "/api/search?q=" + url.QueryEscape(longQuery), "", true, true},
		{"GET", "/api", "", false, false},
		{"GET", "/", "", false, true},
		{"HEAD", "/", "", false, false},
		{"POST", "/", "", false, false},
		{"GET", "/styles.css", "", false, true},
	} {
		label := tc.method + " " + tc.path
		if len(label) > 80 {
			label = label[:80]
		}
		id := "refusal-" + string(rune('a'+i))
		h := http.Header{"X-Request-Id": {id}}
		if tc.gzip {
			h.Set("Accept-Encoding", "gzip")
		}
		switch tc.key {
		case "good":
			h.Set(apiKeyHeader, good)
		case "bad":
			h.Set(apiKeyHeader, "bad-key-0000")
		case "two":
			h[apiKeyHeader] = []string{good, good}
		}
		res, body := ask(tc.method, tc.path, h)

		if res.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s: status %d, want 503", label, res.StatusCode)
			continue
		}
		if got := res.Header.Get("Retry-After"); got != "1" {
			t.Errorf("%s: Retry-After %q, want 1", label, got)
		}
		if !res.Close {
			t.Errorf("%s: the refusal kept the connection open", label)
		}
		if enc := res.Header.Get("Content-Encoding"); enc != "" {
			t.Errorf("%s: the refusal was sent with Content-Encoding %q", label, enc)
		}
		if v := res.Header.Get("Vary"); v != "" {
			t.Errorf("%s: the refusal varies on %q", label, v)
		}
		if res.Header.Get("WWW-Authenticate") != "" {
			t.Errorf("%s: the refusal carries a key challenge", label)
		}
		if got := res.Header.Get("X-Request-Id"); got != id {
			t.Errorf("%s: X-Request-Id %q, want %q", label, got, id)
		}
		if tc.method == http.MethodHead {
			if len(body) != 0 {
				t.Errorf("%s: a HEAD refusal has a body of %d bytes", label, len(body))
			}
			continue
		}
		if res.ContentLength != int64(len(body)) {
			t.Errorf("%s: Content-Length %d for a body of %d bytes", label, res.ContentLength, len(body))
		}
		ct := res.Header.Get("Content-Type")
		if tc.api {
			if ct != "application/json" {
				t.Errorf("%s: Content-Type %q, want application/json", label, ct)
			}
			assertSchemaShaped(t, label, body, enum)
			if !bytes.Contains(body, []byte(`"request_id":"`+id+`"`)) {
				t.Errorf("%s: body %s does not carry request_id %s", label, body, id)
			}
		} else if !strings.HasPrefix(ct, "text/plain") || !strings.Contains(string(body), "capacity") {
			t.Errorf("%s: Content-Type %q body %q, want plain text about capacity", label, ct, body)
		}
		if tc.key != "" {
			h := res.Header.Clone()
			h.Del("Date")
			h.Del("X-Request-Id")
			byKey[tc.key] = answer{h, bytes.ReplaceAll(body, []byte(id), []byte("ID"))}
		}
	}

	none := byKey["none"]
	for _, k := range []string{"good", "bad", "two"} {
		a := byKey[k]
		if !bytes.Equal(a.body, none.body) {
			t.Errorf("refusal with key %s: body %s, without a key %s", k, a.body, none.body)
		}
		for name := range a.header {
			if a.header.Get(name) != none.header.Get(name) {
				t.Errorf("refusal with key %s: header %s = %q, without a key %q", k, name, a.header.Get(name), none.header.Get(name))
			}
		}
		if len(a.header) != len(none.header) {
			t.Errorf("refusal with key %s has %d headers, without a key %d", k, len(a.header), len(none.header))
		}
	}

	// The 20 KB query is refused with one access line, which names the path
	// and never repeats the query, and no query-log line.
	lines := logs.linesFor(t, "refusal-l")
	if len(lines) != 1 || lines[0]["msg"] != "access" || lines[0]["status"] != float64(503) {
		t.Fatalf("lines for the refused 20 KB query: %v, want one access line with status 503", lines)
	}
	raw, _ := json.Marshal(lines[0])
	if bytes.Contains(raw, []byte("<<<<")) || len(raw) > 1024 {
		t.Errorf("the refused 20 KB query left an access line of %d bytes: %.200s", len(raw), raw)
	}
}

// A refused single-card id is logged cut however hostile its characters are:
// quotes, backslashes, control characters and invalid UTF-8 cost several bytes
// each once escaped, and must not make the line long. The answer is the
// ordinary invalid_param 400.
func TestRefusedV1CardHostileIDsLogCut(t *testing.T) {
	for name, id := range map[string]string{
		"quotes":       strings.Repeat(`"`, 20000),
		"backslashes":  strings.Repeat(`\`, 20000),
		"newlines":     strings.Repeat("\n", 20000),
		"control":      strings.Repeat("\x01\x7f", 10000),
		"invalid utf8": strings.Repeat("\xff\xfe", 10000),
		"mixed":        strings.Repeat("a\"\\\n\xff<é", 3000),
		"line split":   "base1-4\n{\"level\":\"ERROR\",\"msg\":\"forged\"}",
	} {
		t.Run(name, func(t *testing.T) {
			s, logBuf := newTestServer(t, neverCalled(t))
			rec := get(t, s, "/api/v1/cards/"+url.PathEscape(id))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400: %.300s", rec.Code, rec.Body.String())
			}
			if body := decodeError(t, rec); body.Error.Code != "invalid_param" || body.Error.Field != "id" {
				t.Errorf("400 body = %+v, want invalid_param on id", body.Error)
			}
			if line := rawQueryLine(t, logBuf); len(line) > maxRefusedLogLine {
				t.Errorf("query-log line is %d bytes for an id of %d bytes, want at most %d", len(line), len(id), maxRefusedLogLine)
			}
			for _, line := range strings.Split(strings.TrimSpace(logBuf.String()), "\n") {
				if strings.Contains(line, "forged") && !strings.Contains(line, `\"msg\":\"forged\"`) {
					t.Errorf("an id split the log into a forged line: %s", line)
				}
			}
			if n := strings.Count(strings.TrimSpace(logBuf.String()), "\n") + 1; n != 2 {
				t.Errorf("%d log lines, want the query line and the access line", n)
			}
		})
	}
}

// A slow upstream answer whose client has already gone keeps no slot once the
// handler returns: the cap counts handlers, not connections.
func TestInFlightCapFreesSlotsOfClientsThatLeftBeforeTheAnswer(t *testing.T) {
	es := newHeldES()
	_, ts, logs := capServer(t, es, 3)
	// One after another, so a retried upstream call of one request is never
	// taken for the arrival of the next. Each handler returns once its
	// upstream call sees the cancelled context, and its access line is
	// written after that.
	for _, id := range []string{"left-a", "left-b", "left-c"} {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _, err := fetch(t, ctx, ts.Client(), ts.URL+"/api/v1/cards/base1-4", map[string]string{"X-Request-Id": id})
			if err == nil || !errors.Is(err, context.Canceled) {
				t.Errorf("a cancelled request ended with %v", err)
			}
		}()
		es.waitEntered(t, 1)
		cancel()
		<-done
		deadline := time.Now().Add(5 * time.Second)
		for len(logs.linesFor(t, id)) == 0 {
			if time.Now().After(deadline) {
				t.Fatalf("the request %s of a client that left never returned", id)
			}
			time.Sleep(10 * time.Millisecond)
		}
		for len(es.entered) > 0 {
			<-es.entered
		}
	}
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 8}}
	t.Cleanup(client.CloseIdleConnections)
	wait := holdN(t, es, client, ts.URL, 3)
	res, body, err := fetch(t, context.Background(), client, ts.URL+"/api/v1/cards/base1-4", nil)
	if err != nil || !overloaded(res, body) {
		t.Errorf("a fourth request under a cap of three was not refused (err %v)", err)
	}
	for range 3 {
		es.release <- struct{}{}
	}
	wait()
}
