package inventory

import (
	"regexp"
	"strconv"
	"strings"
)

// maxRefText bounds how much of a PR body Refs scans.
const maxRefText = 64 << 10

// refRe finds ticket references, leftmost first. The alternatives, tried in
// this order at each position:
//  1. an issue URL: https://github.com/<owner>/<repo>/issues/<n>
//  2. an issue in owner/repo#<n> form
//  3. gh-<n>, any case
//  4. #<n> on its own: not part of a word, a path or an HTML entity, and not
//     the text of a link (">#12</a>", "[#12](...)"), which dependency bots
//     use for other repositories' issues; Refs marks it bare
//  5. a Jira-shaped key: [A-Z]+-<digits>
var refRe = regexp.MustCompile(`(?i:https://github\.com/([\w.-]+/[\w.-]+)/issues/(\d+))` +
	`|([\w.-]+/[\w.-]+)#(\d+)\b` +
	`|(?i:\bgh-(\d+))\b` +
	`|(?:^|[^\w/&#>\[])#(\d+)\b` +
	`|\b([A-Z]+-\d+)\b`)

// Refs returns the ticket references in title, then body, of PR number in
// repository repo (owner/repo), ordered and deduplicated. Jira-shaped keys
// are kept as written, whatever their prefix: SHA-256 and TEAM-123 alike,
// for the reader to filter by known prefixes. GitHub issue references count
// only within repo: "gh-<n>" when written out (gh-12, owner/repo#12, an
// issue URL), "#<n>" for a bare #12, which in other people's PRs often names
// no issue of repo, for the reader to keep or not. A number written both
// ways is kept once, as "gh-<n>", where it first appears. A reference to the
// PR itself is dropped.
func Refs(repo string, number int, title, body string) []string {
	if len(body) > maxRefText {
		body = body[:maxRefText]
	}
	var out []string
	seen := map[string]bool{}
	// at holds the index in out of each issue number's reference.
	at := map[int]int{}
	for _, text := range []string{title, body} {
		for _, m := range refRe.FindAllStringSubmatch(text, -1) {
			num, bare := "", false
			switch {
			case m[2] != "":
				if strings.EqualFold(m[1], repo) {
					num = m[2]
				}
			case m[4] != "":
				if strings.EqualFold(m[3], repo) {
					num = m[4]
				}
			case m[5] != "":
				num = m[5]
			case m[6] != "":
				num, bare = m[6], true
			default:
				if ref := m[7]; !seen[ref] {
					seen[ref] = true
					out = append(out, ref)
				}
				continue
			}
			// Atoi also drops leading zeros, so #012 and gh-12 are one
			// issue.
			v, err := strconv.Atoi(num)
			if err != nil || v <= 0 || v == number {
				continue
			}
			ref := "gh-" + strconv.Itoa(v)
			if bare {
				ref = "#" + strconv.Itoa(v)
			}
			if i, ok := at[v]; ok {
				if !bare {
					out[i] = ref // written out after all
				}
				continue
			}
			at[v] = len(out)
			out = append(out, ref)
		}
	}
	return out
}
