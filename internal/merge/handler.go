package merge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/api/typed"
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

// HandleMerge handles POST /repos/{owner}/{repo}/merges for performing Git merges.
//
// Typed handler — multi-field error responses (conflicts, ConcurrentUpdate)
// 는 responseErr wrapper 로 반환되어 routes.go 의 mergeErrorMapper 가
// status + body 를 그대로 사용. 단순 4xx 는 typed.NewHTTPError.
func (h *MergeOperationsHandler) HandleMerge(c *gin.Context) (MergeResponse, error) {
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")

	if err := h.validatePath(owner, repo); err != nil {
		return MergeResponse{}, typed.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	var req MergeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return MergeResponse{}, typed.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Invalid JSON: %v", err))
	}

	if req.Method == "" {
		req.Method = MergeMethodMerge
	}
	if req.Strategy == "" {
		req.Strategy = MergeStrategyRecursive
	}

	if err := h.validateMergeRequest(&req); err != nil {
		return MergeResponse{}, typed.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return MergeResponse{}, typed.NewHTTPError(http.StatusNotFound, err.Error())
	}

	sourceHash, err := gitRevParse(ctx, repoPath, req.SourceRef)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "unknown revision") {
			status = http.StatusNotFound
		}
		return MergeResponse{}, typed.NewHTTPError(status, fmt.Sprintf("Failed to resolve source ref: %v", err))
	}

	targetHash, err := gitRevParse(ctx, repoPath, req.TargetRef)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "unknown revision") {
			status = http.StatusNotFound
		}
		return MergeResponse{}, typed.NewHTTPError(status, fmt.Sprintf("Failed to resolve target ref: %v", err))
	}

	if h.hooks != nil && h.hooks.AfterReadTargetRef != nil {
		close(h.hooks.AfterReadTargetRef)
	}

	conflictAnalysis, err := h.analyzeMerge(ctx, repoPath, sourceHash, targetHash, &req)
	if err != nil {
		return MergeResponse{}, typed.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	// Dry run
	if req.DryRun {
		if req.Method == MergeMethodRebase {
			rebaseAnalysis, err := h.analyzeRebaseDryRun(ctx, repoPath, sourceHash, targetHash, &req)
			if err != nil {
				return MergeResponse{}, typed.NewHTTPError(http.StatusInternalServerError, err.Error())
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

			return MergeResponse{
				Success:         !rebaseAnalysis.HasConflicts || req.ForceConflict,
				IsFastForward:   false,
				HasConflicts:    rebaseAnalysis.HasConflicts,
				ConflictCount:   rebaseAnalysis.ConflictCount,
				Strategy:        req.Strategy,
				Method:          req.Method,
				Message:         "Rebase dry run completed - no changes made",
				ConflictSummary: rebaseAnalysis,
				Details:         details,
			}, nil
		}

		dryRunMessage := "Dry run completed - no changes made"
		if conflictAnalysis.IsAlreadyMerged {
			dryRunMessage = "Source is already merged into target - no merge needed"
		}

		return MergeResponse{
			Success:         conflictAnalysis.CanMerge,
			IsFastForward:   conflictAnalysis.IsFastForward,
			HasConflicts:    conflictAnalysis.HasConflicts,
			ConflictCount:   conflictAnalysis.ConflictCount,
			Strategy:        req.Strategy,
			Method:          req.Method,
			Message:         dryRunMessage,
			ConflictSummary: conflictAnalysis,
			Details: map[string]interface{}{
				"dry_run":           true,
				"can_merge":         conflictAnalysis.CanMerge,
				"is_already_merged": conflictAnalysis.IsAlreadyMerged,
				"estimated_time":    conflictAnalysis.EstimatedTime.String(),
				"recommendations":   conflictAnalysis.Recommendations,
			},
		}, nil
	}

	// Source already merged — idempotent success
	if conflictAnalysis.IsAlreadyMerged {
		h.logger.Info("Source already merged into target, returning existing commit info",
			zap.String("source", req.SourceRef),
			zap.String("target", req.TargetRef))
		return MergeResponse{
			Success:         true,
			MergeCommit:     conflictAnalysis.TargetCommit,
			IsFastForward:   false,
			IsAlreadyMerged: true,
			HasConflicts:    false,
			ConflictCount:   0,
			Strategy:        req.Strategy,
			Method:          req.Method,
			Message:         fmt.Sprintf("Source '%s' is already merged into target '%s'", req.SourceRef, req.TargetRef),
			ConflictSummary: conflictAnalysis,
		}, nil
	}

	// Conflict safety checks (multi-field error responses)
	if conflictAnalysis.HasConflicts {
		hasEnvironmentConflicts := h.hasEnvironmentFileConflicts(conflictAnalysis)

		if hasEnvironmentConflicts && !req.ForceConflict {
			return MergeResponse{}, &responseErr{
				status: http.StatusConflict,
				body: gin.H{
					"success":             false,
					"error":               "Environment file conflicts detected - manual resolution required",
					"conflicts":           h.formatConflictsForResponse(conflictAnalysis),
					"safety_warning":      "Environment files contain sensitive configuration. Please review conflicts manually.",
					"resolution_required": true,
				},
			}
		}

		if !req.ForceConflict {
			return MergeResponse{}, &responseErr{
				status: http.StatusConflict,
				body: gin.H{
					"success":   false,
					"error":     fmt.Sprintf("Merge has %d conflicts requiring resolution", conflictAnalysis.ConflictCount),
					"conflicts": h.formatConflictsForResponse(conflictAnalysis),
					"resolution_options": map[string]interface{}{
						"manual_resolution": "Resolve conflicts manually and retry merge",
						"choose_source":     "Use source branch version (set force_conflict=true with confirmation)",
						"choose_target":     "Use target branch version (default behavior)",
					},
					"warning": "Forcing merge may overwrite important changes. Review conflicts carefully.",
				},
			}
		}

		h.logger.Warn("Forced merge with conflicts",
			zap.String("source", req.SourceRef),
			zap.String("target", req.TargetRef),
			zap.Int("conflict_count", conflictAnalysis.ConflictCount),
			zap.Bool("has_environment_conflicts", hasEnvironmentConflicts))
	}

	// Serialize writes
	if repoPath != "" {
		repoLock := repositories.GetRepoRWLock(repoPath)
		repoLock.Lock()
		defer repoLock.Unlock()
	}

	mergeResult, err := h.performMerge(ctx, repoPath, sourceHash, targetHash, &req, conflictAnalysis)
	if err != nil {
		var concurrentErr *ConcurrentUpdateError
		if errors.As(err, &concurrentErr) {
			return MergeResponse{}, &responseErr{
				status: http.StatusConflict,
				body: gin.H{
					"success":    false,
					"error_code": "CONCURRENT_UPDATE",
					"error":      concurrentErr.Message,
				},
			}
		}
		return MergeResponse{}, typed.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	response := MergeResponse{
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

	return response, nil
}

// HandleMergePreview handles GET /repos/{owner}/{repo}/merges/preview.
//
// query string 은 typed.RegisterGetWithQuery wrapper 가 MergePreviewRequest
// 로 bind 한 뒤 본 함수에 전달한다 — wrapper 가 spec generator 에 query
// parameter 메타데이터를 자동 노출하므로 orchestrator auto-gen client 가
// source_ref/target_ref 등 인자를 누락하지 않는다.
func (h *MergePreviewHandler) HandleMergePreview(c *gin.Context, req MergePreviewRequest) (MergePreviewResponse, error) {
	startTime := time.Now()
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")

	if err := h.validatePath(owner, repo); err != nil {
		return MergePreviewResponse{}, typed.NewHTTPError(http.StatusBadRequest, err.Error())
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
		return MergePreviewResponse{}, typed.NewHTTPError(status, err.Error())
	}

	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return MergePreviewResponse{}, typed.NewHTTPError(http.StatusNotFound, err.Error())
	}

	sourceHash, err := gitRevParse(ctx, repoPath, req.SourceRef)
	if err != nil {
		return MergePreviewResponse{}, typed.NewHTTPError(http.StatusNotFound, fmt.Sprintf("Failed to resolve source ref: %v", err))
	}

	targetHash, err := gitRevParse(ctx, repoPath, req.TargetRef)
	if err != nil {
		return MergePreviewResponse{}, typed.NewHTTPError(http.StatusNotFound, fmt.Sprintf("Failed to resolve target ref: %v", err))
	}

	if sourceHash == targetHash {
		return MergePreviewResponse{}, typed.NewHTTPError(http.StatusBadRequest, "Source and target references point to the same commit")
	}

	previewResult, err := h.performMergePreview(ctx, repoPath, sourceHash, targetHash, &req, startTime)
	if err != nil {
		return MergePreviewResponse{}, typed.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return *previewResult, nil
}

// HandleMergeDiff handles GET /repos/{owner}/{repo}/merges/diff.
//
// merge-base 기준 diff를 반환한다 (Change Request 페이지에서 source 브랜치
// 가 도입한 변경만 보여주기 위함). DiffTooLargeError → 413 with reason
// 필드, REF_NOT_FOUND substring → 404 with code 필드 — 둘 다 multi-field
// 응답이라 responseErr 로 전달, ErrorMapper 가 처리.
//
// query string 은 typed.RegisterGetWithQuery wrapper 가 MergeDiffRequest
// 로 bind 한 뒤 본 함수에 전달한다 — wrapper 가 spec generator 에 query
// parameter 메타데이터를 자동 노출하므로 orchestrator auto-gen client 가
// source_ref/target_ref 등 인자를 누락하지 않는다 (Issue-2317).
func (h *MergePreviewHandler) HandleMergeDiff(c *gin.Context, req MergeDiffRequest) (diffs.DiffResponse, error) {
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")

	if err := h.validatePath(owner, repo); err != nil {
		return diffs.DiffResponse{}, typed.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	sourceRef := req.SourceRef
	targetRef := req.TargetRef

	// LEGACY: `c.Query("context")` 가 비어 있지 않고 atoi 성공 + v >= 0 이면
	// contextLines = v. NEW: req.Context 가 nil 이면 default 3, non-nil 이면
	// 음수만 거르고 그대로 사용 (=0 → "no context" 보존).
	contextLines := 3
	if req.Context != nil && *req.Context >= 0 {
		contextLines = *req.Context
	}

	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return diffs.DiffResponse{}, typed.NewHTTPError(http.StatusNotFound, err.Error())
	}

	// base = target (merge INTO), head = source (merge FROM)
	diffResp, err := diffs.CalculateMergeBaseDiff(ctx, repoPath, targetRef, sourceRef, contextLines)
	if err != nil {
		// errors.As — wrap 된 *DiffTooLargeError 도 감지.
		var diffErr *diffs.DiffTooLargeError
		if errors.As(err, &diffErr) {
			return diffs.DiffResponse{}, &responseErr{
				status: http.StatusRequestEntityTooLarge,
				body: gin.H{
					"success": false,
					"error":   "This diff is too large to display",
					"reason":  diffErr.Reason,
				},
			}
		}
		errStr := err.Error()
		if strings.Contains(errStr, "not found") || strings.Contains(errStr, "bad revision") {
			return diffs.DiffResponse{}, &responseErr{
				status: http.StatusNotFound,
				body: gin.H{
					"success": false,
					"error":   err.Error(),
					"code":    "REF_NOT_FOUND",
				},
			}
		}
		return diffs.DiffResponse{}, typed.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	// 페이지네이션 — req.Page 또는 req.PageSize 둘 중 하나라도 query string
	// 으로 전달되면 (nil 이 아니면) paginate 진입한다. LEGACY:
	// `c.Query("page") != "" || c.Query("page_size") != ""` 와 동등 — `=0`
	// 명시 호출도 paginate 진입 (default 1, 50 으로 fallback).
	if req.Page != nil || req.PageSize != nil {
		page, pageSize := 1, 50
		if req.Page != nil && *req.Page > 0 {
			page = *req.Page
		}
		if req.PageSize != nil && *req.PageSize > 0 && *req.PageSize <= 200 {
			pageSize = *req.PageSize
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

	return *diffResp, nil
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
