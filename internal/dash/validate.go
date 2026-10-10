package dash

import (
	"errors"
	"fmt"
	"maps"
	"slices"
)

// maxValidateErrors caps how many problems Validate reports individually.
const maxValidateErrors = 20

// edgeEndpoints lists, per edge kind, the allowed source and target kinds.
var edgeEndpoints = map[EdgeKind]struct{ src, dst []NodeKind }{
	EdgeContains: {[]NodeKind{KindEffort}, []NodeKind{KindWorkspace}},
	EdgeOn:       {[]NodeKind{KindWorkspace}, []NodeKind{KindBookmark}},
	EdgePR:       {[]NodeKind{KindBookmark}, []NodeKind{KindPR}},
	EdgeTicket:   {[]NodeKind{KindPR, KindBookmark}, []NodeKind{KindTicket}},
	EdgeRuns:     {[]NodeKind{KindEffort}, []NodeKind{KindAgent}},
	EdgeWorksIn:  {[]NodeKind{KindAgent}, []NodeKind{KindWorkspace}},
}

// Validate checks the snapshot's structural invariants: schema version,
// unique typed nodes, well-formed edges, consistent counts and resolvable
// cross-references. It returns every problem found (the first
// maxValidateErrors individually) joined into one error, or nil.
func (s *Snapshot) Validate() error {
	if s == nil {
		return errors.New("snapshot: nil")
	}
	v := &validator{}

	if s.Schema != SchemaVersion {
		v.addf("schema: got %d, want %d", s.Schema, SchemaVersion)
	}

	kinds := make(map[string]NodeKind, len(s.Nodes))
	for i, n := range s.Nodes {
		if n.ID == "" {
			v.addf("node id: node %d has an empty id", i)
			continue
		}
		if _, dup := kinds[n.ID]; dup {
			v.addf("node id: duplicate id %q", n.ID)
			continue
		}
		kinds[n.ID] = n.Kind
		v.checkNodeKind(n)
	}

	// isKind reports whether id names an existing node of one of ks.
	isKind := func(id string, ks ...NodeKind) bool {
		k, ok := kinds[id]
		return ok && slices.Contains(ks, k)
	}

	for _, e := range s.Edges {
		srcKind, srcOK := kinds[e.Source]
		dstKind, dstOK := kinds[e.Target]
		if !srcOK {
			v.addf("edge source: %s edge %q -> %q: source not found", e.Kind, e.Source, e.Target)
		}
		if !dstOK {
			v.addf("edge target: %s edge %q -> %q: target not found", e.Kind, e.Source, e.Target)
		}
		ends, ok := edgeEndpoints[e.Kind]
		if !ok {
			v.addf("edge kind: unknown kind %q on %q -> %q", e.Kind, e.Source, e.Target)
			continue
		}
		if srcOK && dstOK && (!slices.Contains(ends.src, srcKind) || !slices.Contains(ends.dst, dstKind)) {
			v.addf("edge endpoints: %s edge %q (%s) -> %q (%s) joins the wrong kinds", e.Kind, e.Source, srcKind, e.Target, dstKind)
		}
	}

	// Counts.
	if s.Days > 0 && len(s.Counts.Days) != s.Days {
		v.addf("counts days: got %d days, want %d", len(s.Counts.Days), s.Days)
	}
	for _, id := range sortedKeys(s.Counts.Activity) {
		if got := len(s.Counts.Activity[id]); got != len(s.Counts.Days) {
			v.addf("counts activity: %q has %d entries, want %d", id, got, len(s.Counts.Days))
		}
		if !isKind(id, KindEffort) {
			v.addf("counts key: activity key %q is not an effort node", id)
		}
	}
	for _, id := range sortedKeys(s.Counts.PRStates) {
		if !isKind(id, KindEffort) {
			v.addf("counts key: pr_states key %q is not an effort node", id)
		}
	}
	for _, id := range sortedKeys(s.Counts.Agents) {
		if !isKind(id, KindEffort) {
			v.addf("counts key: agents key %q is not an effort node", id)
		}
	}

	// Cross-references.
	for _, n := range s.Nodes {
		switch {
		case n.Workspace != nil:
			if !isKind(n.Workspace.EffortID, KindEffort) {
				v.addf("reference: workspace %q effort_id %q is not an effort node", n.ID, n.Workspace.EffortID)
			}
		case n.PR != nil:
			if !isKind(n.PR.BookmarkID, KindBookmark) {
				v.addf("reference: pr %q bookmark_id %q is not a bookmark node", n.ID, n.PR.BookmarkID)
			}
		case n.Agent != nil:
			if !isKind(n.Agent.EffortID, KindEffort) {
				v.addf("reference: agent %q effort_id %q is not an effort node", n.ID, n.Agent.EffortID)
			}
			// An empty WorkspaceID is a session outside any discovered workspace.
			if id := n.Agent.WorkspaceID; id != "" && !isKind(id, KindWorkspace) {
				v.addf("reference: agent %q workspace_id %q is not a workspace node", n.ID, id)
			}
		case n.Effort != nil:
			for _, cf := range n.Effort.Conflicts {
				if !isKind(cf.WorkspaceID, KindWorkspace) {
					v.addf("reference: effort %q conflict workspace_id %q is not a workspace node", n.ID, cf.WorkspaceID)
				}
			}
		}
	}
	return v.err()
}

// checkNodeKind checks that n has a known kind and exactly the matching
// kind-specific field set.
func (v *validator) checkNodeKind(n Node) {
	set := map[NodeKind]bool{
		KindEffort:    n.Effort != nil,
		KindWorkspace: n.Workspace != nil,
		KindBookmark:  n.Bookmark != nil,
		KindPR:        n.PR != nil,
		KindTicket:    n.Ticket != nil,
		KindAgent:     n.Agent != nil,
	}
	if _, known := set[n.Kind]; !known {
		v.addf("node kind: %q has unknown kind %q", n.ID, n.Kind)
		return
	}
	var present []NodeKind
	for _, k := range []NodeKind{KindEffort, KindWorkspace, KindBookmark, KindPR, KindTicket, KindAgent} {
		if set[k] {
			present = append(present, k)
		}
	}
	if len(present) != 1 || present[0] != n.Kind {
		v.addf("node kind: %q of kind %s must carry exactly its %s info, has %v", n.ID, n.Kind, n.Kind, present)
		return
	}
	if n.Agent != nil {
		switch n.Agent.Liveness {
		case LivenessActive, LivenessLive, LivenessUncertain:
		default:
			v.addf("node kind: agent %q has unknown liveness %q", n.ID, n.Agent.Liveness)
		}
	}
}

type validator struct {
	errs  []error
	extra int
}

func (v *validator) addf(format string, args ...any) {
	if len(v.errs) >= maxValidateErrors {
		v.extra++
		return
	}
	v.errs = append(v.errs, fmt.Errorf(format, args...))
}

func (v *validator) err() error {
	if v.extra > 0 {
		v.errs = append(v.errs, fmt.Errorf("and %d more", v.extra))
	}
	return errors.Join(v.errs...)
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
