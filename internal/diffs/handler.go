// Package protocol은 executor를 위한 Git diff REST API 엔드포인트를 구현합니다.
// 이 파일은 구조화된 JSON 응답을 제공하는 diff 작업을 처리합니다.
package diffs

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/protocol"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// DiffHandler는 Git diff REST API 엔드포인트를 처리합니다
type DiffHandler struct {
	pathConfig *repositories.PathConfig
	logger     *logging.Logger
}

// NewDiffHandler는 새로운 diff 핸들러를 생성합니다
func NewDiffHandler(cfg *config.Config) (*DiffHandler, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	// Create logger
	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	// 경로 설정 생성
	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	return &DiffHandler{
		pathConfig: pathConfig,
		logger:     logger,
	}, nil
}

// HandleCommitDiff는 GET /repos/:owner/:repo/diff/commits/:base/:head를 처리합니다
func (h *DiffHandler) HandleCommitDiff(c *gin.Context) {
	ctx := c.Request.Context()

	// 공통 파라미터 추출 및 검증
	params, err := h.validateAndExtractParams(c)
	if err != nil {
		h.logger.Error("Failed to validate and extract parameters", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	// URL 경로에서 base와 head 파라미터 추출 (v1과 동일하게 동작)
	base := protocol.SafeParam(c, "base")
	head := protocol.SafeParam(c, "head")
	if base == "" || head == "" {
		h.logger.Error("Missing required parameters: base or head")
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "Missing required parameters: base or head"})
		return
	}

	// params에 URL 경로 파라미터 설정
	params.Base = base
	params.Head = head

	// 권한 검증 및 저장소 경로 가져오기
	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		h.logger.Error("Invalid repository path", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	// 쿼리 파라미터에서 컨텍스트 라인 수 가져오기
	contextLines := 3 // 기본값
	if contextStr := c.Query("context"); contextStr != "" {
		if ctx, err := strconv.Atoi(contextStr); err == nil && ctx >= 0 {
			contextLines = ctx
		}
	}

	// diff 계산
	diffResp, err := h.calculateCommitDiff(ctx, repoPath, params.Base, params.Head, contextLines)
	if err != nil {
		// DiffTooLargeError인 경우 특별 처리
		if diffErr, ok := err.(*DiffTooLargeError); ok {
			h.logger.Warn("Diff too large to display", zap.String("reason", diffErr.Reason))
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"success": false,
				"error":   "This diff is too large to display",
				"reason":  diffErr.Reason,
			})
			return
		}
		// object not found 에러는 404로 처리 (500 방지)
		errStr := err.Error()
		if strings.Contains(errStr, "object not found") ||
			strings.Contains(errStr, "not found") ||
			strings.Contains(errStr, "reference not found") ||
			strings.Contains(errStr, "Invalid revision range") ||
			strings.Contains(errStr, "bad revision") {
			h.logger.Warn("Reference or object not found", zap.Error(err))
			c.JSON(http.StatusNotFound, gin.H{
				"success": false,
				"error":   err.Error(),
				"code":    "OBJECT_NOT_FOUND",
			})
			return
		}
		h.logger.Error("Failed to calculate commit diff", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
		return
	}

	// 페이지네이션 요청 확인
	if c.Query("page") != "" || c.Query("page_size") != "" {
		pagination := extractPaginationParams(c.Request)
		paginatedFiles, updatedPagination := paginateFiles(diffResp.Files, pagination)

		response := &PaginatedDiffResponse{
			Success: true,
			DiffResponse: &DiffResponse{
				Base:  diffResp.Base,
				Head:  diffResp.Head,
				Files: paginatedFiles,
				Stats: DiffStats{
					TotalAdditions: diffResp.Stats.TotalAdditions,
					TotalDeletions: diffResp.Stats.TotalDeletions,
					FilesChanged:   len(paginatedFiles), // 페이지네이션 응답에 맞게 업데이트
				},
			},
			Pagination: &updatedPagination,
		}
		c.JSON(http.StatusOK, response)
	} else {
		c.JSON(http.StatusOK, diffResp)
	}
}

// HandleBranchDiff는 GET /repos/:owner/:repo/diff/branches/:base/:head를 처리합니다
func (h *DiffHandler) HandleBranchDiff(c *gin.Context) {
	ctx := c.Request.Context()

	// 공통 파라미터 추출 및 검증
	params, err := h.validateAndExtractParams(c)
	if err != nil {
		h.logger.Error("Failed to validate and extract parameters", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	// URL 경로에서 base와 head 파라미터 추출 (v1과 동일하게 동작)
	base := protocol.SafeParam(c, "base")
	head := protocol.SafeParam(c, "head")
	if base == "" || head == "" {
		h.logger.Error("Missing required parameters: base or head")
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "Missing required parameters: base or head"})
		return
	}

	// params에 URL 경로 파라미터 설정
	params.Base = base
	params.Head = head

	// 저장소 경로 검증
	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		h.logger.Error("Invalid repository path", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	// 쿼리 파라미터에서 컨텍스트 라인 수 가져오기
	contextLines := 3 // 기본값
	if contextStr := c.Query("context"); contextStr != "" {
		if ctx, err := strconv.Atoi(contextStr); err == nil && ctx >= 0 {
			contextLines = ctx
		}
	}

	// 브랜치 diff 계산
	diffResp, err := h.CalculateBranchDiff(ctx, repoPath, params.Base, params.Head, contextLines)
	if err != nil {
		// DiffTooLargeError인 경우 특별 처리
		if diffErr, ok := err.(*DiffTooLargeError); ok {
			h.logger.Warn("Diff too large to display", zap.String("reason", diffErr.Reason))
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"success": false,
				"error":   "This diff is too large to display",
				"reason":  diffErr.Reason,
			})
			return
		}
		// object/reference not found 에러는 404로 처리 (500 방지)
		errStr := err.Error()
		if strings.Contains(errStr, "object not found") ||
			strings.Contains(errStr, "not found") ||
			strings.Contains(errStr, "reference not found") ||
			strings.Contains(errStr, "Invalid revision range") ||
			strings.Contains(errStr, "bad revision") {
			h.logger.Warn("Reference or object not found", zap.Error(err))
			c.JSON(http.StatusNotFound, gin.H{
				"success": false,
				"error":   err.Error(),
				"code":    "REF_NOT_FOUND",
			})
			return
		}
		h.logger.Error("Failed to calculate branch diff", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	// 페이지네이션 요청 확인
	if c.Query("page") != "" || c.Query("page_size") != "" {
		pagination := extractPaginationParams(c.Request)
		paginatedFiles, updatedPagination := paginateFiles(diffResp.Files, pagination)

		response := &PaginatedDiffResponse{
			Success: true,
			DiffResponse: &DiffResponse{
				Base:  diffResp.Base,
				Head:  diffResp.Head,
				Files: paginatedFiles,
				Stats: DiffStats{
					TotalAdditions: diffResp.Stats.TotalAdditions,
					TotalDeletions: diffResp.Stats.TotalDeletions,
					FilesChanged:   len(paginatedFiles), // 페이지네이션 응답에 맞게 업데이트
				},
				Commits:   diffResp.Commits,
				MergeBase: diffResp.MergeBase,
			},
			Pagination: &updatedPagination,
		}
		c.JSON(http.StatusOK, response)
	} else {
		c.JSON(http.StatusOK, diffResp)
	}
}

// HandleBranchDiffQuery는 GET /repos/:owner/:repo/diff/branches?base=:base&head=:head를 처리합니다.
// HandleBranchDiff와 동일한 응답을 반환하지만, 브랜치명을 query parameter로 받아
// 슬래시(/)가 포함된 브랜치명(예: fix/some-branch)을 안전하게 처리합니다.
func (h *DiffHandler) HandleBranchDiffQuery(c *gin.Context) {
	ctx := c.Request.Context()

	// 공통 파라미터 추출 및 검증
	params, err := h.validateAndExtractParams(c)
	if err != nil {
		h.logger.Error("Failed to validate and extract parameters", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	// query parameter에서 base와 head 추출
	base := c.Query("base")
	head := c.Query("head")
	if base == "" || head == "" {
		h.logger.Error("Missing required query parameters: base or head")
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "Missing required query parameters: base or head"})
		return
	}

	params.Base = base
	params.Head = head

	// 저장소 경로 검증
	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		h.logger.Error("Invalid repository path", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	// 쿼리 파라미터에서 컨텍스트 라인 수 가져오기
	contextLines := 3 // 기본값
	if contextStr := c.Query("context"); contextStr != "" {
		if ctx, err := strconv.Atoi(contextStr); err == nil && ctx >= 0 {
			contextLines = ctx
		}
	}

	// 브랜치 diff 계산 (HandleBranchDiff와 동일한 로직)
	diffResp, err := h.CalculateBranchDiff(ctx, repoPath, params.Base, params.Head, contextLines)
	if err != nil {
		if diffErr, ok := err.(*DiffTooLargeError); ok {
			h.logger.Warn("Diff too large to display", zap.String("reason", diffErr.Reason))
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"success": false,
				"error":   "This diff is too large to display",
				"reason":  diffErr.Reason,
			})
			return
		}
		errStr := err.Error()
		if strings.Contains(errStr, "object not found") ||
			strings.Contains(errStr, "not found") ||
			strings.Contains(errStr, "reference not found") ||
			strings.Contains(errStr, "Invalid revision range") ||
			strings.Contains(errStr, "bad revision") {
			h.logger.Warn("Reference or object not found", zap.Error(err))
			c.JSON(http.StatusNotFound, gin.H{
				"success": false,
				"error":   err.Error(),
				"code":    "REF_NOT_FOUND",
			})
			return
		}
		h.logger.Error("Failed to calculate branch diff", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	// 페이지네이션 요청 확인
	if c.Query("page") != "" || c.Query("page_size") != "" {
		pagination := extractPaginationParams(c.Request)
		paginatedFiles, updatedPagination := paginateFiles(diffResp.Files, pagination)

		response := &PaginatedDiffResponse{
			Success: true,
			DiffResponse: &DiffResponse{
				Base:  diffResp.Base,
				Head:  diffResp.Head,
				Files: paginatedFiles,
				Stats: DiffStats{
					TotalAdditions: diffResp.Stats.TotalAdditions,
					TotalDeletions: diffResp.Stats.TotalDeletions,
					FilesChanged:   len(paginatedFiles),
				},
				Commits:   diffResp.Commits,
				MergeBase: diffResp.MergeBase,
			},
			Pagination: &updatedPagination,
		}
		c.JSON(http.StatusOK, response)
	} else {
		c.JSON(http.StatusOK, diffResp)
	}
}

// HandleFileDiff는 GET /repos/:owner/:repo/diff/file?path=:path&base=:base&head=:head를 처리합니다
func (h *DiffHandler) HandleFileDiff(c *gin.Context) {
	ctx := c.Request.Context()

	// 공통 파라미터 추출 및 검증
	params, err := h.validateAndExtractParams(c)
	if err != nil {
		h.logger.Error("Failed to validate and extract parameters", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	// 쿼리 문자열에서 파일별 파라미터 추출
	filePath := c.Query("path")
	if filePath == "" {
		h.logger.Error("Missing required parameter: path")
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "Missing required parameter: path"})
		return
	}

	base := c.Query("base")
	head := c.Query("head")
	if base == "" || head == "" {
		h.logger.Error("Missing required parameters: base or head")
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "Missing required parameters: base or head"})
		return
	}

	// 파일 diff를 위해 params를 쿼리 파라미터로 덮어쓰기
	params.Base = base
	params.Head = head

	// 권한 검증 및 저장소 경로 가져오기
	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		h.logger.Error("Invalid repository path", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	// 쿼리 파라미터에서 컨텍스트 라인과 형식 가져오기
	contextLines := 3 // 기본값
	if contextStr := c.Query("context"); contextStr != "" {
		if ctx, err := strconv.Atoi(contextStr); err == nil && ctx >= 0 {
			contextLines = ctx
		}
	}

	// full_context 파라미터 확인 (Pull Request 뷰용)
	fullContext := c.Query("full_context") == "true"
	if fullContext {
		contextLines = 999999 // 전체 파일을 포함하기 위한 큰 숫자
	}

	format := c.Query("format")
	if format == "" {
		format = "unified" // 기본값
	}

	// 파일 diff 계산
	diffResp, err := h.calculateFileDiff(ctx, repoPath, filePath, params.Base, params.Head, contextLines, format)
	if err != nil {
		// DiffTooLargeError인 경우 특별 처리
		if diffErr, ok := err.(*DiffTooLargeError); ok {
			h.logger.Warn("Diff too large to display", zap.String("reason", diffErr.Reason))
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"success": false,
				"error":   "This diff is too large to display",
				"reason":  diffErr.Reason,
			})
			return
		}
		h.logger.Error("Failed to calculate file diff", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, diffResp)
}

// HandleSingleCommitDiff는 GET /repos/:owner/:repo/commits/:sha/diff를 처리합니다
func (h *DiffHandler) HandleSingleCommitDiff(c *gin.Context) {
	ctx := c.Request.Context()

	// 공통 파라미터 추출 및 검증
	params, err := h.validateAndExtractParams(c)
	if err != nil {
		h.logger.Error("Failed to validate and extract parameters", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	// URL 경로에서 SHA 파라미터 가져오기
	sha := c.Param("sha")
	if sha == "" {
		h.logger.Error("Missing required parameter: sha")
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "Missing required parameter: sha"})
		return
	}

	// 저장소 경로 가져오기
	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		h.logger.Error("Invalid repository path", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	// 쿼리 파라미터에서 컨텍스트 라인 수 가져오기
	contextLines := 3 // 기본값
	if contextStr := c.Query("context"); contextStr != "" {
		if ctx, err := strconv.Atoi(contextStr); err == nil && ctx >= 0 {
			contextLines = ctx
		}
	}

	// 단일 커밋 diff 계산
	response, err := h.calculateSingleCommitDiff(ctx, repoPath, sha, contextLines)
	if err != nil {
		// DiffTooLargeError인 경우 특별 처리
		if diffErr, ok := err.(*DiffTooLargeError); ok {
			h.logger.Warn("Diff too large to display", zap.String("reason", diffErr.Reason))
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"success": false,
				"error":   "This diff is too large to display",
				"reason":  diffErr.Reason,
			})
			return
		}
		h.logger.Error("Failed to calculate single commit diff", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, response)
}

// HandleExpandContext는 GET /repos/:owner/:repo/diff/expand-context를 처리합니다.
// hunk 사이 숨겨진 context 라인만 blob에서 읽어 반환하는 경량 API입니다.
// git diff를 실행하지 않으므로 파일 크기에 관계없이 빠릅니다.
//
// 파라미터 (query string):
//   - path, base, head: 필수
//   - start_old, start_new, count, direction: 오케스트레이터 호환 파라미터
//   - from_old, to_old, from_new: 레거시 파라미터 (start_old/start_new/count 우선)
//   - side: "old"이면 base에서 읽음 (deleted 파일용)
func (h *DiffHandler) HandleExpandContext(c *gin.Context) {
	ctx := c.Request.Context()

	params, err := h.validateAndExtractParams(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	filePath := c.Query("path")
	base := c.Query("base")
	head := c.Query("head")

	if filePath == "" || base == "" || head == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "required parameters: path, base, head"})
		return
	}

	var fromOld, toOld, fromNew int

	// 오케스트레이터 호환: start_old, start_new, count, direction
	if startOldStr := c.Query("start_old"); startOldStr != "" {
		startOld, err := strconv.Atoi(startOldStr)
		if err != nil || startOld < 1 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "start_old must be a positive integer"})
			return
		}

		startNewStr := c.Query("start_new")
		if startNewStr == "" {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "start_new is required when using start_old"})
			return
		}
		startNew, err := strconv.Atoi(startNewStr)
		if err != nil || startNew < 1 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "start_new must be a positive integer"})
			return
		}

		count := 20 // 기본값
		if countStr := c.Query("count"); countStr != "" {
			count, err = strconv.Atoi(countStr)
			if err != nil || count < 1 {
				c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "count must be a positive integer"})
				return
			}
		}

		direction := c.DefaultQuery("direction", "down")

		if direction == "up" {
			// 위로 확장: start_old 위쪽 count 라인
			fromOld = startOld - count
			if fromOld < 1 {
				fromOld = 1
			}
			toOld = startOld - 1
			if toOld < 1 {
				toOld = 1
			}
			fromNew = startNew - (startOld - fromOld)
			if fromNew < 1 {
				fromNew = 1
			}
		} else {
			// 아래로 확장 (기본): start_old부터 count 라인
			fromOld = startOld
			toOld = startOld + count - 1
			fromNew = startNew
		}
	} else {
		// 레거시 파라미터: from_old, to_old, from_new
		fromOldStr := c.Query("from_old")
		toOldStr := c.Query("to_old")
		fromNewStr := c.Query("from_new")

		if fromOldStr == "" || toOldStr == "" || fromNewStr == "" {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "required parameters: path, base, head, and either (start_old, start_new, count) or (from_old, to_old, from_new)"})
			return
		}

		fromOld, err = strconv.Atoi(fromOldStr)
		if err != nil || fromOld < 1 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "from_old must be a positive integer"})
			return
		}
		toOld, err = strconv.Atoi(toOldStr)
		if err != nil || toOld < fromOld {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "to_old must be >= from_old"})
			return
		}
		fromNew, err = strconv.Atoi(fromNewStr)
		if err != nil || fromNew < 1 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "from_new must be a positive integer"})
			return
		}
	}

	// 최대 500 라인 제한
	if toOld-fromOld+1 > 500 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "line range too large (max 500 lines)"})
		return
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	// side 파라미터: deleted 파일은 base에서 읽어야 함 (기본: head)
	ref := head
	if side := c.Query("side"); side == "old" {
		ref = base
	}

	response, err := nativeExpandContextLines(ctx, repoPath, ref, filePath, fromOld, toOld, fromNew)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "not found") {
			c.JSON(http.StatusNotFound, gin.H{"success": false, "error": errStr})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": errStr})
		return
	}

	c.JSON(http.StatusOK, response)
}

// HandlePatch는 GET /repos/:owner/:repo/patch/:base/:head를 처리합니다
func (h *DiffHandler) HandlePatch(c *gin.Context) {
	// 공통 파라미터 추출 및 검증
	params, err := h.validateAndExtractParams(c)
	if err != nil {
		h.logger.Error("Failed to validate and extract parameters", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	// URL 경로에서 base와 head 파라미터 추출 (v1과 동일하게 동작)
	base := protocol.SafeParam(c, "base")
	head := protocol.SafeParam(c, "head")
	if base == "" || head == "" {
		h.logger.Error("Missing required parameters: base or head")
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "Missing required parameters: base or head"})
		return
	}

	// params에 URL 경로 파라미터 설정
	params.Base = base
	params.Head = head

	// 권한 검증 및 저장소 경로 가져오기
	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		h.logger.Error("Invalid repository path", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	// patch 콘텐츠 생성
	patchContent, err := h.generatePatch(repoPath, params.Base, params.Head)
	if err != nil {
		h.logger.Error("Failed to generate patch", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	// patch 콘텐츠에 적절한 헤더 설정
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s-%s.patch\"", params.Base, params.Head))

	// patch 콘텐츠 작성
	c.Status(http.StatusOK)
	_, err = c.Writer.Write([]byte(patchContent))
	if err != nil {
		h.logger.Error("Failed to write patch response", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
		return
	}
}
