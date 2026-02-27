// Package gitcmd provides native git command wrappers for protocol-level operations.
// It replaces go-git library calls with exec-based git commands following the Gitaly pattern:
// protocol framing is maintained in Go, while heavy git operations are delegated to native git.
package gitcmd

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// RefEntry represents a single Git reference returned by ListRefs.
type RefEntry struct {
	ObjectName string // SHA-1 hex
	ObjectType string // "commit", "tag", "tree", "blob"
	RefName    string // e.g. "refs/heads/main"
	PeeledOID  string // dereferenced OID for tag objects (empty if not a tag)
	SymRef     string // symref target (empty if not symbolic)
}

// HEADInfo holds resolved HEAD information.
type HEADInfo struct {
	OID        string // resolved commit OID
	IsSymbolic bool
	Target     string // e.g. "refs/heads/main" (only if symbolic)
}

// ListRefs runs `git for-each-ref` and returns matching references.
// patterns are ref-prefix patterns (e.g. "refs/heads/", "refs/tags/").
// If no patterns are given, all refs are listed.
func ListRefs(ctx context.Context, repoPath string, patterns ...string) ([]RefEntry, error) {
	// Format: objectname objecttype refname dereferenced-oid symref
	format := "%(objectname) %(objecttype) %(refname) %(*objectname) %(symref)"
	args := []string{"for-each-ref", "--format=" + format}
	args = append(args, patterns...)

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git for-each-ref: %w", err)
	}

	var refs []RefEntry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		// Fields: objectname objecttype refname *objectname symref
		// *objectname and symref may be empty
		fields := strings.SplitN(line, " ", 5)
		if len(fields) < 3 {
			continue
		}
		entry := RefEntry{
			ObjectName: fields[0],
			ObjectType: fields[1],
			RefName:    fields[2],
		}
		if len(fields) > 3 {
			entry.PeeledOID = strings.TrimSpace(fields[3])
		}
		if len(fields) > 4 {
			entry.SymRef = strings.TrimSpace(fields[4])
		}
		refs = append(refs, entry)
	}
	return refs, nil
}

// ResolveHEAD resolves the HEAD reference of a bare repository.
// Returns the resolved OID and symbolic target (if HEAD is symbolic).
func ResolveHEAD(ctx context.Context, repoPath string) (*HEADInfo, error) {
	info := &HEADInfo{}

	// Check if HEAD is symbolic
	symCmd := exec.CommandContext(ctx, "git", "symbolic-ref", "HEAD")
	symCmd.Dir = repoPath
	symOut, symErr := symCmd.Output()
	if symErr == nil {
		// HEAD is symbolic
		info.IsSymbolic = true
		info.Target = strings.TrimSpace(string(symOut))
	}

	// Resolve HEAD to OID
	revCmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	revCmd.Dir = repoPath
	var stderr bytes.Buffer
	revCmd.Stderr = &stderr
	revOut, revErr := revCmd.Output()
	if revErr != nil {
		// HEAD may be unborn (no commits yet)
		return info, fmt.Errorf("git rev-parse HEAD: %w (stderr: %s)", revErr, stderr.String())
	}
	info.OID = strings.TrimSpace(string(revOut))

	return info, nil
}
