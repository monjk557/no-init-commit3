package protocol

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/git"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repository"
	"github.com/collabops/git-executor/internal/schemas/common"
)

// MergeOperationsHandler handles REST API operations for Git merge operations
type MergeOperationsHandler struct {
	pathConfig       *repository.PathConfig
	logger           *logging.Logger
	errorMapper      *api.GitErrorMapper
	objectManager    *git.GitObjectManager
	conflictDetector *git.ConflictDetector
}

// NewMergeOperationsHandler creates a new merge operations handler
func NewMergeOperationsHandler(cfg *config.Config) (*MergeOperationsHandler, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	pathConfig, err := repository.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	objectManager, err := git.NewGitObjectManager(pathConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create object manager: %w", err)
	}
	errorMapper := api.GetDefaultMapper()
	conflictDetector := git.NewConflictDetector(pathConfig, logger)

	return &MergeOperationsHandler{
		pathConfig:       pathConfig,
		logger:           logger,
		errorMapper:      errorMapper,
		objectManager:    objectManager,
		conflictDetector: conflictDetector,
	}, nil
}

// MergeStrategy represents different merge strategies
type MergeStrategy string

const (
	MergeStrategyRecursive MergeStrategy = "recursive" // Default three-way merge
	MergeStrategyResolve   MergeStrategy = "resolve"   // Three-way merge with aggressive resolution
	MergeStrategyOurs      MergeStrategy = "ours"      // Keep our version on conflicts
	MergeStrategyTheirs    MergeStrategy = "theirs"    // Keep their version on conflicts
	MergeStrategySubtree   MergeStrategy = "subtree"   // Subtree merge strategy
)

// MergeRequest represents the JSON payload for merge operations
type MergeRequest struct {
	SourceRef     string                 `json:"source_ref" binding:"required"` // Source branch/commit to merge FROM
	TargetRef     string                 `json:"target_ref" binding:"required"` // Target branch/commit to merge INTO
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

// HandleMerge handles POST /repos/{owner}/{repo}/merges for performing Git merges
func (h *MergeOperationsHandler) HandleMerge(c *gin.Context) {
	ctx := c.Request.Context()

	// Extract URL parameters
	owner := c.Param("owner")
	repo := c.Param("repo")

	h.logger.Info("Processing merge request",
		zap.String("owner", owner),
		zap.String("repo", repo))

	// Validate path components
	if err := h.validatePath(owner, repo); err != nil {
		apiErr := h.errorMapper.MapError(err, "validate_path")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{
			"success": false,
			"error":   apiErr.Message,
		})
		return
	}

	// Parse and validate JSON request
	var req MergeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Invalid JSON: %v", err),
		})
		return
	}

	// Set default strategy if not specified
	if req.Strategy == "" {
		req.Strategy = MergeStrategyRecursive
	}

	// Validate merge request
	if err := h.validateMergeRequest(&req); err != nil {
		if gitAPIErr, ok := err.(*api.GitAPIError); ok {
			c.JSON(gitAPIErr.HTTPStatusCode(), gin.H{
				"success": false,
				"error":   gitAPIErr.Message,
			})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   err.Error(),
			})
		}
		return
	}

	// Open repository
	repository, err := h.objectManager.OpenRepository(ctx, owner, repo)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "open_repository")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{
			"success": false,
			"error":   apiErr.Message,
		})
		return
	}

	// Resolve source and target commits
	sourceCommit, err := h.resolveReference(repository, req.SourceRef)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "resolve_source_ref")
		_ = apiErr.WithDetails("source_ref", req.SourceRef)
		c.JSON(apiErr.HTTPStatusCode(), gin.H{
			"success": false,
			"error":   apiErr.Message,
		})
		return
	}

	targetCommit, err := h.resolveReference(repository, req.TargetRef)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "resolve_target_ref")
		_ = apiErr.WithDetails("target_ref", req.TargetRef)
		c.JSON(apiErr.HTTPStatusCode(), gin.H{
			"success": false,
			"error":   apiErr.Message,
		})
		return
	}

	h.logger.Debug("Resolved merge references",
		zap.String("source_commit", sourceCommit.String()),
		zap.String("target_commit", targetCommit.String()))

	// Check if source and target are the same
	if sourceCommit == targetCommit {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "Source and target references point to the same commit",
		})
		return
	}

	// Perform merge analysis first
	conflictAnalysis, err := h.analyzeMerge(ctx, repository, sourceCommit, targetCommit, &req)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "analyze_merge")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// Handle dry run
	if req.DryRun {
		response := &MergeResponse{
			Success:         !conflictAnalysis.HasConflicts || req.ForceConflict,
			IsFastForward:   conflictAnalysis.IsFastForward,
			HasConflicts:    conflictAnalysis.HasConflicts,
			ConflictCount:   conflictAnalysis.ConflictCount,
			Strategy:        req.Strategy,
			Message:         "Dry run completed - no changes made",
			ConflictSummary: conflictAnalysis,
			Details: map[string]interface{}{
				"dry_run":               true,
				"can_merge":             conflictAnalysis.CanMerge,
				"auto_resolvable_count": conflictAnalysis.AutoResolvable,
				"estimated_time":        conflictAnalysis.EstimatedTime.String(),
				"recommendations":       conflictAnalysis.Recommendations,
			},
		}
		h.SendSuccess(c, response, "Merge analysis completed successfully")
		return
	}

	// Check for conflicts (unless force is specified)
	if conflictAnalysis.HasConflicts && !req.ForceConflict {
		apiErr := api.NewBadRequestError(fmt.Sprintf("Merge has %d conflicts. Use force_conflict=true to override", conflictAnalysis.ConflictCount))
		_ = apiErr.WithDetails("conflict_count", conflictAnalysis.ConflictCount)
		_ = apiErr.WithDetails("conflict_summary", conflictAnalysis)
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// Perform the actual merge
	mergeResult, err := h.performMerge(ctx, repository, sourceCommit, targetCommit, &req, conflictAnalysis)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "perform_merge")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// Prepare response
	response := &MergeResponse{
		Success:       true,
		IsFastForward: mergeResult.IsFastForward,
		HasConflicts:  mergeResult.HasConflicts,
		ConflictCount: 0, // Successful merge means conflicts were resolved
		Strategy:      req.Strategy,
		Message:       h.generateMergeMessage(mergeResult, &req),
	}

	if mergeResult.MergeCommit != nil {
		response.MergeCommit = h.commitToInfo(mergeResult.MergeCommit)
	}

	// Add additional details
	details := map[string]interface{}{
		"source_ref":    req.SourceRef,
		"target_ref":    req.TargetRef,
		"merge_type":    h.getMergeType(mergeResult),
		"strategy_used": string(req.Strategy),
	}

	if req.Message != "" {
		details["custom_message"] = req.Message
	}

	if mergeResult.HasConflicts {
		details["conflicts_resolved"] = true
		details["force_applied"] = req.ForceConflict
	}

	response.Details = details

	// Log success
	h.logger.Info("Merge completed successfully",
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.String("source_ref", req.SourceRef),
		zap.String("target_ref", req.TargetRef),
		zap.Bool("fast_forward", mergeResult.IsFastForward),
		zap.Bool("had_conflicts", mergeResult.HasConflicts),
		zap.String("strategy", string(req.Strategy)))

	// Send success response
	h.SendSuccess(c, response, response.Message)
}

// validatePath validates the owner and repo path components
func (h *MergeOperationsHandler) validatePath(owner, repo string) error {
	// Check for empty components
	if owner == "" {
		return api.NewBadRequestError("owner parameter is required")
	}
	if repo == "" {
		return api.NewBadRequestError("repo parameter is required")
	}

	// Check path traversal and security
	pathValidator := repository.NewGitAPIPathValidator(h.pathConfig)
	return pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo))
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

	// Validate strategy
	switch req.Strategy {
	case MergeStrategyRecursive, MergeStrategyResolve, MergeStrategyOurs, MergeStrategyTheirs, MergeStrategySubtree:
		// Valid strategy
	default:
		return api.NewBadRequestError(fmt.Sprintf("Invalid merge strategy: %s", req.Strategy))
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

// resolveReference resolves a Git reference to a commit hash
func (h *MergeOperationsHandler) resolveReference(repo *gogit.Repository, ref string) (plumbing.Hash, error) {
	// Try to resolve as hash first
	if hash := plumbing.NewHash(ref); !hash.IsZero() {
		// Verify the commit exists
		_, err := repo.CommitObject(hash)
		if err == nil {
			return hash, nil
		}
	}

	// Try to resolve as reference
	reference, err := repo.Reference(plumbing.ReferenceName(ref), true)
	if err == nil {
		return reference.Hash(), nil
	}

	// Try with refs/heads/ prefix for branch names
	if !strings.HasPrefix(ref, "refs/") {
		branchRef := "refs/heads/" + ref
		reference, err := repo.Reference(plumbing.ReferenceName(branchRef), true)
		if err == nil {
			return reference.Hash(), nil
		}

		// Try with refs/tags/ prefix for tag names
		tagRef := "refs/tags/" + ref
		reference, err = repo.Reference(plumbing.ReferenceName(tagRef), true)
		if err == nil {
			return reference.Hash(), nil
		}
	}

	return plumbing.ZeroHash, fmt.Errorf("unable to resolve reference: %s", ref)
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

// performMerge performs the actual Git merge operation
func (h *MergeOperationsHandler) performMerge(ctx context.Context, repo *gogit.Repository, sourceHash, targetHash plumbing.Hash, req *MergeRequest, analysis *git.MergeAnalysisResult) (*MergeResult, error) {
	result := &MergeResult{
		IsFastForward:    analysis.IsFastForward,
		HasConflicts:     analysis.HasConflicts,
		ConflictAnalysis: analysis,
	}

	// Handle fast-forward merge
	if analysis.IsFastForward {
		err := h.performFastForwardMerge(repo, targetHash, sourceHash, req.TargetRef)
		if err != nil {
			return nil, fmt.Errorf("fast-forward merge failed: %w", err)
		}

		// Get the source commit as the result
		commit, err := repo.CommitObject(sourceHash)
		if err != nil {
			return nil, fmt.Errorf("failed to get source commit: %w", err)
		}
		result.MergeCommit = commit
		return result, nil
	}

	// Perform three-way merge
	mergeCommit, err := h.performThreeWayMerge(ctx, repo, sourceHash, targetHash, req, analysis)
	if err != nil {
		return nil, fmt.Errorf("three-way merge failed: %w", err)
	}

	result.MergeCommit = mergeCommit
	return result, nil
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

	// Determine which tree to use based on strategy and conflict status
	var mergeTreeHash plumbing.Hash

	if analysis.HasConflicts && req.ForceConflict {
		// When forcing conflicts, use strategy to determine which tree to use
		h.logger.Info("Resolving conflicts using strategy",
			zap.String("strategy", string(req.Strategy)),
			zap.Int("conflict_count", analysis.ConflictCount))

		switch req.Strategy {
		case MergeStrategyOurs:
			// Keep our version (target)
			mergeTreeHash = targetCommit.TreeHash
			h.logger.Debug("Using 'ours' strategy - keeping target tree")

		case MergeStrategyTheirs:
			// Keep their version (source)
			mergeTreeHash = sourceCommit.TreeHash
			h.logger.Debug("Using 'theirs' strategy - using source tree")

		case MergeStrategyRecursive, MergeStrategyResolve:
			// For recursive/resolve strategy, perform actual 3-way merge
			if analysis.AutoResolvable > 0 && analysis.AutoResolvable == analysis.ConflictCount {
				// All conflicts are auto-resolvable, create merged tree
				h.logger.Debug("Performing 3-way merge - all conflicts auto-resolvable")

				mergedTree, err := h.createMergedTree(ctx, repo, sourceCommit, targetCommit, analysis, req.Strategy)
				if err != nil {
					h.logger.Error("Failed to create merged tree, falling back to source tree",
						zap.Error(err))
					mergeTreeHash = sourceCommit.TreeHash
				} else {
					mergeTreeHash = mergedTree
					h.logger.Info("Successfully created merged tree",
						zap.String("merged_tree", mergedTree.String()))
				}
			} else if analysis.AutoResolvable > 0 {
				// Mixed auto-resolvable and manual conflicts
				h.logger.Warn("Not all conflicts auto-resolvable - attempting partial merge",
					zap.Int("auto_resolvable", analysis.AutoResolvable),
					zap.Int("total_conflicts", analysis.ConflictCount))

				// Attempt to create merged tree with auto-resolvable conflicts
				mergedTree, err := h.createMergedTree(ctx, repo, sourceCommit, targetCommit, analysis, req.Strategy)
				if err != nil {
					h.logger.Error("Failed to create partially merged tree, falling back to target tree",
						zap.Error(err))
					mergeTreeHash = targetCommit.TreeHash
				} else {
					mergeTreeHash = mergedTree
					h.logger.Info("Successfully created partially merged tree",
						zap.String("merged_tree", mergedTree.String()))
				}
			} else {
				// No auto-resolvable conflicts, cannot safely merge
				h.logger.Warn("No auto-resolvable conflicts - using target tree")
				mergeTreeHash = targetCommit.TreeHash
			}

		case MergeStrategySubtree:
			// Subtree merge uses source tree
			mergeTreeHash = sourceCommit.TreeHash
			h.logger.Debug("Using 'subtree' strategy - using source tree")

		default:
			// Fallback to target tree for unknown strategies
			mergeTreeHash = targetCommit.TreeHash
			h.logger.Warn("Unknown strategy - defaulting to target tree",
				zap.String("strategy", string(req.Strategy)))
		}
	} else if analysis.HasConflicts && !req.ForceConflict {
		// This should not happen as we check for conflicts earlier
		return nil, fmt.Errorf("conflicts detected but force_conflict is false")
	} else {
		// No conflicts - use source tree for clean merge
		mergeTreeHash = sourceCommit.TreeHash
		h.logger.Debug("No conflicts - using source tree for clean merge")
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
		author = &object.Signature{
			Name:  req.Author.Name,
			Email: req.Author.Email,
			When:  req.Author.Date,
		}
	}

	if req.Committer != nil {
		committer = &object.Signature{
			Name:  req.Committer.Name,
			Email: req.Committer.Email,
			When:  req.Committer.Date,
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

	// Create a map to track all files from all three trees
	fileMap := make(map[string]*mergeFileEntry)

	// Collect all files from base tree
	_ = baseTree.Files().ForEach(func(f *object.File) error {
		fileMap[f.Name] = &mergeFileEntry{
			path:     f.Name,
			baseHash: f.Hash,
			baseMode: f.Mode,
		}
		return nil
	})

	// Collect all files from source tree
	_ = sourceTree.Files().ForEach(func(f *object.File) error {
		if entry, exists := fileMap[f.Name]; exists {
			entry.sourceHash = f.Hash
			entry.sourceMode = f.Mode
		} else {
			fileMap[f.Name] = &mergeFileEntry{
				path:       f.Name,
				sourceHash: f.Hash,
				sourceMode: f.Mode,
			}
		}
		return nil
	})

	// Collect all files from target tree
	_ = targetTree.Files().ForEach(func(f *object.File) error {
		if entry, exists := fileMap[f.Name]; exists {
			entry.targetHash = f.Hash
			entry.targetMode = f.Mode
		} else {
			fileMap[f.Name] = &mergeFileEntry{
				path:       f.Name,
				targetHash: f.Hash,
				targetMode: f.Mode,
			}
		}
		return nil
	})

	// Build conflict map for quick lookup
	conflictMap := make(map[string]*git.FileConflict)
	for _, conflict := range analysis.FileConflicts {
		conflictMap[conflict.FilePath] = &conflict
	}

	// Create new tree entries by resolving each file
	var treeEntries []object.TreeEntry

	for path, entry := range fileMap {
		resolvedHash, resolvedMode, err := h.resolveFileForMerge(entry, conflictMap[path], strategy)
		if err != nil {
			h.logger.Warn("Failed to resolve file, skipping",
				zap.String("path", path),
				zap.Error(err))
			continue
		}

		if !resolvedHash.IsZero() {
			treeEntries = append(treeEntries, object.TreeEntry{
				Name: path,
				Mode: resolvedMode,
				Hash: resolvedHash,
			})
		}
	}

	// Sort tree entries by name (required by Git)
	// Using a simple bubble sort for clarity
	for i := 0; i < len(treeEntries); i++ {
		for j := i + 1; j < len(treeEntries); j++ {
			if treeEntries[i].Name > treeEntries[j].Name {
				treeEntries[i], treeEntries[j] = treeEntries[j], treeEntries[i]
			}
		}
	}

	// Create and store the new tree
	newTree := &object.Tree{
		Entries: treeEntries,
	}

	treeObj := repo.Storer.NewEncodedObject()
	if err := newTree.Encode(treeObj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to encode merged tree: %w", err)
	}

	treeHash, err := repo.Storer.SetEncodedObject(treeObj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to store merged tree: %w", err)
	}

	h.logger.Info("Successfully created merged tree",
		zap.String("tree_hash", treeHash.String()),
		zap.Int("entries", len(treeEntries)))

	return treeHash, nil
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
	if baseExists && !sourceExists && targetExists {
		if entry.baseHash == entry.targetHash {
			// Target unchanged, respect source deletion
			h.logger.Debug("File deleted in source, unchanged in target", zap.String("path", entry.path))
			return plumbing.ZeroHash, 0, nil
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
		default:
			// Default to source for recursive strategy
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
		h.logger.Debug("File exists in base and target, deleted in source", zap.String("path", entry.path))
		return plumbing.ZeroHash, 0, nil // Respect deletion
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

// SendSuccess sends a successful JSON response
func (h *MergeOperationsHandler) SendSuccess(c *gin.Context, data interface{}, message string) {
	response := map[string]interface{}{
		"success": true,
		"message": message,
		"data":    data,
	}
	c.JSON(http.StatusOK, response)
}
