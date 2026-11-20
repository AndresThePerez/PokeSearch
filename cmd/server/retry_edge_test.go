package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
)

// scriptedTransport answers round trip n with outcomes[n] (the last one
// repeats) and keeps the body of every request it was sent.
type scriptedTransport struct {
	mu       sync.Mutex
	outcomes []func(*http.Request) (*http.Response, error)
	bodies   [][]byte
}

func (s *scriptedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	s.mu.Lock()
	n := len(s.bodies)
	s.bodies = append(s.bodies, body)
	s.mu.Unlock()
	if n >= len(s.outcomes) {
		n = len(s.outcomes) - 1
	}
	return s.outcomes[n](r)
}

func (s *scriptedTransport) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

func reply(status int, body string) func(*http.Request) (*http.Response, error) {
	return func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"X-Elastic-Product": []string{"Elasticsearch"}, "Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	}
}

func refuse(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial tcp 192.0.2.10:9200: connect: connection refused")
}

func scriptedClient(t *testing.T, rt *scriptedTransport) *elasticsearch.Client {
	t.Helper()
	cfg := newESConfig("http://192.0.2.10:9200")
	cfg.Transport = rt
	es, err := elasticsearch.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

// Only a 502, a 504 or a transport error earns the one retry; every other
// status, the client's own mistakes and the cluster's other failures alike,
// is answered after a single round trip.
func TestESConfigRetriesOnlyGatewayFailures(t *testing.T) {
	for _, tc := range []struct {
		status    int
		wantCalls int
	}{
		{200, 1}, {201, 1}, {400, 1}, {401, 1}, {404, 1}, {409, 1}, {429, 1},
		{500, 1}, {501, 1}, {503, 1}, {505, 1}, {502, 2}, {504, 2},
	} {
		rt := &scriptedTransport{outcomes: []func(*http.Request) (*http.Response, error){reply(tc.status, `{}`)}}
		res, err := scriptedClient(t, rt).Info()
		if err == nil {
			res.Body.Close()
		}
		if got := rt.calls(); got != tc.wantCalls {
			t.Errorf("status %d: %d round trips, want %d", tc.status, got, tc.wantCalls)
		}
	}
}

// A retry that succeeds is a success, and the retried _search carries the
// same body bytes as the attempt that failed.
func TestESConfigRetryResendsTheBodyAndCanSucceed(t *testing.T) {
	for name, first := range map[string]func(*http.Request) (*http.Response, error){
		"refused":     refuse,
		"bad gateway": reply(502, `{"error":"proxy"}`),
	} {
		t.Run(name, func(t *testing.T) {
			rt := &scriptedTransport{outcomes: []func(*http.Request) (*http.Response, error){
				first, reply(200, `{"took":1,"hits":{"hits":[]}}`),
			}}
			es := scriptedClient(t, rt)
			query := `{"query":{"match":{"name":"charizard \"ex\" é"}},"size":24}`
			res, err := es.Search(es.Search.WithIndex("cards"), es.Search.WithBody(strings.NewReader(query)))
			if err != nil {
				t.Fatalf("the retry should have answered: %v", err)
			}
			defer res.Body.Close()
			if res.StatusCode != 200 || rt.calls() != 2 {
				t.Fatalf("status %d after %d round trips, want 200 after 2", res.StatusCode, rt.calls())
			}
			for i, got := range rt.bodies {
				if !bytes.Equal(got, []byte(query)) {
					t.Errorf("attempt %d sent %q, want %q", i+1, got, query)
				}
			}
		})
	}
}

// After the last attempt nothing is drained: a 502 that is still a 502 on
// retry reaches the caller with its body readable, so the cause can be
// logged, and the call is not held for a trailing backoff.
func TestESConfigLastFailureKeepsItsBody(t *testing.T) {
	const cause = "upstream-cause-0000"
	rt := &scriptedTransport{outcomes: []func(*http.Request) (*http.Response, error){reply(502, `{"error":"`+cause+`"}`)}}
	start := time.Now()
	res, err := scriptedClient(t, rt).Count()
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("a status reply is not a transport error: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 502 || !strings.Contains(string(body), cause) || rt.calls() != 2 {
		t.Errorf("status %d body %q after %d round trips, want the second 502 with its body", res.StatusCode, body, rt.calls())
	}
	// One 100 ms pause before the retry, none after it: generous for a slow
	// runner, but well short of the three-pause budget this replaced.
	if elapsed < 90*time.Millisecond || elapsed > 2*time.Second {
		t.Errorf("two attempts took %s, want about one backoff", elapsed)
	}
	cfg := newESConfig("http://192.0.2.10:9200")
	if got := cfg.RetryBackoff(1); got != esRetryBackoff {
		t.Errorf("pause before the retry %s, want %s", got, esRetryBackoff)
	}
	for _, attempt := range []int{2, 3, 10} {
		if got := cfg.RetryBackoff(attempt); got != 0 {
			t.Errorf("pause after attempt %d is %s, want none", attempt, got)
		}
	}
}

// A request cancelled while the client waits to retry gives up there: the
// second round trip never happens and the caller sees the cancellation.
func TestESConfigCancelDuringBackoffStopsTheRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt := &scriptedTransport{outcomes: []func(*http.Request) (*http.Response, error){
		func(r *http.Request) (*http.Response, error) {
			cancel()
			return refuse(r)
		},
		reply(200, `{"count":1}`),
	}}
	es := scriptedClient(t, rt)
	start := time.Now()
	res, err := es.Count(es.Count.WithContext(ctx))
	if err == nil {
		res.Body.Close()
		t.Fatal("a cancelled request answered")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want the cancellation", err)
	}
	if rt.calls() != 1 {
		t.Errorf("%d round trips, want the retry abandoned", rt.calls())
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("a cancelled call took %s", elapsed)
	}
}
