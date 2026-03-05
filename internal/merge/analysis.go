package merge

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
)

// analyzeMerge performs merge conflict analysis using native git.
func (h *MergeOperationsHandler) analyzeMerge(ctx context.Context, repoPath string, sourceHash, targetHash string, req *MergeRequest) (*MergeAnalysisResult, error) {
	analysisReq := &MergeAnalysisRequest{
		RepoPath:       repoPath,
		SourceRef:      sourceHash,
		TargetRef:      targetHash,
		Strategy:       MergeStrategy(req.Strategy),
		AutoResolve:    false,
		IncludeContent: true,
		MaxFileSize:    1024 * 1024, // 1MB max for conflict analysis
	}

	conflictDetector := NewConflictDetector(h.pathConfig, h.logger)
	result, err := conflictDetector.AnalyzeMerge(ctx, analysisReq)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze merge: %w", err)
	}

	return result, nil
}

// analyzeRebaseDryRun performs rebase-specific dry run analysis using native git.
func (h *MergeOperationsHandler) analyzeRebaseDryRun(ctx context.Context, repoPath string, sourceHash, targetHash string, req *MergeRequest) (*MergeAnalysisResult, error) {
	// Find merge base
	mergeBaseHash, err := gitMergeBase(ctx, repoPath, sourceHash, targetHash)
	if err != nil {
		return nil, fmt.Errorf("failed to find merge base: %w", err)
	}

	// If source is already based on target, nothing to rebase
	if mergeBaseHash == sourceHash {
		return &MergeAnalysisResult{
			CanMerge:          true,
			HasConflicts:      false,
			IsFastForward:     false,
			ConflictCount:     0,
			FileConflicts:     []FileConflict{},
			Strategy:          MergeStrategy(req.Strategy),
			AutoResolvable:    0,
			SeverityBreakdown: make(map[ConflictSeverity]int),
			EstimatedTime:     0,
			Recommendations:   []string{"Source is already based on target, nothing to rebase"},
		}, nil
	}

	// Get commits to rebase (in chronological order: oldest first)
	commitsToRebase, err := gitRevListReverse(ctx, repoPath, mergeBaseHash, sourceHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get commits to rebase: %w", err)
	}

	if len(commitsToRebase) == 0 {
		return &MergeAnalysisResult{
			CanMerge:          true,
			HasConflicts:      false,
			IsFastForward:     false,
			ConflictCount:     0,
			FileConflicts:     []FileConflict{},
			Strategy:          MergeStrategy(req.Strategy),
			AutoResolvable:    0,
			SeverityBreakdown: make(map[ConflictSeverity]int),
			EstimatedTime:     0,
			Recommendations:   []string{"No commits to rebase"},
		}, nil
	}

	// Analyze each commit sequentially (simulating rebase)
	allConflicts := make(map[string]FileConflict)
	currentBaseHash := targetHash
	totalConflictCount := 0
	firstConflictCommitSHA := ""

	h.logger.Info("Analyzing rebase dry run",
		zap.Int("commits_to_rebase", len(commitsToRebase)))

	for i, commitHash := range commitsToRebase {
		commit, err := gitCatFileCommit(ctx, repoPath, commitHash)
		if err != nil {
			h.logger.Warn("Failed to read rebase commit",
				zap.String("commit", commitHash), zap.Error(err))
			continue
		}

		// Skip root commits
		if len(commit.ParentHashes) == 0 {
			continue
		}

		// Analyze this commit's rebase onto current base
		singleAnalysis, err := h.analyzeMerge(ctx, repoPath, commitHash, currentBaseHash, req)
		if err != nil {
			h.logger.Warn("Failed to analyze rebase commit",
				zap.String("commit", commitHash), zap.Error(err))
			continue
		}

		if singleAnalysis.HasConflicts {
			if firstConflictCommitSHA == "" {
				firstConflictCommitSHA = commitHash
			}
			totalConflictCount += singleAnalysis.ConflictCount
			for _, conflict := range singleAnalysis.FileConflicts {
				allConflicts[conflict.FilePath] = conflict
			}
		}

		h.logger.Debug("Analyzed rebase commit",
			zap.Int("index", i+1),
			zap.Int("total", len(commitsToRebase)),
			zap.String("commit", commitHash))
	}

	// Convert conflicts map to slice
	fileConflicts := make([]FileConflict, 0, len(allConflicts))
	for _, conflict := range allConflicts {
		fileConflicts = append(fileConflicts, conflict)
	}

	severityBreakdown := make(map[ConflictSeverity]int)
	for _, conflict := range fileConflicts {
		severityBreakdown[conflict.Severity]++
	}

	recommendations := []string{}
	if totalConflictCount > 0 {
		recommendations = append(recommendations, fmt.Sprintf("Rebase will encounter %d conflicts across %d commits", totalConflictCount, len(commitsToRebase)))
		if firstConflictCommitSHA != "" {
			shortSHA := firstConflictCommitSHA
			if len(shortSHA) > 8 {
				shortSHA = shortSHA[:8]
			}
			recommendations = append(recommendations, fmt.Sprintf("First conflict occurs at commit %s", shortSHA))
		}
		recommendations = append(recommendations, "All conflicts require manual resolution")
	} else {
		recommendations = append(recommendations, "Rebase will complete without conflicts")
	}

	metadata := map[string]interface{}{
		"commits_to_rebase":     len(commitsToRebase),
		"first_conflict_commit": firstConflictCommitSHA,
		"rebase_method":         "sequential",
	}

	return &MergeAnalysisResult{
		CanMerge:          totalConflictCount == 0,
		HasConflicts:      totalConflictCount > 0,
		IsFastForward:     false,
		ConflictCount:     len(fileConflicts),
		FileConflicts:     fileConflicts,
		Strategy:          MergeStrategy(req.Strategy),
		AutoResolvable:    0,
		SeverityBreakdown: severityBreakdown,
		EstimatedTime:     time.Duration(totalConflictCount) * time.Minute,
		Recommendations:   recommendations,
		Metadata:          metadata,
	}, nil
}
