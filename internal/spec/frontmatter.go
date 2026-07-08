package spec

import (
	"fmt"
	"os"

	"go.yaml.in/yaml/v3"
)

func UpdateFrontmatter(path string, fn func(*Frontmatter) error) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	sf, err := ParseBytes(data)
	if err != nil {
		return fmt.Errorf("parse spec: %w", err)
	}

	if err := fn(&sf.Frontmatter); err != nil {
		return err
	}

	yamlBytes, err := yaml.Marshal(&sf.Frontmatter)
	if err != nil {
		return fmt.Errorf("marshal frontmatter: %w", err)
	}

	newContent := "---\n" + string(yamlBytes) + "---\n" + sf.Body

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(newContent), 0o644); err != nil {
		return fmt.Errorf("write tmp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename file: %w", err)
	}

	return nil
}
