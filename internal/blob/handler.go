package blob

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/api/typed"
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
		writeCatalogError(c, api.NewCoded(api.ErrCodeInvalidInput, err.Error()))
		return
	}

	// Range 헤더가 있으나 유효하지 않으면 416 반환
	if rangeHeader := c.GetHeader("Range"); rangeHeader != "" {
		if !parseRangeValid(rangeHeader) {
			c.Header("Content-Range", "*")
			writeCatalogError(c, api.NewCoded(api.ErrCodeRangeNotSatisfiable))
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
		writeCatalogError(c, api.NewCoded(api.ErrCodeRepositoryNotFound))
		return
	}

	// Blob 내용 가져오기 (native git 사용)
	content, contentType, err := h.service.GetBlobContent(ctx, repoPath, path, ref)
	if err != nil {
		// 파일이 너무 큰 경우 특별 처리
		if errors.Is(err, ErrBlobTooLarge) {
			h.logger.Warn("File too large to display", zap.String("path", path))
			writeCatalogError(c, api.NewCoded(api.ErrCodeRequestTooLarge, "This file is too large to display: "+err.Error()))
			return
		}
		writeCatalogError(c, classifyBlobError(err))
		return
	}

	// LFS pointer 라도 binary streaming 으로 통일한다 (Issue-2327). client 는
	// /overview/blob 의 IsLFSPointer / LFS 필드로 메타데이터를 얻고, /blob 은
	// 단일 application/octet-stream contract 만 노출한다.

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

// writeCatalogError 는 *typed.HTTPError 를 표준 envelope 로 응답한다. /blob
// 은 typed.RegisterRaw 로 등록돼 JSON wrapper 를 안 거치므로(binary streaming)
// handler 가 직접 쓴다 — wrapper.go 의 writeErrorWithCode 와 동일한 shape.
func writeCatalogError(c *gin.Context, he *typed.HTTPError) {
	body := gin.H{"success": false, "error": he.Message}
	if he.Code != "" {
		body["code"] = he.Code
	}
	c.JSON(he.Status, body)
}

// classifyBlobError 는 GetBlobContent 의 raw 에러를 카탈로그 code 를 가진
// *typed.HTTPError 로 변환한다. 자체 발생 에러는 sentinel(errors.Is)로
// 우선 분류하고 — 메시지 문구가 바뀌어도 매핑이 안 깨진다 — 그 외(하위
// 계층의 자유 문자열 등)는 기존 substring 매칭으로 폴백한다(status 는
// legacy 와 동일).
func classifyBlobError(err error) *typed.HTTPError {
	errMsg := err.Error()

	switch {
	case errors.Is(err, ErrBlobIsDirectory):
		return api.NewCoded(api.ErrCodeInvalidInput, errMsg)
	case errors.Is(err, ErrBlobTooLarge):
		return api.NewCoded(api.ErrCodeRequestTooLarge, errMsg)
	case errors.Is(err, ErrBlobNotFound):
		return api.NewCoded(api.ErrCodeFileNotFound, errMsg)
	}

	switch {
	case strings.Contains(errMsg, "path is a directory"):
		return api.NewCoded(api.ErrCodeInvalidInput, errMsg)
	case strings.Contains(errMsg, "not found"), strings.Contains(errMsg, "file not found"):
		return api.NewCoded(api.ErrCodeFileNotFound, errMsg)
	case strings.Contains(errMsg, "missing"), strings.Contains(errMsg, "invalid"):
		return api.NewCoded(api.ErrCodeInvalidInput, errMsg)
	case strings.Contains(errMsg, "permission"), strings.Contains(errMsg, "access denied"):
		return api.NewCoded(api.ErrCodeForbidden, errMsg)
	case strings.Contains(errMsg, "authentication"):
		return api.NewCoded(api.ErrCodeUnauthorized, errMsg)
	}

	return api.NewCoded(api.ErrCodeInternalError, errMsg)
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
