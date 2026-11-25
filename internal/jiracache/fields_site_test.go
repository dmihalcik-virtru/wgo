package jiracache

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscoveredFieldIDsAreScopedToSite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, WriteFieldsForSite("first.atlassian.net", Fields{Sprint: "customfield_10020"}))
	require.NoError(t, WriteFieldsForSite("second.atlassian.net", Fields{StoryPoints: "customfield_20016"}))
	first, ok := ReadFieldsForSite("first.atlassian.net", FieldsTTL)
	require.True(t, ok)
	assert.Equal(t, "customfield_10020", first.Sprint)
	assert.Empty(t, first.StoryPoints)
	second, ok := ReadFieldsForSite("second.atlassian.net", FieldsTTL)
	require.True(t, ok)
	assert.Equal(t, "customfield_20016", second.StoryPoints)
	_, ok = ReadFieldsForSite("third.atlassian.net", FieldsTTL)
	assert.False(t, ok)
}
