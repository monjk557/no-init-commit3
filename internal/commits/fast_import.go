package commits

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// fastImportTimeout is the maximum duration for the entire fast-import process.
const fastImportTimeout = 60 * time.Second

// FastImportCommitRequest contains all information needed to create a commit
// via a single git fast-import process, replacing the previous multi-process
// pipeline (hash-object × N + mktree × D + commit-tree + update-ref).
type FastImportCommitRequest struct {
	RepoPath     string
	Branch       string // e.g. "main" (without refs/heads/ prefix)
	BaseTreeHash string // empty for root commit
	Changes      []FileChange
	CommitInfo   CommitInfo
}

// FastImportCommitResult is the result of a fast-import commit.
type FastImportCommitResult struct {
	CommitHash string
	TreeHash   string
}

// ExecuteFastImportCommit creates a commit using a single git fast-import process.
//
// This replaces the previous multi-process approach:
//   - Before: hash-object×N + mktree×D + commit-tree + update-ref = 2+N+D processes
//   - After:  1 fast-import process handles everything
//
// The fast-import stream format:
//  1. For each blob (create/update/move with content): "blob" + "mark" + "data"
//  2. "commit refs/heads/<branch>" with author/committer/message
//  3. "from <parent>" if not root commit
//  4. File modifications: "M <mode> :<mark> <path>" for creates/updates
//  5. File deletions: "D <path>"
//  6. "done"
func ExecuteFastImportCommit(ctx context.Context, req *FastImportCommitRequest) (*FastImportCommitResult, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, fastImportTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", req.RepoPath,
		"fast-import", "--quiet", "--done")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("fast-import stdin pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("fast-import start: %w", err)
	}

	// Write the fast-import stream
	if err := writeFastImportStream(stdin, req); err != nil {
		stdin.Close()
		cmd.Wait()
		return nil, fmt.Errorf("fast-import stream: %w", err)
	}

	if err := stdin.Close(); err != nil {
		cmd.Wait()
		return nil, fmt.Errorf("fast-import stdin close: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		return nil, fmt.Errorf("fast-import failed: %s", errMsg)
	}

	// Read back the commit hash from the ref we just created/updated
	commitHash, err := nativeResolveRef(ctx, req.RepoPath, "refs/heads/"+req.Branch)
	if err != nil {
		return nil, fmt.Errorf("fast-import: ref not updated: %w", err)
	}

	treeHash, err := nativeGetTreeHash(ctx, req.RepoPath, commitHash)
	if err != nil {
		return nil, fmt.Errorf("fast-import: tree hash: %w", err)
	}

	return &FastImportCommitResult{
		CommitHash: commitHash,
		TreeHash:   treeHash,
	}, nil
}

// writeFastImportStream writes the complete fast-import protocol stream.
func writeFastImportStream(w io.WriteCloser, req *FastImportCommitRequest) error {
	// Phase 1: Write blob objects for all file contents, assign marks
	// Marks start at :1 for the first blob
	markCounter := 0
	type blobMark struct {
		mark int
		path string
		mode string
	}
	var blobMarks []blobMark
	var deletePaths []string
	// Track move operations: delete original, create at new path
	var moveDeletePaths []string

	for _, change := range req.Changes {
		switch change.Operation {
		case FileOperationCreate, FileOperationUpdate:
			markCounter++
			mark := markCounter
			if err := writeBlob(w, mark, change.Content); err != nil {
				return err
			}
			mode := "100644"
			if change.Mode != nil {
				mode = *change.Mode
			}
			blobMarks = append(blobMarks, blobMark{mark: mark, path: change.Path, mode: mode})

		case FileOperationDelete:
			deletePaths = append(deletePaths, change.Path)

		case FileOperationMove:
			moveDeletePaths = append(moveDeletePaths, change.OriginalPath)
			markCounter++
			mark := markCounter
			if len(change.Content) > 0 {
				// Move with new content
				if err := writeBlob(w, mark, change.Content); err != nil {
					return err
				}
			} else {
				// Move without content change — we need to read the original blob.
				// Use inline data with "M <mode> inline <path>" approach instead.
				// For moves without content, we use deleteoriginal + filecopy approach.
				// Since fast-import supports "C" (copy) and "R" (rename):
				markCounter-- // revert mark increment
			}
			mode := "100644"
			if change.Mode != nil {
				mode = *change.Mode
			}
			if len(change.Content) > 0 {
				blobMarks = append(blobMarks, blobMark{mark: mark, path: change.Path, mode: mode})
			}
		}
	}

	// Phase 2: Write commit command
	refName := "refs/heads/" + req.Branch
	if _, err := fmt.Fprintf(w, "commit %s\n", refName); err != nil {
		return err
	}

	// Author
	author := req.CommitInfo.Author
	authorTime := author.When.Unix()
	authorTZ := author.When.Format("-0700")
	if _, err := fmt.Fprintf(w, "author %s <%s> %d %s\n",
		author.Name, author.Email, authorTime, authorTZ); err != nil {
		return err
	}

	// Committer
	committer := author
	if req.CommitInfo.Committer != nil {
		committer = *req.CommitInfo.Committer
	}
	committerTime := committer.When.Unix()
	committerTZ := committer.When.Format("-0700")
	if _, err := fmt.Fprintf(w, "committer %s <%s> %d %s\n",
		committer.Name, committer.Email, committerTime, committerTZ); err != nil {
		return err
	}

	// Commit message
	msgBytes := []byte(req.CommitInfo.Message)
	if _, err := fmt.Fprintf(w, "data %d\n", len(msgBytes)); err != nil {
		return err
	}
	if _, err := w.Write(msgBytes); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "\n"); err != nil {
		return err
	}

	// Parent(s)
	if len(req.CommitInfo.Parents) > 0 {
		if _, err := fmt.Fprintf(w, "from %s\n", req.CommitInfo.Parents[0]); err != nil {
			return err
		}
		for _, p := range req.CommitInfo.Parents[1:] {
			if _, err := fmt.Fprintf(w, "merge %s\n", p); err != nil {
				return err
			}
		}
	}

	// Phase 3: File modifications
	// Moves without content change — use rename command
	for _, change := range req.Changes {
		if change.Operation == FileOperationMove && len(change.Content) == 0 {
			if _, err := fmt.Fprintf(w, "R %s %s\n",
				quoteFastImportPath(change.OriginalPath),
				quoteFastImportPath(change.Path)); err != nil {
				return err
			}
		}
	}

	// Delete original paths for moves WITH content (they get new blob at new path)
	for _, p := range moveDeletePaths {
		// Check if this move had content (handled via blobMarks) — need to delete original
		hasContent := false
		for _, change := range req.Changes {
			if change.Operation == FileOperationMove && change.OriginalPath == p && len(change.Content) > 0 {
				hasContent = true
				break
			}
		}
		if hasContent {
			if _, err := fmt.Fprintf(w, "D %s\n", quoteFastImportPath(p)); err != nil {
				return err
			}
		}
	}

	// Deletions
	for _, p := range deletePaths {
		if _, err := fmt.Fprintf(w, "D %s\n", quoteFastImportPath(p)); err != nil {
			return err
		}
	}

	// File modifications from blobs
	for _, bm := range blobMarks {
		if _, err := fmt.Fprintf(w, "M %s :%d %s\n",
			bm.mode, bm.mark, quoteFastImportPath(bm.path)); err != nil {
			return err
		}
	}

	// End of commit
	if _, err := fmt.Fprintf(w, "\n"); err != nil {
		return err
	}

	// Done
	if _, err := fmt.Fprintf(w, "done\n"); err != nil {
		return err
	}

	return nil
}

// writeBlob writes a blob object to the fast-import stream.
func writeBlob(w io.Writer, mark int, content []byte) error {
	if _, err := fmt.Fprintf(w, "blob\nmark :%d\ndata %d\n", mark, len(content)); err != nil {
		return err
	}
	if _, err := w.Write(content); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "\n"); err != nil {
		return err
	}
	return nil
}

// quoteFastImportPath quotes a path for fast-import if it contains special chars.
// Fast-import requires paths with spaces or quotes to be double-quoted.
func quoteFastImportPath(path string) string {
	if strings.ContainsAny(path, " \t\"\\") {
		escaped := strings.ReplaceAll(path, "\\", "\\\\")
		escaped = strings.ReplaceAll(escaped, "\"", "\\\"")
		return "\"" + escaped + "\""
	}
	return path
}

// PreValidateChanges validates file existence for all operations in a single
// git ls-tree call, replacing the per-file nativeLsTreePath calls.
// Returns nil if all validations pass, or an error describing the first failure.
func PreValidateChanges(ctx context.Context, repoPath, baseTreeHash string, changes []FileChange) error {
	if baseTreeHash == "" {
		// Root commit: only creates allowed
		for _, c := range changes {
			if c.Operation != FileOperationCreate {
				return fmt.Errorf("repository has no commits; only create operations are allowed for the first commit")
			}
		}
		return nil
	}

	// Load the full tree once — O(1) git process instead of O(N)
	entries, err := nativeLsTree(ctx, repoPath, baseTreeHash)
	if err != nil {
		return fmt.Errorf("failed to list base tree: %w", err)
	}

	existingFiles := make(map[string]bool, len(entries))
	for _, e := range entries {
		existingFiles[e.Path] = true
	}

	// Track batch state for sequential operations
	batchState := make(map[string]bool)
	fileExists := func(path string) bool {
		if state, tracked := batchState[path]; tracked {
			return state
		}
		return existingFiles[path]
	}

	for _, change := range changes {
		switch change.Operation {
		case FileOperationCreate:
			if fileExists(change.Path) {
				return fmt.Errorf("file already exists: %s", change.Path)
			}
			batchState[change.Path] = true

		case FileOperationUpdate:
			if !fileExists(change.Path) {
				return fmt.Errorf("file '%s' not found in repository", change.Path)
			}

		case FileOperationDelete:
			if !fileExists(change.Path) {
				return fmt.Errorf("file '%s' not found in repository", change.Path)
			}
			batchState[change.Path] = false

		case FileOperationMove:
			if change.OriginalPath == "" {
				return fmt.Errorf("original_path is required for move operations")
			}
			if !fileExists(change.OriginalPath) {
				return fmt.Errorf("file '%s' not found in repository", change.OriginalPath)
			}
			if fileExists(change.Path) {
				return fmt.Errorf("file already exists: %s", change.Path)
			}
			batchState[change.OriginalPath] = false
			batchState[change.Path] = true
		}
	}

	return nil
}
