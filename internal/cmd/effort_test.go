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
		{"remove unknown effort", func() error { return f.env.remove("Missing") },
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
	if err := f.env.remove("Feature X"); err != nil {
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
	if err := f.env.remove("Plan efforts"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got := f.plan(t)
	if strings.Contains(got, "- wgo:gh-71-plan-efforts\n") {
		t.Error("the primary block should be removed")
	}
	if !strings.Contains(got, "### Plan efforts\n- other:duplicate-heading\n") {
		t.Error("the duplicate block must be kept verbatim")
	}
	if !strings.Contains(f.out.String(), "another \"### Plan efforts\" block") {
		t.Errorf("expected a duplicate-heading warning, got %q", f.out.String())
	}
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
		"remove": func() error { return f.env.remove("Feature X") },
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
