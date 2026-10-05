package dash

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/virtru/wgo/internal/review"
)

// ViewSource supplies the published view. Dash implements it.
type ViewSource interface {
	Current() *View
}

// HandlerOptions configures NewHandler.
type HandlerOptions struct {
	// Source supplies the view to serve.
	Source ViewSource
	// Host is the exact Host header to accept: the loopback host:port the
	// server listens on.
	Host string
	// PollInterval is how often the live page asks for /api/snapshot;
	// zero means DefaultPollInterval.
	PollInterval time.Duration
	// Reviews, when set, enables the /review/ routes and review cross-links.
	Reviews *ReviewIndex
	// Logf receives server faults, such as a page that fails to render;
	// nil writes them to stderr.
	Logf func(format string, args ...any)
}

func stderrLogf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "wgo dash: "+format+"\n", args...)
}

// DefaultPollInterval is how often the live page polls by default.
const DefaultPollInterval = 5 * time.Second

// Handler serves the dashboard read-only with default options.
func Handler(src ViewSource, host string) http.Handler {
	return NewHandler(HandlerOptions{Source: src, Host: host})
}

// server holds the pre-rendered pages and routes.
type server struct {
	opts HandlerOptions
	live *page
	err  error // rendering the live page failed (a build defect)
	boot liveBoot
}

// NewHandler serves the dashboard read-only. It never runs discovery, jj, a
// network call or a process: it only serializes what it already holds (the
// view src currently publishes, and pages rendered once at construction), so
// a request costs a pointer load and a write. Requests whose Host header is
// not exactly opts.Host are rejected, which defeats DNS rebinding.
func NewHandler(opts HandlerOptions) http.Handler {
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultPollInterval
	}
	if opts.Logf == nil {
		opts.Logf = stderrLogf
	}
	s := &server{opts: opts}
	s.boot = liveBoot{Mode: string(review.ModeLive), API: "/api/snapshot", PollMS: opts.PollInterval.Milliseconds()}
	if opts.Reviews != nil {
		s.boot.Links = "/api/review-links"
	}
	s.live, s.err = newPage(review.Page{Mode: review.ModeLive, Title: "live work", Boot: s.boot})
	if s.err != nil {
		opts.Logf("render the live page: %v", s.err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) { s.serveLive(w, s.boot, http.StatusOK) })
	mux.HandleFunc("/api/snapshot", s.serveSnapshot)
	mux.HandleFunc("/lookup", s.serveLookup)
	if opts.Reviews != nil {
		mux.HandleFunc("/api/review-links", s.serveReviewLinks)
		mux.HandleFunc("/review/{$}", s.serveReviewIndex)
		mux.HandleFunc("/review/{label}", s.serveReviewRedirect)
		mux.HandleFunc("/review/{label}/{$}", s.serveReviewPage)
		mux.HandleFunc("/review/{label}/graph.json", s.serveReviewJSON)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		if r.Host != opts.Host {
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

func (s *server) serveSnapshot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	v := s.opts.Source.Current()
	if v == nil {
		// Deliberately 200, not 503: this is a polled status resource, not a
		// one-shot page. A client that polls it expects a JSON body every
		// time and reads "status" to tell loading from ready, so "no
		// snapshot yet" is a normal state, not a failure.
		_, _ = w.Write([]byte(`{"status":"loading"}` + "\n"))
		return
	}
	// Encode before writing so an encoding failure can still be a 500; a
	// failed write is only the client going away.
	body, err := v.encodeJSON(time.Now())
	if err != nil {
		s.opts.Logf("encode the snapshot: %v", err)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		http.Error(w, "snapshot could not be served", http.StatusInternalServerError)
		return
	}
	if _, err := w.Write(body); err != nil {
		s.opts.Logf("write the snapshot: %v", err)
	}
}

// serveLive writes the live explorer with boot as its bootstrap.
func (s *server) serveLive(w http.ResponseWriter, boot liveBoot, status int) {
	if s.err != nil {
		s.opts.Logf("render the live page: %v", s.err)
		http.Error(w, "wgo dash could not render its page: "+s.err.Error(), http.StatusInternalServerError)
		return
	}
	b, err := s.live.bytes(boot)
	if err != nil {
		s.opts.Logf("render the live page: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", s.live.csp)
	w.WriteHeader(status)
	_, _ = w.Write(b)
}
