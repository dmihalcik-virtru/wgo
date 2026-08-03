package claudemd

import (
	"strings"
	"testing"
)

func TestRenderTemplate(t *testing.T) {
	tests := []struct {
		name       string
		data       TemplateData
		wantAll    []string
		wantNotAny []string
	}{
		{
			name: "with spec path",
			data: TemplateData{
				Ticket:      "DSPX-2674",
				Description: "remove volume directive",
				SpecPath:    "platform/spec/DSPX-2674.md",
				Repos: []RepoEntry{
					{Dir: "platform", Label: "virtru/platform"},
					{Dir: "cli", Label: "virtru/cli"},
				},
			},
			wantAll: []string{
				"# DSPX-2674 — multi-repo workspace",
				"- `platform/` — virtru/platform",
				"- `cli/` — virtru/cli",
				"[platform/spec/DSPX-2674.md](./platform/spec/DSPX-2674.md)",
				"definition of done across every repo listed above",
			},
			// SpecPath must suppress Description, not merely outrank it.
			wantNotAny: []string{"remove volume directive"},
		},
		{
			name: "no spec path falls back to description",
			data: TemplateData{
				Ticket:      "DSPX-2674",
				Description: "remove volume directive",
				Repos: []RepoEntry{
					{Dir: "platform", Label: "virtru/platform"},
				},
			},
			wantAll: []string{
				"# DSPX-2674 — multi-repo workspace",
				"remove volume directive",
			},
			wantNotAny: []string{"definition of done", "No goal recorded yet"},
		},
		{
			name: "neither spec nor description renders a visible placeholder",
			data: TemplateData{
				Ticket: "DSPX-2674",
				Repos: []RepoEntry{
					{Dir: "platform", Label: "virtru/platform"},
				},
			},
			wantAll: []string{
				"## Goal\n_No goal recorded yet — ask the user",
			},
			// The bug this guards: an empty Goal section running straight into
			// the next heading, leaving the agent with no goal at all.
			wantNotAny: []string{"## Goal\n\n##"},
		},
		{
			name: "empty ticket falls back to Workspace title",
			data: TemplateData{
				Description: "ad hoc join",
				Repos: []RepoEntry{
					{Dir: "platform", Label: "virtru/platform"},
				},
			},
			wantAll: []string{"# Workspace — multi-repo workspace"},
		},
		{
			name: "always carries the generated-by marker for overwrite detection",
			data: TemplateData{
				Description: "x",
				Repos:       []RepoEntry{{Dir: "a", Label: "o/a"}},
			},
			wantAll: []string{Marker},
		},
		{
			name: "guidance steers agents to jj and away from gh stack",
			data: TemplateData{
				Description: "x",
				Repos:       []RepoEntry{{Dir: "a", Label: "o/a"}},
			},
			wantAll: []string{"never run `gh stack`", "Use `jj` for history operations"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RenderTemplate(tt.data)
			if err != nil {
				t.Fatalf("RenderTemplate: %v", err)
			}
			out := string(got)
			for _, want := range tt.wantAll {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q\n--- output ---\n%s", want, out)
				}
			}
			for _, notWant := range tt.wantNotAny {
				if strings.Contains(out, notWant) {
					t.Errorf("output unexpectedly contains %q\n--- output ---\n%s", notWant, out)
				}
			}
		})
	}
}

// The rendered file must start with Marker so callers can byte-prefix match it.
func TestRenderTemplateMarkerIsFirst(t *testing.T) {
	got, err := RenderTemplate(TemplateData{Repos: []RepoEntry{{Dir: "a", Label: "o/a"}}})
	if err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}
	if !strings.HasPrefix(string(got), Marker) {
		t.Errorf("output does not start with Marker\n--- output ---\n%s", got)
	}
}

// Repos render in the caller's order; writeSharedClaudeMD owns the sorting, so
// RenderTemplate must not reorder behind its back.
func TestRenderTemplatePreservesRepoOrder(t *testing.T) {
	got, err := RenderTemplate(TemplateData{
		Description: "x",
		Repos: []RepoEntry{
			{Dir: "zeta", Label: "o/zeta"},
			{Dir: "alpha", Label: "o/alpha"},
		},
	})
	if err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}
	out := string(got)
	if strings.Index(out, "`zeta/`") > strings.Index(out, "`alpha/`") {
		t.Errorf("repos were reordered\n--- output ---\n%s", out)
	}
}
