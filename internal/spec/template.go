package spec

import (
	_ "embed"
	"fmt"
	"strconv"
	"strings"
	"text/template"
	"time"

	"go.yaml.in/yaml/v3"
)

//go:embed default_template.md
var defaultTemplate string

type TemplateData struct {
	Ticket      string
	Title       string
	Description string
	Authors     []string
	Branches    []string
	Now         time.Time
}

func RenderTemplate(d TemplateData) ([]byte, error) {
	tmpl, err := template.New("spec").Funcs(template.FuncMap{"yaml": yamlScalar}).Parse(defaultTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, d); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}

	return []byte(buf.String()), nil
}

// yamlScalar renders s as an inline YAML scalar, quoting it only when needed.
// Titles come from the CLI or Jira, so a value like "web-sdk: range decrypt"
// would otherwise be read as a nested mapping and break the frontmatter.
func yamlScalar(s string) (string, error) {
	out, err := yaml.Marshal(s)
	if err != nil {
		return "", err
	}
	q := strings.TrimSuffix(string(out), "\n")
	if strings.Contains(q, "\n") {
		// Multi-line values marshal as block scalars, which cannot sit inline.
		return strconv.Quote(s), nil
	}
	return q, nil
}
