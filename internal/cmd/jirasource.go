package cmd

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
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

// jiraFetcher adapts the twg-backed jira client to jiracache.Fetcher: it reads
// a ticket's live status and assignee and projects them onto the compact
// jiracache.Info the cache stores.
type jiraFetcher struct{}

// newJiraFetcher builds a jiracache.Fetcher backed by the jira client.
func newJiraFetcher() jiracache.Fetcher {
	return jiraFetcher{}
}

// jiraFetcherFn is the seam used to obtain the Fetcher for cache resolution.
// Production uses the twg-backed newJiraFetcher; tests override it to inject a
// stub so they never shell out to twg.
var jiraFetcherFn = newJiraFetcher

// FetchJira implements jiracache.Fetcher. The twg shell-out lands here (off the
// statusline hot path), never on reads. Besides the status and assignee the
// statusline shows, it reads the ticket facts wgo dash shows (gh-98); see
// fetchTicketFacts.
func (f jiraFetcher) FetchJira(ticket string) (jiracache.Info, error) {
	return f.FetchJiraContext(context.Background(), ticket)
}

// FetchJiraContext implements jiracache.ContextFetcher: FetchJira, with
// twg killed when ctx is done.
func (jiraFetcher) FetchJiraContext(ctx context.Context, ticket string) (jiracache.Info, error) {
	info, err := fetchTicketFactsContext(ctx, ticket)
	if err != nil {
		return jiracache.Info{}, err
	}
	// Best-effort: also cache the twg-detected site host so ticketURL can build
	// a Jira browse link when jira.site isn't configured. A failure here (no
	// twg, not authenticated) just leaves Site empty — it doesn't fail the
	// status fetch.
	info.Site = twgSiteHost(ctx)
	return info, nil
}

// FetchJiraBatchContext hydrates dashboard tickets with a single Jira
// bulkfetch request. A missing ticket does not discard its neighbors.
func (jiraFetcher) FetchJiraBatchContext(ctx context.Context, tickets []string) (map[string]jiracache.FetchOutcome, error) {
	items, err := jira.BulkGetIssuesJSONContext(ctx, tickets, jira.FactFields)
	if err != nil {
		return nil, err
	}
	site := twgSiteHost(ctx)
	out := make(map[string]jiracache.FetchOutcome, len(items))
	for _, item := range items {
		if item.Err != nil {
			out[item.Input] = jiracache.FetchOutcome{Err: item.Err}
			continue
		}
		ids, notes := jiraFieldIDs(ctx, item.Input, item.Data)
		facts, factNotes, err := jira.ParseFacts(item.Data, ids)
		if err != nil {
			out[item.Input] = jiracache.FetchOutcome{Err: fmt.Errorf("jira %s: %w", item.Input, err)}
			continue
		}
		info := infoFromFacts(facts)
		info.Site = site
		info.Notes = append(notes, factNotes...)
		out[item.Input] = jiracache.FetchOutcome{Info: info}
	}
	return out, nil
}

// resolveJiraStatus returns the live Jira status, assignee, and twg-detected
// site host for a ticket according to opts, delegating the cache/network
// reconciliation to jiracache.Resolve (mirroring resolvePRs):
//
//   - statusline hot path (LocalOnly): read-through only; a Stale/Miss serves
//     whatever is cached and kicks a background refresh. Never blocks on twg.
//   - wgo . (default): read-through with a synchronous fetch on a cold miss.
//   - --refresh (opts.Refresh): bypass the cache and fetch synchronously.
//
// It returns empty strings for non-Jira tickets (GH-<n> GitHub issues) and when
// the status/site is unavailable (no twg / not authenticated). site is only
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

// getIssueJSON is the twg seam of the ticket fetcher; tests replace it.
var getIssueJSON = jira.GetIssueJSONContext

// jiraLoginFn is the twg seam wgo dash asks whether twg is logged in when
// Jira lookups fail; tests replace it.
var jiraLoginFn = jira.LoginStatus

// fetchTicketFacts reads a ticket with every field, so the site's custom
// fields come along under their IDs, and parses its facts with the field
// IDs jiraFieldIDs resolves. Should twg refuse every field, it falls back
// to the system fields by name, leaving the custom fields unknown with a
// note saying so.
func fetchTicketFacts(ticket string) (jiracache.Info, error) {
	return fetchTicketFactsContext(context.Background(), ticket)
}

// fetchTicketFactsContext is fetchTicketFacts, with twg killed when ctx is
// done.
func fetchTicketFactsContext(ctx context.Context, ticket string) (jiracache.Info, error) {
	raw, err := getIssueJSON(ctx, ticket, jira.FactFields)
	partial := false
	var ids jira.FieldIDs
	var notes []string
	switch {
	case err != nil && fieldsRefused(err):
		// Only a refused field list is worth a second call; any other
		// failure (an expired login, a missing ticket) would fail it too.
		debugf("jira %s: twg refused %s: %v", ticket, jira.FactFields, err)
		var ferr error
		if raw, ferr = getIssueJSON(ctx, ticket, jira.FactFieldList); ferr != nil {
			// The retry's error is the one to act on (an expired login, say);
			// the refusal says why there was a retry.
			return jiracache.Info{}, errors.Join(ferr, err)
		}
		partial = true
		notes = append(notes, "twg refused to return every Jira field ('*all'), so story points, sprints and team are unknown; wgo retries soon")
	case err != nil:
		return jiracache.Info{}, err
	default:
		ids, notes = jiraFieldIDs(ctx, ticket, raw)
	}
	facts, factNotes, err := jira.ParseFacts(raw, ids)
	if err != nil {
		return jiracache.Info{}, fmt.Errorf("jira %s: %w", ticket, err)
	}
	info := infoFromFacts(facts)
	info.Notes = append(notes, factNotes...)
	info.Partial = partial
	return info, nil
}

// fieldsRefused reports whether twg rejected the --fields value, as it
// does with "field '…' is not allowed" for fields its search refuses.
func fieldsRefused(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "not allowed") || (strings.Contains(s, "field") && strings.Contains(s, "invalid"))
}

// siteHostTTL bounds how long a process reuses the twg-detected site host.
const siteHostTTL = 15 * time.Minute

// siteHostFn is the twg seam of twgSiteHost; tests replace it.
var siteHostFn = jira.SiteHostContext

var siteHostMemo struct {
	sync.Mutex
	site string
	at   time.Time
}

// twgSiteHost returns the Jira host twg is logged in to, or "" when
// jira.site is configured or twg cannot say. twg
// is asked at most once per siteHostTTL in a process, so a refresh of many
// tickets costs one extra twg call, not one per ticket.
func twgSiteHost(ctx context.Context) string {
	if cfg := config.Get(); cfg != nil && cfg.Jira.Site != "" {
		return cfg.Jira.Site
	}
	siteHostMemo.Lock()
	defer siteHostMemo.Unlock()
	if !siteHostMemo.at.IsZero() && time.Since(siteHostMemo.at) < siteHostTTL {
		return siteHostMemo.site
	}
	site, err := siteHostFn(ctx)
	if err != nil {
		site = ""
	}
	siteHostMemo.site, siteHostMemo.at = site, time.Now()
	return site
}

// fieldIDRe matches a custom field ID.
var fieldIDRe = regexp.MustCompile(`^customfield_[0-9]+$`)

// noField is the [jira.fields] value for a field the site does not have.
const noField = "none"

// jiraFieldIDs resolves the site's custom field IDs: [jira.fields] first,
// then IDs discovered earlier (cached for jiracache.FieldsTTL per site),
// then field update-metadata for the ticket. Notes name fields that stay unknown
// and the [jira.fields] key that would fix it.
var discoverFieldIDs = jira.DiscoverFieldIDsContext

func jiraFieldIDs(ctx context.Context, ticket string, raw []byte) (jira.FieldIDs, []string) {
	cfg := config.Get()
	if cfg == nil {
		// A command that never loaded config would write the entry without
		// [jira.fields]; every command that fetches loads it first, so this
		// only guards a future one.
		_ = config.Init()
		cfg = config.Get()
	}
	var conf config.JiraFieldsConfig
	if cfg != nil {
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
	site := twgSiteHost(ctx)
	cached, fresh := jiracache.ReadFieldsForSite(site, jiracache.FieldsTTL)
	known := jira.FieldIDs{StoryPoints: cached.StoryPoints, Sprint: cached.Sprint, Team: cached.Team, EpicLink: cached.EpicLink}
	if !fresh && ctx.Err() == nil {
		found, err := discoverFieldIDs(ctx, ticket)
		if err != nil {
			debugf("jira %s: field metadata: %v", ticket, err)
			// A sprint value still identifies its field if metadata is
			// unavailable, without guessing the other IDs.
			found, _ = jira.DiscoverFieldIDs(raw)
		}
		for _, name := range jira.FieldNames {
			if known.Get(name) == "" && found.Get(name) != "" {
				known.Set(name, found.Get(name))
			}
		}
		if err == nil {
			err := jiracache.WriteFieldsForSite(site, jiracache.Fields{StoryPoints: known.StoryPoints, Sprint: known.Sprint, Team: known.Team, EpicLink: known.EpicLink})
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
		hint := "twg field metadata did not provide its ID"
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

// twgErrorLine folds an twg error onto one line: its message comes after a
// first line that only says the command failed. It is capped at 300
// characters, for a diagnostic.
func twgErrorLine(s string) string {
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
