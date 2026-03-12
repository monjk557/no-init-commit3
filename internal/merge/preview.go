package merge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/collabops/git-executor/internal/diffs"
)

// validateMergePreviewRequest validates the merge preview request parameters
func (h *MergePreviewHandler) validateMergePreviewRequest(req *MergePreviewRequest) (int, error) {
	if req.SourceRef == "" {
		return http.StatusBadRequest, errors.New("source_ref is required")
	}
	if req.TargetRef == "" {
		return http.StatusBadRequest, errors.New("target_ref is required")
	}

	switch req.Strategy {
	case MergeStrategyRecursive, MergeStrategyResolve, MergeStrategyOurs, MergeStrategyTheirs, MergeStrategySubtree:
		// Valid strategy
	default:
		return http.StatusBadRequest, fmt.Errorf("invalid merge strategy: %s", req.Strategy)
	}

	if req.MaxFileSize < 0 {
		return http.StatusBadRequest, errors.New("max_file_size cannot be negative")
	}
	if req.MaxFileSize > 100*1024*1024 {
		return http.StatusBadRequest, errors.New("max_file_size cannot exceed 100MB")
	}
	if req.MaxFiles < 0 {
		return http.StatusBadRequest, errors.New("max_files cannot be negative")
	}
	if req.MaxFiles > 10000 {
		return http.StatusBadRequest, errors.New("max_files cannot exceed 10000")
	}

	switch req.CompareStrategy {
	case "tree", "diff", "full":
		// Valid
	default:
		return http.StatusBadRequest, fmt.Errorf("invalid compare strategy: %s (must be tree, diff, or full)", req.CompareStrategy)
	}

	return http.StatusOK, nil
}

// performMergePreview performs comprehensive merge preview analysis using native git.
func (h *MergePreviewHandler) performMergePreview(ctx context.Context, repoPath string, sourceHash, targetHash string, req *MergePreviewRequest, startTime time.Time) (*MergePreviewResponse, error) {
	metrics := &PreviewMetrics{
		FilesAnalyzed:    0,
		ObjectsTraversed: 0,
	}

	// Phase 1: Conflict Analysis
	conflictStartTime := time.Now()
	conflictAnalysis, err := h.analyzeConflicts(ctx, repoPath, sourceHash, targetHash, req)
	if err != nil {
		return nil, fmt.Errorf("conflict analysis failed: %w", err)
	}
	metrics.ConflictAnalysisTime = time.Since(conflictStartTime)

	// Phase 2: Tree Comparison via native git diff-tree
	treeStartTime := time.Now()
	changesSummary, filesChanged, err := h.compareRepositoryTrees(ctx, repoPath, sourceHash, targetHash, req, metrics)
	if err != nil {
		return nil, fmt.Errorf("tree comparison failed: %w", err)
	}
	metrics.TreeComparisonTime = time.Since(treeStartTime)

	// Phase 3: Recommendations and warnings
	recommendations := h.generatePreviewRecommendations(conflictAnalysis, changesSummary)
	warnings := h.generatePreviewWarnings(conflictAnalysis, changesSummary, req)

	// Phase 4: Merge base info
	var mergeBaseInfo *CommitInfo
	if conflictAnalysis != nil && !conflictAnalysis.IsFastForward {
		mbHash, err := gitMergeBase(ctx, repoPath, sourceHash, targetHash)
		if err == nil {
			mbCommit, err := gitCatFileCommit(ctx, repoPath, mbHash)
			if err == nil {
				mergeBaseInfo = nativeCommitToInfo(mbCommit)
			}
		}
	}

	metrics.AnalysisTime = time.Since(startTime)

	// Phase 5: GitHub PR style statistics
	commitStats, contributors, commitsByDate, err := h.calculateMergeStatistics(ctx, repoPath, sourceHash, targetHash)
	if err != nil {
		h.logger.Warn("Failed to calculate merge statistics", zap.Error(err))
		commitStats = &CommitStats{Ahead: 0, Behind: 0}
		contributors = []string{}
		commitsByDate = []CommitsByDate{}
	}

	filesCount := 0
	if changesSummary != nil {
		filesCount = changesSummary.TotalFiles
	}

	countSummary := &CountSummary{
		Commits:      commitStats.Ahead,
		Files:        filesCount,
		Contributors: len(contributors),
	}

	response := &MergePreviewResponse{
		CanMerge:           !conflictAnalysis.HasConflicts,
		IsFastForward:      conflictAnalysis.IsFastForward,
		HasConflicts:       conflictAnalysis.HasConflicts,
		ConflictCount:      conflictAnalysis.ConflictCount,
		Strategy:           req.Strategy,
		RecommendedActions: recommendations,
		MergeBase:          mergeBaseInfo,
		ConflictSummary:    conflictAnalysis,
		ChangesSummary:     changesSummary,
		FilesChanged:       filesChanged,
		PerformanceMetrics: metrics,
		Warnings:           warnings,
		Commits:            commitStats,
		Contributors:       contributors,
		CommitsByDate:      commitsByDate,
		CountSummary:       countSummary,
	}

	return response, nil
}

// analyzeConflicts performs conflict analysis using the ConflictDetector.
func (h *MergePreviewHandler) analyzeConflicts(ctx context.Context, repoPath string, sourceHash, targetHash string, req *MergePreviewRequest) (*MergeAnalysisResult, error) {
	analysisReq := &MergeAnalysisRequest{
		RepoPath:       repoPath,
		SourceRef:      sourceHash,
		TargetRef:      targetHash,
		Strategy:       MergeStrategy(req.Strategy),
		AutoResolve:    false,
		IncludeContent: req.IncludeContent,
		MaxFileSize:    req.MaxFileSize,
	}

	result, err := h.conflictDetector.AnalyzeMerge(ctx, analysisReq)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze conflicts: %w", err)
	}

	return result, nil
}

// compareRepositoryTrees compares source and target trees using native git diff-tree.
func (h *MergePreviewHandler) compareRepositoryTrees(ctx context.Context, repoPath string, sourceHash, targetHash string, req *MergePreviewRequest, metrics *PreviewMetrics) (*ChangesSummary, []*FileChange, error) {
	// Get diff between target and source trees using native git
	diffEntries, err := gitDiffTree(ctx, repoPath, targetHash, sourceHash)
	if err != nil {
		return nil, nil, fmt.Errorf("diff-tree failed: %w", err)
	}

	summary := &ChangesSummary{}

	maxFiles := req.MaxFiles
	if maxFiles == 0 {
		maxFiles = 10000
	}

	fileChanges := make([]*FileChange, 0, len(diffEntries))

	for i, de := range diffEntries {
		if i >= maxFiles {
			break
		}

		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		default:
		}

		filePath := de.Path
		if de.NewPath != "" {
			filePath = de.NewPath
		}

		status := diffEntryToStatus(de.Status)

		fileDiff := diffs.FileDiff{
			Path:   filePath,
			Status: status,
		}

		if de.Status == "R" || de.Status == "C" {
			fileDiff.OldPath = de.Path
			fileDiff.Path = de.NewPath
		}

		fileDiff.IsBinary = isBinaryByExtension(filePath)

		fileChange := &FileChange{
			FileDiff: fileDiff,
		}

		// Fetch content if requested and file is not binary
		if req.IncludeContent && !fileDiff.IsBinary {
			fileChange.Content = h.getFileContentNative(ctx, repoPath, &de)
		}

		switch status {
		case diffs.FileDiffStatusAdded:
			summary.AddedFiles++
		case diffs.FileDiffStatusDeleted:
			summary.DeletedFiles++
		case diffs.FileDiffStatusRenamed:
			summary.RenamedFiles++
		case diffs.FileDiffStatusModified:
			summary.ModifiedFiles++
		}

		if fileDiff.IsBinary {
			summary.BinaryFiles++
		}

		fileChanges = append(fileChanges, fileChange)
		metrics.FilesAnalyzed++
	}

	// LoadStatus 할당: 프론트엔드 diff 상태 구분 지원
	for i := range fileChanges {
		if fileChanges[i].IsBinary {
			fileChanges[i].LoadStatus = diffs.FileLoadStatusBinary
		} else {
			fileChanges[i].LoadStatus = diffs.FileLoadStatusCollapsed
		}
	}

	summary.TotalFiles = len(fileChanges)
	metrics.ObjectsTraversed = len(diffEntries)

	return summary, fileChanges, nil
}

// getFileContentNative reads file content from git objects.
func (h *MergePreviewHandler) getFileContentNative(ctx context.Context, repoPath string, de *DiffEntry) *FileContent {
	content := &FileContent{}
	zeroHash := strings.Repeat("0", 40)

	if de.SrcHash != "" && de.SrcHash != zeroHash {
		if data, err := gitCatFileBlob(ctx, repoPath, de.SrcHash); err == nil {
			if len(data) <= 1024*1024 { // 1MB limit
				content.SourceContent = string(data)
			} else {
				content.IsTruncated = true
			}
		}
	}

	if de.DstHash != "" && de.DstHash != zeroHash {
		if data, err := gitCatFileBlob(ctx, repoPath, de.DstHash); err == nil {
			if len(data) <= 1024*1024 {
				content.TargetContent = string(data)
			} else {
				content.IsTruncated = true
			}
		}
	}

	return content
}

// diffEntryToStatus converts a git diff status letter to FileDiffStatus.
func diffEntryToStatus(status string) diffs.FileDiffStatus {
	switch status {
	case "A":
		return diffs.FileDiffStatusAdded
	case "D":
		return diffs.FileDiffStatusDeleted
	case "R", "C":
		return diffs.FileDiffStatusRenamed
	case "M", "T":
		return diffs.FileDiffStatusModified
	default:
		return diffs.FileDiffStatusModified
	}
}

// isBinaryByExtension checks if a file is binary based on extension.
func isBinaryByExtension(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	binaryExtensions := []string{
		".png", ".jpg", ".jpeg", ".gif", ".bmp", ".ico", ".webp",
		".pdf", ".zip", ".tar", ".gz", ".bz2", ".xz", ".7z",
		".exe", ".bin", ".dll", ".so", ".dylib",
		".woff", ".woff2", ".ttf", ".eot",
		".mp3", ".mp4", ".avi", ".mov", ".wav",
	}
	for _, binExt := range binaryExtensions {
		if ext == binExt {
			return true
		}
	}
	return false
}

// generatePreviewRecommendations generates actionable recommendations.
func (h *MergePreviewHandler) generatePreviewRecommendations(conflictAnalysis *MergeAnalysisResult, changesSummary *ChangesSummary) []string {
	var recommendations []string

	if conflictAnalysis.IsFastForward {
		recommendations = append(recommendations, "This merge can be performed as a fast-forward merge for optimal performance")
	}

	if conflictAnalysis.HasConflicts {
		recommendations = append(recommendations,
			fmt.Sprintf("All %d conflicts require manual resolution", conflictAnalysis.ConflictCount))
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

// generatePreviewWarnings generates warnings based on analysis.
func (h *MergePreviewHandler) generatePreviewWarnings(conflictAnalysis *MergeAnalysisResult, changesSummary *ChangesSummary, req *MergePreviewRequest) []string {
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

// calculateMergeStatistics calculates GitHub PR style statistics using native git.
func (h *MergePreviewHandler) calculateMergeStatistics(ctx context.Context, repoPath string, sourceHash, targetHash string) (*CommitStats, []string, []CommitsByDate, error) {
	mergeBaseHash, err := gitMergeBase(ctx, repoPath, sourceHash, targetHash)
	if err != nil {
		if strings.Contains(err.Error(), "no merge base") {
			return &CommitStats{Ahead: 0, Behind: 0}, []string{}, []CommitsByDate{}, nil
		}
		return nil, nil, nil, fmt.Errorf("failed to find merge base: %w", err)
	}

	// Commits ahead: mergeBase..source
	aheadHashes, err := gitRevList(ctx, repoPath, mergeBaseHash, sourceHash)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to count ahead: %w", err)
	}
	// Commits behind: mergeBase..target
	behindHashes, err := gitRevList(ctx, repoPath, mergeBaseHash, targetHash)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to count behind: %w", err)
	}

	commitStats := &CommitStats{
		Ahead:  len(aheadHashes),
		Behind: len(behindHashes),
	}

	// Get contributors from ahead commits
	contributorSet := make(map[string]bool)
	commitsByDateMap := make(map[string][]diffs.CommitMetadata)

	for _, hash := range aheadHashes {
		commit, err := gitCatFileCommit(ctx, repoPath, hash)
		if err != nil {
			continue
		}
		if commit.Author.Email != "" {
			contributorSet[commit.Author.Email] = true
		}

		date := commit.Author.When.Format("2006-01-02")
		commitsByDateMap[date] = append(commitsByDateMap[date], diffs.CommitMetadata{
			SHA:     commit.Hash,
			Author:  commit.Author.Name,
			Email:   commit.Author.Email,
			Date:    commit.Author.When.Format("2006-01-02 15:04:05"),
			Message: commit.Message,
			Parents: commit.ParentHashes,
		})
	}

	var contributors []string
	for c := range contributorSet {
		contributors = append(contributors, c)
	}

	var commitsByDate []CommitsByDate
	for date, commits := range commitsByDateMap {
		commitsByDate = append(commitsByDate, CommitsByDate{
			Date:    date,
			Commits: commits,
		})
	}

	sort.Slice(commitsByDate, func(i, j int) bool {
		return commitsByDate[i].Date > commitsByDate[j].Date
	})

	return commitStats, contributors, commitsByDate, nil
}

// Helper methods for content analysis (used in "full" compare strategy)

func (h *MergePreviewHandler) analyzeFileContent(fileChange *FileChange) map[string]interface{} {
	analysis := make(map[string]interface{})
	analysis["file_type"] = detectFileType(fileChange.Path)
	analysis["size_change"] = fileChange.Additions - fileChange.Deletions
	analysis["complexity_change"] = analyzeComplexityChange(fileChange)

	if isCodeFile(fileChange.Path) {
		analysis["code_metrics"] = map[string]interface{}{
			"lines_added":   fileChange.Additions,
			"lines_deleted": fileChange.Deletions,
			"net_change":    fileChange.Additions - fileChange.Deletions,
		}
	}

	return analysis
}

func (h *MergePreviewHandler) detectPotentialConflicts(fileChange *FileChange) []string {
	var conflicts []string
	if fileChange.Status == diffs.FileDiffStatusModified && fileChange.Additions > 50 {
		conflicts = append(conflicts, "large_modification")
	}
	if fileChange.Status == diffs.FileDiffStatusRenamed {
		conflicts = append(conflicts, "file_rename")
	}
	if fileChange.IsBinary {
		conflicts = append(conflicts, "binary_file")
	}
	return conflicts
}

func detectFileType(path string) string {
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

func isCodeFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	codeExtensions := []string{".go", ".js", ".ts", ".py", ".java", ".cpp", ".c", ".h", ".cs", ".php", ".rb", ".rs"}
	for _, codeExt := range codeExtensions {
		if ext == codeExt {
			return true
		}
	}
	return false
}

func analyzeComplexityChange(fileChange *FileChange) string {
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
