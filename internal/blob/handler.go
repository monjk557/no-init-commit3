package blob

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
)

// BlobHandler는 Git blob REST API 엔드포인트를 처리합니다.
type BlobHandler struct {
	logger     *logging.Logger
	pathConfig *repositories.PathConfig
	service    *BlobService
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

	service := NewBlobService(pathConfig)

	return &BlobHandler{
		logger:     logger,
		pathConfig: pathConfig,
		service:    service,
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

	// 파라미터 검증
	if err := ValidateBlobParams(owner, repo, path, ref); err != nil {
		h.sendError(c, err, http.StatusBadRequest)
		return
	}

	// Blob 내용 가져오기
	content, contentType, err := h.service.GetBlobContent(ctx, owner, repo, path, ref)
	if err != nil {
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
