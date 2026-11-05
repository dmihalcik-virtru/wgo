package dash

import "strings"

// JiraCurrentUser is the Jira identity of the authenticated user when no
// other is known: the JQL function Jira resolves to whoever acli is
// authenticated as.
const JiraCurrentUser = "currentUser()"

// Person is someone whose PRs and tickets the dash follows (gh-97).
type Person struct {
	// Login is the GitHub login.
	Login string `json:"login"`
	// Jira is a Jira account ID, email or JiraCurrentUser; empty if unknown.
	Jira string `json:"jira,omitempty"`
	// Name is a display name; empty means show Login.
	Name string `json:"name,omitempty"`
	// Me marks the authenticated user, the query macro @me.
	Me bool `json:"me,omitempty"`
}

// ResolvePeople returns the people the dash follows. configured is
// [[dash.people]] in order; when it is empty, pair ([pair]'s teammate) stands
// in, if it has a login. login and jira are the authenticated GitHub login
// and Jira identity, either empty when unknown. When login is known, the
// authenticated user is always included and marked Me: the configured entry
// with that login, or else a new entry placed first. When it is not, nobody
// is Me, and counts that need the user's own data (Counts.Merged) stay
// unknown. The Me entry's Jira is its configured value,
// else jira, else JiraCurrentUser. Logins compare case-insensitively, and an
// entry repeating an earlier login or lacking one is dropped. It returns nil
// when nobody is known. It is pure: callers pass the identity they have
// cached, never one fetched on the way.
func ResolvePeople(configured []Person, pair Person, login, jira string) []Person {
	src := configured
	if len(src) == 0 && pair.Login != "" {
		src = []Person{pair}
	}
	var out []Person
	seen := map[string]bool{}
	me := -1
	for _, p := range src {
		key := strings.ToLower(p.Login)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		p.Me = false
		if login != "" && strings.EqualFold(p.Login, login) {
			me = len(out)
		}
		out = append(out, p)
	}
	if login == "" {
		return out
	}
	if me < 0 {
		out = append([]Person{{Login: login}}, out...)
		me = 0
	}
	out[me].Me = true
	if out[me].Jira == "" {
		out[me].Jira = jira
	}
	if out[me].Jira == "" {
		out[me].Jira = JiraCurrentUser
	}
	return out
}
