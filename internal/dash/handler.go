package dash

import (
	"net/http"
	"time"
)

// ViewSource supplies the published view. Dash implements it.
type ViewSource interface {
	Current() *View
}

const placeholderPage = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>wgo dash</title></head>
<body>
<h1>wgo dash</h1>
<p>The dashboard UI is not built yet. The snapshot is served at <a href="/api/snapshot">/api/snapshot</a>.</p>
</body>
</html>
`

// Handler serves the dashboard read-only. The page at / is a temporary
// placeholder until the UI lands (gh-70 slice 2); only the read-only snapshot
// endpoint is real, and the spec's token and Origin checks do not exist yet.
//
// It never runs discovery, jj, a network call or a process: it only
// serializes the view src currently holds, so a request costs a pointer load
// and a write. Requests whose Host header is not exactly host (the configured
// loopback host:port) are rejected, which defeats DNS rebinding.
func Handler(src ViewSource, host string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		_, _ = w.Write([]byte(placeholderPage))
	})
	mux.HandleFunc("/api/snapshot", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		v := src.Current()
		if v == nil {
			// Deliberately 200, not 503: this is a polled status resource,
			// not a one-shot page. A client that polls it expects a JSON
			// body every time and reads "status" to tell loading from
			// ready, so "no snapshot yet" is a normal state, not a failure.
			// A one-shot page would answer an unready server with an error.
			_, _ = w.Write([]byte(`{"status":"loading"}` + "\n"))
			return
		}
		body, err := v.encodeJSON(time.Now())
		if err != nil {
			logf("dash: encode snapshot: %v", err)
			http.Error(w, "snapshot could not be served", http.StatusInternalServerError)
			return
		}
		if _, err := w.Write(body); err != nil {
			logf("dash: write snapshot: %v", err) // the client went away
		}
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		if r.Host != host {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
