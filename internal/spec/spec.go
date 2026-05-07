// Package spec provides spec file parsing, templating, and frontmatter management.
package spec

import (
	"fmt"
	"os"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

// Status represents the lifecycle status of a spec.
type Status string

const (
	StatusDraft      Status = "draft"
	StatusInProgress Status = "in_progress"
	StatusShipped    Status = "shipped"
	StatusAbandoned  Status = "abandoned"
)

// Frontmatter holds the YAML front matter of a spec file.
type Frontmatter struct {
	Ticket    string    `yaml:"ticket"`
	Title     string    `yaml:"title,omitempty"`
	Status    Status    `yaml:"status"`
	Authors   []string  `yaml:"authors"`
	Branches  []string  `yaml:"branches"`
	PRs       []string  `yaml:"prs"`
	Created   time.Time `yaml:"created"`
	Updated   time.Time `yaml:"updated"`
	Phase     int       `yaml:"phase,omitempty"`
	DependsOn []string  `yaml:"depends_on,omitempty"`
}

// SpecFile is a parsed spec document.
type SpecFile struct {
	Path        string
	Frontmatter Frontmatter
	Body        string // full markdown body after frontmatter
}

// Parse reads path from disk and parses it.
func Parse(path string) (*SpecFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read spec %s: %w", path, err)
	}
	sf, err := ParseBytes(data)
	if err != nil {
		return nil, err
	}
	sf.Path = path
	return sf, nil
}

// ParseBytes parses a spec file from raw bytes.
// If the content starts with "---\n", the YAML front matter between the first
// and second "---" delimiters is unmarshalled into Frontmatter; everything
// after is stored in Body. If there is no front matter, Body holds all content.
func ParseBytes(data []byte) (*SpecFile, error) {
	fmYAML, body := splitFrontmatter(string(data))
	sf := &SpecFile{Body: body}
	if fmYAML == "" {
		return sf, nil
	}
	if err := yaml.Unmarshal([]byte(fmYAML), &sf.Frontmatter); err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}
	return sf, nil
}

// splitFrontmatter splits content into (frontmatterYAML, body). If content
// has no "---\n…\n---\n" front matter block, frontmatterYAML is "" and body
// is the full content.
func splitFrontmatter(content string) (frontmatterYAML, body string) {
	if !strings.HasPrefix(content, "---\n") {
		return "", content
	}
	fmYAML, after, found := strings.Cut(content[4:], "\n---\n")
	if !found {
		return "", content
	}
	return fmYAML, after
}
