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

// Handler serves the dashboard read-only. It never runs discovery, jj, a
// network call or a process: it only serializes the view src currently
// holds, so a request costs a pointer load and a write. Requests whose Host
// header is not exactly host (the configured loopback host:port) are
// rejected, which defeats DNS rebinding.
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
			_, _ = w.Write([]byte(`{"status":"loading"}` + "\n"))
			return
		}
		_ = v.WriteJSON(w, time.Now())
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
