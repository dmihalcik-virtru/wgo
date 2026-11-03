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
//     use for other repositories' issues
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
// only within repo and become "gh-<n>"; a reference to the PR itself is
// dropped.
func Refs(repo string, number int, title, body string) []string {
	if len(body) > maxRefText {
		body = body[:maxRefText]
	}
	var out []string
	seen := map[string]bool{}
	self := "gh-" + strconv.Itoa(number)
	for _, text := range []string{title, body} {
		for _, m := range refRe.FindAllStringSubmatch(text, -1) {
			var ref string
			switch {
			case m[2] != "":
				if strings.EqualFold(m[1], repo) {
					ref = "gh-" + m[2]
				}
			case m[4] != "":
				if strings.EqualFold(m[3], repo) {
					ref = "gh-" + m[4]
				}
			case m[5] != "":
				ref = "gh-" + m[5]
			case m[6] != "":
				ref = "gh-" + m[6]
			default:
				ref = m[7]
			}
			if ref == "" || ref == self || seen[ref] {
				continue
			}
			if n, ok := strings.CutPrefix(ref, "gh-"); ok {
				// Normalize leading zeros so #012 and gh-12 are one issue.
				v, err := strconv.Atoi(n)
				if err != nil || v <= 0 {
					continue
				}
				if ref = "gh-" + strconv.Itoa(v); ref == self || seen[ref] {
					continue
				}
			}
			seen[ref] = true
			out = append(out, ref)
		}
	}
	return out
}
