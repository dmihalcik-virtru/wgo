package dash

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/virtru/wgo/internal/jira"
)

// This file is the Refresher's Jira circuit breaker. Once acli's login has
// expired, every Jira ticket and sprint lookup fails the same way, each after
// about a second and a half: hundreds of tickets would hold a waited-for
// refresh for minutes and fail it, saying only that acli failed. So the
// first such failure pauses the rest, which are reported as skipped, with
// the reason on the jira source, and the background refresh tries again
// later.

// Jira pause timing: the first pause lasts jiraPauseFirst; each lookup that
// fails again when it ends doubles it, up to jiraPauseMax.
const (
	jiraPauseFirst = 5 * time.Minute
	jiraPauseMax   = 30 * time.Minute
)

// jiraFailureStreak is how many Jira lookups in a row failing with the same
// error pause the rest when acli cannot say whether it is logged in.
const jiraFailureStreak = 3

// jiraLoginRecheck is how long an answer of Fetchers.JiraLogin is reused, so
// a ticket that keeps failing on its own (a deleted one) costs the served
// dashboard one more acli call per period at most.
const jiraLoginRecheck = 5 * time.Minute

// jiraNotLoggedIn is the reason of a pause for a lapsed acli login.
var jiraNotLoggedIn = jira.ErrNotLoggedIn.Error()

type loginState int

const (
	loginUnchecked loginState = iota
	loginOK
	loginOut
	loginUnknown
)

// jiraPause decides whether a Jira lookup runs. While closed (no reason),
// every lookup runs. A lookup failing with acli's "unauthorized" message
// pauses the rest; so does any other failure once acli's auth status
// (Fetchers.JiraLogin) confirms that the login lapsed, since `acli jira
// workitem view` does not say so itself. Should acli's auth status not tell,
// jiraFailureStreak failures in a row with the same error pause them too.
// While paused, lookups are skipped. When the pause ends, one lookup runs as
// a probe: success closes the breaker, failure pauses again for twice as
// long.
type jiraPause struct {
	// login is Fetchers.JiraLogin; nil when acli's auth status cannot be
	// asked.
	login func() error
	// first and max are jiraPauseFirst and jiraPauseMax; tests shorten them.
	first, max time.Duration

	checkMu sync.Mutex // one login check at a time; the others wait for it

	mu      sync.Mutex
	reason  string // why lookups are paused; "" while they run
	auth    bool   // paused because acli is logged out
	until   time.Time
	backoff time.Duration
	probing bool
	// last and streak count failures in a row with the same signature.
	last   string
	streak int
	// loginSt is the last answer of login, at loginAt.
	loginSt loginState
	loginAt time.Time
}

func newJiraPause(login func() error) *jiraPause {
	return &jiraPause{login: login, first: jiraPauseFirst, max: jiraPauseMax}
}

// pausedError is a Jira lookup the pause accounts for: skipped while lookups
// were paused, or run into the lapsed login that paused them. It is counted
// as skipped, not as a failure.
type pausedError struct {
	reason string
	err    error // the lookup's own failure, if it ran
}

func (e *pausedError) Error() string {
	if e.err != nil {
		return e.reason + ": " + e.err.Error()
	}
	return "skipped: " + e.reason
}

func (e *pausedError) Unwrap() error { return e.err }

// paused is the current pause's reason, or "" while lookups run.
func (p *jiraPause) paused() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reason
}

// admit reports whether a Jira lookup may run now, and whether it is the
// probe that ends a pause. An admitted lookup must be followed by done or
// release.
func (p *jiraPause) admit() (ok, probe bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.reason == "":
		return true, false
	case p.probing || time.Now().Before(p.until):
		return false, false
	}
	p.probing = true
	return true, true
}

// release ends an admitted lookup that made no call (another wgo process
// held its lease): it tells nothing about acli.
func (p *jiraPause) release(probe bool) {
	if probe {
		p.mu.Lock()
		p.probing = false
		p.mu.Unlock()
	}
}

// done records how an admitted lookup went and returns what to report: nil
// on success, a *pausedError for a failure the pause accounts for, else err.
func (p *jiraPause) done(err error, probe bool) error {
	if probe {
		defer p.release(true)
	}
	if err == nil {
		p.mu.Lock()
		p.closeLocked()
		p.loginSt, p.loginAt = loginOK, time.Now()
		p.mu.Unlock()
		return nil
	}
	p.mu.Lock()
	if p.reason != "" && !probe {
		// Another lookup paused the rest while this one ran.
		reason, auth := p.reason, p.auth
		p.mu.Unlock()
		if auth {
			return &pausedError{reason: reason, err: err}
		}
		return err
	}
	if jira.IsAuthError(err.Error()) {
		p.pauseLocked(jiraNotLoggedIn, true, probe)
		p.mu.Unlock()
		return &pausedError{reason: jiraNotLoggedIn, err: err}
	}
	if sig := failureSignature(err); sig == p.last {
		p.streak++
	} else {
		p.last, p.streak = sig, 1
	}
	streak := p.streak
	p.mu.Unlock()

	switch p.checkLogin() {
	case loginOut:
		p.mu.Lock()
		p.pauseLocked(jiraNotLoggedIn, true, probe)
		p.mu.Unlock()
		return &pausedError{reason: jiraNotLoggedIn, err: err}
	case loginOK:
		// acli is logged in: this ticket's own failure.
		if probe {
			p.mu.Lock()
			p.closeLocked()
			p.mu.Unlock()
		}
		return err
	}
	if probe || streak >= jiraFailureStreak {
		p.mu.Lock()
		p.pauseLocked(fmt.Sprintf("Jira lookups paused after acli failed %d %s in a row (%s)", streak, plural(streak, "time", "times"), oneLine(err.Error())), false, probe)
		p.mu.Unlock()
	}
	return err
}

// checkLogin asks acli whether it is logged in, reusing an answer younger
// than jiraLoginRecheck.
func (p *jiraPause) checkLogin() loginState {
	if p.login == nil {
		return loginUnknown
	}
	p.checkMu.Lock()
	defer p.checkMu.Unlock()
	p.mu.Lock()
	if p.loginSt != loginUnchecked && time.Since(p.loginAt) < jiraLoginRecheck {
		st := p.loginSt
		p.mu.Unlock()
		return st
	}
	p.mu.Unlock()
	st := loginOK
	if err := p.login(); err != nil {
		st = loginUnknown
		if errors.Is(err, jira.ErrNotLoggedIn) || jira.IsAuthError(err.Error()) {
			st = loginOut
		}
	}
	p.mu.Lock()
	p.loginSt, p.loginAt = st, time.Now()
	p.mu.Unlock()
	return st
}

// pauseLocked pauses lookups for reason: for p.first, or for twice the last
// pause when a probe failed. A lookup that ran beside the one that paused
// them changes nothing. p.mu is held.
func (p *jiraPause) pauseLocked(reason string, auth, probe bool) {
	switch {
	case p.reason == "":
		p.backoff = p.first
	case probe:
		p.backoff = min(2*p.backoff, p.max)
	default:
		return
	}
	p.reason, p.auth, p.until = reason, auth, time.Now().Add(p.backoff)
	p.last, p.streak = "", 0
	logf("dash: %s; pausing Jira lookups for %v", reason, p.backoff)
}

// closeLocked lets every lookup run again. p.mu is held.
func (p *jiraPause) closeLocked() {
	if p.reason != "" {
		logf("dash: Jira lookups work again")
	}
	p.reason, p.auth, p.until, p.backoff = "", false, time.Time{}, 0
	p.last, p.streak = "", 0
}

// signatureRe matches what differs between two lookups failing the same
// way: ticket keys, board and sprint IDs, exit codes.
var signatureRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9]*-\d+|\d+`)

// failureSignature is err with the parts that name the lookup blanked, so
// failures of two tickets for one cause compare equal.
func failureSignature(err error) string {
	return signatureRe.ReplaceAllString(oneLine(err.Error()), "#")
}

// oneLine folds s onto one line of at most 200 characters, for a diagnostic.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:199]) + "…"
	}
	return s
}

// markPaused shows a Jira pause on the sources it holds up: jira, and
// jira_sprints when a board is configured, are in error with its reason.
func (r *Refresher) markPaused(s *Snapshot) {
	why := r.jira.paused()
	if why == "" {
		return
	}
	for _, name := range []string{SourceJira, SourceJiraSprints} {
		st, ok := s.Sources[name]
		if !ok || (st.Fresh+st.Stale+st.Unknown+st.Error == 0 && len(st.Parts) == 0) {
			continue // nothing to look up
		}
		if name == SourceJiraSprints && len(st.Parts) == 0 {
			continue // no [jira] board
		}
		st.State, st.Detail = Error, why
		s.Sources[name] = st
	}
}
