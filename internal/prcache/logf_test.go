package prcache

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// breakCache makes the cache directory unwritable by putting a file where the
// "cache" directory is needed.
func breakCache(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	base := filepath.Join(home, ".wgo")
	require.NoError(t, os.MkdirAll(base, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(base, "cache"), []byte("x"), 0o644))
}

func captureLogf(t *testing.T) *[]string {
	t.Helper()
	var msgs []string
	old := Logf
	Logf = func(format string, args ...any) { msgs = append(msgs, fmt.Sprintf(format, args...)) }
	t.Cleanup(func() { Logf = old })
	return &msgs
}

func TestFetchAndStoreLogsWriteFailure(t *testing.T) {
	breakCache(t)
	msgs := captureLogf(t)
	f := &countingFetcher{refs: sampleRefs()}
	res := Resolve(f, testRemote, testRepo, "feature-x", Opts{Synchronous: true})
	require.NoError(t, res.Err)
	require.Error(t, res.WriteErr)
	require.Len(t, *msgs, 1)
	require.Contains(t, (*msgs)[0], "pr cache: write")
}

func TestFetchAndStoreLogsWriteFailureOfFailedFetch(t *testing.T) {
	breakCache(t)
	msgs := captureLogf(t)
	f := &countingFetcher{err: errors.New("boom")}
	res := Resolve(f, testRemote, testRepo, "feature-x", Opts{Synchronous: true})
	require.Error(t, res.Err)
	require.Error(t, res.WriteErr)
	require.Len(t, *msgs, 1)
	require.True(t, strings.Contains((*msgs)[0], "record failure"), (*msgs)[0])
}
