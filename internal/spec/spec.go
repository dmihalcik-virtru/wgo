package spec

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
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
	Branches  []string  `yaml:"branches"` // "owner/repo:branch"
	PRs       []string  `yaml:"prs"`      // "owner/repo#123"
	Created   time.Time `yaml:"created"`
	Updated   time.Time `yaml:"updated"`
	Phase     int       `yaml:"phase,omitempty"`
	DependsOn []string  `yaml:"depends_on,omitempty"`
}

type SpecFile struct {
	Path        string
	Frontmatter Frontmatter
	Sections    map[string]string
	Body        string
}

func Parse(path string) (*SpecFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	spec, err := ParseBytes(data)
	if err != nil {
		return nil, err
	}
	spec.Path = path
	return spec, nil
}

func ParseBytes(data []byte) (*SpecFile, error) {
	if !bytes.HasPrefix(data, []byte("---\n")) {
		return &SpecFile{Body: string(data)}, nil
	}

	parts := bytes.SplitN(data, []byte("---\n"), 3)
	if len(parts) < 3 {
		return &SpecFile{Body: string(data)}, nil
	}

	var fm Frontmatter
	if err := yaml.Unmarshal(parts[1], &fm); err != nil {
		return nil, fmt.Errorf("unmarshal frontmatter: %w", err)
	}

	spec := &SpecFile{
		Frontmatter: fm,
		Body:        string(parts[2]),
		Sections:    make(map[string]string),
	}

	spec.parseSections()
	return spec, nil
}

func (s *SpecFile) parseSections() {
	lines := strings.Split(s.Body, "\n")
	var currentSection string
	var sectionBody []string

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			if currentSection != "" {
				s.Sections[currentSection] = strings.TrimSpace(strings.Join(sectionBody, "\n"))
			}
			currentSection = strings.TrimPrefix(line, "## ")
			sectionBody = nil
		} else if currentSection != "" {
			sectionBody = append(sectionBody, line)
		}
	}
	if currentSection != "" {
		s.Sections[currentSection] = strings.TrimSpace(strings.Join(sectionBody, "\n"))
	}
}

func UpdateFrontmatter(path string, fn func(*Frontmatter) error) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	if !bytes.HasPrefix(data, []byte("---\n")) {
		return errors.New("no frontmatter found")
	}

	parts := bytes.SplitN(data, []byte("---\n"), 3)
	if len(parts) < 3 {
		return errors.New("invalid spec file format")
	}

	var fm Frontmatter
	if err := yaml.Unmarshal(parts[1], &fm); err != nil {
		return fmt.Errorf("unmarshal frontmatter: %w", err)
	}

	if err := fn(&fm); err != nil {
		return err
	}

	fm.Updated = time.Now()
	newFm, err := yaml.Marshal(fm)
	if err != nil {
		return fmt.Errorf("marshal frontmatter: %w", err)
	}

	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(newFm)
	buf.WriteString("---\n")
	buf.Write(parts[2])

	return os.WriteFile(path, buf.Bytes(), 0644)
}
