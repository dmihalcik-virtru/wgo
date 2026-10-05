package dash

import (
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// maxEntity bounds a /lookup entity.
const maxEntity = 1024

// resolveEntity maps a stable identifier to the live node it names and the
// efforts that node belongs to. It accepts live node IDs (effort:…, ws-…,
// pr:owner/repo#N, ticket:jira:KEY, agent:…) and the review graph's IDs for
// the same things: pr:<url> matches a live PR with exactly that URL, and
// ticket:<KEY> a live ticket with exactly that key or URL. Nothing is
// matched by title or label.
func resolveEntity(s *Snapshot, entity string) (node string, efforts []string) {
	if s == nil {
		return "", nil
	}
	ix := indexSnapshot(s)
	switch {
	case ix.ids[entity] != nil:
		node = entity
	case strings.HasPrefix(entity, "pr:"):
		u := strings.TrimPrefix(entity, "pr:")
		for i := range s.Nodes {
			if p := s.Nodes[i].PR; p != nil && p.URL != "" && p.URL == u {
				node = s.Nodes[i].ID
				break
			}
		}
	case strings.HasPrefix(entity, "ticket:"):
		key := strings.TrimPrefix(entity, "ticket:")
		if ix.ids[jiraTicketID(key)] != nil {
			node = jiraTicketID(key)
			break
		}
		for i := range s.Nodes {
			if t := s.Nodes[i].Ticket; t != nil && (t.Key == key || (t.URL != "" && t.URL == key)) {
				node = s.Nodes[i].ID
				break
			}
		}
	}
	if node == "" {
		return "", nil
	}
	return node, ix.effortsOf(node)
}

func validEntity(e string) bool {
	if e == "" || len(e) > maxEntity {
		return false
	}
	for _, r := range e {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// serveLookup resolves ?entity= against the current snapshot and serves the
// live page focused on it, or a page saying it is not active.
func (s *server) serveLookup(w http.ResponseWriter, r *http.Request) {
	entity := r.URL.Query().Get("entity")
	if !validEntity(entity) {
		s.serveNotice(w, http.StatusBadRequest, "wgo dash — bad lookup",
			"Look up an entity with /lookup?entity=<id>, for example a PR URL as pr:https://github.com/owner/repo/pull/1 or a ticket as ticket:KEY-1.")
		return
	}
	v := s.opts.Source.Current()
	if v == nil {
		w.Header().Set("Retry-After", "2")
		s.serveNotice(w, http.StatusServiceUnavailable, "wgo dash — loading",
			"wgo dash has not collected its first snapshot yet, so "+entity+" cannot be looked up. Reload in a moment.")
		return
	}
	node, efforts := resolveEntity(v.Snapshot, entity)
	if node == "" || len(efforts) == 0 {
		s.serveNotice(w, http.StatusNotFound, "wgo dash — not active",
			entity+" is not active: no effort in the current snapshot (generation "+strconv.FormatUint(v.Snapshot.Generation, 10)+") includes it. It may be finished, or its workspace may not be discovered.")
		return
	}
	boot := s.boot
	boot.Focus, boot.Entity = node, entity
	s.serveLive(w, boot, http.StatusOK)
}

// serveNotice writes a static, script-free page with a wgo title, a visible
// heading and a link back to the dashboard.
func (s *server) serveNotice(w http.ResponseWriter, status int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", staticCSP)
	w.WriteHeader(status)
	_, _ = w.Write(staticPage(title, "wgo dash", message, "Open the live dashboard", "/"))
}

// serveReviewIndex lists the runs the dashboard can serve.
func (s *server) serveReviewIndex(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head><meta charset=\"utf-8\"><meta name=\"color-scheme\" content=\"light dark\"><title>wgo dash — review runs</title></head>\n<body>\n<h1>wgo dash</h1>\n<h2>Year-in-review runs</h2>\n")
	var runs []*reviewRun
	if s.opts.Reviews != nil {
		runs = s.opts.Reviews.state.Load().runs
	}
	if len(runs) == 0 {
		b.WriteString("<p>No year-in-review runs found. Run the /year-in-review skill, then wait for the next refresh.</p>\n")
	} else {
		b.WriteString("<ul>\n")
		for _, run := range runs {
			b.WriteString("<li><a href=\"/review/" + html.EscapeString(url.PathEscape(run.label)) + "/\">" + html.EscapeString(run.label) + "</a> (" + run.mod.Format("2006-01-02 15:04") + ")</li>\n")
		}
		b.WriteString("</ul>\n")
	}
	b.WriteString("<p><a href=\"/\">Open the live dashboard</a></p>\n</body>\n</html>\n")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", staticCSP)
	_, _ = w.Write([]byte(b.String()))
}

// reviewRunFor resolves the {label} wildcard to a known run. The label is
// validated after routing because the wildcard value is unescaped (so it
// can contain "/" from %2F); it is only ever used as a map key, never as a
// path.
func (s *server) reviewRunFor(w http.ResponseWriter, r *http.Request) *reviewRun {
	run := s.opts.Reviews.run(r.PathValue("label"))
	if run == nil {
		http.NotFound(w, r)
	}
	return run
}

func (s *server) serveReviewRedirect(w http.ResponseWriter, r *http.Request) {
	if run := s.reviewRunFor(w, r); run != nil {
		http.Redirect(w, r, "/review/"+url.PathEscape(run.label)+"/", http.StatusMovedPermanently)
	}
}

func (s *server) serveReviewPage(w http.ResponseWriter, r *http.Request) {
	run := s.reviewRunFor(w, r)
	if run == nil {
		return
	}
	b, err := run.page.bytes(reviewBoot{Mode: "review", Served: true, Run: run.label, Lookup: "/lookup", Live: "/"})
	if err != nil {
		s.opts.Logf("render review run %s: %v", run.label, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", run.page.csp)
	_, _ = w.Write(b)
}

func (s *server) serveReviewJSON(w http.ResponseWriter, r *http.Request) {
	if run := s.reviewRunFor(w, r); run != nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(run.json)
	}
}

func (s *server) serveReviewLinks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.opts.Reviews == nil {
		_, _ = w.Write([]byte(`{"efforts":{},"entities":{},"errors":[]}` + "\n"))
		return
	}
	_, _ = w.Write(s.opts.Reviews.linksJSON(s.opts.Source.Current()))
}
