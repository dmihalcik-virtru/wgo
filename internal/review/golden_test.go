package review

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// updateGolden rewrites the golden files instead of comparing against them:
//
//	go test ./internal/review -run TestGolden -update
var updateGolden = flag.Bool("update", false, "rewrite review golden files")

// TestGoldenGraphJSONAndHTML pins the `wgo review graph` JSON schema and the
// offline HTML explorer for a small fixture run, so changes to shared explorer
// code (the gh-70 dashboard reuses it) cannot silently alter either output.
// The JSON is encoded exactly as runReviewGraph encodes it.
func TestGoldenGraphJSONAndHTML(t *testing.T) {
	run, err := Load(writeRun(t))
	require.NoError(t, err)
	g := Build(run)

	var js bytes.Buffer
	enc := json.NewEncoder(&js)
	enc.SetIndent("", " ")
	require.NoError(t, enc.Encode(g))

	page, err := Render(g)
	require.NoError(t, err)

	checkGolden(t, filepath.Join("testdata", "graph.golden.json"), js.Bytes())
	checkGolden(t, filepath.Join("testdata", "graph.golden.html"), page)
}

func checkGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *updateGolden {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, got, 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "missing golden; run go test ./internal/review -run TestGolden -update")
	if !bytes.Equal(want, got) {
		t.Fatalf("%s changed (%d bytes, want %d); if intended, rerun with -update and review the diff", path, len(got), len(want))
	}
}
