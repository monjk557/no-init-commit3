package revert

import (
	"context"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"go.uber.org/zap"
)

// RevertHandler handles REST API operations for revert operations.
type RevertHandler struct {
	pathConfig   *repositories.PathConfig
	logger       *logging.Logger
	eventsClient events.RedisStreamClient
}

// NewRevertHandler creates a new revert handler.
func NewRevertHandler(cfg *config.Config) (*RevertHandler, error) {
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

	var eventsClient events.RedisStreamClient
	if cfg.Events.Enabled {
		eventsClient, err = events.NewRedisStreamClientFromConfig(cfg.Events, logger.Logger)
		if err != nil {
			logger.Warn("Failed to initialize Redis events client, continuing without events",
				zap.Error(err))
		}
	}

	return &RevertHandler{
		pathConfig:   pathConfig,
		logger:       logger,
		eventsClient: eventsClient,
	}, nil
}

// HandleRevert handles POST /repos/{owner}/{repo}/revert.
//
// Typed handler — bind error 의 사용자 친화 메시지 보존을 위해 RegisterNoBody +
// WithRequestBody 조합 (lenient bind archetype). performRevert 의 sentinel /
// substring 기반 status 매핑은 routes.go 의 WithErrorMapper 가 담당.
func (h *RevertHandler) HandleRevert(c *gin.Context) (RevertResponse, error) {
	ctx := c.Request.Context()
	owner := c.Param("owner")
	repo := c.Param("repo")

	if err := h.validatePath(owner, repo); err != nil {
		return RevertResponse{}, api.NewCoded(api.ErrCodeInvalidInput, err.Error())
	}

	var req RevertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return RevertResponse{}, api.NewCoded(api.ErrCodeInvalidInput, fmt.Sprintf("Invalid JSON: %v", err))
	}

	if err := h.validateRevertRequest(&req); err != nil {
		return RevertResponse{}, api.NewCoded(api.ErrCodeInvalidInput, err.Error())
	}

	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return RevertResponse{}, api.NewCoded(api.ErrCodeRepositoryNotFound, err.Error())
	}

	// Serialize writes to prevent concurrent conflicts
	repoLock := repositories.GetRepoRWLock(repoPath)
	repoLock.Lock()
	defer repoLock.Unlock()

	result, err := performRevert(ctx, repoPath, &req)
	if err != nil {
		return RevertResponse{}, err
	}

	// Publish revert commit event (legacy 와 동일하게 응답 직전 호출).
	userID := c.GetHeader("X-USER-ID")
	if h.eventsClient != nil && result.RevertCommit != nil {
		h.publishRevertEvent(ctx, repoPath, result, req.TargetRef, owner, repo, userID)
	}

	return *result, nil
}

// validatePath validates the owner and repo path components.
func (h *RevertHandler) validatePath(owner, repo string) error {
	if owner == "" {
		return fmt.Errorf("owner parameter is required")
	}
	if repo == "" {
		return fmt.Errorf("repo parameter is required")
	}
	pathValidator := repositories.NewGitAPIPathValidator(h.pathConfig)
	return pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo))
}

// validateRevertRequest validates the revert request fields.
func (h *RevertHandler) validateRevertRequest(req *RevertRequest) error {
	if req.CommitSHA == "" {
		return fmt.Errorf("commit_sha is required")
	}
	if req.TargetRef == "" {
		return fmt.Errorf("target_ref is required")
	}
	if req.ParentNumber < 0 {
		return fmt.Errorf("parent_number must be non-negative")
	}
	return nil
}

// publishRevertEvent publishes a revert commit event to Redis Stream.
func (h *RevertHandler) publishRevertEvent(_ context.Context, repoPath string, result *RevertResponse, targetRef, owner, repoName, userID string) {
	if h.eventsClient == nil || result.RevertCommit == nil {
		return
	}

	fsPath, err := repositories.GetRepoPath(h.pathConfig, owner, repoName)
	if err != nil {
		return
	}
	relativePath := events.GetRelativeRepoPath(fsPath, h.pathConfig.GetRepoRoot())

	refName := "refs/heads/" + result.RevertBranch

	event := &events.CommitEvent{
		RepoPath:      relativePath,
		EventType:     events.EventTypeCommit,
		CommitHash:    result.RevertCommit.SHA,
		RefName:       refName,
		Pusher:        "git",
		UserID:        userID,
		CommitMessage: result.RevertCommit.Message,
		Timestamp:     result.RevertCommit.Author.Date,
		PushedAt:      time.Now(),
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.eventsClient.AddCommitEventToStream(ctx, event)
	}()
}
