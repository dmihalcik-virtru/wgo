package urlhandler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/dash"
)

func TestOpenDeclineStartsCooldown(t *testing.T) {
	f := newOpenFixture(t)
	f.confirmer.answer = false
	if _, err := f.open(OpenURL(f.wsID)); !errors.Is(err, ErrDeclined) {
		t.Fatalf("err = %v, want ErrDeclined", err)
	}

	// The page fires the link again: no dialog, no launch.
	f.now = f.now.Add(DeclineCooldown - time.Second)
	f.confirmer.answer = true
	_, err := f.open(OpenURL(f.wsID))
	var cd *CoolingDownError
	if !errors.As(err, &cd) {
		t.Fatalf("err = %v, want a *CoolingDownError", err)
	}
	if want := f.now.Add(-DeclineCooldown + time.Second).Add(DeclineCooldown); !cd.Until.Equal(want) {
		t.Fatalf("Until = %v, want %v", cd.Until, want)
	}
	if strings.Contains(err.Error(), f.wsID) {
		t.Fatalf("error quotes the workspace ID: %v", err)
	}
	if len(f.confirmer.prompts) != 1 || len(f.launcher.acts) != 0 {
		t.Fatalf("prompts %d, launches %d; want 1 and 0", len(f.confirmer.prompts), len(f.launcher.acts))
	}

	// Another workspace is not affected.
	if _, err := f.open(OpenURL(f.outsideID)); errors.As(err, &cd) {
		t.Fatalf("cooldown leaked to another workspace: %v", err)
	}

	// Once it has cooled down, the link asks again.
	f.now = f.now.Add(time.Second)
	if _, err := f.open(OpenURL(f.wsID)); err != nil {
		t.Fatal(err)
	}
	if len(f.confirmer.prompts) != 2 || len(f.launcher.acts) != 1 {
		t.Fatalf("prompts %d, launches %d; want 2 and 1", len(f.confirmer.prompts), len(f.launcher.acts))
	}
}

func TestOpenInteractiveSkipsCooldownAndApprovalClearsIt(t *testing.T) {
	f := newOpenFixture(t)
	f.confirmer.answer = false
	if _, err := f.open(OpenURL(f.wsID)); !errors.Is(err, ErrDeclined) {
		t.Fatal(err)
	}
	f.interactive = true
	f.confirmer.answer = true
	if _, err := f.open(OpenURL(f.wsID)); err != nil {
		t.Fatalf("interactive open during cooldown: %v", err)
	}
	if len(f.confirmer.prompts) != 2 {
		t.Fatalf("prompts = %d, want the interactive run to ask", len(f.confirmer.prompts))
	}
	if _, cooling, err := f.approvals.CoolingDown(f.wsID, f.now); cooling || err != nil {
		t.Fatalf("CoolingDown after approval = %v, %v; want cleared", cooling, err)
	}
}

func TestOpenConfirmErrorStartsNoCooldown(t *testing.T) {
	f := newOpenFixture(t)
	f.confirmer.err = errors.New("no GUI")
	if _, err := f.open(OpenURL(f.wsID)); err == nil {
		t.Fatal("want the confirmer's error")
	}
	if _, cooling, _ := f.approvals.CoolingDown(f.wsID, f.now); cooling {
		t.Fatal("a dialog that could not be shown is not a decline")
	}
}

func TestCoolingDownIgnoresFutureDeclines(t *testing.T) {
	f := newOpenFixture(t)
	if err := f.approvals.Decline(f.wsID, f.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, cooling, err := f.approvals.CoolingDown(f.wsID, f.now); cooling || err != nil {
		t.Fatalf("CoolingDown = %v, %v; a decline stamped in the future must not block", cooling, err)
	}
}

func TestDeclineDropsCooledDownEntries(t *testing.T) {
	f := newOpenFixture(t)
	if err := f.approvals.Decline(f.outsideID, f.now); err != nil {
		t.Fatal(err)
	}
	later := f.now.Add(DeclineCooldown)
	if err := f.approvals.Decline(f.wsID, later); err != nil {
		t.Fatal(err)
	}
	doc, err := f.approvals.load()
	if err != nil || len(doc.Declines) != 1 || doc.Declines[0].WorkspaceID != f.wsID {
		t.Fatalf("declines = %+v, %v; want only the recent one", doc.Declines, err)
	}
}

// TestApprovalsCoverOnlyTheirID: an approval is for an ID at a path, so a
// different ID resolving to the same path asks again, and approving it
// keeps the first approval.
func TestApprovalsCoverOnlyTheirID(t *testing.T) {
	f := newOpenFixture(t)
	if _, err := f.open(OpenURL(f.wsID)); err != nil {
		t.Fatal(err)
	}
	otherID := dash.WorkspaceID(filepath.Join(f.root, "mains", "other"), f.ws)
	f.resolver[otherID] = dash.Target{ID: otherID, Root: f.ws}
	if _, err := f.open(OpenURL(otherID)); err != nil {
		t.Fatal(err)
	}
	if len(f.confirmer.prompts) != 2 {
		t.Fatalf("prompts = %d; an approval for one ID must not cover another at the same path", len(f.confirmer.prompts))
	}
	for _, id := range []string{f.wsID, otherID} {
		if ok, err := f.approvals.Approved(id, f.ws); !ok || err != nil {
			t.Fatalf("Approved(%s) = %v, %v; want both kept", id, ok, err)
		}
	}
}

// TestApproveLeavesUnreadableFileAlone: only a corrupt file is replaced; a
// file that cannot be read keeps its approvals.
func TestApproveLeavesUnreadableFileAlone(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any file")
	}
	f := newOpenFixture(t)
	if err := f.approvals.Approve(f.outsideID, f.outside, f.now, nil); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(f.approvals.Path())
	if err := os.Chmod(f.approvals.Path(), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(f.approvals.Path(), 0o600) })
	var warned bool
	err := f.approvals.Approve(f.wsID, f.ws, f.now, func(string, ...any) { warned = true })
	if err == nil || warned {
		t.Fatalf("Approve = %v (warned %v); want an error and no replacement", err, warned)
	}
	os.Chmod(f.approvals.Path(), 0o600)
	if after, _ := os.ReadFile(f.approvals.Path()); string(after) != string(before) {
		t.Fatalf("an unreadable approvals file was rewritten:\n%s", after)
	}
}

type errResolver struct{ err error }

func (r errResolver) Resolve(string) (dash.Target, error) { return dash.Target{}, r.err }

func TestOpenReportsDiscoveryProblems(t *testing.T) {
	f := newOpenFixture(t)
	diag := "repo /src/mains/wgo: could not list workspaces: jj: not found"
	_, err := Open(context.Background(), OpenURL(f.wsID), OpenOptions{
		Resolver:  errResolver{&dash.DiscoveryIncompleteError{Diagnostics: []string{diag}}},
		Roots:     []string{f.root},
		Launcher:  f.launcher,
		Confirmer: f.confirmer,
		Approvals: f.approvals,
	})
	if err == nil || !strings.Contains(err.Error(), diag) || !strings.Contains(err.Error(), "wgo setup url-handler") {
		t.Fatalf("err = %v; want the discovery problem and the setup hint", err)
	}
	if len(f.confirmer.prompts) != 0 || len(f.launcher.acts) != 0 {
		t.Fatal("nothing may be asked or launched")
	}
}
