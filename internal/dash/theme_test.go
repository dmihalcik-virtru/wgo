package dash

import (
	"testing"

	"github.com/virtru/wgo/internal/effort"
)

func TestResolveTheme(t *testing.T) {
	efforts := map[string]effort.MergedEffort{
		"e1":      {ID: "e1", Name: "Login Revamp"},
		"e2":      {ID: "e2", Name: "Billing"},
		"dupA":    {ID: "dupA", Name: "Search"},
		"dupB":    {ID: "dupB", Name: "search"},
		"Billing": {ID: "Billing", Name: "Something else"},
	}
	tests := []struct {
		name, theme, want string
	}{
		{"ID match", "e1", "e1"},
		{"unique name, case-insensitive", "LOGIN revamp", "e1"},
		{"ambiguous name is left unchanged", "Search", "Search"},
		{"no match is left unchanged", "nonsense", "nonsense"},
		{"ID match beats a name collision", "Billing", "Billing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveTheme(tt.theme, efforts); got != tt.want {
				t.Fatalf("resolveTheme(%q) = %q, want %q", tt.theme, got, tt.want)
			}
		})
	}
	// Without the collision, the name "Billing" maps to e2.
	delete(efforts, "Billing")
	if got := resolveTheme("billing", efforts); got != "e2" {
		t.Fatalf("name match: got %q, want e2", got)
	}
}
