package fork

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ForkHandler는 Git 저장소 포크 작업을 처리합니다.
type ForkHandler struct {
	config        *config.Config
	logger        *logging.Logger
	pathConfig    *repositories.PathConfig
	pathValidator *repositories.GitAPIPathValidator
	service       *ForkService
	eventsClient  events.RedisStreamClient
}

// NewForkHandler는 새로운 포크 핸들러를 생성합니다.
func NewForkHandler(cfg *config.Config) (*ForkHandler, error) {
	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	pathValidator := repositories.NewGitAPIPathValidator(pathConfig)

	service := NewForkService(logger, pathConfig)

	var eventsClient events.RedisStreamClient
	if cfg.Events.Enabled && cfg.Events.RedisURL != "" {
		eventsClient, err = events.NewRedisStreamClient(cfg.Events.RedisURL, logger.Logger)
		if err != nil {
			// Log warning but continue without events
			_ = err
		}
	}

	return &ForkHandler{
		config:        cfg,
		logger:        logger,
		pathConfig:    pathConfig,
		pathValidator: pathValidator,
		service:       service,
		eventsClient:  eventsClient,
	}, nil
}

// HandleForkRepository handles POST /repos/{owner}/{repo}/forks
// 소스 저장소를 지정된 타겟 소유자 아래로 포크합니다.
func (h *ForkHandler) HandleForkRepository(c *gin.Context) {
	ctx := c.Request.Context()

	sourceOwner := c.Param("owner")
	sourceRepo := c.Param("repo")

	h.logger.LogInfo(ctx, "Processing fork request",
		zap.String("event", "fork_request"),
		zap.String("source_owner", sourceOwner),
		zap.String("source_repo", sourceRepo))

	// Validate source path
	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", sourceOwner, sourceRepo)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Invalid source path: %v", err),
		})
		return
	}

	// Parse request body
	var req ForkRepositoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Invalid JSON: %v", err),
		})
		return
	}

	// Default target repo name to source repo name
	targetRepo := req.Repo
	if targetRepo == "" {
		targetRepo = sourceRepo
	}

	// Validate target path
	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", req.Owner, targetRepo)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Invalid target path: %v", err),
		})
		return
	}

	// Get user ID from request header
	userID := c.GetHeader("X-USER-ID")

	// Execute fork
	result, err := h.service.ForkRepository(ctx, sourceOwner, sourceRepo, req.Owner, targetRepo, userID)
	if err != nil {
		statusCode := http.StatusInternalServerError
		if errors.Is(err, ErrSourceNotFound) {
			statusCode = http.StatusNotFound
		} else if errors.Is(err, ErrForkAlreadyExists) {
			statusCode = http.StatusConflict
		}
		c.JSON(statusCode, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// Override default branch if specified
	if req.DefaultBranch != "" {
		refPath := "refs/heads/" + req.DefaultBranch
		if setErr := nativeSetSymbolicRef(ctx, result.TargetPath, refPath); setErr != nil {
			h.logger.Warn("Failed to set custom default branch for fork",
				zap.String("branch", req.DefaultBranch),
				zap.Error(setErr))
		} else {
			result.DefaultBranch = req.DefaultBranch
		}
	}

	// Publish fork event (non-blocking)
	if h.eventsClient != nil {
		relativePath := events.GetRelativeRepoPath(result.TargetPath, h.pathConfig.GetRepoRoot())
		event := &events.CommitEvent{
			RepoPath:      relativePath,
			EventType:     EventTypeFork,
			RefName:       "refs/heads/" + result.DefaultBranch,
			UserID:        userID,
			CommitMessage: fmt.Sprintf("Forked from %s/%s", sourceOwner, sourceRepo),
			Timestamp:     time.Now(),
		}
		go func() {
			evCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if pubErr := h.eventsClient.AddCommitEventToStream(evCtx, event); pubErr != nil {
				h.logger.Warn("Failed to publish fork event",
					zap.String("source", fmt.Sprintf("%s/%s", sourceOwner, sourceRepo)),
					zap.String("target", fmt.Sprintf("%s/%s", req.Owner, targetRepo)),
					zap.Error(pubErr))
			}
		}()
	}

	response := ForkRepositoryResponse{
		Success:          true,
		Repository:       fmt.Sprintf("%s/%s", req.Owner, targetRepo),
		SourceRepository: fmt.Sprintf("%s/%s", sourceOwner, sourceRepo),
		Owner:            req.Owner,
		Name:             targetRepo,
		DefaultBranch:    result.DefaultBranch,
		CreatedAt:        result.CreatedAt,
		ForkMetadata:     result.ForkMetadata,
	}

	c.JSON(http.StatusCreated, response)
}

// HandleGetForkInfo handles GET /repos/{owner}/{repo}/fork
// 저장소가 포크인지 확인하고 포크 메타데이터를 반환합니다.
func (h *ForkHandler) HandleGetForkInfo(c *gin.Context) {
	owner := c.Param("owner")
	repo := c.Param("repo")

	// Validate path
	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Invalid path: %v", err),
		})
		return
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return
	}

	if !h.service.IsFork(repoPath) {
		c.JSON(http.StatusOK, ForkInfoResponse{
			Success:    true,
			IsFork:     false,
			Repository: fmt.Sprintf("%s/%s", owner, repo),
		})
		return
	}

	metadata, err := h.service.GetForkMetadata(repoPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   fmt.Sprintf("failed to read fork metadata: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, ForkInfoResponse{
		Success:      true,
		IsFork:       true,
		Repository:   fmt.Sprintf("%s/%s", owner, repo),
		ForkMetadata: metadata,
	})
}

// HandleSyncUpstream handles POST /repos/{owner}/{repo}/fork/sync
// 포크된 저장소를 업스트림과 동기화합니다.
func (h *ForkHandler) HandleSyncUpstream(c *gin.Context) {
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")

	h.logger.LogInfo(ctx, "Processing upstream sync request",
		zap.String("event", "sync_upstream_request"),
		zap.String("owner", owner),
		zap.String("repo", repo))

	// Validate path
	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Invalid path: %v", err),
		})
		return
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return
	}

	result, err := h.service.SyncUpstream(ctx, repoPath)
	if err != nil {
		statusCode := http.StatusInternalServerError
		if errors.Is(err, ErrNotAFork) {
			statusCode = http.StatusBadRequest
		} else if errors.Is(err, ErrUpstreamNotFound) {
			statusCode = http.StatusNotFound
		}
		c.JSON(statusCode, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// Ensure non-nil slices for JSON
	synced := result.BranchesSynced
	if synced == nil {
		synced = []SyncedBranch{}
	}
	conflicts := result.Conflicts
	if conflicts == nil {
		conflicts = []ConflictBranch{}
	}

	c.JSON(http.StatusOK, SyncUpstreamResponse{
		Success:        true,
		BranchesSynced: synced,
		Conflicts:      conflicts,
	})
}

// EventTypeFork is the event type for fork operations.
const EventTypeFork events.CommitEventType = "fork"
