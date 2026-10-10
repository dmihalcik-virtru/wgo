package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtru/wgo/internal/jjtest"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/store"
)

// effortFixture is a temp HOME whose discovery config finds three main
// clones: acme/wgo, other/wgo (same directory name) and acme/api. The plan
// starts as the golden plan.
type effortFixture struct {
	env      *effortEnv
	out      *bytes.Buffer
	planPath string
	golden   string
	mains    string
}

func newEffortFixture(t *testing.T) *effortFixture {
	t.Helper()
	jjtest.RequireJJ(t)
	jjtest.SetIdentity(t)
	mains := t.TempDir()
	for _, rel := range []string{"acme/wgo", "other/wgo", "acme/api"} {
		dir := filepath.Join(mains, rel)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		mustJJ(t, dir, "git", "init", "--colocate")
	}
	toml := "[discovery]\nbase_dirs = [\"" + mains + "\"]\nscan_depth = 3\n"
	planPath, golden := goldenHome(t, toml)
	out := &bytes.Buffer{}
	return &effortFixture{
		env:      &effortEnv{store: store.NewWithDir(filepath.Dir(planPath)), clones: discoverMainClones, out: out},
		out:      out,
		planPath: planPath,
		golden:   golden,
		mains:    mains,
	}
}

func (f *effortFixture) plan(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(f.planPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (f *effortFixture) stateBytes(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(filepath.Dir(f.planPath), "state.json"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return b
}

func (f *effortFixture) effort(t *testing.T, name string) (store.Effort, bool) {
	t.Helper()
	var st store.State
	if b := f.stateBytes(t); len(b) > 0 {
		if err := json.Unmarshal(b, &st); err != nil {
			t.Fatal(err)
		}
	}
	e, ok := st.Efforts[plan.GenerateEffortID(name)]
	return e, ok
}

// path resolves a fixture repo the way discovery reports it.
func (f *effortFixture) path(t *testing.T, rel string) string {
	t.Helper()
	clones, err := discoverMainClones()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range clones {
		if strings.HasSuffix(c.Path, string(filepath.Separator)+filepath.FromSlash(rel)) {
			return c.Path
		}
	}
	t.Fatalf("clone %s not discovered in %+v", rel, clones)
	return ""
}

// wantErr asserts err mentions every fragment and that the call changed
// neither file.
func (f *effortFixture) wantErr(t *testing.T, err error, fragments ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error containing %q", fragments)
	}
	for _, frag := range fragments {
		if !strings.Contains(err.Error(), frag) {
			t.Errorf("error %q does not mention %q", err, frag)
		}
	}
}

const goldenEffortsTail = "~~~\n## fenced inside efforts\n- not:an-entry\n~~~\n"

func TestEffortCommandsRoundTrip(t *testing.T) {
	f := newEffortFixture(t)
	api := f.path(t, "acme/api")
	acmeWgo := f.path(t, "acme/wgo")

	// add: a new block at the end of Efforts; everything else verbatim.
	if err := f.env.add("Feature X", "Cross-repo thing"); err != nil {
		t.Fatalf("add: %v", err)
	}
	block := "\n### Feature X\nCross-repo thing\n"
	afterAdd := mustReplaceOnce(t, f.golden, goldenEffortsTail, goldenEffortsTail+block)
	assertPlanBytes(t, f.planPath, afterAdd)
	e, ok := f.effort(t, "Feature X")
	if !ok || e.Description != "Cross-repo thing" || len(e.Branches) != 0 {
		t.Fatalf("state after add = %+v, %v", e, ok)
	}

	// link by directory name and by owner/repo: state stores the clone path,
	// the plan shows the shortest unambiguous name.
	if err := f.env.link("Feature X", "api:feat"); err != nil {
		t.Fatalf("link api: %v", err)
	}
	if err := f.env.link("feature x", "acme/wgo:feat"); err != nil {
		t.Fatalf("link acme/wgo: %v", err)
	}
	afterLink := mustReplaceOnce(t, afterAdd, block, block+"\n- api:feat\n- acme/wgo:feat\n")
	assertPlanBytes(t, f.planPath, afterLink)
	e, _ = f.effort(t, "Feature X")
	if got, want := strings.Join(e.Branches, ","), api+":feat,"+acmeWgo+":feat"; got != want {
		t.Errorf("state branches = %q, want %q", got, want)
	}

	// Error paths leave both files untouched.
	before := f.stateBytes(t)
	errCases := []struct {
		name      string
		run       func() error
		fragments []string
	}{
		{"already linked", func() error { return f.env.link("Feature X", "api:feat") },
			[]string{"already linked", "wgo plan effort unlink"}},
		{"already linked by path", func() error { return f.env.link("Feature X", api+":feat") },
			[]string{"already linked"}},
		{"ambiguous repo", func() error { return f.env.link("Feature X", "wgo:feat") },
			[]string{"ambiguous", "acme/wgo", "other/wgo", "owner/repo"}},
		{"unknown repo", func() error { return f.env.link("Feature X", "nope:feat") },
			[]string{"nope", "api"}},
		{"missing colon", func() error { return f.env.link("Feature X", "feat") },
			[]string{"repo:bookmark"}},
		{"unknown effort", func() error { return f.env.link("Missing", "api:feat") },
			[]string{`effort "Missing" not found`, "wgo plan effort add"}},
		{"not linked", func() error { return f.env.unlink("Feature X", "api:other") },
			[]string{"not linked"}},
		{"unlink unknown effort", func() error { return f.env.unlink("Missing", "api:feat") },
			[]string{"not found"}},
		{"remove unknown effort", func() error { return f.env.remove("Missing", false) },
			[]string{"not found"}},
		{"add existing", func() error { return f.env.add("Feature X", "") },
			[]string{"already exists"}},
	}
	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			f.wantErr(t, tc.run(), tc.fragments...)
			assertPlanBytes(t, f.planPath, afterLink)
			if !bytes.Equal(f.stateBytes(t), before) {
				t.Errorf("state changed")
			}
		})
	}

	// unlink removes the entry from both sides.
	if err := f.env.unlink("Feature X", "api:feat"); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	assertPlanBytes(t, f.planPath, mustReplaceOnce(t, afterLink, "- api:feat\n", ""))
	e, _ = f.effort(t, "Feature X")
	if got := strings.Join(e.Branches, ","); got != acmeWgo+":feat" {
		t.Errorf("state branches after unlink = %q", got)
	}

	// remove restores the golden plan exactly.
	if err := f.env.remove("Feature X", false); err != nil {
		t.Fatalf("remove: %v", err)
	}
	assertPlanBytes(t, f.planPath, f.golden)
	if _, ok := f.effort(t, "Feature X"); ok {
		t.Error("effort still in state after remove")
	}
}

func TestEffortLinkPlanOnlyEffort(t *testing.T) {
	f := newEffortFixture(t)
	// "Dashboard" exists only in the hand-written plan as "- wgo:gh-70-dashboard",
	// which is ambiguous here; link must keep it verbatim and add the new one.
	if err := f.env.link("Dashboard", "api:dash"); err != nil {
		t.Fatalf("link: %v", err)
	}
	want := mustReplaceOnce(t, f.golden, "- wgo:gh-70-dashboard\n", "- wgo:gh-70-dashboard\n- api:dash\n")
	assertPlanBytes(t, f.planPath, want)
	e, ok := f.effort(t, "Dashboard")
	if !ok {
		t.Fatal("link should create the state record for a plan-only effort")
	}
	if got, want := strings.Join(e.Branches, ","), "wgo:gh-70-dashboard,"+f.path(t, "acme/api")+":dash"; got != want {
		t.Errorf("state branches = %q, want %q", got, want)
	}
}

func TestEffortRemoveWarnsAboutDuplicateHeading(t *testing.T) {
	f := newEffortFixture(t)
	// The primary block holds a stray line, so removal needs --force.
	if err := f.env.remove("Plan efforts", true); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got := f.plan(t)
	if strings.Contains(got, "- wgo:gh-71-plan-efforts\n") {
		t.Error("the primary block should be removed")
	}
	if !strings.Contains(got, "### Plan efforts\n- other:duplicate-heading\n") {
		t.Error("the duplicate block must be kept verbatim")
	}
	out := f.out.String()
	if !strings.Contains(out, "still has another ### Plan efforts block") || !strings.Contains(out, "wgo plan edit") {
		t.Errorf("expected a duplicate-heading notice, got %q", out)
	}
	if strings.Contains(out, "Removed effort: ") {
		t.Errorf("must not claim a clean removal: %q", out)
	}
}

func TestEffortRemoveRefusesToDeleteUnparsedText(t *testing.T) {
	f := newEffortFixture(t)
	err := f.env.remove("Plan efforts", false)
	f.wantErr(t, err, "A stray line after the entries.", "--force", "wgo plan edit")
	assertPlanBytes(t, f.planPath, f.golden)
	if len(f.stateBytes(t)) != 0 {
		t.Error("state should not have been written")
	}

	// An effort with nothing uninterpreted removes without --force.
	if err := f.env.remove("Dashboard", false); err != nil {
		t.Fatalf("remove Dashboard: %v", err)
	}
	if strings.Contains(f.plan(t), "### Dashboard") {
		t.Error("Dashboard should be gone")
	}
}

func TestEffortUnlinkDeadReference(t *testing.T) {
	f := newEffortFixture(t)
	if err := f.env.add("Feature X", ""); err != nil {
		t.Fatal(err)
	}
	// Hand-edit a link to a clone that is not discovered.
	edited := mustReplaceOnce(t, f.plan(t), "### Feature X\n", "### Feature X\n- gone:feat\n")
	if err := os.WriteFile(f.planPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.env.unlink("Feature X", "gone:feat"); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if strings.Contains(f.plan(t), "gone:feat") {
		t.Error("dead entry still in the plan")
	}
	if !strings.Contains(f.out.String(), "no longer resolves") {
		t.Errorf("expected a 'no longer resolves' notice, got %q", f.out.String())
	}

	// A true miss still says so, and mentions why the ref did not resolve.
	err := f.env.unlink("Feature X", "gone:other")
	f.wantErr(t, err, "not linked", "no longer resolves")
}

func TestEffortAddDescriptionConflict(t *testing.T) {
	f := newEffortFixture(t)
	// "Dashboard" is plan-only with no description; a differing -d is an error.
	err := f.env.add("Dashboard", "new text")
	f.wantErr(t, err, "already exists", "wgo plan edit")
	assertPlanBytes(t, f.planPath, f.golden)

	// Case-insensitive: "plan efforts" is the existing "Plan efforts" on both
	// sides once state has it.
	err = f.env.add("dashboard", "")
	if err != nil {
		t.Fatalf("add without a description projects the plan effort into state: %v", err)
	}
	if _, ok := f.effort(t, "Dashboard"); !ok {
		t.Error("state should now hold Dashboard")
	}
	f.wantErr(t, f.env.add("DASHBOARD", ""), "already exists")
	assertPlanBytes(t, f.planPath, f.golden)

	// Names that would corrupt the plan are rejected.
	f.wantErr(t, f.env.add("# sneaky", ""), "single line")
	f.wantErr(t, f.env.add("ok", "- looks like an entry"), "description")
}

func TestEffortStateWriteFailureRestoresPlan(t *testing.T) {
	f := newEffortFixture(t)
	if err := f.env.add("Feature X", ""); err != nil {
		t.Fatal(err)
	}
	planBefore := f.plan(t)
	stateBefore := f.stateBytes(t)

	// A directory where SaveState writes its temp file fails the state write
	// after the plan was already written.
	if err := os.Mkdir(filepath.Join(filepath.Dir(f.planPath), "state.json.tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := f.env.add("Other", "")
	f.wantErr(t, err, "plan restored")
	assertPlanBytes(t, f.planPath, planBefore)
	if !bytes.Equal(f.stateBytes(t), stateBefore) {
		t.Error("state changed")
	}
}

func TestEffortCobraWiring(t *testing.T) {
	jjtest.RequireJJ(t)
	planPath, golden := goldenHome(t, "")
	defer func() { effortDescription, effortForce = "", false }()
	rootCmd.SetArgs([]string{"plan", "effort", "add", "Wired", "-d", "from the flag"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	want := mustReplaceOnce(t, golden, goldenEffortsTail, goldenEffortsTail+"\n### Wired\nfrom the flag\n")
	assertPlanBytes(t, planPath, want)
}

func TestEffortPlanWriteFailureLeavesStateUnchanged(t *testing.T) {
	f := newEffortFixture(t)
	if err := f.env.add("Feature X", ""); err != nil {
		t.Fatal(err)
	}
	stateBefore := f.stateBytes(t)
	planBefore := f.plan(t)

	// A directory where SavePlan writes its temp file makes the plan write fail.
	if err := os.Mkdir(f.planPath+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	ops := map[string]func() error{
		"add":    func() error { return f.env.add("Other", "") },
		"link":   func() error { return f.env.link("Feature X", "api:feat") },
		"remove": func() error { return f.env.remove("Feature X", false) },
	}
	for name, op := range ops {
		err := op()
		if err == nil || !strings.Contains(err.Error(), "state left unchanged") {
			t.Errorf("%s: expected a plan-write error, got %v", name, err)
		}
		if !bytes.Equal(f.stateBytes(t), stateBefore) {
			t.Errorf("%s: state.json changed after a failed plan write", name)
		}
		assertPlanBytes(t, f.planPath, planBefore)
	}
}
