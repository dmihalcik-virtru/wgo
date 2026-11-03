package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/virtru/wgo/internal/atomicfile"
	"github.com/virtru/wgo/internal/github"
)

// IdentityTTL is how long the cached login of the authenticated GitHub user
// is trusted. A stale login is still served: it changes only when the token
// does.
const IdentityTTL = 24 * time.Hour

// identityFile is the cached login, beside the per-person directories (a
// login cannot contain a dot, so it never collides with one).
const identityFile = "identity.json"

type identityEntry struct {
	Login         string    `json:"login,omitempty"`
	FetchedAt     time.Time `json:"fetched_at,omitzero"`
	LastAttemptAt time.Time `json:"last_attempt_at,omitzero"`
	LastError     string    `json:"last_error,omitempty"`
}

func identityPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, identityFile), nil
}

// ReadIdentity returns the cached login of the authenticated GitHub user
// and its freshness at now: "" and Miss when it was never fetched. It never
// makes a network call or starts a process, so it can back
// dash.Config.Identity.
func ReadIdentity(ttl time.Duration, now time.Time) (string, State) {
	path, err := identityPath()
	if err != nil {
		return "", Miss
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			logf("inventory cache: read identity: %v", err)
		}
		return "", Miss
	}
	var e identityEntry
	if err := json.Unmarshal(data, &e); err != nil {
		logf("inventory cache: corrupt identity: %v", err)
		return "", Miss
	}
	switch {
	case e.Login == "" || e.FetchedAt.IsZero():
		return "", Miss
	case now.Sub(e.FetchedAt) >= ttl:
		return e.Login, Stale
	}
	return e.Login, Fresh
}

// RefreshIdentity asks GitHub for the authenticated user's login and caches
// it. A failure is recorded beside the previous login, which is kept; a
// missing token or a cancelled ctx is not recorded.
func RefreshIdentity(ctx context.Context, f Fetcher) (string, error) {
	if f == nil {
		return "", errors.New("inventory: no fetcher")
	}
	path, err := identityPath()
	if err != nil {
		return "", err
	}
	login, err := f.Viewer(ctx)
	now := time.Now()
	writeMu.Lock()
	defer writeMu.Unlock()
	var e identityEntry
	if data, rerr := os.ReadFile(path); rerr == nil {
		_ = json.Unmarshal(data, &e)
	}
	if err != nil {
		if errors.Is(err, github.ErrNoAuth) || ctx.Err() != nil {
			return e.Login, err
		}
		e.LastAttemptAt, e.LastError = now, err.Error()
	} else {
		e = identityEntry{Login: login, FetchedAt: now, LastAttemptAt: now}
	}
	data, merr := json.Marshal(&e)
	if merr == nil {
		merr = atomicfile.Write(path, data, 0o644)
	}
	if merr != nil {
		logf("inventory cache: write identity: %v", merr)
		err = errors.Join(err, fmt.Errorf("inventory cache write: %w", merr))
	}
	return e.Login, err
}

// LockIdentityRefresh takes the identity's refresh lease, like LockRefresh.
func LockIdentityRefresh(window time.Duration) bool {
	path, err := identityPath()
	if err != nil {
		logf("inventory cache: identity lease: %v", err)
		return false
	}
	return acquireLease(path[:len(path)-len(".json")]+".lock", window, "identity")
}
