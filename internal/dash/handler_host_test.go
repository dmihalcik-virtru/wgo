package dash

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestHandlerHostCheckStrict(t *testing.T) {
	srv := newServer(t, largeDash(t))
	good := srv.Listener.Addr().String()
	host, port, _ := strings.Cut(good, ":")
	do := func(method, path, h string) (*http.Response, string) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = h
		if h == "" {
			// net/http fills in the URL host for an empty Host; send an
			// HTTP/1.0 request, which legitimately has none.
			return rawNoHost(t, method, path, good)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	for _, p := range []string{"/", "/api/snapshot"} {
		if resp, _ := do(http.MethodGet, p, good); resp.StatusCode != 200 {
			t.Fatalf("positive control GET %s with Host %q: %d", p, good, resp.StatusCode)
		}
	}
	bad := []string{"evil.example", host + ":" + port + "1", host + ".:" + port, "LOCALHOST:" + port, "", "localhost:" + port}
	for _, h := range bad {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, p := range []string{"/", "/api/snapshot"} {
				resp, body := do(method, p, h)
				if resp.StatusCode != http.StatusForbidden {
					t.Fatalf("%s %s Host %q: status %d", method, p, h, resp.StatusCode)
				}
				if method == http.MethodGet && body != "forbidden host\n" {
					t.Fatalf("%s %s Host %q: body %q", method, p, h, body)
				}
				if method == http.MethodHead && body != "" {
					t.Fatalf("HEAD %s Host %q: body %q", p, h, body)
				}
				if resp.Header.Get("Access-Control-Allow-Origin") != "" {
					t.Fatalf("%s %s Host %q: CORS header set", method, p, h)
				}
				if resp.Header.Get("Cache-Control") != "no-store" {
					t.Fatalf("%s %s Host %q: Cache-Control %q", method, p, h, resp.Header.Get("Cache-Control"))
				}
			}
		}
	}
}

// rawNoHost sends an HTTP/1.0 request with no Host header.
func rawNoHost(t *testing.T, method, path, addr string) (*http.Response, string) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "%s %s HTTP/1.0\r\n\r\n", method, path)
	req := &http.Request{Method: method}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}
