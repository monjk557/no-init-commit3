package revert

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/gitutil"
)

// BranchExistsError is returned when a revert branch already exists.
// It carries the existing branch name and tip commit info so that
// callers (handler) can include them in the HTTP response.
type BranchExistsError struct {
	BranchName string
	TipCommit  *CommitInfo
}

func (e *BranchExistsError) Error() string {
	return fmt.Sprintf("revert branch '%s' already exists for this commit", e.BranchName)
}

// maxSparseCheckoutFiles is the threshold for using sparse-checkout.
// If a commit touches more files than this, fall back to full checkout
// to avoid diminishing returns and command-line length issues.
const maxSparseCheckoutFiles = 5000

const defaultGitTimeout = 30 * time.Second

// runGit executes a git command against a bare repo and returns stdout.
func runGit(ctx context.Context, repoPath string, args ...string) ([]byte, error) {
	fullArgs := append([]string{"--git-dir", repoPath}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return nil, fmt.Errorf("git %s failed: %s: %w", args[0], strings.TrimSpace(stderr.String()), err)
	}
	return out, nil
}

// nativeCommit holds the data we parse from git cat-file commit.
type nativeCommit struct {
	Hash         string
	TreeHash     string
	ParentHashes []string
	AuthorName   string
	AuthorEmail  string
	AuthorDate   time.Time
	CommitName   string
	CommitEmail  string
	CommitDate   time.Time
	Message      string
}

// toCommitInfo converts a nativeCommit to the API-facing CommitInfo.
func (c *nativeCommit) toCommitInfo() *CommitInfo {
	return &CommitInfo{
		SHA:     c.Hash,
		Message: c.Message,
		Author: AuthorInfo{
			Name:  c.AuthorName,
			Email: c.AuthorEmail,
			Date:  c.AuthorDate,
		},
		Committer: AuthorInfo{
			Name:  c.CommitName,
			Email: c.CommitEmail,
			Date:  c.CommitDate,
		},
		CreatedAt: c.CommitDate.Format(time.RFC3339),
		TreeSHA:   c.TreeHash,
	}
}

// catFileCommit reads and parses a commit object.
func catFileCommit(ctx context.Context, repoPath, hash string) (*nativeCommit, error) {
	out, err := runGit(ctx, repoPath, "cat-file", "commit", hash)
	if err != nil {
		return nil, err
	}
	return parseCommitObject(hash, out)
}

// parseCommitObject parses raw `git cat-file commit` output.
func parseCommitObject(hash string, data []byte) (*nativeCommit, error) {
	c := &nativeCommit{Hash: hash}
	parts := bytes.SplitN(data, []byte("\n\n"), 2)
	if len(parts) == 2 {
		c.Message = strings.TrimSpace(string(parts[1]))
	}

	for _, line := range strings.Split(string(parts[0]), "\n") {
		key, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		switch key {
		case "tree":
			c.TreeHash = value
		case "parent":
			c.ParentHashes = append(c.ParentHashes, value)
		case "author":
			c.AuthorName, c.AuthorEmail, c.AuthorDate = parseSignature(value)
		case "committer":
			c.CommitName, c.CommitEmail, c.CommitDate = parseSignature(value)
		}
	}
	return c, nil
}

// parseSignature parses "Name <email> timestamp timezone".
func parseSignature(raw string) (name, email string, when time.Time) {
	ltIdx := strings.LastIndex(raw, "<")
	gtIdx := strings.LastIndex(raw, ">")
	if ltIdx < 0 || gtIdx < 0 || gtIdx <= ltIdx {
		return raw, "", time.Time{}
	}
	name = strings.TrimSpace(raw[:ltIdx])
	email = raw[ltIdx+1 : gtIdx]

	rest := strings.TrimSpace(raw[gtIdx+1:])
	fields := strings.Fields(rest)
	if len(fields) >= 1 {
		var ts int64
		fmt.Sscanf(fields[0], "%d", &ts)
		when = time.Unix(ts, 0)
	}
	return
}

// performRevert executes the revert logic on a bare repository.
//
// Algorithm (mirrors `git revert -m <parent_number> <commit>`):
//  1. Resolve the commit to revert and the current target branch tip.
//  2. Identify the mainline parent (parent_number, default 1).
//  3. Compute the revert tree via 3-way merge:
//     base = commit's tree, ours = target tip tree, theirs = mainline parent tree.
//  4. Create a revert commit on a new branch.
func performRevert(ctx context.Context, repoPath string, req *RevertRequest) (*RevertResponse, error) {
	// 1. Resolve target branch tip
	targetHash, err := revParse(ctx, repoPath, req.TargetRef)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve target ref %q: %w", req.TargetRef, err)
	}

	// 2. Read the commit to revert
	commitToRevert, err := catFileCommit(ctx, repoPath, req.CommitSHA)
	if err != nil {
		return nil, fmt.Errorf("failed to read commit %s: %w", req.CommitSHA, err)
	}

	// 3. Validate and determine parent number
	parentNumber := req.ParentNumber
	if len(commitToRevert.ParentHashes) <= 1 {
		// Regular commit or root commit — parent_number is not used by git revert
		parentNumber = 0
	} else {
		// Merge commit — parent_number is required
		if parentNumber == 0 {
			parentNumber = 1
		}
		if parentNumber < 1 || parentNumber > len(commitToRevert.ParentHashes) {
			return nil, fmt.Errorf("invalid parent_number %d: commit %s has %d parents",
				parentNumber, req.CommitSHA, len(commitToRevert.ParentHashes))
		}
	}

	// 4. Compute revert tree via temporary worktree + git revert
	revertTreeHash, hasConflicts, err := revertViaWorktree(ctx, repoPath,
		req.CommitSHA, targetHash, parentNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to compute revert tree: %w", err)
	}
	if hasConflicts {
		return nil, fmt.Errorf("revert produces conflicts: cannot auto-revert commit %s", req.CommitSHA)
	}

	// 5. Generate revert commit message
	message := req.Message
	if message == "" {
		message = fmt.Sprintf("Revert \"%s\"\n\nThis reverts commit %s.",
			firstLine(commitToRevert.Message), commitToRevert.Hash)
	}

	// 6. Author / committer
	now := time.Now()
	author := resolveAuthor(req.Author, commitToRevert, now)
	committer := resolveCommitter(req.Committer, &author, now)

	// 7. Create the revert commit (parent = target tip)
	revertCommitHash, err := commitTree(ctx, repoPath, revertTreeHash,
		[]string{targetHash}, author, committer, message)
	if err != nil {
		return nil, fmt.Errorf("failed to create revert commit: %w", err)
	}

	// 8. Create the revert branch (fail if it already exists)
	branchName := req.BranchName
	if branchName == "" {
		branchName = "revert/" + shortSHA(req.CommitSHA)
	}
	refName := "refs/heads/" + branchName

	// Check if branch already exists — return existing branch info instead of a bare error
	if existingHash, err := revParse(ctx, repoPath, refName); err == nil && existingHash != "" {
		var tipInfo *CommitInfo
		if tip, err := catFileCommit(ctx, repoPath, existingHash); err == nil {
			tipInfo = tip.toCommitInfo()
		}
		return nil, &BranchExistsError{BranchName: branchName, TipCommit: tipInfo}
	}

	if err := updateRef(ctx, repoPath, refName, revertCommitHash); err != nil {
		return nil, fmt.Errorf("failed to create branch %s: %w", branchName, err)
	}

	// 9. Read back the revert commit
	revertCommit, err := catFileCommit(ctx, repoPath, revertCommitHash)
	if err != nil {
		return nil, fmt.Errorf("failed to read revert commit: %w", err)
	}

	return &RevertResponse{
		Success:        true,
		RevertBranch:   branchName,
		RevertCommit:   revertCommit.toCommitInfo(),
		RevertedCommit: commitToRevert.toCommitInfo(),
		Message:        fmt.Sprintf("Revert branch '%s' created successfully", branchName),
	}, nil
}

// ---------------------------------------------------------------------------
// Git plumbing helpers
// ---------------------------------------------------------------------------

func revParse(ctx context.Context, repoPath, ref string) (string, error) {
	out, err := runGit(ctx, repoPath, "rev-parse", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// revertViaWorktree performs a revert using a temporary git worktree.
// This uses git's own `revert` machinery, which handles all edge cases
// (rename detection, delete/modify, etc.) correctly across all git versions.
//
// Optimization: when the commit touches fewer than maxSparseCheckoutFiles,
// sparse-checkout is used so that only the affected files are materialised
// on disk. This dramatically reduces I/O for large repositories and shortens
// the time the caller holds RepoRWLock.
//
// Parameters:
//
//	commitHash   = the commit to revert
//	targetRef    = the branch/commit to base the worktree on (HEAD of revert)
//	parentNumber = for merge commits, which parent is mainline (1-based, 0 means auto)
//
// Returns (treeHash, hasConflicts, error).
func revertViaWorktree(ctx context.Context, repoPath, commitHash, targetRef string, parentNumber int) (string, bool, error) {
	// Prune stale worktree entries left by previous crashes / OOM kills.
	pruneCmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "worktree", "prune")
	pruneCmd.Run() // best-effort

	// Determine which files the commit touches so we can use sparse-checkout.
	affectedFiles := getAffectedFiles(ctx, repoPath, commitHash, parentNumber)
	useSparse := len(affectedFiles) > 0 && len(affectedFiles) <= maxSparseCheckoutFiles

	// Create a temporary directory for the worktree
	wtDir, err := os.MkdirTemp("", "revert-wt-*")
	if err != nil {
		return "", false, fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		rmCmd := exec.CommandContext(cleanupCtx, "git", "--git-dir", repoPath,
			"worktree", "remove", "--force", wtDir)
		rmCmd.Run() // best-effort
		os.RemoveAll(wtDir)
	}()

	// Remove the temp dir so git worktree add can create it
	os.RemoveAll(wtDir)

	if useSparse {
		err = createSparseWorktree(ctx, repoPath, wtDir, targetRef, affectedFiles)
	} else {
		err = createFullWorktree(ctx, repoPath, wtDir, targetRef)
	}
	if err != nil {
		return "", false, err
	}

	// Run git revert --no-commit in the worktree
	revertArgs := []string{"revert", "--no-commit"}
	if parentNumber > 0 {
		revertArgs = append(revertArgs, fmt.Sprintf("-m%d", parentNumber))
	}
	revertArgs = append(revertArgs, commitHash)
	revertCmd := exec.CommandContext(ctx, "git", revertArgs...)
	revertCmd.Dir = wtDir
	var revertStderr bytes.Buffer
	revertCmd.Stderr = &revertStderr
	if err := revertCmd.Run(); err != nil {
		stderrStr := strings.TrimSpace(revertStderr.String())
		if strings.Contains(stderrStr, "CONFLICT") || strings.Contains(stderrStr, "conflict") {
			return "", true, nil
		}
		return "", false, fmt.Errorf("git revert failed: %s: %w", stderrStr, err)
	}

	// Write the resulting tree from the index
	writeCmd := exec.CommandContext(ctx, "git", "write-tree")
	writeCmd.Dir = wtDir
	var writeStderr bytes.Buffer
	writeCmd.Stderr = &writeStderr
	treeOut, err := gitutil.OutputWithLimit(writeCmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return "", false, fmt.Errorf("git write-tree failed: %s: %w",
			strings.TrimSpace(writeStderr.String()), err)
	}

	return strings.TrimSpace(string(treeOut)), false, nil
}

// createFullWorktree creates a regular (full-checkout) worktree.
func createFullWorktree(ctx context.Context, repoPath, wtDir, targetRef string) error {
	addCmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath,
		"worktree", "add", wtDir, targetRef, "--detach")
	var stderr bytes.Buffer
	addCmd.Stderr = &stderr
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("git worktree add failed: %s: %w",
			strings.TrimSpace(stderr.String()), err)
	}
	return nil
}

// createSparseWorktree creates a worktree that only materialises the given
// files on disk. The full index is still populated (all entries present) so
// that `git write-tree` produces a complete tree.
//
// Sequence:
//  1. worktree add --no-checkout --detach
//  2. sparse-checkout init --no-cone
//  3. Write affected file paths to the sparse-checkout file
//  4. git checkout  (populates full index, writes only sparse files to disk)
func createSparseWorktree(ctx context.Context, repoPath, wtDir, targetRef string, files []string) error {
	// 1. Create worktree without checking out any files
	addCmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath,
		"worktree", "add", wtDir, targetRef, "--detach", "--no-checkout")
	var addStderr bytes.Buffer
	addCmd.Stderr = &addStderr
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("git worktree add --no-checkout failed: %s: %w",
			strings.TrimSpace(addStderr.String()), err)
	}

	// 2. Initialise sparse-checkout in no-cone mode (supports individual file paths)
	initCmd := exec.CommandContext(ctx, "git", "sparse-checkout", "init", "--no-cone")
	initCmd.Dir = wtDir
	var initStderr bytes.Buffer
	initCmd.Stderr = &initStderr
	if err := initCmd.Run(); err != nil {
		return fmt.Errorf("git sparse-checkout init failed: %s: %w",
			strings.TrimSpace(initStderr.String()), err)
	}

	// 3. Write patterns directly to sparse-checkout file.
	//    For linked worktrees the git dir is at <repoPath>/worktrees/<name>.
	//    We resolve it via `git rev-parse --git-dir` inside the worktree.
	sparseFile, err := resolveSparsePath(ctx, wtDir)
	if err != nil {
		return err
	}
	if err := writeSparsePatterns(sparseFile, files); err != nil {
		return err
	}

	// 4. Populate the index (all entries) and materialise only sparse files
	checkoutCmd := exec.CommandContext(ctx, "git", "checkout")
	checkoutCmd.Dir = wtDir
	var coStderr bytes.Buffer
	checkoutCmd.Stderr = &coStderr
	if err := checkoutCmd.Run(); err != nil {
		return fmt.Errorf("git checkout (sparse) failed: %s: %w",
			strings.TrimSpace(coStderr.String()), err)
	}

	return nil
}

// resolveSparsePath returns the absolute path to the sparse-checkout file
// for the given worktree directory.
func resolveSparsePath(ctx context.Context, wtDir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-dir")
	cmd.Dir = wtDir
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return "", fmt.Errorf("failed to resolve worktree git dir: %w", err)
	}
	gitDir := strings.TrimSpace(string(out))
	// git rev-parse --git-dir may return a relative path
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(wtDir, gitDir)
	}
	return filepath.Join(gitDir, "info", "sparse-checkout"), nil
}

// writeSparsePatterns writes no-cone sparse-checkout patterns to the given file.
// Each file path is prefixed with "/" so it matches from the repo root.
func writeSparsePatterns(path string, files []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create sparse-checkout dir: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create sparse-checkout file: %w", err)
	}
	defer f.Close()

	for _, p := range files {
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		fmt.Fprintln(f, p)
	}
	return nil
}

// getAffectedFiles returns the deduplicated list of file paths changed by
// a commit. For merge commits, diff-tree compares against the mainline parent.
// Rename detection is enabled (-M) so both old and new paths are included.
func getAffectedFiles(ctx context.Context, repoPath, commitHash string, parentNumber int) []string {
	var parentRef string
	if parentNumber > 0 {
		parentRef = fmt.Sprintf("%s^%d", commitHash, parentNumber)
	} else {
		parentRef = commitHash + "^"
	}

	out, err := runGit(ctx, repoPath, "diff-tree", "-r", "--no-commit-id", "-M", parentRef, commitHash)
	if err != nil {
		return nil // on error, caller will fall back to full checkout
	}

	seen := make(map[string]struct{})
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		paths := parseDiffTreePaths(line)
		for _, p := range paths {
			seen[p] = struct{}{}
		}
	}

	result := make([]string, 0, len(seen))
	for p := range seen {
		result = append(result, p)
	}
	return result
}

// parseDiffTreePaths extracts file paths from a single diff-tree -r -M line.
// Format: ":<old-mode> <new-mode> <old-hash> <new-hash> <status>\t<path>[\t<new-path>]"
func parseDiffTreePaths(line string) []string {
	tabIdx := strings.IndexByte(line, '\t')
	if tabIdx < 0 {
		return nil
	}
	pathPart := line[tabIdx+1:]
	parts := strings.SplitN(pathPart, "\t", 2)
	paths := []string{parts[0]}
	if len(parts) == 2 && parts[1] != "" {
		paths = append(paths, parts[1]) // rename target
	}
	return paths
}

// commitTree creates a commit object.
func commitTree(ctx context.Context, repoPath, treeHash string, parents []string, author, committer AuthorInfo, message string) (string, error) {
	args := []string{"--git-dir", repoPath, "commit-tree", treeHash}
	for _, p := range parents {
		args = append(args, "-p", p)
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stdin = strings.NewReader(message)
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME="+author.Name,
		"GIT_AUTHOR_EMAIL="+author.Email,
		"GIT_AUTHOR_DATE="+author.Date.Format(time.RFC3339),
		"GIT_COMMITTER_NAME="+committer.Name,
		"GIT_COMMITTER_EMAIL="+committer.Email,
		"GIT_COMMITTER_DATE="+committer.Date.Format(time.RFC3339),
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return "", fmt.Errorf("git commit-tree failed: %s: %w",
			strings.TrimSpace(stderr.String()), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func updateRef(ctx context.Context, repoPath, refName, hash string) error {
	_, err := runGit(ctx, repoPath, "update-ref", refName, hash)
	return err
}

// ---------------------------------------------------------------------------
// Utility helpers
// ---------------------------------------------------------------------------

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func resolveAuthor(override *AuthorInfo, original *nativeCommit, now time.Time) AuthorInfo {
	if override != nil {
		a := *override
		if a.Date.IsZero() {
			a.Date = now
		}
		return a
	}
	return AuthorInfo{
		Name:  original.CommitName,
		Email: original.CommitEmail,
		Date:  now,
	}
}

func resolveCommitter(override *AuthorInfo, fallback *AuthorInfo, now time.Time) AuthorInfo {
	if override != nil {
		c := *override
		if c.Date.IsZero() {
			c.Date = now
		}
		return c
	}
	if fallback != nil {
		c := *fallback
		c.Date = now
		return c
	}
	return AuthorInfo{Name: "System", Email: "system@collabops.io", Date: now}
}
