package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// heldES is a fake Elasticsearch whose _mget calls wait until the test lets
// them answer, so a /api/v1/cards/{id} request can be held inside the server
// for as long as a test needs. Every other call answers at once.
type heldES struct {
	entered chan struct{} // one value per _mget that arrived
	release chan struct{} // one value lets one waiting _mget answer
	calls   atomic.Int32
}

func newHeldES() *heldES {
	return &heldES{entered: make(chan struct{}, 1024), release: make(chan struct{}, 1024)}
}

func (h *heldES) RoundTrip(r *http.Request) (*http.Response, error) {
	h.calls.Add(1)
	switch {
	case strings.Contains(r.URL.Path, "_mget"):
		h.entered <- struct{}{}
		select {
		case <-h.release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return esResponse(200, `{"docs":[]}`), nil
	case strings.Contains(r.URL.Path, "_count"):
		return esResponse(200, `{"count":7}`), nil
	default:
		return esResponse(404, `{}`), nil
	}
}

// waitEntered waits for n held requests to reach Elasticsearch.
func (h *heldES) waitEntered(t *testing.T, n int) {
	t.Helper()
	for i := range n {
		select {
		case <-h.entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d requests reached Elasticsearch", i, n)
		}
	}
}

// lockedBuffer is a log sink the test may read while the server still runs.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// linesFor returns every log line that names the request id.
func (b *lockedBuffer) linesFor(t *testing.T, id string) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("undecodable log line %q: %v", line, err)
		}
		if m["request_id"] == id {
			out = append(out, m)
		}
	}
	return out
}

// capServer is a server capped at n requests in flight behind a real
// net/http listener, so disconnects and panics behave as in production.
func capServer(t *testing.T, es http.RoundTripper, n int) (*Server, *httptest.Server, *lockedBuffer) {
	t.Helper()
	logs := &lockedBuffer{}
	s := newTestServerLogging(t, es, logs)
	if n > 0 {
		if err := s.SetMaxInFlight(n); err != nil {
			t.Fatal(err)
		}
	}
	ts := httptest.NewUnstartedServer(s)
	// net/http reports a recovered panic on this logger; the panic test
	// provokes one on purpose.
	ts.Config.ErrorLog = log.New(io.Discard, "", 0)
	ts.Start()
	t.Cleanup(ts.Close)
	return s, ts, logs
}

// fetch sends one GET with optional headers and returns the answer with its
// body read.
func fetch(t *testing.T, ctx context.Context, client *http.Client, url string, headers map[string]string) (*http.Response, []byte, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	return res, body, err
}

// overloaded reports whether an answer is the cap's refusal: a 503 that says
// overloaded, in the envelope or in plain text, and not any other 503.
func overloaded(res *http.Response, body []byte) bool {
	return res != nil && res.StatusCode == http.StatusServiceUnavailable &&
		(bytes.Contains(body, []byte(`"code":"overloaded"`)) || bytes.Contains(body, []byte("capacity")))
}

// holdN starts n card requests and waits until all of them are held inside
// the server. The returned wait blocks until every one of them has returned.
func holdN(t *testing.T, es *heldES, client *http.Client, base string, n int) (wait func()) {
	t.Helper()
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, body, err := fetch(t, context.Background(), client, base+"/api/v1/cards/base1-4", nil)
			if err == nil && overloaded(res, body) {
				t.Errorf("a request that should have been admitted was refused")
			}
		}()
	}
	es.waitEntered(t, n)
	return wg.Wait
}

// assertRefused checks the answer the cap gives on a full server.
func assertRefused(t *testing.T, res *http.Response, body []byte, elapsed time.Duration, wantJSON bool, id string) {
	t.Helper()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503: %s", res.StatusCode, body)
	}
	if elapsed > time.Second {
		t.Errorf("the refusal took %s, want it at once", elapsed)
	}
	if got := res.Header.Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want 1", got)
	}
	if got := res.Header.Get("X-Request-Id"); got != id {
		t.Errorf("X-Request-Id = %q, want %q", got, id)
	}
	for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if res.Header.Get(h) == "" {
			t.Errorf("the refusal lacks the security header %s", h)
		}
	}
	ct := res.Header.Get("Content-Type")
	if !wantJSON {
		if !strings.HasPrefix(ct, "text/plain") || !strings.Contains(string(body), "capacity") {
			t.Errorf("page refusal: Content-Type %q body %q, want plain text about capacity", ct, body)
		}
		return
	}
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Field   string `json:"field"`
			Message string `json:"message"`
		} `json:"error"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("refusal body %q: %v", body, err)
	}
	if env.Error.Code != "overloaded" || env.Error.Field != "" || !strings.Contains(env.Error.Message, "capacity") {
		t.Errorf("refusal error = %+v, want code overloaded, no field, a message about capacity", env.Error)
	}
	if env.RequestID != id {
		t.Errorf("request_id = %q, want %q", env.RequestID, id)
	}
}

// assertOneAccessLine checks a refused request left exactly one log line: its
// access line, with status 503, and no query-log line.
func assertOneAccessLine(t *testing.T, logs *lockedBuffer, id string) {
	t.Helper()
	lines := logs.linesFor(t, id)
	if len(lines) != 1 {
		t.Fatalf("%d log lines for the refused request %s, want exactly its access line: %v", len(lines), id, lines)
	}
	if lines[0]["msg"] != "access" || lines[0]["status"] != float64(503) {
		t.Errorf("refused request's line = %v, want an access line with status 503", lines[0])
	}
	if _, ok := lines[0]["endpoint"]; ok {
		t.Errorf("a refused request wrote a query-log line: %v", lines[0])
	}
}

func TestInFlightCapRefusesAtOnceWhenFull(t *testing.T) {
	es := newHeldES()
	_, ts, logs := capServer(t, es, 2)
	client := ts.Client()
	wait := holdN(t, es, client, ts.URL, 2)
	callsWhileFull := es.calls.Load()

	for _, tc := range []struct {
		path     string
		wantJSON bool
		id       string
	}{
		{"/api/v1/cards/base1-4", true, "refused-api-1"},
		{"/api/search?q=pikachu", true, "refused-api-2"},
		{"/", false, "refused-page-1"},
		{"/styles.css", false, "refused-page-2"},
	} {
		start := time.Now()
		res, body, err := fetch(t, context.Background(), client, ts.URL+tc.path, map[string]string{"X-Request-Id": tc.id})
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		assertRefused(t, res, body, time.Since(start), tc.wantJSON, tc.id)
	}
	if got := es.calls.Load(); got != callsWhileFull {
		t.Errorf("refused requests made %d Elasticsearch calls, want none", got-callsWhileFull)
	}

	es.release <- struct{}{}
	es.release <- struct{}{}
	wait()
	for _, id := range []string{"refused-api-1", "refused-api-2", "refused-page-1", "refused-page-2"} {
		assertOneAccessLine(t, logs, id)
	}
}

// Releasing one held request lets exactly one more in, and once every request
// has returned the full number is admitted again: no slot is kept.
func TestInFlightCapFreesSlotsAsRequestsReturn(t *testing.T) {
	es := newHeldES()
	_, ts, _ := capServer(t, es, 2)
	client := ts.Client()
	refused := func() bool {
		t.Helper()
		res, body, err := fetch(t, context.Background(), client, ts.URL+"/api/v1/cards/base1-4", nil)
		if err != nil {
			t.Fatal(err)
		}
		return overloaded(res, body)
	}

	wait := holdN(t, es, client, ts.URL, 2)
	if !refused() {
		t.Fatal("a third request was admitted under a cap of two")
	}
	es.release <- struct{}{}
	// The released request returns on its own; its slot must come back.
	deadline := time.Now().Add(5 * time.Second)
	var wait2 func()
	for {
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _, _ = fetch(t, context.Background(), client, ts.URL+"/api/v1/cards/base1-4", nil)
		}()
		select {
		case <-es.entered:
			wait2 = func() { <-done }
		case <-done:
		}
		if wait2 != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the released slot never came back")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !refused() {
		t.Fatal("a request was admitted beyond the cap after one slot was reused")
	}
	es.release <- struct{}{}
	es.release <- struct{}{}
	wait()
	wait2()

	for round := range 3 {
		wait := holdN(t, es, client, ts.URL, 2)
		if !refused() {
			t.Fatalf("round %d: a request was admitted beyond the cap", round)
		}
		es.release <- struct{}{}
		es.release <- struct{}{}
		wait()
	}
}

// A handler that panics gives its slot back, and net/http still aborts the
// connection as it always did.
func TestInFlightCapReleasesASlotOnPanic(t *testing.T) {
	es := newHeldES()
	s, ts, _ := capServer(t, es, 1)
	s.mux.HandleFunc("GET /api/boom", func(http.ResponseWriter, *http.Request) { panic("boom") })
	client := ts.Client()

	for range 3 {
		if res, body, err := fetch(t, context.Background(), client, ts.URL+"/api/boom", nil); err == nil {
			t.Fatalf("a panicking handler answered %d %s; want the connection aborted", res.StatusCode, body)
		}
	}
	wait := holdN(t, es, client, ts.URL, 1)
	res, body, err := fetch(t, context.Background(), client, ts.URL+"/api/v1/cards/base1-4", nil)
	if err != nil || !overloaded(res, body) {
		t.Errorf("with the one slot held, a second request was not refused (err %v)", err)
	}
	es.release <- struct{}{}
	wait()
}

// A client that goes away while its request is held frees the slot once the
// handler returns, which it does because the request's context is cancelled.
func TestInFlightCapReleasesASlotWhenTheClientLeaves(t *testing.T) {
	es := newHeldES()
	_, ts, logs := capServer(t, es, 1)
	client := ts.Client()

	ctx, cancel := context.WithCancel(context.Background())
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		_, _, _ = fetch(t, ctx, client, ts.URL+"/api/v1/cards/base1-4", map[string]string{"X-Request-Id": "client-left-1"})
	}()
	es.waitEntered(t, 1)
	cancel()
	<-gone

	// The access line is written after the handler, and so the cap, has
	// returned: once it is there the slot has been given back.
	deadline := time.Now().Add(5 * time.Second)
	for len(logs.linesFor(t, "client-left-1")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the request of a client that left never returned")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Forget any retry of the abandoned upstream call.
	for len(es.entered) > 0 {
		<-es.entered
	}

	wait := holdN(t, es, client, ts.URL, 1)
	res, body, err := fetch(t, context.Background(), client, ts.URL+"/api/v1/cards/base1-4", nil)
	if err != nil || !overloaded(res, body) {
		t.Errorf("with the returned slot taken again, another request was not refused (err %v)", err)
	}
	es.release <- struct{}{}
	wait()
}

// The health probes are never refused: the container healthcheck (the
// binary's -ping asks /livez) and an uptime monitor must stay green while the
// cap is full. The metrics document is not a probe and is capped.
func TestInFlightCapExemptsTheHealthProbes(t *testing.T) {
	es := newHeldES()
	s, ts, _ := capServer(t, es, 1)
	s.EnableMetrics()
	client := ts.Client()
	wait := holdN(t, es, client, ts.URL, 1)

	for _, path := range []string{"/livez", "/healthz"} {
		for range 3 {
			res, body, err := fetch(t, context.Background(), client, ts.URL+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			if res.StatusCode != http.StatusOK {
				t.Errorf("%s answered %d %s while the cap was full, want 200", path, res.StatusCode, body)
			}
		}
	}
	req, _ := http.NewRequest(http.MethodHead, ts.URL+"/livez", nil)
	if res, err := client.Do(req); err != nil || res.StatusCode != http.StatusOK {
		t.Errorf("HEAD /livez while the cap was full: %v %v", res, err)
	} else {
		res.Body.Close()
	}
	for _, path := range []string{"/debug/vars", "/api/meta"} {
		res, body, err := fetch(t, context.Background(), client, ts.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !overloaded(res, body) {
			t.Errorf("%s answered %d %s while the cap was full, want the overloaded 503", path, res.StatusCode, body)
		}
	}
	es.release <- struct{}{}
	wait()
}

// A new server is capped at DefaultMaxInFlight without being told to be.
func TestNewServerIsCappedAtTheDefault(t *testing.T) {
	es := newHeldES()
	_, ts, _ := capServer(t, es, 0)
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: DefaultMaxInFlight}}
	t.Cleanup(client.CloseIdleConnections)
	wait := holdN(t, es, client, ts.URL, DefaultMaxInFlight)
	res, body, err := fetch(t, context.Background(), client, ts.URL+"/api/v1/cards/base1-4", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !overloaded(res, body) {
		t.Errorf("request %d answered %d %s, want the overloaded 503", DefaultMaxInFlight+1, res.StatusCode, body)
	}
	for range DefaultMaxInFlight {
		es.release <- struct{}{}
	}
	wait()
}

// Zero or a negative number is refused, never read as "no cap", and leaves
// the cap the server had.
func TestSetMaxInFlightRefusesZeroAndNegatives(t *testing.T) {
	es := newHeldES()
	s, ts, _ := capServer(t, es, 1)
	for _, n := range []int{0, -1, -1 << 40} {
		if err := s.SetMaxInFlight(n); err == nil {
			t.Errorf("SetMaxInFlight(%d) accepted", n)
		}
	}
	client := ts.Client()
	wait := holdN(t, es, client, ts.URL, 1)
	res, body, err := fetch(t, context.Background(), client, ts.URL+"/api/v1/cards/base1-4", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !overloaded(res, body) {
		t.Errorf("after refused settings, a second request answered %d %s, want the overloaded 503 of a cap of one", res.StatusCode, body)
	}
	es.release <- struct{}{}
	wait()
}
