package merge

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"go.uber.org/zap"
)

// ConflictDetector는 Merge 충돌 감지 및 분석 기능을 제공합니다.
type ConflictDetector struct {
	pathConfig *repositories.PathConfig
	logger     *logging.Logger
}

// NewConflictDetector는 새로운 충돌 감지기 인스턴스를 생성합니다.
func NewConflictDetector(pathConfig *repositories.PathConfig, logger *logging.Logger) *ConflictDetector {
	return &ConflictDetector{
		pathConfig: pathConfig,
		logger:     logger,
	}
}

// AnalyzeMerge performs comprehensive merge conflict analysis using native git.
func (cd *ConflictDetector) AnalyzeMerge(ctx context.Context, req *MergeAnalysisRequest) (*MergeAnalysisResult, error) {
	cd.logger.Info("Starting merge conflict analysis",
		zap.String("owner", req.Owner),
		zap.String("repo", req.Repo),
		zap.String("source_ref", req.SourceRef),
		zap.String("target_ref", req.TargetRef),
		zap.String("strategy", string(req.Strategy)))

	// Determine repo path
	repoPath := req.RepoPath
	if repoPath == "" {
		repoPath = cd.pathConfig.FormatRepoPath(req.Owner, req.Repo)
	}

	// Resolve source and target commits
	sourceHash, err := gitRevParse(ctx, repoPath, req.SourceRef)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve source ref %s: %w", req.SourceRef, err)
	}

	targetHash, err := gitRevParse(ctx, repoPath, req.TargetRef)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve target ref %s: %w", req.TargetRef, err)
	}

	// Read commit objects for info
	sourceCommit, err := gitCatFileCommit(ctx, repoPath, sourceHash)
	if err != nil {
		return nil, fmt.Errorf("failed to read source commit: %w", err)
	}
	targetCommit, err := gitCatFileCommit(ctx, repoPath, targetHash)
	if err != nil {
		return nil, fmt.Errorf("failed to read target commit: %w", err)
	}

	cd.logger.Debug("Resolved commits",
		zap.String("source_commit", sourceHash),
		zap.String("target_commit", targetHash))

	// Check for fast-forward merge
	isFastForward, err := gitMergeBaseIsAncestor(ctx, repoPath, targetHash, sourceHash)
	if err != nil {
		return nil, fmt.Errorf("failed to check fast-forward: %w", err)
	}

	if isFastForward {
		cd.logger.Info("Fast-forward merge detected")
		return &MergeAnalysisResult{
			CanMerge:          true,
			HasConflicts:      false,
			IsFastForward:     true,
			ConflictCount:     0,
			FileConflicts:     []FileConflict{},
			SourceCommit:      nativeCommitToInfo(sourceCommit),
			TargetCommit:      nativeCommitToInfo(targetCommit),
			Strategy:          req.Strategy,
			AutoResolvable:    0,
			SeverityBreakdown: make(map[ConflictSeverity]int),
			EstimatedTime:     0,
			Recommendations:   []string{"Fast-forward merge - no conflicts expected"},
		}, nil
	}

	// Find merge base
	mergeBaseHash, err := gitMergeBase(ctx, repoPath, sourceHash, targetHash)
	if err != nil {
		return nil, fmt.Errorf("failed to find merge base: %w", err)
	}

	mergeBaseCommit, err := gitCatFileCommit(ctx, repoPath, mergeBaseHash)
	if err != nil {
		return nil, fmt.Errorf("failed to read merge base commit: %w", err)
	}

	cd.logger.Debug("Found merge base", zap.String("merge_base", mergeBaseHash))

	// Use git merge-tree --write-tree to detect conflicts
	conflicts, err := cd.analyzeThreeWayMerge(ctx, repoPath, mergeBaseHash, sourceHash, targetHash, req)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze three-way merge: %w", err)
	}

	// Calculate summary statistics
	// Industry standard: conflicts exist → cannot merge. No auto-resolve.
	hasConflicts := len(conflicts) > 0
	result := &MergeAnalysisResult{
		CanMerge:          !hasConflicts,
		HasConflicts:      hasConflicts,
		IsFastForward:     false,
		ConflictCount:     len(conflicts),
		FileConflicts:     conflicts,
		MergeBase:         nativeCommitToInfo(mergeBaseCommit),
		SourceCommit:      nativeCommitToInfo(sourceCommit),
		TargetCommit:      nativeCommitToInfo(targetCommit),
		Strategy:          req.Strategy,
		AutoResolvable:    0,
		SeverityBreakdown: cd.calculateSeverityBreakdown(conflicts),
		EstimatedTime:     cd.estimateResolutionTime(conflicts),
		Recommendations:   cd.generateRecommendations(conflicts, req.Strategy),
	}

	cd.logger.Info("Merge analysis completed",
		zap.Int("total_conflicts", result.ConflictCount),
		zap.Bool("can_merge", result.CanMerge),
		zap.Duration("estimated_time", result.EstimatedTime))

	return result, nil
}

// analyzeThreeWayMerge uses native git to perform three-way merge analysis.
// It uses two approaches:
//  1. git merge-tree --write-tree for conflict detection
//  2. git diff-tree for change enumeration
func (cd *ConflictDetector) analyzeThreeWayMerge(ctx context.Context, repoPath, baseHash, sourceHash, targetHash string, req *MergeAnalysisRequest) ([]FileConflict, error) {
	// Use git merge-tree to detect actual conflicts
	mergeResult, err := gitMergeTree(ctx, repoPath, baseHash, targetHash, sourceHash)
	if err != nil {
		return nil, fmt.Errorf("merge-tree analysis failed: %w", err)
	}

	if !mergeResult.HasConflicts {
		return nil, nil
	}

	// We have conflicts — build detailed FileConflict info by comparing trees
	var conflicts []FileConflict

	// Get diff from base to source and base to target
	sourceDiffs, err := gitDiffTree(ctx, repoPath, baseHash, sourceHash)
	if err != nil {
		return nil, fmt.Errorf("diff-tree base..source: %w", err)
	}
	targetDiffs, err := gitDiffTree(ctx, repoPath, baseHash, targetHash)
	if err != nil {
		return nil, fmt.Errorf("diff-tree base..target: %w", err)
	}

	// Build maps of changed files
	sourceChanges := make(map[string]*DiffEntry)
	for i := range sourceDiffs {
		sourceChanges[sourceDiffs[i].Path] = &sourceDiffs[i]
	}
	targetChanges := make(map[string]*DiffEntry)
	for i := range targetDiffs {
		targetChanges[targetDiffs[i].Path] = &targetDiffs[i]
	}

	// Files changed in both branches are potential conflicts
	// The merge-tree output tells us which are actual conflicts
	conflictPaths := make(map[string]bool)
	for _, c := range mergeResult.Conflicts {
		if c.Path != "" {
			conflictPaths[c.Path] = true
		}
	}

	// For each conflict path, create a FileConflict
	for path := range conflictPaths {
		sourceDiff := sourceChanges[path]
		targetDiff := targetChanges[path]

		conflict := cd.buildFileConflict(ctx, repoPath, path, sourceDiff, targetDiff, req)
		if conflict != nil {
			conflicts = append(conflicts, *conflict)
		}
	}

	// Also check for files changed in both branches not caught by path extraction
	if len(conflicts) == 0 && mergeResult.HasConflicts {
		for path, se := range sourceChanges {
			if te, ok := targetChanges[path]; ok {
				// Both branches modified same file
				if se.DstHash != te.DstHash {
					conflict := cd.buildFileConflict(ctx, repoPath, path, se, te, req)
					if conflict != nil {
						conflicts = append(conflicts, *conflict)
					}
				}
			}
		}
	}

	return conflicts, nil
}

// buildFileConflict creates a FileConflict from diff entries.
func (cd *ConflictDetector) buildFileConflict(ctx context.Context, repoPath, path string, sourceDiff, targetDiff *DiffEntry, req *MergeAnalysisRequest) *FileConflict {
	conflict := &FileConflict{
		FilePath: path,
		Regions:  []ConflictRegion{},
		Metadata: make(map[string]interface{}),
	}

	// Determine file existence and build versions
	sourceExists := sourceDiff != nil && sourceDiff.Status != "D"
	targetExists := targetDiff != nil && targetDiff.Status != "D"
	baseExists := true // if we have diffs from base, base existed for modified/deleted files
	if sourceDiff != nil && sourceDiff.Status == "A" {
		baseExists = false
	}
	if targetDiff != nil && targetDiff.Status == "A" {
		baseExists = false
	}

	conflict.OurVersion = buildFileVersionFromDiff(sourceDiff, true)
	conflict.TheirVersion = buildFileVersionFromDiff(targetDiff, false)
	conflict.BaseVersion = buildBaseFileVersionFromDiff(sourceDiff, targetDiff)

	// Determine conflict type
	conflict.ConflictType = cd.determineConflictType(baseExists, sourceExists, targetExists, sourceDiff, targetDiff)
	conflict.Severity = cd.calculateConflictSeverity(conflict.ConflictType, conflict.OurVersion, conflict.TheirVersion)

	// Analyze content conflicts if requested
	if req.IncludeContent && conflict.ConflictType == ConflictTypeContent {
		regions, err := cd.analyzeContentConflicts(ctx, repoPath, sourceDiff, targetDiff, req.MaxFileSize)
		if err != nil {
			cd.logger.Warn("Failed to analyze content conflicts",
				zap.String("path", path), zap.Error(err))
		} else {
			conflict.Regions = regions
		}
	}

	// All conflicts require manual human resolution (no auto-resolve)
	conflict.AutoResolvable = false

	return conflict
}

// buildFileVersionFromDiff builds a FileVersion from a DiffEntry.
func buildFileVersionFromDiff(diff *DiffEntry, isSource bool) *FileVersion {
	if diff == nil {
		return &FileVersion{Exists: false}
	}

	hash := diff.DstHash
	modeStr := diff.DstMode
	exists := diff.Status != "D"

	if !exists {
		return &FileVersion{Exists: false}
	}

	mode, _ := strconv.ParseUint(modeStr, 8, 32)
	return &FileVersion{
		Hash:   hash,
		Mode:   uint32(mode),
		Exists: true,
	}
}

// buildBaseFileVersionFromDiff extracts base version info from diff entries.
func buildBaseFileVersionFromDiff(sourceDiff, targetDiff *DiffEntry) *FileVersion {
	// The base version comes from SrcHash of either diff
	diff := sourceDiff
	if diff == nil {
		diff = targetDiff
	}
	if diff == nil || diff.Status == "A" {
		return &FileVersion{Exists: false}
	}

	mode, _ := strconv.ParseUint(diff.SrcMode, 8, 32)
	return &FileVersion{
		Hash:   diff.SrcHash,
		Mode:   uint32(mode),
		Exists: true,
	}
}

// determineConflictType determines the type of conflict based on diff states.
func (cd *ConflictDetector) determineConflictType(baseExists, sourceExists, targetExists bool, sourceDiff, targetDiff *DiffEntry) ConflictType {
	// Delete/modify conflict
	if baseExists && !sourceExists && targetExists {
		return ConflictTypeDelete
	}
	if baseExists && sourceExists && !targetExists {
		return ConflictTypeDelete
	}

	// Add/add conflict
	if !baseExists && sourceExists && targetExists {
		return ConflictTypeAdd
	}

	// Mode conflict
	if sourceDiff != nil && targetDiff != nil &&
		sourceDiff.DstHash == targetDiff.DstHash &&
		sourceDiff.DstMode != targetDiff.DstMode {
		return ConflictTypeMode
	}

	// Content conflict (default for both-modified)
	return ConflictTypeContent
}

// calculateConflictSeverity determines the severity of a conflict
func (cd *ConflictDetector) calculateConflictSeverity(conflictType ConflictType, sourceVersion, targetVersion *FileVersion) ConflictSeverity {
	switch conflictType {
	case ConflictTypeMode:
		return ConflictSeverityLow
	case ConflictTypeAdd:
		if sourceVersion != nil && targetVersion != nil && sourceVersion.IsBinary != targetVersion.IsBinary {
			return ConflictSeverityHigh
		}
		return ConflictSeverityMedium
	case ConflictTypeDelete:
		return ConflictSeverityHigh
	case ConflictTypeContent:
		if (sourceVersion != nil && sourceVersion.IsBinary) || (targetVersion != nil && targetVersion.IsBinary) {
			return ConflictSeverityCritical
		}
		return ConflictSeverityMedium
	default:
		return ConflictSeverityMedium
	}
}

// analyzeContentConflicts analyzes content-level conflicts using native git.
func (cd *ConflictDetector) analyzeContentConflicts(ctx context.Context, repoPath string, sourceDiff, targetDiff *DiffEntry, maxFileSize int64) ([]ConflictRegion, error) {
	var regions []ConflictRegion

	// Read file contents via git cat-file
	var baseContent, sourceContent, targetContent string

	if sourceDiff != nil && sourceDiff.SrcHash != "" && sourceDiff.SrcHash != strings.Repeat("0", 40) {
		if size, err := gitCatFileBlobSize(ctx, repoPath, sourceDiff.SrcHash); err == nil && size <= maxFileSize {
			if data, err := gitCatFileBlob(ctx, repoPath, sourceDiff.SrcHash); err == nil {
				baseContent = string(data)
			}
		}
	}
	if sourceDiff != nil && sourceDiff.DstHash != "" && sourceDiff.DstHash != strings.Repeat("0", 40) {
		if size, err := gitCatFileBlobSize(ctx, repoPath, sourceDiff.DstHash); err == nil && size <= maxFileSize {
			if data, err := gitCatFileBlob(ctx, repoPath, sourceDiff.DstHash); err == nil {
				sourceContent = string(data)
			}
		}
	}
	if targetDiff != nil && targetDiff.DstHash != "" && targetDiff.DstHash != strings.Repeat("0", 40) {
		if size, err := gitCatFileBlobSize(ctx, repoPath, targetDiff.DstHash); err == nil && size <= maxFileSize {
			if data, err := gitCatFileBlob(ctx, repoPath, targetDiff.DstHash); err == nil {
				targetContent = string(data)
			}
		}
	}

	// Simple line-based conflict detection
	baseLines := strings.Split(baseContent, "\n")
	sourceLines := strings.Split(sourceContent, "\n")
	targetLines := strings.Split(targetContent, "\n")

	if sourceContent != targetContent {
		regions = append(regions, ConflictRegion{
			StartLine:    1,
			EndLine:      len(sourceLines),
			OurContent:   strings.Join(sourceLines, "\n"),
			TheirContent: strings.Join(targetLines, "\n"),
			BaseContent:  strings.Join(baseLines, "\n"),
			MarkerStyle:  "standard",
		})
	}

	return regions, nil
}

func (cd *ConflictDetector) isBinaryContent(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return false
}

func (cd *ConflictDetector) calculateSeverityBreakdown(conflicts []FileConflict) map[ConflictSeverity]int {
	breakdown := make(map[ConflictSeverity]int)
	for _, conflict := range conflicts {
		breakdown[conflict.Severity]++
	}
	return breakdown
}

func (cd *ConflictDetector) estimateResolutionTime(conflicts []FileConflict) time.Duration {
	var totalMinutes int
	for _, conflict := range conflicts {
		switch conflict.Severity {
		case ConflictSeverityLow:
			totalMinutes += 2
		case ConflictSeverityMedium:
			totalMinutes += 10
		case ConflictSeverityHigh:
			totalMinutes += 30
		case ConflictSeverityCritical:
			totalMinutes += 60
		}
	}
	return time.Duration(totalMinutes) * time.Minute
}

func (cd *ConflictDetector) generateRecommendations(conflicts []FileConflict, strategy MergeStrategy) []string {
	if len(conflicts) == 0 {
		return []string{"No conflicts detected - merge can proceed safely"}
	}

	recommendations := []string{
		fmt.Sprintf("All %d conflicts require manual resolution", len(conflicts)),
	}

	switch strategy {
	case MergeStrategyRecursive:
		recommendations = append(recommendations, "Recursive strategy provides good conflict detection")
	case MergeStrategyOurs:
		recommendations = append(recommendations, "Using 'ours' strategy will favor your changes in conflicts")
	case MergeStrategyTheirs:
		recommendations = append(recommendations, "Using 'theirs' strategy will favor their changes in conflicts")
	}

	return recommendations
}
