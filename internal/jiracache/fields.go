package jiracache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/virtru/wgo/internal/atomicfile"
	"github.com/virtru/wgo/internal/store"
)

// FieldsTTL is how long discovered field IDs are reused before discovery
// runs again: a site's field IDs practically never change.
const FieldsTTL = 7 * 24 * time.Hour

// Fields are a site's custom field IDs found by discovery, keyed like
// [jira.fields]. An empty ID was not found.
type Fields struct {
	StoryPoints string `json:"story_points,omitempty"`
	Sprint      string `json:"sprint,omitempty"`
	Team        string `json:"team,omitempty"`
	EpicLink    string `json:"epic_link,omitempty"`
}

type fieldsEntry struct {
	Fields       Fields    `json:"fields"`
	DiscoveredAt time.Time `json:"discovered_at"`
}

// ReadFields returns the discovered field IDs, and whether they are younger
// than ttl. It never makes a network call.
func ReadFields(ttl time.Duration) (Fields, bool) {
	path, err := fieldsPath()
	if err != nil {
		return Fields{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logf("jira cache: read %s: %v", path, err)
		}
		return Fields{}, false
	}
	var e fieldsEntry
	if err := json.Unmarshal(data, &e); err != nil {
		logf("jira cache: corrupt entry %s: %v", path, err)
		return Fields{}, false
	}
	if time.Since(e.DiscoveredAt) >= ttl {
		return Fields{}, false
	}
	return e.Fields, true
}

// WriteFields stores discovered field IDs, stamped now, atomically.
func WriteFields(f Fields) error {
	path, err := fieldsPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(fieldsEntry{Fields: f, DiscoveredAt: time.Now()}, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, data, 0o644)
}

// fieldsPath is ~/.wgo/cache/jira/fields.json. No ticket key can collide
// with it: keys carry a dash and digits.
func fieldsPath() (string, error) {
	s, err := store.New()
	if err != nil {
		return "", err
	}
	return filepath.Join(s.BaseDir(), "cache", "jira", "fields.json"), nil
}
