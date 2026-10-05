package plan

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/virtru/wgo/internal/bujo"
)

// Owned section names. Only the first occurrence of each is managed; a
// repeated one is kept verbatim like any unknown section.
const (
	sectionTasks          = "Tasks"
	sectionActiveBranches = "Active Branches"
	sectionEfforts        = "Efforts"
	sectionNotes          = "Notes"
)

// ownedOrder is the order new owned sections are inserted in when a file
// lacks them.
var ownedOrder = []string{sectionTasks, sectionActiveBranches, sectionEfforts, sectionNotes}

// freshPlan is the document a Plan built in code (not via Parse) renders on.
const freshPlan = "# Plan\n\n## Active Branches\n\n## Notes\n"

// document is the raw line model of a plan file.
type document struct {
	eol             string // "\n" or "\r\n"
	trailingNewline bool
	trailingBlank   int      // blank lines at the end of the file
	preamble        []string // lines before the first "## " heading
	sections        []*section
}

// section is one "## " heading and the raw lines below it.
type section struct {
	header string // raw heading line
	name   string // heading text, trimmed
	owned  bool   // true only for the first occurrence of an owned name
	body   []string
	start  int // 0-based line index of the heading in the file

	// Tasks: line indices (into body) of task lines and their canonical form.
	// Active Branches: the same for branch entries, plus their keys.
	entryIdx  []int
	entryKeys []string
	branchKey []string

	// Notes: the parsed (trimmed) text.
	notes string

	// Efforts: lines before the first "### " heading, then the blocks.
	intro  []string
	blocks []*effortBlock
}

// effortBlock is one "### " heading and the raw lines up to the next one.
type effortBlock struct {
	lines   []string // lines[0] is the heading
	id      string
	primary bool // false for a repeated heading, which is kept verbatim
	parsed  EffortEntry

	descStart, descEnd int   // description line range [start, end) in lines; start == -1 when none
	branchIdx          []int // indices in lines of "- repo:bookmark" entries
}

// fenceState tracks fenced code blocks (``` or ~~~) so headings and list
// items inside them are never interpreted.
type fenceState struct {
	char  byte
	count int
}

// fenceMarker reports the fence character and run length if line opens or
// closes a fence (up to three spaces of indentation, three or more ` or ~).
func fenceMarker(line string) (byte, int, string) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) < 3 {
		return 0, 0, ""
	}
	c := trimmed[0]
	if c != '`' && c != '~' {
		return 0, 0, ""
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == c {
		n++
	}
	if n < 3 {
		return 0, 0, ""
	}
	return c, n, trimmed[n:]
}

// step advances the fence state over line and reports whether line is part
// of a fence (a delimiter or fenced content).
func (f *fenceState) step(line string) bool {
	c, n, rest := fenceMarker(line)
	if f.char == 0 {
		if c != 0 {
			f.char, f.count = c, n
			return true
		}
		return false
	}
	if c == f.char && n >= f.count && strings.TrimSpace(rest) == "" {
		f.char, f.count = 0, 0
	}
	return true
}

// headingText returns the text of an ATX heading of exactly the given level
// ("## " or "### ") with at most three spaces of indentation.
func headingText(line, marker string) (string, bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return "", false
	}
	trimmed = strings.TrimRight(trimmed, " \t\r")
	if trimmed == strings.TrimSpace(marker) {
		return "", true
	}
	if !strings.HasPrefix(trimmed, marker) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(trimmed, marker)), true
}

// splitLines splits content into lines without their endings and detects
// the line ending to write back. Mixed endings are normalized to the
// majority ending and reported.
func splitLines(content string) (lines []string, eol string, trailing bool, mixed bool) {
	if content == "" {
		return nil, "\n", true, false
	}
	raw := strings.Split(content, "\n")
	trailing = strings.HasSuffix(content, "\n")
	if trailing {
		raw = raw[:len(raw)-1]
	}
	crlf, lf := 0, 0
	for i, l := range raw {
		hasEnding := i < len(raw)-1 || trailing
		if strings.HasSuffix(l, "\r") {
			raw[i] = strings.TrimSuffix(l, "\r")
			if hasEnding {
				crlf++
			}
		} else if hasEnding {
			lf++
		}
	}
	eol = "\n"
	if crlf > lf {
		eol = "\r\n"
	}
	return raw, eol, trailing, crlf > 0 && lf > 0
}

// parseDocument builds the raw model of content and fills p's parsed view.
func parseDocument(content string, p *Plan) *document {
	lines, eol, trailing, mixed := splitLines(content)
	d := &document{eol: eol, trailingNewline: trailing, trailingBlank: countTrailingBlank(lines)}
	if mixed {
		p.diag(0, "mixed line endings; the plan is written back with %q endings", eol)
	}

	// Mark fenced lines across the whole file so a fence that spans a
	// heading-looking line never splits a section.
	fenced := make([]bool, len(lines))
	var fs fenceState
	for i, l := range lines {
		fenced[i] = fs.step(l)
	}
	if fs.char != 0 {
		p.diag(0, "unclosed code fence; everything after it is kept verbatim")
	}

	var cur *section
	seen := map[string]int{}
	for i, l := range lines {
		if !fenced[i] {
			if name, ok := headingText(l, "## "); ok {
				cur = &section{header: l, name: name, start: i}
				switch name {
				case sectionTasks, sectionActiveBranches, sectionEfforts, sectionNotes:
					if first, dup := seen[name]; dup {
						p.diag(i, "duplicate %q section (first at line %d); only the first is managed, this one is kept verbatim", "## "+name, first+1)
					} else {
						seen[name] = i
						cur.owned = true
					}
				}
				d.sections = append(d.sections, cur)
				continue
			}
		}
		if cur == nil {
			d.preamble = append(d.preamble, l)
		} else {
			cur.body = append(cur.body, l)
		}
	}

	for idx, sec := range d.sections {
		bodyFenced := fenced[sec.start+1 : sec.start+1+len(sec.body)]
		if !sec.owned {
			p.UnmanagedSections = append(p.UnmanagedSections, Section{
				Header:  sec.header,
				Content: strings.Join(sec.body, "\n"),
				Index:   idx,
			})
			continue
		}
		switch sec.name {
		case sectionTasks:
			for j, l := range sec.body {
				if bodyFenced[j] {
					continue
				}
				if task := bujo.ParseTask(l); task != nil && task.Text != "" {
					p.Tasks = append(p.Tasks, *task)
					sec.entryIdx = append(sec.entryIdx, j)
					sec.entryKeys = append(sec.entryKeys, task.Render())
				}
			}
		case sectionActiveBranches:
			for j, l := range sec.body {
				if bodyFenced[j] {
					continue
				}
				entry, ok := parseActiveBranchLine(l)
				if !ok {
					continue
				}
				key := entry.Repo + ":" + entry.Branch
				if _, dup := p.ActiveBranches[key]; dup {
					p.diag(sec.start+1+j, "duplicate active branch %s; kept verbatim", key)
					continue
				}
				p.ActiveBranches[key] = entry
				sec.entryIdx = append(sec.entryIdx, j)
				sec.entryKeys = append(sec.entryKeys, renderBranchLine(entry))
				sec.branchKey = append(sec.branchKey, key)
			}
		case sectionNotes:
			sec.notes = strings.TrimSpace(strings.Join(sec.body, "\n"))
			p.Notes = sec.notes
		case sectionEfforts:
			p.parseEfforts(sec, bodyFenced)
		}
	}
	p.sortDiagnostics()
	return d
}

// parseEfforts splits the Efforts body into its intro and "### " blocks.
func (p *Plan) parseEfforts(sec *section, fenced []bool) {
	var blk *effortBlock
	var blkStart int
	finish := func() {
		if blk != nil {
			p.finalizeEffort(blk, sec.start+1+blkStart, fenced[blkStart:blkStart+len(blk.lines)])
			sec.blocks = append(sec.blocks, blk)
		}
	}
	for j, l := range sec.body {
		if !fenced[j] {
			if _, ok := headingText(l, "### "); ok {
				finish()
				blk = &effortBlock{lines: []string{l}}
				blkStart = j
				continue
			}
		}
		if blk == nil {
			sec.intro = append(sec.intro, l)
		} else {
			blk.lines = append(blk.lines, l)
		}
	}
	finish()
}

// finalizeEffort parses one effort block. start is the 0-based file line of
// its heading. Nothing in the block is discarded: lines it cannot interpret
// stay in blk.lines and are reported.
func (p *Plan) finalizeEffort(blk *effortBlock, start int, fenced []bool) {
	name, _ := headingText(blk.lines[0], "### ")
	entry := EffortEntry{Name: name}
	blk.descStart, blk.descEnd = -1, -1

	if name == "" {
		p.diag(start, "effort heading without a name; kept verbatim")
	}

	// Consecutive stray lines (a fenced block counts as one run, blank lines
	// included) are reported once, at their first line.
	strayAt, strayN := -1, 0
	flushStray := func() {
		if strayN == 0 {
			return
		}
		first := strings.TrimSpace(blk.lines[strayAt])
		if strayN == 1 {
			p.diag(start+strayAt, "effort %q: stray text %q after its entries; kept verbatim", name, first)
		} else {
			p.diag(start+strayAt, "effort %q: stray text %q and %d more lines after its entries; kept verbatim", name, first, strayN-1)
		}
		strayAt, strayN = -1, 0
	}
	for i := 1; i < len(blk.lines); i++ {
		l := blk.lines[i]
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			if !fenced[i] {
				flushStray()
			}
			continue
		}
		if !fenced[i] && strings.HasPrefix(trimmed, "- ") {
			flushStray()
			ref := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			repo, bookmark, ok := strings.Cut(ref, ":")
			if !ok || strings.TrimSpace(repo) == "" || strings.TrimSpace(bookmark) == "" || strings.ContainsAny(ref, " \t") {
				p.diag(start+i, "effort %q: malformed entry %q (want - repo:bookmark); kept verbatim", name, trimmed)
				continue
			}
			entry.Branches = append(entry.Branches, ref)
			blk.branchIdx = append(blk.branchIdx, i)
			continue
		}
		if len(blk.branchIdx) == 0 {
			// Text before the first entry is the description.
			if blk.descStart == -1 {
				blk.descStart = i
			}
			blk.descEnd = i + 1
			continue
		}
		if strayN == 0 {
			strayAt = i
		}
		strayN++
	}
	flushStray()
	if blk.descStart != -1 {
		entry.Description = strings.TrimSpace(strings.Join(blk.lines[blk.descStart:blk.descEnd], "\n"))
	}

	entry.ID = GenerateEffortID(name)
	blk.id = entry.ID
	blk.parsed = cloneEffort(entry)
	if _, dup := p.Efforts[entry.ID]; dup || name == "" {
		if name != "" {
			p.diag(start, "duplicate effort heading %q; the effort is ambiguous, only the first block is used and this one is kept verbatim", "### "+name)
		}
		return
	}
	blk.primary = true
	p.Efforts[entry.ID] = cloneEffort(entry)
	p.EffortOrder = append(p.EffortOrder, entry.ID)
}

// diag records a diagnostic. line is a 0-based file line index, or 0 for a
// file-level message.
func (p *Plan) diag(line int, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if line > 0 {
		msg = fmt.Sprintf("plan line %d: %s", line+1, msg)
	} else {
		msg = "plan: " + msg
	}
	p.Diagnostics = append(p.Diagnostics, msg)
	p.diagLines = append(p.diagLines, line)
}

// sortDiagnostics orders diagnostics by file line, file-level ones first.
func (p *Plan) sortDiagnostics() {
	idx := make([]int, len(p.Diagnostics))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return p.diagLines[idx[a]] < p.diagLines[idx[b]] })
	diags := make([]string, len(idx))
	lines := make([]int, len(idx))
	for i, j := range idx {
		diags[i], lines[i] = p.Diagnostics[j], p.diagLines[j]
	}
	p.Diagnostics, p.diagLines = diags, lines
}

func cloneEffort(e EffortEntry) EffortEntry {
	e.Branches = slices.Clone(e.Branches)
	return e
}

func effortEqual(a, b EffortEntry) bool {
	return a.Name == b.Name && a.Description == b.Description && slices.Equal(a.Branches, b.Branches)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// render writes the document back. extra, when non-empty, is a generated
// section inserted after Active Branches (see RenderWithPair).
func (p *Plan) render(extra []string) string {
	d := p.doc
	if d == nil {
		// A plan built in code renders onto the default skeleton.
		blank := &Plan{ActiveBranches: map[string]BranchEntry{}, Efforts: map[string]EffortEntry{}}
		d = parseDocument(freshPlan, blank)
	}

	var out []string
	out = append(out, d.preamble...)

	present := map[string]bool{}
	for _, sec := range d.sections {
		if sec.owned {
			present[sec.name] = true
		}
	}

	// Missing owned sections that now have content are inserted before the
	// next owned section in ownedOrder, or after the last one.
	pending := map[string][]string{}
	for _, name := range ownedOrder {
		if !present[name] {
			if lines := p.freshSection(name); lines != nil {
				pending[name] = lines
			}
		}
	}
	insertBefore := map[string][]string{}
	var trailingNew []string
	for i, name := range ownedOrder {
		lines, ok := pending[name]
		if !ok {
			continue
		}
		anchor := ""
		for _, next := range ownedOrder[i+1:] {
			if present[next] {
				anchor = next
				break
			}
		}
		if anchor != "" {
			insertBefore[anchor] = append(insertBefore[anchor], lines...)
		} else {
			trailingNew = append(trailingNew, lines...)
		}
	}

	extraDone := len(extra) == 0
	for _, sec := range d.sections {
		if sec.owned {
			out = append(out, insertBefore[sec.name]...)
			if !extraDone && sec.name == sectionNotes {
				out = append(out, extra...)
				extraDone = true
			}
		}
		out = append(out, sec.header)
		out = append(out, p.renderBody(sec)...)
		if !extraDone && sec.owned && sec.name == sectionActiveBranches {
			out = ensureBlankEnd(out)
			out = append(out, extra...)
			extraDone = true
		}
	}
	if !extraDone {
		trailingNew = append(extra, trailingNew...)
	}
	if len(trailingNew) > 0 {
		out = ensureBlankEnd(out)
		out = append(out, trailingNew...)
	}
	// Content added at the end of the file must not grow its trailing blank
	// lines: keep exactly as many as the file had.
	for countTrailingBlank(out) > d.trailingBlank {
		out = out[:len(out)-1]
	}

	s := strings.Join(out, d.eol)
	if d.trailingNewline || len(trailingNew) > 0 {
		s += d.eol
	}
	return s
}

func countTrailingBlank(lines []string) int {
	n := 0
	for n < len(lines) && strings.TrimSpace(lines[len(lines)-1-n]) == "" {
		n++
	}
	return n
}

// ensureBlankEnd makes sure out ends with a blank line, so appended content
// does not run into the previous paragraph.
func ensureBlankEnd(out []string) []string {
	if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
		out = append(out, "")
	}
	return out
}

// renderBody renders the lines below a section heading.
func (p *Plan) renderBody(sec *section) []string {
	if !sec.owned {
		return sec.body
	}
	switch sec.name {
	case sectionTasks:
		keys := make([]string, len(p.Tasks))
		for i := range p.Tasks {
			keys[i] = p.Tasks[i].Render()
		}
		return patchList(sec.body, sec.entryIdx, sec.entryKeys, keys, listAnchor(sec.body))
	case sectionActiveBranches:
		keys := p.branchKeysInOrder(sec.branchKey)
		lines := make([]string, len(keys))
		for i, k := range keys {
			lines[i] = renderBranchLine(p.ActiveBranches[k])
		}
		return patchList(sec.body, sec.entryIdx, sec.entryKeys, lines, listAnchor(sec.body))
	case sectionNotes:
		if p.Notes == sec.notes {
			return sec.body
		}
		if p.Notes == "" {
			return nil
		}
		body := append([]string{""}, strings.Split(p.Notes, "\n")...)
		if n := len(sec.body); n > 0 && strings.TrimSpace(sec.body[n-1]) == "" {
			body = append(body, "")
		}
		return body
	case sectionEfforts:
		return p.renderEfforts(sec)
	}
	return sec.body
}

// branchKeysInOrder returns Active Branches keys: those already in the file
// in file order, then new ones sorted, so rendering is deterministic.
func (p *Plan) branchKeysInOrder(fileKeys []string) []string {
	var keys []string
	seen := map[string]bool{}
	for _, k := range fileKeys {
		if _, ok := p.ActiveBranches[k]; ok && !seen[k] {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	for _, k := range sortedKeys(p.ActiveBranches) {
		if k != "" && !seen[k] {
			keys = append(keys, k)
		}
	}
	return keys
}

// freshSection renders an owned section the file does not have yet, or nil
// when it would be empty.
func (p *Plan) freshSection(name string) []string {
	var body []string
	switch name {
	case sectionTasks:
		for i := range p.Tasks {
			body = append(body, p.Tasks[i].Render())
		}
	case sectionActiveBranches:
		for _, k := range p.branchKeysInOrder(nil) {
			body = append(body, renderBranchLine(p.ActiveBranches[k]))
		}
	case sectionNotes:
		if p.Notes != "" {
			body = strings.Split(p.Notes, "\n")
		}
	case sectionEfforts:
		for _, e := range p.newEfforts(nil) {
			body = append(body, renderEffort(e)...)
		}
		if len(body) > 0 {
			body = body[:len(body)-1] // renderEffort ends with a blank line
		}
	}
	if len(body) == 0 {
		return nil
	}
	lines := append([]string{"## " + name, ""}, body...)
	return append(lines, "")
}

// newEfforts returns efforts not present in the file, sorted by name.
func (p *Plan) newEfforts(sec *section) []EffortEntry {
	inFile := map[string]bool{}
	if sec != nil {
		for _, b := range sec.blocks {
			if b.primary {
				inFile[b.id] = true
			}
		}
	}
	var out []EffortEntry
	for id, e := range p.Efforts {
		if !inFile[id] {
			if e.ID == "" {
				e.ID = id
			}
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// renderEfforts renders the Efforts body: the intro verbatim, each block
// verbatim unless its effort changed, removed efforts dropped, then new
// efforts by name.
func (p *Plan) renderEfforts(sec *section) []string {
	out := slices.Clone(sec.intro)
	for _, b := range sec.blocks {
		if !b.primary {
			out = append(out, b.lines...)
			continue
		}
		cur, ok := p.Efforts[b.id]
		if !ok {
			continue // removed by a command
		}
		if effortEqual(cur, b.parsed) {
			out = append(out, b.lines...)
			continue
		}
		out = append(out, patchEffortBlock(b, cur)...)
	}
	added := p.newEfforts(sec)
	if len(added) > 0 {
		// Keep the section's trailing blank lines after the new blocks.
		trail := 0
		for trail < len(out) && strings.TrimSpace(out[len(out)-1-trail]) == "" {
			trail++
		}
		tail := slices.Clone(out[len(out)-trail:])
		out = out[:len(out)-trail]
		out = ensureBlankEnd(out)
		if len(out) == 0 {
			out = append(out, "")
		}
		for _, e := range added {
			out = append(out, renderEffort(e)...)
		}
		out = out[:len(out)-1]
		if len(tail) == 0 {
			tail = []string{""}
		}
		out = append(out, tail...)
	}
	return out
}

// renderEffort renders a new effort block, ending with a blank line.
func renderEffort(e EffortEntry) []string {
	lines := []string{"### " + e.Name}
	if e.Description != "" {
		lines = append(lines, strings.Split(e.Description, "\n")...)
		if len(e.Branches) > 0 {
			lines = append(lines, "")
		}
	}
	for _, b := range e.Branches {
		lines = append(lines, "- "+b)
	}
	return append(lines, "")
}

// patchEffortBlock applies the changes between b.parsed and cur to the raw
// block lines, keeping every line it was not asked to change.
func patchEffortBlock(b *effortBlock, cur EffortEntry) []string {
	lines := slices.Clone(b.lines)
	branchIdx := slices.Clone(b.branchIdx)
	if cur.Name != b.parsed.Name {
		lines[0] = "### " + cur.Name
	}

	descEnd := b.descEnd
	if cur.Description != b.parsed.Description {
		var desc []string
		if cur.Description != "" {
			desc = strings.Split(cur.Description, "\n")
		}
		start, end := b.descStart, b.descEnd
		repl := desc
		if start == -1 {
			start, end = 1, 1
			if len(desc) > 0 {
				repl = append(slices.Clone(desc), "")
			}
		} else if len(desc) == 0 && end < len(lines) && strings.TrimSpace(lines[end]) == "" {
			end++ // drop the blank line that separated the old description
		}
		lines = slices.Concat(lines[:start], repl, lines[end:])
		shift := len(repl) - (end - start)
		for i := range branchIdx {
			if branchIdx[i] >= end {
				branchIdx[i] += shift
			}
		}
		descEnd = -1
		if len(desc) > 0 {
			descEnd = start + len(desc)
		}
	}

	newLines := make([]string, len(cur.Branches))
	for i, br := range cur.Branches {
		newLines[i] = "- " + br
	}
	oldKeys := make([]string, len(b.parsed.Branches))
	for i, br := range b.parsed.Branches {
		oldKeys[i] = "- " + br
	}
	// Without entries, new ones go after the heading and description.
	anchor := 1
	if descEnd != -1 {
		anchor = descEnd
		if anchor < len(lines) && strings.TrimSpace(lines[anchor]) == "" {
			anchor++
		}
	}
	return patchList(lines, branchIdx, oldKeys, newLines, anchor)
}

// listAnchor returns where list entries go in a section body that has none:
// directly after the blank line under the heading, or at the top.
func listAnchor(body []string) int {
	if len(body) > 0 && strings.TrimSpace(body[0]) == "" {
		return 1
	}
	return 0
}

// patchList rewrites the list entries of body to newLines while keeping
// every other line in place. entryIdx are the body indices of the existing
// entries and oldKeys their canonical forms. Entries are aligned by longest
// common subsequence: matched entries keep their raw line, removed entries are
// dropped, and new entries are written where they belong in the sequence. When
// body has no entries, new ones are inserted at anchor.
func patchList(body []string, entryIdx []int, oldKeys, newLines []string, anchor int) []string {
	if slices.Equal(oldKeys, newLines) {
		return body
	}
	if len(entryIdx) == 0 {
		if len(newLines) == 0 {
			return body
		}
		ins := slices.Clone(newLines)
		if anchor >= len(body) {
			if anchor == 0 || strings.TrimSpace(body[len(body)-1]) != "" {
				ins = append([]string{""}, ins...)
			}
			return append(slices.Clone(body), append(ins, "")...)
		}
		if strings.TrimSpace(body[anchor]) != "" {
			ins = append(ins, "")
		}
		return slices.Concat(body[:anchor], ins, body[anchor:])
	}

	matchOld := lcsMatch(oldKeys, newLines)
	pos := make(map[int]int, len(entryIdx))
	for k, i := range entryIdx {
		pos[i] = k
	}
	last := entryIdx[len(entryIdx)-1]

	var out []string
	next := 0
	emitTo := func(j int) {
		if j > next {
			out = append(out, newLines[next:j]...)
			next = j
		}
	}
	nextMatch := func(k int) int {
		for ; k < len(matchOld); k++ {
			if matchOld[k] >= 0 {
				return matchOld[k]
			}
		}
		return len(newLines)
	}
	for i, line := range body {
		k, isEntry := pos[i]
		if !isEntry {
			out = append(out, line)
			continue
		}
		if j := matchOld[k]; j >= 0 {
			emitTo(j)
			out = append(out, line)
			next = j + 1
		} else {
			// Removed or changed: what replaces it goes here.
			emitTo(nextMatch(k + 1))
		}
		if i == last {
			emitTo(len(newLines))
		}
	}
	return out
}

// lcsMatch aligns a and b by longest common subsequence and returns, for
// each index of a, the matched index of b or -1.
func lcsMatch(a, b []string) []int {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = max(dp[i+1][j], dp[i][j+1])
			}
		}
	}
	match := make([]int, n)
	for i := range match {
		match[i] = -1
	}
	for i, j := 0, 0; i < n && j < m; {
		switch {
		case a[i] == b[j]:
			match[i] = j
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			i++
		default:
			j++
		}
	}
	return match
}
