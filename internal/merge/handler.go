package merge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"go.uber.org/zap"
)

// MergeOperationsHandler handles REST API operations for Git merge operations
type MergeOperationsHandler struct {
	pathConfig       *repositories.PathConfig
	logger           *logging.Logger
	objectManager    *repositories.GitObjectManager
	conflictDetector *ConflictDetector
	eventsClient     events.RedisStreamClient
	config           *config.Config
	// hooks is only set in tests for deterministic concurrent testing
	// In production, this is always nil and has no effect
	hooks *MergeHooks
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

	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	objectManager, err := repositories.NewGitObjectManager(pathConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create object manager: %w", err)
	}
	conflictDetector := NewConflictDetector(pathConfig, logger)

	// Initialize events client if enabled
	var eventsClient events.RedisStreamClient
	if cfg.Events.Enabled && cfg.Events.RedisURL != "" {
		eventsClient, err = events.NewRedisStreamClient(cfg.Events.RedisURL, logger.Logger)
		if err != nil {
			// Log warning but continue without events
			_ = err
		}
	}

	return &MergeOperationsHandler{
		pathConfig:       pathConfig,
		logger:           logger,
		objectManager:    objectManager,
		conflictDetector: conflictDetector,
		eventsClient:     eventsClient,
		config:           cfg,
	}, nil
}

// MergePreviewHandler handles REST API operations for Git merge preview operations
type MergePreviewHandler struct {
	pathConfig       *repositories.PathConfig
	logger           *logging.Logger
	objectManager    *repositories.GitObjectManager
	conflictDetector *ConflictDetector
}

// NewMergePreviewHandler creates a new merge preview handler
func NewMergePreviewHandler(cfg *config.Config) (*MergePreviewHandler, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	objectManager, err := repositories.NewGitObjectManager(pathConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create object manager: %w", err)
	}
	conflictDetector := NewConflictDetector(pathConfig, logger)

	return &MergePreviewHandler{
		pathConfig:       pathConfig,
		logger:           logger,
		objectManager:    objectManager,
		conflictDetector: conflictDetector,
	}, nil
}

// HandleMerge handles POST /repos/{owner}/{repo}/merges for performing Git merges
func (h *MergeOperationsHandler) HandleMerge(c *gin.Context) {
	ctx := c.Request.Context()

	// Extract URL parameters
	owner := c.Param("owner")
	repo := c.Param("repo")

	// Validate path components
	if err := h.validatePath(owner, repo); err != nil {

		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   err.Error(),
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

	// Set default method if not specified
	if req.Method == "" {
		req.Method = MergeMethodMerge
	}

	// Set default strategy if not specified
	if req.Strategy == "" {
		req.Strategy = MergeStrategyRecursive
	}

	// Validate merge request
	if err := h.validateMergeRequest(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// Open repository
	repository, err := h.objectManager.OpenRepository(owner, repo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// Resolve source and target commits
	sourceCommit, err := h.objectManager.ResolveReference(repository, req.SourceRef)
	if err != nil {
		if errors.Is(err, repositories.ErrRefNotFound) ||
			errors.Is(err, repositories.ErrObjectNotFound) ||
			errors.Is(err, plumbing.ErrReferenceNotFound) {
			c.JSON(http.StatusNotFound, gin.H{
				"success": false,
				"error":   err.Error(),
			})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   err.Error(),
			})
		}
		return
	}

	targetCommit, err := h.objectManager.ResolveReference(repository, req.TargetRef)
	if err != nil {
		if errors.Is(err, repositories.ErrRefNotFound) ||
			errors.Is(err, repositories.ErrObjectNotFound) ||
			errors.Is(err, plumbing.ErrReferenceNotFound) {
			c.JSON(http.StatusNotFound, gin.H{
				"success": false,
				"error":   err.Error(),
			})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   err.Error(),
			})
		}
		return
	}

	// Hook: Signal that target ref has been read (for testing concurrent scenarios)
	if h.hooks != nil && h.hooks.AfterReadTargetRef != nil {
		close(h.hooks.AfterReadTargetRef)
	}

	// Perform merge analysis first
	conflictAnalysis, err := h.analyzeMerge(ctx, repository, sourceCommit, targetCommit, &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// Handle dry run
	if req.DryRun {
		// Rebase method의 경우 각 커밋 단계별로 conflict를 분석해야 함
		if req.Method == MergeMethodRebase {
			rebaseAnalysis, err := h.analyzeRebaseDryRun(ctx, repository, sourceCommit, targetCommit, &req)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"success": false,
					"error":   err.Error(),
				})
				return
			}

			details := map[string]interface{}{
				"dry_run":               true,
				"can_merge":             rebaseAnalysis.CanMerge,
				"auto_resolvable_count": rebaseAnalysis.AutoResolvable,
				"estimated_time":        rebaseAnalysis.EstimatedTime.String(),
				"recommendations":       rebaseAnalysis.Recommendations,
			}
			// Add rebase-specific metadata if available
			if rebaseAnalysis.Metadata != nil {
				if commitsToRebase, ok := rebaseAnalysis.Metadata["commits_to_rebase"].(int); ok {
					details["commits_to_rebase"] = commitsToRebase
				}
				if firstConflictCommit, ok := rebaseAnalysis.Metadata["first_conflict_commit"].(string); ok {
					details["first_conflict_commit"] = firstConflictCommit
				}
			}

			response := &MergeResponse{
				Success:         !rebaseAnalysis.HasConflicts || req.ForceConflict,
				IsFastForward:   false, // Rebase는 fast-forward가 아님
				HasConflicts:    rebaseAnalysis.HasConflicts,
				ConflictCount:   rebaseAnalysis.ConflictCount,
				Strategy:        req.Strategy,
				Method:          req.Method,
				Message:         "Rebase dry run completed - no changes made",
				ConflictSummary: rebaseAnalysis,
				Details:         details,
			}
			c.JSON(http.StatusOK, response)
			return
		}

		// Merge와 Squash는 동일한 분석 사용 (둘 다 source vs target의 단일 merge)
		response := &MergeResponse{
			Success:         !conflictAnalysis.HasConflicts || req.ForceConflict,
			IsFastForward:   conflictAnalysis.IsFastForward,
			HasConflicts:    conflictAnalysis.HasConflicts,
			ConflictCount:   conflictAnalysis.ConflictCount,
			Strategy:        req.Strategy,
			Method:          req.Method,
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
		c.JSON(http.StatusOK, response)
		return
	}

	// SAFETY FIX: Replace dangerous force_conflict with explicit conflict resolution
	if conflictAnalysis.HasConflicts {
		// Check if this is an environment file conflict
		hasEnvironmentConflicts := h.hasEnvironmentFileConflicts(conflictAnalysis)

		if hasEnvironmentConflicts && !req.ForceConflict {
			// Environment conflicts require explicit acknowledgment
			c.JSON(http.StatusConflict, gin.H{
				"success":             false,
				"error":               "Environment file conflicts detected - manual resolution required",
				"conflicts":           h.formatConflictsForResponse(conflictAnalysis),
				"safety_warning":      "Environment files contain sensitive configuration. Please review conflicts manually.",
				"resolution_required": true,
			})
			return
		}

		if !req.ForceConflict {
			// For non-environment conflicts, provide clear resolution options
			c.JSON(http.StatusConflict, gin.H{
				"success":   false,
				"error":     fmt.Sprintf("Merge has %d conflicts requiring resolution", conflictAnalysis.ConflictCount),
				"conflicts": h.formatConflictsForResponse(conflictAnalysis),
				"resolution_options": map[string]interface{}{
					"manual_resolution": "Resolve conflicts manually and retry merge",
					"choose_source":     "Use source branch version (set force_conflict=true with confirmation)",
					"choose_target":     "Use target branch version (default behavior)",
				},
				"warning": "Forcing merge may overwrite important changes. Review conflicts carefully.",
			})
			return
		}

		// If force_conflict=true, log the decision for audit
		h.logger.Warn("Forced merge with conflicts",
			zap.String("source", req.SourceRef),
			zap.String("target", req.TargetRef),
			zap.Int("conflict_count", conflictAnalysis.ConflictCount),
			zap.Bool("has_environment_conflicts", hasEnvironmentConflicts))
	}

	// Perform the actual merge
	mergeResult, err := h.performMerge(ctx, repository, sourceCommit, targetCommit, &req, conflictAnalysis)
	if err != nil {
		// Check for concurrent update error
		var concurrentErr *ConcurrentUpdateError
		if errors.As(err, &concurrentErr) {
			c.JSON(http.StatusConflict, gin.H{
				"success":    false,
				"error_code": "CONCURRENT_UPDATE",
				"error":      concurrentErr.Message,
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// Prepare response
	response := &MergeResponse{
		Success:       true,
		IsFastForward: mergeResult.IsFastForward,
		HasConflicts:  mergeResult.HasConflicts,
		ConflictCount: 0, // Successful merge means conflicts were resolved
		Strategy:      req.Strategy,
		Method:        req.Method, // 사용된 merge method 추가
		Message:       "Merge completed successfully",
	}

	if mergeResult.MergeCommit != nil {
		response.MergeCommit = convertCommitToInfo(mergeResult.MergeCommit)
	}

	// Add additional details
	details := map[string]interface{}{
		"source_ref":    req.SourceRef,
		"target_ref":    req.TargetRef,
		"merge_type":    getMergeType(mergeResult),
		"merge_method":  string(req.Method),
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

	// Extract user ID from X-USER-ID header
	userID := c.GetHeader("X-USER-ID")
	if userID == "" {
		userID = "" // Empty string if not provided
	}

	// Publish commit event for merge operations
	if h.eventsClient != nil {
		targetHashStr := targetCommit.String()
		if mergeResult.MergeCommit != nil {
			// Merge commit was created (non-fast-forward merge)
			h.publishMergeCommitEvent(ctx, repository, targetHashStr, mergeResult.MergeCommit, req.TargetRef, owner, repo, userID)
		} else if mergeResult.IsFastForward {
			// Fast-forward merge: target ref was updated to source commit
			h.publishFastForwardMergeEvent(ctx, repository, targetHashStr, sourceCommit, req.TargetRef, owner, repo, userID)
		}
	}

	// Send success response
	c.JSON(http.StatusOK, response)
}

// HandleMergePreview handles GET /repos/{owner}/{repo}/merges/preview for merge analysis
func (h *MergePreviewHandler) HandleMergePreview(c *gin.Context) {
	startTime := time.Now()
	ctx := c.Request.Context()

	// Extract URL parameters
	owner := c.Param("owner")
	repo := c.Param("repo")

	// Validate path components
	if err := h.validatePath(owner, repo); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// Parse and validate query parameters
	var req MergePreviewRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Invalid query parameters: %v", err),
		})
		return
	}

	// Set defaults
	if req.Strategy == "" {
		req.Strategy = MergeStrategyRecursive
	}
	if req.MaxFileSize == 0 {
		req.MaxFileSize = 1024 * 1024 // 1MB default
	}
	if req.MaxFiles == 0 {
		req.MaxFiles = 1000 // 1000 files default
	}
	if req.CompareStrategy == "" {
		req.CompareStrategy = "tree"
	}

	// Validate merge preview request
	status, err := h.validateMergePreviewRequest(&req)
	if err != nil {
		c.JSON(status, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// Open repository
	repository, err := h.objectManager.OpenRepository(owner, repo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// Resolve source and target commits
	sourceCommit, err := h.objectManager.ResolveReference(repository, req.SourceRef)
	if err != nil {
		if errors.Is(err, repositories.ErrRefNotFound) ||
			errors.Is(err, repositories.ErrObjectNotFound) ||
			errors.Is(err, plumbing.ErrReferenceNotFound) {
			c.JSON(http.StatusNotFound, gin.H{
				"success": false,
				"error":   err.Error(),
			})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   err.Error(),
			})
		}
		return
	}

	targetCommit, err := h.objectManager.ResolveReference(repository, req.TargetRef)
	if err != nil {
		if errors.Is(err, repositories.ErrRefNotFound) ||
			errors.Is(err, repositories.ErrObjectNotFound) ||
			errors.Is(err, plumbing.ErrReferenceNotFound) {
			c.JSON(http.StatusNotFound, gin.H{
				"success": false,
				"error":   err.Error(),
			})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   err.Error(),
			})
		}
		return
	}

	// Check if source and target are the same
	if sourceCommit == targetCommit {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "Source and target references point to the same commit",
		})
		return
	}

	// Perform merge preview analysis
	previewResult, err := h.performMergePreview(ctx, repository, sourceCommit, targetCommit, &req, startTime)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// Send response directly without wrapping in data field
	c.JSON(http.StatusOK, previewResult)
}

// validatePath validates the owner and repo path components
func (h *MergeOperationsHandler) validatePath(owner, repo string) error {
	// Check for empty components
	if owner == "" {
		return fmt.Errorf("owner parameter is required")
	}
	if repo == "" {
		return fmt.Errorf("repo parameter is required")
	}

	// Check path traversal and security
	pathValidator := repositories.NewGitAPIPathValidator(h.pathConfig)
	return pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo))
}

// validatePath validates the owner and repo path components
func (h *MergePreviewHandler) validatePath(owner, repo string) error {
	// Check for empty components
	if owner == "" {
		return fmt.Errorf("owner parameter is required")
	}
	if repo == "" {
		return fmt.Errorf("repo parameter is required")
	}

	// Check path traversal and security
	pathValidator := repositories.NewGitAPIPathValidator(h.pathConfig)
	return pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo))
}

// resolveReference resolves a Git reference to a commit hash
//
//nolint:unused // ref resolution helper
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

// resolveReference resolves a Git reference to a commit hash
//
//nolint:unused // ref resolution helper
func (h *MergePreviewHandler) resolveReference(repo *gogit.Repository, ref string) (plumbing.Hash, error) {
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

// publishMergeCommitEvent publishes a merge commit event to Redis Stream
func (h *MergeOperationsHandler) publishMergeCommitEvent(ctx context.Context, repo *gogit.Repository, oldHash string, commit *object.Commit, refName, owner, repoName, userID string) {
	if h.eventsClient == nil {
		return
	}

	// Get full filesystem path for the repository
	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repoName)
	if err != nil {
		return
	}

	// Convert to relative path (work_space_name/uuid.git format)
	relativePath := events.GetRelativeRepoPath(repoPath, h.pathConfig.GetRepoRoot())

	// Normalize ref name
	if !strings.HasPrefix(refName, "refs/") {
		refName = "refs/heads/" + refName
	}

	// Create event
	event := &events.CommitEvent{
		RepoPath:      relativePath,
		EventType:     events.EventTypeMerge,
		CommitHash:    commit.Hash.String(),
		OldRev:        oldHash,
		RefName:       refName,
		Pusher:        "git", // Default pusher identifier
		UserID:        userID,
		CommitMessage: commit.Message,
		Timestamp:     commit.Committer.When,
	}

	// Publish asynchronously to avoid blocking
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = h.eventsClient.AddCommitEventToStream(ctx, event)
	}()
}

// publishFastForwardMergeEvent publishes a fast-forward merge event to Redis Stream
func (h *MergeOperationsHandler) publishFastForwardMergeEvent(ctx context.Context, repo *gogit.Repository, oldHash string, commitHash plumbing.Hash, refName, owner, repoName, userID string) {
	if h.eventsClient == nil {
		return
	}

	// Get commit object
	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		return
	}

	// Get full filesystem path for the repository
	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repoName)
	if err != nil {
		return
	}

	// Convert to relative path (work_space_name/uuid.git format)
	relativePath := events.GetRelativeRepoPath(repoPath, h.pathConfig.GetRepoRoot())

	// Normalize ref name
	if !strings.HasPrefix(refName, "refs/") {
		refName = "refs/heads/" + refName
	}

	// Create event
	event := &events.CommitEvent{
		RepoPath:      relativePath,
		EventType:     events.EventTypeMerge,
		CommitHash:    commitHash.String(),
		OldRev:        oldHash,
		RefName:       refName,
		Pusher:        "git", // Default pusher identifier
		UserID:        userID,
		CommitMessage: commit.Message,
		Timestamp:     commit.Committer.When,
	}

	// Publish asynchronously to avoid blocking
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = h.eventsClient.AddCommitEventToStream(ctx, event)
	}()
}

// SAFETY FIX: Helper methods for enhanced conflict handling

// hasEnvironmentFileConflicts checks if any conflicts involve environment files
func (h *MergeOperationsHandler) hasEnvironmentFileConflicts(analysis *MergeAnalysisResult) bool {
	if analysis == nil {
		return false
	}

	environmentPatterns := []string{
		"config/env/",
		".env",
		"environments/",
		"config/staging/",
		"config/production/",
	}

	for _, conflict := range analysis.FileConflicts {
		for _, pattern := range environmentPatterns {
			if h.matchesPattern(conflict.FilePath, pattern) {
				return true
			}
		}
	}
	return false
}

// formatConflictsForResponse formats conflicts for API response
func (h *MergeOperationsHandler) formatConflictsForResponse(analysis *MergeAnalysisResult) []map[string]interface{} {
	if analysis == nil {
		return []map[string]interface{}{}
	}

	conflicts := make([]map[string]interface{}, 0, len(analysis.FileConflicts))
	for _, conflict := range analysis.FileConflicts {
		conflicts = append(conflicts, map[string]interface{}{
			"file_path":           conflict.FilePath,
			"conflict_type":       string(conflict.ConflictType),
			"severity":            string(conflict.Severity),
			"is_environment_file": h.isEnvironmentFile(conflict.FilePath),
			"auto_resolvable":     conflict.AutoResolvable,
		})
	}
	return conflicts
}

// matchesPattern provides simple pattern matching
func (h *MergeOperationsHandler) matchesPattern(path, pattern string) bool {
	if len(pattern) > 0 && pattern[len(pattern)-1] == '/' {
		// Directory pattern
		return len(path) > len(pattern) && path[:len(pattern)] == pattern
	}
	if len(pattern) > 0 && pattern[0] == '*' {
		// Suffix pattern
		suffix := pattern[1:]
		return len(path) >= len(suffix) && path[len(path)-len(suffix):] == suffix
	}
	// Exact or prefix match
	return path == pattern || (len(path) > len(pattern) && path[:len(pattern)] == pattern)
}
