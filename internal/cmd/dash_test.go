package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestDashRequiresJSON(t *testing.T) {
	old := dashJSON
	t.Cleanup(func() { dashJSON = old })
	dashJSON = false
	err := runDash(context.Background(), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "wgo dash --json") {
		t.Fatalf("want an error suggesting --json, got %v", err)
	}
}
