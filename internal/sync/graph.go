package sync

import (
	"errors"
	"fmt"
	"sort"

	"github.com/virtru/wgo/internal/jj"
)

// ErrCycle is returned when a graph operation encounters a cycle. With a jj
// DAG this should be impossible; we keep the sentinel for defensive checks
// in case of template-parse glitches.
var ErrCycle = errors.New("sync: cycle detected in jj DAG")

// Node is a single bookmarked change in a per-repo jj DAG.
type Node struct {
	Bookmark string   // local bookmark name (the "branch" the PR is on)
	ChangeID string   // jj change id
	Parents  []string // bookmark names of in-graph ancestors (nearest reachable)
}

// Graph is a per-repo DAG built from jj log output. Only changes that hold
// a local bookmark become nodes — the rest are collapsed into edges by
// walking parent change_ids until we hit another bookmark or root().
type Graph struct {
	// Nodes is keyed by bookmark name.
	Nodes map[string]*Node
	// Children is the reverse adjacency, precomputed at build time.
	Children map[string][]string
}

// BuildFromLog constructs a Graph from jj log entries that should already
// have been filtered to `bookmarks() & ::heads()` (every entry's commit
// either has a bookmark itself or is on the path between two bookmarked
// commits). Entries without bookmarks are walked through to collapse the
// in-between commits into direct bookmark→bookmark edges.
func BuildFromLog(entries []jj.LogEntry) (*Graph, error) {
	byChange := make(map[string]jj.LogEntry, len(entries))
	for _, e := range entries {
		byChange[e.ChangeID] = e
	}

	g := &Graph{
		Nodes:    map[string]*Node{},
		Children: map[string][]string{},
	}

	for _, e := range entries {
		if len(e.Bookmarks) == 0 {
			continue
		}
		bm := e.Bookmarks[0] // take first bookmark; multi-bookmark changes are rare
		node := &Node{Bookmark: bm, ChangeID: e.ChangeID}
		for _, parentChangeID := range e.Parents {
			ancestor := walkToBookmark(byChange, parentChangeID)
			if ancestor == "" || ancestor == bm {
				continue
			}
			node.Parents = append(node.Parents, ancestor)
		}
		// Deduplicate parents (a diamond merge might surface the same ancestor twice).
		node.Parents = dedup(node.Parents)
		g.Nodes[bm] = node
	}

	for child, n := range g.Nodes {
		for _, p := range n.Parents {
			g.Children[p] = append(g.Children[p], child)
		}
	}
	for k := range g.Children {
		sort.Strings(g.Children[k])
	}

	if cycle := findCycle(g); cycle != nil {
		return nil, fmt.Errorf("%w: %v", ErrCycle, cycle)
	}
	return g, nil
}

// walkToBookmark follows parent change_ids in byChange until it finds an
// entry whose bookmarks list is non-empty, returning the first bookmark.
// Returns "" if no bookmarked ancestor is reachable (e.g. the chain runs
// off the end of the log set into root()).
func walkToBookmark(byChange map[string]jj.LogEntry, changeID string) string {
	visited := map[string]bool{}
	for changeID != "" && !visited[changeID] {
		visited[changeID] = true
		e, ok := byChange[changeID]
		if !ok {
			return ""
		}
		if len(e.Bookmarks) > 0 {
			return e.Bookmarks[0]
		}
		if len(e.Parents) == 0 {
			return ""
		}
		changeID = e.Parents[0] // follow first parent on linear walks
	}
	return ""
}

// Roots returns nodes with no in-graph parent — bookmarks that base directly
// off the trunk (e.g. main).
func (g *Graph) Roots() []string {
	var roots []string
	for name, n := range g.Nodes {
		if len(n.Parents) == 0 {
			roots = append(roots, name)
		}
	}
	sort.Strings(roots)
	return roots
}

// TopoSort returns bookmark names in topological order (roots first).
func (g *Graph) TopoSort() ([]string, error) {
	indegree := make(map[string]int, len(g.Nodes))
	for name := range g.Nodes {
		indegree[name] = 0
	}
	for _, n := range g.Nodes {
		for _, p := range n.Parents {
			if _, ok := g.Nodes[p]; ok {
				indegree[n.Bookmark]++
			}
		}
	}

	var queue []string
	for name, deg := range indegree {
		if deg == 0 {
			queue = append(queue, name)
		}
	}
	sort.Strings(queue)

	var order []string
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		order = append(order, name)
		children := append([]string(nil), g.Children[name]...)
		sort.Strings(children)
		for _, c := range children {
			indegree[c]--
			if indegree[c] == 0 {
				queue = append(queue, c)
			}
		}
	}
	if len(order) != len(g.Nodes) {
		return nil, ErrCycle
	}
	return order, nil
}

// NearestAncestorWith returns the nearest ancestor bookmark for which pred
// returns true. Walks parent chains breadth-first. Returns "" if none match.
func (g *Graph) NearestAncestorWith(bookmark string, pred func(string) bool) string {
	visited := map[string]bool{bookmark: true}
	queue := append([]string(nil), g.Nodes[bookmark].Parents...)
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		if visited[next] {
			continue
		}
		visited[next] = true
		if pred(next) {
			return next
		}
		if n, ok := g.Nodes[next]; ok {
			queue = append(queue, n.Parents...)
		}
	}
	return ""
}

func dedup(in []string) []string {
	if len(in) <= 1 {
		return in
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func findCycle(g *Graph) []string {
	const (
		unvisited = 0
		visiting  = 1
		done      = 2
	)
	color := make(map[string]int, len(g.Nodes))
	var path []string

	var dfs func(string) []string
	dfs = func(node string) []string {
		switch color[node] {
		case visiting:
			start := 0
			for i, n := range path {
				if n == node {
					start = i
					break
				}
			}
			return append(append([]string{}, path[start:]...), node)
		case done:
			return nil
		}
		color[node] = visiting
		path = append(path, node)
		for _, p := range g.Nodes[node].Parents {
			if _, ok := g.Nodes[p]; !ok {
				continue
			}
			if cycle := dfs(p); cycle != nil {
				return cycle
			}
		}
		path = path[:len(path)-1]
		color[node] = done
		return nil
	}

	names := make([]string, 0, len(g.Nodes))
	for name := range g.Nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, n := range names {
		if color[n] == unvisited {
			if cycle := dfs(n); cycle != nil {
				return cycle
			}
		}
	}
	return nil
}
