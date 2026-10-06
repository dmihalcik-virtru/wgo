//go:build linux

package proc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseStatCommWithSpacesAndParens(t *testing.T) {
	line := "1234 (my (odd) proc) S 99 1234 1234 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 555555 1000 10"
	info, err := parseStat(1234, line)
	require.NoError(t, err)
	assert.Equal(t, "my (odd) proc", info.Name)
	assert.Equal(t, 99, info.PPID)
	assert.Equal(t, int64(555555), info.Start)

	_, err = parseStat(1, "1 (zombie) Z 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 1 0 0")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestParseStatMalformedIsNotGone(t *testing.T) {
	_, err := parseStat(7, "7 (truncated")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNotFound, "an unreadable stat line is not proof the process exited")
}
