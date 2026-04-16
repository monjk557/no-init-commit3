package repositories

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/gitutil"
)

// nativeSignature represents a Git author/committer for native git operations.
type nativeSignature struct {
	Name  string
	Email string
	When  time.Time
}

// nativeMkTreeEntry represents a single entry for git mktree.
type nativeMkTreeEntry struct {
	Mode string // "100644", "100755", "040000"
	Type string // "blob", "tree"
	Hash string
	Name string
}

// nativeInitBare initializes a new bare git repository at path.
func nativeInitBare(ctx context.Context, path string) error {
	cmd := exec.CommandContext(ctx, "git", "init", "--bare", path)
	if out, err := gitutil.CombinedOutputWithLimit(cmd, gitutil.DefaultMaxOutput); err != nil {
		return fmt.Errorf("git init --bare failed: %s: %w", string(out), err)
	}
	return nil
}

// nativeSetSymbolicRef sets HEAD to point to the given branch ref.
func nativeSetSymbolicRef(ctx context.Context, repoPath, target string) error {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "symbolic-ref", "HEAD", target)
	if out, err := gitutil.CombinedOutputWithLimit(cmd, gitutil.DefaultMaxOutput); err != nil {
		return fmt.Errorf("git symbolic-ref failed: %s: %w", string(out), err)
	}
	return nil
}

// nativeSymbolicRef returns the target of HEAD (e.g. "refs/heads/main").
// Returns empty string if HEAD is detached.
func nativeSymbolicRef(ctx context.Context, repoPath string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "symbolic-ref", "-q", "HEAD")
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		// Exit code 1 = detached HEAD (not an error)
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", fmt.Errorf("git symbolic-ref failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// nativeHashObject writes content to the object store and returns the blob hash.
func nativeHashObject(ctx context.Context, repoPath string, content []byte) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "hash-object", "-w", "--stdin")
	cmd.Stdin = bytes.NewReader(content)
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return "", fmt.Errorf("git hash-object failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// nativeMkTree creates a tree object from the given entries and returns its hash.
func nativeMkTree(ctx context.Context, repoPath string, entries []nativeMkTreeEntry) (string, error) {
	var buf bytes.Buffer
	for _, e := range entries {
		fmt.Fprintf(&buf, "%s %s %s\t%s\n", e.Mode, e.Type, e.Hash, e.Name)
	}

	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "mktree")
	cmd.Stdin = &buf
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return "", fmt.Errorf("git mktree failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// nativeCommitTree creates a commit object pointing to the given tree with optional parents.
func nativeCommitTree(ctx context.Context, repoPath, treeHash string, parents []string, message string, author, committer *nativeSignature) (string, error) {
	args := []string{"--git-dir", repoPath, "commit-tree", treeHash}
	for _, p := range parents {
		args = append(args, "-p", p)
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stdin = strings.NewReader(message)

	env := cmd.Environ()
	if author != nil {
		env = append(env,
			"GIT_AUTHOR_NAME="+author.Name,
			"GIT_AUTHOR_EMAIL="+author.Email,
			"GIT_AUTHOR_DATE="+author.When.Format(time.RFC3339),
		)
	}
	if committer != nil {
		env = append(env,
			"GIT_COMMITTER_NAME="+committer.Name,
			"GIT_COMMITTER_EMAIL="+committer.Email,
			"GIT_COMMITTER_DATE="+committer.When.Format(time.RFC3339),
		)
	}
	cmd.Env = env

	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return "", fmt.Errorf("git commit-tree failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// nativeUpdateRef sets a reference to the given hash.
func nativeUpdateRef(ctx context.Context, repoPath, refName, hash string) error {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "update-ref", refName, hash)
	if out, err := gitutil.CombinedOutputWithLimit(cmd, gitutil.DefaultMaxOutput); err != nil {
		return fmt.Errorf("git update-ref failed: %s: %w", string(out), err)
	}
	return nil
}

// nativeResolveRef resolves a ref to its SHA hash.
func nativeResolveRef(ctx context.Context, repoPath, ref string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "rev-parse", ref)
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return "", fmt.Errorf("failed to resolve ref '%s': %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// nativeRevParseVerify verifies and resolves a ref. Returns hash and whether it exists.
func nativeRevParseVerify(ctx context.Context, repoPath, ref string) (string, bool) {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "rev-parse", "--verify", ref)
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// nativeCatFileType returns the type of an object ("commit", "tree", "blob", "tag").
func nativeCatFileType(ctx context.Context, repoPath, hash string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "cat-file", "-t", hash)
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return "", fmt.Errorf("git cat-file -t failed for %s: %w", hash, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// nativeForEachRefCount counts references matching a prefix pattern.
func nativeForEachRefCount(ctx context.Context, repoPath, prefix string) (int, error) {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "for-each-ref", "--format=%(refname)", prefix)
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return 0, fmt.Errorf("git for-each-ref failed: %w", err)
	}
	output := strings.TrimSpace(string(out))
	if output == "" {
		return 0, nil
	}
	return len(strings.Split(output, "\n")), nil
}

// nativeRevListCount counts the number of commits reachable from ref.
func nativeRevListCount(ctx context.Context, repoPath, ref string) (int, error) {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "rev-list", "--count", ref)
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return 0, fmt.Errorf("git rev-list --count failed: %w", err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("failed to parse commit count: %w", err)
	}
	return count, nil
}

// nativeHasAnyRefs checks if the repository has any refs at all.
func nativeHasAnyRefs(ctx context.Context, repoPath string) bool {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "for-each-ref", "--count=1", "--format=%(refname)")
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// nativeDereferenceTag resolves a tag ref to the underlying commit hash.
// For annotated tags, it dereferences to the target commit.
// For lightweight tags, returns the hash directly.
func nativeDereferenceTag(ctx context.Context, repoPath, tagRef string) (string, error) {
	// Try ^{commit} to dereference annotated tags
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "rev-parse", tagRef+"^{commit}")
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return "", fmt.Errorf("failed to dereference tag %s: %w", tagRef, err)
	}
	return strings.TrimSpace(string(out)), nil
}
