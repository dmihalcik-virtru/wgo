// Package stack models PR stacks as a directed acyclic graph over branch
// annotations. A stack is identified by Annotation.StackID; edges come from
// Annotation.Parents. The graph itself is rebuilt on demand from state, so
// there is no separate edge table to keep in sync.
package stack

import (
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/virtru/wgo/internal/store"
)

// ErrCycle is returned when a graph operation encounters a cycle.
var ErrCycle = errors.New("stack: cycle detected")

// Graph is a rooted DAG built from a single stack's annotations.
// Keys are annotation keys ("repoPath:branch"); values are the parent keys
// recorded in store.Annotation.Parents.
type Graph struct {
	StackID string
	Parents map[string][]string // node -> parents
	// Children is the reverse adjacency, computed at build time so descendant
	// walks stay O(n) regardless of stack size.
	Children map[string][]string
}

// Build constructs a Graph from every annotation whose StackID matches.
// Parents that point at branches outside the stack are kept in the parents
// list — callers (notably restack) treat those as "external bases" and
// query their tips directly rather than rebasing them. Cycles are detected
// up front so downstream walks can assume acyclicity.
func Build(state *store.State, stackID string) (*Graph, error) {
	if state == nil {
		return nil, errors.New("stack: nil state")
	}
	if stackID == "" {
		return nil, errors.New("stack: empty stack id")
	}

	g := &Graph{
		StackID:  stackID,
		Parents:  make(map[string][]string),
		Children: make(map[string][]string),
	}

	for _, key := range state.AnnotationsInStack(stackID) {
		ann := state.Annotations[key]
		// Dedup defensively. SetParents drops duplicates on write, but state
		// produced by an older wgo or hand-edited state.json could still have
		// repeats — and a duplicated parent would push the child's indegree
		// above its in-edge count and trigger a false ErrCycle in TopoSort.
		parents := dedupParents(ann.Parents)
		g.Parents[key] = parents
		for _, p := range parents {
			// Only register reverse edges between in-stack nodes. External
			// parents (e.g. a branch in a different stack, or origin/main as
			// the implicit root) don't need a children list.
			if _, inStack := g.Parents[p]; inStack {
				g.Children[p] = append(g.Children[p], key)
				continue
			}
			// Parent may not have been visited yet — record a placeholder so
			// the second pass below can backfill the reverse edge.
		}
	}

	// Second pass: backfill reverse edges for in-stack parents we hadn't
	// seen during the first pass (map iteration order is random).
	for child, parents := range g.Parents {
		for _, p := range parents {
			if _, inStack := g.Parents[p]; !inStack {
				continue
			}
			if !slices.Contains(g.Children[p], child) {
				g.Children[p] = append(g.Children[p], child)
			}
		}
	}

	// Deterministic order for children lists keeps test output and
	// restack walks stable across runs.
	for k := range g.Children {
		sort.Strings(g.Children[k])
	}

	if cycle := findCycle(g); cycle != nil {
		return nil, fmt.Errorf("%w: %v", ErrCycle, cycle)
	}
	return g, nil
}

// Roots returns the in-stack nodes that have no in-stack parent. These are
// the branches that rebase onto the stack's RootRef (e.g. origin/main).
func (g *Graph) Roots() []string {
	var roots []string
	for node, parents := range g.Parents {
		isRoot := true
		for _, p := range parents {
			if _, inStack := g.Parents[p]; inStack {
				isRoot = false
				break
			}
		}
		if isRoot {
			roots = append(roots, node)
		}
	}
	sort.Strings(roots)
	return roots
}

// Leaves returns the in-stack nodes that have no in-stack children.
func (g *Graph) Leaves() []string {
	var leaves []string
	for node := range g.Parents {
		if len(g.Children[node]) == 0 {
			leaves = append(leaves, node)
		}
	}
	sort.Strings(leaves)
	return leaves
}

// TopoSort returns all in-stack nodes in dependency order: every node appears
// after all of its in-stack parents. Returns ErrCycle if Build's cycle check
// missed one (defensive — Build rejects cycles).
func (g *Graph) TopoSort() ([]string, error) {
	indegree := make(map[string]int, len(g.Parents))
	for node, parents := range g.Parents {
		count := 0
		for _, p := range parents {
			if _, inStack := g.Parents[p]; inStack {
				count++
			}
		}
		indegree[node] = count
	}

	// Kahn's algorithm with a sorted ready queue for deterministic output.
	var ready []string
	for node, d := range indegree {
		if d == 0 {
			ready = append(ready, node)
		}
	}
	sort.Strings(ready)

	var order []string
	for len(ready) > 0 {
		node := ready[0]
		ready = ready[1:]
		order = append(order, node)
		for _, child := range g.Children[node] {
			indegree[child]--
			if indegree[child] == 0 {
				ready = append(ready, child)
				sort.Strings(ready)
			}
		}
	}

	if len(order) != len(g.Parents) {
		return nil, ErrCycle
	}
	return order, nil
}

// AffectedDescendants returns every in-stack node reachable from root via
// the Children adjacency, in topological order, excluding the root itself.
// Use this to determine which branches need rebasing when `root` has moved.
func (g *Graph) AffectedDescendants(root string) ([]string, error) {
	if _, ok := g.Parents[root]; !ok {
		return nil, fmt.Errorf("stack: node %q not in stack %q", root, g.StackID)
	}

	visited := map[string]bool{root: true}
	var collect func(string)
	collect = func(node string) {
		for _, child := range g.Children[node] {
			if visited[child] {
				continue
			}
			visited[child] = true
			collect(child)
		}
	}
	collect(root)

	order, err := g.TopoSort()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, node := range order {
		if visited[node] && node != root {
			out = append(out, node)
		}
	}
	return out, nil
}

// WouldCreateCycle reports whether adding the edge child -> parent (i.e.
// recording `parent` in child.Parents) would close a cycle. Used by
// `wgo stack push --on` before persisting state.
func WouldCreateCycle(state *store.State, stackID, childKey, parentKey string) bool {
	if childKey == parentKey {
		return true
	}
	g, err := Build(state, stackID)
	if err != nil {
		// If the existing graph already has a cycle, any addition is unsafe.
		return true
	}
	// Cycle iff parent is already reachable as a descendant of child.
	descendants, err := g.AffectedDescendants(childKey)
	if err != nil {
		// child isn't in the stack yet — no descendants, no cycle possible.
		return false
	}
	return slices.Contains(descendants, parentKey)
}

func findCycle(g *Graph) []string {
	const (
		white = 0 // unvisited
		gray  = 1 // on current DFS stack
		black = 2 // fully explored
	)
	color := make(map[string]int, len(g.Parents))
	var path []string
	var dfs func(node string) []string

	dfs = func(node string) []string {
		color[node] = gray
		path = append(path, node)
		for _, child := range g.Children[node] {
			switch color[child] {
			case gray:
				// Found a back edge — extract the cycle for the error message.
				for i, n := range path {
					if n == child {
						return append([]string(nil), path[i:]...)
					}
				}
			case white:
				if cyc := dfs(child); cyc != nil {
					return cyc
				}
			}
		}
		color[node] = black
		path = path[:len(path)-1]
		return nil
	}

	// Iterate in sorted order so the reported cycle is deterministic across runs.
	keys := make([]string, 0, len(g.Parents))
	for k := range g.Parents {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, node := range keys {
		if color[node] == white {
			if cyc := dfs(node); cyc != nil {
				return cyc
			}
		}
	}
	return nil
}

// dedupParents preserves first-occurrence order. Tiny helper; not worth
// pulling another package import just to share with store.dedupStrings.
func dedupParents(in []string) []string {
	if len(in) == 0 {
		return nil
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
