package gitcmd

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// GetShallowBoundary computes which commits should become shallow grafts on the client.
// For a shallow fetch with deepen=N, these are the commits at depth N whose parents
// will NOT be included in the packfile. The client marks these commits as shallow
// (parents unavailable). The returned OIDs are used in:
//   - shallow-info section: "shallow <OID>" lines sent to client
//   - pack-objects stdin:   "--shallow <OID>" lines to limit traversal
func GetShallowBoundary(ctx context.Context, repoPath string, wants []string, haves []string, depth int) ([]string, error) {
	if depth <= 0 || len(wants) == 0 {
		return nil, nil
	}

	// For depth=1, the want commits themselves are the shallow grafts
	// (their parents are not included in the pack).
	if depth == 1 {
		seen := make(map[string]bool)
		var result []string
		for _, w := range wants {
			if !seen[w] {
				seen[w] = true
				result = append(result, w)
			}
		}
		return result, nil
	}

	// For depth > 1, shallow grafts = commits reachable within depth N
	// but NOT within depth N-1. These are the commits at exactly depth N
	// whose parents are cut off.
	atN, err := revListMaxCount(ctx, repoPath, wants, haves, depth)
	if err != nil {
		return nil, fmt.Errorf("rev-list at depth %d: %w", depth, err)
	}
	atNMinus1, err := revListMaxCount(ctx, repoPath, wants, haves, depth-1)
	if err != nil {
		return nil, fmt.Errorf("rev-list at depth %d: %w", depth-1, err)
	}

	inner := make(map[string]bool, len(atNMinus1))
	for _, c := range atNMinus1 {
		inner[c] = true
	}

	var shallow []string
	for _, c := range atN {
		if !inner[c] {
			shallow = append(shallow, c)
		}
	}
	return shallow, nil
}

// revListMaxCount runs `git rev-list --max-count=N --stdin` and returns commit OIDs.
func revListMaxCount(ctx context.Context, repoPath string, wants, haves []string, maxCount int) ([]string, error) {
	args := []string{"rev-list", "--stdin"}
	if maxCount > 0 {
		args = append(args, "--max-count="+strconv.Itoa(maxCount))
	}

	var stdinBuf bytes.Buffer
	for _, w := range wants {
		stdinBuf.WriteString(w)
		stdinBuf.WriteByte('\n')
	}
	for _, h := range haves {
		stdinBuf.WriteString("^")
		stdinBuf.WriteString(h)
		stdinBuf.WriteByte('\n')
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoPath
	cmd.Stdin = &stdinBuf
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git rev-list --max-count: %w (stderr: %s)", err, stderr.String())
	}

	var commits []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			commits = append(commits, line)
		}
	}
	return commits, nil
}

// CountObjects runs `git rev-list --count --objects --stdin` to count the number of
// objects that would be sent in a fetch.
func CountObjects(ctx context.Context, repoPath string, wants, haves []string, depth int) (int, error) {
	args := []string{"rev-list", "--count", "--objects", "--stdin"}
	if depth > 0 {
		args = append(args, "--max-count="+strconv.Itoa(depth))
	}

	var stdinBuf bytes.Buffer
	for _, w := range wants {
		stdinBuf.WriteString(w)
		stdinBuf.WriteByte('\n')
	}
	for _, h := range haves {
		stdinBuf.WriteString("^")
		stdinBuf.WriteString(h)
		stdinBuf.WriteByte('\n')
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoPath
	cmd.Stdin = &stdinBuf
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("git rev-list --count: %w (stderr: %s)", err, stderr.String())
	}

	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("parse rev-list --count: %w", err)
	}
	return n, nil
}
