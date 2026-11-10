package dash

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

// The analysis fixture is a stored snapshot: the page and wgo dash --fixture
// serve it without assembling, so it carries the derived facts (gh-99). This
// file keeps those facts equal to what ComputeFacts derives from the rest of
// the fixture, and rewrites them with
//
//	go test ./internal/dash -run TestAnalysisFixtureFacts -update
//
// touching nothing else: the fixture is hand-formatted, and the rewrite
// keeps its layout (see writeOrdered).

func TestAnalysisFixtureFacts(t *testing.T) {
	data, err := os.ReadFile(analysisFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	s := loadAnalysisFixture(t)
	facts := ComputeFacts(s, s.People, DefaultFactThresholds(), s.GeneratedAt)
	if *updateFields {
		out, err := rewriteFixtureFacts(data, facts)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(analysisFixturePath, out, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	stale := func(what string, stored, computed any) {
		t.Helper()
		a, _ := json.Marshal(stored)
		b, _ := json.Marshal(computed)
		if !bytes.Equal(a, b) {
			t.Errorf("%s: the fixture holds\n  %s\nComputeFacts gives\n  %s\nrun go test ./internal/dash -run TestAnalysisFixtureFacts -update", what, a, b)
		}
	}
	for _, n := range s.Nodes {
		switch {
		case n.PR != nil:
			stale(n.ID, n.PR.Facts, facts.PRs[n.ID])
		case n.Ticket != nil:
			stale(n.ID, n.Ticket.Facts, facts.Tickets[n.ID])
		}
	}
	stale("check_stats", s.CheckStats, facts.CheckStats)
}

// TestFixtureRewriteKeepsTheLayout: reading and writing the fixture with no
// change reproduces it byte for byte, so -update changes only facts.
func TestFixtureRewriteKeepsTheLayout(t *testing.T) {
	data, err := os.ReadFile(analysisFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	root, err := readOrdered(data)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	writeOrdered(&buf, root, "")
	buf.WriteByte('\n')
	if !bytes.Equal(buf.Bytes(), data) {
		got, want := strings.Split(buf.String(), "\n"), strings.Split(string(data), "\n")
		for i := range min(len(got), len(want)) {
			if got[i] != want[i] {
				t.Fatalf("line %d differs:\n got %s\nwant %s", i+1, got[i], want[i])
			}
		}
		t.Fatalf("got %d lines, want %d", len(got), len(want))
	}
}

// rewriteFixtureFacts returns the fixture data with each PR's and ticket's
// facts and the snapshot's check_stats replaced by facts, keeping the rest
// of the document and its layout.
func rewriteFixtureFacts(data []byte, facts Facts) ([]byte, error) {
	root, err := readOrdered(data)
	if err != nil {
		return nil, err
	}
	toNode := func(v any) (*jsonValue, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		return readOrdered(b)
	}
	nodes := root.get("nodes")
	if nodes == nil {
		return nil, fmt.Errorf("fixture has no nodes")
	}
	for _, n := range nodes.vals {
		id, _ := n.get("id").str()
		for _, kind := range []string{"pr", "ticket"} {
			info := n.get(kind)
			if info == nil {
				continue
			}
			var v any = facts.PRs[id]
			if kind == "ticket" {
				v = facts.Tickets[id]
			}
			fv, err := toNode(v)
			if err != nil {
				return nil, err
			}
			info.set("facts", fv)
		}
	}
	cs, err := toNode(facts.CheckStats)
	if err != nil {
		return nil, err
	}
	root.set("check_stats", cs)
	var buf bytes.Buffer
	writeOrdered(&buf, root, "")
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// jsonValue is a JSON value that keeps its object keys in document order.
type jsonValue struct {
	kind byte // '{', '[' or 0 for a scalar
	keys []string
	vals []*jsonValue
	raw  string // a scalar's JSON text
}

func (v *jsonValue) get(key string) *jsonValue {
	if v == nil {
		return nil
	}
	for i, k := range v.keys {
		if k == key {
			return v.vals[i]
		}
	}
	return nil
}

// set replaces key's value, or appends key; a JSON null removes it.
func (v *jsonValue) set(key string, val *jsonValue) {
	for i, k := range v.keys {
		if k == key {
			if val.raw == "null" {
				v.keys = append(v.keys[:i], v.keys[i+1:]...)
				v.vals = append(v.vals[:i], v.vals[i+1:]...)
				return
			}
			v.vals[i] = val
			return
		}
	}
	if val.raw != "null" {
		v.keys, v.vals = append(v.keys, key), append(v.vals, val)
	}
}

func (v *jsonValue) str() (string, bool) {
	var s string
	if v == nil || json.Unmarshal([]byte(v.raw), &s) != nil {
		return "", false
	}
	return s, true
}

func readOrdered(data []byte) (*jsonValue, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := readValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing data after the JSON value")
	}
	return v, nil
}

func readValue(dec *json.Decoder) (*jsonValue, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		v := &jsonValue{kind: byte(t)}
		for dec.More() {
			if v.kind == '{' {
				k, err := dec.Token()
				if err != nil {
					return nil, err
				}
				v.keys = append(v.keys, k.(string))
			}
			child, err := readValue(dec)
			if err != nil {
				return nil, err
			}
			v.vals = append(v.vals, child)
		}
		if _, err := dec.Token(); err != nil { // the closing delimiter
			return nil, err
		}
		return v, nil
	case json.Number:
		return &jsonValue{raw: t.String()}, nil
	case nil:
		return &jsonValue{raw: "null"}, nil
	default:
		return &jsonValue{raw: scalarJSON(t)}, nil
	}
}

func scalarJSON(x any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(x)
	return strings.TrimSuffix(buf.String(), "\n")
}

// hasObject reports whether an object is nested anywhere inside v.
func (v *jsonValue) hasObject() bool {
	for _, c := range v.vals {
		if c.kind == '{' || c.hasObject() {
			return true
		}
	}
	return false
}

// writeOrdered writes v in the fixture's layout: an object or array with no
// object inside it on one line ({"k": v, ...}, [a, b]), anything else one
// member per line, indented by two spaces.
func writeOrdered(w *bytes.Buffer, v *jsonValue, indent string) {
	if v.kind == 0 {
		w.WriteString(v.raw)
		return
	}
	open, end := "{", "}"
	if v.kind == '[' {
		open, end = "[", "]"
	}
	member := func(i int, ind string) {
		if v.kind == '{' {
			w.WriteString(scalarJSON(v.keys[i]) + ": ")
		}
		writeOrdered(w, v.vals[i], ind)
	}
	if !v.hasObject() {
		w.WriteString(open)
		for i := range v.vals {
			if i > 0 {
				w.WriteString(", ")
			}
			member(i, indent)
		}
		w.WriteString(end)
		return
	}
	inner := indent + "  "
	w.WriteString(open + "\n")
	for i := range v.vals {
		w.WriteString(inner)
		member(i, inner)
		if i < len(v.vals)-1 {
			w.WriteByte(',')
		}
		w.WriteByte('\n')
	}
	w.WriteString(indent + end)
}
