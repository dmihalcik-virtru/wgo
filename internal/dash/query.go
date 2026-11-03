package dash

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// This file is the Go half of the dash query language (spec/gh-100.md). The
// JS half lives in internal/review/web/live.js; both run the shared vectors
// in testdata/query_vectors.json, and their error messages are identical.
//
//	query    = [ clause *( ";" clause ) ]       ; AND
//	clause   = [ "!" ] name ":" operand         ; the first ":" splits
//	operand  = "*" / range / list
//	list     = value *( "," value )             ; OR
//	range    = [ bound ] ".." [ bound ] / ( ">=" / "<=" / ">" / "<" ) bound
//	bound    = number [ "d" / "w" ] / YYYY-MM-DD / "today" / ( "-" / "+" ) digits ( "d" / "w" )
//	value    = token / DQUOTE *( qchar ) DQUOTE  ; \" and \\ escape inside quotes
//	token    = 1*( ALPHA / DIGIT / "-" / "_" / "." / "/" / "@" / "#" / "+" / ":" )
//
// Whitespace around clauses and values is ignored, and so is one trailing
// ";". Positions are 1-based columns counted in code points.

// QueryError is a malformed or unsatisfiable query. Msg names the column
// and the fix; Fixed, when set, is the expression with that fix applied.
type QueryError struct {
	Pos   int    `json:"pos"`
	Msg   string `json:"error"`
	Fixed string `json:"fixed,omitempty"`
}

func (e *QueryError) Error() string { return e.Msg }

type opKind int

const (
	opAny opKind = iota
	opList
	opRange
)

// qValue is one value of a list, or a range bound.
type qValue struct {
	text   string // unquoted text
	quoted bool
	macro  string // "me" or "people" for an unquoted @me / @people
	start  int    // rune offsets of the value in the expression
	end    int
}

// Clause is one parsed clause.
type Clause struct {
	Neg   bool
	Name  string
	field Field
	op    opKind
	vals  []qValue
	// Range bounds; nil means open.
	lo, hi         *qValue
	loIncl, hiIncl bool
	col            int // 1-based column of the clause
}

// Query is a parsed expression.
type Query struct {
	Expr    string
	Clauses []Clause
}

// Empty reports whether q has no clauses: no filter at all.
func (q *Query) Empty() bool { return q == nil || len(q.Clauses) == 0 }

// HasKind reports whether q restricts rows with a kind clause.
func (q *Query) HasKind() bool {
	for _, c := range q.Clauses {
		if c.Name == "kind" {
			return true
		}
	}
	return false
}

// Fields returns the names of the fields q uses, in order, without repeats.
func (q *Query) Fields() []string {
	var out []string
	for _, c := range q.Clauses {
		if !slices.Contains(out, c.Name) {
			out = append(out, c.Name)
		}
	}
	return out
}

var (
	tokenChar   = regexp.MustCompile(`^[A-Za-z0-9\-_./@#+:]$`)
	numberRe    = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)
	durationRe  = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[dw]?$`)
	dateRe      = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	relativeRe  = regexp.MustCompile(`^[+-][0-9]+[dw]$`)
	timestampRe = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})$`)
)

func isSpace(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }

// trimSpan narrows [s, e) of rs past surrounding whitespace.
func trimSpan(rs []rune, s, e int) (int, int) {
	for s < e && isSpace(rs[s]) {
		s++
	}
	for e > s && isSpace(rs[e-1]) {
		e--
	}
	return s, e
}

// splitOutsideQuotes returns the [start, end) spans of rs[s:e] between sep
// characters that are outside double quotes.
func splitOutsideQuotes(rs []rune, s, e int, sep rune) [][2]int {
	var out [][2]int
	start, inQ := s, false
	for i := s; i < e; i++ {
		c := rs[i]
		if inQ {
			if c == '\\' && i+1 < e {
				i++
			} else if c == '"' {
				inQ = false
			}
			continue
		}
		if c == '"' {
			inQ = true
		} else if c == sep {
			out = append(out, [2]int{start, i})
			start = i + 1
		}
	}
	return append(out, [2]int{start, e})
}

func qerr(pos int, format string, args ...any) *QueryError {
	return &QueryError{Pos: pos, Msg: fmt.Sprintf(format, args...)}
}

// ParseQuery parses expr against reg. An empty expression is a query with
// no clauses.
func ParseQuery(expr string, reg *Registry) (*Query, error) {
	rs := []rune(expr)
	q := &Query{Expr: expr}
	spans := splitOutsideQuotes(rs, 0, len(rs), ';')
	for k, sp := range spans {
		s, e := trimSpan(rs, sp[0], sp[1])
		if s == e {
			if len(spans) == 1 || (k == len(spans)-1 && k > 0) {
				continue // the empty query, or one trailing ";"
			}
			return nil, qerr(sp[0]+1, "empty clause at column %d; remove the extra \";\"", sp[0]+1)
		}
		c, err := parseClause(rs, s, e, reg)
		if err != nil {
			return nil, err
		}
		q.Clauses = append(q.Clauses, c)
	}
	if err := checkKinds(q); err != nil {
		return nil, err
	}
	return q, nil
}

func parseClause(rs []rune, s, e int, reg *Registry) (Clause, error) {
	c := Clause{col: s + 1}
	if rs[s] == '!' {
		c.Neg = true
		s++
	}
	colon := -1
	for i := s; i < e; i++ {
		if rs[i] == ':' {
			colon = i
			break
		}
		if rs[i] == '"' {
			break
		}
	}
	if colon < 0 {
		return c, qerr(s+1, "expected name:value at column %d; for example prstate:open", s+1)
	}
	ns, ne := trimSpan(rs, s, colon)
	if ns == ne {
		return c, qerr(s+1, "missing field name before \":\" at column %d; for example prstate:open", s+1)
	}
	c.Name = string(rs[ns:ne])
	f, ok := reg.Field(c.Name)
	if !ok {
		return c, unknownFieldError(rs, ns, ne, c.Name, reg)
	}
	c.field = f
	os, oe := trimSpan(rs, colon+1, e)
	if os == oe {
		return c, qerr(colon+2, "missing value after \"%s:\" at column %d; use %s:* for any known value", c.Name, colon+2, c.Name)
	}
	op := string(rs[os:oe])
	switch {
	case op == "*":
		c.op = opAny
		return c, nil
	case rs[os] == '>' || rs[os] == '<':
		if err := rangeAllowed(f, os, reg); err != nil {
			return c, err
		}
		n := 1
		if os+1 < oe && rs[os+1] == '=' {
			n = 2
		}
		cmp := string(rs[os : os+n])
		bs, be := trimSpan(rs, os+n, oe)
		if bs == be {
			return c, qerr(os+n+1, "missing bound after \"%s\" at column %d; %s", cmp, os+n+1, boundHint(f))
		}
		b, err := parseBound(rs, bs, be, f)
		if err != nil {
			return c, err
		}
		c.op = opRange
		switch cmp {
		case ">":
			c.lo = &b
		case ">=":
			c.lo, c.loIncl = &b, true
		case "<":
			c.hi = &b
		default:
			c.hi, c.hiIncl = &b, true
		}
		return c, nil
	case indexDots(rs, os, oe) >= 0:
		if err := rangeAllowed(f, os, reg); err != nil {
			return c, err
		}
		dd := indexDots(rs, os, oe)
		ls, le := trimSpan(rs, os, dd)
		hs, he := trimSpan(rs, dd+2, oe)
		if ls == le && hs == he {
			return c, qerr(os+1, "a range needs at least one bound at column %d; %s", os+1, boundHint(f))
		}
		for i := os; i < oe; i++ {
			if rs[i] == ',' {
				return c, qerr(i+1, "a range cannot be combined with other values at column %d; write a separate clause for each", i+1)
			}
		}
		c.op = opRange
		c.loIncl, c.hiIncl = true, true
		if ls < le {
			b, err := parseBound(rs, ls, le, f)
			if err != nil {
				return c, err
			}
			c.lo = &b
		}
		if hs < he {
			b, err := parseBound(rs, hs, he, f)
			if err != nil {
				return c, err
			}
			c.hi = &b
		}
		return c, nil
	}
	c.op = opList
	parts := splitOutsideQuotes(rs, os, oe, ',')
	for _, p := range parts {
		vs, ve := trimSpan(rs, p[0], p[1])
		if vs == ve {
			return c, qerr(p[0]+1, "empty value at column %d; remove the extra \",\"", p[0]+1)
		}
		if ve == vs+1 && rs[vs] == '*' {
			return c, qerr(vs+1, "* cannot be combined with other values at column %d; use %s:* alone", vs+1, c.Name)
		}
		v, err := parseValue(rs, vs, ve)
		if err != nil {
			return c, err
		}
		if err := checkValue(rs, v, f, reg); err != nil {
			return c, err
		}
		c.vals = append(c.vals, v)
	}
	return c, nil
}

// indexDots returns the offset of the first ".." outside double quotes in
// rs[s:e], or -1.
func indexDots(rs []rune, s, e int) int {
	inQ := false
	for i := s; i < e; i++ {
		c := rs[i]
		if inQ {
			if c == '\\' && i+1 < e {
				i++
			} else if c == '"' {
				inQ = false
			}
			continue
		}
		if c == '"' {
			inQ = true
		} else if c == '.' && i+1 < e && rs[i+1] == '.' {
			return i
		}
	}
	return -1
}

// parseValue reads one list value or bound: a token or a quoted string.
func parseValue(rs []rune, s, e int) (qValue, error) {
	v := qValue{start: s, end: e}
	if rs[s] == '"' {
		var b strings.Builder
		i := s + 1
		closed := false
		for ; i < e; i++ {
			c := rs[i]
			if c == '\\' && i+1 < e {
				i++
				b.WriteRune(rs[i])
				continue
			}
			if c == '"' {
				closed = true
				break
			}
			b.WriteRune(c)
		}
		if !closed {
			return v, qerr(s+1, "unterminated quoted value at column %d; close it with \"", s+1)
		}
		if i+1 < e {
			return v, qerr(i+2, "unexpected %s after a quoted value at column %d; separate values with \",\"", quoteRune(rs[i+1]), i+2)
		}
		v.text, v.quoted = b.String(), true
		return v, nil
	}
	for i := s; i < e; i++ {
		if !tokenChar.MatchString(string(rs[i])) {
			return v, qerr(i+1, "unexpected %s at column %d; put a value with spaces or punctuation in double quotes", quoteRune(rs[i]), i+1)
		}
	}
	v.text = string(rs[s:e])
	if strings.HasPrefix(v.text, "@") {
		switch v.text {
		case "@me":
			v.macro = "me"
		case "@people":
			v.macro = "people"
		default:
			return v, qerr(s+1, "unknown macro %q at column %d; macros are @me and @people (quote the value to match it literally)", v.text, s+1)
		}
	}
	return v, nil
}

func quoteRune(r rune) string {
	if isSpace(r) {
		return "space"
	}
	return strconv.Quote(string(r))
}

// checkValue validates a list value against its field's type.
func checkValue(rs []rune, v qValue, f Field, reg *Registry) error {
	col := v.start + 1
	unknownLit := !v.quoted && v.text == "unknown"
	if v.macro != "" {
		switch {
		case f.Type == FieldEnum && len(f.Values) > 0:
			return badEnumValue(rs, v, f)
		case f.Type != FieldEnum && f.Type != FieldSet && f.Type != FieldText:
			return qerr(col, "%s does not apply to %s field %s at column %d; macros name people, for enum, set and text fields", v.text, f.Type, f.Name, col)
		}
		return nil
	}
	switch f.Type {
	case FieldText, FieldSet:
		return nil
	case FieldEnum:
		if len(f.Values) == 0 || unknownLit {
			return nil
		}
		if !slices.Contains(f.Values, strings.ToLower(v.text)) {
			return badEnumValue(rs, v, f)
		}
	case FieldBool:
		if t := strings.ToLower(v.text); t != "true" && t != "false" && !unknownLit {
			return qerr(col, "%q is not a value of %s at column %d; allowed: true, false, unknown", v.text, f.Name, col)
		}
	default:
		if unknownLit {
			return nil
		}
		return checkBound(v, f)
	}
	return nil
}

func badEnumValue(rs []rune, v qValue, f Field) error {
	col := v.start + 1
	allowed := slices.Clone(f.Values)
	if !slices.Contains(allowed, "unknown") {
		allowed = append(allowed, "unknown")
	}
	e := &QueryError{Pos: col}
	if s := closest(strings.ToLower(v.text), f.Values); s != "" {
		e.Msg = fmt.Sprintf("%q is not a value of %s at column %d; did you mean %s? Allowed: %s", v.text, f.Name, col, s, strings.Join(allowed, ", "))
		e.Fixed = string(rs[:v.start]) + s + string(rs[v.end:])
	} else {
		e.Msg = fmt.Sprintf("%q is not a value of %s at column %d; allowed: %s", v.text, f.Name, col, strings.Join(allowed, ", "))
	}
	return e
}

func parseBound(rs []rune, s, e int, f Field) (qValue, error) {
	v, err := parseValue(rs, s, e)
	if err != nil {
		return v, err
	}
	if v.macro != "" || v.quoted {
		return v, qerr(s+1, "%q is not a valid bound for %s at column %d; %s", v.text, f.Name, s+1, boundHint(f))
	}
	return v, checkBound(v, f)
}

// checkBound validates an ordered field's value or bound.
func checkBound(v qValue, f Field) error {
	col := v.start + 1
	ok := false
	switch f.Type {
	case FieldNumber:
		ok = !v.quoted && numberRe.MatchString(v.text)
	case FieldDuration:
		ok = !v.quoted && durationRe.MatchString(v.text)
	case FieldDate:
		ok = !v.quoted && (v.text == "today" || relativeRe.MatchString(v.text) || validDate(v.text))
	}
	if !ok {
		return qerr(col, "%q is not a valid %s for %s at column %d; %s", v.text, f.Type, f.Name, col, boundHint(f))
	}
	return nil
}

func validDate(s string) bool {
	if !dateRe.MatchString(s) {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func boundHint(f Field) string {
	switch f.Type {
	case FieldNumber:
		return "use a number such as 10"
	case FieldDuration:
		return "use days such as 30, 30d or 2w"
	case FieldDate:
		return "use YYYY-MM-DD, today, -7d or +2w"
	}
	return "this field takes no range"
}

func rangeAllowed(f Field, os int, reg *Registry) error {
	if f.Type.ordered() {
		return nil
	}
	return qerr(os+1, "%s does not take a range at column %d; ranges apply to number, duration and date fields: %s", f.Name, os+1, strings.Join(reg.orderedNames(), ", "))
}

func unknownFieldError(rs []rune, ns, ne int, name string, reg *Registry) error {
	col := ns + 1
	list := strings.Join(reg.Names(), ", ")
	if owner, ok := reg.Planned(name); ok {
		return qerr(col, "field %s at column %d is not available yet: it arrives with %s. Fields: %s", name, col, owner, list)
	}
	e := &QueryError{Pos: col}
	if s := closest(strings.ToLower(name), reg.Names()); s != "" {
		e.Msg = fmt.Sprintf("unknown field %q at column %d; did you mean %s? Fields: %s", name, col, s, list)
		e.Fixed = string(rs[:ns]) + s + string(rs[ne:])
	} else {
		e.Msg = fmt.Sprintf("unknown field %q at column %d. Fields: %s", name, col, list)
	}
	return e
}

// closest returns the candidate nearest to s by edit distance, if near
// enough to be a likely typo; ties go to the earlier candidate.
func closest(s string, cands []string) string {
	best, bd := "", math.MaxInt
	for _, c := range cands {
		if d := editDistance([]rune(s), []rune(c)); d < bd {
			best, bd = c, d
		}
	}
	if bd <= max(2, len([]rune(s))/3) {
		return best
	}
	return ""
}

func editDistance(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// checkKinds rejects clauses that no node kind can satisfy together: fields
// with no common kind, or kind clauses that leave no rows.
func checkKinds(q *Query) error {
	common := slices.Clone(queryKinds)
	var seen []Clause
	for _, c := range q.Clauses {
		if c.Name == "kind" {
			continue
		}
		next := intersectKinds(common, c.field.Kinds)
		if len(next) == 0 {
			names := []string{}
			var parts []string
			var other *Clause
			for i := range seen {
				if len(intersectKinds(seen[i].field.Kinds, c.field.Kinds)) == 0 {
					other = &seen[i]
					break
				}
			}
			list := seen
			if other != nil {
				list = []Clause{*other}
			}
			for _, d := range append(slices.Clone(list), c) {
				if slices.Contains(names, d.Name) {
					continue
				}
				names = append(names, d.Name)
				parts = append(parts, d.Name+": "+strings.Join(kindNames(intersectKinds(queryKinds, d.field.Kinds)), ", "))
			}
			return qerr(c.col, "fields %s apply to no common node kind (%s) at column %d; split them into separate queries", joinAnd(names), strings.Join(parts, "; "), c.col)
		}
		common = next
		seen = append(seen, c)
	}
	rows := slices.Clone(queryKinds)
	first := 0
	for _, c := range q.Clauses {
		if c.Name != "kind" {
			continue
		}
		if first == 0 {
			first = c.col
		}
		var set []NodeKind
		switch c.op {
		case opAny:
			set = slices.Clone(queryKinds)
		default:
			for _, v := range c.vals {
				k := NodeKind(strings.ToLower(v.text))
				if slices.Contains(queryKinds, k) {
					set = append(set, k)
				}
			}
		}
		if c.Neg {
			var comp []NodeKind
			for _, k := range queryKinds {
				if !slices.Contains(set, k) {
					comp = append(comp, k)
				}
			}
			set = comp
		}
		rows = intersectKinds(rows, set)
	}
	if first == 0 || len(intersectKinds(rows, common)) > 0 {
		return nil
	}
	if len(rows) == 0 {
		return qerr(first, "the kind clause at column %d leaves no rows: it allows no kind; change or remove it", first)
	}
	return qerr(first, "the kind clause at column %d leaves no rows: it allows %s, while %s can only match %s; change or remove it",
		first, strings.Join(kindNames(rows), ", "), strings.Join(nonKindNames(q), ", "), strings.Join(kindNames(common), ", "))
}

func nonKindNames(q *Query) []string {
	var out []string
	for _, c := range q.Clauses {
		if c.Name != "kind" && !slices.Contains(out, c.Name) {
			out = append(out, c.Name)
		}
	}
	return out
}

func joinAnd(names []string) string {
	if len(names) <= 1 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// intersectKinds returns the kinds of a also in b, in queryKinds order.
func intersectKinds(a, b []NodeKind) []NodeKind {
	var out []NodeKind
	for _, k := range queryKinds {
		if slices.Contains(a, k) && slices.Contains(b, k) {
			out = append(out, k)
		}
	}
	return out
}

// ---------- environment ----------

// QueryPerson is a configured person, as the snapshot lists them (gh-97).
type QueryPerson struct {
	Login string `json:"login"`
	Jira  string `json:"jira,omitempty"`
	Name  string `json:"name,omitempty"`
	Me    bool   `json:"me,omitempty"`
}

// QueryEnv is what macros and relative dates resolve against: @me, @people
// and the snapshot's generated_at.
type QueryEnv struct {
	Me     string        `json:"me,omitempty"`
	People []QueryPerson `json:"people,omitempty"`
	Now    time.Time     `json:"-"`
}

// queryEnv builds the query environment from a snapshot's generated_at and
// its optional people list. It is the one place that knows where people come
// from: gh-97 adds them to the snapshot as
// "people": [{"login", "jira", "name", "me"}], read here tolerantly from
// the JSON so a snapshot without them (or with a malformed list) simply has
// no @me. The live page's bootstrap and live.js's L.queryEnv use the same
// rules.
func queryEnv(generatedAt string, people json.RawMessage) QueryEnv {
	var env QueryEnv
	if t, err := time.Parse(time.RFC3339Nano, generatedAt); err == nil && timestampRe.MatchString(generatedAt) {
		env.Now = t
	}
	var list []json.RawMessage
	if json.Unmarshal(people, &list) != nil {
		return env
	}
	for _, item := range list {
		var p map[string]any
		if json.Unmarshal(item, &p) != nil {
			continue
		}
		login, _ := p["login"].(string)
		if login == "" {
			login, _ = p["github"].(string)
		}
		if login == "" {
			continue
		}
		qp := QueryPerson{Login: login}
		qp.Jira, _ = p["jira"].(string)
		qp.Name, _ = p["name"].(string)
		qp.Me, _ = p["me"].(bool)
		if qp.Me && env.Me == "" {
			env.Me = login
		}
		env.People = append(env.People, qp)
	}
	return env
}

// ---------- index ----------

// fieldValue is one field's value on one node.
type fieldValue struct {
	na      bool // the field does not apply to the node's kind
	unknown bool
	disp    []string  // display text (enum, set, text, bool, date)
	norm    []string  // lowercased text, or YYYY-MM-DD days for dates
	nums    []float64 // number and duration values
}

// queryIndex holds a snapshot decoded generically (so fields the Go model
// does not know yet still resolve) with every registered field's value per
// node, computed once.
type queryIndex struct {
	reg      *Registry
	env      QueryEnv
	nodes    []map[string]any
	ids      []string
	kinds    []NodeKind
	byID     map[string]int
	out, inc [][]int
	values   map[string][]fieldValue
	efforts  [][]int // effort node indices per node
	days     []string
	activity map[string][]float64
	loc      *time.Location
	nowMs    int64
	today    string // local day of generated_at; "" when unknown
	total    int
}

type rawSnapshot struct {
	GeneratedAt string           `json:"generated_at"`
	Nodes       []map[string]any `json:"nodes"`
	Edges       []struct {
		Source string `json:"source"`
		Target string `json:"target"`
	} `json:"edges"`
	Counts struct {
		Days     []string             `json:"days"`
		Activity map[string][]float64 `json:"activity"`
	} `json:"counts"`
	People json.RawMessage `json:"people"`
}

// newQueryIndex decodes a snapshot's JSON and indexes reg's fields over it.
func newQueryIndex(raw []byte, reg *Registry) (*queryIndex, error) {
	var doc rawSnapshot
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decode snapshot for query: %w", err)
	}
	ix := &queryIndex{reg: reg, env: queryEnv(doc.GeneratedAt, doc.People), byID: map[string]int{},
		days: doc.Counts.Days, activity: doc.Counts.Activity}
	if !ix.env.Now.IsZero() {
		_, off := ix.env.Now.Zone()
		ix.loc = time.FixedZone("", off)
		ix.nowMs = ix.env.Now.UnixMilli()
		ix.today = ix.env.Now.In(ix.loc).Format("2006-01-02")
	}
	for _, n := range doc.Nodes {
		id, _ := n["id"].(string)
		if _, dup := ix.byID[id]; dup || id == "" {
			continue
		}
		kind, _ := n["kind"].(string)
		ix.byID[id] = len(ix.nodes)
		ix.nodes = append(ix.nodes, n)
		ix.ids = append(ix.ids, id)
		ix.kinds = append(ix.kinds, NodeKind(kind))
	}
	ix.total = len(ix.nodes)
	ix.out = make([][]int, ix.total)
	ix.inc = make([][]int, ix.total)
	for _, e := range doc.Edges {
		s, ok1 := ix.byID[e.Source]
		t, ok2 := ix.byID[e.Target]
		if !ok1 || !ok2 {
			continue
		}
		ix.out[s] = append(ix.out[s], t)
		ix.inc[t] = append(ix.inc[t], s)
	}
	ix.efforts = make([][]int, ix.total)
	for i, k := range ix.kinds {
		if k != KindEffort {
			continue
		}
		for _, j := range reach(ix.out, []int{i}) {
			ix.efforts[j] = append(ix.efforts[j], i)
		}
	}
	for j := range ix.efforts {
		slices.Sort(ix.efforts[j])
	}
	ix.values = map[string][]fieldValue{}
	for _, f := range reg.fields {
		vals := make([]fieldValue, ix.total)
		for i := range ix.nodes {
			vals[i] = ix.extract(f, i)
		}
		ix.values[f.Name] = vals
	}
	return ix, nil
}

// reach returns seeds and every node reachable from them along adj, in
// visiting order.
func reach(adj [][]int, seeds []int) []int {
	seen := map[int]bool{}
	var out, queue []int
	for _, s := range seeds {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
			queue = append(queue, s)
		}
	}
	for len(queue) > 0 {
		i := queue[0]
		queue = queue[1:]
		for _, j := range adj[i] {
			if !seen[j] {
				seen[j] = true
				out = append(out, j)
				queue = append(queue, j)
			}
		}
	}
	return out
}

func (ix *queryIndex) effortKey(i int) string {
	if e, ok := ix.nodes[i]["effort"].(map[string]any); ok {
		if k, ok := e["key"].(string); ok && k != "" {
			return k
		}
	}
	return strings.TrimPrefix(ix.ids[i], "effort:")
}

// pathValues reads f's paths on node i.
func (ix *queryIndex) pathValues(f Field, i int) (vals []any, known bool) {
	for _, p := range f.Paths {
		segs, _ := parsePath(p) // validated by NewRegistry
		v, k := walkPath(ix.nodes[i], segs)
		vals = append(vals, v...)
		known = known || k
	}
	return vals, known
}

// raw computes f's values on node i before type normalization.
func (ix *queryIndex) raw(f Field, i int) (vals []any, known bool) {
	if f.Derive == "" || (len(f.DeriveKinds) > 0 && !slices.Contains(f.DeriveKinds, ix.kinds[i])) {
		return ix.pathValues(f, i)
	}
	switch f.Derive {
	case DeriveEfforts:
		for _, e := range ix.efforts[i] {
			vals = append(vals, ix.effortKey(e))
		}
		return vals, true
	case DeriveActivityDays:
		if len(ix.days) == 0 {
			return nil, false
		}
		set := map[string]bool{}
		for _, e := range ix.efforts[i] {
			for d, n := range ix.activity[ix.ids[e]] {
				if n > 0 && d < len(ix.days) {
					set[ix.days[d]] = true
				}
			}
		}
		for _, d := range slices.Sorted(maps.Keys(set)) {
			vals = append(vals, d)
		}
		return vals, true
	case DeriveLinkedRepos:
		for _, j := range ix.inc[i] {
			if ix.kinds[j] != KindPR && ix.kinds[j] != KindBookmark {
				continue
			}
			v, k := ix.pathValues(f, j)
			vals = append(vals, v...)
			known = known || k
		}
		return vals, known
	case DeriveDaysSince:
		if ix.today == "" {
			return nil, false
		}
		ts, k := ix.pathValues(f, i)
		for _, t := range ts {
			if ms, ok := parseTimestampMs(t); ok {
				vals = append(vals, float64(ix.nowMs-ms)/86400000)
			}
		}
		return vals, k
	case DeriveCount:
		v, k := ix.pathValues(f, i)
		if !k {
			return nil, false
		}
		return []any{float64(len(v))}, true
	case DerivePRState:
		n := ix.nodes[i]
		if pr, ok := n["pr"].(map[string]any); ok {
			state, _ := pr["state"].(string)
			switch {
			case state == "open" && truthy(pr["is_draft"]):
				return []any{"draft"}, true
			case state == "":
				return []any{"unknown"}, true
			}
			return []any{state}, true
		}
		bm, _ := n["bookmark"].(map[string]any)
		lookup, _ := bm["pr_lookup"].(string)
		if bm == nil || lookup == "" || lookup == "unknown" || lookup == "error" {
			return []any{"unknown"}, true
		}
		if c, _ := bm["pr_count"].(float64); c > 0 {
			return nil, true
		}
		return []any{"none"}, true
	}
	return nil, false
}

func parseTimestampMs(v any) (int64, bool) {
	s, ok := v.(string)
	if !ok || !timestampRe.MatchString(s) {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, false
	}
	return t.UnixMilli(), true
}

// extract computes and normalizes f's value on node i.
func (ix *queryIndex) extract(f Field, i int) fieldValue {
	if !slices.Contains(f.Kinds, ix.kinds[i]) {
		return fieldValue{na: true, unknown: true}
	}
	vals, known := ix.raw(f, i)
	var fv fieldValue
	switch f.Type {
	case FieldNumber, FieldDuration:
		for _, v := range vals {
			if x, ok := v.(float64); ok {
				fv.nums = append(fv.nums, x)
			}
		}
	case FieldDate:
		seen := map[string]bool{}
		for _, v := range vals {
			d := ix.dayOf(v)
			if d != "" && !seen[d] {
				seen[d] = true
				fv.norm = append(fv.norm, d)
				fv.disp = append(fv.disp, d)
			}
		}
	default:
		seen := map[string]bool{}
		for _, v := range vals {
			s, ok := scalarString(v)
			if !ok || s == "" {
				continue
			}
			if f.Type != FieldText {
				if m, ok := f.Map[strings.ToLower(s)]; ok {
					s = m
				}
			}
			n := strings.ToLower(s)
			if f.Type != FieldText && seen[n] {
				continue
			}
			seen[n] = true
			fv.disp = append(fv.disp, s)
			fv.norm = append(fv.norm, n)
		}
		if len(fv.norm) == 0 && f.Type != FieldText {
			if m, ok := f.Map[""]; ok {
				fv.disp, fv.norm, known = []string{m}, []string{strings.ToLower(m)}, true
			}
		}
	}
	fv.unknown = !known || ((f.Type == FieldEnum || f.Type == FieldSet) && len(fv.norm) == 1 && fv.norm[0] == "unknown")
	return fv
}

// dayOf turns a date or timestamp into the local calendar day of the
// snapshot's time zone (the offset of generated_at).
func (ix *queryIndex) dayOf(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	if validDate(s) {
		return s
	}
	if ix.loc == nil {
		return ""
	}
	ms, ok := parseTimestampMs(s)
	if !ok {
		return ""
	}
	return time.UnixMilli(ms).In(ix.loc).Format("2006-01-02")
}

// resolveDay turns a date value or bound into a YYYY-MM-DD day.
func (ix *queryIndex) resolveDay(s string) (string, bool) {
	if validDate(s) {
		return s, true
	}
	if ix.today == "" {
		return "", false
	}
	n := 0
	if s != "today" {
		k, err := strconv.Atoi(s[1 : len(s)-1])
		if err != nil {
			return "", false
		}
		if s[len(s)-1] == 'w' {
			k *= 7
		}
		if s[0] == '-' {
			k = -k
		}
		n = k
	}
	t, _ := time.Parse("2006-01-02", ix.today)
	return t.AddDate(0, 0, n).Format("2006-01-02"), true
}

func durationDays(s string) float64 {
	mult := 1.0
	switch s[len(s)-1] {
	case 'w':
		mult, s = 7, s[:len(s)-1]
	case 'd':
		s = s[:len(s)-1]
	}
	x, _ := strconv.ParseFloat(s, 64)
	return x * mult
}

// ---------- evaluation ----------

type tri int8

const (
	triFalse tri = iota
	triTrue
	triUnknown
)

// boundClause is a clause with its operands resolved against an index.
type boundClause struct {
	Clause
	strs       []string  // lowercased list values
	nums       []float64 // list values of number and duration fields
	days       []string  // list values of date fields
	hasUnknown bool
	empty      bool // every value was an unresolved macro: never decidable
	lo, hi     *rangeBound
}

type rangeBound struct {
	num float64
	day string
	ok  bool
}

// QueryResult is the outcome of a query over a snapshot.
type QueryResult struct {
	Expr string `json:"expr"`
	// Matches are the nodes for which every non-kind clause applies and
	// holds; Rows are the matches the kind clauses keep.
	Matches []string `json:"matches"`
	Rows    []string `json:"rows"`
	// Warnings name operands that could not be resolved, such as @me in a
	// snapshot that lists no people.
	Warnings []string `json:"warnings,omitempty"`
	matchIdx []int
	rowIdx   []int
	// visible is the closure of the matches, or nil when the query has no
	// non-kind clause (no filter).
	visible map[string]bool
}

// Filtered reports whether the query filters the graph at all.
func (r *QueryResult) Filtered() bool { return r.visible != nil }

// Visible reports whether node id is in the visible set: the matches, what
// they lead to and what leads to them. With no filter every node is.
func (r *QueryResult) Visible(id string) bool {
	return r.visible == nil || r.visible[id]
}

func (ix *queryIndex) bind(c Clause) (boundClause, []string) {
	b := boundClause{Clause: c}
	var warns []string
	switch c.op {
	case opList:
		for _, v := range c.vals {
			if !v.quoted && v.text == "unknown" && c.field.Type != FieldText {
				b.hasUnknown = true
				continue
			}
			var texts []string
			switch v.macro {
			case "me":
				if ix.env.Me == "" {
					warns = append(warns, "@me is unknown: the snapshot names no authenticated person, so "+c.Name+":@me matches nothing")
				} else {
					texts = []string{ix.env.Me}
				}
			case "people":
				if len(ix.env.People) == 0 {
					warns = append(warns, "@people is unknown: the snapshot lists no configured people, so "+c.Name+":@people matches nothing")
				}
				for _, p := range ix.env.People {
					texts = append(texts, p.Login)
				}
			default:
				texts = []string{v.text}
			}
			for _, t := range texts {
				switch c.field.Type {
				case FieldNumber, FieldDuration:
					b.nums = append(b.nums, boundNumber(c.field, t))
				case FieldDate:
					if d, ok := ix.resolveDay(t); ok {
						b.days = append(b.days, d)
					}
				default:
					b.strs = append(b.strs, strings.ToLower(t))
				}
			}
		}
		b.empty = len(b.strs) == 0 && len(b.nums) == 0 && len(b.days) == 0 && !b.hasUnknown
	case opRange:
		b.lo, b.hi = ix.bindBound(c.field, c.lo), ix.bindBound(c.field, c.hi)
		if (b.lo != nil && !b.lo.ok) || (b.hi != nil && !b.hi.ok) {
			b.empty = true
		}
	}
	return b, warns
}

func boundNumber(f Field, s string) float64 {
	if f.Type == FieldDuration {
		return durationDays(s)
	}
	x, _ := strconv.ParseFloat(s, 64)
	return x
}

func (ix *queryIndex) bindBound(f Field, v *qValue) *rangeBound {
	if v == nil {
		return nil
	}
	if f.Type == FieldDate {
		d, ok := ix.resolveDay(v.text)
		return &rangeBound{day: d, ok: ok}
	}
	return &rangeBound{num: boundNumber(f, v.text), ok: true}
}

func (b *boundClause) test(v fieldValue) tri {
	var r tri
	switch {
	case v.na:
		return triUnknown
	case v.unknown:
		if b.op == opList && b.hasUnknown {
			r = triTrue
		} else {
			r = triUnknown
		}
	case b.empty:
		r = triUnknown
	case b.holds(v):
		r = triTrue
	default:
		r = triFalse
	}
	if b.Neg && r != triUnknown {
		r = 1 - r
	}
	return r
}

func (b *boundClause) holds(v fieldValue) bool {
	t := b.field.Type
	switch b.op {
	case opAny:
		return len(v.norm) > 0 || len(v.nums) > 0
	case opList:
		switch t {
		case FieldNumber, FieldDuration:
			for _, x := range v.nums {
				if slices.Contains(b.nums, x) {
					return true
				}
			}
		case FieldDate:
			for _, d := range v.norm {
				if slices.Contains(b.days, d) {
					return true
				}
			}
		case FieldText:
			for _, s := range v.norm {
				for _, o := range b.strs {
					if strings.Contains(s, o) {
						return true
					}
				}
			}
		default:
			for _, s := range v.norm {
				if slices.Contains(b.strs, s) {
					return true
				}
			}
		}
		return false
	}
	// opRange
	if t == FieldDate {
		for _, d := range v.norm {
			if inRange(cmpStr(d, b.lo), cmpStr(d, b.hi), b.loIncl, b.hiIncl, b.lo != nil, b.hi != nil) {
				return true
			}
		}
		return false
	}
	for _, x := range v.nums {
		if inRange(cmpNum(x, b.lo), cmpNum(x, b.hi), b.loIncl, b.hiIncl, b.lo != nil, b.hi != nil) {
			return true
		}
	}
	return false
}

func cmpStr(d string, b *rangeBound) int {
	if b == nil {
		return 0
	}
	return strings.Compare(d, b.day)
}

func cmpNum(x float64, b *rangeBound) int {
	if b == nil {
		return 0
	}
	switch {
	case x < b.num:
		return -1
	case x > b.num:
		return 1
	}
	return 0
}

// inRange combines a value's comparisons with the low and high bounds.
func inRange(lo, hi int, loIncl, hiIncl, hasLo, hasHi bool) bool {
	if hasLo && (lo < 0 || (lo == 0 && !loIncl)) {
		return false
	}
	if hasHi && (hi > 0 || (hi == 0 && !hiIncl)) {
		return false
	}
	return true
}

// evaluate runs q over the index.
func (ix *queryIndex) evaluate(q *Query) *QueryResult {
	res := &QueryResult{Expr: q.Expr, Matches: []string{}, Rows: []string{}}
	var filters, kinds []boundClause
	for _, c := range q.Clauses {
		b, w := ix.bind(c)
		for _, s := range w {
			if !slices.Contains(res.Warnings, s) {
				res.Warnings = append(res.Warnings, s)
			}
		}
		if c.Name == "kind" {
			kinds = append(kinds, b)
		} else {
			filters = append(filters, b)
		}
	}
	for i := range ix.nodes {
		ok := true
		for k := range filters {
			if filters[k].test(ix.values[filters[k].Name][i]) != triTrue {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		res.matchIdx = append(res.matchIdx, i)
		res.Matches = append(res.Matches, ix.ids[i])
		row := true
		for k := range kinds {
			if kinds[k].test(ix.values["kind"][i]) != triTrue {
				row = false
				break
			}
		}
		if row {
			res.rowIdx = append(res.rowIdx, i)
			res.Rows = append(res.Rows, ix.ids[i])
		}
	}
	if len(filters) > 0 {
		res.visible = map[string]bool{}
		for _, i := range reach(ix.out, res.matchIdx) {
			res.visible[ix.ids[i]] = true
		}
		for _, i := range reach(ix.inc, res.matchIdx) {
			res.visible[ix.ids[i]] = true
		}
	}
	return res
}

// jsNumber formats x as JavaScript's String(x) does for ordinary values.
func jsNumber(x float64) string {
	if a := math.Abs(x); a == 0 || (a >= 1e-6 && a < 1e21) {
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return strconv.FormatFloat(x, 'g', -1, 64)
}
