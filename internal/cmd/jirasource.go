package cmd

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/jira"
	"github.com/virtru/wgo/internal/jiracache"
)

// Route jiracache's otherwise-swallowed cache faults through the WGO_DEBUG
// logger so a broken (unwritable/unreadable) cache is diagnosable without
// polluting the hot-path output.
func init() {
	jiracache.Logf = debugf
}

// jiraFetcher adapts the acli-backed jira client to jiracache.Fetcher: it reads
// a ticket's live status and assignee and projects them onto the compact
// jiracache.Info the cache stores.
type jiraFetcher struct{}

// newJiraFetcher builds a jiracache.Fetcher backed by the jira client.
func newJiraFetcher() jiracache.Fetcher {
	return jiraFetcher{}
}

// jiraFetcherFn is the seam used to obtain the Fetcher for cache resolution.
// Production uses the acli-backed newJiraFetcher; tests override it to inject a
// stub so they never shell out to acli.
var jiraFetcherFn = newJiraFetcher

// FetchJira implements jiracache.Fetcher. The acli shell-out lands here (off the
// statusline hot path), never on reads. Besides the status and assignee the
// statusline shows, it reads the ticket facts wgo dash shows (gh-98); see
// fetchTicketFacts.
func (jiraFetcher) FetchJira(ticket string) (jiracache.Info, error) {
	info, err := fetchTicketFacts(ticket)
	if err != nil {
		return jiracache.Info{}, err
	}
	// Best-effort: also cache the acli-detected site host so ticketURL can build
	// a Jira browse link when jira.site isn't configured. A failure here (no
	// acli, not authenticated) just leaves Site empty — it doesn't fail the
	// status fetch.
	if site, err := jira.SiteHost(); err == nil {
		info.Site = site
	}
	return info, nil
}

// resolveJiraStatus returns the live Jira status, assignee, and acli-detected
// site host for a ticket according to opts, delegating the cache/network
// reconciliation to jiracache.Resolve (mirroring resolvePRs):
//
//   - statusline hot path (LocalOnly): read-through only; a Stale/Miss serves
//     whatever is cached and kicks a background refresh. Never blocks on acli.
//   - wgo . (default): read-through with a synchronous fetch on a cold miss.
//   - --refresh (opts.Refresh): bypass the cache and fetch synchronously.
//
// It returns empty strings for non-Jira tickets (GH-<n> GitHub issues) and when
// the status/site is unavailable (no acli / not authenticated). site is only
// ever used by ticketURL as a fallback when jira.site isn't configured.
func resolveJiraStatus(ticket string, opts contextOptions) (status, assignee, site string) {
	if ticket == "" || strings.HasPrefix(ticket, "GH-") {
		return "", "", ""
	}
	info, _, _ := jiracache.Resolve(jiraFetcherFn(), ticket, jiraCacheOpts(opts))
	return info.Status, info.Assignee, info.Site
}

// jiraCacheOpts maps the context resolver options onto jiracache.Opts.
func jiraCacheOpts(opts contextOptions) jiracache.Opts {
	switch {
	case opts.Refresh:
		return jiracache.Opts{Synchronous: true}
	case opts.LocalOnly:
		return jiracache.Opts{TTL: jiraTTL(), RefreshStale: true}
	default:
		return jiracache.Opts{TTL: jiraTTL(), RefreshStale: true, SyncOnMiss: true}
	}
}

// jiraTTL returns the configured Jira cache freshness window (config
// cache.jira_ttl), falling back to 600s (10m) when config is unavailable or
// unset.
func jiraTTL() time.Duration {
	if cfg := config.Get(); cfg != nil && cfg.Cache.JiraTTL > 0 {
		return time.Duration(cfg.Cache.JiraTTL) * time.Second
	}
	return 600 * time.Second
}

// getIssueJSON is the acli seam of the ticket fetcher; tests replace it.
var getIssueJSON = jira.GetIssueJSON

// fetchTicketFacts reads a ticket with every field, so the site's custom
// fields come along under their IDs, and parses its facts with the field
// IDs jiraFieldIDs resolves. Should acli refuse every field, it falls back
// to the system fields by name, leaving the custom fields unknown with a
// note saying so.
func fetchTicketFacts(ticket string) (jiracache.Info, error) {
	raw, err := getIssueJSON(ticket, jira.FactFields)
	var ids jira.FieldIDs
	var notes []string
	if err != nil {
		var ferr error
		if raw, ferr = getIssueJSON(ticket, jira.FactFieldList); ferr != nil {
			return jiracache.Info{}, err
		}
		notes = append(notes, fmt.Sprintf("Jira %s: acli could not return every field (%s), so story points, sprints and team are unknown", ticket, acliErrorLine(err.Error())))
	} else {
		ids, notes = jiraFieldIDs(raw)
	}
	facts, factNotes, err := jira.ParseFacts(raw, ids)
	if err != nil {
		return jiracache.Info{}, fmt.Errorf("jira %s: %w", ticket, err)
	}
	info := infoFromFacts(facts)
	info.Notes = append(notes, factNotes...)
	return info, nil
}

// fieldIDRe matches a custom field ID.
var fieldIDRe = regexp.MustCompile(`^customfield_[0-9]+$`)

// noField is the [jira.fields] value for a field the site does not have.
const noField = "none"

// jiraFieldIDs resolves the site's custom field IDs: [jira.fields] first,
// then IDs discovered earlier (cached for jiracache.FieldsTTL in
// ~/.wgo/cache/jira/fields.json), then discovery in raw, a ticket's full
// JSON, whose finds are cached. notes name each field that stays unknown
// and the [jira.fields] key that would fix it.
func jiraFieldIDs(raw []byte) (jira.FieldIDs, []string) {
	var conf config.JiraFieldsConfig
	if cfg := config.Get(); cfg != nil {
		conf = cfg.Jira.Fields
	}
	configured := map[string]string{
		jira.FieldStoryPoints: conf.StoryPoints,
		jira.FieldSprint:      conf.Sprint,
		jira.FieldTeam:        conf.Team,
		jira.FieldEpicLink:    conf.EpicLink,
	}
	var ids jira.FieldIDs
	var notes []string
	unset := map[string]bool{}
	for _, name := range jira.FieldNames {
		switch v := strings.TrimSpace(configured[name]); {
		case strings.EqualFold(v, noField):
		case fieldIDRe.MatchString(v):
			ids.Set(name, v)
		default:
			if v != "" {
				notes = append(notes, fmt.Sprintf("ignoring [jira.fields] %s = %q in ~/.wgo/config.toml: not a field ID such as customfield_10016, or \"none\"", name, v))
			}
			unset[name] = true
		}
	}
	if len(unset) == 0 {
		return ids, notes
	}
	cached, _ := jiracache.ReadFields(jiracache.FieldsTTL)
	known := jira.FieldIDs{StoryPoints: cached.StoryPoints, Sprint: cached.Sprint, Team: cached.Team, EpicLink: cached.EpicLink}
	if found, err := jira.DiscoverFieldIDs(raw); err == nil {
		grew := false
		for _, name := range jira.FieldNames {
			if known.Get(name) == "" && found.Get(name) != "" {
				known.Set(name, found.Get(name))
				grew = true
			}
		}
		if grew {
			err := jiracache.WriteFields(jiracache.Fields{StoryPoints: known.StoryPoints, Sprint: known.Sprint, Team: known.Team, EpicLink: known.EpicLink})
			if err != nil {
				debugf("jira cache: write discovered fields: %v", err)
			}
		}
	}
	for _, name := range jira.FieldNames {
		if !unset[name] {
			continue
		}
		if id := known.Get(name); id != "" {
			ids.Set(name, id)
			continue
		}
		// Jira Cloud reports epics as parent, so a missing epic link is
		// not worth a diagnostic.
		if name == jira.FieldEpicLink {
			continue
		}
		hint := "acli cannot list a site's fields to discover it"
		if name == jira.FieldSprint {
			hint = "it is discovered from a ticket that is in a sprint"
		}
		notes = append(notes, fmt.Sprintf("Jira %s are unknown: set %s in [jira.fields] of ~/.wgo/config.toml to this site's field ID (such as customfield_10016), or to \"none\"; %s",
			jira.FieldLabel(name), name, hint))
	}
	return ids, notes
}

// infoFromFacts projects parsed facts onto the cached Info.
func infoFromFacts(f jira.Facts) jiracache.Info {
	info := jiracache.Info{
		Status:           f.Status,
		Assignee:         f.Assignee,
		Summary:          f.Summary,
		StatusCategory:   f.StatusCategory,
		Type:             f.Type,
		Priority:         f.Priority,
		StoryPoints:      f.StoryPoints,
		StoryPointsKnown: f.StoryPointsKnown,
		Team:             f.Team,
		Components:       f.Components,
		Labels:           f.Labels,
		Resolution:       f.Resolution,
		ResolvedAt:       f.ResolvedAt,
		CreatedAt:        f.CreatedAt,
		UpdatedAt:        f.UpdatedAt,
		AssigneeID:       f.AssigneeID,
	}
	if f.Sprints != nil {
		info.Sprints = make([]jiracache.SprintRef, 0, len(f.Sprints))
		for _, s := range f.Sprints {
			info.Sprints = append(info.Sprints, jiracache.SprintRef{ID: s.ID, Name: s.Name, State: s.State})
		}
	}
	if f.Parent != nil {
		info.Parent = &jiracache.Parent{Key: f.Parent.Key, Summary: f.Parent.Summary, Type: f.Parent.Type}
	}
	return info
}

// acliErrorLine folds an acli error onto one line: its message comes after a
// first line that only says the command failed. It is capped at 300
// characters, for a diagnostic.
func acliErrorLine(s string) string {
	var parts []string
	for line := range strings.SplitSeq(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			parts = append(parts, line)
		}
	}
	out := strings.Join(parts, ": ")
	if r := []rune(out); len(r) > 300 {
		out = string(r[:299]) + "…"
	}
	return out
}
