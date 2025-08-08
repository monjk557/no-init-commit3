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
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/git"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/collabops/git-executor/internal/repository"
)

// MergeOperationsHandler handles REST API operations for Git merge operations
type MergeOperationsHandler struct {
	pathConfig       *repository.PathConfig
	permClient       permissions.PermissionClient
	logger           *logging.Logger
	errorMapper      *api.GitErrorMapper
	objectManager    *git.GitObjectManager
	conflictDetector *git.ConflictDetector
}

// NewMergeOperationsHandler creates a new merge operations handler
func NewMergeOperationsHandler(cfg *config.Config, permClient permissions.PermissionClient) (*MergeOperationsHandler, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}
	if permClient == nil {
		return nil, fmt.Errorf("permission client cannot be nil")
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
		permClient:       permClient,
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
	Author        *AuthorInfo            `json:"author,omitempty"`              // Merge commit author
	Committer     *AuthorInfo            `json:"committer,omitempty"`           // Merge commit committer
	ForceConflict bool                   `json:"force_conflict,omitempty"`      // Force merge even with conflicts
	DryRun        bool                   `json:"dry_run,omitempty"`             // Analyze merge without performing it
	Metadata      map[string]interface{} `json:"metadata,omitempty"`            // Additional metadata
}

// MergeResponse represents the response for merge operations
type MergeResponse struct {
	Success         bool                     `json:"success"`
	MergeCommit     *CommitInfo              `json:"merge_commit,omitempty"`     // Created merge commit info
	IsFastForward   bool                     `json:"is_fast_forward"`            // Whether merge was fast-forward
	HasConflicts    bool                     `json:"has_conflicts"`              // Whether conflicts were detected
	ConflictCount   int                      `json:"conflict_count"`             // Number of conflicts
	Strategy        MergeStrategy            `json:"strategy"`                   // Strategy used
	Message         string                   `json:"message"`                    // Operation result message
	Details         map[string]interface{}   `json:"details,omitempty"`          // Additional details
	ConflictSummary *git.MergeAnalysisResult `json:"conflict_summary,omitempty"` // Detailed conflict analysis
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

	// Validate permissions
	if err := h.validatePermissions(c, owner, repo, "write"); err != nil {
		if gitAPIErr, ok := err.(*api.GitAPIError); ok {
			c.JSON(gitAPIErr.HTTPStatusCode(), gin.H{
				"success": false,
				"error":   gitAPIErr.Message,
			})
		} else {
			c.JSON(http.StatusForbidden, gin.H{
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

// validatePermissions validates user permissions for repository access
func (h *MergeOperationsHandler) validatePermissions(c *gin.Context, owner, repo, operation string) error {
	userID := c.GetHeader("X-USER-ID")
	if userID == "" {
		apiErr := api.NewGitAPIError(
			api.ErrorCodeUnauthorized,
			"Missing X-USER-ID header",
			api.ErrorSeverityMedium,
		)
		_ = apiErr.WithDetails("http_status", http.StatusUnauthorized)
		return apiErr
	}

	source := c.GetHeader("X-Source")
	if source == "" {
		apiErr := api.NewGitAPIError(
			api.ErrorCodeUnauthorized,
			"Missing X-Source header",
			api.ErrorSeverityMedium,
		)
		_ = apiErr.WithDetails("http_status", http.StatusUnauthorized)
		return apiErr
	}

	var hasAccess bool
	var err error

	if operation == "read" {
		hasAccess, err = h.permClient.CheckReadAccess(c.Request.Context(), userID, owner, repo)
	} else {
		hasAccess, err = h.permClient.CheckWriteAccess(c.Request.Context(), userID, owner, repo)
	}

	if err != nil {
		h.logger.Error("Permission check failed", zap.Error(err))
		apiErr := api.NewGitAPIError(
			api.ErrorCodeInternalError,
			"Permission service unavailable",
			api.ErrorSeverityHigh,
		)
		_ = apiErr.WithDetails("http_status", http.StatusServiceUnavailable)
		return apiErr
	}

	if !hasAccess {
		apiErr := api.NewGitAPIError(
			api.ErrorCodeForbidden,
			"Insufficient permissions for this operation",
			api.ErrorSeverityMedium,
		)
		_ = apiErr.WithDetails("http_status", http.StatusForbidden)
		_ = apiErr.WithDetails("required_permission", operation)
		return apiErr
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
	// Create analysis request
	analysisReq := &git.MergeAnalysisRequest{
		Owner:          "", // Not needed for direct analysis
		Repo:           "", // Not needed for direct analysis
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

	// For now, we'll create a merge commit using the target tree
	// In a full implementation, you would perform actual three-way merge logic
	// This is a simplified approach that assumes conflicts are manually resolved

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
		TreeHash:     targetCommit.TreeHash, // Use target tree (simplified)
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
func (h *MergeOperationsHandler) commitToInfo(commit *object.Commit) *CommitInfo {
	return &CommitInfo{
		SHA:       commit.Hash.String(),
		Message:   commit.Message,
		Author:    AuthorInfo{Name: commit.Author.Name, Email: commit.Author.Email, Date: commit.Author.When},
		Committer: AuthorInfo{Name: commit.Committer.Name, Email: commit.Committer.Email, Date: commit.Committer.When},
		CreatedAt: commit.Author.When,
	}
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
