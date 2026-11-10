package dash

import "testing"

// The sprint view's browser suite (gh-105, live_browser.js): the analysis
// fixture as it is (--sprint), and the same snapshot without [jira] board
// (--sprint-noboard), whose explanation the suite checks too.
func init() {
	browserSuites = append(browserSuites,
		browserSuite{flag: "sprint", options: func(t testing.TB) HandlerOptions {
			return HandlerOptions{Source: StaticSource{V: sprintView(t, nil)}, Filters: MergeFilters(nil, Fields())}
		}},
		browserSuite{flag: "sprint-noboard", options: func(t testing.TB) HandlerOptions {
			return HandlerOptions{Source: StaticSource{V: sprintView(t, func(s *Snapshot) {
				var nodes []Node
				for _, n := range s.Nodes {
					if n.Kind != KindSprint {
						nodes = append(nodes, n)
					}
				}
				s.Nodes = nodes
				s.Sources[SourceJiraSprints] = SourceStatus{State: Unknown, Detail: "no [jira] board is configured"}
			})}}
		}})
}
