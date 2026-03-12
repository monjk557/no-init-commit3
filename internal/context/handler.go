package context

import (
	"fmt"
	"net/http"
	"regexp"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

var shaRegex = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ContextHandler는 bare repo 컨텍스트 조회 엔드포인트를 처리합니다
type ContextHandler struct {
	pathConfig *repositories.PathConfig
	logger     *logging.Logger
	service    *Service
}

// NewContextHandler는 새로운 컨텍스트 핸들러를 생성합니다
func NewContextHandler(cfg *config.Config) (*ContextHandler, error) {
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

	service := NewService(logger)

	return &ContextHandler{
		pathConfig: pathConfig,
		logger:     logger,
		service:    service,
	}, nil
}

// HandleContext는 POST /repos/:owner/:repo/context를 처리합니다
func (h *ContextHandler) HandleContext(c *gin.Context) {
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")

	if owner == "" || repo == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "missing required parameters: owner or repo"})
		return
	}

	// 요청 바디 파싱
	var req ContextRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.logger.Error("Failed to parse request body", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid request body: old_rev and new_rev are required"})
		return
	}

	// SHA 형식 검증
	if !isNullSHA(req.OldRev) && !shaRegex.MatchString(req.OldRev) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid old_rev: must be 40-character hex SHA"})
		return
	}
	if !shaRegex.MatchString(req.NewRev) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid new_rev: must be 40-character hex SHA"})
		return
	}

	// 저장소 경로 검증
	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		h.logger.Error("Invalid repository path", zap.Error(err))
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return
	}

	// 컨텍스트 조회
	resp, err := h.service.GetContext(ctx, repoPath, req.OldRev, req.NewRev)
	if err != nil {
		h.logger.Error("Failed to get context",
			zap.String("owner", owner),
			zap.String("repo", repo),
			zap.Error(err),
		)
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, resp)
}
