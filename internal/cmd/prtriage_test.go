package cmd

import (
	"strings"
	"testing"

	"github.com/virtru/wgo/internal/config"
)

func TestTriageRulesFromConfig(t *testing.T) {
	if _, err := triageRules(&config.Config{}); err != nil {
		t.Errorf("defaults: %v", err)
	}
	cfg := &config.Config{PR: config.PRConfig{Triage: config.PRTriageConfig{Rules: []config.PRTriageRule{
		{Bucket: "mine", Match: "prstate:draft"},
		{Bucket: "broken", Match: "prstat:draft"},
		{Bucket: "rest"},
	}}}}
	_, err := triageRules(cfg)
	if err == nil || !strings.Contains(err.Error(), `rule 2 (bucket "broken")`) || !strings.Contains(err.Error(), "config.toml") {
		t.Errorf("invalid rule: %v", err)
	}
}
