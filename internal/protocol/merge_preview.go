package protocol

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/git"
	"github.com/collabops/git-executor/internal/schemas/common"
)

// MergePreviewRequest represents the request for merge preview operations
type MergePreviewRequest struct {
	SourceRef       string        `form:"source_ref" binding:"required"` // Source branch/commit to merge FROM
	TargetRef       string        `form:"target_ref" binding:"required"` // Target branch/commit to merge INTO
	Strategy        MergeStrategy `form:"strategy,omitempty"`            // Merge strategy (default: recursive)
	IncludeChanges  bool          `form:"include_changes,omitempty"`     // Include detailed file changes
	IncludeContent  bool          `form:"include_content,omitempty"`     // Include file content previews
	MaxFileSize     int64         `form:"max_file_size,omitempty"`       // Max file size for content preview (default: 1MB)
	MaxFiles        int           `form:"max_files,omitempty"`           // Max files to analyze (default: 1000)
	CompareStrategy string        `form:"compare_strategy,omitempty"`    // tree, diff, or full (default: tree)
}

// MergePreviewResponse represents the response for merge preview operations
type MergePreviewResponse struct {
	CanMerge           bool                     `json:"can_merge"`                     // Whether merge can proceed
	IsFastForward      bool                     `json:"is_fast_forward"`               // Whether merge would be fast-forward
	HasConflicts       bool                     `json:"has_conflicts"`                 // Whether conflicts exist
	ConflictCount      int                      `json:"conflict_count"`                // Number of conflicts
	Strategy           MergeStrategy            `json:"strategy"`                      // Strategy to be used
	RecommendedActions []string                 `json:"recommended_actions,omitempty"` // Recommended actions
	MergeBase          *common.CommitInfo       `json:"merge_base,omitempty"`          // Common ancestor commit
	ConflictSummary    *git.MergeAnalysisResult `json:"conflict_summary,omitempty"`    // Detailed conflict analysis
	ChangesSummary     *ChangesSummary          `json:"changes_summary,omitempty"`     // Summary of changes
	FilesChanged       []*FileChange            `json:"files_changed,omitempty"`       // Detailed file changes
	PerformanceMetrics *PreviewMetrics          `json:"performance_metrics,omitempty"` // Analysis performance
	Warnings           []string                 `json:"warnings,omitempty"`            // Any warnings or notes
	// GitHub PR style statistics
	Commits       *CommitStats    `json:"commits,omitempty"`         // Commit statistics (ahead/behind)
	Contributors  []string        `json:"contributors,omitempty"`    // List of contributors
	CommitsByDate []CommitsByDate `json:"commits_by_date,omitempty"` // Commits grouped by date

	// Simple count summary for UI display
	CountSummary *CountSummary `json:"count_summary,omitempty"` // Simple count summary
}

// CountSummary provides simple count summary for UI display
type CountSummary struct {
	Commits      int `json:"commits"`      // Total commits count
	Files        int `json:"files"`        // Total files changed count
	Contributors int `json:"contributors"` // Total contributors count
}

// ChangesSummary provides high-level statistics about merge changes
type ChangesSummary struct {
	TotalFiles    int `json:"total_files"`    // Total files affected
	AddedFiles    int `json:"added_files"`    // Files added
	ModifiedFiles int `json:"modified_files"` // Files modified
	DeletedFiles  int `json:"deleted_files"`  // Files deleted
	RenamedFiles  int `json:"renamed_files"`  // Files renamed
	LinesAdded    int `json:"lines_added"`    // Total lines added
	LinesDeleted  int `json:"lines_deleted"`  // Total lines deleted
	BinaryFiles   int `json:"binary_files"`   // Binary files affected
}

// FileChange represents a change to a single file in the merge
// Reuses FileDiff from diff.go but adds merge-specific fields
type FileChange struct {
	FileDiff                                  // Embed FileDiff to reuse all diff functionality
	Content            *FileContent           `json:"content,omitempty"`             // File content preview
	Conflicts          []string               `json:"conflicts,omitempty"`           // Conflict descriptions
	ContentAnalysis    map[string]interface{} `json:"content_analysis,omitempty"`    // Detailed content analysis
	PotentialConflicts []string               `json:"potential_conflicts,omitempty"` // Potential merge conflicts
}

// FileContent represents file content preview
type FileContent struct {
	SourceContent string `json:"source_content,omitempty"` // Source file content
	TargetContent string `json:"target_content,omitempty"` // Target file content
	MergedContent string `json:"merged_content,omitempty"` // Predicted merged content
	Encoding      string `json:"encoding,omitempty"`       // Content encoding
	IsTruncated   bool   `json:"is_truncated,omitempty"`   // Whether content was truncated
}

// PreviewMetrics contains performance metrics for the preview operation
type PreviewMetrics struct {
	AnalysisTime         time.Duration `json:"analysis_time"`          // Total analysis time
	ConflictAnalysisTime time.Duration `json:"conflict_analysis_time"` // Conflict analysis time
	TreeComparisonTime   time.Duration `json:"tree_comparison_time"`   // Tree comparison time
	FilesAnalyzed        int           `json:"files_analyzed"`         // Number of files analyzed
	ObjectsTraversed     int           `json:"objects_traversed"`      // Git objects traversed
	MemoryUsed           int64         `json:"memory_used,omitempty"`  // Memory usage estimate
}

// validateMergePreviewRequest validates the merge preview request parameters
func (h *MergePreviewHandler) validateMergePreviewRequest(req *MergePreviewRequest) error {
	// Validate source and target refs
	if req.SourceRef == "" {
		return api.NewBadRequestError("source_ref is required")
	}
	if req.TargetRef == "" {
		return api.NewBadRequestError("target_ref is required")
	}

	// Validate strategy
	switch req.Strategy {
	case MergeStrategyRecursive, MergeStrategyResolve, MergeStrategyOurs, MergeStrategyTheirs, MergeStrategySubtree:
		// Valid strategy
	default:
		return api.NewBadRequestError(fmt.Sprintf("Invalid merge strategy: %s", req.Strategy))
	}

	// Validate file size limits
	if req.MaxFileSize < 0 {
		return api.NewBadRequestError("max_file_size cannot be negative")
	}
	if req.MaxFileSize > 100*1024*1024 { // 100MB max
		return api.NewBadRequestError("max_file_size cannot exceed 100MB")
	}

	// Validate file count limits
	if req.MaxFiles < 0 {
		return api.NewBadRequestError("max_files cannot be negative")
	}
	if req.MaxFiles > 10000 { // 10k files max
		return api.NewBadRequestError("max_files cannot exceed 10000")
	}

	// Validate compare strategy
	switch req.CompareStrategy {
	case "tree", "diff", "full":
		// Valid strategies
	default:
		return api.NewBadRequestError(fmt.Sprintf("Invalid compare strategy: %s (must be tree, diff, or full)", req.CompareStrategy))
	}

	return nil
}

// performMergePreview performs comprehensive merge preview analysis
func (h *MergePreviewHandler) performMergePreview(ctx context.Context, repo *gogit.Repository, sourceHash, targetHash plumbing.Hash, req *MergePreviewRequest, startTime time.Time) (*MergePreviewResponse, error) {
	metrics := &PreviewMetrics{
		FilesAnalyzed:    0,
		ObjectsTraversed: 0,
	}

	// Phase 1: Conflict Analysis
	conflictStartTime := time.Now()
	conflictAnalysis, err := h.analyzeConflicts(ctx, repo, sourceHash, targetHash, req)
	if err != nil {
		return nil, fmt.Errorf("conflict analysis failed: %w", err)
	}
	metrics.ConflictAnalysisTime = time.Since(conflictStartTime)

	// Phase 2: Tree Comparison (always include changes for merge preview)
	var changesSummary *ChangesSummary
	var filesChanged []*FileChange
	treeStartTime := time.Now()
	changesSummary, filesChanged, err = h.compareRepositoryTrees(ctx, repo, sourceHash, targetHash, req, metrics)
	if err != nil {
		return nil, fmt.Errorf("tree comparison failed: %w", err)
	}
	metrics.TreeComparisonTime = time.Since(treeStartTime)

	// Phase 3: Generate recommendations and warnings
	recommendations := h.generateRecommendations(conflictAnalysis, changesSummary, req)
	warnings := h.generateWarnings(conflictAnalysis, changesSummary, req)

	// Phase 4: Find merge base
	var mergeBase *common.CommitInfo
	if conflictAnalysis != nil && !conflictAnalysis.IsFastForward {
		mergeBaseCommit, err := h.findMergeBase(repo, sourceHash, targetHash)
		if err != nil {
			h.logger.Warn("Failed to find merge base", zap.Error(err))
		} else if mergeBaseCommit != nil {
			// Convert to common.CommitInfo
			mergeBase = &common.CommitInfo{
				SHA:     mergeBaseCommit.Hash.String(),
				Message: mergeBaseCommit.Message,
				Author: common.AuthorInfo{
					Name:  mergeBaseCommit.Author.Name,
					Email: mergeBaseCommit.Author.Email,
				},
				Committer: common.AuthorInfo{
					Name:  mergeBaseCommit.Committer.Name,
					Email: mergeBaseCommit.Committer.Email,
				},
				CreatedAt: mergeBaseCommit.Author.When,
			}
		}
	}

	// Calculate total analysis time
	metrics.AnalysisTime = time.Since(startTime)

	// Calculate GitHub PR style statistics
	commitStats, contributors, commitsByDate, err := h.calculateMergeStatistics(ctx, repo, sourceHash, targetHash)
	if err != nil {
		h.logger.Warn("Failed to calculate merge statistics", zap.Error(err))
		// Initialize with empty values to ensure response fields are always present
		commitStats = &CommitStats{Ahead: 0, Behind: 0}
		contributors = []string{}
		commitsByDate = []CommitsByDate{}
	}

	// Calculate simple count summary for UI display
	filesCount := 0
	if changesSummary != nil {
		filesCount = changesSummary.TotalFiles
	}

	// Count summary: target 기준으로 병합될 source의 커밋 수
	countSummary := &CountSummary{
		Commits:      commitStats.Ahead, // Source가 target보다 앞서 있는 커밋 수 (= 병합될 커밋 수)
		Files:        filesCount,        // Total files changed
		Contributors: len(contributors), // Number of contributors
	}

	// Build response
	response := &MergePreviewResponse{
		CanMerge:           !conflictAnalysis.HasConflicts || conflictAnalysis.CanMerge,
		IsFastForward:      conflictAnalysis.IsFastForward,
		HasConflicts:       conflictAnalysis.HasConflicts,
		ConflictCount:      conflictAnalysis.ConflictCount,
		Strategy:           req.Strategy,
		RecommendedActions: recommendations,
		MergeBase:          mergeBase,
		ConflictSummary:    conflictAnalysis,
		ChangesSummary:     changesSummary,
		FilesChanged:       filesChanged,
		PerformanceMetrics: metrics,
		Warnings:           warnings,
		// GitHub PR style statistics
		Commits:       commitStats,
		Contributors:  contributors,
		CommitsByDate: commitsByDate,
		// Simple count summary
		CountSummary: countSummary,
	}

	return response, nil
}

// analyzeConflicts performs conflict analysis using the ConflictDetector
func (h *MergePreviewHandler) analyzeConflicts(ctx context.Context, repo *gogit.Repository, sourceHash, targetHash plumbing.Hash, req *MergePreviewRequest) (*git.MergeAnalysisResult, error) {
	// Create analysis request with already opened repository
	analysisReq := &git.MergeAnalysisRequest{
		Repository:     repo, // Use already opened repository (중복 오픈 방지)
		Owner:          "",   // Not needed when Repository is provided
		Repo:           "",   // Not needed when Repository is provided
		SourceRef:      sourceHash.String(),
		TargetRef:      targetHash.String(),
		Strategy:       git.MergeStrategy(req.Strategy),
		AutoResolve:    false, // Preview shouldn't auto-resolve
		IncludeContent: req.IncludeContent,
		MaxFileSize:    req.MaxFileSize,
	}

	// Perform analysis
	return h.conflictDetector.AnalyzeMerge(ctx, analysisReq)
}

// compareRepositoryTrees compares source and target trees to find changes
func (h *MergePreviewHandler) compareRepositoryTrees(ctx context.Context, repo *gogit.Repository, sourceHash, targetHash plumbing.Hash, req *MergePreviewRequest, metrics *PreviewMetrics) (*ChangesSummary, []*FileChange, error) {
	// Get source and target commits
	sourceCommit, err := repo.CommitObject(sourceHash)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get source commit: %w", err)
	}

	targetCommit, err := repo.CommitObject(targetHash)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get target commit: %w", err)
	}

	// Get source and target trees
	sourceTree, err := sourceCommit.Tree()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get source tree: %w", err)
	}

	targetTree, err := targetCommit.Tree()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get target tree: %w", err)
	}

	// Compare trees based on strategy (default to diff for better analysis)
	strategy := req.CompareStrategy
	if strategy == "" {
		strategy = "diff"
	}

	switch strategy {
	case "tree":
		return h.compareTreesBasic(repo, sourceTree, targetTree, req, metrics)
	case "diff":
		return h.compareTreesWithDiff(repo, sourceTree, targetTree, req, metrics)
	case "full":
		return h.compareTreesFull(repo, sourceTree, targetTree, req, metrics)
	default:
		return h.compareTreesWithDiff(repo, sourceTree, targetTree, req, metrics)
	}
}

// compareTreesBasic performs basic tree comparison without detailed diffs
func (h *MergePreviewHandler) compareTreesBasic(repo *gogit.Repository, sourceTree, targetTree *object.Tree, req *MergePreviewRequest, metrics *PreviewMetrics) (*ChangesSummary, []*FileChange, error) {
	// Use go-git's DiffTree to get actual diff between trees
	changes, err := object.DiffTree(targetTree, sourceTree)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to calculate tree diff: %w", err)
	}

	// Process the changes to extract file information
	summary := &ChangesSummary{
		TotalFiles:    0,
		AddedFiles:    0,
		ModifiedFiles: 0,
		DeletedFiles:  0,
		RenamedFiles:  0,
		LinesAdded:    0,
		LinesDeleted:  0,
		BinaryFiles:   0,
	}

	var fileChanges []*FileChange

	// Process each change using diff.go's logic
	for _, change := range changes {
		// Use diff.go's processChange function to get FileDiff with line changes
		fileDiff, err := h.processChangeFromDiff(change, 3) // Default context lines
		if err != nil {
			// Log error but continue with other files
			fmt.Printf("Error processing change for %s: %v\n", getFileName(change), err)
			continue
		}

		// Create FileChange with embedded FileDiff
		fileChange := &FileChange{
			FileDiff: fileDiff,
		}

		// Add merge-specific content
		if repo != nil {
			fileChange.Content = h.getFileContent(repo, change)
		}

		// Update summary based on file status
		switch fileDiff.Status {
		case FileDiffStatusAdded:
			summary.AddedFiles++
		case FileDiffStatusDeleted:
			summary.DeletedFiles++
		case FileDiffStatusRenamed:
			summary.RenamedFiles++
		case FileDiffStatusModified:
			summary.ModifiedFiles++
		}

		// Update line counts
		summary.LinesAdded += fileDiff.Additions
		summary.LinesDeleted += fileDiff.Deletions

		if fileDiff.IsBinary {
			summary.BinaryFiles++
		}

		fileChanges = append(fileChanges, fileChange)
		metrics.FilesAnalyzed++
	}

	summary.TotalFiles = len(fileChanges)
	metrics.ObjectsTraversed = len(changes)

	return summary, fileChanges, nil
}

// compareTreesWithDiff performs tree comparison with diff analysis
func (h *MergePreviewHandler) compareTreesWithDiff(repo *gogit.Repository, sourceTree, targetTree *object.Tree, req *MergePreviewRequest, metrics *PreviewMetrics) (*ChangesSummary, []*FileChange, error) {
	// Use go-git's DiffTree to get actual diff between trees
	changes, err := object.DiffTree(targetTree, sourceTree)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to calculate tree diff: %w", err)
	}

	// Process the changes to extract file information
	summary := &ChangesSummary{
		TotalFiles:    0,
		AddedFiles:    0,
		ModifiedFiles: 0,
		DeletedFiles:  0,
		RenamedFiles:  0,
		LinesAdded:    0,
		LinesDeleted:  0,
		BinaryFiles:   0,
	}

	var fileChanges []*FileChange

	// Process each change using diff.go's logic
	for _, change := range changes {
		// Use diff.go's processChange function to get FileDiff with line changes
		fileDiff, err := h.processChangeFromDiff(change, 3) // Default context lines
		if err != nil {
			// Log error but continue with other files
			fmt.Printf("Error processing change for %s: %v\n", getFileName(change), err)
			continue
		}

		// Create FileChange with embedded FileDiff
		fileChange := &FileChange{
			FileDiff: fileDiff,
		}

		// Add merge-specific content
		fileChange.Content = h.getFileContent(repo, change)

		// Update summary based on file status
		switch fileDiff.Status {
		case FileDiffStatusAdded:
			summary.AddedFiles++
		case FileDiffStatusDeleted:
			summary.DeletedFiles++
		case FileDiffStatusRenamed:
			summary.RenamedFiles++
		case FileDiffStatusModified:
			summary.ModifiedFiles++
		}

		// Update line counts
		summary.LinesAdded += fileDiff.Additions
		summary.LinesDeleted += fileDiff.Deletions

		if fileDiff.IsBinary {
			summary.BinaryFiles++
		}

		fileChanges = append(fileChanges, fileChange)
		metrics.FilesAnalyzed++
	}

	summary.TotalFiles = len(fileChanges)
	metrics.ObjectsTraversed = len(changes)

	return summary, fileChanges, nil
}

// isBinaryFile checks if a file change involves binary content
func (h *MergePreviewHandler) isBinaryFile(change *object.Change) bool {
	// Simplified binary file detection based on file extension
	ext := strings.ToLower(filepath.Ext(change.To.Name))
	binaryExtensions := []string{".png", ".jpg", ".jpeg", ".gif", ".pdf", ".zip", ".tar", ".gz", ".exe", ".bin"}
	for _, binExt := range binaryExtensions {
		if ext == binExt {
			return true
		}
	}
	return false
}

// getFileContent extracts file content from a change
func (h *MergePreviewHandler) getFileContent(repo *gogit.Repository, change *object.Change) *FileContent {
	content := &FileContent{}

	// Get source content (if available)
	if change.From.Name != "" && !change.From.TreeEntry.Hash.IsZero() {
		if blob, err := repo.BlobObject(change.From.TreeEntry.Hash); err == nil {
			if reader, err := blob.Reader(); err == nil {
				defer reader.Close()
				data := make([]byte, 1024*1024) // 1MB limit
				if n, err := reader.Read(data); err == nil || err.Error() == "EOF" {
					content.SourceContent = string(data[:n])
				}
			}
		}
	}

	// Get target content (if available)
	if change.To.Name != "" && !change.To.TreeEntry.Hash.IsZero() {
		if blob, err := repo.BlobObject(change.To.TreeEntry.Hash); err == nil {
			if reader, err := blob.Reader(); err == nil {
				defer reader.Close()
				data := make([]byte, 1024*1024) // 1MB limit
				if n, err := reader.Read(data); err == nil || err.Error() == "EOF" {
					content.TargetContent = string(data[:n])
				}
			}
		}
	}

	// Always return content, even if empty
	return content
}

// analyzeFileContent performs detailed content analysis on a file change
func (h *MergePreviewHandler) analyzeFileContent(fileChange *FileChange) map[string]interface{} {
	analysis := make(map[string]interface{})

	// Basic file information
	analysis["file_type"] = h.detectFileType(fileChange.Path)
	analysis["size_change"] = fileChange.Additions - fileChange.Deletions
	analysis["complexity_change"] = h.analyzeComplexityChange(fileChange)

	// Language-specific analysis
	if h.isCodeFile(fileChange.Path) {
		analysis["code_metrics"] = h.analyzeCodeMetrics(fileChange)
	}

	return analysis
}

// detectPotentialConflicts identifies potential merge conflicts
func (h *MergePreviewHandler) detectPotentialConflicts(fileChange *FileChange) []string {
	var conflicts []string

	// Check for common conflict patterns
	if fileChange.Status == FileDiffStatusModified && fileChange.Additions > 50 {
		conflicts = append(conflicts, "large_modification")
	}

	if fileChange.Status == FileDiffStatusRenamed {
		conflicts = append(conflicts, "file_rename")
	}

	if fileChange.IsBinary {
		conflicts = append(conflicts, "binary_file")
	}

	return conflicts
}

// detectFileType determines the type of file based on extension
func (h *MergePreviewHandler) detectFileType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return "go"
	case ".js", ".ts":
		return "javascript"
	case ".py":
		return "python"
	case ".java":
		return "java"
	case ".cpp", ".c", ".h":
		return "cpp"
	case ".md":
		return "markdown"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	default:
		return "unknown"
	}
}

// isCodeFile checks if a file is a code file
func (h *MergePreviewHandler) isCodeFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	codeExtensions := []string{".go", ".js", ".ts", ".py", ".java", ".cpp", ".c", ".h", ".cs", ".php", ".rb", ".rs"}
	for _, codeExt := range codeExtensions {
		if ext == codeExt {
			return true
		}
	}
	return false
}

// analyzeComplexityChange analyzes the complexity change in a file
func (h *MergePreviewHandler) analyzeComplexityChange(fileChange *FileChange) string {
	netChange := fileChange.Additions - fileChange.Deletions

	if netChange > 100 {
		return "high_complexity_increase"
	} else if netChange > 20 {
		return "moderate_complexity_increase"
	} else if netChange < -50 {
		return "complexity_decrease"
	}
	return "low_complexity_change"
}

// analyzeCodeMetrics analyzes code-specific metrics
func (h *MergePreviewHandler) analyzeCodeMetrics(fileChange *FileChange) map[string]interface{} {
	metrics := make(map[string]interface{})

	// Basic metrics
	metrics["lines_added"] = fileChange.Additions
	metrics["lines_deleted"] = fileChange.Deletions
	metrics["net_change"] = fileChange.Additions - fileChange.Deletions

	// Complexity indicators
	if fileChange.Additions > fileChange.Deletions {
		metrics["trend"] = "growing"
	} else if fileChange.Deletions > fileChange.Additions {
		metrics["trend"] = "shrinking"
	} else {
		metrics["trend"] = "stable"
	}

	return metrics
}

// compareTreesFull performs comprehensive tree comparison with content analysis
func (h *MergePreviewHandler) compareTreesFull(repo *gogit.Repository, sourceTree, targetTree *object.Tree, req *MergePreviewRequest, metrics *PreviewMetrics) (*ChangesSummary, []*FileChange, error) {
	// Use the diff-based comparison as the foundation
	summary, fileChanges, err := h.compareTreesWithDiff(repo, sourceTree, targetTree, req, metrics)
	if err != nil {
		return nil, nil, err
	}

	// Add additional content analysis for each file change
	for _, fileChange := range fileChanges {
		// Add detailed content analysis
		fileChange.ContentAnalysis = h.analyzeFileContent(fileChange)

		// Add conflict detection for merge scenarios
		fileChange.PotentialConflicts = h.detectPotentialConflicts(fileChange)
	}

	return summary, fileChanges, nil
}

// findMergeBase finds the common ancestor commit
func (h *MergePreviewHandler) findMergeBase(repo *gogit.Repository, sourceHash, targetHash plumbing.Hash) (*object.Commit, error) {
	// Get source and target commits
	sourceCommit, err := repo.CommitObject(sourceHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get source commit: %w", err)
	}

	targetCommit, err := repo.CommitObject(targetHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get target commit: %w", err)
	}

	// Find merge base using go-git's merge base algorithm
	mergeBases, err := sourceCommit.MergeBase(targetCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to find merge base: %w", err)
	}

	if len(mergeBases) == 0 {
		return nil, fmt.Errorf("no common ancestor found between commits")
	}

	// Return the first merge base (most recent common ancestor)
	return mergeBases[0], nil
}

// generateRecommendations generates actionable recommendations based on analysis
func (h *MergePreviewHandler) generateRecommendations(conflictAnalysis *git.MergeAnalysisResult, changesSummary *ChangesSummary, req *MergePreviewRequest) []string {
	var recommendations []string

	if conflictAnalysis.IsFastForward {
		recommendations = append(recommendations, "This merge can be performed as a fast-forward merge for optimal performance")
	}

	if conflictAnalysis.HasConflicts {
		if conflictAnalysis.AutoResolvable > 0 {
			recommendations = append(recommendations, fmt.Sprintf("%d conflicts can be automatically resolved", conflictAnalysis.AutoResolvable))
		}
		if conflictAnalysis.ConflictCount-conflictAnalysis.AutoResolvable > 0 {
			recommendations = append(recommendations, "Manual conflict resolution will be required")
		}
		recommendations = append(recommendations, "Consider using a different merge strategy to reduce conflicts")
	}

	if changesSummary != nil && changesSummary.TotalFiles > 1000 {
		recommendations = append(recommendations, "Large number of files changed - consider breaking into smaller merges")
	}

	if changesSummary != nil && changesSummary.BinaryFiles > 0 {
		recommendations = append(recommendations, fmt.Sprintf("%d binary files detected - ensure proper Git LFS configuration", changesSummary.BinaryFiles))
	}

	if len(recommendations) == 0 {
		recommendations = append(recommendations, "Merge can proceed without issues")
	}

	return recommendations
}

// generateWarnings generates warnings based on analysis
func (h *MergePreviewHandler) generateWarnings(conflictAnalysis *git.MergeAnalysisResult, changesSummary *ChangesSummary, req *MergePreviewRequest) []string {
	var warnings []string

	if conflictAnalysis.HasConflicts && conflictAnalysis.ConflictCount > 50 {
		warnings = append(warnings, "High number of conflicts detected - merge may be complex")
	}

	if changesSummary != nil && changesSummary.TotalFiles > 5000 {
		warnings = append(warnings, "Very large merge - performance may be impacted")
	}

	if req.MaxFileSize < 1024*1024 {
		warnings = append(warnings, "Low max_file_size may limit content analysis accuracy")
	}

	if req.MaxFiles < 100 {
		warnings = append(warnings, "Low max_files limit may result in incomplete analysis")
	}

	return warnings
}

// commitToInfo converts a commit object to CommitInfo

// processChangeFromDiff wraps diff.go's processChange function for reuse
func (h *MergePreviewHandler) processChangeFromDiff(change *object.Change, contextLines int) (FileDiff, error) {
	// Use the same logic as diff.go's processChange
	fileName := getFileName(change)
	fileDiff := FileDiff{
		Path:     fileName,
		Metadata: getFileMetadata(change),
	}

	// 변경 상태 결정
	switch {
	case change.From.Name == "" && change.To.Name != "":
		fileDiff.Status = FileDiffStatusAdded
	case change.From.Name != "" && change.To.Name == "":
		fileDiff.Status = FileDiffStatusDeleted
	case change.From.Name != change.To.Name:
		fileDiff.Status = FileDiffStatusRenamed
		fileDiff.OldPath = change.From.Name
	default:
		fileDiff.Status = FileDiffStatusModified
	}

	// 라인 레벨 변경을 위한 patch 정보 가져오기
	patch, err := change.Patch()
	if err != nil {
		return fileDiff, fmt.Errorf("failed to get patch: %w", err)
	}

	// patch에서 실제 추가/삭제된 라인 수를 정확히 계산 (FilePatches 사용)
	additions, deletions := h.countPatchLines(patch)
	fileDiff.Additions = additions
	fileDiff.Deletions = deletions

	// patch 콘텐츠와 파일명을 모두 사용한 향상된 바이너리 파일 감지
	patchText := patch.String()
	fileDiff.IsBinary = isBinaryFile(patchText, fileName)

	// 바이너리가 아니고 contextLines > 0이면 라인 레벨 변경 생성
	if !fileDiff.IsBinary && contextLines > 0 {
		lines := splitLines(patchText)
		lineChanges := GenerateLineChanges(lines, contextLines)
		fileDiff.Changes = lineChanges
	}

	return fileDiff, nil
}

// countPatchLines는 patch에서 실제 추가/삭제된 라인 수를 정확히 계산합니다
// diff 헤더(+++ b/file, --- a/file 등)는 제외하고 실제 변경 라인만 카운트합니다
func (h *MergePreviewHandler) countPatchLines(patch *object.Patch) (additions, deletions int) {
	for _, filePatch := range patch.FilePatches() {
		for _, chunk := range filePatch.Chunks() {
			// chunk.Type()의 반환값:
			// 0 = Context (변경 없음, 컨텍스트 라인)
			// 1 = Add (추가된 라인)
			// 2 = Delete (삭제된 라인)
			switch chunk.Type() {
			case 1: // Add
				// Content에서 실제 라인 수 계산
				content := chunk.Content()
				// 개행 문자로 분할하여 라인 수 계산
				lines := strings.Split(content, "\n")
				// 마지막이 빈 문자열이면(개행으로 끝난 경우) 제거
				if len(lines) > 0 && lines[len(lines)-1] == "" {
					lines = lines[:len(lines)-1]
				}
				additions += len(lines)
			case 2: // Delete
				content := chunk.Content()
				lines := strings.Split(content, "\n")
				if len(lines) > 0 && lines[len(lines)-1] == "" {
					lines = lines[:len(lines)-1]
				}
				deletions += len(lines)
			}
		}
	}
	return additions, deletions
}

// calculateMergeStatistics calculates GitHub PR style statistics for merge preview
func (h *MergePreviewHandler) calculateMergeStatistics(ctx context.Context, repo *gogit.Repository, sourceHash, targetHash plumbing.Hash) (*CommitStats, []string, []CommitsByDate, error) {
	// Get source and target commits
	sourceCommit, err := repo.CommitObject(sourceHash)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get source commit: %w", err)
	}

	targetCommit, err := repo.CommitObject(targetHash)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get target commit: %w", err)
	}

	// Find merge base - handle case where no common ancestor exists
	mergeBase, err := h.findMergeBase(repo, sourceHash, targetHash)
	if err != nil {
		// If no common ancestor found, return empty statistics rather than failing
		if strings.Contains(err.Error(), "no common ancestor found") {
			h.logger.Debug("No common ancestor found, returning empty statistics")
			return &CommitStats{Ahead: 0, Behind: 0}, []string{}, []CommitsByDate{}, nil
		}
		return nil, nil, nil, fmt.Errorf("failed to find merge base: %w", err)
	}

	// Calculate commit statistics (ahead/behind)
	commitStats, err := h.calculateCommitCounts(repo, sourceCommit, targetCommit, mergeBase)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to calculate commit counts: %w", err)
	}

	// Get contributors from source branch commits (simple approach)
	contributors, err := h.getContributorsSimple(repo, sourceCommit)
	if err != nil {
		h.logger.Warn("Failed to get contributors, using empty list", zap.Error(err))
		contributors = []string{}
	}

	// Get commits by date (from merge base to source commit) - optional
	commitsByDate, err := h.getCommitsByDate(repo, mergeBase, sourceCommit)
	if err != nil {
		h.logger.Warn("Failed to get commits by date, using empty list", zap.Error(err))
		commitsByDate = []CommitsByDate{}
	}

	return commitStats, contributors, commitsByDate, nil
}

// calculateCommitCounts calculates ahead/behind commit counts (target 기준으로 source가 얼마나 앞서 있는지)
func (h *MergePreviewHandler) calculateCommitCounts(repo *gogit.Repository, sourceCommit, targetCommit, mergeBase *object.Commit) (*CommitStats, error) {
	// Calculate commits ahead (from merge base to source) - source가 target 기준으로 얼마나 앞서 있는지
	ahead, err := h.countCommitsBetween(repo, mergeBase, sourceCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to count ahead commits: %w", err)
	}

	// Calculate commits behind (from merge base to target) - target이 source 기준으로 얼마나 뒤처져 있는지
	behind, err := h.countCommitsBetween(repo, mergeBase, targetCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to count behind commits: %w", err)
	}

	return &CommitStats{
		Ahead:  ahead,  // source가 target보다 앞서 있는 커밋 수
		Behind: behind, // target이 source보다 뒤처져 있는 커밋 수
	}, nil
}

// getContributorsSimple extracts contributors from a branch's commit history (simple approach)
func (h *MergePreviewHandler) getContributorsSimple(repo *gogit.Repository, headCommit *object.Commit) ([]string, error) {
	// Get all commits from the head commit
	commitIter, err := repo.Log(&gogit.LogOptions{
		From: headCommit.Hash,
	})
	if err != nil {
		return []string{}, nil // Return empty on error
	}
	defer commitIter.Close()

	// Track unique contributors
	contributorSet := make(map[string]bool)

	err = commitIter.ForEach(func(commit *object.Commit) error {
		// Add author email if not empty
		if commit.Author.Email != "" {
			contributorSet[commit.Author.Email] = true
		}
		return nil
	})

	if err != nil {
		return []string{}, nil // Return empty on error
	}

	// Convert set to slice
	var contributors []string
	for contributor := range contributorSet {
		contributors = append(contributors, contributor)
	}

	return contributors, nil
}

// getCommitsByDate groups commits by date (reused from diff.go logic)
func (h *MergePreviewHandler) getCommitsByDate(repo *gogit.Repository, from, to *object.Commit) ([]CommitsByDate, error) {
	if from.Hash == to.Hash {
		return []CommitsByDate{}, nil
	}

	// Get commit log from 'to' commit
	commitIter, err := repo.Log(&gogit.LogOptions{
		From: to.Hash,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get commit log: %w", err)
	}
	defer commitIter.Close()

	// Group commits by date
	commitsByDate := make(map[string][]CommitMetadata)
	fromHash := from.Hash.String()

	err = commitIter.ForEach(func(commit *object.Commit) error {
		if commit.Hash.String() == fromHash {
			// Reached 'from' commit, stop iteration
			return fmt.Errorf("stop iteration")
		}

		// Format date as YYYY-MM-DD
		date := commit.Author.When.Format("2006-01-02")

		// Create commit metadata
		commitMeta := CommitMetadata{
			SHA:     commit.Hash.String(),
			Author:  commit.Author.Name,
			Email:   commit.Author.Email,
			Date:    commit.Author.When.Format("2006-01-02 15:04:05"),
			Message: commit.Message,
			Parents: make([]string, len(commit.ParentHashes)),
		}

		// Add parent hashes
		for i, parentHash := range commit.ParentHashes {
			commitMeta.Parents[i] = parentHash.String()
		}

		// Group by date
		commitsByDate[date] = append(commitsByDate[date], commitMeta)
		return nil
	})

	// Check if we stopped at the 'from' commit (normal case)
	if err != nil && err.Error() != "stop iteration" {
		return nil, fmt.Errorf("failed to iterate commits: %w", err)
	}

	// Convert map to slice and sort by date
	var result []CommitsByDate
	for date, commits := range commitsByDate {
		result = append(result, CommitsByDate{
			Date:    date,
			Commits: commits,
		})
	}

	// Sort by date (newest first)
	sort.Slice(result, func(i, j int) bool {
		return result[i].Date > result[j].Date
	})

	return result, nil
}

// countCommitsBetween counts commits between two commits (reused from diff.go logic)
func (h *MergePreviewHandler) countCommitsBetween(repo *gogit.Repository, from, to *object.Commit) (int, error) {
	if from.Hash == to.Hash {
		return 0, nil
	}

	// Get commit log from 'to' commit
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
			// Reached 'from' commit, stop iteration
			return fmt.Errorf("stop iteration")
		}
		count++
		return nil
	})

	// Check if we stopped at the 'from' commit (normal case)
	if err != nil && err.Error() == "stop iteration" {
		return count, nil
	}

	// If no error, we counted all commits to root
	if err == nil {
		return count, nil
	}

	return 0, fmt.Errorf("failed to iterate commits: %w", err)
}
