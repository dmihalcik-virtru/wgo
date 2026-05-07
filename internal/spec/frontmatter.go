package spec

import (
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

// UpdateFrontmatter reads the spec at path, calls fn to mutate the parsed
// Frontmatter, then re-serializes only the frontmatter block. The body
// after the closing `---` is preserved byte-for-byte. Writes are atomic
// via .tmp + rename.
func UpdateFrontmatter(path string, fn func(*Frontmatter) error) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	fmBytes, bodyBytes, ok := splitFrontmatter(data)
	if !ok {
		return fmt.Errorf("no frontmatter in %s", path)
	}
	var fm Frontmatter
	if err := yaml.Unmarshal(fmBytes, &fm); err != nil {
		return fmt.Errorf("unmarshal frontmatter: %w", err)
	}
	if err := fn(&fm); err != nil {
		return err
	}
	newFM, err := yaml.Marshal(&fm)
	if err != nil {
		return fmt.Errorf("marshal frontmatter: %w", err)
	}
	result := "---\n" + strings.TrimRight(string(newFM), "\n") + "\n---"
	if len(bodyBytes) > 0 {
		// Preserve the leading newline that originally separated `---` from
		// the body, plus the rest of the body bytes verbatim.
		result += string(bodyBytes)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(result), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
