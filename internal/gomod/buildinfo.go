package gomod

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Module is one entry of a Go build list. The field set mirrors the JSON that
// `go list -m -json` emits, restricted to what wgo actually consumes.
type Module struct {
	Path      string  `json:"Path"`
	Version   string  `json:"Version"`
	Main      bool    `json:"Main"`
	Indirect  bool    `json:"Indirect"`
	Dir       string  `json:"Dir"`
	GoVersion string  `json:"GoVersion"`
	Replace   *Module `json:"Replace"`
}

// Effective returns the module that actually supplies the code: the
// replacement when one is in force, otherwise the module itself.
func (m Module) Effective() Module {
	if m.Replace != nil {
		return *m.Replace
	}
	return m
}

// ParseModuleList decodes the output of `go list -m -json all`.
//
// That command emits a stream of concatenated JSON objects rather than a JSON
// array, so this decodes in a loop rather than unmarshalling once.
func ParseModuleList(r io.Reader) ([]Module, error) {
	dec := json.NewDecoder(r)
	var out []Module
	for {
		var m Module
		err := dec.Decode(&m)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("gomod: decoding module list: %w", err)
		}
		out = append(out, m)
	}
}

// BuildInfo is the build metadata embedded in a compiled Go binary, as
// reported by `go version -m <binary>`.
//
// This is the highest-fidelity pin source available: it records the exact
// module versions that were linked into a shipped artifact, so a rig built
// from it reproduces the artifact's dependency set by construction.
type BuildInfo struct {
	GoVersion string
	Path      string // main package import path
	Main      Module
	Deps      []Module
	Settings  map[string]string
}

// ParseBuildInfo parses `go version -m` output.
//
// The format is a header line naming the binary and its Go version, followed
// by tab-indented, tab-separated records:
//
//	/path/to/app: go1.24.5
//		path	github.com/acme/app/cmd/app
//		mod	github.com/acme/app	v1.2.3	h1:…
//		dep	github.com/acme/lib	v0.4.0	h1:…
//		dep	github.com/other/pkg	v1.0.0	h1:…
//		=>	github.com/other/fork	v1.0.1	h1:…
//		build	vcs.revision=deadbeef
//
// A "=>" record replaces the dep immediately preceding it.
func ParseBuildInfo(out []byte) (*BuildInfo, error) {
	bi := &BuildInfo{Settings: map[string]string{}}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	sawHeader := false
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		// The header is the only line that is not indented.
		if !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, " ") {
			if _, ver, ok := strings.Cut(line, ": "); ok {
				bi.GoVersion = strings.TrimSpace(ver)
				sawHeader = true
			}
			continue
		}

		fields := strings.Split(strings.TrimLeft(line, " \t"), "\t")
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		if len(fields) == 0 || fields[0] == "" {
			continue
		}

		switch fields[0] {
		case "path":
			if len(fields) >= 2 {
				bi.Path = fields[1]
			}
		case "mod":
			if len(fields) >= 3 {
				bi.Main = Module{Path: fields[1], Version: fields[2], Main: true}
			}
		case "dep":
			if len(fields) >= 3 {
				bi.Deps = append(bi.Deps, Module{Path: fields[1], Version: fields[2]})
			}
		case "=>":
			// Applies to the dep just recorded.
			if len(fields) >= 3 && len(bi.Deps) > 0 {
				bi.Deps[len(bi.Deps)-1].Replace = &Module{Path: fields[1], Version: fields[2]}
			}
		case "build":
			if len(fields) >= 2 {
				k, v, _ := strings.Cut(fields[1], "=")
				bi.Settings[k] = v
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("gomod: reading build info: %w", err)
	}
	if !sawHeader && len(bi.Deps) == 0 {
		return nil, errors.New("gomod: no build info found; not a Go binary, or built without module support")
	}
	return bi, nil
}
