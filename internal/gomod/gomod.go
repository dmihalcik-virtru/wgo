// Package gomod maps Go module paths onto the repositories, subdirectories and
// version-control refs they are published from.
//
// The mapping matters because a monorepo publishes each of its modules
// independently: a module in subdirectory `service` is released under the tag
// `service/v0.11.6`, so a single downstream artifact routinely pins several
// different commits of the same repository at once. Reconstructing the source
// for such an artifact means resolving every pin back to a (repo, subdir, tag)
// triple, which is what this package does.
//
// The repository rule is deliberately structural rather than prefix-based:
// after stripping any trailing major-version element, the first two path
// elements following a recognised code host name the repository and everything
// after them is the subdirectory. Prefix matching gets this wrong in practice —
// github.com/opentdf/otdfctl is its own repository, while
// github.com/opentdf/platform/otdfctl is a module inside opentdf/platform.
//
// Everything here is pure: no subprocesses, no network, no filesystem. The
// `go` toolchain shell-outs that produce this package's inputs live in
// internal/gotool.
package gomod

import (
	"errors"
	"fmt"
	"go/version"
	"path"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

var (
	// ErrUnsupportedHost is returned for module paths whose host is not a code
	// host we know how to map, e.g. "golang.org/x/mod" or "gopkg.in/square/go-jose.v2".
	//
	// The element-count check runs first, so a two-element vanity path such as
	// "gopkg.in/yaml.v3" returns ErrNotRepoPath instead. Both mean "no checkout
	// for this module"; do not switch on them to tell a vanity import from a
	// malformed one.
	ErrUnsupportedHost = errors.New("gomod: module host is not a supported code host")
	// ErrNotRepoPath is returned for module paths with too few elements to name
	// a repository, e.g. "github.com/opentdf" or "gopkg.in/yaml.v3".
	ErrNotRepoPath = errors.New("gomod: module path has too few elements to name a repo")
	// ErrMalformedPath is returned when a path cannot be split into a module
	// prefix and major-version suffix at all.
	ErrMalformedPath = errors.New("gomod: malformed module path")
)

// codeHosts are the hosts whose URL layout is <host>/<owner>/<repo>/<subdir…>.
//
// gitlab.com is deliberately absent: it permits arbitrarily nested subgroups,
// so the two-element rule would silently produce the wrong repository.
var codeHosts = map[string]bool{
	"github.com": true,
}

// Origin identifies where a module's source lives in version control.
type Origin struct {
	Host   string // "github.com"
	Owner  string // "opentdf"
	Repo   string // "platform"
	Subdir string // "" for a root module; "service"; "protocol/go"
	Major  string // "" or "v2" — the /vN element stripped from the module path
}

// Slug is the "owner/repo" form used throughout wgo.
func (o Origin) Slug() string { return o.Owner + "/" + o.Repo }

// ParseOrigin maps a module path to an Origin.
//
// A trailing major-version element is stripped first, so:
//
//	github.com/virtru-corp/data-security-platform/v2     -> dsp,      subdir ""
//	github.com/virtru-corp/data-security-platform/sdk/v2 -> dsp,      subdir "sdk"
//	github.com/opentdf/otdfctl                           -> otdfctl,  subdir ""
//	github.com/opentdf/platform/otdfctl                  -> platform, subdir "otdfctl"
func ParseOrigin(modulePath string) (Origin, error) {
	prefix, pathMajor, ok := module.SplitPathVersion(modulePath)
	if !ok {
		return Origin{}, fmt.Errorf("%w: %q", ErrMalformedPath, modulePath)
	}
	// pathMajor is "", "/v2", or ".v2" (gopkg.in). Keep just the "vN".
	major := strings.TrimLeft(pathMajor, "/.")

	parts := strings.Split(prefix, "/")
	if len(parts) < 3 {
		return Origin{}, fmt.Errorf("%w: %q", ErrNotRepoPath, modulePath)
	}
	if !codeHosts[parts[0]] {
		return Origin{}, fmt.Errorf("%w: %q", ErrUnsupportedHost, modulePath)
	}
	return Origin{
		Host:   parts[0],
		Owner:  parts[1],
		Repo:   parts[2],
		Subdir: strings.Join(parts[3:], "/"),
		Major:  major,
	}, nil
}

// canonicalVersion strips the +incompatible build tag.
//
// Go records it in go.mod for a v2+ module that has no /vN path suffix, but it
// is metadata about the module, not part of the version: the git tag is plain
// "v2.0.0". Leaving it on would produce a tag that cannot exist, and callers
// read an unresolvable tag as a wrong module->repo mapping.
func canonicalVersion(version string) string {
	return strings.TrimSuffix(version, "+incompatible")
}

// TagFor returns the VCS tag a module version is published under.
//
// The major-version element is not part of the tag — it lives in the module
// path only — so a module at subdir "sdk" with path suffix /v2 releases
// v2.7.1 as "sdk/v2.7.1", not "sdk/v2/v2.7.1".
func (o Origin) TagFor(version string) string {
	version = canonicalVersion(version)
	if o.Subdir == "" {
		return version
	}
	return o.Subdir + "/" + version
}

// Revset returns a jj revset that resolves version inside the origin repo.
//
// Pseudo-versions carry their commit directly, so they resolve to the short
// hash. Everything else resolves through the tag, wrapped so that the revset
// is unambiguous against a same-named bookmark and evaluates to an empty set —
// rather than erroring — when the tag is not present locally.
func (o Origin) Revset(version string) string {
	if rev := PseudoCommit(version); rev != "" {
		return rev
	}
	return fmt.Sprintf("present(tags(exact:%q))", o.TagFor(version))
}

// PseudoCommit returns the commit a pseudo-version was minted from, or "" if
// version is an ordinary tagged version.
func PseudoCommit(version string) string {
	v := canonicalVersion(version)
	if !module.IsPseudoVersion(v) {
		return ""
	}
	rev, err := module.PseudoVersionRev(v)
	if err != nil {
		return ""
	}
	return rev
}

// DevelVersion is the version Go records for a module that was not consumed
// from the module proxy: one supplied by a go.work `use`, replaced by a
// directory, or built straight from its own working tree.
const DevelVersion = "(devel)"

// CompareVersions orders two module versions the way MVS does, reporting
// ok=false when they cannot be ordered at all.
//
// The distinction matters to drift detection. semver.Compare ranks an invalid
// version below every valid one and calls two invalid ones equal, so on its own
// it would report a garbage version as a downgrade, or silently pass a pair of
// them off as unchanged. A difference that cannot be ordered is neither an
// upgrade nor a downgrade, and saying so is more use than picking one.
func CompareVersions(a, b string) (int, bool) {
	ca, cb := canonicalVersion(strings.TrimSpace(a)), canonicalVersion(strings.TrimSpace(b))
	if !semver.IsValid(ca) || !semver.IsValid(cb) {
		return 0, false
	}
	return semver.Compare(ca, cb), true
}

// ToolchainVersion turns the toolchain string a binary records ("go1.27.0")
// into a bare `go` directive version ("1.27.0"), or "" if it is not one.
func ToolchainVersion(s string) string {
	s = strings.TrimSpace(s)
	// A toolchain may carry a suffix, as in "go1.27.0-custom"; the directive
	// cannot, and MaxGoVersion drops what go/version rejects.
	if before, _, ok := strings.Cut(s, "-"); ok {
		s = before
	}
	return MaxGoVersion(strings.TrimPrefix(s, "go"))
}

// IsResolvableVersion reports whether a version can be mapped back to a commit.
//
// Not every entry in a build list names a release. A module supplied by a
// go.work, replaced by a directory, or built from a working tree is recorded as
// "(devel)"; a main module is recorded with no version at all. Neither is a tag
// or a pseudo-version, so no revset resolves it, and attempting one produces
// `tags(exact:"(devel)")` — an empty result whose error message blames a
// missing tag for what is really an unpinnable module.
func IsResolvableVersion(version string) bool {
	return semver.IsValid(canonicalVersion(strings.TrimSpace(version)))
}

// InOrg reports whether modulePath falls under any of prefixes.
//
// Matching is on whole path elements, so the prefix "github.com/opentdf" does
// not match "github.com/opentdfx/thing".
func InOrg(modulePath string, prefixes []string) bool {
	for _, p := range prefixes {
		p = strings.TrimSuffix(strings.TrimSpace(p), "/")
		if p == "" {
			continue
		}
		if modulePath == p || strings.HasPrefix(modulePath, p+"/") {
			return true
		}
	}
	return false
}

// MaxGoVersion returns the highest of the given `go` directive versions
// ("1.24.5", "1.25", "1.25rc1"), or "" if none are usable.
//
// Comparison goes through go/version rather than semver: a `go` directive is
// not semver, and semver rejects the prerelease forms Go allows, so "1.25rc1"
// would lose to "1.24" and silently downgrade the generated go.work below what
// a member module requires. Unparseable versions are dropped rather than
// returned verbatim, which would write `go <garbage>` into go.work.
func MaxGoVersion(versions ...string) string {
	best := ""
	for _, v := range versions {
		v = strings.TrimSpace(v)
		if v == "" || !version.IsValid("go"+v) {
			continue
		}
		if best == "" || version.Compare("go"+v, "go"+best) > 0 {
			best = v
		}
	}
	return best
}

// Replace is one replace directive from a go.mod or go.work.
type Replace struct {
	OldPath, OldVersion string
	NewPath, NewVersion string
}

// IsLocal reports whether the replacement points at a directory rather than
// another module version. Go signals this by omitting the version.
func (r Replace) IsLocal() bool { return r.NewVersion == "" }

// LocalReplaceTargets returns the repo-root-relative directories that modDir's
// local replace directives point at, given the module's own subdir within the
// repository. Targets that leave the repository (or are absolute) cannot be
// satisfied from a single checkout and are returned in escaped.
//
// Sparse checkouts must be widened to cover inRepo, or the replaced module's
// source will be missing from the working copy.
func LocalReplaceTargets(f *modfile.File, subdir string) (inRepo []string, escaped []string) {
	if f == nil {
		return nil, nil
	}
	seen := map[string]bool{}
	for _, r := range f.Replace {
		if r.New.Version != "" {
			continue // replaced with another module version, not a directory
		}
		target := r.New.Path
		if target == "" {
			continue
		}
		if path.IsAbs(target) {
			escaped = append(escaped, target)
			continue
		}
		joined := path.Join(subdir, target)
		if joined == ".." || strings.HasPrefix(joined, "../") {
			escaped = append(escaped, target)
			continue
		}
		if joined == "." {
			joined = ""
		}
		if !seen[joined] {
			seen[joined] = true
			inRepo = append(inRepo, joined)
		}
	}
	sort.Strings(inRepo)
	sort.Strings(escaped)
	return inRepo, escaped
}
