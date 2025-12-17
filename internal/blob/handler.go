package blob

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// BlobHandler는 Git blob REST API 엔드포인트를 처리합니다.
type BlobHandler struct {
	config        *config.Config
	logger        *logging.Logger
	pathConfig    *repositories.PathConfig
	pathValidator *repositories.GitAPIPathValidator
	repoManager   *repositories.DefaultRepositoryManager
	service       *BlobService
}

// NewBlobHandler는 새로운 blob 핸들러를 생성합니다.
func NewBlobHandler(cfg *config.Config) (*BlobHandler, error) {
	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	pathValidator := repositories.NewGitAPIPathValidator(pathConfig)

	repoManager, err := repositories.NewRepositoryManager(pathConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create repository manager: %w", err)
	}

	service := NewBlobService(pathConfig)

	return &BlobHandler{
		config:        cfg,
		logger:        logger,
		pathConfig:    pathConfig,
		pathValidator: pathValidator,
		repoManager:   repoManager,
		service:       service,
	}, nil
}

// repositoryContext는 저장소 관련 공통 컨텍스트를 담는 구조체입니다.
type repositoryContext struct {
	owner       string
	repo        string
	ref         string
	path        string
	repoPath    string
	repoWrapper *repositories.RepositoryWrapper
}

// validateAndOpenRepository는 저장소 검증 및 열기를 수행하는 공통 로직입니다.
func (h *BlobHandler) validateAndOpenRepository(ctx context.Context, c *gin.Context, owner, repo, ref string) (*repositoryContext, bool) {
	// ref가 제공되지 않으면 기본값으로 HEAD 사용
	if ref == "" {
		ref = "HEAD"
	}

	// 경로 구성 요소 검증
	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo)); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return nil, false
	}

	// 저장소 경로 가져오기
	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return nil, false
	}

	// 저장소 열기 (존재 여부는 OpenRepository에서 확인)
	repoWrapper, err := h.repoManager.OpenRepository(repoPath)
	if err != nil {
		// repository not found 또는 corrupted
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return nil, false
	}

	return &repositoryContext{
		owner:       owner,
		repo:        repo,
		ref:         ref,
		repoPath:    repoPath,
		repoWrapper: repoWrapper,
	}, true
}

// HandleBlobAPI는 GET /repos/{owner}/{repo}/blob?path=<file_path>&ref=<git_ref> 요청을 처리합니다.
func (h *BlobHandler) HandleBlobAPI(c *gin.Context) {
	ctx := c.Request.Context()

	// 파라미터 추출
	owner := c.Param("owner")
	repo := c.Param("repo")
	path := c.Query("path")
	ref := c.Query("ref")

	// path 파라미터 검증
	if path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "missing required parameter: path"})
		return
	}

	// 파일 경로 검증
	if err := ValidateFilePath(path); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": fmt.Sprintf("invalid path: %v", err)})
		return
	}

	// 공통 검증 및 저장소 열기
	repoCtx, ok := h.validateAndOpenRepository(ctx, c, owner, repo, ref)
	if !ok {
		return
	}
	defer func() { _ = repoCtx.repoWrapper.Close() }()

	repoCtx.path = path

	// Blob 내용 가져오기 (이미 열린 repository 사용)
	content, contentType, err := h.service.GetBlobContent(ctx, owner, repo, path, repoCtx.ref, repoCtx.repoWrapper)
	if err != nil {
		// 파일이 너무 큰 경우 특별 처리
		if strings.Contains(err.Error(), "too large") {
			h.logger.Warn("File too large to display", zap.String("path", path))
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"success": false,
				"error":   "This file is too large to display",
				"reason":  err.Error(),
			})
			return
		}
		statusCode := h.getErrorStatusCode(err)
		h.sendError(c, err, statusCode)
		return
	}

	// 헤더 설정
	c.Header("Content-Type", contentType)
	c.Header("Content-Length", fmt.Sprintf("%d", len(content)))
	c.Header("Cache-Control", "public, max-age=3600")

	// 바이너리 콘텐츠인 경우
	if IsBinaryContent(content, contentType) {
		c.Header("X-Content-Encoding", "binary")
	}

	// 응답 전송
	c.Data(http.StatusOK, contentType, content)
}

// sendError는 에러 응답을 전송합니다.
func (h *BlobHandler) sendError(c *gin.Context, err error, statusCode int) {
	c.JSON(statusCode, gin.H{"error": err.Error()})
}

// getErrorStatusCode는 에러에 따라 적절한 HTTP 상태 코드를 반환합니다.
func (h *BlobHandler) getErrorStatusCode(err error) int {
	errMsg := err.Error()

	if strings.Contains(errMsg, "not found") || strings.Contains(errMsg, "file not found") {
		return http.StatusNotFound
	}

	if strings.Contains(errMsg, "missing") || strings.Contains(errMsg, "invalid") {
		return http.StatusBadRequest
	}

	if strings.Contains(errMsg, "permission") || strings.Contains(errMsg, "access denied") {
		return http.StatusForbidden
	}

	if strings.Contains(errMsg, "authentication") {
		return http.StatusUnauthorized
	}

	return http.StatusInternalServerError
}
