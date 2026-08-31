package rig

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/jj"
)

// fakePinned answers Inspect's revset per checkout directory. Anything not in
// log is a directory the test did not set up, which must not read as healthy.
type fakePinned struct {
	log  map[string][]jj.LogEntry
	errs map[string]error
	// asked records the revset each call used, so the test can assert the
	// classification asks about descent from the pin rather than, say, walking
	// the whole history.
	asked map[string]string
}

func (f *fakePinned) Log(repo, revset string) ([]jj.LogEntry, error) {
	if f.asked == nil {
		f.asked = map[string]string{}
	}
	f.asked[repo] = revset
	if err := f.errs[repo]; err != nil {
		return nil, err
	}
	entries, ok := f.log[repo]
	if !ok {
		return nil, errors.New("no such workspace")
	}
	return entries, nil
}

// onPin is the shape jj reports for an untouched rig checkout: a fresh empty
// working copy sitting directly on the pinned commit.
func onPin(commit string) []jj.LogEntry {
	return []jj.LogEntry{
		{CommitID: "9999999999999999999999999999999999999999", Empty: true, CurrentWorkingCopy: true},
		{CommitID: commit},
	}
}

// healthyRig materialises twoCheckoutManifest on disk and returns the rig root
// alongside a fake reporting both checkouts on their pins.
func healthyRig(t *testing.T) (string, *Manifest, *fakePinned) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "dsp")
	m := twoCheckoutManifest()
	log := map[string][]jj.LogEntry{}
	for _, c := range m.Checkouts {
		dest := filepath.Join(root, SrcDir, c.Dir)
		require.NoError(t, os.MkdirAll(dest, 0o755))
		log[dest] = onPin(c.Commit)
	}
	return root, m, &fakePinned{log: log}
}

func TestInspectClassifiesAnIntactRig(t *testing.T) {
	root, m, p := healthyRig(t)

	got := Inspect(p, m, root)

	require.Len(t, got, 2, "every checkout is classified, not just the broken ones")
	for _, c := range got {
		assert.True(t, c.OK(), "%s: %s", c.Checkout.Dir, c.Health)
		assert.Equal(t, "dsp", c.Rig)
		assert.Empty(t, c.At)
	}
	assert.Equal(t, filepath.Join(root, SrcDir, "platform-v0.9.0"), got[0].Path)
	assert.Equal(t,
		"@ | @- | (::@ & present(aaaaaaaa1111))",
		p.asked[got[0].Path],
		"the pin is asked about as an ancestor of @, not just as @-")
}

// `jj edit` on the pinned commit makes @ the pin rather than its child. The
// checkout still holds exactly the pinned source, so it is not drift.
func TestInspectAcceptsTheWorkingCopyItself(t *testing.T) {
	root, m, p := healthyRig(t)
	dest := filepath.Join(root, SrcDir, "otdfctl-v0.3.0")
	p.log[dest] = []jj.LogEntry{{CommitID: "bbbbbbbb2222", CurrentWorkingCopy: true}}

	got := Inspect(p, m, root)

	assert.True(t, got[1].OK())
}

// The condition doctor exists for: someone ran `jj new` onto another commit, so
// the working copy no longer descends from the pin. go.work still resolves and
// the build still succeeds — only the source stopped being what shipped.
func TestInspectReportsAMovedCheckout(t *testing.T) {
	root, m, p := healthyRig(t)
	dest := filepath.Join(root, SrcDir, "platform-v0.9.0")
	p.log[dest] = []jj.LogEntry{
		{CommitID: "9999999999999999999999999999999999999999", Empty: true, CurrentWorkingCopy: true},
		{CommitID: "cccccccc3333"},
	}

	got := Inspect(p, m, root)

	require.Equal(t, HealthMoved, got[0].Health)
	// The parent, not the anonymous working copy: that is the commit the user
	// actually moved to, and the only one worth naming back at them.
	assert.Equal(t, "cccccccc3333", got[0].At)
	assert.True(t, got[1].OK(), "the other checkout is untouched")
}

// With no parent to report — the user is sitting directly on some other commit
// — the working copy is the commit they moved to.
func TestInspectNamesTheWorkingCopyWhenItIsAllThereIs(t *testing.T) {
	root, m, p := healthyRig(t)
	dest := filepath.Join(root, SrcDir, "platform-v0.9.0")
	p.log[dest] = []jj.LogEntry{{CommitID: "cccccccc3333", CurrentWorkingCopy: true}}

	got := Inspect(p, m, root)

	assert.Equal(t, HealthMoved, got[0].Health)
	assert.Equal(t, "cccccccc3333", got[0].At)
}

// The workflow a rig exists for: the user committed twice on top of the pin, so
// the pin is @-- and neither @ nor @- is it. jj still reports it as an ancestor,
// and reporting that as drift would flag every rig anybody actually worked in.
func TestInspectAcceptsCommitsStackedOnThePin(t *testing.T) {
	root, m, p := healthyRig(t)
	dest := filepath.Join(root, SrcDir, "platform-v0.9.0")
	p.log[dest] = []jj.LogEntry{
		{CommitID: "9999999999999999999999999999999999999999", Empty: true, CurrentWorkingCopy: true},
		{CommitID: "dddddddd4444"},
		{CommitID: m.Checkouts[0].Commit},
	}

	got := Inspect(p, m, root)

	assert.True(t, got[0].OK(), "two commits of work on top of the pin is not drift")
}

// `jj edit <other commit>` leaves the user on a commit with content in it. That
// commit is where they are; its parent is not, and naming the parent would send
// them looking in the wrong place.
func TestInspectNamesANonEmptyWorkingCopyOverItsParent(t *testing.T) {
	root, m, p := healthyRig(t)
	dest := filepath.Join(root, SrcDir, "platform-v0.9.0")
	p.log[dest] = []jj.LogEntry{
		{CommitID: "cccccccc3333", CurrentWorkingCopy: true},
		{CommitID: "eeeeeeee5555"},
	}

	got := Inspect(p, m, root)

	require.Equal(t, HealthMoved, got[0].Health)
	assert.Equal(t, "cccccccc3333", got[0].At)
}

// A pin that is not a commit id never reaches jj's revset parser: rig.toml is
// hand-editable, and a typo there must not turn into a revset syntax error.
func TestInspectDoesNotSpliceANonCommitPinIntoTheRevset(t *testing.T) {
	root, m, p := healthyRig(t)
	dest := filepath.Join(root, SrcDir, "platform-v0.9.0")
	m.Checkouts[0].Commit = `heads(all())`

	got := Inspect(p, m, root)

	assert.Equal(t, "@ | @-", p.asked[dest])
	assert.Equal(t, HealthMoved, got[0].Health)
}

func TestIsCommitID(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"full hash", "aaaaaaaa1111222233334444555566667777", true},
		{"abbreviation", "aaaaaaa", true},
		{"too short", "aaaaaa", false},
		{"not hex", "aaaaaaag", false},
		{"a revset", "heads(all())", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isCommitID(tt.in))
		})
	}
}

func TestInspectReportsAMissingCheckout(t *testing.T) {
	root, m, p := healthyRig(t)
	dest := filepath.Join(root, SrcDir, "platform-v0.9.0")
	require.NoError(t, os.RemoveAll(dest))

	got := Inspect(p, m, root)

	assert.Equal(t, HealthMissing, got[0].Health)
	assert.NotContains(t, p.asked, dest, "a directory that is gone is not worth a jj call")
}

// "we could not tell" must not read as "it is fine": a jj failure is its own
// condition, carrying the error so the report can name it.
func TestInspectReportsAnUnreadableCheckout(t *testing.T) {
	root, m, p := healthyRig(t)
	dest := filepath.Join(root, SrcDir, "platform-v0.9.0")
	p.errs = map[string]error{dest: errors.New("workspace is stale")}

	got := Inspect(p, m, root)

	assert.Equal(t, HealthUnreadable, got[0].Health)
	assert.Contains(t, got[0].Detail, "workspace is stale")
	assert.Empty(t, got[0].At)
}

// An empty log is a successful call that answered nothing. Treating it as drift
// would name no commit to move back to.
func TestInspectReportsAnEmptyLogAsUnreadable(t *testing.T) {
	root, m, p := healthyRig(t)
	dest := filepath.Join(root, SrcDir, "platform-v0.9.0")
	p.log[dest] = nil

	got := Inspect(p, m, root)

	assert.Equal(t, HealthUnreadable, got[0].Health)
	assert.Contains(t, got[0].Detail, "@ | @-")
	assert.Empty(t, got[0].At)
}

// rig.toml is documented as hand-editable, so a commit pasted in from `jj log`
// is abbreviated. That must not read as a moved checkout.
func TestInspectAcceptsAnAbbreviatedPin(t *testing.T) {
	root, m, p := healthyRig(t)
	dest := filepath.Join(root, SrcDir, "platform-v0.9.0")
	full := "aaaaaaaa1111222233334444555566667777"
	p.log[dest] = onPin(full)
	m.Checkouts[0].Commit = full[:10]

	got := Inspect(p, m, root)

	assert.True(t, got[0].OK())
}

func TestSameCommit(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical", "aaaaaaaa1111", "aaaaaaaa1111", true},
		{"abbreviated pin", "aaaaaaaa1111", "aaaaaaa", true},
		{"abbreviated log entry", "aaaaaaa", "aaaaaaaa1111", true},
		{"different", "aaaaaaaa1111", "bbbbbbbb2222", false},
		// Below the floor a shared prefix is coincidence, not identity — and
		// mistaking one for the other silently passes a drifted checkout.
		{"too short to be meaningful", "aaaaaa", "aaaaaaaa1111", false},
		{"both empty", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sameCommit(tt.a, tt.b))
		})
	}
}
