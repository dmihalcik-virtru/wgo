package spec

import (
	"fmt"
	"os"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Status string

const (
	StatusDraft      Status = "draft"
	StatusInProgress Status = "in_progress"
	StatusShipped    Status = "shipped"
	StatusAbandoned  Status = "abandoned"
)

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

type SpecFile struct {
	Path        string
	Frontmatter Frontmatter
	Body        string
}

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

func ParseBytes(data []byte) (*SpecFile, error) {
	sf := &SpecFile{}

	content := string(data)
	parts := strings.SplitN(content, "---", 3)

	if len(parts) < 3 || strings.TrimSpace(parts[0]) != "" {
		sf.Body = content
		return sf, nil
	}

	yamlContent := parts[1]
	sf.Body = parts[2]

	var fm Frontmatter
	if err := yaml.Unmarshal([]byte(yamlContent), &fm); err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}
	sf.Frontmatter = fm
	return sf, nil
}
