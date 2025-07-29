// Package protocol implements Git diff REST API endpoints for the executor.
// This file handles diff operations that provide structured JSON responses.
package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/git"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/collabops/git-executor/internal/repository"
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
	Path      string                 `json:"path"`
	Status    string                 `json:"status"` // "added", "deleted", "modified", "renamed"
	Additions int                    `json:"additions"`
	Deletions int                    `json:"deletions"`
	Changes   []LineChange           `json:"changes,omitempty"`
	IsBinary  bool                   `json:"is_binary"`
	OldPath   string                 `json:"old_path,omitempty"` // For renames
	Metadata  map[string]interface{} `json:"metadata,omitempty"` // File metadata (hashes, modes)
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

	// Extract and validate common parameters
	params, err := h.validateAndExtractParams(r)
	if err != nil {
		handleDiffError(w, err, "extract parameters")
		return
	}

	// Validate base and head parameters are required for commit diff
	if params.Base == "" || params.Head == "" {
		handleDiffError(w, fmt.Errorf("missing required parameters: base or head"), "validate commit parameters")
		return
	}

	// Validate permissions and get repository path
	repoPath, err := h.validateRequestAndPermissions(ctx, params)
	if err != nil {
		handleDiffError(w, err, "validate permissions")
		return
	}

	// Get context lines from query parameter
	contextLines := 3 // default
	if contextStr := r.URL.Query().Get("context"); contextStr != "" {
		if ctx, err := strconv.Atoi(contextStr); err == nil && ctx >= 0 {
			contextLines = ctx
		}
	}

	// Calculate diff
	diffResp, err := h.calculateCommitDiff(ctx, repoPath, params.Base, params.Head, contextLines)
	if err != nil {
		handleDiffError(w, err, "calculate commit diff")
		return
	}

	// Check for pagination request
	if r.URL.Query().Get("page") != "" || r.URL.Query().Get("page_size") != "" {
		pagination := extractPaginationParams(r)
		paginatedFiles, updatedPagination := paginateFiles(diffResp.Files, pagination)

		response := &PaginatedDiffResponse{
			DiffResponse: &DiffResponse{
				Base:  diffResp.Base,
				Head:  diffResp.Head,
				Files: paginatedFiles,
				Stats: DiffStats{
					TotalAdditions: diffResp.Stats.TotalAdditions,
					TotalDeletions: diffResp.Stats.TotalDeletions,
					FilesChanged:   len(paginatedFiles), // Updated for paginated response
				},
			},
			Pagination: &updatedPagination,
		}
		sendJSONResponse(w, response, http.StatusOK)
	} else {
		sendJSONResponse(w, diffResp, http.StatusOK)
	}
}

// HandleBranchDiff handles GET /repos/:owner/:repo/diff/branches/:base/:head
func (h *DiffHandler) HandleBranchDiff(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract and validate common parameters
	params, err := h.validateAndExtractParams(r)
	if err != nil {
		handleDiffError(w, err, "extract parameters")
		return
	}

	// Validate base and head parameters are required for branch diff
	if params.Base == "" || params.Head == "" {
		handleDiffError(w, fmt.Errorf("missing required parameters: base or head"), "validate branch parameters")
		return
	}

	// Validate permissions and get repository path
	repoPath, err := h.validateRequestAndPermissions(ctx, params)
	if err != nil {
		handleDiffError(w, err, "validate permissions")
		return
	}

	// Get context lines from query parameter
	contextLines := 3 // default
	if contextStr := r.URL.Query().Get("context"); contextStr != "" {
		if ctx, err := strconv.Atoi(contextStr); err == nil && ctx >= 0 {
			contextLines = ctx
		}
	}

	// Calculate branch diff
	diffResp, err := h.calculateBranchDiff(ctx, repoPath, params.Base, params.Head, contextLines)
	if err != nil {
		handleDiffError(w, err, "calculate branch diff")
		return
	}

	// Check for pagination request
	if r.URL.Query().Get("page") != "" || r.URL.Query().Get("page_size") != "" {
		pagination := extractPaginationParams(r)
		paginatedFiles, updatedPagination := paginateFiles(diffResp.Files, pagination)

		response := &PaginatedDiffResponse{
			DiffResponse: &DiffResponse{
				Base:  diffResp.Base,
				Head:  diffResp.Head,
				Files: paginatedFiles,
				Stats: DiffStats{
					TotalAdditions: diffResp.Stats.TotalAdditions,
					TotalDeletions: diffResp.Stats.TotalDeletions,
					FilesChanged:   len(paginatedFiles), // Updated for paginated response
				},
				Commits:   diffResp.Commits,
				MergeBase: diffResp.MergeBase,
			},
			Pagination: &updatedPagination,
		}
		sendJSONResponse(w, response, http.StatusOK)
	} else {
		sendJSONResponse(w, diffResp, http.StatusOK)
	}
}

// HandleFileDiff handles GET /repos/:owner/:repo/diff/file?path=:path&base=:base&head=:head
func (h *DiffHandler) HandleFileDiff(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract and validate common parameters
	params, err := h.validateAndExtractParams(r)
	if err != nil {
		handleDiffError(w, err, "extract parameters")
		return
	}

	// Extract file-specific parameters from query string
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		handleDiffError(w, fmt.Errorf("missing required parameter: path"), "validate file parameters")
		return
	}

	base := r.URL.Query().Get("base")
	head := r.URL.Query().Get("head")
	if base == "" || head == "" {
		handleDiffError(w, fmt.Errorf("missing required parameters: base or head"), "validate file diff parameters")
		return
	}

	// Override params with query parameters for file diff
	params.Base = base
	params.Head = head

	// Validate permissions and get repository path
	repoPath, err := h.validateRequestAndPermissions(ctx, params)
	if err != nil {
		handleDiffError(w, err, "validate permissions")
		return
	}

	// Get context lines and format from query parameters
	contextLines := 3 // default
	if contextStr := r.URL.Query().Get("context"); contextStr != "" {
		if ctx, err := strconv.Atoi(contextStr); err == nil && ctx >= 0 {
			contextLines = ctx
		}
	}

	format := r.URL.Query().Get("format")
	if format == "" {
		format = "unified" // default
	}

	// Calculate file diff
	diffResp, err := h.calculateFileDiff(ctx, repoPath, filePath, params.Base, params.Head, contextLines, format)
	if err != nil {
		handleDiffError(w, err, "calculate file diff")
		return
	}

	sendJSONResponse(w, diffResp, http.StatusOK)
}

// HandlePatch handles GET /repos/:owner/:repo/patch/:base/:head
func (h *DiffHandler) HandlePatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract and validate common parameters
	params, err := h.validateAndExtractParams(r)
	if err != nil {
		handleDiffError(w, err, "extract parameters")
		return
	}

	// Validate base and head parameters are required for patch
	if params.Base == "" || params.Head == "" {
		handleDiffError(w, fmt.Errorf("missing required parameters: base or head"), "validate patch parameters")
		return
	}

	// Validate permissions and get repository path
	repoPath, err := h.validateRequestAndPermissions(ctx, params)
	if err != nil {
		handleDiffError(w, err, "validate permissions")
		return
	}

	// Generate patch content
	patchContent, err := h.generatePatch(ctx, repoPath, params.Base, params.Head)
	if err != nil {
		handleDiffError(w, err, "generate patch")
		return
	}

	// Set appropriate headers for patch content
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s-%s.patch\"", params.Base, params.Head))

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
	// Open repository using cached approach
	gitRepo, cleanup, err := h.getGitRepository(repoPath)
	if err != nil {
		return nil, err
	}
	defer cleanup()

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
	fileName := getFileName(change)
	fileDiff := FileDiff{
		Path:     fileName,
		Metadata: getFileMetadata(change),
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

	// Enhanced binary file detection using both patch content and filename
	fileDiff.IsBinary = isBinaryFile(patchText, fileName)

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
	// Open repository using cached approach
	gitRepo, cleanup, err := h.getGitRepository(repoPath)
	if err != nil {
		return nil, err
	}
	defer cleanup()

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
	// Open repository using cached approach
	gitRepo, cleanup, err := h.getGitRepository(repoPath)
	if err != nil {
		return nil, err
	}
	defer cleanup()

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

// Common request validation and response utilities

// RequestParams holds common request parameters for diff endpoints
type RequestParams struct {
	Owner  string
	Repo   string
	Base   string
	Head   string
	UserID string
}

// PaginationParams holds pagination parameters
type PaginationParams struct {
	Page     int  `json:"page"`
	PageSize int  `json:"page_size"`
	Total    int  `json:"total"`
	HasMore  bool `json:"has_more"`
}

// ErrorResponse represents a standardized error response
type ErrorResponse struct {
	Error   string `json:"error"`
	Code    string `json:"code,omitempty"`
	Details string `json:"details,omitempty"`
}

// PaginatedDiffResponse extends DiffResponse with pagination
type PaginatedDiffResponse struct {
	*DiffResponse
	Pagination *PaginationParams `json:"pagination,omitempty"`
}

// validateAndExtractParams extracts and validates common parameters from request
func (h *DiffHandler) validateAndExtractParams(r *http.Request) (*RequestParams, error) {
	// Extract path parameters
	owner := URLParam(r, "owner")
	repo := URLParam(r, "repo")
	base := URLParam(r, "base")
	head := URLParam(r, "head")

	// Validate required parameters
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("missing required parameters: owner or repo")
	}

	// Get user ID from header for permission checking
	userID := r.Header.Get("X-USER-ID")
	if userID == "" {
		return nil, fmt.Errorf("missing user authentication")
	}

	return &RequestParams{
		Owner:  owner,
		Repo:   repo,
		Base:   base,
		Head:   head,
		UserID: userID,
	}, nil
}

// validateRequestAndPermissions performs common validation and permission checking
func (h *DiffHandler) validateRequestAndPermissions(ctx context.Context, params *RequestParams) (string, error) {
	// Check user permissions (read access for diff operations)
	hasAccess, err := h.permissionClient.CheckReadAccess(ctx, params.UserID, params.Owner, params.Repo)
	if err != nil {
		return "", fmt.Errorf("permission check failed: %w", err)
	}
	if !hasAccess {
		return "", fmt.Errorf("access denied to repository")
	}

	// Get repository path and validate
	repoPath, err := repository.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		return "", fmt.Errorf("invalid repository path: %w", err)
	}

	return repoPath, nil
}

// sendJSONResponse sends a standardized JSON response
func sendJSONResponse(w http.ResponseWriter, data interface{}, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if err := json.NewEncoder(w).Encode(data); err != nil {
		// Log error but can't change response at this point
		fmt.Printf("Failed to encode JSON response: %v\n", err)
	}
}

// sendErrorResponse sends a standardized error response
func sendErrorResponse(w http.ResponseWriter, message string, statusCode int) {
	errorResp := ErrorResponse{
		Error: message,
	}
	sendJSONResponse(w, errorResp, statusCode)
}

// handleDiffError categorizes errors and sends appropriate HTTP responses
func handleDiffError(w http.ResponseWriter, err error, operation string) {
	if isCommitNotFoundError(err) {
		sendErrorResponse(w, fmt.Sprintf("Commit not found: %v", err), http.StatusNotFound)
	} else if isBranchNotFoundError(err) {
		sendErrorResponse(w, fmt.Sprintf("Branch not found: %v", err), http.StatusNotFound)
	} else if isFileNotFoundError(err) {
		sendErrorResponse(w, fmt.Sprintf("File not found: %v", err), http.StatusNotFound)
	} else if strings.Contains(err.Error(), "permission") {
		sendErrorResponse(w, fmt.Sprintf("Permission check failed: %v", err), http.StatusInternalServerError)
	} else if strings.Contains(err.Error(), "access denied") {
		sendErrorResponse(w, "Access denied to repository", http.StatusForbidden)
	} else if strings.Contains(err.Error(), "authentication") {
		sendErrorResponse(w, err.Error(), http.StatusUnauthorized)
	} else if strings.Contains(err.Error(), "missing") || strings.Contains(err.Error(), "invalid") {
		sendErrorResponse(w, err.Error(), http.StatusBadRequest)
	} else {
		sendErrorResponse(w, fmt.Sprintf("Failed to %s: %v", operation, err), http.StatusInternalServerError)
	}
}

// extractPaginationParams extracts pagination parameters from query string
func extractPaginationParams(r *http.Request) PaginationParams {
	params := PaginationParams{
		Page:     1,  // Default to page 1
		PageSize: 50, // Default page size
	}

	if pageStr := r.URL.Query().Get("page"); pageStr != "" {
		if page, err := strconv.Atoi(pageStr); err == nil && page > 0 {
			params.Page = page
		}
	}

	if sizeStr := r.URL.Query().Get("page_size"); sizeStr != "" {
		if size, err := strconv.Atoi(sizeStr); err == nil && size > 0 && size <= 200 {
			params.PageSize = size
		}
	}

	return params
}

// paginateFiles applies pagination to file diffs
func paginateFiles(files []FileDiff, pagination PaginationParams) ([]FileDiff, PaginationParams) {
	total := len(files)
	pagination.Total = total

	// Calculate start and end indices
	start := (pagination.Page - 1) * pagination.PageSize
	if start >= total {
		pagination.HasMore = false
		return []FileDiff{}, pagination
	}

	end := start + pagination.PageSize
	if end > total {
		end = total
		pagination.HasMore = false
	} else {
		pagination.HasMore = true
	}

	return files[start:end], pagination
}

// Enhanced binary detection utilities

// isBinaryFile checks if a file is binary based on its content and extension
func isBinaryFile(content string, filename string) bool {
	// First check content for binary indicators
	if isBinaryContent(content) {
		return true
	}

	// Check file extension for known binary types
	ext := strings.ToLower(filepath.Ext(filename))
	binaryExtensions := map[string]bool{
		".exe": true, ".dll": true, ".so": true, ".dylib": true,
		".zip": true, ".tar": true, ".gz": true, ".bz2": true,
		".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".bmp": true,
		".pdf": true, ".doc": true, ".docx": true, ".xls": true, ".xlsx": true,
		".mp3": true, ".mp4": true, ".avi": true, ".mov": true,
		".bin": true, ".dat": true, ".db": true,
	}

	return binaryExtensions[ext]
}

// getFileMetadata extracts metadata about a file change
func getFileMetadata(change *object.Change) map[string]interface{} {
	metadata := make(map[string]interface{})

	// Extract file mode information
	if change.From.TreeEntry.Mode != 0 {
		metadata["old_mode"] = change.From.TreeEntry.Mode.String()
	}
	if change.To.TreeEntry.Mode != 0 {
		metadata["new_mode"] = change.To.TreeEntry.Mode.String()
	}

	// Extract file hash information
	if !change.From.TreeEntry.Hash.IsZero() {
		metadata["old_hash"] = change.From.TreeEntry.Hash.String()
	}
	if !change.To.TreeEntry.Hash.IsZero() {
		metadata["new_hash"] = change.To.TreeEntry.Hash.String()
	}

	return metadata
}

// getGitRepository opens a Git repository using the cache and returns the underlying go-git repository
func (h *DiffHandler) getGitRepository(repoPath string) (*gogit.Repository, func(), error) {
	repo, err := git.OpenRepositoryWithNewCache(repoPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open repository: %w", err)
	}

	// Create cleanup function
	cleanup := func() {
		_ = repo.Close()
	}

	// Get the underlying go-git repository
	goGitRepo, ok := repo.(*git.GoGitRepository)
	if !ok {
		cleanup()
		return nil, nil, fmt.Errorf("unexpected repository type")
	}

	return goGitRepo.GetGoGitRepo(), cleanup, nil
}
