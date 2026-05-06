package spec

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

var frontmatterBlockRe = regexp.MustCompile(`(?s)^---\r?\n(.*?)\r?\n---`)

type frontmatterYAML struct {
	Ticket    string   `yaml:"ticket"`
	Title     string   `yaml:"title,omitempty"`
	Status    string   `yaml:"status"`
	Authors   []string `yaml:"authors"`
	Branches  []string `yaml:"branches"`
	PRs       []string `yaml:"prs"`
	Created   string   `yaml:"created"`
	Updated   string   `yaml:"updated"`
	Phase     int      `yaml:"phase,omitempty"`
	DependsOn []string `yaml:"depends_on,omitempty"`
}

func splitFrontmatter(data []byte) ([]byte, []byte, error) {
	matches := frontmatterBlockRe.FindSubmatchIndex(data)
	if len(matches) < 4 || matches[0] != 0 {
		return nil, nil, errors.New("missing frontmatter")
	}

	return data[matches[2]:matches[3]], data[matches[1]:], nil
}

func parseFrontmatter(data []byte) (Frontmatter, error) {
	var raw frontmatterYAML
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Frontmatter{}, err
	}
	return raw.toFrontmatter()
}

func (f frontmatterYAML) toFrontmatter() (Frontmatter, error) {
	created, err := parseFrontmatterDate(f.Created)
	if err != nil {
		return Frontmatter{}, fmt.Errorf("created: %w", err)
	}

	updated, err := parseFrontmatterDate(f.Updated)
	if err != nil {
		return Frontmatter{}, fmt.Errorf("updated: %w", err)
	}

	return Frontmatter{
		Ticket:    f.Ticket,
		Title:     f.Title,
		Status:    Status(f.Status),
		Authors:   append([]string(nil), f.Authors...),
		Branches:  append([]string(nil), f.Branches...),
		PRs:       append([]string(nil), f.PRs...),
		Created:   created,
		Updated:   updated,
		Phase:     f.Phase,
		DependsOn: append([]string(nil), f.DependsOn...),
	}, nil
}

func parseFrontmatterDate(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}

	for _, layout := range []string{time.DateOnly, time.RFC3339, "2006-01-02 15:04:05 -0700"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed, nil
		}
	}

	return time.Time{}, fmt.Errorf("unsupported date %q", value)
}

// UpdateFrontmatter updates a spec's frontmatter while preserving the body bytes.
func UpdateFrontmatter(path string, fn func(*Frontmatter) error) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	frontmatterData, body, err := splitFrontmatter(data)
	if err != nil {
		return err
	}

	var root yaml.Node
	if err := yaml.Unmarshal(frontmatterData, &root); err != nil {
		return fmt.Errorf("unmarshal frontmatter: %w", err)
	}

	fm, err := parseFrontmatter(frontmatterData)
	if err != nil {
		return err
	}

	if err := fn(&fm); err != nil {
		return err
	}

	if err := applyFrontmatter(&root, fm); err != nil {
		return err
	}

	renderedFrontmatter, err := yaml.Marshal(&root)
	if err != nil {
		return fmt.Errorf("marshal frontmatter: %w", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	output := append([]byte("---\n"), renderedFrontmatter...)
	output = append(output, []byte("---")...)
	output = append(output, body...)
	return os.WriteFile(path, output, info.Mode())
}

func applyFrontmatter(root *yaml.Node, fm Frontmatter) error {
	if root.Kind == 0 {
		root.Kind = yaml.DocumentNode
	}

	var mapping *yaml.Node
	if len(root.Content) == 0 {
		mapping = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = []*yaml.Node{mapping}
	} else {
		mapping = root.Content[0]
		if mapping.Kind != yaml.MappingNode {
			return fmt.Errorf("frontmatter is not a mapping")
		}
	}

	setMappingString(mapping, "ticket", fm.Ticket, false)
	setMappingString(mapping, "title", fm.Title, true)
	setMappingString(mapping, "status", string(fm.Status), false)
	setMappingStringSlice(mapping, "authors", fm.Authors, false)
	setMappingStringSlice(mapping, "branches", fm.Branches, false)
	setMappingStringSlice(mapping, "prs", fm.PRs, false)
	setMappingString(mapping, "created", formatFrontmatterDate(fm.Created), false)
	setMappingString(mapping, "updated", formatFrontmatterDate(fm.Updated), false)
	setMappingInt(mapping, "phase", fm.Phase, true)
	setMappingStringSlice(mapping, "depends_on", fm.DependsOn, true)
	return nil
}

func formatFrontmatterDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.DateOnly)
}

func setMappingString(mapping *yaml.Node, key, value string, omitEmpty bool) {
	value = strings.TrimSpace(value)
	if omitEmpty && value == "" {
		removeMappingKey(mapping, key)
		return
	}

	node := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
	setMappingValue(mapping, key, node)
}

func setMappingInt(mapping *yaml.Node, key string, value int, omitEmpty bool) {
	if omitEmpty && value == 0 {
		removeMappingKey(mapping, key)
		return
	}

	node := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprintf("%d", value)}
	setMappingValue(mapping, key, node)
}

func setMappingStringSlice(mapping *yaml.Node, key string, values []string, omitEmpty bool) {
	if omitEmpty && len(values) == 0 {
		removeMappingKey(mapping, key)
		return
	}

	node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, value := range values {
		node.Content = append(node.Content, &yaml.Node{
			Kind:  yaml.ScalarNode,
			Tag:   "!!str",
			Value: value,
		})
	}
	setMappingValue(mapping, key, node)
}

func setMappingValue(mapping *yaml.Node, key string, value *yaml.Node) {
	if idx := mappingKeyIndex(mapping, key); idx >= 0 {
		mapping.Content[idx+1] = value
		return
	}

	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value,
	)
}

func removeMappingKey(mapping *yaml.Node, key string) {
	if idx := mappingKeyIndex(mapping, key); idx >= 0 {
		mapping.Content = append(mapping.Content[:idx], mapping.Content[idx+2:]...)
	}
}

func mappingKeyIndex(mapping *yaml.Node, key string) int {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return i
		}
	}
	return -1
}
