package dash

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/virtru/wgo/internal/review"
)

// ViewSource supplies the published view. Dash implements it.
type ViewSource interface {
	Current() *View
}

// diagnosticSource is a ViewSource that can report its problems before it
// has a view. Dash implements it.
type diagnosticSource interface {
	Diagnostics() []string
}

// pendingDiagnostics returns src's problems while it has no view yet, or nil.
func pendingDiagnostics(src ViewSource) []string {
	if d, ok := src.(diagnosticSource); ok {
		return d.Diagnostics()
	}
	return nil
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
	// Actions, when set, enables POST /api/action (Open tab, Resume,
	// Editor, Finder, Plan, Spec).
	Actions *ActionOptions
	// Ack, when set, enables POST /api/ack (Mark seen).
	Ack Acker
	// Token is the per-launch action token the page must send in
	// TokenHeader. When empty and actions or ack are enabled, NewHandler
	// generates one. It is embedded only in the live page.
	Token string
	// Filters are the saved filters ([dash.filters] merged over the
	// built-ins); nil means the built-ins alone.
	Filters []SavedFilter
	// Addons replaces the live page's addon scripts (liveAddons), read from
	// AddonFS when set instead of the embedded files. Tests use them.
	Addons  []string
	AddonFS fs.FS
}

func stderrLogf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "wgo dash: "+format+"\n", args...)
}

// DefaultPollInterval is how often the live page polls by default.
const DefaultPollInterval = 5 * time.Second

// Handler serves the dashboard read-only with default options.
func Handler(src ViewSource, host string) http.Handler {
	h, _ := NewHandler(HandlerOptions{Source: src, Host: host}) // read-only: cannot fail
	return h
}

// server holds the pre-rendered live page and the routes; review pages
// are rendered by ReviewIndex.Scan.
type server struct {
	opts  HandlerOptions
	token string // the action token; empty when no POST route is enabled
	live  *page
	err   error // rendering the live page failed (a build defect)
	boot  liveBoot
}

// NewHandler serves the dashboard. Its GET routes never run discovery, jj, a
// network call or a process: they only read in-memory state (the view src
// currently publishes, the live page rendered at construction, and the
// review pages and links the background Scan prepared). Requests whose Host
// header is not exactly opts.Host are rejected, which defeats DNS rebinding.
//
// The only state-changing routes are POST /api/action and POST /api/ack,
// enabled by opts.Actions and opts.Ack. They additionally require an Origin
// exactly matching the server, the per-launch token in TokenHeader, a JSON
// body with only known fields, and a known action. No route ever sends CORS
// headers.
//
// NewHandler fails only when POST routes are enabled and no action token
// can be generated: it never falls back to serving read-only.
func NewHandler(opts HandlerOptions) (http.Handler, error) {
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultPollInterval
	}
	if opts.Logf == nil {
		opts.Logf = stderrLogf
	}
	if opts.Filters == nil {
		opts.Filters = MergeFilters(nil, Fields())
	}
	s := &server{opts: opts}
	reg := Fields().JSON()
	s.boot = liveBoot{Mode: string(review.ModeLive), API: "/api/snapshot", PollMS: opts.PollInterval.Milliseconds(),
		Fields: &reg, Filters: opts.Filters, Extract: ExtractPath}
	if opts.Reviews != nil {
		s.boot.Links = "/api/review-links"
	}
	if opts.Actions != nil || opts.Ack != nil {
		s.token = opts.Token
		if s.token == "" {
			var err error
			if s.token, err = newToken(); err != nil {
				return nil, fmt.Errorf("wgo dash: generate the browser action token: %w", err)
			}
		}
	}
	if s.token != "" {
		s.boot.Token = s.token
		if opts.Actions != nil {
			s.boot.ActionAPI = ActionPath
			s.boot.Resume = opts.Actions.Resume
		}
		if opts.Ack != nil {
			s.boot.AckAPI = AckPath
		}
	}
	addons := liveAddons
	if opts.Addons != nil {
		addons = opts.Addons
	}
	s.live, s.err = newPage(review.Page{Mode: review.ModeLive, Title: "live work", Boot: s.boot, Addons: addons, AddonFS: opts.AddonFS})
	if s.err != nil {
		opts.Logf("render the live page: %v", s.err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) { s.serveLive(w, s.boot, http.StatusOK) })
	mux.HandleFunc("/api/snapshot", s.serveSnapshot)
	mux.HandleFunc(ExtractPath, s.serveExtract)
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
		if r.URL.Path == ActionPath || r.URL.Path == AckPath {
			// OPTIONS (a CORS preflight) lands here too: it is refused
			// like any other method, with no Access-Control-Allow-*.
			if r.Method != http.MethodPost {
				h.Set("Allow", http.MethodPost)
				s.writeError(w, reject(http.StatusMethodNotAllowed, "%s accepts only POST", r.URL.Path))
				return
			}
			if r.URL.Path == ActionPath {
				s.serveAction(w, r)
			} else {
				s.serveAck(w, r)
			}
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		mux.ServeHTTP(w, r)
	}), nil
}

func (s *server) serveSnapshot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	v := s.opts.Source.Current()
	if v == nil {
		// Deliberately 200, not 503: this is a polled status resource, not a
		// one-shot page. A client that polls it expects a JSON body every
		// time and reads "status" to tell loading from ready, so "no
		// snapshot yet" is a normal state, not a failure. Diagnostics say
		// why the first snapshot has not arrived, e.g. a failed collection.
		body, err := json.Marshal(struct {
			Status      string   `json:"status"`
			Diagnostics []string `json:"diagnostics,omitempty"`
		}{"loading", pendingDiagnostics(s.opts.Source)})
		if err != nil {
			body = []byte(`{"status":"loading"}`)
		}
		_, _ = w.Write(append(body, '\n'))
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

// ExtractPath is the Markdown extract route (spec/gh-100.md, requirement 24).
const ExtractPath = "/api/extract"

// serveExtract renders the held snapshot as Markdown for q, cols, group,
// layout, view and sprint, exactly as wgo dash --md does. Like every GET
// route it reads only the published view.
func (s *server) serveExtract(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	v := s.opts.Source.Current()
	if v == nil {
		http.Error(w, "wgo dash has no snapshot yet; try again once the page shows one", http.StatusServiceUnavailable)
		return
	}
	p := r.URL.Query()
	md, err := Extract(v, Fields(), s.opts.Filters, ExtractArgs{
		Query: p.Get("q"), Cols: SplitCols(p.Get("cols")), Group: p.Get("group"),
		Layout: p.Get("layout"), View: p.Get("view"), Sprint: p.Get("sprint"),
	})
	if err != nil {
		var qe *QueryError
		if errors.As(err, &qe) || errors.Is(err, ErrExtractArgs) {
			http.Error(w, strings.TrimPrefix(err.Error(), ErrExtractArgs.Error()+": "), http.StatusBadRequest)
			return
		}
		s.opts.Logf("render the extract: %v", err)
		http.Error(w, "the extract could not be rendered", http.StatusInternalServerError)
		return
	}
	h.Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = w.Write([]byte(md))
}

// SplitCols splits a comma-separated column list, dropping blanks.
func SplitCols(s string) []string {
	var out []string
	for _, c := range strings.Split(s, ",") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// serveLive writes the live explorer with boot as its bootstrap. Me and
// People come from the snapshot being served, when there is one.
func (s *server) serveLive(w http.ResponseWriter, boot liveBoot, status int) {
	if s.opts.Source != nil {
		if v := s.opts.Source.Current(); v != nil {
			env := v.Env()
			boot.Me, boot.People = env.Me, env.People
		}
	}
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
