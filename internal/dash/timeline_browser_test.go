package dash

import "testing"

// The PR timeline view's browser suite (gh-104, live_browser.js) drives the
// analysis fixture as it is: its PRs with and without a cached history,
// waits on alice (@me) directly and through team:web.
func init() {
	browserSuites = append(browserSuites, browserSuite{flag: "timeline", options: func(t testing.TB) HandlerOptions {
		v, err := LoadFixture(analysisFixturePath)
		if err != nil {
			t.Fatal(err)
		}
		return HandlerOptions{Source: StaticSource{V: v}}
	}})
}
