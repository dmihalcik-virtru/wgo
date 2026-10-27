package jj

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// jj lists files it refused to snapshot (too large, or left out by
// snapshot.auto-track) under "Untracked paths:"; they are not changes.
func TestParseStatusUntracked(t *testing.T) {
	st := parseStatus("Working copy changes:\nA small.txt\nUntracked paths:\n? big.bin\n? dir/other.bin\n" +
		"Working copy  (@) : mmxpmnkq 6958b167 (no description set)\nParent commit (@-): rtnyvklk 5b9fd583 base\n")
	assert.False(t, st.Clean)
	assert.Equal(t, []string{"small.txt"}, st.Added)
	assert.Equal(t, []string{"big.bin", "dir/other.bin"}, st.Untracked)

	st = parseStatus("The working copy has no changes.\nUntracked paths:\n? big.bin\nWorking copy  (@) : x\n")
	assert.True(t, st.Clean)
	assert.Equal(t, []string{"big.bin"}, st.Untracked)
}
