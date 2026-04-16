package merge

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/gitutil"
)

// ---------------------------------------------------------------------------
// Native git command layer — replaces go-git in-process library
// ---------------------------------------------------------------------------
//
// Every git operation is delegated to the native git CLI, leveraging C
// optimizations, bloom filters, commit-graph, and other index structures
// that make native git 10-100x faster than go-git for large repositories.
//
// All functions accept a bare-repo path (repoPath) and operate via
// --git-dir to avoid requiring a working tree.
// ---------------------------------------------------------------------------

// NativeSignature mirrors object.Signature without the go-git dependency.
type NativeSignature struct {
	Name  string
	Email string
	When  time.Time
}

// NativeCommit is the native replacement for *object.Commit.
type NativeCommit struct {
	Hash         string
	TreeHash     string
	ParentHashes []string
	Author       NativeSignature
	Committer    NativeSignature
	Message      string
}

// NativeTreeEntry is the native replacement for object.TreeEntry.
type NativeTreeEntry struct {
	Mode string // e.g. "100644", "040000", "120000"
	Type string // "blob" or "tree"
	Hash string
	Path string // full path for -r, name-only for non-recursive
	Size int64  // -1 when unknown
}

// MergeTreeResult holds the output of `git merge-tree --write-tree`.
type MergeTreeResult struct {
	TreeHash     string
	HasConflicts bool
	Conflicts    []MergeConflictInfo
}

// MergeConflictInfo captures a single conflict from merge-tree output.
type MergeConflictInfo struct {
	Path    string
	Type    string // e.g. "content", "add/add", "modify/delete"
	Message string
}

// DiffEntry represents one file change from `git diff-tree -r`.
type DiffEntry struct {
	SrcMode string
	DstMode string
	SrcHash string
	DstHash string
	Status  string // "A", "M", "D", "R", "C", "T"
	Path    string
	NewPath string // only for R/C
}

// ---------------------------------------------------------------------------
// Low-level helpers
// ---------------------------------------------------------------------------

const defaultGitTimeout = 30 * time.Second

// runGit executes a git command and returns stdout. stderr is captured for
// error messages.
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

// runGitWithStdin executes a git command with stdin data.
func runGitWithStdin(ctx context.Context, repoPath string, stdin []byte, args ...string) ([]byte, error) {
	fullArgs := append([]string{"--git-dir", repoPath}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return nil, fmt.Errorf("git %s failed: %s: %w", args[0], strings.TrimSpace(stderr.String()), err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Reference resolution
// ---------------------------------------------------------------------------

// gitRevParse resolves a ref (branch name, tag, HEAD, etc.) to a full SHA.
func gitRevParse(ctx context.Context, repoPath, ref string) (string, error) {
	if ref == "" {
		ref = "HEAD"
	}
	out, err := runGit(ctx, repoPath, "rev-parse", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitRevParseVerify resolves a ref and verifies it exists. Returns empty
// string + nil if the ref doesn't exist.
func gitRevParseVerify(ctx context.Context, repoPath, ref string) (string, error) {
	out, err := runGit(ctx, repoPath, "rev-parse", "--verify", "--quiet", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ---------------------------------------------------------------------------
// Merge base
// ---------------------------------------------------------------------------

// gitMergeBase finds the best common ancestor of two commits.
func gitMergeBase(ctx context.Context, repoPath, commitA, commitB string) (string, error) {
	out, err := runGit(ctx, repoPath, "merge-base", commitA, commitB)
	if err != nil {
		return "", fmt.Errorf("merge-base %s %s: %w", commitA, commitB, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// gitMergeBaseIsAncestor checks if commitA is an ancestor of commitB.
func gitMergeBaseIsAncestor(ctx context.Context, repoPath, commitA, commitB string) (bool, error) {
	fullArgs := []string{"--git-dir", repoPath, "merge-base", "--is-ancestor", commitA, commitB}
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// ---------------------------------------------------------------------------
// Merge tree (Git 2.38+)
// ---------------------------------------------------------------------------

// gitMergeTree performs a 3-way merge using `git merge-tree --write-tree`.
// This replaces the entire 821-line apply_tree.go implementation.
//
// Git 2.38+ merge-tree writes the merged tree directly to the object store
// and reports conflicts on stdout without requiring a working directory.
func gitMergeTree(ctx context.Context, repoPath, base, ours, theirs string) (*MergeTreeResult, error) {
	args := []string{"--git-dir", repoPath, "merge-tree", "--write-tree", "--no-messages", ours, theirs}
	if base != "" {
		// With explicit merge base: merge-tree --write-tree --merge-base=<base> <ours> <theirs>
		args = []string{"--git-dir", repoPath, "merge-tree", "--write-tree", "--merge-base=" + base, ours, theirs}
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	output := strings.TrimSpace(stdout.String())

	if err != nil {
		// Exit code 1 means conflicts detected but tree was still written
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return parseMergeTreeOutput(output, true)
		}
		return nil, fmt.Errorf("git merge-tree failed: %s: %w", strings.TrimSpace(stderr.String()), err)
	}

	return parseMergeTreeOutput(output, false)
}

// parseMergeTreeOutput parses the output of `git merge-tree --write-tree`.
// Format:
//
//	<tree-hash>
//	<informational messages about conflicts>
func parseMergeTreeOutput(output string, hasConflicts bool) (*MergeTreeResult, error) {
	lines := strings.SplitN(output, "\n", 2)
	if len(lines) == 0 || lines[0] == "" {
		return nil, fmt.Errorf("empty merge-tree output")
	}

	result := &MergeTreeResult{
		TreeHash:     lines[0],
		HasConflicts: hasConflicts,
	}

	// Parse conflict info if present
	if hasConflicts && len(lines) > 1 {
		result.Conflicts = parseMergeConflicts(lines[1])
	}

	return result, nil
}

// parseMergeConflicts parses conflict information lines from merge-tree output.
func parseMergeConflicts(conflictOutput string) []MergeConflictInfo {
	var conflicts []MergeConflictInfo
	scanner := bufio.NewScanner(strings.NewReader(conflictOutput))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		conflict := MergeConflictInfo{Message: line}
		// Try to extract path and type from structured output
		// Common format: "CONFLICT (content): Merge conflict in <path>"
		// or "Auto-merging <path>"
		if strings.HasPrefix(line, "CONFLICT") {
			conflict.Type = extractConflictType(line)
			conflict.Path = extractConflictPath(line)
		}
		conflicts = append(conflicts, conflict)
	}
	return conflicts
}

func extractConflictType(line string) string {
	// "CONFLICT (content): ..." → "content"
	start := strings.Index(line, "(")
	end := strings.Index(line, ")")
	if start >= 0 && end > start {
		return line[start+1 : end]
	}
	return "unknown"
}

func extractConflictPath(line string) string {
	// "CONFLICT (content): Merge conflict in path/to/file" → "path/to/file"
	idx := strings.Index(line, "Merge conflict in ")
	if idx >= 0 {
		return strings.TrimSpace(line[idx+len("Merge conflict in "):])
	}
	// "CONFLICT (modify/delete): path/to/file deleted in ..." → "path/to/file"
	parts := strings.SplitN(line, ": ", 2)
	if len(parts) == 2 {
		rest := parts[1]
		spIdx := strings.IndexByte(rest, ' ')
		if spIdx > 0 {
			return rest[:spIdx]
		}
		return rest
	}
	return ""
}

// ---------------------------------------------------------------------------
// Commit creation
// ---------------------------------------------------------------------------

// gitCommitTree creates a new commit object from a tree hash, parent hashes,
// author/committer info, and message. Returns the new commit's SHA.
func gitCommitTree(ctx context.Context, repoPath string, treeHash string, parentHashes []string, author, committer NativeSignature, message string) (string, error) {
	args := []string{"--git-dir", repoPath, "commit-tree", treeHash}
	for _, p := range parentHashes {
		args = append(args, "-p", p)
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stdin = strings.NewReader(message)

	// Set author/committer via environment
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME="+author.Name,
		"GIT_AUTHOR_EMAIL="+author.Email,
		"GIT_AUTHOR_DATE="+author.When.Format(time.RFC3339),
		"GIT_COMMITTER_NAME="+committer.Name,
		"GIT_COMMITTER_EMAIL="+committer.Email,
		"GIT_COMMITTER_DATE="+committer.When.Format(time.RFC3339),
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return "", fmt.Errorf("git commit-tree failed: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ---------------------------------------------------------------------------
// Reference updates
// ---------------------------------------------------------------------------

// gitUpdateRef atomically updates a reference to point to a new hash.
func gitUpdateRef(ctx context.Context, repoPath, refName, newHash string) error {
	_, err := runGit(ctx, repoPath, "update-ref", refName, newHash)
	return err
}

// gitUpdateRefCAS performs a compare-and-swap reference update. The ref is
// only updated if its current value matches oldHash. Pass empty oldHash to
// require creation of a new ref.
func gitUpdateRefCAS(ctx context.Context, repoPath, refName, newHash, oldHash string) error {
	_, err := runGit(ctx, repoPath, "update-ref", refName, newHash, oldHash)
	return err
}

// ---------------------------------------------------------------------------
// Object reading
// ---------------------------------------------------------------------------

// gitCatFileCommit reads a commit object and returns a NativeCommit.
func gitCatFileCommit(ctx context.Context, repoPath, commitHash string) (*NativeCommit, error) {
	out, err := runGit(ctx, repoPath, "cat-file", "commit", commitHash)
	if err != nil {
		return nil, err
	}
	return parseCommitObject(commitHash, out)
}

// parseCommitObject parses raw `git cat-file commit` output.
func parseCommitObject(hash string, data []byte) (*NativeCommit, error) {
	commit := &NativeCommit{Hash: hash}
	parts := bytes.SplitN(data, []byte("\n\n"), 2)
	if len(parts) == 2 {
		commit.Message = string(parts[1])
	}

	scanner := bufio.NewScanner(bytes.NewReader(parts[0]))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		switch key {
		case "tree":
			commit.TreeHash = value
		case "parent":
			commit.ParentHashes = append(commit.ParentHashes, value)
		case "author":
			commit.Author = parseSignature(value)
		case "committer":
			commit.Committer = parseSignature(value)
		}
	}
	return commit, nil
}

// parseSignature parses "Name <email> timestamp timezone" into NativeSignature.
func parseSignature(raw string) NativeSignature {
	sig := NativeSignature{}
	// Format: "Name <email> 1234567890 +0000"
	ltIdx := strings.LastIndex(raw, "<")
	gtIdx := strings.LastIndex(raw, ">")
	if ltIdx < 0 || gtIdx < 0 || gtIdx <= ltIdx {
		sig.Name = raw
		return sig
	}

	sig.Name = strings.TrimSpace(raw[:ltIdx])
	sig.Email = raw[ltIdx+1 : gtIdx]

	// Parse timestamp after ">"
	rest := strings.TrimSpace(raw[gtIdx+1:])
	fields := strings.Fields(rest)
	if len(fields) >= 1 {
		ts, err := strconv.ParseInt(fields[0], 10, 64)
		if err == nil {
			sig.When = time.Unix(ts, 0)
			// Apply timezone if available
			if len(fields) >= 2 {
				loc := parseTZOffset(fields[1])
				sig.When = sig.When.In(loc)
			}
		}
	}
	return sig
}

// parseTZOffset parses "+0530" or "-0800" style timezone offset.
func parseTZOffset(tz string) *time.Location {
	if len(tz) != 5 || (tz[0] != '+' && tz[0] != '-') {
		return time.UTC
	}
	hours, err1 := strconv.Atoi(tz[1:3])
	minutes, err2 := strconv.Atoi(tz[3:5])
	if err1 != nil || err2 != nil {
		return time.UTC
	}
	offset := hours*3600 + minutes*60
	if tz[0] == '-' {
		offset = -offset
	}
	return time.FixedZone(tz, offset)
}

// ---------------------------------------------------------------------------
// Tree listing
// ---------------------------------------------------------------------------

// gitLsTree lists tree entries. If recursive is true, lists all blobs
// recursively. Returns entries in git's output order.
func gitLsTree(ctx context.Context, repoPath, treeish string, recursive bool) ([]NativeTreeEntry, error) {
	args := []string{"ls-tree", "-l"}
	if recursive {
		args = append(args, "-r")
	}
	args = append(args, treeish)

	out, err := runGit(ctx, repoPath, args...)
	if err != nil {
		return nil, err
	}

	return parseLsTreeOutput(out)
}

// parseLsTreeOutput parses `git ls-tree -l` output.
// Format: "<mode> <type> <hash> <size>\t<path>"
func parseLsTreeOutput(data []byte) ([]NativeTreeEntry, error) {
	var entries []NativeTreeEntry
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		entry, err := parseSingleLsTreeLine(line)
		if err != nil {
			continue // skip malformed lines
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// parseSingleLsTreeLine parses a single ls-tree output line.
func parseSingleLsTreeLine(line string) (NativeTreeEntry, error) {
	// "<mode> <type> <hash> <size>\t<path>"
	tabIdx := strings.IndexByte(line, '\t')
	if tabIdx < 0 {
		return NativeTreeEntry{}, fmt.Errorf("no tab in ls-tree line")
	}
	meta := line[:tabIdx]
	path := line[tabIdx+1:]

	fields := strings.Fields(meta)
	if len(fields) < 4 {
		return NativeTreeEntry{}, fmt.Errorf("insufficient fields in ls-tree meta")
	}

	var size int64 = -1
	if fields[3] != "-" {
		s, err := strconv.ParseInt(fields[3], 10, 64)
		if err == nil {
			size = s
		}
	}

	return NativeTreeEntry{
		Mode: fields[0],
		Type: fields[1],
		Hash: fields[2],
		Size: size,
		Path: path,
	}, nil
}

// ---------------------------------------------------------------------------
// Object creation
// ---------------------------------------------------------------------------

// gitHashObject writes content to the object store and returns its blob hash.
func gitHashObject(ctx context.Context, repoPath string, content []byte) (string, error) {
	out, err := runGitWithStdin(ctx, repoPath, content, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitMktree creates a tree object from ls-tree formatted input.
// Input format: "<mode> <type> <hash>\t<name>\n" per entry.
func gitMktree(ctx context.Context, repoPath string, entries []NativeTreeEntry) (string, error) {
	var buf bytes.Buffer
	for _, e := range entries {
		fmt.Fprintf(&buf, "%s %s %s\t%s\n", e.Mode, e.Type, e.Hash, e.Path)
	}
	out, err := runGitWithStdin(ctx, repoPath, buf.Bytes(), "mktree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ---------------------------------------------------------------------------
// Commit history
// ---------------------------------------------------------------------------

// gitLogFormat used for structured commit parsing via --format.
const nativeGitLogFormat = "%H%x00%T%x00%P%x00%an%x00%ae%x00%aI%x00%cn%x00%ce%x00%cI%x00%B%x00"

// gitRevList returns commit hashes between two refs (from..to).
func gitRevList(ctx context.Context, repoPath, from, to string) ([]string, error) {
	rangeSpec := from + ".." + to
	out, err := runGit(ctx, repoPath, "rev-list", rangeSpec)
	if err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, nil
	}
	return strings.Split(raw, "\n"), nil
}

// gitRevListReverse returns commit hashes between two refs in chronological order.
func gitRevListReverse(ctx context.Context, repoPath, from, to string) ([]string, error) {
	rangeSpec := from + ".." + to
	out, err := runGit(ctx, repoPath, "rev-list", "--reverse", rangeSpec)
	if err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, nil
	}
	return strings.Split(raw, "\n"), nil
}

// gitRevListCount returns the number of commits in a range without listing them.
func gitRevListCount(ctx context.Context, repoPath, from, to string) (int, error) {
	rangeSpec := from + ".." + to
	out, err := runGit(ctx, repoPath, "rev-list", "--count", rangeSpec)
	if err != nil {
		return 0, err
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("parse rev-list count: %w", err)
	}
	return count, nil
}

// gitLogRange returns parsed NativeCommit objects for a range (from..to) with pagination.
func gitLogRange(ctx context.Context, repoPath, from, to string, limit, skip int) ([]*NativeCommit, error) {
	args := []string{"log", "--format=" + nativeGitLogFormat}
	if limit > 0 {
		args = append(args, fmt.Sprintf("--max-count=%d", limit))
	}
	if skip > 0 {
		args = append(args, fmt.Sprintf("--skip=%d", skip))
	}
	args = append(args, from+".."+to)

	out, err := runGit(ctx, repoPath, args...)
	if err != nil {
		return nil, err
	}
	return parseLogOutput(out)
}

// gitLog returns parsed NativeCommit objects for a ref, limited to n commits.
func gitLog(ctx context.Context, repoPath, ref string, n int) ([]*NativeCommit, error) {
	args := []string{"log", fmt.Sprintf("-%d", n), "--format=" + nativeGitLogFormat, ref}
	out, err := runGit(ctx, repoPath, args...)
	if err != nil {
		return nil, err
	}

	return parseLogOutput(out)
}

// parseLogOutput parses git log output with nativeGitLogFormat.
func parseLogOutput(data []byte) ([]*NativeCommit, error) {
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return nil, nil
	}

	// Each record ends with \x00, split by record separator
	records := strings.Split(raw, "\x00")
	var commits []*NativeCommit

	for i := 0; i+9 <= len(records); i += 10 {
		sha := strings.TrimSpace(records[i])
		if sha == "" {
			continue
		}
		treeHash := records[i+1]
		parentRaw := records[i+2]
		authorName := records[i+3]
		authorEmail := records[i+4]
		authorDateStr := records[i+5]
		committerName := records[i+6]
		committerEmail := records[i+7]
		committerDateStr := records[i+8]
		message := records[i+9]

		authorDate, _ := time.Parse(time.RFC3339, authorDateStr)
		committerDate, _ := time.Parse(time.RFC3339, committerDateStr)

		var parentHashes []string
		if parentRaw != "" {
			parentHashes = strings.Split(parentRaw, " ")
		}

		commits = append(commits, &NativeCommit{
			Hash:         sha,
			TreeHash:     treeHash,
			ParentHashes: parentHashes,
			Author: NativeSignature{
				Name:  authorName,
				Email: authorEmail,
				When:  authorDate,
			},
			Committer: NativeSignature{
				Name:  committerName,
				Email: committerEmail,
				When:  committerDate,
			},
			Message: strings.TrimSpace(message),
		})
	}
	return commits, nil
}

// ---------------------------------------------------------------------------
// Diff / tree comparison
// ---------------------------------------------------------------------------

// gitDiffTree compares two trees and returns the list of changed files.
func gitDiffTree(ctx context.Context, repoPath, treeA, treeB string) ([]DiffEntry, error) {
	out, err := runGit(ctx, repoPath, "diff-tree", "-r", "--no-commit-id", "-M", treeA, treeB)
	if err != nil {
		return nil, err
	}
	return parseDiffTreeOutput(out)
}

// parseDiffTreeOutput parses `git diff-tree -r -M` output.
// Format: ":<old-mode> <new-mode> <old-hash> <new-hash> <status>\t<path>[\t<new-path>]"
func parseDiffTreeOutput(data []byte) ([]DiffEntry, error) {
	var entries []DiffEntry
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		entry, err := parseSingleDiffTreeLine(line)
		if err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func parseSingleDiffTreeLine(line string) (DiffEntry, error) {
	// ":<old-mode> <new-mode> <old-hash> <new-hash> <status>\t<path>"
	if !strings.HasPrefix(line, ":") {
		return DiffEntry{}, fmt.Errorf("line does not start with ':'")
	}
	line = line[1:] // strip leading ':'

	tabIdx := strings.IndexByte(line, '\t')
	if tabIdx < 0 {
		return DiffEntry{}, fmt.Errorf("no tab in diff-tree line")
	}

	meta := line[:tabIdx]
	paths := line[tabIdx+1:]

	fields := strings.Fields(meta)
	if len(fields) < 5 {
		return DiffEntry{}, fmt.Errorf("insufficient fields in diff-tree meta")
	}

	entry := DiffEntry{
		SrcMode: fields[0],
		DstMode: fields[1],
		SrcHash: fields[2],
		DstHash: fields[3],
		Status:  fields[4][:1], // Take first char (R100 → R)
	}

	// Handle rename/copy with two paths
	pathParts := strings.SplitN(paths, "\t", 2)
	entry.Path = pathParts[0]
	if len(pathParts) == 2 {
		entry.NewPath = pathParts[1]
	}

	return entry, nil
}

// ---------------------------------------------------------------------------
// File content reading
// ---------------------------------------------------------------------------

// gitShowFile reads a file's content at a specific ref/treeish.
func gitShowFile(ctx context.Context, repoPath, ref, path string) ([]byte, error) {
	spec := ref + ":" + path
	return runGit(ctx, repoPath, "show", spec)
}

// gitCatFileBlob reads a blob object by its hash.
func gitCatFileBlob(ctx context.Context, repoPath, hash string) ([]byte, error) {
	return runGit(ctx, repoPath, "cat-file", "blob", hash)
}

// gitCatFileBlobSize returns the size of a blob without reading its content.
func gitCatFileBlobSize(ctx context.Context, repoPath, hash string) (int64, error) {
	out, err := runGit(ctx, repoPath, "cat-file", "-s", hash)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
}

// ---------------------------------------------------------------------------
// Batch cat-file for efficient multi-object reads
// ---------------------------------------------------------------------------

// BatchCatFile provides a persistent git cat-file --batch process for
// reading multiple objects efficiently without per-object process overhead.
type BatchCatFile struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
}

// NewBatchCatFile starts a git cat-file --batch process.
func NewBatchCatFile(ctx context.Context, repoPath string) (*BatchCatFile, error) {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "cat-file", "--batch")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("start cat-file: %w", err)
	}
	return &BatchCatFile{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReaderSize(stdout, 64*1024),
	}, nil
}

// ReadBlob sends a hash to the cat-file process and reads back the blob
// content. Limits reads to maxBytes (pass 0 for no limit).
func (b *BatchCatFile) ReadBlob(hash string, maxBytes int) ([]byte, error) {
	if _, err := fmt.Fprintf(b.stdin, "%s\n", hash); err != nil {
		return nil, fmt.Errorf("write hash: %w", err)
	}

	// Read header: "<hash> <type> <size>\n"
	header, err := b.stdout.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	header = strings.TrimSpace(header)

	// Check for missing object
	if strings.HasSuffix(header, " missing") {
		return nil, fmt.Errorf("object %s not found", hash)
	}

	parts := strings.Fields(header)
	if len(parts) < 3 {
		return nil, fmt.Errorf("malformed cat-file header: %s", header)
	}

	size, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse size: %w", err)
	}

	// Read content
	readSize := int(size)
	if maxBytes > 0 && readSize > maxBytes {
		readSize = maxBytes
	}

	content := make([]byte, readSize)
	if _, err := io.ReadFull(b.stdout, content); err != nil {
		return nil, fmt.Errorf("read content: %w", err)
	}

	// Discard remaining bytes + trailing newline
	remaining := size - int64(readSize) + 1 // +1 for trailing \n
	if remaining > 0 {
		if _, err := io.CopyN(io.Discard, b.stdout, remaining); err != nil {
			return nil, fmt.Errorf("discard remaining: %w", err)
		}
	}

	return content, nil
}

// Close terminates the cat-file process.
func (b *BatchCatFile) Close() error {
	b.stdin.Close()
	return b.cmd.Wait()
}

// ---------------------------------------------------------------------------
// Conversion helpers: NativeCommit ↔ CommitInfo
// ---------------------------------------------------------------------------

// nativeCommitToInfo converts a NativeCommit to the API-facing CommitInfo.
func nativeCommitToInfo(c *NativeCommit) *CommitInfo {
	if c == nil {
		return nil
	}
	return &CommitInfo{
		SHA:     c.Hash,
		Message: c.Message,
		Author: AuthorInfo{
			Name:  c.Author.Name,
			Email: c.Author.Email,
			Date:  c.Author.When,
		},
		Committer: AuthorInfo{
			Name:  c.Committer.Name,
			Email: c.Committer.Email,
			Date:  c.Committer.When,
		},
		CreatedAt: c.Committer.When.Format(time.RFC3339),
		TreeSHA:   c.TreeHash,
	}
}

// toNativeSignature converts AuthorInfo to NativeSignature.
func toNativeSignature(a *AuthorInfo) NativeSignature {
	if a == nil {
		return NativeSignature{}
	}
	return NativeSignature{
		Name:  a.Name,
		Email: a.Email,
		When:  a.Date,
	}
}
