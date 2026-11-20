package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AndresThePerez/pokesearch/internal/server"
)

// MAX_IN_FLIGHT unset is the default; a positive whole number is taken as it
// is; anything else stops the server from starting, so a typo can never
// become "no cap" or a cap nobody chose.
func TestMaxInFlightSetting(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		want    int
		wantErr bool
	}{
		{"", server.DefaultMaxInFlight, false},
		{"1", 1, false},
		{"64", 64, false},
		{"5000", 5000, false},
		{"0", 0, true},
		{"-1", 0, true},
		{"-128", 0, true},
		{"abc", 0, true},
		{"12.5", 0, true},
		{"1e3", 0, true},
		{" 64", 0, true},
		{"64 ", 0, true},
		{"+", 0, true},
		{"99999999999999999999999", 0, true},
	} {
		got, err := maxInFlightSetting(tc.raw)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("maxInFlightSetting(%q) = %d, %v; want %d, error %v", tc.raw, got, err, tc.want, tc.wantErr)
		}
		if err != nil && !strings.Contains(err.Error(), "MAX_IN_FLIGHT") {
			t.Errorf("maxInFlightSetting(%q) error %q does not name the variable", tc.raw, err)
		}
	}
}

// heldCluster is a fake Elasticsearch whose _mget waits until release is
// closed, so a card request is held inside the server.
func heldCluster(t *testing.T) (url string, entered chan struct{}, release chan struct{}) {
	t.Helper()
	entered = make(chan struct{}, 16)
	release = make(chan struct{})
	es := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "_mget") {
			entered <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
			}
			_, _ = io.WriteString(w, `{"docs":[]}`)
			return
		}
		_, _ = io.WriteString(w, `{"count":1}`)
	}))
	t.Cleanup(es.Close)
	return es.URL, entered, release
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

// run serves with the cap it is configured with.
func TestRunAppliesTheInFlightCap(t *testing.T) {
	esURL, entered, release := heldCluster(t)
	port := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, config{port: port, esURL: esURL, maxInFlight: 1}) }()
	base := "http://127.0.0.1:" + port
	for i := 0; ; i++ {
		if res, err := http.Get(base + "/livez"); err == nil {
			res.Body.Close()
			break
		}
		if i == 100 {
			t.Fatal("the server never started")
		}
		time.Sleep(20 * time.Millisecond)
	}

	held := make(chan struct{})
	go func() {
		defer close(held)
		if res, err := http.Get(base + "/api/v1/cards/base1-4"); err == nil {
			res.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first request never reached Elasticsearch")
	}
	res, err := http.Get(base + "/api/v1/cards/base1-4")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), `"code":"overloaded"`) {
		t.Errorf("a second request under a cap of one answered %d %s, want the overloaded 503", res.StatusCode, body)
	}
	close(release)
	<-held
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not exit after cancel")
	}
}

// A cap that is not positive is refused when run starts, never served as
// "no cap".
func TestRunRefusesANonPositiveCap(t *testing.T) {
	for _, n := range []int{-1, -1000} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := run(ctx, config{port: "0", esURL: "http://127.0.0.1:1", maxInFlight: n})
		cancel()
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			t.Errorf("run with maxInFlight %d = %v, want a start-up error", n, err)
		}
	}
}
