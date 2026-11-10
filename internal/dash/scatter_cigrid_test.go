package dash

import "testing"

// The scatterplot and CI grid's browser suite (gh-103) drives the analysis
// fixture as it is: acme/web#208 (whose move is unknown), the check failing
// everywhere in acme/api and not in acme/web.
func init() {
	browserSuites = append(browserSuites, browserSuite{flag: "scatter-ci", options: func(t testing.TB) HandlerOptions {
		v, err := LoadFixture(analysisFixturePath)
		if err != nil {
			t.Fatal(err)
		}
		return HandlerOptions{Source: StaticSource{V: v}}
	}})
}
