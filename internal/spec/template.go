package spec

import (
	"bytes"
	_ "embed"
	"strings"
	"text/template"
	"time"

	"go.yaml.in/yaml/v3"
)

// TemplateData is the input for the default spec scaffold template.
type TemplateData struct {
	Ticket, Title, Description string
	Authors                    []string
	Branches                   []string
	Now                        time.Time
}

//go:embed default_template.md
var defaultTemplate string

var scaffoldTemplate = template.Must(template.New("default_template.md").Funcs(template.FuncMap{
	"flowList": flowList,
	"scalar":   yamlScalar,
}).Parse(defaultTemplate))

// RenderTemplate renders the default spec scaffold.
func RenderTemplate(d TemplateData) ([]byte, error) {
	var buf bytes.Buffer
	if err := scaffoldTemplate.Execute(&buf, d); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func flowList(values []string) string {
	rendered := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		rendered = append(rendered, yamlScalar(value))
	}
	return strings.Join(rendered, ", ")
}

func yamlScalar(value string) string {
	data, err := yaml.Marshal(value)
	if err != nil {
		return `""`
	}
	return strings.TrimSpace(string(data))
}
