package spec

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Status represents the lifecycle state of a spec.
type Status string

const (
	StatusDraft      Status = "draft"
	StatusInProgress Status = "in_progress"
	StatusShipped    Status = "shipped"
	StatusAbandoned  Status = "abandoned"
)

// Frontmatter contains the managed metadata for a spec.
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

// SpecFile is the parsed representation of a spec document.
type SpecFile struct {
	Path        string
	Frontmatter Frontmatter
	Sections    map[string]string
	Body        string
}

// Parse parses a spec file from disk.
func Parse(path string) (*SpecFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	sf, err := ParseBytes(data)
	if err != nil {
		return nil, fmt.Errorf("parse spec: %w", err)
	}

	absPath, err := filepath.Abs(path)
	if err == nil {
		sf.Path = absPath
	} else {
		sf.Path = path
	}

	return sf, nil
}

// ParseBytes parses a spec document from raw bytes.
func ParseBytes(data []byte) (*SpecFile, error) {
	frontmatterData, body, err := splitFrontmatter(data)
	if err != nil {
		return nil, err
	}

	fm, err := parseFrontmatter(frontmatterData)
	if err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}

	bodyText := string(body)
	return &SpecFile{
		Frontmatter: fm,
		Sections:    parseSections(bodyText),
		Body:        bodyText,
	}, nil
}

func parseSections(body string) map[string]string {
	normalized := strings.ReplaceAll(body, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	sections := make(map[string]string)

	var current string
	var sectionLines []string
	flush := func() {
		if current == "" {
			return
		}
		sections[current] = strings.TrimSpace(strings.Join(sectionLines, "\n"))
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			flush()
			current = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			sectionLines = nil
			continue
		}
		if current != "" {
			sectionLines = append(sectionLines, line)
		}
	}

	flush()
	return sections
}
