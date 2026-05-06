package repositories

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/gitutil"
)

const gitmodulesReadTimeout = 5 * time.Second

// ErrGitmodulesNotFound is returned when the ref does not contain a
// .gitmodules file. Callers should treat this as a non-fatal signal — the
// repository simply has no submodules at that ref.
var ErrGitmodulesNotFound = errors.New("gitmodules: file not found at ref")

// SubmoduleConfig is a single [submodule "name"] section parsed from
// .gitmodules. It mirrors the on-disk INI layout, not the gitlink commit hash.
// The pinned commit must be sourced separately from `git ls-tree` (mode 160000).
type SubmoduleConfig struct {
	Name   string // section name — e.g. "vendor/protobuf"
	Path   string // working-tree path of the submodule
	URL    string // remote url
	Branch string // optional: branch to track (empty when unset)
}

// GitmodulesIndex is the parsed view of a .gitmodules file at a specific ref.
// LookupByPath returns nil when the path is not declared as a submodule.
type GitmodulesIndex struct {
	byPath map[string]*SubmoduleConfig
}

// LookupByPath returns the SubmoduleConfig declared for the given working-tree
// path, or nil if the path is not a submodule. Path comparison is exact —
// callers must pass the entry's Path as it appears in the tree.
func (g *GitmodulesIndex) LookupByPath(path string) *SubmoduleConfig {
	if g == nil || g.byPath == nil {
		return nil
	}
	return g.byPath[path]
}

// IsEmpty reports whether the index contains no submodule sections.
func (g *GitmodulesIndex) IsEmpty() bool {
	return g == nil || len(g.byPath) == 0
}

// ParseGitmodules parses the byte content of a .gitmodules file and returns
// an index keyed by submodule path. Empty input yields an empty (non-nil)
// index — callers can rely on LookupByPath always being safe to call.
//
// The format is git-config style INI:
//
//	[submodule "name"]
//	    path = vendor/foo
//	    url  = https://github.com/example/foo
//	    branch = main
//
// Sections that are not "submodule" headers are ignored. Sections without a
// non-empty path are also ignored — they cannot map to a tree entry.
func ParseGitmodules(content []byte) *GitmodulesIndex {
	idx := &GitmodulesIndex{byPath: make(map[string]*SubmoduleConfig)}

	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), 1*1024*1024)

	var current *SubmoduleConfig
	flush := func() {
		if current == nil {
			return
		}
		if current.Path != "" {
			idx.byPath[current.Path] = current
		}
		current = nil
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			flush()
			name, ok := parseSubmoduleSectionHeader(line)
			if !ok {
				current = nil
				continue
			}
			current = &SubmoduleConfig{Name: name}
			continue
		}

		if current == nil {
			continue
		}

		key, value, ok := splitGitmodulesAssignment(line)
		if !ok {
			continue
		}
		switch strings.ToLower(key) {
		case "path":
			current.Path = value
		case "url":
			current.URL = value
		case "branch":
			current.Branch = value
		}
	}
	flush()

	return idx
}

// parseSubmoduleSectionHeader extracts the section name from a header like
//
//	[submodule "vendor/foo"]
//
// Returns ok=false for any other section type (e.g. [core], [remote "x"]).
func parseSubmoduleSectionHeader(line string) (string, bool) {
	inner := strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
	inner = strings.TrimSpace(inner)
	const prefix = "submodule"
	if !strings.HasPrefix(inner, prefix) {
		return "", false
	}
	rest := strings.TrimSpace(inner[len(prefix):])
	if !strings.HasPrefix(rest, "\"") || !strings.HasSuffix(rest, "\"") || len(rest) < 2 {
		return "", false
	}
	name := rest[1 : len(rest)-1]
	if name == "" {
		return "", false
	}
	return name, true
}

// splitGitmodulesAssignment splits a "key = value" line. Inline comments
// starting with `#` or `;` are stripped. Surrounding double quotes are
// removed if present.
func splitGitmodulesAssignment(line string) (string, string, bool) {
	// Strip inline comment — git-config does not support inline `;` comments,
	// but `#` mid-value is rare; we still trim it conservatively to avoid
	// surfacing trailing junk if a hand-edited file contains it.
	if idx := strings.IndexAny(line, "#;"); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}
	eq := strings.IndexByte(line, '=')
	if eq < 0 {
		return "", "", false
	}
	key := strings.TrimSpace(line[:eq])
	value := strings.TrimSpace(line[eq+1:])
	if key == "" {
		return "", "", false
	}
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		value = value[1 : len(value)-1]
	}
	return key, value, true
}

// LoadGitmodulesAtRef reads `<ref>:.gitmodules` from a bare repository and
// returns the parsed index. ErrGitmodulesNotFound is returned (without
// wrapping) when the ref does not contain a .gitmodules file — callers
// should treat that as the common "no submodules" path, not a failure.
func LoadGitmodulesAtRef(ctx context.Context, repoPath, ref string) (*GitmodulesIndex, error) {
	if ref == "" {
		ref = "HEAD"
	}

	cmdCtx, cancel := context.WithTimeout(ctx, gitmodulesReadTimeout)
	defer cancel()

	target := fmt.Sprintf("%s:.gitmodules", ref)
	cmd := exec.CommandContext(cmdCtx, "git", "-c", "core.quotePath=false", "--git-dir", repoPath, "show", target)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.MaxOutputSmall)
	if err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if isGitmodulesMissingError(errMsg) {
			return nil, ErrGitmodulesNotFound
		}
		return nil, fmt.Errorf("git show %s: %s: %w", target, errMsg, err)
	}

	return ParseGitmodules(out), nil
}

// isGitmodulesMissingError detects the stderr signatures git emits when a
// path does not exist in a tree, so we can map them to ErrGitmodulesNotFound.
func isGitmodulesMissingError(msg string) bool {
	if msg == "" {
		return false
	}
	low := strings.ToLower(msg)
	return strings.Contains(low, "does not exist") ||
		strings.Contains(low, "exists on disk, but not in") ||
		strings.Contains(low, "path '.gitmodules' does not exist") ||
		strings.Contains(low, "not a valid object name")
}
