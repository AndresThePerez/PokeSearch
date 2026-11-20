package main

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// goSlack is the allowance net/http adds on top of Server.MaxHeaderBytes: the
// server stops reading a request's line and headers at MaxHeaderBytes plus
// 4096 bytes (the bufio slop), so that sum is the largest accepted head.
const goSlack = 4096

// headLimit is the largest request head (request line, headers and the blank
// line that ends them) the server as main builds it accepts.
const headLimit = maxRequestHeaderBytes + goSlack

// serveWith starts the server as main builds it, around a handler that counts
// its calls, on a loopback listener.
func serveWith(t *testing.T) (addr string, calls *atomic.Int32) {
	t.Helper()
	calls = new(atomic.Int32)
	srv := newHTTPServer("", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = fmt.Fprint(w, "handled")
	}))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String(), calls
}

// send writes raw bytes to the server and returns its status line and body.
// The standard client refuses to send some of these requests, hence raw.
func send(t *testing.T, addr, raw string) (status, body string) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte(raw)); err != nil {
		t.Fatalf("write: %v", err)
	}
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer res.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 512)
	for {
		n, err := res.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return res.Status, sb.String()
}

// requestOfHeadSize builds a GET whose head is exactly size bytes, by padding
// the value of the one query parameter named pad.
func requestOfHeadSize(size int, extraHeaders string) string {
	const prefix = "GET /api/search?pad="
	const suffix = " HTTP/1.1\r\nHost: 127.0.0.1\r\n"
	const end = "\r\n"
	fixed := len(prefix) + len(suffix) + len(extraHeaders) + len(end)
	return prefix + strings.Repeat("a", size-fixed) + suffix + extraHeaders + end
}

func TestHTTPServerRefusesAnOversizedRequestLine(t *testing.T) {
	addr, calls := serveWith(t)
	raw := requestOfHeadSize(headLimit+1, "")
	status, body := send(t, addr, raw)
	if status != "431 Request Header Fields Too Large" {
		t.Errorf("status %q, want 431", status)
	}
	if body != "431 Request Header Fields Too Large" {
		t.Errorf("body %q", body)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("handler ran %d times for a refused request", n)
	}
}

func TestHTTPServerAcceptsARequestAtTheLimit(t *testing.T) {
	addr, calls := serveWith(t)
	status, body := send(t, addr, requestOfHeadSize(headLimit, ""))
	if status != "200 OK" || body != "handled" {
		t.Errorf("got %q %q, want the handler's 200", status, body)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("handler ran %d times, want 1", n)
	}
}

func TestHTTPServerRefusesOneOversizedHeader(t *testing.T) {
	addr, calls := serveWith(t)
	raw := "GET /healthz HTTP/1.1\r\nHost: 127.0.0.1\r\nX-Padding: " + strings.Repeat("b", headLimit) + "\r\n\r\n"
	status, _ := send(t, addr, raw)
	if status != "431 Request Header Fields Too Large" {
		t.Errorf("status %q, want 431", status)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("handler ran %d times for a refused request", n)
	}
}

// The limit is a bound for abuse, not a squeeze on real traffic. The largest
// requests the page, Courier and PokéPrices send, each behind the headers a
// CDN and a browser add, reach the handler.
func TestHTTPServerAcceptsTheLargestRealRequests(t *testing.T) {
	cdn := "User-Agent: " + strings.Repeat("u", 300) + "\r\n" +
		"Accept: text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8\r\n" +
		"Accept-Language: en-GB,en;q=0.9\r\nAccept-Encoding: gzip, br, zstd\r\n" +
		"Cookie: " + strings.Repeat("c", 1500) + "\r\n" +
		"Referer: https://pokesearch.example/?q=" + strings.Repeat("r", 500) + "\r\n" +
		"Cf-Ray: 0000000000000000-AMS\r\nCf-Connecting-Ip: 192.0.2.10\r\nCdn-Loop: cloudflare\r\n" +
		"X-Forwarded-For: 192.0.2.10, 198.51.100.7\r\nX-Forwarded-Proto: https\r\n" +
		"Cf-Ipcountry: NL\r\nCf-Visitor: {\"scheme\":\"https\"}\r\nX-Api-Key: " + strings.Repeat("0", 64) + "\r\n"
	// 100 ids of the longest length the pattern allows, none needing escapes.
	ids := make([]string, 100)
	for i := range ids {
		ids[i] = fmt.Sprintf("a%0127d", i)
	}
	// A 256-character q with every byte percent-encoded (non-ASCII, 3 bytes
	// per character, so 9 URL bytes each is the worst of it).
	encodedQ := strings.Repeat("%E2%9C%A8", 256)
	for name, target := range map[string]string{
		"100 ids at the maximum length": "/api/v1/cards?ids=" + strings.Join(ids, ","),
		"100 ids with encoded commas":   "/api/v1/cards?ids=" + strings.Join(ids, "%2C"),
		"fully encoded q":               "/api/search?q=" + encodedQ + "&page_size=100&sort=name&order=asc&series=Scarlet+%26+Violet",
	} {
		t.Run(name, func(t *testing.T) {
			addr, calls := serveWith(t)
			raw := "GET " + target + " HTTP/1.1\r\nHost: 127.0.0.1\r\n" + cdn + "\r\n"
			if len(raw) > headLimit {
				t.Fatalf("the request head is %d bytes, over the limit of %d", len(raw), headLimit)
			}
			status, _ := send(t, addr, raw)
			if status != "200 OK" || calls.Load() != 1 {
				t.Errorf("status %q after %d handler calls, want the handler's 200", status, calls.Load())
			}
		})
	}
}

// The timeouts guard against slow clients; building the server in one place
// must not lose or change any of them.
func TestHTTPServerKeepsItsTimeouts(t *testing.T) {
	srv := newHTTPServer(":8080", http.NotFoundHandler())
	for _, tc := range []struct {
		name      string
		got, want time.Duration
	}{
		{"ReadHeaderTimeout", srv.ReadHeaderTimeout, 5 * time.Second},
		{"ReadTimeout", srv.ReadTimeout, 10 * time.Second},
		{"WriteTimeout", srv.WriteTimeout, 30 * time.Second},
		{"IdleTimeout", srv.IdleTimeout, 120 * time.Second},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %s, want %s", tc.name, tc.got, tc.want)
		}
	}
	if srv.Addr != ":8080" {
		t.Errorf("Addr = %q", srv.Addr)
	}
}

// Keep the helper honest: a plain request answers through the same path.
func TestHTTPServerServesAnOrdinaryRequest(t *testing.T) {
	addr, calls := serveWith(t)
	status, body := send(t, addr, "GET /healthz HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n")
	if status != "200 OK" || body != "handled" || calls.Load() != 1 {
		t.Errorf("got %q %q after %d calls", status, body, calls.Load())
	}
}
