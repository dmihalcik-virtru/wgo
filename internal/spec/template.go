package spec

import (
	"bytes"
	_ "embed"
	"text/template"
	"time"
)

//go:embed default_template.md
var defaultTemplate string

type TemplateData struct {
	Ticket, Title, Description string
	Authors                    []string
	Branches                   []string
	Now                        time.Time
}

func RenderTemplate(d TemplateData) ([]byte, error) {
	if d.Now.IsZero() {
		d.Now = time.Now()
	}
	tmpl, err := template.New("spec").Parse(defaultTemplate)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, d); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
