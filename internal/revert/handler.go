package revert

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/gitutil"
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
		eventsClient = events.NewLazyRedisStreamClient(cfg.Events, logger.Logger)
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
// substring 기반 status 매핑은 classifyRevertError 가 카탈로그 code 로 변환한다
// (routes.go 에 커스텀 ErrorMapper 없음 — 모든 에러가 이미 *typed.HTTPError 라
// wrapper 의 default 해석으로 충분).
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
		return RevertResponse{}, classifyRevertError(err)
	}

	// Publish revert commit event (legacy 와 동일하게 응답 직전 호출).
	userID := c.GetHeader("X-USER-ID")
	if h.eventsClient != nil && result.RevertCommit != nil {
		h.publishRevertEvent(ctx, repoPath, result, req.TargetRef, owner, repo, userID)
	}

	return *result, nil
}

// classifyRevertError 는 performRevert 의 sentinel/substring 에러를 카탈로그
// code 를 가진 *typed.HTTPError 로 변환한다. BranchExistsError 의 extra
// field(branch_name/tip_commit) 는 NewCodedWithParams 의 params 로 실린다 —
// 과거의 flat revert_branch/revert_commit 최상위 필드에서 params.branch_name
// / params.tip_commit 중첩 구조로 shape 이 바뀐다 (consumer 동시 수정 필요).
func classifyRevertError(err error) error {
	var branchExistsErr *BranchExistsError
	if errors.As(err, &branchExistsErr) {
		tipSHA := ""
		if branchExistsErr.TipCommit != nil {
			tipSHA = branchExistsErr.TipCommit.SHA
		}
		return api.NewCodedWithParams(api.ErrCodeBranchAlreadyExists, branchExistsErr.Error(), map[string]string{
			"branch_name": branchExistsErr.BranchName,
			"tip_commit":  tipSHA,
		})
	}

	errMsg := err.Error()
	switch {
	case strings.Contains(errMsg, "conflicts"):
		return api.NewCoded(api.ErrCodeRevertConflict, errMsg)
	case strings.Contains(errMsg, "unknown revision") || strings.Contains(errMsg, "failed to read commit"):
		return api.NewCoded(api.ErrCodeCommitNotFound, errMsg)
	case strings.Contains(errMsg, "invalid parent_number"):
		return api.NewCoded(api.ErrCodeInvalidParent, errMsg)
	}
	return api.NewCoded(api.ErrCodeInternalError, errMsg)
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
func (h *RevertHandler) publishRevertEvent(ctx context.Context, repoPath string, result *RevertResponse, targetRef, owner, repoName, userID string) {
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
		// revert 는 항상 커밋 1개를 새로 만든다. OldRev 가 없어 범위 조회를 쓸 수
		// 없고, 쓸 필요도 없다. 단 default branch 일 때만 기여로 센다 (D9).
		Commits: revertContribution(ctx, repoPath, refName, result.RevertCommit),
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if pubErr := h.eventsClient.AddCommitEventToStream(ctx, event); pubErr != nil {
			h.logger.Error("Failed to publish revert commit event",
				zap.String("repo_path", relativePath),
				zap.String("commit", event.CommitHash),
				zap.String("ref", refName),
				zap.Error(pubErr))
		}
	}()
}

// revertContribution 은 revert 커밋을 기여 목록으로 변환한다. default branch 가
// 아니면 비운다 — 기여는 default branch 도달 커밋만 센다 (설계 D9).
func revertContribution(
	ctx context.Context, repoPath, refName string, commit *CommitInfo,
) []gitutil.CommitMeta {
	if commit == nil || !gitutil.IsDefaultBranch(ctx, repoPath, refName) {
		return nil
	}
	return []gitutil.CommitMeta{{
		SHA:         commit.SHA,
		AuthorEmail: commit.Author.Email,
		AuthoredAt:  commit.Author.Date.Format(time.RFC3339),
	}}
}
