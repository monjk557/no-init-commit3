// Package diffs 는 executor를 위한 Git diff REST API 엔드포인트를 구현합니다.
// 구조화된 JSON 응답을 제공하는 diff 작업을 처리합니다.
package diffs

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/collabops/git-executor/internal/api/typed"
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

	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	return &DiffHandler{
		pathConfig: pathConfig,
		logger:     logger,
	}, nil
}

// extractContextLines 는 ?context= query 를 정수로 파싱한다 (기본 3).
func extractContextLines(c *gin.Context) int {
	contextLines := 3
	if contextStr := c.Query("context"); contextStr != "" {
		if v, err := strconv.Atoi(contextStr); err == nil && v >= 0 {
			contextLines = v
		}
	}
	return contextLines
}

// classifyDiffError 는 diff 에러를 status + responseOverride 또는 typed.HTTPError
// 로 분류한다. DiffTooLarge → 413 + reason 필드, NOT_FOUND substring → 404 +
// code 필드, 그 외 → 400.
//
// errors.As 를 사용해 wrap 된 *DiffTooLargeError 도 감지 (fmt.Errorf("...: %w",
// dtlErr) 처럼 wrap 되어도 동작).
func classifyDiffError(err error, defaultStatus int, code string) error {
	var diffErr *DiffTooLargeError
	if errors.As(err, &diffErr) {
		return &responseOverride{
			status: http.StatusRequestEntityTooLarge,
			body: gin.H{
				"success": false,
				"error":   "This diff is too large to display",
				"reason":  diffErr.Reason,
			},
		}
	}
	errStr := err.Error()
	if code != "" && (strings.Contains(errStr, "object not found") ||
		strings.Contains(errStr, "not found") ||
		strings.Contains(errStr, "reference not found") ||
		strings.Contains(errStr, "Invalid revision range") ||
		strings.Contains(errStr, "bad revision")) {
		return &responseOverride{
			status: http.StatusNotFound,
			body: gin.H{
				"success": false,
				"error":   err.Error(),
				"code":    code,
			},
		}
	}
	return typed.NewHTTPError(defaultStatus, err.Error())
}

// HandleCommitDiff handles GET /diff/commits/:base/:head.
func (h *DiffHandler) HandleCommitDiff(c *gin.Context) (DiffResponse, error) {
	ctx := c.Request.Context()

	params, err := h.validateAndExtractParams(c)
	if err != nil {
		return DiffResponse{}, typed.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	base := protocol.SafeParam(c, "base")
	head := protocol.SafeParam(c, "head")
	if base == "" || head == "" {
		return DiffResponse{}, typed.NewHTTPError(http.StatusBadRequest, "Missing required parameters: base or head")
	}
	params.Base = base
	params.Head = head

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		return DiffResponse{}, typed.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	contextLines := extractContextLines(c)

	diffResp, err := h.calculateCommitDiff(ctx, repoPath, params.Base, params.Head, contextLines)
	if err != nil {
		return DiffResponse{}, classifyDiffError(err, http.StatusInternalServerError, "OBJECT_NOT_FOUND")
	}

	if c.Query("page") != "" || c.Query("page_size") != "" {
		pagination := extractPaginationParams(c.Request)
		paginatedFiles, updatedPagination := paginateFiles(diffResp.Files, pagination)
		paginated := &PaginatedDiffResponse{
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
			},
			Pagination: &updatedPagination,
		}
		return DiffResponse{}, &responseOverride{status: http.StatusOK, body: paginated}
	}

	return *diffResp, nil
}

// HandleBranchDiff handles GET /diff/branches/:base/:head.
func (h *DiffHandler) HandleBranchDiff(c *gin.Context) (DiffResponse, error) {
	ctx := c.Request.Context()

	params, err := h.validateAndExtractParams(c)
	if err != nil {
		return DiffResponse{}, typed.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	base := protocol.SafeParam(c, "base")
	head := protocol.SafeParam(c, "head")
	if base == "" || head == "" {
		return DiffResponse{}, typed.NewHTTPError(http.StatusBadRequest, "Missing required parameters: base or head")
	}
	params.Base = base
	params.Head = head

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		return DiffResponse{}, typed.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	contextLines := extractContextLines(c)

	diffResp, err := h.CalculateBranchDiff(ctx, repoPath, params.Base, params.Head, contextLines)
	if err != nil {
		return DiffResponse{}, classifyDiffError(err, http.StatusBadRequest, "REF_NOT_FOUND")
	}

	if c.Query("page") != "" || c.Query("page_size") != "" {
		pagination := extractPaginationParams(c.Request)
		paginatedFiles, updatedPagination := paginateFiles(diffResp.Files, pagination)
		paginated := &PaginatedDiffResponse{
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
		return DiffResponse{}, &responseOverride{status: http.StatusOK, body: paginated}
	}

	return *diffResp, nil
}

// HandleBranchDiffQuery handles GET /diff/branches?base=&head= — query parameter
// 방식 (슬래시 포함 브랜치명 지원).
//
// query 는 typed.RegisterGetWithQuery wrapper 가 BranchDiffQueryRequest 로
// bind 한 뒤 본 함수에 전달한다 — wrapper 가 spec generator 에 base/head 를
// in: query 로 노출 (Issue-2317 후속).
func (h *DiffHandler) HandleBranchDiffQuery(c *gin.Context, req BranchDiffQueryRequest) (DiffResponse, error) {
	ctx := c.Request.Context()

	params, err := h.validateAndExtractParams(c)
	if err != nil {
		return DiffResponse{}, typed.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	base := req.Base
	head := req.Head
	params.Base = base
	params.Head = head

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		return DiffResponse{}, typed.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	contextLines := extractContextLines(c)

	diffResp, err := h.CalculateBranchDiff(ctx, repoPath, params.Base, params.Head, contextLines)
	if err != nil {
		return DiffResponse{}, classifyDiffError(err, http.StatusBadRequest, "REF_NOT_FOUND")
	}

	if c.Query("page") != "" || c.Query("page_size") != "" {
		pagination := extractPaginationParams(c.Request)
		paginatedFiles, updatedPagination := paginateFiles(diffResp.Files, pagination)
		paginated := &PaginatedDiffResponse{
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
		return DiffResponse{}, &responseOverride{status: http.StatusOK, body: paginated}
	}

	return *diffResp, nil
}

// HandleFileDiff handles GET /diff/file?path=&base=&head=.
//
// 응답 type: DiffResponse (이전 legacy spec 은 "FileDiff" 라 명시했으나 runtime
// 은 항상 DiffResponse 를 반환하던 schema/runtime 불일치 — runtime 과 일치
// 시킴, tags Phase 2.7 과 동일 archetype).
//
// 기본 비교 모드는 merge_base — CR 트리(file list)가 merge-base 기준으로 계산되므로
// 단일 파일 diff도 같은 effective_base를 써야 변경 집합이 어긋나지 않습니다 (Issue-2095).
// 호출자가 commit-vs-commit 직접 비교를 원하면 diff_mode=direct로 명시.
func (h *DiffHandler) HandleFileDiff(c *gin.Context, req FileDiffRequest) (DiffResponse, error) {
	ctx := c.Request.Context()

	params, err := h.validateAndExtractParams(c)
	if err != nil {
		return DiffResponse{}, typed.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	filePath := req.Path
	base := req.Base
	head := req.Head
	params.Base = base
	params.Head = head

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		return DiffResponse{}, typed.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	contextLines := extractContextLines(c)

	if c.Query("full_context") == "true" {
		contextLines = 999999
	}

	// diff_mode 결정 + base/head를 effective_base로 해석.
	mode := parseDiffMode(c, DiffModeMergeBase)
	rr, err := nativeResolveDiffRange(ctx, repoPath, base, head, mode)
	if err != nil {
		h.logger.Error("Failed to resolve diff range",
			zap.String("base", base), zap.String("head", head), zap.String("diff_mode", string(mode)), zap.Error(err))
		// legacy 호환 (CR-2805): 404 응답에 code: REF_NOT_FOUND 보존.
		return DiffResponse{}, &responseOverride{
			status: http.StatusNotFound,
			body:   gin.H{"success": false, "error": err.Error(), "code": "REF_NOT_FOUND"},
		}
	}

	// 윈도우(부분) 로드 파라미터 해석 (Issue-3159). line_offset/line_limit 중 하나라도
	// 주어지면 윈도우 모드. limit 은 미지정 시 기본값, 상한 초과 시 캡으로 bound.
	win := fileDiffWindow{}
	if req.LineOffset > 0 || req.LineLimit > 0 {
		win.enabled = true
		win.offset = req.LineOffset
		win.limit = req.LineLimit
		if win.limit <= 0 {
			win.limit = DefaultDiffWindowLines
		}
		if win.limit > MaxDiffWindowLines {
			win.limit = MaxDiffWindowLines
		}
	}

	diffResp, err := h.calculateFileDiff(ctx, repoPath, filePath, rr, contextLines, win)
	if err != nil {
		if IsFileDiffNotFound(err) {
			h.logger.Info("File not present in either base or head",
				zap.String("path", filePath),
				zap.String("base", rr.BaseHash), zap.String("head", rr.HeadHash),
				zap.String("effective_base", rr.EffectiveBase), zap.String("merge_base", rr.MergeBase))
			// legacy 호환 (CR-2805): 404 응답에 code: FILE_NOT_FOUND 보존.
			return DiffResponse{}, &responseOverride{
				status: http.StatusNotFound,
				body:   gin.H{"success": false, "error": err.Error(), "code": "FILE_NOT_FOUND"},
			}
		}
		h.logger.Error("Failed to calculate file diff",
			zap.String("path", filePath),
			zap.String("base", rr.BaseHash), zap.String("head", rr.HeadHash),
			zap.String("effective_base", rr.EffectiveBase), zap.String("merge_base", rr.MergeBase),
			zap.Error(err))
		return DiffResponse{}, classifyDiffError(err, http.StatusBadRequest, "")
	}

	// 응답 envelope에 mode 메타 채우기 — 호출자가 trees/file diff 정렬 여부를 검증할 수 있도록.
	diffResp.Base = base
	diffResp.Head = head
	diffResp.DiffMode = string(rr.Mode)
	diffResp.EffectiveBase = rr.EffectiveBase
	diffResp.MergeBase = rr.MergeBase

	return *diffResp, nil
}

// HandleSingleCommitDiff handles GET /diff/commit/:sha.
func (h *DiffHandler) HandleSingleCommitDiff(c *gin.Context) (SingleCommitDiffResponse, error) {
	ctx := c.Request.Context()

	params, err := h.validateAndExtractParams(c)
	if err != nil {
		return SingleCommitDiffResponse{}, typed.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	sha := c.Param("sha")
	if sha == "" {
		return SingleCommitDiffResponse{}, typed.NewHTTPError(http.StatusBadRequest, "Missing required parameter: sha")
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		return SingleCommitDiffResponse{}, typed.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	contextLines := extractContextLines(c)

	response, err := h.calculateSingleCommitDiff(ctx, repoPath, sha, contextLines)
	if err != nil {
		return SingleCommitDiffResponse{}, classifyDiffError(err, http.StatusBadRequest, "")
	}

	return *response, nil
}

// HandleExpandContext handles GET /diff/expand-context — hunk 사이 숨겨진
// context 라인을 blob 에서 직접 읽어 반환하는 경량 API.
//
// query 는 typed.RegisterGetWithQuery wrapper 가 ExpandContextRequest 로
// bind 한 뒤 본 함수에 전달한다 — wrapper 가 spec generator 에 path/base/head/
// start_old/start_new/count/direction 등을 in: query 로 노출 (Issue-2317
// 후속). from_old/to_old/from_new/side/diff_mode/type 등 보조 파라미터는
// handler 본문에서 c.Query 로 직접 읽는다 (기존 custom 에러 메시지 보존).
func (h *DiffHandler) HandleExpandContext(c *gin.Context, req ExpandContextRequest) (ExpandContextResponse, error) {
	ctx := c.Request.Context()

	params, err := h.validateAndExtractParams(c)
	if err != nil {
		return ExpandContextResponse{}, typed.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	filePath := req.Path
	base := req.Base
	head := req.Head

	var fromOld, toOld, fromNew int

	if startOldStr := req.StartOld; startOldStr != "" {
		startOld, err := strconv.Atoi(startOldStr)
		if err != nil || startOld < 1 {
			return ExpandContextResponse{}, typed.NewHTTPError(http.StatusBadRequest, "start_old must be a positive integer")
		}

		startNewStr := req.StartNew
		if startNewStr == "" {
			return ExpandContextResponse{}, typed.NewHTTPError(http.StatusBadRequest, "start_new is required when using start_old")
		}
		startNew, err := strconv.Atoi(startNewStr)
		if err != nil || startNew < 1 {
			return ExpandContextResponse{}, typed.NewHTTPError(http.StatusBadRequest, "start_new must be a positive integer")
		}

		count := 20
		if countStr := req.Count; countStr != "" {
			count, err = strconv.Atoi(countStr)
			if err != nil || count < 1 {
				return ExpandContextResponse{}, typed.NewHTTPError(http.StatusBadRequest, "count must be a positive integer")
			}
		}

		direction := req.Direction
		if direction == "" {
			direction = "down"
		}

		if direction == "up" {
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
			fromOld = startOld
			toOld = startOld + count - 1
			fromNew = startNew
		}
	} else {
		fromOldStr := c.Query("from_old")
		toOldStr := c.Query("to_old")
		fromNewStr := c.Query("from_new")

		if fromOldStr == "" || toOldStr == "" || fromNewStr == "" {
			return ExpandContextResponse{}, typed.NewHTTPError(http.StatusBadRequest, "required parameters: path, base, head, and either (start_old, start_new, count) or (from_old, to_old, from_new)")
		}

		fromOld, err = strconv.Atoi(fromOldStr)
		if err != nil || fromOld < 1 {
			return ExpandContextResponse{}, typed.NewHTTPError(http.StatusBadRequest, "from_old must be a positive integer")
		}
		toOld, err = strconv.Atoi(toOldStr)
		if err != nil || toOld < fromOld {
			return ExpandContextResponse{}, typed.NewHTTPError(http.StatusBadRequest, "to_old must be >= from_old")
		}
		fromNew, err = strconv.Atoi(fromNewStr)
		if err != nil || fromNew < 1 {
			return ExpandContextResponse{}, typed.NewHTTPError(http.StatusBadRequest, "from_new must be a positive integer")
		}
	}

	if toOld-fromOld+1 > 500 {
		return ExpandContextResponse{}, typed.NewHTTPError(http.StatusBadRequest, "line range too large (max 500 lines)")
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		return ExpandContextResponse{}, typed.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	// diff_mode 결정 + effective_base 해석 (Issue-2095).
	// side=old일 때는 effective_base에서 context 라인을 읽어야 file diff hunk와 라인 번호가 정렬.
	mode := parseDiffMode(c, DiffModeMergeBase)
	rr, err := nativeResolveDiffRange(ctx, repoPath, base, head, mode)
	if err != nil {
		// legacy 호환 (CR-2805): ref resolve 실패는 404 + code: REF_NOT_FOUND.
		return ExpandContextResponse{}, &responseOverride{
			status: http.StatusNotFound,
			body:   gin.H{"success": false, "error": err.Error(), "code": "REF_NOT_FOUND"},
		}
	}

	ref := rr.HeadHash
	if side := c.Query("side"); side == "old" {
		ref = rr.EffectiveBase
	}

	response, err := nativeExpandContextLines(ctx, repoPath, ref, filePath, fromOld, toOld, fromNew)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "not found") {
			return ExpandContextResponse{}, typed.NewHTTPError(http.StatusNotFound, errStr)
		}
		return ExpandContextResponse{}, typed.NewHTTPError(http.StatusInternalServerError, errStr)
	}

	return *response, nil
}

// HandlePatch handles GET /diff/patch/:base/:head — text/plain output.
//
// raw response (Content-Type: text/plain) 이라 typed wrapper 와 호환되지 않음.
// gin.HandlerFunc 시그니처 유지 → routes.go 에서 RegisterRaw 로 등록.
func (h *DiffHandler) HandlePatch(c *gin.Context) {
	params, err := h.validateAndExtractParams(c)
	if err != nil {
		h.logger.Error("Failed to validate and extract parameters", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	base := protocol.SafeParam(c, "base")
	head := protocol.SafeParam(c, "head")
	if base == "" || head == "" {
		h.logger.Error("Missing required parameters: base or head")
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "Missing required parameters: base or head"})
		return
	}
	params.Base = base
	params.Head = head

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		h.logger.Error("Invalid repository path", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	patchContent, err := h.generatePatch(repoPath, params.Base, params.Head)
	if err != nil {
		h.logger.Error("Failed to generate patch", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s-%s.patch\"", params.Base, params.Head))
	c.Status(http.StatusOK)
	if _, err := c.Writer.Write([]byte(patchContent)); err != nil {
		h.logger.Error("Failed to write patch response", zap.Error(err))
	}
}

// HandleDiffFileSearch handles GET /diff/search/files.
//
// diff_mode 쿼리(merge_base 기본 / direct)로 file list 계산 기준을 통일 (Issue-2095).
// 레거시 type=branch|commit 별칭은 diff_mode 미지정 시 fallback. 응답에 effective_base /
// diff_mode 가 함께 노출돼 호출자가 file list와 file diff의 정렬 여부를 검증 가능.
func (h *DiffHandler) HandleDiffFileSearch(c *gin.Context) (DiffFileSearchResponse, error) {
	ctx := c.Request.Context()

	params, err := h.validateAndExtractParams(c)
	if err != nil {
		return DiffFileSearchResponse{}, typed.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	base := c.Query("base")
	head := c.Query("head")
	query := c.Query("query")

	if base == "" || head == "" {
		return DiffFileSearchResponse{}, typed.NewHTTPError(http.StatusBadRequest, "Missing required parameters: base, head")
	}
	if query == "" {
		return DiffFileSearchResponse{}, typed.NewHTTPError(http.StatusBadRequest, "Missing required parameter: query")
	}

	// diff_mode 결정. 명시 우선; 없으면 레거시 type 별칭; 둘 다 없으면 merge_base.
	mode := DiffModeMergeBase
	if rawMode := strings.TrimSpace(c.Query("diff_mode")); rawMode != "" {
		m := DiffMode(rawMode)
		if !m.IsValid() {
			return DiffFileSearchResponse{}, typed.NewHTTPError(http.StatusBadRequest, "diff_mode must be 'merge_base' or 'direct'")
		}
		mode = m
	} else if rawType := strings.TrimSpace(c.Query("type")); rawType != "" {
		switch rawType {
		case "branch":
			mode = DiffModeMergeBase
		case "commit":
			mode = DiffModeDirect
		default:
			return DiffFileSearchResponse{}, typed.NewHTTPError(http.StatusBadRequest, "type must be 'commit' or 'branch'")
		}
	}

	params.Base = base
	params.Head = head

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		return DiffFileSearchResponse{}, typed.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	rr, err := nativeResolveDiffRange(ctx, repoPath, base, head, mode)
	if err != nil {
		// legacy 호환 (CR-2805): ref resolve 실패는 404 + code: REF_NOT_FOUND.
		return DiffFileSearchResponse{}, &responseOverride{
			status: http.StatusNotFound,
			body:   gin.H{"success": false, "error": err.Error(), "code": "REF_NOT_FOUND"},
		}
	}

	fileMetas, err := nativeGetDiffFileList(ctx, repoPath, rr.EffectiveBase, rr.HeadHash)
	if err != nil {
		return DiffFileSearchResponse{}, typed.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	queryLower := strings.ToLower(query)
	var results []DiffFileSearchResult
	for _, meta := range fileMetas {
		pathMatch := strings.Contains(strings.ToLower(meta.Path), queryLower)
		oldPathMatch := meta.OldPath != "" && strings.Contains(strings.ToLower(meta.OldPath), queryLower)
		if pathMatch || oldPathMatch {
			results = append(results, DiffFileSearchResult{
				Path:      meta.Path,
				OldPath:   meta.OldPath,
				Status:    meta.Status,
				Additions: meta.Additions,
				Deletions: meta.Deletions,
				IsBinary:  meta.IsBinary,
			})
		}
	}

	if results == nil {
		results = []DiffFileSearchResult{}
	}

	// 레거시 type 필드 채우기 (호환성). 새 호출자는 diff_mode를 봅니다.
	legacyType := "branch"
	if rr.Mode == DiffModeDirect {
		legacyType = "commit"
	}

	return DiffFileSearchResponse{
		Success:       true,
		Query:         query,
		Type:          legacyType,
		DiffMode:      string(rr.Mode),
		Base:          base,
		Head:          head,
		EffectiveBase: rr.EffectiveBase,
		MergeBase:     rr.MergeBase,
		Files:         results,
		TotalCount:    len(results),
	}, nil
}
