package spec

import (
	"bytes"
	_ "embed"
	"fmt"
	"text/template"
	"time"
)

//go:embed default_template.md
var defaultTemplateSource string

// TemplateData holds the values injected into the default spec template.
type TemplateData struct {
	Ticket      string
	Title       string
	Description string
	Authors     []string
	Branches    []string // "owner/repo:branch"
	Now         time.Time
}

// RenderTemplate executes the embedded default_template.md with d.
func RenderTemplate(d TemplateData) ([]byte, error) {
	tmpl, err := template.New("spec").Parse(defaultTemplateSource)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, d); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}
	return buf.Bytes(), nil
}
