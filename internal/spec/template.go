package spec

import (
	"bytes"
	_ "embed"
	"fmt"
	"text/template"
	"time"
)

//go:embed default_template.md
var defaultTemplate string

// TemplateData is the data passed to the spec template.
type TemplateData struct {
	Ticket      string
	Title       string
	Description string
	Authors     []string
	Branches    []string
	Now         time.Time
}

// RenderTemplate renders the default spec template with the provided data.
func RenderTemplate(d TemplateData) ([]byte, error) {
	t, err := template.New("spec").Parse(defaultTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse spec template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, d); err != nil {
		return nil, fmt.Errorf("render spec template: %w", err)
	}
	return buf.Bytes(), nil
}
