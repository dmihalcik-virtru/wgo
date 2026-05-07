package spec

import (
	_ "embed"
	"fmt"
	"strings"
	"text/template"
	"time"
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
	tmpl, err := template.New("spec").Parse(defaultTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, d); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}

	return []byte(buf.String()), nil
}
