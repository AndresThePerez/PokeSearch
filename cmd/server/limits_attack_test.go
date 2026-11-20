package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// headOf builds a GET for target with the given header lines, padded to
// exactly size bytes by growing pad (a function of the remaining room).
func headOf(t *testing.T, size int, build func(room int) string) string {
	t.Helper()
	raw := build(0)
	raw = build(size - len(raw))
	if len(raw) != size {
		t.Fatalf("built a %d-byte head, want %d", len(raw), size)
	}
	return raw
}

// The limit counts the whole head wherever its bytes are: in the request
// line, in one header value, spread over a hundred headers, or in a header
// name. The largest head is served and one byte more is refused.
func TestHeadLimitHoldsWhereverTheBytesAre(t *testing.T) {
	shapes := map[string]func(room int) string{
		"request line": func(room int) string {
			return "GET /api/search?q=" + strings.Repeat("a", room) + " HTTP/1.1\r\nHost: x\r\n\r\n"
		},
		"one header value": func(room int) string {
			return "GET / HTTP/1.1\r\nHost: x\r\nX-Pad: " + strings.Repeat("v", room) + "\r\n\r\n"
		},
		"a hundred headers": func(room int) string {
			var b strings.Builder
			b.WriteString("GET / HTTP/1.1\r\nHost: x\r\n")
			for i := range 100 {
				n := room / 100
				if i == 0 {
					n += room % 100
				}
				fmt.Fprintf(&b, "X-H%02d: %s\r\n", i, strings.Repeat("v", n))
			}
			b.WriteString("\r\n")
			return b.String()
		},
		"a header name": func(room int) string {
			return "GET / HTTP/1.1\r\nHost: x\r\nX-" + strings.Repeat("N", room) + ": v\r\n\r\n"
		},
	}
	for name, build := range shapes {
		t.Run(name, func(t *testing.T) {
			addr, calls := serveWith(t)
			if status, _ := send(t, addr, headOf(t, headLimit, build)); status != "200 OK" {
				t.Errorf("a %d-byte head: %q, want 200", headLimit, status)
			}
			if status, _ := send(t, addr, headOf(t, headLimit+1, build)); status != "431 Request Header Fields Too Large" {
				t.Errorf("a %d-byte head: %q, want 431", headLimit+1, status)
			}
			if n := calls.Load(); n != 1 {
				t.Errorf("handler ran %d times, want 1", n)
			}
		})
	}
}

// The limit is per request, not per connection: ten largest heads in a row
// on one keep-alive connection, written at once (pipelined), are all served,
// and an oversized one after them is refused with the connection closed.
// Bytes of a pipelined head that net/http already holds in its 4 KB read
// buffer are not counted again, so there the refusal is asserted one buffer
// past the limit rather than one byte past it.
func TestHeadLimitIsPerRequestOnOneConnection(t *testing.T) {
	addr, calls := serveWith(t)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	line := func(size int) string {
		return headOf(t, size, func(room int) string {
			return "GET /?p=" + strings.Repeat("a", room) + " HTTP/1.1\r\nHost: x\r\n\r\n"
		})
	}
	var all strings.Builder
	for range 10 {
		all.WriteString(line(headLimit))
	}
	all.WriteString(line(headLimit + 2*goSlack))
	go func() { _, _ = io.WriteString(conn, all.String()) }()
	br := bufio.NewReader(conn)
	for i := range 10 {
		res, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("response %d: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("response %d: %s, want 200", i, res.Status)
		}
	}
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("last response: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 431 || !res.Close {
		t.Errorf("oversized head after ten: %s close=%v, want 431 closing the connection", res.Status, res.Close)
	}
	if n := calls.Load(); n != 10 {
		t.Errorf("handler ran %d times, want 10", n)
	}
}

// The refusal is exactly what consumers were told: a plain-text 431 that
// closes the connection, and the server keeps serving other connections.
func TestHeadLimitRefusalIsPlainAndTheServerCarriesOn(t *testing.T) {
	addr, calls := serveWith(t)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	go func() { _, _ = io.WriteString(conn, requestOfHeadSize(headLimit+1, "")) }()
	raw, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := "HTTP/1.1 431 Request Header Fields Too Large\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\nConnection: close\r\n\r\n" +
		"431 Request Header Fields Too Large"
	if string(raw) != want {
		t.Errorf("refusal = %q, want %q", raw, want)
	}
	for range 3 {
		if status, body := send(t, addr, "GET / HTTP/1.1\r\nHost: x\r\n\r\n"); status != "200 OK" || body != "handled" {
			t.Errorf("after a refusal: %q %q", status, body)
		}
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("handler ran %d times, want 3", n)
	}
}

// The server speaks HTTP/1 only over plain TCP: a client opening with the
// HTTP/2 preface is answered in HTTP/1.1 (net/http hands it on as an
// ordinary request), never with an HTTP/2 session, so no second, larger
// header limit applies anywhere.
func TestServerDoesNotSpeakCleartextHTTP2(t *testing.T) {
	addr, _ := serveWith(t)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	go func() { _, _ = io.WriteString(conn, "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n") }()
	raw, _ := io.ReadAll(conn)
	if !strings.HasPrefix(string(raw), "HTTP/1.1 ") {
		t.Errorf("answer to the HTTP/2 preface = %.80q, want HTTP/1.1", raw)
	}
}
