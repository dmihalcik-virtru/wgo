// Package spec provides parsing, templating, and locating for spec files.
//
// A spec file is a markdown document with optional YAML frontmatter that
// describes a single work item (typically a Jira ticket). It lives at
// spec/<TICKET>.md inside a repository.
package spec

import (
	"bytes"
	"fmt"
	"os"
	"time"

	"go.yaml.in/yaml/v3"
)

// Status is the lifecycle state of a spec.
type Status string

const (
	StatusDraft      Status = "draft"
	StatusInProgress Status = "in_progress"
	StatusShipped    Status = "shipped"
	StatusAbandoned  Status = "abandoned"
)

// Date is a time.Time that round-trips through YAML as YYYY-MM-DD.
type Date struct {
	time.Time
}

// MarshalYAML emits the date as YYYY-MM-DD.
func (d Date) MarshalYAML() (any, error) {
	if d.Time.IsZero() {
		return "", nil
	}
	return d.Time.Format("2006-01-02"), nil
}

// UnmarshalYAML parses YYYY-MM-DD, falling back to RFC3339 for tolerance.
func (d *Date) UnmarshalYAML(value *yaml.Node) error {
	if value.Value == "" {
		return nil
	}
	t, err := time.Parse("2006-01-02", value.Value)
	if err != nil {
		t, err = time.Parse(time.RFC3339, value.Value)
		if err != nil {
			return fmt.Errorf("cannot parse date %q: %w", value.Value, err)
		}
	}
	d.Time = t
	return nil
}

// Frontmatter is the structured header at the top of a spec file.
type Frontmatter struct {
	Ticket    string   `yaml:"ticket"`
	Title     string   `yaml:"title,omitempty"`
	Status    Status   `yaml:"status"`
	Authors   []string `yaml:"authors"`
	Branches  []string `yaml:"branches"`
	PRs       []string `yaml:"prs"`
	Created   Date     `yaml:"created"`
	Updated   Date     `yaml:"updated"`
	Phase     int      `yaml:"phase,omitempty"`
	Estimate  string   `yaml:"estimate,omitempty"`
	DependsOn []string `yaml:"depends_on,omitempty"`
}

// SpecFile is a parsed spec file.
type SpecFile struct {
	Path        string      // absolute path; empty when parsed from bytes
	Frontmatter Frontmatter // zero value when no frontmatter present
	Body        string      // markdown body after the closing ---
}

// ParseBytes parses a spec from raw bytes. A file without frontmatter is
// valid: Frontmatter is the zero value and the whole input is in Body.
func ParseBytes(data []byte) (*SpecFile, error) {
	sf := &SpecFile{}
	fmBytes, bodyBytes, ok := splitFrontmatter(data)
	if !ok {
		sf.Body = string(data)
		return sf, nil
	}
	sf.Body = bodyStr(bodyBytes)
	if err := yaml.Unmarshal(fmBytes, &sf.Frontmatter); err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}
	return sf, nil
}

// Parse reads path from disk and parses it.
func Parse(path string) (*SpecFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sf, err := ParseBytes(data)
	if err != nil {
		return nil, err
	}
	sf.Path = path
	return sf, nil
}

// splitFrontmatter returns (frontmatter bytes, body bytes including any
// leading newline, true) when data begins with `---\n` and contains a
// closing `\n---`. Otherwise returns (nil, original data, false).
func splitFrontmatter(data []byte) ([]byte, []byte, bool) {
	if !bytes.HasPrefix(data, []byte("---\n")) {
		return nil, data, false
	}
	rest := data[4:]
	end := bytes.Index(rest, []byte("\n---"))
	if end == -1 {
		return nil, data, false
	}
	return rest[:end], rest[end+4:], true
}

// bodyStr trims a single leading newline from the body bytes that follow
// the closing `---` delimiter.
func bodyStr(b []byte) string {
	if len(b) > 0 && b[0] == '\n' {
		return string(b[1:])
	}
	return string(b)
}
