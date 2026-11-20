package main

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// MAX_IN_FLIGHT is taken only as plain ASCII decimal digits: other number
// syntaxes, other scripts' digits, separators and whitespace stop the server,
// and the largest int is a cap like any other.
func TestMaxInFlightSettingRefusesOtherNumberSyntaxes(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		want    int
		wantErr bool
	}{
		{"+5", 5, false},
		{"007", 7, false},
		{strconv.Itoa(math.MaxInt), math.MaxInt, false},
		{"9223372036854775808", 0, true},
		{"-9223372036854775808", 0, true},
		{"-0", 0, true},
		{"+0", 0, true},
		{"00", 0, true},
		{"0x10", 0, true},
		{"0b11", 0, true},
		{"0o17", 0, true},
		{"1_000", 0, true},
		{"1,000", 0, true},
		{"٣", 0, true},
		{"９", 0, true},
		{"5\n", 0, true},
		{"\t5", 0, true},
		{"5\x00", 0, true},
		{"++5", 0, true},
		{"Inf", 0, true},
		{"NaN", 0, true},
		{" ", 0, true},
	} {
		got, err := maxInFlightSetting(tc.raw)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("maxInFlightSetting(%q) = %d, %v; want %d, error %v", tc.raw, got, err, tc.want, tc.wantErr)
		}
		if err != nil && (!strings.Contains(err.Error(), "MAX_IN_FLIGHT") || !strings.Contains(err.Error(), strconv.Quote(tc.raw))) {
			t.Errorf("maxInFlightSetting(%q) error %q does not name the variable and the value", tc.raw, err)
		}
	}
}

// Whatever the variable holds, the setting is either an error that names it
// or a whole number of at least one whose decimal spelling is the value, an
// optional plus sign and leading zeros aside.
func FuzzMaxInFlightSetting(f *testing.F) {
	for _, seed := range []string{"", "1", "128", "+5", "0", "-1", "0x10", "1_000", "٣", " 7", "99999999999999999999"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		n, err := maxInFlightSetting(raw)
		if raw == "" {
			if err != nil || n < 1 {
				t.Fatalf("unset gave %d, %v", n, err)
			}
			return
		}
		if err != nil {
			if n != 0 || !strings.Contains(err.Error(), "MAX_IN_FLIGHT") {
				t.Fatalf("maxInFlightSetting(%q) = %d, %v", raw, n, err)
			}
			return
		}
		if n < 1 {
			t.Fatalf("maxInFlightSetting(%q) accepted %d", raw, n)
		}
		digits := strings.TrimLeft(strings.TrimPrefix(raw, "+"), "0")
		if digits != strconv.Itoa(n) {
			t.Fatalf("maxInFlightSetting(%q) = %d, which is not the value written", raw, n)
		}
	})
}

// The largest cap the variable can name is served, not a panic or an
// allocation of its size, and the probes and an ordinary request answer.
func TestRunServesWithTheLargestCap(t *testing.T) {
	esURL, entered, release := heldCluster(t)
	close(release)
	go func() {
		for range entered {
		}
	}()
	port := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, config{port: port, esURL: esURL, maxInFlight: math.MaxInt}) }()
	base := "http://127.0.0.1:" + port
	waitUp(t, base)
	for _, path := range []string{"/livez", "/healthz", "/api/v1/cards/base1-4"} {
		res, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode == http.StatusServiceUnavailable {
			t.Errorf("%s answered 503 %s under the largest cap", path, body)
		}
	}
	stopRun(t, cancel, done)
}

// With a cap of one and its slot held, the probes still answer 200, a second
// request is refused, and once the slot is free the next one is admitted.
func TestRunWithACapOfOneKeepsTheProbesAnswering(t *testing.T) {
	esURL, entered, release := heldCluster(t)
	port := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, config{port: port, esURL: esURL, maxInFlight: 1}) }()
	base := "http://127.0.0.1:" + port
	waitUp(t, base)

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
	for _, path := range []string{"/livez", "/healthz", "/livez", "/healthz"} {
		res, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("%s answered %d with the one slot held, want 200", path, res.StatusCode)
		}
	}
	res, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable || res.Header.Get("Retry-After") != "1" {
		t.Errorf("the page with the one slot held answered %d Retry-After %q, want 503 and 1", res.StatusCode, res.Header.Get("Retry-After"))
	}
	close(release)
	<-held
	res, err = http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("the page after the slot came back answered %d, want 200", res.StatusCode)
	}
	stopRun(t, cancel, done)
}

func waitUp(t *testing.T, base string) {
	t.Helper()
	for i := 0; ; i++ {
		if res, err := http.Get(base + "/livez"); err == nil {
			res.Body.Close()
			return
		}
		if i == 100 {
			t.Fatal("the server never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func stopRun(t *testing.T, cancel context.CancelFunc, done chan error) {
	t.Helper()
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
