package models

// ContextSchemaVersion is the version of the wgo . --json projection.
//
// Additive fields do not bump it; a breaking change (rename/removal/semantic
// change) bumps it and is noted in the spec changelog (see spec/WGO-130.md).
const ContextSchemaVersion = 1

// Context is the single strongly-typed value that `wgo .` resolves and renders
// as both human (text) and machine (--json) output. Both renderers are pure
// projections of this value, so the two outputs can never drift.
type Context struct {
	SchemaVersion  int          `json:"schema_version"`
	Repo           string       `json:"repo"`
	RepoURL        string       `json:"repo_url"`
	Branch         string       `json:"branch"`
	Status         string       `json:"status"`  // clean|modified|staged|conflict
	Changes        GitStatus    `json:"changes"` // detailed counts behind the status word
	Dirty          bool         `json:"dirty"`
	Ahead          int          `json:"ahead"`
	Behind         int          `json:"behind"`
	SyncUnknown    bool         `json:"sync_unknown,omitempty"` // ahead/behind could not be determined
	Remote         string       `json:"remote"`
	BranchURL      string       `json:"branch_url"`
	Commit         CommitInfo   `json:"commit"`
	Ticket         string       `json:"ticket,omitempty"`
	TicketURL      string       `json:"ticket_url,omitempty"` // Jira/GitHub-issue link for Ticket
	Spec           *SpecRef     `json:"spec,omitempty"`
	SpecMissing    bool         `json:"spec_missing,omitempty"`    // ticket present but no spec file
	SpecUnreadable bool         `json:"spec_unreadable,omitempty"` // spec file present but unparseable
	Tasks          []TaskRef    `json:"tasks,omitempty"`
	PRs            []PRRef      `json:"prs,omitempty"`
	Siblings       []SiblingRef `json:"siblings,omitempty"`
	// SiblingsOverflow counts jj repos beyond the display cap of 10.
	SiblingsOverflow int `json:"siblings_overflow,omitempty"`
}

// SpecRef references the spec file associated with the current ticket branch.
type SpecRef struct {
	Path    string `json:"path"`
	Status  string `json:"status"`
	Updated string `json:"updated"` // YYYY-MM-DD
}

// TaskRef is a plan task linked to the current branch.
type TaskRef struct {
	Bullet string `json:"bullet"`
	Text   string `json:"text"`
}

// PRRef is a pull request associated with the current branch.
type PRRef struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	URL    string `json:"url"`
	// ReviewDecision is the rolled-up review state: APPROVED,
	// CHANGES_REQUESTED, or "" when none applies. REVIEW_REQUIRED is not
	// synthesized (it needs branch-protection data, which wgo does not read).
	ReviewDecision string `json:"review_decision,omitempty"`
	// IsDraft reports whether the PR is a draft.
	IsDraft bool `json:"is_draft,omitempty"`
	// Checks is the CI/checks rollup for the PR's head commit.
	Checks CIStatus `json:"checks"`
}

// CIStatus is the rolled-up CI/checks state for a commit, summarizing GitHub's
// check-runs and legacy commit statuses into a single glanceable signal.
type CIStatus struct {
	State   string `json:"state"` // success | failure | pending | none
	Passed  int    `json:"passed"`
	Failed  int    `json:"failed"`
	Pending int    `json:"pending"`
	Total   int    `json:"total"`
	// URL is a per-state deep link: failing job (failure), merge-queue view
	// (pending), or the PR checks tab (success). Empty when State is none.
	URL string `json:"url,omitempty"`
}

// SiblingRef is a sibling workspace/repo in the parent directory.
type SiblingRef struct {
	Name   string `json:"name"`
	Branch string `json:"branch"`
	// Status is a human-readable summary phrase (e.g. "clean" or
	// "2 modified, 1 added"), not the Context.Status enum. It is keyed
	// separately in JSON to avoid colliding with that enum's value space.
	Status string `json:"status_summary"`
}
