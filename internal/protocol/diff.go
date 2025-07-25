// Package protocol implements Git diff REST API endpoints for the executor.
// This file handles diff operations that provide structured JSON responses.
package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/collabops/git-executor/internal/repository"
	"github.com/go-chi/chi/v5"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// DiffHandler handles Git diff REST API endpoints
type DiffHandler struct {
	pathConfig       *repository.PathConfig
	permissionClient permissions.PermissionClient
}

// DiffResponse represents the response structure for diff endpoints
type DiffResponse struct {
	Base      string       `json:"base"`
	Head      string       `json:"head"`
	Files     []FileDiff   `json:"files"`
	Stats     DiffStats    `json:"stats"`
	Commits   *CommitStats `json:"commits,omitempty"`    // For branch diffs
	MergeBase string       `json:"merge_base,omitempty"` // For branch diffs
}

// CommitStats represents commit count differences for branch comparisons
type CommitStats struct {
	Ahead  int `json:"ahead"`  // Commits in head that aren't in base
	Behind int `json:"behind"` // Commits in base that aren't in head
}

// FileDiff represents changes to a single file
type FileDiff struct {
	Path      string       `json:"path"`
	Status    string       `json:"status"` // "added", "deleted", "modified", "renamed"
	Additions int          `json:"additions"`
	Deletions int          `json:"deletions"`
	Changes   []LineChange `json:"changes,omitempty"`
	IsBinary  bool         `json:"is_binary"`
	OldPath   string       `json:"old_path,omitempty"` // For renames
}

// LineChange represents a single line change in a diff
type LineChange struct {
	Type    string `json:"type"`    // "add", "del", "context"
	LineNum int    `json:"line"`    // Line number in the file
	Content string `json:"content"` // Line content with diff prefix
}

// DiffStats represents overall statistics for a diff
type DiffStats struct {
	TotalAdditions int `json:"total_additions"`
	TotalDeletions int `json:"total_deletions"`
	FilesChanged   int `json:"files_changed"`
}

// NewDiffHandler creates a new diff handler
func NewDiffHandler(cfg *config.Config, permClient permissions.PermissionClient) (*DiffHandler, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	if permClient == nil {
		return nil, fmt.Errorf("permission client cannot be nil")
	}

	// Create path configuration
	pathConfig, err := repository.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	return &DiffHandler{
		pathConfig:       pathConfig,
		permissionClient: permClient,
	}, nil
}

// HandleCommitDiff handles GET /repos/:owner/:repo/diff/commits/:base/:head
func (h *DiffHandler) HandleCommitDiff(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract path parameters
	owner := chi.URLParam(r, "owner")
	repo := chi.URLParam(r, "repo")
	base := chi.URLParam(r, "base")
	head := chi.URLParam(r, "head")

	// Validate required parameters
	if owner == "" || repo == "" || base == "" || head == "" {
		http.Error(w, "Missing required parameters: owner, repo, base, or head", http.StatusBadRequest)
		return
	}

	// Get user ID from header for permission checking
	userID := r.Header.Get("X-USER-ID")
	if userID == "" {
		http.Error(w, "Missing user authentication", http.StatusUnauthorized)
		return
	}

	// Check user permissions (read access for diff operations)
	hasAccess, err := h.permissionClient.CheckReadAccess(ctx, userID, owner, repo)
	if err != nil {
		http.Error(w, fmt.Sprintf("Permission check failed: %v", err), http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		http.Error(w, "Access denied to repository", http.StatusForbidden)
		return
	}

	// Get repository path and validate
	repoPath, err := repository.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid repository path: %v", err), http.StatusBadRequest)
		return
	}

	// Ensure repository exists
	if err := repository.EnsureRepoExists(h.pathConfig, repoPath); err != nil {
		http.Error(w, "Repository not found or inaccessible", http.StatusNotFound)
		return
	}

	// Parse query parameters for context lines (default: 3)
	contextLines := 3
	if ctxStr := r.URL.Query().Get("context"); ctxStr != "" {
		if parsed, err := strconv.Atoi(ctxStr); err == nil && parsed >= 0 {
			contextLines = parsed
		}
	}

	// Calculate diff between commits
	diffResp, err := h.calculateCommitDiff(ctx, repoPath, base, head, contextLines)
	if err != nil {
		// Check if it's a commit not found error
		if isCommitNotFoundError(err) {
			http.Error(w, fmt.Sprintf("Commit not found: %v", err), http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("Failed to calculate diff: %v", err), http.StatusInternalServerError)
		return
	}

	// Set response headers
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	// Encode and send response
	if err := json.NewEncoder(w).Encode(diffResp); err != nil {
		// Log error but can't change response at this point
		fmt.Printf("Failed to encode diff response: %v\n", err)
	}
}

// HandleBranchDiff handles GET /repos/:owner/:repo/diff/branches/:base/:head
func (h *DiffHandler) HandleBranchDiff(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract path parameters
	owner := chi.URLParam(r, "owner")
	repo := chi.URLParam(r, "repo")
	baseBranch := chi.URLParam(r, "base")
	headBranch := chi.URLParam(r, "head")

	// Validate required parameters
	if owner == "" || repo == "" || baseBranch == "" || headBranch == "" {
		http.Error(w, "Missing required parameters: owner, repo, base, or head", http.StatusBadRequest)
		return
	}

	// Get user ID from header for permission checking
	userID := r.Header.Get("X-USER-ID")
	if userID == "" {
		http.Error(w, "Missing user authentication", http.StatusUnauthorized)
		return
	}

	// Check user permissions (read access for diff operations)
	hasAccess, err := h.permissionClient.CheckReadAccess(ctx, userID, owner, repo)
	if err != nil {
		http.Error(w, fmt.Sprintf("Permission check failed: %v", err), http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		http.Error(w, "Access denied to repository", http.StatusForbidden)
		return
	}

	// Get repository path and validate
	repoPath, err := repository.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid repository path: %v", err), http.StatusBadRequest)
		return
	}

	// Ensure repository exists
	if err := repository.EnsureRepoExists(h.pathConfig, repoPath); err != nil {
		http.Error(w, "Repository not found or inaccessible", http.StatusNotFound)
		return
	}

	// Parse query parameters for context lines (default: 3)
	contextLines := 3
	if ctxStr := r.URL.Query().Get("context"); ctxStr != "" {
		if parsed, err := strconv.Atoi(ctxStr); err == nil && parsed >= 0 {
			contextLines = parsed
		}
	}

	// Calculate diff between branches
	diffResp, err := h.calculateBranchDiff(ctx, repoPath, baseBranch, headBranch, contextLines)
	if err != nil {
		// Check if it's a branch not found error
		if isBranchNotFoundError(err) {
			http.Error(w, fmt.Sprintf("Branch not found: %v", err), http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("Failed to calculate branch diff: %v", err), http.StatusInternalServerError)
		return
	}

	// Set response headers
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	// Encode and send response
	if err := json.NewEncoder(w).Encode(diffResp); err != nil {
		// Log error but can't change response at this point
		fmt.Printf("Failed to encode branch diff response: %v\n", err)
	}
}

// HandleFileDiff handles GET /repos/:owner/:repo/diff/file?path=:path&base=:base&head=:head
func (h *DiffHandler) HandleFileDiff(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract path parameters
	owner := chi.URLParam(r, "owner")
	repo := chi.URLParam(r, "repo")

	// Extract query parameters
	filePath := r.URL.Query().Get("path")
	base := r.URL.Query().Get("base")
	head := r.URL.Query().Get("head")

	// Validate required parameters
	if owner == "" || repo == "" {
		http.Error(w, "Missing required path parameters: owner or repo", http.StatusBadRequest)
		return
	}

	if filePath == "" || base == "" || head == "" {
		http.Error(w, "Missing required query parameters: path, base, or head", http.StatusBadRequest)
		return
	}

	// Get user ID from header for permission checking
	userID := r.Header.Get("X-USER-ID")
	if userID == "" {
		http.Error(w, "Missing user authentication", http.StatusUnauthorized)
		return
	}

	// Check user permissions (read access for diff operations)
	hasAccess, err := h.permissionClient.CheckReadAccess(ctx, userID, owner, repo)
	if err != nil {
		http.Error(w, fmt.Sprintf("Permission check failed: %v", err), http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		http.Error(w, "Access denied to repository", http.StatusForbidden)
		return
	}

	// Get repository path and validate
	repoPath, err := repository.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid repository path: %v", err), http.StatusBadRequest)
		return
	}

	// Ensure repository exists
	if err := repository.EnsureRepoExists(h.pathConfig, repoPath); err != nil {
		http.Error(w, "Repository not found or inaccessible", http.StatusNotFound)
		return
	}

	// Parse query parameters for context lines (default: 3)
	contextLines := 3
	if ctxStr := r.URL.Query().Get("context"); ctxStr != "" {
		if parsed, err := strconv.Atoi(ctxStr); err == nil && parsed >= 0 {
			contextLines = parsed
		}
	}

	// Parse diff format (default: unified)
	diffFormat := r.URL.Query().Get("format")
	if diffFormat == "" {
		diffFormat = "unified"
	}
	if diffFormat != "unified" && diffFormat != "split" {
		http.Error(w, "Invalid format parameter: must be 'unified' or 'split'", http.StatusBadRequest)
		return
	}

	// Calculate diff for the specific file
	diffResp, err := h.calculateFileDiff(ctx, repoPath, filePath, base, head, contextLines, diffFormat)
	if err != nil {
		// Check if it's a file/reference not found error
		if isFileNotFoundError(err) || isCommitNotFoundError(err) {
			http.Error(w, fmt.Sprintf("File or reference not found: %v", err), http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("Failed to calculate file diff: %v", err), http.StatusInternalServerError)
		return
	}

	// Set response headers
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	// Encode and send response
	if err := json.NewEncoder(w).Encode(diffResp); err != nil {
		// Log error but can't change response at this point
		fmt.Printf("Failed to encode file diff response: %v\n", err)
	}
}

// HandlePatch handles GET /repos/:owner/:repo/patch/:base/:head
func (h *DiffHandler) HandlePatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract path parameters
	owner := chi.URLParam(r, "owner")
	repo := chi.URLParam(r, "repo")
	base := chi.URLParam(r, "base")
	head := chi.URLParam(r, "head")

	// Validate required parameters
	if owner == "" || repo == "" || base == "" || head == "" {
		http.Error(w, "Missing required parameters: owner, repo, base, or head", http.StatusBadRequest)
		return
	}

	// Get user ID from header for permission checking
	userID := r.Header.Get("X-USER-ID")
	if userID == "" {
		http.Error(w, "Missing user authentication", http.StatusUnauthorized)
		return
	}

	// Check user permissions (read access for patch operations)
	hasAccess, err := h.permissionClient.CheckReadAccess(ctx, userID, owner, repo)
	if err != nil {
		http.Error(w, fmt.Sprintf("Permission check failed: %v", err), http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		http.Error(w, "Access denied to repository", http.StatusForbidden)
		return
	}

	// Get repository path and validate
	repoPath, err := repository.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid repository path: %v", err), http.StatusBadRequest)
		return
	}

	// Generate patch content
	patchContent, err := h.generatePatch(ctx, repoPath, base, head)
	if err != nil {
		if isCommitNotFoundError(err) {
			http.Error(w, fmt.Sprintf("Commit not found: %v", err), http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("Failed to generate patch: %v", err), http.StatusInternalServerError)
		return
	}

	// Set appropriate headers for patch content
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s-%s.patch\"", base, head))

	// Write patch content
	w.WriteHeader(http.StatusOK)
	_, err = w.Write([]byte(patchContent))
	if err != nil {
		// Log error but can't send HTTP error at this point
		fmt.Printf("Error writing patch response: %v\n", err)
	}
}

// generatePatch creates Git patch format output for commit range
func (h *DiffHandler) generatePatch(ctx context.Context, repoPath, base, head string) (string, error) {
	// Open repository
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return "", fmt.Errorf("failed to open repository: %w", err)
	}

	// Resolve base and head references to commit objects
	baseCommit, err := h.resolveReferenceToCommit(repo, base)
	if err != nil {
		return "", fmt.Errorf("failed to resolve base reference %s: %w", base, err)
	}

	headCommit, err := h.resolveReferenceToCommit(repo, head)
	if err != nil {
		return "", fmt.Errorf("failed to resolve head reference %s: %w", head, err)
	}

	// Generate patch using go-git's built-in patch functionality
	patch, err := baseCommit.Patch(headCommit)
	if err != nil {
		return "", fmt.Errorf("failed to generate patch: %w", err)
	}

	return patch.String(), nil
}

// calculateCommitDiff performs the actual diff calculation between two commits
func (h *DiffHandler) calculateCommitDiff(ctx context.Context, repoPath, base, head string, contextLines int) (*DiffResponse, error) {
	// Open repository using go-git
	gitRepo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	// Get commit objects
	baseCommit, err := gitRepo.CommitObject(plumbing.NewHash(base))
	if err != nil {
		return nil, fmt.Errorf("failed to get base commit %s: %w", base, err)
	}

	headCommit, err := gitRepo.CommitObject(plumbing.NewHash(head))
	if err != nil {
		return nil, fmt.Errorf("failed to get head commit %s: %w", head, err)
	}

	// Get trees from commits
	baseTree, err := baseCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get base tree: %w", err)
	}

	headTree, err := headCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get head tree: %w", err)
	}

	// Calculate diff between trees
	changes, err := object.DiffTree(baseTree, headTree)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate tree diff: %w", err)
	}

	// Process changes into structured response
	fileDiffs, stats, err := h.processChanges(changes, contextLines)
	if err != nil {
		return nil, fmt.Errorf("failed to process changes: %w", err)
	}

	return &DiffResponse{
		Base:  base,
		Head:  head,
		Files: fileDiffs,
		Stats: stats,
	}, nil
}

// processChanges converts go-git changes into structured FileDiff objects
func (h *DiffHandler) processChanges(changes object.Changes, contextLines int) ([]FileDiff, DiffStats, error) {
	var fileDiffs []FileDiff
	var stats DiffStats

	for _, change := range changes {
		fileDiff, err := h.processChange(change, contextLines)
		if err != nil {
			// Get the file name for logging
			fileName := getFileName(change)
			// Log error but continue processing other files
			fmt.Printf("Error processing change for %s: %v\n", fileName, err)
			continue
		}

		fileDiffs = append(fileDiffs, fileDiff)
		stats.FilesChanged++
		stats.TotalAdditions += fileDiff.Additions
		stats.TotalDeletions += fileDiff.Deletions
	}

	return fileDiffs, stats, nil
}

// processChange processes a single file change
func (h *DiffHandler) processChange(change *object.Change, contextLines int) (FileDiff, error) {
	fileDiff := FileDiff{
		Path: getFileName(change),
	}

	// Determine change status
	switch {
	case change.From.Name == "" && change.To.Name != "":
		fileDiff.Status = "added"
	case change.From.Name != "" && change.To.Name == "":
		fileDiff.Status = "deleted"
	case change.From.Name != change.To.Name:
		fileDiff.Status = "renamed"
		fileDiff.OldPath = change.From.Name
	default:
		fileDiff.Status = "modified"
	}

	// Get patch information for line-level changes
	patch, err := change.Patch()
	if err != nil {
		return fileDiff, fmt.Errorf("failed to get patch: %w", err)
	}

	// Count additions and deletions from patch content
	patchText := patch.String()
	lines := splitLines(patchText)

	for _, line := range lines {
		if len(line) > 0 {
			switch line[0] {
			case '+':
				fileDiff.Additions++
			case '-':
				fileDiff.Deletions++
			}
		}
	}

	// Check if this is a binary file change (simple heuristic)
	fileDiff.IsBinary = isBinaryPatch(patchText)

	// Generate line-level changes if not binary and contextLines > 0
	if !fileDiff.IsBinary && contextLines > 0 {
		lineChanges := h.generateLineChanges(lines, contextLines)
		fileDiff.Changes = lineChanges
	}

	return fileDiff, nil
}

// generateLineChanges creates line-by-line diff information
func (h *DiffHandler) generateLineChanges(lines []string, contextLines int) []LineChange {
	var lineChanges []LineChange
	lineNum := 1

	for _, line := range lines {
		if len(line) == 0 {
			continue
		}

		var changeType string
		switch line[0] {
		case '+':
			changeType = "add"
		case '-':
			changeType = "del"
		case ' ':
			changeType = "context"
		default:
			changeType = "context"
		}

		lineChanges = append(lineChanges, LineChange{
			Type:    changeType,
			LineNum: lineNum,
			Content: line,
		})

		if changeType != "del" {
			lineNum++
		}
	}

	return lineChanges
}

// getFileName extracts the file name from a change object
func getFileName(change *object.Change) string {
	if change.To.Name != "" {
		return change.To.Name
	}
	return change.From.Name
}

// isBinaryPatch checks if a patch represents a binary file change
func isBinaryPatch(patchText string) bool {
	// Simple heuristic: binary files often contain null bytes or have specific markers
	return strings.Contains(patchText, "Binary files") ||
		strings.Contains(patchText, "\x00") ||
		strings.Contains(patchText, "GIT binary patch")
}

// splitLines splits content into individual lines
func splitLines(content string) []string {
	if content == "" {
		return nil
	}

	var lines []string
	start := 0
	for i, char := range content {
		if char == '\n' {
			lines = append(lines, content[start:i])
			start = i + 1
		}
	}

	// Add final line if content doesn't end with newline
	if start < len(content) {
		lines = append(lines, content[start:])
	}

	return lines
}

// isCommitNotFoundError checks if an error is due to commit not being found
func isCommitNotFoundError(err error) bool {
	return err != nil && (err.Error() == "object not found" ||
		err.Error() == "reference not found" ||
		strings.Contains(err.Error(), "object not found") ||
		strings.Contains(err.Error(), "commit not found"))
}

// calculateBranchDiff performs diff calculation between two branches with merge base analysis
func (h *DiffHandler) calculateBranchDiff(ctx context.Context, repoPath, baseBranch, headBranch string, contextLines int) (*DiffResponse, error) {
	// Open repository using go-git
	gitRepo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	// Resolve branch names to commits
	baseCommit, err := h.resolveBranchToCommit(gitRepo, baseBranch)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve base branch %s: %w", baseBranch, err)
	}

	headCommit, err := h.resolveBranchToCommit(gitRepo, headBranch)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve head branch %s: %w", headBranch, err)
	}

	// Find merge base for accurate comparison
	mergeBase, err := h.findMergeBase(gitRepo, baseCommit, headCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to find merge base: %w", err)
	}

	// Calculate commit counts (ahead/behind)
	commitStats, err := h.calculateCommitCounts(gitRepo, baseCommit, headCommit, mergeBase)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate commit counts: %w", err)
	}

	// Get trees for diff calculation (use merge base vs head for accurate branch comparison)
	mergeBaseTree, err := mergeBase.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get merge base tree: %w", err)
	}

	headTree, err := headCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get head tree: %w", err)
	}

	// Calculate diff between merge base and head
	changes, err := object.DiffTree(mergeBaseTree, headTree)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate tree diff: %w", err)
	}

	// Process changes into structured response
	fileDiffs, stats, err := h.processChanges(changes, contextLines)
	if err != nil {
		return nil, fmt.Errorf("failed to process changes: %w", err)
	}

	return &DiffResponse{
		Base:      baseBranch,
		Head:      headBranch,
		Files:     fileDiffs,
		Stats:     stats,
		Commits:   commitStats,
		MergeBase: mergeBase.Hash.String(),
	}, nil
}

// resolveBranchToCommit resolves a branch name to its commit object
func (h *DiffHandler) resolveBranchToCommit(repo *gogit.Repository, branchName string) (*object.Commit, error) {
	// Try to resolve as a reference first
	ref, err := repo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	if err != nil {
		// If branch reference fails, try as a direct hash
		if plumbing.IsHash(branchName) {
			return repo.CommitObject(plumbing.NewHash(branchName))
		}
		return nil, fmt.Errorf("branch %s not found", branchName)
	}

	// Get commit from reference
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, fmt.Errorf("failed to get commit from branch %s: %w", branchName, err)
	}

	return commit, nil
}

// findMergeBase finds the merge base (common ancestor) of two commits
func (h *DiffHandler) findMergeBase(repo *gogit.Repository, commit1, commit2 *object.Commit) (*object.Commit, error) {
	// Use go-git's merge base algorithm
	mergeBaseCommits, err := commit1.MergeBase(commit2)
	if err != nil {
		return nil, fmt.Errorf("failed to find merge base: %w", err)
	}

	if len(mergeBaseCommits) == 0 {
		return nil, fmt.Errorf("no common ancestor found")
	}

	// Return the first (most recent) merge base
	return mergeBaseCommits[0], nil
}

// calculateCommitCounts calculates how many commits each branch is ahead/behind
func (h *DiffHandler) calculateCommitCounts(repo *gogit.Repository, baseCommit, headCommit, mergeBase *object.Commit) (*CommitStats, error) {
	// Count commits from merge base to head (ahead)
	ahead, err := h.countCommitsBetween(repo, mergeBase, headCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to count ahead commits: %w", err)
	}

	// Count commits from merge base to base (behind)
	behind, err := h.countCommitsBetween(repo, mergeBase, baseCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to count behind commits: %w", err)
	}

	return &CommitStats{
		Ahead:  ahead,
		Behind: behind,
	}, nil
}

// countCommitsBetween counts commits between two commits (exclusive of from, inclusive of to)
func (h *DiffHandler) countCommitsBetween(repo *gogit.Repository, from, to *object.Commit) (int, error) {
	if from.Hash == to.Hash {
		return 0, nil
	}

	// Get commit iterator from 'to' commit
	commitIter, err := repo.Log(&gogit.LogOptions{
		From: to.Hash,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to get commit log: %w", err)
	}
	defer commitIter.Close()

	count := 0
	fromHash := from.Hash.String()

	err = commitIter.ForEach(func(commit *object.Commit) error {
		if commit.Hash.String() == fromHash {
			// Stop when we reach the 'from' commit (don't count it)
			return fmt.Errorf("stop iteration") // Use error to break iteration
		}
		count++
		return nil
	})

	// Check if we stopped due to finding the 'from' commit (normal case)
	if err != nil && err.Error() == "stop iteration" {
		return count, nil
	}

	// If no error, we've counted all commits until the root without finding 'from'
	// This means 'from' is not an ancestor of 'to'
	if err == nil {
		return count, nil
	}

	return 0, fmt.Errorf("failed to iterate commits: %w", err)
}

// isBranchNotFoundError checks if an error is due to branch not being found
func isBranchNotFoundError(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "branch") && strings.Contains(err.Error(), "not found") ||
		strings.Contains(err.Error(), "reference not found"))
}

// isFileNotFoundError checks if an error is due to file not being found
func isFileNotFoundError(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "file not found") ||
		strings.Contains(err.Error(), "path not found") ||
		strings.Contains(err.Error(), "entry not found"))
}

// calculateFileDiff performs diff calculation for a specific file between two references
func (h *DiffHandler) calculateFileDiff(ctx context.Context, repoPath, filePath, base, head string, contextLines int, format string) (*DiffResponse, error) {
	// Open repository using go-git
	gitRepo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	// Resolve base and head to commit objects (support both commit hashes and branch names)
	baseCommit, err := h.resolveReferenceToCommit(gitRepo, base)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve base reference %s: %w", base, err)
	}

	headCommit, err := h.resolveReferenceToCommit(gitRepo, head)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve head reference %s: %w", head, err)
	}

	// Get trees from commits
	baseTree, err := baseCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get base tree: %w", err)
	}

	headTree, err := headCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get head tree: %w", err)
	}

	// Get file entries from both trees
	baseFile, baseErr := baseTree.File(filePath)
	headFile, headErr := headTree.File(filePath)

	// Handle cases where file doesn't exist in one or both trees
	var fileDiff FileDiff
	var stats DiffStats

	// Determine file status and process accordingly
	switch {
	case baseErr != nil && headErr != nil:
		// File doesn't exist in either tree
		return nil, fmt.Errorf("file not found in either base or head: %s", filePath)

	case baseErr != nil && headErr == nil:
		// File added in head
		fileDiff, err = h.processFileAddition(headFile, filePath, contextLines, format)
		if err != nil {
			return nil, fmt.Errorf("failed to process file addition: %w", err)
		}

	case baseErr == nil && headErr != nil:
		// File deleted in head
		fileDiff, err = h.processFileDeletion(baseFile, filePath, contextLines, format)
		if err != nil {
			return nil, fmt.Errorf("failed to process file deletion: %w", err)
		}

	default:
		// File exists in both trees, calculate modification diff
		fileDiff, err = h.processFileModification(baseFile, headFile, filePath, contextLines, format)
		if err != nil {
			return nil, fmt.Errorf("failed to process file modification: %w", err)
		}
	}

	// Update stats
	stats.FilesChanged = 1
	stats.TotalAdditions = fileDiff.Additions
	stats.TotalDeletions = fileDiff.Deletions

	return &DiffResponse{
		Base:  base,
		Head:  head,
		Files: []FileDiff{fileDiff},
		Stats: stats,
	}, nil
}

// resolveReferenceToCommit resolves a reference (commit hash or branch name) to a commit object
func (h *DiffHandler) resolveReferenceToCommit(repo *gogit.Repository, ref string) (*object.Commit, error) {
	// Try as commit hash first
	if plumbing.IsHash(ref) {
		return repo.CommitObject(plumbing.NewHash(ref))
	}

	// Try as branch reference
	branchRef, err := repo.Reference(plumbing.NewBranchReferenceName(ref), true)
	if err == nil {
		return repo.CommitObject(branchRef.Hash())
	}

	// Try as tag reference
	tagRef, err := repo.Reference(plumbing.NewTagReferenceName(ref), true)
	if err == nil {
		// Tags might point to commits or tag objects
		obj, err := repo.Object(plumbing.AnyObject, tagRef.Hash())
		if err != nil {
			return nil, fmt.Errorf("failed to resolve tag object: %w", err)
		}

		switch o := obj.(type) {
		case *object.Commit:
			return o, nil
		case *object.Tag:
			return repo.CommitObject(o.Target)
		default:
			return nil, fmt.Errorf("tag points to unsupported object type")
		}
	}

	return nil, fmt.Errorf("reference %s not found", ref)
}

// processFileAddition handles when a file is added (exists only in head)
func (h *DiffHandler) processFileAddition(file *object.File, filePath string, contextLines int, format string) (FileDiff, error) {
	content, err := file.Contents()
	if err != nil {
		return FileDiff{}, fmt.Errorf("failed to get file contents: %w", err)
	}

	lines := splitLines(content)
	additions := len(lines)

	fileDiff := FileDiff{
		Path:      filePath,
		Status:    "added",
		Additions: additions,
		Deletions: 0,
		IsBinary:  isBinaryContent(content),
	}

	// Generate line changes if not binary and contextLines > 0
	if !fileDiff.IsBinary && contextLines > 0 {
		var lineChanges []LineChange
		for i, line := range lines {
			lineChanges = append(lineChanges, LineChange{
				Type:    "add",
				LineNum: i + 1,
				Content: "+" + line,
			})
		}
		fileDiff.Changes = lineChanges
	}

	return fileDiff, nil
}

// processFileDeletion handles when a file is deleted (exists only in base)
func (h *DiffHandler) processFileDeletion(file *object.File, filePath string, contextLines int, format string) (FileDiff, error) {
	content, err := file.Contents()
	if err != nil {
		return FileDiff{}, fmt.Errorf("failed to get file contents: %w", err)
	}

	lines := splitLines(content)
	deletions := len(lines)

	fileDiff := FileDiff{
		Path:      filePath,
		Status:    "deleted",
		Additions: 0,
		Deletions: deletions,
		IsBinary:  isBinaryContent(content),
	}

	// Generate line changes if not binary and contextLines > 0
	if !fileDiff.IsBinary && contextLines > 0 {
		var lineChanges []LineChange
		for i, line := range lines {
			lineChanges = append(lineChanges, LineChange{
				Type:    "del",
				LineNum: i + 1,
				Content: "-" + line,
			})
		}
		fileDiff.Changes = lineChanges
	}

	return fileDiff, nil
}

// processFileModification handles when a file is modified (exists in both base and head)
func (h *DiffHandler) processFileModification(baseFile, headFile *object.File, filePath string, contextLines int, format string) (FileDiff, error) {
	baseContent, err := baseFile.Contents()
	if err != nil {
		return FileDiff{}, fmt.Errorf("failed to get base file contents: %w", err)
	}

	headContent, err := headFile.Contents()
	if err != nil {
		return FileDiff{}, fmt.Errorf("failed to get head file contents: %w", err)
	}

	// Check if either file is binary
	isBinary := isBinaryContent(baseContent) || isBinaryContent(headContent)

	fileDiff := FileDiff{
		Path:     filePath,
		Status:   "modified",
		IsBinary: isBinary,
	}

	// For binary files, just indicate they changed
	if isBinary {
		return fileDiff, nil
	}

	// Calculate line-level diff for text files
	baseLines := splitLines(baseContent)
	headLines := splitLines(headContent)

	// Simple line-based diff algorithm
	lineChanges, additions, deletions := h.calculateLineDiff(baseLines, headLines, contextLines, format)

	fileDiff.Additions = additions
	fileDiff.Deletions = deletions
	fileDiff.Changes = lineChanges

	return fileDiff, nil
}

// calculateLineDiff performs line-based diff calculation
func (h *DiffHandler) calculateLineDiff(baseLines, headLines []string, contextLines int, format string) ([]LineChange, int, int) {
	var lineChanges []LineChange
	additions := 0
	deletions := 0

	// Simple diff algorithm: find common subsequence and identify changes
	// This is a basic implementation - for production, consider using a more sophisticated diff algorithm

	baseIndex := 0
	headIndex := 0
	lineNum := 1

	for baseIndex < len(baseLines) || headIndex < len(headLines) {
		if baseIndex >= len(baseLines) {
			// Remaining lines in head are additions
			for headIndex < len(headLines) {
				lineChanges = append(lineChanges, LineChange{
					Type:    "add",
					LineNum: lineNum,
					Content: "+" + headLines[headIndex],
				})
				additions++
				headIndex++
				lineNum++
			}
		} else if headIndex >= len(headLines) {
			// Remaining lines in base are deletions
			for baseIndex < len(baseLines) {
				lineChanges = append(lineChanges, LineChange{
					Type:    "del",
					LineNum: lineNum,
					Content: "-" + baseLines[baseIndex],
				})
				deletions++
				baseIndex++
			}
		} else if baseLines[baseIndex] == headLines[headIndex] {
			// Lines are the same, add as context if within context window
			lineChanges = append(lineChanges, LineChange{
				Type:    "context",
				LineNum: lineNum,
				Content: " " + baseLines[baseIndex],
			})
			baseIndex++
			headIndex++
			lineNum++
		} else {
			// Lines differ, mark as deletion and addition
			lineChanges = append(lineChanges, LineChange{
				Type:    "del",
				LineNum: lineNum,
				Content: "-" + baseLines[baseIndex],
			})
			deletions++
			baseIndex++

			lineChanges = append(lineChanges, LineChange{
				Type:    "add",
				LineNum: lineNum,
				Content: "+" + headLines[headIndex],
			})
			additions++
			headIndex++
			lineNum++
		}
	}

	return lineChanges, additions, deletions
}

// isBinaryContent checks if content appears to be binary
func isBinaryContent(content string) bool {
	// Simple heuristic: if content contains null bytes, consider it binary
	return strings.Contains(content, "\x00")
}
