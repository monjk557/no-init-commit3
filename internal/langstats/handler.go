package langstats

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/collabops/git-executor/internal/api/typed"
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

// HandleGetLanguageStats는 GET /repos/{owner}/{repo}/languages 요청을 처리합니다.
//
// Typed handler — typed.RegisterGet wrapper 가 정상 결과는 c.JSON(200, resp)
// 으로, *typed.HTTPError 는 wrapper 의 default 매핑 (status + 기존 응답
// 형태) 으로 처리한다. 외부 동작은 legacy 와 byte-equivalent.
func (h *Handler) HandleGetLanguageStats(c *gin.Context) (LanguageStatsResponse, error) {
	owner := c.Param("owner")
	repo := c.Param("repo")
	ref := c.Query("ref")

	if ref == "" {
		ref = "HEAD"
	}

	// 경로 검증
	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo)); err != nil {
		return LanguageStatsResponse{}, typed.NewHTTPError(http.StatusNotFound, "repository not found")
	}

	// 저장소 경로
	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return LanguageStatsResponse{}, typed.NewHTTPError(http.StatusNotFound, "repository not found")
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
		return LanguageStatsResponse{}, typed.NewHTTPError(http.StatusUnprocessableEntity, "failed to compute language stats")
	}

	resp.Repository = fmt.Sprintf("%s/%s", owner, repo)

	return *resp, nil
}
