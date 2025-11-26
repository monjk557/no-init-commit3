package protocol

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/git"
	"github.com/collabops/git-executor/internal/schemas/common"
)

// MergeMethod represents different merge methods (how to combine branches)
type MergeMethod string

const (
	MergeMethodMerge  MergeMethod = "merge"  // Default: create a merge commit
	MergeMethodSquash MergeMethod = "squash" // Squash all commits into a single commit
	MergeMethodRebase MergeMethod = "rebase" // Rebase source branch onto target (not yet implemented)
)

// MergeStrategy represents different merge strategies (Git's internal merge algorithm)
type MergeStrategy string

const (
	MergeStrategyRecursive MergeStrategy = "recursive" // Default three-way merge
	MergeStrategyResolve   MergeStrategy = "resolve"   // Three-way merge with aggressive resolution
	MergeStrategyORT       MergeStrategy = "ort"       // Optimized Recursive Three-way merge (improved recursive)
	MergeStrategyOurs      MergeStrategy = "ours"      // Keep our version on conflicts
	MergeStrategyTheirs    MergeStrategy = "theirs"    // Keep their version on conflicts
	MergeStrategySubtree   MergeStrategy = "subtree"   // Subtree merge strategy
)

// MergeRequest represents the JSON payload for merge operations
type MergeRequest struct {
	SourceRef     string                 `json:"source_ref" binding:"required"` // Source branch/commit to merge FROM
	TargetRef     string                 `json:"target_ref" binding:"required"` // Target branch/commit to merge INTO
	Method        MergeMethod            `json:"method,omitempty"`              // Merge method (merge, squash, rebase) - default: merge
	Strategy      MergeStrategy          `json:"strategy,omitempty"`            // Merge strategy (default: recursive)
	Message       string                 `json:"message,omitempty"`             // Custom merge commit message
	Author        *common.AuthorInfo     `json:"author,omitempty"`              // Merge commit author
	Committer     *common.AuthorInfo     `json:"committer,omitempty"`           // Merge commit committer
	ForceConflict bool                   `json:"force_conflict,omitempty"`      // Force merge even with conflicts
	DryRun        bool                   `json:"dry_run,omitempty"`             // Analyze merge without performing it
	Metadata      map[string]interface{} `json:"metadata,omitempty"`            // Additional metadata
}

// MergeResponse represents the response for merge operations
type MergeResponse struct {
	Success         bool                     `json:"success"`
	MergeCommit     *common.CommitInfo       `json:"merge_commit"`     // Created merge commit info
	IsFastForward   bool                     `json:"is_fast_forward"`  // Whether merge was fast-forward
	HasConflicts    bool                     `json:"has_conflicts"`    // Whether conflicts were detected
	ConflictCount   int                      `json:"conflict_count"`   // Number of conflicts
	Strategy        MergeStrategy            `json:"strategy"`         // Strategy used
	Method          MergeMethod              `json:"method"`           // Merge method used (merge, squash, rebase)
	Message         string                   `json:"message"`          // Operation result message
	Details         map[string]interface{}   `json:"details"`          // Additional details
	ConflictSummary *git.MergeAnalysisResult `json:"conflict_summary"` // Detailed conflict analysis
}

// MergeResult represents the internal result of a merge operation
type MergeResult struct {
	MergeCommit      *object.Commit
	IsFastForward    bool
	HasConflicts     bool
	ConflictAnalysis *git.MergeAnalysisResult
}

// validateMergeRequest validates the merge request parameters
func (h *MergeOperationsHandler) validateMergeRequest(req *MergeRequest) error {
	// Validate source and target refs
	if req.SourceRef == "" {
		return api.NewBadRequestError("source_ref is required")
	}
	if req.TargetRef == "" {
		return api.NewBadRequestError("target_ref is required")
	}

	// Validate method
	switch req.Method {
	case MergeMethodMerge, MergeMethodSquash, MergeMethodRebase:
		// Valid method
	default:
		if req.Method != "" {
			return api.NewBadRequestError(fmt.Sprintf("Invalid merge method: %s. Must be one of: merge, squash, rebase", req.Method))
		}
	}

	// Validate strategy
	switch req.Strategy {
	case MergeStrategyRecursive, MergeStrategyResolve, MergeStrategyORT, MergeStrategyOurs, MergeStrategyTheirs, MergeStrategySubtree:
		// Valid strategy
	default:
		if req.Strategy != "" {
			return api.NewBadRequestError(fmt.Sprintf("Invalid merge strategy: %s", req.Strategy))
		}
	}

	// Validate message length
	if len(req.Message) > 5000 {
		return api.NewBadRequestError("Merge message too long (max 5000 characters)")
	}

	// Validate author info if provided
	if req.Author != nil {
		if req.Author.Name == "" {
			return api.NewBadRequestError("Author name cannot be empty")
		}
		if req.Author.Email == "" {
			return api.NewBadRequestError("Author email cannot be empty")
		}
	}

	// Validate committer info if provided
	if req.Committer != nil {
		if req.Committer.Name == "" {
			return api.NewBadRequestError("Committer name cannot be empty")
		}
		if req.Committer.Email == "" {
			return api.NewBadRequestError("Committer email cannot be empty")
		}
	}

	return nil
}

// analyzeMerge performs merge conflict analysis
func (h *MergeOperationsHandler) analyzeMerge(ctx context.Context, repo *gogit.Repository, sourceHash, targetHash plumbing.Hash, req *MergeRequest) (*git.MergeAnalysisResult, error) {
	// Create analysis request with already opened repository
	analysisReq := &git.MergeAnalysisRequest{
		Repository:     repo, // Use already opened repository (중복 오픈 방지)
		Owner:          "",   // Not needed when Repository is provided
		Repo:           "",   // Not needed when Repository is provided
		SourceRef:      sourceHash.String(),
		TargetRef:      targetHash.String(),
		Strategy:       git.MergeStrategy(req.Strategy),
		AutoResolve:    req.ForceConflict, // Only auto-resolve if forcing
		IncludeContent: true,
		MaxFileSize:    1024 * 1024, // 1MB max for conflict analysis
	}

	// Perform analysis
	return h.conflictDetector.AnalyzeMerge(ctx, analysisReq)
}

// analyzeRebaseDryRun performs rebase-specific dry run analysis
// Rebase는 각 커밋을 순차적으로 rebase하면서 각 단계에서 conflict를 확인해야 함
func (h *MergeOperationsHandler) analyzeRebaseDryRun(ctx context.Context, repo *gogit.Repository, sourceHash, targetHash plumbing.Hash, req *MergeRequest) (*git.MergeAnalysisResult, error) {
	// Get source and target commits
	sourceCommit, err := repo.CommitObject(sourceHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get source commit: %w", err)
	}

	targetCommit, err := repo.CommitObject(targetHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get target commit: %w", err)
	}

	// Find merge base (common ancestor)
	mergeBases, err := sourceCommit.MergeBase(targetCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to find merge base: %w", err)
	}
	if len(mergeBases) == 0 {
		return nil, fmt.Errorf("no common ancestor found for rebase")
	}

	mergeBase := mergeBases[0]

	// If source and target are the same or source is already on target, nothing to rebase
	if mergeBase.Hash == sourceCommit.Hash {
		return &git.MergeAnalysisResult{
			CanMerge:          true,
			HasConflicts:      false,
			IsFastForward:     false,
			ConflictCount:     0,
			FileConflicts:     []git.FileConflict{},
			SourceCommit:      nil, // Will be set if needed
			TargetCommit:      nil,
			Strategy:          git.MergeStrategy(req.Strategy),
			AutoResolvable:    0,
			SeverityBreakdown: make(map[git.ConflictSeverity]int),
			EstimatedTime:     0,
			Recommendations:   []string{"Source is already based on target, nothing to rebase"},
		}, nil
	}

	// Collect commits from merge base to source (exclusive of merge base, inclusive of source)
	commitsToRebase, err := h.getCommitsBetween(repo, mergeBase, sourceCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to get commits to rebase: %w", err)
	}

	if len(commitsToRebase) == 0 {
		return &git.MergeAnalysisResult{
			CanMerge:          true,
			HasConflicts:      false,
			IsFastForward:     false,
			ConflictCount:     0,
			FileConflicts:     []git.FileConflict{},
			Strategy:          git.MergeStrategy(req.Strategy),
			AutoResolvable:    0,
			SeverityBreakdown: make(map[git.ConflictSeverity]int),
			EstimatedTime:     0,
			Recommendations:   []string{"No commits to rebase"},
		}, nil
	}

	// Analyze each commit sequentially (simulating rebase)
	allConflicts := make(map[string]git.FileConflict) // Use map to deduplicate conflicts
	currentBase := targetCommit
	totalConflictCount := 0
	var firstConflictCommit *object.Commit
	firstConflictCommitSHA := ""

	h.logger.Info("Analyzing rebase dry run",
		zap.Int("commits_to_rebase", len(commitsToRebase)))

	for i, commitToRebase := range commitsToRebase {
		// Get the commit's parent (the base from which this commit was made)
		var parentCommit *object.Commit
		if len(commitToRebase.ParentHashes) > 0 {
			parentCommit, err = repo.CommitObject(commitToRebase.ParentHashes[0])
			if err != nil {
				h.logger.Warn("Failed to get parent commit for rebase analysis",
					zap.String("commit", commitToRebase.Hash.String()),
					zap.Error(err))
				continue
			}
		}

		// For non-root commits, analyze conflict with current base
		if parentCommit != nil {
			// Analyze this commit's rebase onto current base
			singleAnalysis, err := h.analyzeMerge(ctx, repo, commitToRebase.Hash, currentBase.Hash, req)
			if err != nil {
				h.logger.Warn("Failed to analyze rebase commit",
					zap.String("commit", commitToRebase.Hash.String()),
					zap.Error(err))
				continue
			}

			// Collect conflicts from this step
			if singleAnalysis.HasConflicts {
				if firstConflictCommit == nil {
					firstConflictCommit = commitToRebase
					firstConflictCommitSHA = commitToRebase.Hash.String()
				}
				totalConflictCount += singleAnalysis.ConflictCount

				// Merge conflicts into allConflicts map (deduplicate by path)
				for _, conflict := range singleAnalysis.FileConflicts {
					allConflicts[conflict.FilePath] = conflict
				}
			}

			// Update current base for next iteration (simulating successful rebase)
			// In dry run, we don't actually create commits, so we simulate by using target
			// This is approximate - actual rebase would have new commits
			// For analysis purposes, we continue with the assumption that rebase succeeds
		}

		h.logger.Debug("Analyzed rebase commit",
			zap.Int("index", i+1),
			zap.Int("total", len(commitsToRebase)),
			zap.String("commit", commitToRebase.Hash.String()),
			zap.Bool("has_conflicts", parentCommit != nil && totalConflictCount > 0))
	}

	// Convert conflicts map to slice
	fileConflicts := make([]git.FileConflict, 0, len(allConflicts))
	for _, conflict := range allConflicts {
		fileConflicts = append(fileConflicts, conflict)
	}

	// Calculate auto-resolvable count
	autoResolvable := 0
	for _, conflict := range fileConflicts {
		if conflict.AutoResolvable {
			autoResolvable++
		}
	}

	// Calculate severity breakdown
	severityBreakdown := make(map[git.ConflictSeverity]int)
	for _, conflict := range fileConflicts {
		severityBreakdown[conflict.Severity]++
	}

	// Generate recommendations
	recommendations := []string{}
	if totalConflictCount > 0 {
		recommendations = append(recommendations, fmt.Sprintf("Rebase will encounter %d conflicts across %d commits", totalConflictCount, len(commitsToRebase)))
		if firstConflictCommitSHA != "" {
			recommendations = append(recommendations, fmt.Sprintf("First conflict occurs at commit %s: %s", firstConflictCommitSHA[:8], firstConflictCommit.Message))
		}
		if autoResolvable > 0 {
			recommendations = append(recommendations, fmt.Sprintf("%d conflicts may be auto-resolvable", autoResolvable))
		}
	} else {
		recommendations = append(recommendations, "Rebase will complete without conflicts")
	}

	// Add rebase-specific metadata
	metadata := map[string]interface{}{
		"commits_to_rebase":     len(commitsToRebase),
		"first_conflict_commit": firstConflictCommitSHA,
		"rebase_method":         "sequential",
	}

	return &git.MergeAnalysisResult{
		CanMerge:          totalConflictCount == 0 || (req.ForceConflict && autoResolvable == totalConflictCount),
		HasConflicts:      totalConflictCount > 0,
		IsFastForward:     false,              // Rebase is never fast-forward
		ConflictCount:     len(fileConflicts), // Unique file conflicts
		FileConflicts:     fileConflicts,
		MergeBase:         nil, // Not used for rebase
		SourceCommit:      nil, // Can be set if needed
		TargetCommit:      nil,
		Strategy:          git.MergeStrategy(req.Strategy),
		AutoResolvable:    autoResolvable,
		SeverityBreakdown: severityBreakdown,
		EstimatedTime:     time.Duration(totalConflictCount) * time.Minute, // Rough estimate
		Recommendations:   recommendations,
		Metadata:          metadata,
	}, nil
}

// performMerge performs the actual Git merge operation
func (h *MergeOperationsHandler) performMerge(ctx context.Context, repo *gogit.Repository, sourceHash, targetHash plumbing.Hash, req *MergeRequest, analysis *git.MergeAnalysisResult) (*MergeResult, error) {
	result := &MergeResult{
		IsFastForward:    analysis.IsFastForward,
		HasConflicts:     analysis.HasConflicts,
		ConflictAnalysis: analysis,
	}

	// Route to appropriate merge method
	switch req.Method {
	case MergeMethodSquash:
		// Perform squash merge
		mergeCommit, err := h.performSquashMerge(ctx, repo, sourceHash, targetHash, req, analysis)
		if err != nil {
			return nil, fmt.Errorf("squash merge failed: %w", err)
		}
		result.MergeCommit = mergeCommit
		return result, nil

	case MergeMethodRebase:
		// Perform rebase
		rebaseCommit, err := h.performRebase(ctx, repo, sourceHash, targetHash, req, analysis)
		if err != nil {
			return nil, fmt.Errorf("rebase failed: %w", err)
		}
		result.MergeCommit = rebaseCommit
		return result, nil

	case MergeMethodMerge:
		fallthrough
	default:
		// For merge method, always create a merge commit (even for fast-forward)
		// This is equivalent to git merge --no-ff
		// Perform three-way merge (which will create a merge commit)
		mergeCommit, err := h.performThreeWayMerge(ctx, repo, sourceHash, targetHash, req, analysis)
		if err != nil {
			return nil, fmt.Errorf("three-way merge failed: %w", err)
		}

		result.MergeCommit = mergeCommit
		return result, nil
	}
}

// performSquashMerge performs a squash merge by combining all source commits into a single commit
// Squash merge creates a single commit that contains all changes from the source branch,
// applied on top of the target branch. Unlike regular merge, it doesn't create a merge commit.
func (h *MergeOperationsHandler) performSquashMerge(ctx context.Context, repo *gogit.Repository, sourceHash, targetHash plumbing.Hash, req *MergeRequest, analysis *git.MergeAnalysisResult) (*object.Commit, error) {
	// Get source and target commits
	sourceCommit, err := repo.CommitObject(sourceHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get source commit: %w", err)
	}

	targetCommit, err := repo.CommitObject(targetHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get target commit: %w", err)
	}

	h.logger.Info("Performing squash merge",
		zap.String("source", sourceCommit.Hash.String()),
		zap.String("target", targetCommit.Hash.String()))

	// For squash merge, we need to merge source's tree onto target's tree
	// This is similar to a three-way merge, but we create a single-parent commit instead of a merge commit
	if analysis.HasConflicts && !req.ForceConflict {
		// Even for squash merge, we should check conflicts
		return nil, fmt.Errorf("squash merge has %d conflicts. Use force_conflict=true to override", analysis.ConflictCount)
	}

	// Use centralized strategy resolution logic
	squashTreeHash, err := h.resolveMergeTree(ctx, repo, sourceCommit, targetCommit, analysis, req.Strategy, req.ForceConflict, true)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve merge tree for squash: %w", err)
	}

	// Generate squash commit message
	message := h.generateSquashCommitMessage(req, sourceCommit, targetCommit)

	// Set up author and committer
	now := time.Now()
	author := &object.Signature{
		Name:  "Git Executor",
		Email: "git@executor.com",
		When:  now,
	}
	committer := author

	if req.Author != nil {
		authorDate := req.Author.Date
		// Date가 zero value인 경우 현재 시간 사용
		if authorDate.IsZero() {
			authorDate = now
		}
		author = &object.Signature{
			Name:  req.Author.Name,
			Email: req.Author.Email,
			When:  authorDate,
		}
	} else {
		// Default to source commit author if not specified
		author = &object.Signature{
			Name:  sourceCommit.Author.Name,
			Email: sourceCommit.Author.Email,
			When:  sourceCommit.Author.When,
		}
	}

	if req.Committer != nil {
		committerDate := req.Committer.Date
		// Date가 zero value인 경우 현재 시간 사용
		if committerDate.IsZero() {
			committerDate = now
		}
		committer = &object.Signature{
			Name:  req.Committer.Name,
			Email: req.Committer.Email,
			When:  committerDate,
		}
	} else {
		committer = author
	}

	// Create squash commit with single parent (target only)
	// Unlike merge commit, squash commit has only one parent (the target)
	commit := &object.Commit{
		Message:      message,
		TreeHash:     squashTreeHash,
		ParentHashes: []plumbing.Hash{targetHash}, // Single parent - target branch
		Author:       *author,
		Committer:    *committer,
	}

	// Store the commit
	obj := repo.Storer.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		return nil, fmt.Errorf("failed to encode squash commit: %w", err)
	}

	hash, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		return nil, fmt.Errorf("failed to store squash commit: %w", err)
	}

	commit.Hash = hash

	// Update the target reference to point to the new squash commit
	var refName plumbing.ReferenceName
	if strings.HasPrefix(req.TargetRef, "refs/") {
		refName = plumbing.ReferenceName(req.TargetRef)
	} else {
		refName = plumbing.ReferenceName("refs/heads/" + req.TargetRef)
	}

	ref := plumbing.NewHashReference(refName, hash)
	if err := repo.Storer.SetReference(ref); err != nil {
		return nil, fmt.Errorf("failed to update target reference: %w", err)
	}

	h.logger.Info("Squash merge completed successfully",
		zap.String("commit_hash", hash.String()),
		zap.String("target_ref", req.TargetRef))

	return commit, nil
}

// generateSquashCommitMessage generates an appropriate commit message for squash merge
func (h *MergeOperationsHandler) generateSquashCommitMessage(req *MergeRequest, source, target *object.Commit) string {
	if req.Message != "" {
		return req.Message
	}

	// Generate default squash merge message
	sourceRef := req.SourceRef
	targetRef := req.TargetRef

	// Simplify ref names if they're full refs
	sourceRef = strings.TrimPrefix(sourceRef, "refs/heads/")
	targetRef = strings.TrimPrefix(targetRef, "refs/heads/")

	return fmt.Sprintf("Squash merge branch '%s' into '%s'", sourceRef, targetRef)
}

// performFastForwardMerge performs a fast-forward merge by updating the target reference
func (h *MergeOperationsHandler) performFastForwardMerge(repo *gogit.Repository, targetHash, sourceHash plumbing.Hash, targetRef string) error {
	// Determine the reference name to update
	var refName plumbing.ReferenceName
	if strings.HasPrefix(targetRef, "refs/") {
		refName = plumbing.ReferenceName(targetRef)
	} else {
		refName = plumbing.ReferenceName("refs/heads/" + targetRef)
	}

	// Update the reference to point to the source commit
	ref := plumbing.NewHashReference(refName, sourceHash)
	return repo.Storer.SetReference(ref)
}

// performRebase performs a rebase by reapplying source branch commits onto target branch
// Rebase takes commits from source branch (after merge base) and applies them one by one onto target
func (h *MergeOperationsHandler) performRebase(ctx context.Context, repo *gogit.Repository, sourceHash, targetHash plumbing.Hash, req *MergeRequest, analysis *git.MergeAnalysisResult) (*object.Commit, error) {
	// Get source and target commits
	sourceCommit, err := repo.CommitObject(sourceHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get source commit: %w", err)
	}

	targetCommit, err := repo.CommitObject(targetHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get target commit: %w", err)
	}

	h.logger.Info("Performing rebase",
		zap.String("source", sourceCommit.Hash.String()),
		zap.String("target", targetCommit.Hash.String()))

	// Find merge base (common ancestor)
	mergeBases, err := sourceCommit.MergeBase(targetCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to find merge base: %w", err)
	}
	if len(mergeBases) == 0 {
		return nil, fmt.Errorf("no common ancestor found for rebase")
	}

	mergeBase := mergeBases[0]
	h.logger.Debug("Found merge base for rebase",
		zap.String("merge_base", mergeBase.Hash.String()))

	// If source and target are the same or source is already on target, nothing to rebase
	if mergeBase.Hash == sourceCommit.Hash {
		h.logger.Info("Source is already based on target, nothing to rebase")
		return targetCommit, nil
	}

	// Collect commits from merge base to source (exclusive of merge base, inclusive of source)
	commitsToRebase, err := h.getCommitsBetween(repo, mergeBase, sourceCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to get commits to rebase: %w", err)
	}

	if len(commitsToRebase) == 0 {
		h.logger.Info("No commits to rebase")
		return targetCommit, nil
	}

	h.logger.Info("Rebasing commits",
		zap.Int("commit_count", len(commitsToRebase)))

	// Start with target as the base
	currentBase := targetCommit

	// Apply each commit sequentially
	for i, commitToRebase := range commitsToRebase {
		h.logger.Debug("Rebasing commit",
			zap.Int("index", i+1),
			zap.Int("total", len(commitsToRebase)),
			zap.String("commit", commitToRebase.Hash.String()))

		// For rebase, we need to apply the commit's changes to the current base
		// The commit's changes are: (commitToRebase - commitToRebase.Parents[0])
		// We apply these changes to currentBase
		// This is different from a regular merge where we need a common ancestor

		// Get the commit's parent (the base from which this commit was made)
		var parentCommit *object.Commit
		if len(commitToRebase.ParentHashes) > 0 {
			parentCommit, err = repo.CommitObject(commitToRebase.ParentHashes[0])
			if err != nil {
				return nil, fmt.Errorf("failed to get parent commit for rebase: %w", err)
			}
		} else {
			// This is a root commit, just use its tree
			parentCommit = nil
		}

		// For rebase, we merge: currentBase (target) <- commitToRebase (source)
		// with parentCommit as the common ancestor
		// If it's a root commit, we just use the commit's tree directly
		var rebasedTreeHash plumbing.Hash
		var singleCommitAnalysis *git.MergeAnalysisResult

		if parentCommit == nil {
			// Root commit: use its tree directly merged with currentBase
			// This is a simple case - just apply the root commit's tree
			rebasedTreeHash = commitToRebase.TreeHash
		} else {
			// Perform 3-way merge: parentCommit (base) + commitToRebase (source) -> currentBase (target)
			// Create a temporary analysis for this single commit rebase
			var err error
			singleCommitAnalysis, err = h.analyzeMerge(ctx, repo, commitToRebase.Hash, currentBase.Hash, req)
			if err != nil {
				return nil, fmt.Errorf("failed to analyze rebase commit %s: %w", commitToRebase.Hash.String(), err)
			}

			// Check for conflicts if not forcing
			if singleCommitAnalysis.HasConflicts && !req.ForceConflict {
				return nil, fmt.Errorf("rebase conflict at commit %s: %d conflicts. Use force_conflict=true to override", commitToRebase.Hash.String(), singleCommitAnalysis.ConflictCount)
			}

			// Use createMergedTreeWithBase for rebase (using parentCommit as base)
			rebasedTreeHash, err = h.createMergedTreeWithBase(ctx, repo, commitToRebase, currentBase, parentCommit, singleCommitAnalysis, req.Strategy)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve tree for rebase commit %s: %w", commitToRebase.Hash.String(), err)
			}
		}

		// Create new commit with rebased tree
		// Preserve original commit author and message, but use new parent (currentBase)
		var author object.Signature
		var committer object.Signature

		// Allow override of author/committer if provided in request
		if req.Author != nil {
			authorDate := req.Author.Date
			// Date가 zero value인 경우 원본 커밋의 작성자 날짜 사용
			if authorDate.IsZero() {
				authorDate = commitToRebase.Author.When
			}
			author = object.Signature{
				Name:  req.Author.Name,
				Email: req.Author.Email,
				When:  authorDate,
			}
		} else {
			author = commitToRebase.Author
		}

		if req.Committer != nil {
			committerDate := req.Committer.Date
			// Date가 zero value인 경우 현재 시간 사용 (rebase는 커밋 시간 변경)
			if committerDate.IsZero() {
				committerDate = time.Now()
			}
			committer = object.Signature{
				Name:  req.Committer.Name,
				Email: req.Committer.Email,
				When:  committerDate,
			}
		} else {
			// Use current time for committer if not specified (rebase changes commit time)
			committer = object.Signature{
				Name:  author.Name,
				Email: author.Email,
				When:  time.Now(),
			}
		}

		// Create rebased commit
		rebasedCommit := &object.Commit{
			Message:      commitToRebase.Message,
			TreeHash:     rebasedTreeHash,
			ParentHashes: []plumbing.Hash{currentBase.Hash}, // Single parent - the current base
			Author:       author,
			Committer:    committer,
		}

		// Store the commit
		obj := repo.Storer.NewEncodedObject()
		if err := rebasedCommit.Encode(obj); err != nil {
			return nil, fmt.Errorf("failed to encode rebased commit: %w", err)
		}

		hash, err := repo.Storer.SetEncodedObject(obj)
		if err != nil {
			return nil, fmt.Errorf("failed to store rebased commit: %w", err)
		}

		// Reload the commit from the repository to ensure it's fully stored
		// This ensures we have a valid commit object with proper storage references for the next iteration
		currentBase, err = repo.CommitObject(hash)
		if err != nil {
			return nil, fmt.Errorf("failed to reload rebased commit: %w", err)
		}

		h.logger.Debug("Successfully rebased commit",
			zap.String("original", commitToRebase.Hash.String()),
			zap.String("rebased", hash.String()))
	}

	// Update target reference to point to the final rebased commit
	var refName plumbing.ReferenceName
	if strings.HasPrefix(req.TargetRef, "refs/") {
		refName = plumbing.ReferenceName(req.TargetRef)
	} else {
		refName = plumbing.ReferenceName("refs/heads/" + req.TargetRef)
	}

	ref := plumbing.NewHashReference(refName, currentBase.Hash)
	if err := repo.Storer.SetReference(ref); err != nil {
		return nil, fmt.Errorf("failed to update target reference: %w", err)
	}

	h.logger.Info("Rebase completed successfully",
		zap.String("final_commit", currentBase.Hash.String()),
		zap.Int("rebased_commits", len(commitsToRebase)))

	return currentBase, nil
}

// getCommitsBetween collects commits from 'from' (exclusive) to 'to' (inclusive) in chronological order
// Returns commits in order from oldest to newest
func (h *MergeOperationsHandler) getCommitsBetween(repo *gogit.Repository, from, to *object.Commit) ([]*object.Commit, error) {
	if from.Hash == to.Hash {
		return []*object.Commit{}, nil
	}

	// Get commit log from 'to' commit
	commitIter, err := repo.Log(&gogit.LogOptions{
		From: to.Hash,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get commit log: %w", err)
	}
	defer commitIter.Close()

	var commits []*object.Commit
	fromHashStr := from.Hash.String()

	err = commitIter.ForEach(func(commit *object.Commit) error {
		// Stop when we reach the 'from' commit
		if commit.Hash.String() == fromHashStr {
			return fmt.Errorf("stop iteration")
		}

		commits = append(commits, commit)
		return nil
	})

	// Check if we stopped at the 'from' commit (normal case)
	if err != nil && err.Error() != "stop iteration" {
		return nil, fmt.Errorf("failed to iterate commits: %w", err)
	}

	// Reverse the list to get chronological order (oldest first)
	// Since we collected from newest to oldest, we need to reverse
	for i, j := 0, len(commits)-1; i < j; i, j = i+1, j-1 {
		commits[i], commits[j] = commits[j], commits[i]
	}

	return commits, nil
}

// performThreeWayMerge performs a three-way merge and creates a merge commit
func (h *MergeOperationsHandler) performThreeWayMerge(ctx context.Context, repo *gogit.Repository, sourceHash, targetHash plumbing.Hash, req *MergeRequest, analysis *git.MergeAnalysisResult) (*object.Commit, error) {
	// Get source and target commits
	sourceCommit, err := repo.CommitObject(sourceHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get source commit: %w", err)
	}

	targetCommit, err := repo.CommitObject(targetHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get target commit: %w", err)
	}

	// Use centralized strategy resolution logic
	mergeTreeHash, err := h.resolveMergeTree(ctx, repo, sourceCommit, targetCommit, analysis, req.Strategy, req.ForceConflict, false)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve merge tree: %w", err)
	}

	if analysis.HasConflicts && !req.ForceConflict {
		// This should not happen as we check for conflicts earlier
		return nil, fmt.Errorf("conflicts detected but force_conflict is false")
	}

	// Generate merge commit message
	message := h.generateMergeCommitMessage(req, sourceCommit, targetCommit)

	// Set up author and committer
	now := time.Now()
	author := &object.Signature{
		Name:  "Git Executor",
		Email: "git@executor.com",
		When:  now,
	}
	committer := author

	if req.Author != nil {
		authorDate := req.Author.Date
		// Date가 zero value인 경우 현재 시간 사용
		if authorDate.IsZero() {
			authorDate = now
		}
		author = &object.Signature{
			Name:  req.Author.Name,
			Email: req.Author.Email,
			When:  authorDate,
		}
	}

	if req.Committer != nil {
		committerDate := req.Committer.Date
		// Date가 zero value인 경우 현재 시간 사용
		if committerDate.IsZero() {
			committerDate = now
		}
		committer = &object.Signature{
			Name:  req.Committer.Name,
			Email: req.Committer.Email,
			When:  committerDate,
		}
	}

	// Create merge commit
	commit := &object.Commit{
		Message:      message,
		TreeHash:     mergeTreeHash, // Use strategy-determined tree
		ParentHashes: []plumbing.Hash{targetHash, sourceHash},
		Author:       *author,
		Committer:    *committer,
	}

	// Store the commit
	obj := repo.Storer.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		return nil, fmt.Errorf("failed to encode merge commit: %w", err)
	}

	hash, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		return nil, fmt.Errorf("failed to store merge commit: %w", err)
	}

	commit.Hash = hash

	// Update the target reference to point to the new merge commit
	if strings.HasPrefix(req.TargetRef, "refs/") {
		refName := plumbing.ReferenceName(req.TargetRef)
		ref := plumbing.NewHashReference(refName, hash)
		if err := repo.Storer.SetReference(ref); err != nil {
			return nil, fmt.Errorf("failed to update target reference: %w", err)
		}
	} else {
		refName := plumbing.ReferenceName("refs/heads/" + req.TargetRef)
		ref := plumbing.NewHashReference(refName, hash)
		if err := repo.Storer.SetReference(ref); err != nil {
			return nil, fmt.Errorf("failed to update target reference: %w", err)
		}
	}

	return commit, nil
}

// generateMergeCommitMessage generates an appropriate merge commit message
func (h *MergeOperationsHandler) generateMergeCommitMessage(req *MergeRequest, source, target *object.Commit) string {
	if req.Message != "" {
		return req.Message
	}

	// Generate default merge message
	sourceRef := req.SourceRef
	targetRef := req.TargetRef

	// Simplify ref names if they're full refs
	sourceRef = strings.TrimPrefix(sourceRef, "refs/heads/")
	targetRef = strings.TrimPrefix(targetRef, "refs/heads/")

	return fmt.Sprintf("Merge branch '%s' into '%s'", sourceRef, targetRef)
}

// generateMergeMessage generates the response message for a completed merge
func (h *MergeOperationsHandler) generateMergeMessage(result *MergeResult, req *MergeRequest) string {
	if result.IsFastForward {
		return fmt.Sprintf("Fast-forward merge from '%s' to '%s' completed successfully", req.SourceRef, req.TargetRef)
	}

	if result.HasConflicts {
		return fmt.Sprintf("Merge from '%s' to '%s' completed with conflicts resolved", req.SourceRef, req.TargetRef)
	}

	return fmt.Sprintf("Merge from '%s' to '%s' completed successfully", req.SourceRef, req.TargetRef)
}

// getMergeType returns the type of merge performed
func (h *MergeOperationsHandler) getMergeType(result *MergeResult) string {
	if result.IsFastForward {
		return "fast-forward"
	}
	if result.HasConflicts {
		return "three-way-with-conflicts"
	}
	return "three-way"
}

// getMergeType returns the type of merge performed based on method and result
func (h *MergeOperationsHandler) getMergeTypeForMethod(method MergeMethod, result *MergeResult) string {
	switch method {
	case MergeMethodSquash:
		return "squash"
	case MergeMethodRebase:
		return "rebase"
	case MergeMethodMerge:
		fallthrough
	default:
		return h.getMergeType(result)
	}
}

// commitToInfo converts a commit object to CommitInfo
func (h *MergeOperationsHandler) commitToInfo(commit *object.Commit) *common.CommitInfo {
	return &common.CommitInfo{
		SHA:       commit.Hash.String(),
		Message:   commit.Message,
		Author:    common.AuthorInfo{Name: commit.Author.Name, Email: commit.Author.Email, Date: commit.Author.When},
		Committer: common.AuthorInfo{Name: commit.Committer.Name, Email: commit.Committer.Email, Date: commit.Committer.When},
		CreatedAt: commit.Author.When,
	}
}

// resolveMergeTree determines which tree to use based on merge strategy and conflict status
// This function centralizes the strategy resolution logic used by both regular and squash merges
func (h *MergeOperationsHandler) resolveMergeTree(
	ctx context.Context,
	repo *gogit.Repository,
	sourceCommit, targetCommit *object.Commit,
	analysis *git.MergeAnalysisResult,
	strategy MergeStrategy,
	forceConflict bool,
	isSquash bool,
) (plumbing.Hash, error) {
	// If there are conflicts and we're not forcing, we should not call this function
	// But handle it gracefully anyway
	if analysis.HasConflicts && !forceConflict {
		if isSquash {
			// For squash, prefer source changes when not forcing
			return sourceCommit.TreeHash, nil
		}
		return targetCommit.TreeHash, nil
	}

	// Handle conflicts with force
	if analysis.HasConflicts && forceConflict {
		h.logger.Info("Resolving conflicts using strategy",
			zap.String("strategy", string(strategy)),
			zap.Int("conflict_count", analysis.ConflictCount),
			zap.Bool("is_squash", isSquash))

		switch strategy {
		case MergeStrategyOurs:
			// Keep our version (target)
			if isSquash {
				h.logger.Warn("'ours' strategy in squash merge - keeping target tree (may lose source changes)")
			} else {
				h.logger.Debug("Using 'ours' strategy - keeping target tree")
			}
			return targetCommit.TreeHash, nil

		case MergeStrategyTheirs:
			// Keep their version (source)
			if isSquash {
				h.logger.Debug("Using 'theirs' strategy for squash - using source tree")
			} else {
				h.logger.Debug("Using 'theirs' strategy - using source tree")
			}
			return sourceCommit.TreeHash, nil

		case MergeStrategyRecursive, MergeStrategyResolve, MergeStrategyORT:
			// For recursive/resolve/ORT strategy, perform actual 3-way merge
			if analysis.AutoResolvable > 0 && analysis.AutoResolvable == analysis.ConflictCount {
				// All conflicts are auto-resolvable, create merged tree
				h.logger.Debug("Performing 3-way merge - all conflicts auto-resolvable",
					zap.String("strategy", string(strategy)))

				mergedTree, err := h.createMergedTree(ctx, repo, sourceCommit, targetCommit, analysis, strategy)
				if err != nil {
					fallbackTree := sourceCommit.TreeHash
					if !isSquash {
						fallbackTree = targetCommit.TreeHash
					}
					h.logger.Error("Failed to create merged tree, falling back",
						zap.Error(err),
						zap.Bool("is_squash", isSquash))
					return fallbackTree, nil
				}
				h.logger.Info("Successfully created merged tree",
					zap.String("merged_tree", mergedTree.String()),
					zap.String("strategy", string(strategy)))
				return mergedTree, nil
			} else if analysis.AutoResolvable > 0 {
				// Mixed auto-resolvable and manual conflicts
				h.logger.Warn("Not all conflicts auto-resolvable - attempting partial merge",
					zap.Int("auto_resolvable", analysis.AutoResolvable),
					zap.Int("total_conflicts", analysis.ConflictCount),
					zap.String("strategy", string(strategy)))

				// Attempt to create merged tree with auto-resolvable conflicts
				mergedTree, err := h.createMergedTree(ctx, repo, sourceCommit, targetCommit, analysis, strategy)
				if err != nil {
					fallbackTree := sourceCommit.TreeHash
					if !isSquash {
						fallbackTree = targetCommit.TreeHash
					}
					h.logger.Error("Failed to create partially merged tree, falling back",
						zap.Error(err),
						zap.Bool("is_squash", isSquash))
					return fallbackTree, nil
				}
				h.logger.Info("Successfully created partially merged tree",
					zap.String("merged_tree", mergedTree.String()),
					zap.String("strategy", string(strategy)))
				return mergedTree, nil
			} else {
				// No auto-resolvable conflicts
				fallbackTree := sourceCommit.TreeHash
				if !isSquash {
					fallbackTree = targetCommit.TreeHash
				}
				h.logger.Warn("No auto-resolvable conflicts - using fallback tree",
					zap.Bool("is_squash", isSquash),
					zap.String("strategy", string(strategy)))
				return fallbackTree, nil
			}

		case MergeStrategySubtree:
			// Subtree merge uses source tree
			h.logger.Debug("Using 'subtree' strategy - using source tree")
			return sourceCommit.TreeHash, nil

		default:
			// Fallback based on merge method
			fallbackTree := sourceCommit.TreeHash
			if !isSquash {
				fallbackTree = targetCommit.TreeHash
			}
			h.logger.Warn("Unknown strategy - defaulting to fallback tree",
				zap.String("strategy", string(strategy)),
				zap.Bool("is_squash", isSquash))
			return fallbackTree, nil
		}
	}

	// No conflicts - perform proper 3-way merge even when clean
	// This ensures we get the correct merged tree structure
	h.logger.Debug("No conflicts - performing 3-way merge",
		zap.String("strategy", string(strategy)),
		zap.Bool("is_squash", isSquash))

	mergedTree, err := h.createMergedTree(ctx, repo, sourceCommit, targetCommit, analysis, strategy)
	if err != nil {
		// Fallback based on merge method
		fallbackTree := sourceCommit.TreeHash
		if !isSquash {
			fallbackTree = targetCommit.TreeHash
		}
		h.logger.Warn("Failed to create merged tree for clean merge, using fallback",
			zap.Error(err),
			zap.Bool("is_squash", isSquash))
		return fallbackTree, nil
	}

	h.logger.Debug("Successfully created merged tree for clean merge",
		zap.String("merged_tree", mergedTree.String()),
		zap.String("strategy", string(strategy)))
	return mergedTree, nil
}

// createMergedTreeWithBase creates a new tree by performing 3-way merge with an explicit base commit
// This is used for rebase operations where we know the base commit (the parent of the commit being rebased)
func (h *MergeOperationsHandler) createMergedTreeWithBase(
	ctx context.Context,
	repo *gogit.Repository,
	sourceCommit, targetCommit, baseCommit *object.Commit,
	analysis *git.MergeAnalysisResult,
	strategy MergeStrategy,
) (plumbing.Hash, error) {
	if baseCommit == nil {
		return plumbing.ZeroHash, fmt.Errorf("base commit cannot be nil")
	}

	// Get trees for base, source, and target
	baseTree, err := baseCommit.Tree()
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to get base tree: %w", err)
	}

	sourceTree, err := sourceCommit.Tree()
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to get source tree: %w", err)
	}

	targetTree, err := targetCommit.Tree()
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to get target tree: %w", err)
	}

	h.logger.Debug("Starting 3-way merge tree creation",
		zap.String("base", baseCommit.Hash.String()),
		zap.String("source", sourceCommit.Hash.String()),
		zap.String("target", targetCommit.Hash.String()))

	// Build conflict map for quick lookup
	conflictMap := make(map[string]*git.FileConflict)
	if analysis != nil {
		for _, conflict := range analysis.FileConflicts {
			conflictMap[conflict.FilePath] = &conflict
		}
	}

	// Merge trees recursively starting from root
	treeEntries, err := h.mergeTreeEntries(ctx, repo, baseTree, sourceTree, targetTree, "", conflictMap, strategy)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to merge tree entries: %w", err)
	}

	// Sort and store the merged tree
	sortTreeEntries(treeEntries)
	treeHash, err := h.createAndStoreTree(repo, treeEntries)
	if err != nil {
		return plumbing.ZeroHash, err
	}

	h.logger.Info("Successfully created merged tree",
		zap.String("tree_hash", treeHash.String()),
		zap.Int("entries", len(treeEntries)))

	return treeHash, nil
}

// createMergedTree creates a new tree by performing 3-way merge
func (h *MergeOperationsHandler) createMergedTree(
	ctx context.Context,
	repo *gogit.Repository,
	sourceCommit, targetCommit *object.Commit,
	analysis *git.MergeAnalysisResult,
	strategy MergeStrategy,
) (plumbing.Hash, error) {
	// Find merge base (common ancestor)
	mergeBases, err := sourceCommit.MergeBase(targetCommit)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to find merge base: %w", err)
	}
	if len(mergeBases) == 0 {
		return plumbing.ZeroHash, fmt.Errorf("no common ancestor found")
	}

	baseCommit := mergeBases[0]
	return h.createMergedTreeWithBase(ctx, repo, sourceCommit, targetCommit, baseCommit, analysis, strategy)
}

// mergeFileEntry represents a file's state in base, source, and target
type mergeFileEntry struct {
	path       string
	baseHash   plumbing.Hash
	baseMode   filemode.FileMode
	sourceHash plumbing.Hash
	sourceMode filemode.FileMode
	targetHash plumbing.Hash
	targetMode filemode.FileMode
}

// resolveFileForMerge determines which version of a file to use in the merged tree
func (h *MergeOperationsHandler) resolveFileForMerge(
	entry *mergeFileEntry,
	conflict *git.FileConflict,
	strategy MergeStrategy,
) (plumbing.Hash, filemode.FileMode, error) {
	baseExists := !entry.baseHash.IsZero()
	sourceExists := !entry.sourceHash.IsZero()
	targetExists := !entry.targetHash.IsZero()

	// Case 1: File doesn't exist in any tree (shouldn't happen)
	if !baseExists && !sourceExists && !targetExists {
		return plumbing.ZeroHash, 0, nil
	}

	// Case 2: File only exists in source (new file in source)
	if !baseExists && !targetExists && sourceExists {
		h.logger.Debug("New file in source", zap.String("path", entry.path))
		return entry.sourceHash, entry.sourceMode, nil
	}

	// Case 3: File only exists in target (new file in target)
	if !baseExists && !sourceExists && targetExists {
		h.logger.Debug("New file in target", zap.String("path", entry.path))
		return entry.targetHash, entry.targetMode, nil
	}

	// Case 4: File deleted in source but exists in target
	// Note: This case applies when the file was explicitly deleted in source.
	// If the file is unchanged between base and target, we should preserve it
	// (the directory might not exist in source, which is different from explicit deletion).
	// For recursive/ORT strategies, prefer preserving unchanged files from base/target.
	if baseExists && !sourceExists && targetExists {
		if entry.baseHash == entry.targetHash {
			// Target unchanged from base. For recursive/ORT strategies, preserve the file
			// because the directory might not exist in source (not an explicit deletion).
			// For "theirs" strategy, respect the deletion.
			if strategy == MergeStrategyTheirs {
				h.logger.Debug("File deleted in source, unchanged in target - respecting deletion (theirs strategy)", zap.String("path", entry.path))
				return plumbing.ZeroHash, 0, nil
			}
			// For recursive/ORT/ours strategies, preserve the file
			h.logger.Debug("File missing in source but unchanged in target - preserving target (directory may not exist in source)", zap.String("path", entry.path))
			return entry.targetHash, entry.targetMode, nil
		}
		// Conflict: modified in target, deleted in source
		h.logger.Warn("Conflict: modified in target, deleted in source", zap.String("path", entry.path))
		if strategy == MergeStrategyTheirs {
			return entry.sourceHash, entry.sourceMode, nil // Keep deletion
		}
		return entry.targetHash, entry.targetMode, nil // Keep target (ours)
	}

	// Case 5: File deleted in target but exists in source
	if baseExists && sourceExists && !targetExists {
		if entry.baseHash == entry.sourceHash {
			// Source unchanged, respect target deletion
			h.logger.Debug("File deleted in target, unchanged in source", zap.String("path", entry.path))
			return plumbing.ZeroHash, 0, nil
		}
		// Conflict: modified in source, deleted in target
		h.logger.Warn("Conflict: modified in source, deleted in target", zap.String("path", entry.path))
		if strategy == MergeStrategyTheirs {
			return entry.sourceHash, entry.sourceMode, nil // Keep source
		}
		return plumbing.ZeroHash, 0, nil // Keep deletion (ours)
	}

	// Case 6: File exists in all three (most common case)
	if baseExists && sourceExists && targetExists {
		// Both unchanged from base
		if entry.baseHash == entry.sourceHash && entry.baseHash == entry.targetHash {
			h.logger.Debug("File unchanged in both branches", zap.String("path", entry.path))
			return entry.targetHash, entry.targetMode, nil
		}

		// Only source changed
		if entry.baseHash != entry.sourceHash && entry.baseHash == entry.targetHash {
			h.logger.Debug("File only changed in source", zap.String("path", entry.path))
			return entry.sourceHash, entry.sourceMode, nil
		}

		// Only target changed
		if entry.baseHash == entry.sourceHash && entry.baseHash != entry.targetHash {
			h.logger.Debug("File only changed in target", zap.String("path", entry.path))
			return entry.targetHash, entry.targetMode, nil
		}

		// Both changed to same content (identical changes)
		if entry.sourceHash == entry.targetHash {
			h.logger.Debug("File changed identically in both branches", zap.String("path", entry.path))
			return entry.sourceHash, entry.sourceMode, nil
		}

		// Both changed differently - conflict!
		h.logger.Warn("Conflict: file changed differently in both branches", zap.String("path", entry.path))

		// Check if conflict is auto-resolvable
		if conflict != nil && conflict.AutoResolvable {
			h.logger.Debug("Auto-resolvable conflict, using source", zap.String("path", entry.path))
			return entry.sourceHash, entry.sourceMode, nil
		}

		// Use strategy to resolve
		switch strategy {
		case MergeStrategyTheirs:
			return entry.sourceHash, entry.sourceMode, nil
		case MergeStrategyOurs:
			return entry.targetHash, entry.targetMode, nil
		case MergeStrategyRecursive, MergeStrategyResolve, MergeStrategyORT:
			// For recursive/resolve/ORT strategies, prefer source version
			// ORT (Optimized Recursive Three-way) is an improved version of recursive
			return entry.sourceHash, entry.sourceMode, nil
		default:
			// Default to source for other strategies
			return entry.sourceHash, entry.sourceMode, nil
		}
	}

	// Case 7: File exists in base and source but not target
	if baseExists && sourceExists && !targetExists {
		h.logger.Debug("File exists in base and source, deleted in target", zap.String("path", entry.path))
		return plumbing.ZeroHash, 0, nil // Respect deletion
	}

	// Case 8: File exists in base and target but not source
	if baseExists && !sourceExists && targetExists {
		// If target unchanged from base, keep target (don't respect source deletion)
		// This is because if the entire directory doesn't exist in source,
		// we should preserve files that exist in both base and target
		if entry.baseHash == entry.targetHash {
			h.logger.Debug("File exists in base and target but not source, keeping target", zap.String("path", entry.path))
			return entry.targetHash, entry.targetMode, nil // Keep target
		}
		// Conflict: modified in target, deleted in source
		h.logger.Warn("Conflict: modified in target, deleted in source", zap.String("path", entry.path))
		if strategy == MergeStrategyTheirs {
			return plumbing.ZeroHash, 0, nil // Keep deletion
		}
		return entry.targetHash, entry.targetMode, nil // Keep target (ours)
	}

	// Default: keep target version
	if targetExists {
		return entry.targetHash, entry.targetMode, nil
	}
	if sourceExists {
		return entry.sourceHash, entry.sourceMode, nil
	}

	return plumbing.ZeroHash, 0, nil
}

// mergeTreeEntry represents a tree entry's state in base, source, and target
type mergeTreeEntry struct {
	name        string
	baseEntry   *object.TreeEntry
	sourceEntry *object.TreeEntry
	targetEntry *object.TreeEntry
}

// collectTreeEntries collects all entries from three trees into a map
func (h *MergeOperationsHandler) collectTreeEntries(baseTree, sourceTree, targetTree *object.Tree) map[string]*mergeTreeEntry {
	entryMap := make(map[string]*mergeTreeEntry)

	// Helper to add entry to map
	addEntry := func(name string, entry *object.TreeEntry, setter func(*mergeTreeEntry)) {
		if mergeEntry, exists := entryMap[name]; exists {
			setter(mergeEntry)
		} else {
			mergeEntry = &mergeTreeEntry{name: name}
			setter(mergeEntry)
			entryMap[name] = mergeEntry
		}
	}

	// Collect from base tree
	for i := range baseTree.Entries {
		entry := &baseTree.Entries[i]
		addEntry(entry.Name, entry, func(me *mergeTreeEntry) { me.baseEntry = entry })
	}

	// Collect from source tree
	for i := range sourceTree.Entries {
		entry := &sourceTree.Entries[i]
		addEntry(entry.Name, entry, func(me *mergeTreeEntry) { me.sourceEntry = entry })
	}

	// Collect from target tree
	for i := range targetTree.Entries {
		entry := &targetTree.Entries[i]
		addEntry(entry.Name, entry, func(me *mergeTreeEntry) { me.targetEntry = entry })
	}

	return entryMap
}

// sortTreeEntries sorts tree entries by name (required by Git)
func sortTreeEntries(entries []object.TreeEntry) {
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[i].Name > entries[j].Name {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}
}

// createAndStoreTree creates and stores a tree object in the repository
func (h *MergeOperationsHandler) createAndStoreTree(repo *gogit.Repository, entries []object.TreeEntry) (plumbing.Hash, error) {
	newTree := &object.Tree{Entries: entries}
	treeObj := repo.Storer.NewEncodedObject()
	if err := newTree.Encode(treeObj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to encode tree: %w", err)
	}
	treeHash, err := repo.Storer.SetEncodedObject(treeObj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to store tree: %w", err)
	}
	return treeHash, nil
}

// getOrCreateEmptyTree gets an existing tree or returns nil if entry is nil
func (h *MergeOperationsHandler) getOrCreateEmptyTree(repo *gogit.Repository, entry *object.TreeEntry) (*object.Tree, error) {
	if entry == nil {
		return nil, nil
	}
	if entry.Mode != filemode.Dir {
		return nil, fmt.Errorf("entry is not a directory")
	}
	return repo.TreeObject(entry.Hash)
}

// createEmptyTree creates and stores an empty tree in the repository
func (h *MergeOperationsHandler) createEmptyTree(repo *gogit.Repository) (*object.Tree, error) {
	emptyTree := &object.Tree{Entries: []object.TreeEntry{}}
	treeObj := repo.Storer.NewEncodedObject()
	if err := emptyTree.Encode(treeObj); err != nil {
		return nil, fmt.Errorf("failed to encode empty tree: %w", err)
	}
	emptyTreeHash, err := repo.Storer.SetEncodedObject(treeObj)
	if err != nil {
		return nil, fmt.Errorf("failed to store empty tree: %w", err)
	}
	return repo.TreeObject(emptyTreeHash)
}

// mergeTreeEntries recursively merges tree entries from base, source, and target trees
func (h *MergeOperationsHandler) mergeTreeEntries(
	ctx context.Context,
	repo *gogit.Repository,
	baseTree, sourceTree, targetTree *object.Tree,
	prefix string,
	conflictMap map[string]*git.FileConflict,
	strategy MergeStrategy,
) ([]object.TreeEntry, error) {
	// Collect all entries from all three trees
	entryMap := h.collectTreeEntries(baseTree, sourceTree, targetTree)

	// Resolve each entry
	var resolvedEntries []object.TreeEntry

	for _, mergeEntry := range entryMap {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		resolvedEntry, err := h.resolveTreeEntry(ctx, repo, mergeEntry, prefix, conflictMap, strategy)
		if err != nil {
			h.logger.Warn("Failed to resolve tree entry, skipping",
				zap.String("name", mergeEntry.name),
				zap.String("prefix", prefix),
				zap.Error(err))
			continue
		}

		if resolvedEntry != nil {
			resolvedEntries = append(resolvedEntries, *resolvedEntry)
		}
	}

	// Sort tree entries by name (required by Git)
	sortTreeEntries(resolvedEntries)

	return resolvedEntries, nil
}

// resolveTreeEntry resolves a single tree entry (file or directory) for merge
func (h *MergeOperationsHandler) resolveTreeEntry(
	ctx context.Context,
	repo *gogit.Repository,
	mergeEntry *mergeTreeEntry,
	prefix string,
	conflictMap map[string]*git.FileConflict,
	strategy MergeStrategy,
) (*object.TreeEntry, error) {
	baseExists := mergeEntry.baseEntry != nil
	sourceExists := mergeEntry.sourceEntry != nil
	targetExists := mergeEntry.targetEntry != nil

	baseIsDir := baseExists && mergeEntry.baseEntry.Mode == filemode.Dir
	sourceIsDir := sourceExists && mergeEntry.sourceEntry.Mode == filemode.Dir
	targetIsDir := targetExists && mergeEntry.targetEntry.Mode == filemode.Dir

	// Determine full path for conflict lookup
	fullPath := mergeEntry.name
	if prefix != "" {
		fullPath = prefix + "/" + mergeEntry.name
	}

	// Handle type conflicts (directory vs file)
	if h.hasTypeConflict(baseIsDir, sourceIsDir, targetIsDir, sourceExists, targetExists) {
		return h.resolveTypeConflict(ctx, repo, mergeEntry, prefix, conflictMap, strategy, fullPath, baseIsDir, sourceIsDir, targetIsDir, sourceExists, targetExists)
	}

	// Handle directory entries (recursive merge)
	if baseIsDir || sourceIsDir || targetIsDir {
		return h.mergeDirectoryEntry(ctx, repo, mergeEntry, prefix, conflictMap, strategy)
	}

	// Handle file entries (use existing file merge logic)
	fileEntry := &mergeFileEntry{
		path: fullPath,
	}
	if baseExists {
		fileEntry.baseHash = mergeEntry.baseEntry.Hash
		fileEntry.baseMode = mergeEntry.baseEntry.Mode
	}
	if sourceExists {
		fileEntry.sourceHash = mergeEntry.sourceEntry.Hash
		fileEntry.sourceMode = mergeEntry.sourceEntry.Mode
	}
	if targetExists {
		fileEntry.targetHash = mergeEntry.targetEntry.Hash
		fileEntry.targetMode = mergeEntry.targetEntry.Mode
	}

	conflict := conflictMap[fullPath]
	resolvedHash, resolvedMode, err := h.resolveFileForMerge(fileEntry, conflict, strategy)
	if err != nil {
		return nil, err
	}

	if resolvedHash.IsZero() {
		return nil, nil // File was deleted
	}

	return &object.TreeEntry{
		Name: mergeEntry.name,
		Mode: resolvedMode,
		Hash: resolvedHash,
	}, nil
}

// mergeDirectoryEntry recursively merges a directory entry
func (h *MergeOperationsHandler) mergeDirectoryEntry(
	ctx context.Context,
	repo *gogit.Repository,
	mergeEntry *mergeTreeEntry,
	prefix string,
	conflictMap map[string]*git.FileConflict,
	strategy MergeStrategy,
) (*object.TreeEntry, error) {
	// Get sub-trees for base, source, and target (or create empty ones)
	baseSubTree, err := h.getOrCreateEmptyTree(repo, mergeEntry.baseEntry)
	if err != nil && mergeEntry.baseEntry != nil {
		return nil, fmt.Errorf("failed to get base subtree: %w", err)
	}

	// Handle case where source is a file instead of directory (type conflict)
	var sourceSubTree *object.Tree
	if mergeEntry.sourceEntry != nil {
		if mergeEntry.sourceEntry.Mode == filemode.Dir {
			sourceSubTree, err = h.getOrCreateEmptyTree(repo, mergeEntry.sourceEntry)
			if err != nil {
				return nil, fmt.Errorf("failed to get source subtree: %w", err)
			}
		} else {
			// Source is a file, create a tree with that file
			// This handles type conflict where directory is preferred over file
			// The file should be included in the merged directory
			// Use the source entry's name (e.g., "blob" for "tree/blob")
			fileEntries := []object.TreeEntry{
				{
					Name: mergeEntry.sourceEntry.Name, // Use the actual file name from source entry
					Mode: mergeEntry.sourceEntry.Mode,
					Hash: mergeEntry.sourceEntry.Hash,
				},
			}
			sortTreeEntries(fileEntries)
			treeHash, err := h.createAndStoreTree(repo, fileEntries)
			if err != nil {
				return nil, fmt.Errorf("failed to create tree with source file: %w", err)
			}
			sourceSubTree, err = repo.TreeObject(treeHash)
			if err != nil {
				return nil, fmt.Errorf("failed to get tree with source file: %w", err)
			}
		}
	}

	targetSubTree, err := h.getOrCreateEmptyTree(repo, mergeEntry.targetEntry)
	if err != nil && mergeEntry.targetEntry != nil {
		return nil, fmt.Errorf("failed to get target subtree: %w", err)
	}

	// If no sub-trees exist, return nil (directory was deleted)
	if baseSubTree == nil && sourceSubTree == nil && targetSubTree == nil {
		return nil, nil
	}

	// Ensure all trees are non-nil (create empty ones if needed)
	if baseSubTree == nil {
		baseSubTree, err = h.createEmptyTree(repo)
		if err != nil {
			return nil, err
		}
	}
	if sourceSubTree == nil {
		sourceSubTree, err = h.createEmptyTree(repo)
		if err != nil {
			return nil, err
		}
	}
	if targetSubTree == nil {
		targetSubTree, err = h.createEmptyTree(repo)
		if err != nil {
			return nil, err
		}
	}

	// Recursively merge sub-trees
	newPrefix := mergeEntry.name
	if prefix != "" {
		newPrefix = prefix + "/" + mergeEntry.name
	}

	mergedEntries, err := h.mergeTreeEntries(ctx, repo, baseSubTree, sourceSubTree, targetSubTree, newPrefix, conflictMap, strategy)
	if err != nil {
		return nil, fmt.Errorf("failed to merge subtree entries: %w", err)
	}

	// Check if directory should be kept even if merged entries is empty
	baseHadContent := mergeEntry.baseEntry != nil && mergeEntry.baseEntry.Mode == filemode.Dir
	targetHadContent := mergeEntry.targetEntry != nil && mergeEntry.targetEntry.Mode == filemode.Dir
	sourceHadContent := mergeEntry.sourceEntry != nil && mergeEntry.sourceEntry.Mode == filemode.Dir

	// If merged entries is empty, check if we should keep the directory
	if len(mergedEntries) == 0 {
		// If directory existed in base or target (not just source), keep it even if empty
		// This handles the case where source doesn't have the directory but base/target do
		if baseHadContent || targetHadContent {
			// Directory existed in base or target, keep it (even if empty after merge)
			// This is valid in Git - empty directories can exist
			h.logger.Debug("Keeping empty directory that existed in base or target",
				zap.String("name", mergeEntry.name),
				zap.Bool("base", baseHadContent),
				zap.Bool("target", targetHadContent))
		} else if sourceHadContent {
			// Directory only existed in source, keep it
			h.logger.Debug("Keeping empty directory that existed in source",
				zap.String("name", mergeEntry.name))
		} else {
			// Directory was never created, return nil
			h.logger.Debug("Directory was never created, returning nil",
				zap.String("name", mergeEntry.name))
			return nil, nil
		}
	} else {
		// Directory has content, log for debugging
		h.logger.Debug("Directory has merged entries",
			zap.String("name", mergeEntry.name),
			zap.Int("entry_count", len(mergedEntries)))
	}

	// Sort and store the merged tree
	sortTreeEntries(mergedEntries)
	treeHash, err := h.createAndStoreTree(repo, mergedEntries)
	if err != nil {
		return nil, fmt.Errorf("failed to create merged subtree: %w", err)
	}

	return &object.TreeEntry{
		Name: mergeEntry.name,
		Mode: filemode.Dir,
		Hash: treeHash,
	}, nil
}

// hasTypeConflict checks if there's a type conflict (directory vs file)
func (h *MergeOperationsHandler) hasTypeConflict(baseIsDir, sourceIsDir, targetIsDir, sourceExists, targetExists bool) bool {
	return (baseIsDir && sourceExists && !sourceIsDir) ||
		(baseIsDir && targetExists && !targetIsDir) ||
		(sourceIsDir && targetExists && !targetIsDir) ||
		(targetIsDir && sourceExists && !sourceIsDir)
}

// resolveTypeConflict resolves a type conflict (directory vs file) using the merge strategy
func (h *MergeOperationsHandler) resolveTypeConflict(
	ctx context.Context,
	repo *gogit.Repository,
	mergeEntry *mergeTreeEntry,
	prefix string,
	conflictMap map[string]*git.FileConflict,
	strategy MergeStrategy,
	fullPath string,
	baseIsDir, sourceIsDir, targetIsDir, sourceExists, targetExists bool,
) (*object.TreeEntry, error) {
	h.logger.Warn("Type conflict: directory vs file",
		zap.String("path", fullPath),
		zap.Bool("base_is_dir", baseIsDir),
		zap.Bool("source_is_dir", sourceIsDir),
		zap.Bool("target_is_dir", targetIsDir))

	switch strategy {
	case MergeStrategyTheirs:
		if sourceExists {
			if sourceIsDir {
				return h.mergeDirectoryEntry(ctx, repo, mergeEntry, prefix, conflictMap, strategy)
			}
			return mergeEntry.sourceEntry, nil
		}
	case MergeStrategyOurs:
		if targetExists {
			if targetIsDir {
				return h.mergeDirectoryEntry(ctx, repo, mergeEntry, prefix, conflictMap, strategy)
			}
			return mergeEntry.targetEntry, nil
		}
	default:
		// Prefer directory over file
		if sourceIsDir {
			return h.mergeDirectoryEntry(ctx, repo, mergeEntry, prefix, conflictMap, strategy)
		}
		if targetIsDir {
			return h.mergeDirectoryEntry(ctx, repo, mergeEntry, prefix, conflictMap, strategy)
		}
		// Both are files, use source
		if sourceExists {
			return mergeEntry.sourceEntry, nil
		}
		if targetExists {
			return mergeEntry.targetEntry, nil
		}
	}
	return nil, nil
}
