package fork

import (
	"context"
	"fmt"
	"time"

	"github.com/collabops/git-executor/internal/api"
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
	if cfg.Events.Enabled {
		eventsClient = events.NewLazyRedisStreamClient(cfg.Events, logger.Logger)
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

// HandleForkRepository handles POST /repos/{owner}/{repo}/forks.
//
// Typed handler — RegisterJSON wrapper 가 invalid JSON body 시 400 으로
// 응답한다. service 의 sentinel 에러 (ErrSourceNotFound, ErrForkAlreadyExists)
// 는 routes.go 의 WithErrorMapper 가 status 매핑.
func (h *ForkHandler) HandleForkRepository(c *gin.Context, req ForkRepositoryRequest) (ForkRepositoryResponse, error) {
	ctx := c.Request.Context()

	sourceOwner := c.Param("owner")
	sourceRepo := c.Param("repo")

	h.logger.LogInfo(ctx, "Processing fork request",
		zap.String("event", "fork_request"),
		zap.String("source_owner", sourceOwner),
		zap.String("source_repo", sourceRepo))

	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", sourceOwner, sourceRepo)); err != nil {
		return ForkRepositoryResponse{}, api.NewCoded(api.ErrCodeInvalidInput, fmt.Sprintf("Invalid source path: %v", err))
	}

	targetRepo := req.Repo
	if targetRepo == "" {
		targetRepo = sourceRepo
	}

	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", req.Owner, targetRepo)); err != nil {
		return ForkRepositoryResponse{}, api.NewCoded(api.ErrCodeInvalidInput, fmt.Sprintf("Invalid target path: %v", err))
	}

	userID := c.GetHeader("X-USER-ID")

	// service error 는 raw 로 반환 — WithErrorMapper 가 sentinel 매핑.
	result, err := h.service.ForkRepository(ctx, sourceOwner, sourceRepo, req.Owner, targetRepo, userID)
	if err != nil {
		return ForkRepositoryResponse{}, err
	}

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

	return ForkRepositoryResponse{
		Success:          true,
		Repository:       fmt.Sprintf("%s/%s", req.Owner, targetRepo),
		SourceRepository: fmt.Sprintf("%s/%s", sourceOwner, sourceRepo),
		Owner:            req.Owner,
		Name:             targetRepo,
		DefaultBranch:    result.DefaultBranch,
		CreatedAt:        result.CreatedAt,
		ForkMetadata:     result.ForkMetadata,
	}, nil
}

// HandleGetForkInfo handles GET /repos/{owner}/{repo}/fork.
func (h *ForkHandler) HandleGetForkInfo(c *gin.Context) (ForkInfoResponse, error) {
	owner := c.Param("owner")
	repo := c.Param("repo")

	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo)); err != nil {
		return ForkInfoResponse{}, api.NewCoded(api.ErrCodeInvalidInput, fmt.Sprintf("Invalid path: %v", err))
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return ForkInfoResponse{}, api.NewCoded(api.ErrCodeRepositoryNotFound)
	}

	if !h.service.IsFork(repoPath) {
		return ForkInfoResponse{
			Success:    true,
			IsFork:     false,
			Repository: fmt.Sprintf("%s/%s", owner, repo),
		}, nil
	}

	metadata, err := h.service.GetForkMetadata(repoPath)
	if err != nil {
		return ForkInfoResponse{}, api.NewCoded(api.ErrCodeInternalError, fmt.Sprintf("failed to read fork metadata: %v", err))
	}

	return ForkInfoResponse{
		Success:      true,
		IsFork:       true,
		Repository:   fmt.Sprintf("%s/%s", owner, repo),
		ForkMetadata: metadata,
	}, nil
}

// HandleSyncUpstream handles POST /repos/{owner}/{repo}/fork/sync.
//
// service 의 sentinel 에러 (ErrNotAFork, ErrUpstreamNotFound) 는 routes.go
// 의 WithErrorMapper 가 status 매핑.
func (h *ForkHandler) HandleSyncUpstream(c *gin.Context) (SyncUpstreamResponse, error) {
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")

	h.logger.LogInfo(ctx, "Processing upstream sync request",
		zap.String("event", "sync_upstream_request"),
		zap.String("owner", owner),
		zap.String("repo", repo))

	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo)); err != nil {
		return SyncUpstreamResponse{}, api.NewCoded(api.ErrCodeInvalidInput, fmt.Sprintf("Invalid path: %v", err))
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return SyncUpstreamResponse{}, api.NewCoded(api.ErrCodeRepositoryNotFound)
	}

	result, err := h.service.SyncUpstream(ctx, repoPath)
	if err != nil {
		return SyncUpstreamResponse{}, err
	}

	synced := result.BranchesSynced
	if synced == nil {
		synced = []SyncedBranch{}
	}
	conflicts := result.Conflicts
	if conflicts == nil {
		conflicts = []ConflictBranch{}
	}

	return SyncUpstreamResponse{
		Success:        true,
		BranchesSynced: synced,
		Conflicts:      conflicts,
	}, nil
}

// EventTypeFork is the event type for fork operations.
const EventTypeFork events.CommitEventType = "fork"
