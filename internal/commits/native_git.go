package commits

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

const nativeGitTimeout = 10 * time.Second

// NativeSignature represents author/committer info for native git operations.
type NativeSignature struct {
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

// nativeLsTreeEntry represents a single entry from git ls-tree output.
type nativeLsTreeEntry struct {
	Mode string
	Type string
	Hash string
	Path string
}

// nativeResolveRef resolves a ref (branch name, tag, HEAD, hash) to a commit hash.
func nativeResolveRef(ctx context.Context, repoPath, ref string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "rev-parse", "--verify", ref)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		return "", fmt.Errorf("failed to resolve ref '%s': %s", ref, errMsg)
	}
	return strings.TrimSpace(string(out)), nil
}

// nativeSymbolicRef returns the symbolic ref target of HEAD (e.g. "refs/heads/main").
// Returns empty string if HEAD is detached.
func nativeSymbolicRef(ctx context.Context, repoPath string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "symbolic-ref", "-q", "HEAD")
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		// Detached HEAD or no HEAD → return empty
		return "", nil
	}
	return strings.TrimSpace(string(out)), nil
}

// nativeSetSymbolicRef sets HEAD to point to a branch ref.
func nativeSetSymbolicRef(ctx context.Context, repoPath, target string) error {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "symbolic-ref", "HEAD", target)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to set symbolic ref: %s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

// nativeGetTreeHash returns the tree hash for a given commit hash.
func nativeGetTreeHash(ctx context.Context, repoPath, commitHash string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "rev-parse", "--verify", commitHash+"^{tree}")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return "", fmt.Errorf("failed to get tree hash: %s", strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// nativeHashObject writes content as a blob object and returns its hash.
func nativeHashObject(ctx context.Context, repoPath string, content []byte) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "hash-object", "-w", "--stdin")
	cmd.Stdin = bytes.NewReader(content)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return "", fmt.Errorf("failed to hash object: %s", strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// nativeMkTree creates a tree object from entries and returns its hash.
func nativeMkTree(ctx context.Context, repoPath string, entries []nativeMkTreeEntry) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	// Build mktree input: "<mode> <type> <hash>\t<name>\n"
	var input bytes.Buffer
	for _, e := range entries {
		fmt.Fprintf(&input, "%s %s %s\t%s\n", e.Mode, e.Type, e.Hash, e.Name)
	}

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "mktree")
	cmd.Stdin = &input
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return "", fmt.Errorf("failed to create tree: %s", strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// nativeCommitTree creates a commit object and returns its hash.
func nativeCommitTree(ctx context.Context, repoPath, treeHash string, parents []string, message string, author, committer *NativeSignature) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	args := []string{"--git-dir", repoPath, "commit-tree", treeHash}
	for _, p := range parents {
		args = append(args, "-p", p)
	}

	cmd := exec.CommandContext(cmdCtx, "git", args...)
	cmd.Stdin = strings.NewReader(message)

	// Set author/committer via environment
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

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return "", fmt.Errorf("failed to create commit: %s", strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// nativeUpdateRef updates a ref to point to a new hash.
func nativeUpdateRef(ctx context.Context, repoPath, refName, hash string) error {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "update-ref", refName, hash)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to update ref '%s': %s", refName, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// nativeDeleteRef deletes a ref.
func nativeDeleteRef(ctx context.Context, repoPath, refName string) error {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "update-ref", "-d", refName)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to delete ref '%s': %s", refName, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// nativeLsTree lists all entries in a tree recursively.
// Returns all files with their mode, type, hash, and path.
func nativeLsTree(ctx context.Context, repoPath, treeHash string) ([]nativeLsTreeEntry, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "-c", "core.quotePath=false", "--git-dir", repoPath, "ls-tree", "-r", treeHash)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return nil, fmt.Errorf("failed to list tree: %s", strings.TrimSpace(stderr.String()))
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, nil
	}

	var entries []nativeLsTreeEntry
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		// format: <mode> <type> <hash>\t<path>
		tabParts := strings.SplitN(line, "\t", 2)
		if len(tabParts) != 2 {
			continue
		}
		metaParts := strings.Fields(tabParts[0])
		if len(metaParts) != 3 {
			continue
		}
		entries = append(entries, nativeLsTreeEntry{
			Mode: metaParts[0],
			Type: metaParts[1],
			Hash: metaParts[2],
			Path: gitutil.UnquoteGitPath(tabParts[1]),
		})
	}
	return entries, nil
}

// nativeLsTreePath checks if a specific path exists in a tree.
func nativeLsTreePath(ctx context.Context, repoPath, treeHash, path string) (bool, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "-c", "core.quotePath=false", "--git-dir", repoPath, "ls-tree", treeHash, "--", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return false, fmt.Errorf("failed to check path: %s", strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// nativeCheckRefExists checks if a ref exists in the repository.
func nativeCheckRefExists(ctx context.Context, repoPath, refName string) (bool, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "rev-parse", "--verify", refName)
	if err := cmd.Run(); err != nil {
		return false, nil
	}
	return true, nil
}

// nativeHasRefs checks if any refs exist under a given prefix.
func nativeHasRefs(ctx context.Context, repoPath, prefix string) (bool, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "for-each-ref", "--count=1", prefix, "--format=x")
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return false, nil
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// nativeMergeBase finds the merge base of two commits.
func nativeMergeBase(ctx context.Context, repoPath, hash1, hash2 string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "merge-base", hash1, hash2)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			return "", fmt.Errorf("no common ancestor found between commits")
		}
		return "", fmt.Errorf("failed to find merge base: %s", errMsg)
	}
	return strings.TrimSpace(string(out)), nil
}

// nativeGetCommitLog returns commits using git log with a structured format.
// format: hash\tauthor_name\tauthor_email\tauthor_date\tcommitter_name\tcommitter_email\tcommitter_date\tparent_hashes\ttree_hash\tsubject
func nativeGetCommitLog(ctx context.Context, repoPath, ref string, limit, skip int, excludeAncestorsOf string) ([]Commit, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	format := "%H\t%an\t%ae\t%aI\t%cn\t%ce\t%cI\t%P\t%T\t%s"
	args := []string{"--git-dir", repoPath, "log", "--format=" + format}

	if limit > 0 {
		args = append(args, fmt.Sprintf("--max-count=%d", limit))
	}
	if skip > 0 {
		args = append(args, fmt.Sprintf("--skip=%d", skip))
	}

	// For merge-base based queries: ref ^excludeAncestorsOf
	args = append(args, ref)
	if excludeAncestorsOf != "" {
		args = append(args, "^"+excludeAncestorsOf)
	}

	cmd := exec.CommandContext(cmdCtx, "git", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		return nil, fmt.Errorf("failed to get commit log: %s", errMsg)
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return []Commit{}, nil
	}

	var commits []Commit
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 10)
		if len(parts) < 10 {
			continue
		}

		authorDate, _ := time.Parse(time.RFC3339, parts[3])
		committerDate, _ := time.Parse(time.RFC3339, parts[6])

		var parentHashes []string
		if parts[7] != "" {
			parentHashes = strings.Fields(parts[7])
		}

		// Clean commit message
		message := strings.TrimSpace(parts[9])
		message = strings.ReplaceAll(message, "\n", " ")
		message = strings.ReplaceAll(message, "\r", " ")

		commits = append(commits, Commit{
			Sha:     parts[0],
			Message: message,
			Author: CommitAuthor{
				Name:  parts[1],
				Email: parts[2],
				Date:  authorDate,
			},
			Committer: CommitAuthor{
				Name:  parts[4],
				Email: parts[5],
				Date:  committerDate,
			},
			ParentHashes: parentHashes,
			TreeHash:     parts[8],
		})
	}

	return commits, nil
}

// nativeCommitActivityDates returns per-commit dates within the last `days`
// window reachable from ref, using a lightweight `git log` that decodes only
// the date field (no message/tree/diff — no `git show`/blob access). Cost is
// O(commits-in-window), not O(repo history) (Issue-5247).
//
// `useCommitterDate` selects committer date (%cI) vs author date (%aI) for the
// returned timestamps; the --since window itself always filters on commit date
// (git's default), which is acceptable slop for an approximate sparkline.
func nativeCommitActivityDates(ctx context.Context, repoPath, ref string, days int, useCommitterDate bool) ([]time.Time, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	dateFmt := "%aI"
	if useCommitterDate {
		dateFmt = "%cI"
	}

	args := []string{
		"--git-dir", repoPath, "log",
		fmt.Sprintf("--since=%d.days.ago", days),
		"--pretty=" + dateFmt,
		ref,
	}

	cmd := exec.CommandContext(cmdCtx, "git", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return nil, fmt.Errorf("failed to get commit activity: %s", strings.TrimSpace(stderr.String()))
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return []time.Time{}, nil
	}

	var dates []time.Time
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		t, perr := time.Parse(time.RFC3339, line)
		if perr != nil {
			continue
		}
		dates = append(dates, t)
	}
	return dates, nil
}

// nativeRevListCount counts commits reachable from a ref.
func nativeRevListCount(ctx context.Context, repoPath, ref string) (int, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "rev-list", "--count", ref)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return 0, fmt.Errorf("failed to count commits: %s", strings.TrimSpace(stderr.String()))
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("failed to parse commit count: %w", err)
	}
	return count, nil
}

// nativeCatFileType returns the type of a git object (commit, tree, blob, tag).
func nativeCatFileType(ctx context.Context, repoPath, hash string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "cat-file", "-t", hash)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return "", fmt.Errorf("object not found: %s", hash)
	}
	return strings.TrimSpace(string(out)), nil
}

// nativeGetCommitInfo returns commit info for a single commit hash.
func nativeGetCommitInfo(ctx context.Context, repoPath, hash string) (*Commit, error) {
	commits, err := nativeGetCommitLog(ctx, repoPath, hash, 1, 0, "")
	if err != nil {
		return nil, err
	}
	if len(commits) == 0 {
		return nil, fmt.Errorf("commit not found: %s", hash)
	}
	return &commits[0], nil
}

// nativeRevListCountRange counts commits reachable from 'from' but not from 'excludeAncestorsOf'.
// Equivalent to: git rev-list --count from ^excludeAncestorsOf
func nativeRevListCountRange(ctx context.Context, repoPath, from, excludeAncestorsOf string) (int, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "rev-list", "--count", from, "^"+excludeAncestorsOf)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return 0, fmt.Errorf("failed to count commits: %s", strings.TrimSpace(stderr.String()))
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("failed to parse commit count: %w", err)
	}
	return count, nil
}

// nativeDiffNameOnlyCount counts the number of changed files between two commits.
// Equivalent to: git diff --name-only hash1 hash2 | wc -l
// diffTreeMaxFiles mirrors the maxFiles cap used in merge preview's compareRepositoryTrees.
// Both APIs must report identical file counts for the same inputs.
const diffTreeMaxFiles = 10000

func nativeDiffNameOnlyCount(ctx context.Context, repoPath, hash1, hash2 string) (int, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "diff-tree", "-r", "--no-commit-id", "-M", hash1, hash2)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return 0, fmt.Errorf("failed to get diff-tree count: %s", strings.TrimSpace(stderr.String()))
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return 0, nil
	}
	count := len(strings.Split(raw, "\n"))
	if count > diffTreeMaxFiles {
		count = diffTreeMaxFiles
	}
	return count, nil
}

// nativeGetCommitFullMessage returns the full commit message (subject + body) for a commit.
func nativeGetCommitFullMessage(ctx context.Context, repoPath, hash string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "log", "-1", "--format=%B", hash)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return "", fmt.Errorf("failed to get commit message: %s", strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}
