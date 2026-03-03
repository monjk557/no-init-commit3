package blob

import (
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

	service := NewBlobService()

	return &BlobHandler{
		config:        cfg,
		logger:        logger,
		pathConfig:    pathConfig,
		pathValidator: pathValidator,
		service:       service,
	}, nil
}

// HandleBlobAPI는 GET /repos/{owner}/{repo}/blob?path=<file_path>&ref=<git_ref> 요청을 처리합니다.
func (h *BlobHandler) HandleBlobAPI(c *gin.Context) {
	ctx := c.Request.Context()

	// 파라미터 추출
	owner := c.Param("owner")
	repo := c.Param("repo")
	path := c.Query("path")
	ref := c.Query("ref")

	// owner, repo, path, ref 검증 (빈 값·잘못된 형식 시 400)
	if err := ValidateBlobParams(owner, repo, path, ref); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	// Range 헤더가 있으나 유효하지 않으면 416 반환
	if rangeHeader := c.GetHeader("Range"); rangeHeader != "" {
		if !parseRangeValid(rangeHeader) {
			c.Header("Content-Range", "*")
			c.JSON(http.StatusRequestedRangeNotSatisfiable, gin.H{"success": false, "error": "invalid range"})
			return
		}
	}

	// ref 기본값 설정
	if ref == "" {
		ref = "HEAD"
	}

	// 저장소 경로 검증
	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return
	}

	// Blob 내용 가져오기 (native git 사용)
	content, contentType, err := h.service.GetBlobContent(ctx, repoPath, path, ref)
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
	c.JSON(statusCode, gin.H{"success": false, "error": err.Error()})
}

// getErrorStatusCode는 에러에 따라 적절한 HTTP 상태 코드를 반환합니다.
func (h *BlobHandler) getErrorStatusCode(err error) int {
	errMsg := err.Error()

	if strings.Contains(errMsg, "path is a directory") {
		return http.StatusBadRequest
	}
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

// parseRangeValid returns true if the Range header is valid (e.g. bytes=0-499).
// Invalid formats (e.g. bytes=invalid) return false.
func parseRangeValid(rangeHeader string) bool {
	if rangeHeader == "" {
		return true
	}
	// bytes=N-M or bytes=N- format
	if !strings.HasPrefix(rangeHeader, "bytes=") {
		return false
	}
	rest := strings.TrimPrefix(rangeHeader, "bytes=")
	if rest == "" {
		return false
	}
	parts := strings.Split(rest, "-")
	if len(parts) != 2 {
		return false
	}
	var start, end int64
	if _, err := fmt.Sscanf(parts[0], "%d", &start); err != nil && parts[0] != "" {
		return false
	}
	if parts[1] != "" {
		if _, err := fmt.Sscanf(parts[1], "%d", &end); err != nil {
			return false
		}
		if end < start {
			return false
		}
	}
	return true
}
