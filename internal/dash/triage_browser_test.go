package dash

import "testing"

// The triage view's browser suite (gh-133) drives the analysis fixture as it
// is, with the default rules.
func init() {
	browserSuites = append(browserSuites, browserSuite{flag: "triage", options: func(t testing.TB) HandlerOptions {
		v, err := LoadFixture(analysisFixturePath)
		if err != nil {
			t.Fatal(err)
		}
		return HandlerOptions{Source: StaticSource{V: v}, Filters: MergeFilters(nil, Fields())}
	}})
}
