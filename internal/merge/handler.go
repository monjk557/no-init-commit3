package merge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/diffs"
	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
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

	owner := c.Param("owner")
	repo := c.Param("repo")

	if err := h.validatePath(owner, repo); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	var req MergeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": fmt.Sprintf("Invalid JSON: %v", err)})
		return
	}

	if req.Method == "" {
		req.Method = MergeMethodMerge
	}
	if req.Strategy == "" {
		req.Strategy = MergeStrategyRecursive
	}

	if err := h.validateMergeRequest(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	// Resolve repo path (native git — no go-git OpenRepository)
	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": err.Error()})
		return
	}

	// Resolve source and target refs to commit SHAs
	sourceHash, err := gitRevParse(ctx, repoPath, req.SourceRef)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "unknown revision") {
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"success": false, "error": fmt.Sprintf("Failed to resolve source ref: %v", err)})
		return
	}

	targetHash, err := gitRevParse(ctx, repoPath, req.TargetRef)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "unknown revision") {
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"success": false, "error": fmt.Sprintf("Failed to resolve target ref: %v", err)})
		return
	}

	// Hook: Signal that target ref has been read (for testing concurrent scenarios)
	if h.hooks != nil && h.hooks.AfterReadTargetRef != nil {
		close(h.hooks.AfterReadTargetRef)
	}

	// Perform merge analysis
	conflictAnalysis, err := h.analyzeMerge(ctx, repoPath, sourceHash, targetHash, &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
		return
	}

	// Handle dry run
	if req.DryRun {
		if req.Method == MergeMethodRebase {
			rebaseAnalysis, err := h.analyzeRebaseDryRun(ctx, repoPath, sourceHash, targetHash, &req)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
				return
			}

			details := map[string]interface{}{
				"dry_run":         true,
				"can_merge":       rebaseAnalysis.CanMerge,
				"estimated_time":  rebaseAnalysis.EstimatedTime.String(),
				"recommendations": rebaseAnalysis.Recommendations,
			}
			if rebaseAnalysis.Metadata != nil {
				if v, ok := rebaseAnalysis.Metadata["commits_to_rebase"].(int); ok {
					details["commits_to_rebase"] = v
				}
				if v, ok := rebaseAnalysis.Metadata["first_conflict_commit"].(string); ok {
					details["first_conflict_commit"] = v
				}
			}

			c.JSON(http.StatusOK, &MergeResponse{
				Success:         !rebaseAnalysis.HasConflicts || req.ForceConflict,
				IsFastForward:   false,
				HasConflicts:    rebaseAnalysis.HasConflicts,
				ConflictCount:   rebaseAnalysis.ConflictCount,
				Strategy:        req.Strategy,
				Method:          req.Method,
				Message:         "Rebase dry run completed - no changes made",
				ConflictSummary: rebaseAnalysis,
				Details:         details,
			})
			return
		}

		c.JSON(http.StatusOK, &MergeResponse{
			Success:         !conflictAnalysis.HasConflicts || req.ForceConflict,
			IsFastForward:   conflictAnalysis.IsFastForward,
			HasConflicts:    conflictAnalysis.HasConflicts,
			ConflictCount:   conflictAnalysis.ConflictCount,
			Strategy:        req.Strategy,
			Method:          req.Method,
			Message:         "Dry run completed - no changes made",
			ConflictSummary: conflictAnalysis,
			Details: map[string]interface{}{
				"dry_run":         true,
				"can_merge":       conflictAnalysis.CanMerge,
				"estimated_time":  conflictAnalysis.EstimatedTime.String(),
				"recommendations": conflictAnalysis.Recommendations,
			},
		})
		return
	}

	// Conflict safety checks
	if conflictAnalysis.HasConflicts {
		hasEnvironmentConflicts := h.hasEnvironmentFileConflicts(conflictAnalysis)

		if hasEnvironmentConflicts && !req.ForceConflict {
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

		h.logger.Warn("Forced merge with conflicts",
			zap.String("source", req.SourceRef),
			zap.String("target", req.TargetRef),
			zap.Int("conflict_count", conflictAnalysis.ConflictCount),
			zap.Bool("has_environment_conflicts", hasEnvironmentConflicts))
	}

	// Serialize writes to prevent concurrent panics
	if repoPath != "" {
		repoLock := repositories.GetRepoRWLock(repoPath)
		repoLock.Lock()
		defer repoLock.Unlock()
	}

	// Perform the actual merge
	mergeResult, err := h.performMerge(ctx, repoPath, sourceHash, targetHash, &req, conflictAnalysis)
	if err != nil {
		var concurrentErr *ConcurrentUpdateError
		if errors.As(err, &concurrentErr) {
			c.JSON(http.StatusConflict, gin.H{
				"success":    false,
				"error_code": "CONCURRENT_UPDATE",
				"error":      concurrentErr.Message,
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
		return
	}

	// Prepare response
	response := &MergeResponse{
		Success:       true,
		IsFastForward: mergeResult.IsFastForward,
		HasConflicts:  mergeResult.HasConflicts,
		ConflictCount: 0,
		Strategy:      req.Strategy,
		Method:        req.Method,
		Message:       "Merge completed successfully",
	}

	if mergeResult.MergeCommit != nil {
		response.MergeCommit = convertCommitToInfo(mergeResult.MergeCommit)
	}

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

	// Publish commit event
	userID := c.GetHeader("X-USER-ID")
	if h.eventsClient != nil && mergeResult.MergeCommit != nil {
		h.publishMergeCommitEvent(ctx, repoPath, targetHash, mergeResult.MergeCommit, req.TargetRef, owner, repo, userID)
	}

	// Loose object 누적 방지: merge 성공 시 git gc --auto 비동기 실행.
	if repoPath != "" {
		go func() {
			gcCtx, gcCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer gcCancel()
			cmd := exec.CommandContext(gcCtx, "git", "--git-dir", repoPath, "gc", "--auto")
			if gcErr := cmd.Run(); gcErr != nil {
				h.logger.Debug("git gc --auto failed (non-critical)", zap.String("repo", repoPath), zap.Error(gcErr))
			}
		}()
	}

	c.JSON(http.StatusOK, response)
}

// HandleMergePreview handles GET /repos/{owner}/{repo}/merges/preview
func (h *MergePreviewHandler) HandleMergePreview(c *gin.Context) {
	startTime := time.Now()
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")

	if err := h.validatePath(owner, repo); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	var req MergePreviewRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": fmt.Sprintf("Invalid query parameters: %v", err)})
		return
	}

	if req.Strategy == "" {
		req.Strategy = MergeStrategyRecursive
	}
	if req.MaxFileSize == 0 {
		req.MaxFileSize = 1024 * 1024
	}
	if req.MaxFiles == 0 {
		req.MaxFiles = 1000
	}
	if req.CompareStrategy == "" {
		req.CompareStrategy = "tree"
	}

	status, err := h.validateMergePreviewRequest(&req)
	if err != nil {
		c.JSON(status, gin.H{"success": false, "error": err.Error()})
		return
	}

	// Resolve repo path using native git
	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": err.Error()})
		return
	}

	sourceHash, err := gitRevParse(ctx, repoPath, req.SourceRef)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": fmt.Sprintf("Failed to resolve source ref: %v", err)})
		return
	}

	targetHash, err := gitRevParse(ctx, repoPath, req.TargetRef)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": fmt.Sprintf("Failed to resolve target ref: %v", err)})
		return
	}

	if sourceHash == targetHash {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "Source and target references point to the same commit"})
		return
	}

	previewResult, err := h.performMergePreview(ctx, repoPath, sourceHash, targetHash, &req, startTime)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, previewResult)
}

// HandleMergeDiff handles GET /repos/{owner}/{repo}/merges/diff
// merge-base 기준 diff를 반환합니다. Change Request 페이지에서 source 브랜치가
// 도입한 변경만 보여주기 위한 엔드포인트입니다.
// Query params: source_ref (required), target_ref (required), context (default: 3), page, page_size
func (h *MergePreviewHandler) HandleMergeDiff(c *gin.Context) {
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")

	if err := h.validatePath(owner, repo); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	sourceRef := c.Query("source_ref")
	targetRef := c.Query("target_ref")
	if sourceRef == "" || targetRef == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "source_ref and target_ref are required"})
		return
	}

	contextLines := 3
	if contextStr := c.Query("context"); contextStr != "" {
		if v, err := strconv.Atoi(contextStr); err == nil && v >= 0 {
			contextLines = v
		}
	}

	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": err.Error()})
		return
	}

	// base = target (merge INTO), head = source (merge FROM)
	diffResp, err := diffs.CalculateMergeBaseDiff(ctx, repoPath, targetRef, sourceRef, contextLines)
	if err != nil {
		if diffErr, ok := err.(*diffs.DiffTooLargeError); ok {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"success": false,
				"error":   "This diff is too large to display",
				"reason":  diffErr.Reason,
			})
			return
		}
		errStr := err.Error()
		if strings.Contains(errStr, "not found") ||
			strings.Contains(errStr, "bad revision") {
			c.JSON(http.StatusNotFound, gin.H{
				"success": false,
				"error":   err.Error(),
				"code":    "REF_NOT_FOUND",
			})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	// 페이지네이션 지원
	if c.Query("page") != "" || c.Query("page_size") != "" {
		page, pageSize := 1, 50
		if v, err := strconv.Atoi(c.Query("page")); err == nil && v > 0 {
			page = v
		}
		if v, err := strconv.Atoi(c.Query("page_size")); err == nil && v > 0 && v <= 200 {
			pageSize = v
		}

		total := len(diffResp.Files)
		start := (page - 1) * pageSize
		if start >= total {
			diffResp.Files = []diffs.FileDiff{}
		} else {
			end := start + pageSize
			if end > total {
				end = total
			}
			diffResp.Files = diffResp.Files[start:end]
		}
	}

	c.JSON(http.StatusOK, diffResp)
}

// validatePath validates the owner and repo path components
func (h *MergeOperationsHandler) validatePath(owner, repo string) error {
	if owner == "" {
		return fmt.Errorf("owner parameter is required")
	}
	if repo == "" {
		return fmt.Errorf("repo parameter is required")
	}
	pathValidator := repositories.NewGitAPIPathValidator(h.pathConfig)
	return pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo))
}

// validatePath validates the owner and repo path components
func (h *MergePreviewHandler) validatePath(owner, repo string) error {
	if owner == "" {
		return fmt.Errorf("owner parameter is required")
	}
	if repo == "" {
		return fmt.Errorf("repo parameter is required")
	}
	pathValidator := repositories.NewGitAPIPathValidator(h.pathConfig)
	return pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo))
}

// publishMergeCommitEvent publishes a merge commit event to Redis Stream
func (h *MergeOperationsHandler) publishMergeCommitEvent(_ context.Context, repoPath string, oldHash string, commit *NativeCommit, refName, owner, repoName, userID string) {
	if h.eventsClient == nil || commit == nil {
		return
	}

	fsPath, err := repositories.GetRepoPath(h.pathConfig, owner, repoName)
	if err != nil {
		return
	}
	relativePath := events.GetRelativeRepoPath(fsPath, h.pathConfig.GetRepoRoot())

	if !strings.HasPrefix(refName, "refs/") {
		refName = "refs/heads/" + refName
	}

	event := &events.CommitEvent{
		RepoPath:      relativePath,
		EventType:     events.EventTypeMerge,
		CommitHash:    commit.Hash,
		OldRev:        oldHash,
		RefName:       refName,
		Pusher:        "git",
		UserID:        userID,
		CommitMessage: commit.Message,
		Timestamp:     commit.Committer.When,
		PushedAt:      time.Now(),
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.eventsClient.AddCommitEventToStream(ctx, event)
	}()
}

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
			"is_environment_file": isEnvironmentFile(conflict.FilePath),
		})
	}
	return conflicts
}

// matchesPattern provides simple pattern matching
func (h *MergeOperationsHandler) matchesPattern(path, pattern string) bool {
	if len(pattern) > 0 && pattern[len(pattern)-1] == '/' {
		return len(path) > len(pattern) && path[:len(pattern)] == pattern
	}
	if len(pattern) > 0 && pattern[0] == '*' {
		suffix := pattern[1:]
		return len(path) >= len(suffix) && path[len(path)-len(suffix):] == suffix
	}
	return path == pattern || (len(path) > len(pattern) && path[:len(pattern)] == pattern)
}
