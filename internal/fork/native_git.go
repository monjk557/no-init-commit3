package fork

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// nativeCloneBare clones a bare repository from source to destination.
// Uses git clone --bare which copies all refs and objects.
func nativeCloneBare(ctx context.Context, sourcePath, destPath string) error {
	cmd := exec.CommandContext(ctx, "git", "clone", "--bare", sourcePath, destPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone --bare failed: %s: %w", string(out), err)
	}
	return nil
}

// nativeRemoteAdd adds a named remote to a bare repository.
func nativeRemoteAdd(ctx context.Context, repoPath, name, url string) error {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "remote", "add", name, url)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git remote add failed: %s: %w", string(out), err)
	}
	return nil
}

// nativeRemoteSetURL sets the URL of an existing remote.
func nativeRemoteSetURL(ctx context.Context, repoPath, name, url string) error {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "remote", "set-url", name, url)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git remote set-url failed: %s: %w", string(out), err)
	}
	return nil
}

// nativeRemoteGetURL returns the URL of a named remote.
func nativeRemoteGetURL(ctx context.Context, repoPath, name string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "remote", "get-url", name)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git remote get-url failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// nativeRemoteRemove removes a named remote from a bare repository.
func nativeRemoteRemove(ctx context.Context, repoPath, name string) error {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "remote", "remove", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git remote remove failed: %s: %w", string(out), err)
	}
	return nil
}

// nativeFetchUpstream fetches all refs from the upstream remote.
func nativeFetchUpstream(ctx context.Context, repoPath, remoteName string) error {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "fetch", remoteName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch %s failed: %s: %w", remoteName, string(out), err)
	}
	return nil
}

// refInfo represents a git reference with its hash.
type refInfo struct {
	Name string
	Hash string
}

// nativeForEachRef lists refs matching a pattern.
// Returns slice of refInfo with name (short) and hash.
func nativeForEachRef(ctx context.Context, repoPath, pattern string) ([]refInfo, error) {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "for-each-ref",
		"--format=%(objectname) %(refname:short)", pattern)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git for-each-ref failed: %w", err)
	}

	output := strings.TrimSpace(string(out))
	if output == "" {
		return nil, nil
	}

	var refs []refInfo
	for _, line := range strings.Split(output, "\n") {
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		refs = append(refs, refInfo{
			Hash: parts[0],
			Name: parts[1],
		})
	}
	return refs, nil
}

// nativeRevListCount counts commits reachable from ref.
func nativeRevListCount(ctx context.Context, repoPath, ref string) (int, error) {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "rev-list", "--count", ref)
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("git rev-list --count failed: %w", err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("failed to parse commit count: %w", err)
	}
	return count, nil
}

// nativeRevListCountBetween counts commits between base and head (base..head).
func nativeRevListCountBetween(ctx context.Context, repoPath, base, head string) (int, error) {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "rev-list", "--count", base+".."+head)
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("git rev-list --count %s..%s failed: %w", base, head, err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("failed to parse commit count: %w", err)
	}
	return count, nil
}

// nativeMergeBase finds the merge base (common ancestor) of two commits.
func nativeMergeBase(ctx context.Context, repoPath, commit1, commit2 string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "merge-base", commit1, commit2)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git merge-base failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// nativeUpdateRef sets a reference to the given hash.
func nativeUpdateRef(ctx context.Context, repoPath, refName, hash string) error {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "update-ref", refName, hash)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git update-ref failed: %s: %w", string(out), err)
	}
	return nil
}

// nativeSymbolicRef returns the target of HEAD.
func nativeSymbolicRef(ctx context.Context, repoPath string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "symbolic-ref", "-q", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", fmt.Errorf("git symbolic-ref failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// nativeSetSymbolicRef sets HEAD to point to the given branch ref.
func nativeSetSymbolicRef(ctx context.Context, repoPath, target string) error {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "symbolic-ref", "HEAD", target)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git symbolic-ref failed: %s: %w", string(out), err)
	}
	return nil
}

// nativeListRemotes lists all configured remotes.
func nativeListRemotes(ctx context.Context, repoPath string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "remote")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git remote failed: %s: %w", stderr.String(), err)
	}
	output := strings.TrimSpace(stdout.String())
	if output == "" {
		return nil, nil
	}
	return strings.Split(output, "\n"), nil
}
