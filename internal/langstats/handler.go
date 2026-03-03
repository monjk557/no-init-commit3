package langstats

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const langStatsTimeout = 30 * time.Second

// Handler는 언어 통계 HTTP 핸들러입니다.
type Handler struct {
	config        *config.Config
	logger        *logging.Logger
	pathConfig    *repositories.PathConfig
	pathValidator *repositories.GitAPIPathValidator
}

// NewHandler는 새 Handler를 생성합니다.
func NewHandler(cfg *config.Config) (*Handler, error) {
	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	pathValidator := repositories.NewGitAPIPathValidator(pathConfig)

	return &Handler{
		config:        cfg,
		logger:        logger,
		pathConfig:    pathConfig,
		pathValidator: pathValidator,
	}, nil
}

// HandleGetLanguageStats는 GET /repos/:owner/:repo/languages 요청을 처리합니다.
func (h *Handler) HandleGetLanguageStats(c *gin.Context) {
	owner := c.Param("owner")
	repo := c.Param("repo")
	ref := c.Query("ref")

	if ref == "" {
		ref = "HEAD"
	}

	// 경로 검증
	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo)); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return
	}

	// 저장소 경로
	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return
	}

	// Read lock (push와 동시 접근 방지)
	repoLock := repositories.GetRepoRWLock(repoPath)
	repoLock.RLock()
	defer repoLock.RUnlock()

	ctx, cancel := context.WithTimeout(c.Request.Context(), langStatsTimeout)
	defer cancel()

	resp, err := ComputeLanguageStats(ctx, repoPath, ref)
	if err != nil {
		h.logger.Error("language stats computation failed",
			zap.String("owner", owner),
			zap.String("repo", repo),
			zap.String("ref", ref),
			zap.Error(err),
		)
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "failed to compute language stats"})
		return
	}

	resp.Repository = fmt.Sprintf("%s/%s", owner, repo)

	c.JSON(http.StatusOK, resp)
}
