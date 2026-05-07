package spec

import (
	"fmt"
	"os"

	yaml "go.yaml.in/yaml/v3"
)

// UpdateFrontmatter reads path, calls fn on the parsed Frontmatter, then writes
// the file back with the updated frontmatter and the body bytes unchanged.
func UpdateFrontmatter(path string, fn func(*Frontmatter) error) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	fmYAML, body := splitFrontmatter(string(data))
	var fm Frontmatter
	if fmYAML != "" {
		if err := yaml.Unmarshal([]byte(fmYAML), &fm); err != nil {
			return fmt.Errorf("parse frontmatter: %w", err)
		}
	}

	if err := fn(&fm); err != nil {
		return err
	}

	fmOut, err := yaml.Marshal(&fm)
	if err != nil {
		return fmt.Errorf("marshal frontmatter: %w", err)
	}

	result := "---\n" + string(fmOut) + "---\n" + body
	return os.WriteFile(path, []byte(result), 0o644)
}
