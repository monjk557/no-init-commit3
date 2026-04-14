// Package protocol은 Git REST API 엔드포인트의 브랜치 작업을 제공합니다.
// 브랜치 생성, 업데이트, 삭제 등의 Git 브랜치 관리 기능을 구현합니다.
package branches

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/protocol"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// BranchOperationsHandler는 Git 브랜치 작업을 처리합니다.
// 브랜치 생성, 업데이트, 삭제 요청을 처리하고 적절한 응답을 제공합니다.
type BranchOperationsHandler struct {
	config            *config.Config
	logger            *logging.Logger
	pathConfig        *repositories.PathConfig
	pathValidator     *repositories.GitAPIPathValidator
	branchNamePattern *regexp.Regexp
	service           *BranchService
	eventsClient      events.RedisStreamClient
}

// NewBranchOperationsHandler는 새로운 브랜치 작업 핸들러를 생성합니다.
// 설정과 권한 클라이언트를 사용하여 필요한 컴포너트들을 초기화합니다.
func NewBranchOperationsHandler(cfg *config.Config) (*BranchOperationsHandler, error) {
	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	// Create path configuration
	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	// Create path validator
	pathValidator := repositories.NewGitAPIPathValidator(pathConfig)

	// Compile branch name pattern following git check-ref-format rules (blocklist approach)
	// Blocks: control chars (0x00-0x1f, 0x7f), space, ~, ^, :, ?, *, [, \
	branchNamePattern, err := regexp.Compile(`^[^\x00-\x20\x7f~^:?*\[\\]+$`)
	if err != nil {
		return nil, fmt.Errorf("failed to compile branch name pattern: %w", err)
	}

	// Create branch service
	service := NewBranchService(logger)

	// Initialize events client if enabled
	var eventsClient events.RedisStreamClient
	if cfg.Events.Enabled && cfg.Events.RedisURL != "" {
		eventsClient, err = events.NewRedisStreamClient(cfg.Events.RedisURL, logger.Logger)
		if err != nil {
			// Log warning but continue without events
			_ = err
		}
	}

	return &BranchOperationsHandler{
		config:            cfg,
		logger:            logger,
		pathConfig:        pathConfig,
		pathValidator:     pathValidator,
		branchNamePattern: branchNamePattern,
		service:           service,
		eventsClient:      eventsClient,
	}, nil
}

// repositoryContext는 저장소 관련 공통 컨텍스트를 담는 구조체입니다.
type repositoryContext struct {
	owner    string
	repo     string
	repoPath string
}

// validateAndGetRepoPath는 저장소 검증 및 경로를 가져오는 공통 로직입니다.
func (h *BranchOperationsHandler) validateAndGetRepoPath(ctx context.Context, c *gin.Context, owner, repo string) (*repositoryContext, bool) {
	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Invalid path: %v", err),
		})
		return nil, false
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return nil, false
	}

	return &repositoryContext{
		owner:    owner,
		repo:     repo,
		repoPath: repoPath,
	}, true
}

// suggestBranchMaxAttempts는 대체 브랜치 이름을 찾기 위한 최대 시도 횟수입니다.
const suggestBranchMaxAttempts = 5

// randomSuffix는 4바이트(8 hex chars) 난수 suffix를 생성합니다.
func randomSuffix() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// fallback: 타임스탬프 기반
		return fmt.Sprintf("%x", time.Now().UnixNano()&0xFFFFFFFF)
	}
	return hex.EncodeToString(b)
}

// suggestAvailableName은 요청된 이름이 이미 존재할 때 난수 suffix를 붙여 사용 가능한 이름을 제안합니다.
func suggestAvailableName(ctx context.Context, repoPath, baseName string) *string {
	for i := 0; i < suggestBranchMaxAttempts; i++ {
		candidate := baseName + "-" + randomSuffix()
		exists, _, err := NativeCheckBranchExists(ctx, repoPath, candidate)
		if err != nil {
			continue
		}
		if !exists {
			return &candidate
		}
	}
	return nil
}

// HandleValidateBranchName handles GET /repos/{owner}/{repo}/branches/validate/*name
// 브랜치 이름의 사용 가능 여부를 확인하고, 이미 존재하면 난수 suffix 기반 대체 이름을 제안합니다.
func (h *BranchOperationsHandler) HandleValidateBranchName(c *gin.Context) {
	ctx := c.Request.Context()

	// Extract URL parameters
	owner := c.Param("owner")
	repo := c.Param("repo")
	name := strings.TrimPrefix(protocol.SafeParam(c, "name"), "/")

	// 저장소 경로 검증
	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return
	}

	// native git으로 브랜치 존재 여부 확인
	exists, _, err := NativeCheckBranchExists(ctx, repoPath, name)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "repository corrupted"})
		return
	}

	if !exists {
		c.JSON(http.StatusOK, ValidateBranchNameResponse{
			Success:     true,
			IsAvailable: true,
		})
		return
	}

	// 이미 존재하면 난수 suffix 기반 대체 이름 제안
	c.JSON(http.StatusOK, ValidateBranchNameResponse{
		Success:       true,
		IsAvailable:   false,
		SuggestedName: suggestAvailableName(ctx, repoPath, name),
	})
}

// HandleGetBranches handles GET /repos/{owner}/{repo}/branches for getting branches.
// Supports optional ?search=<keyword> to filter branches by name (case-insensitive substring match).
func (h *BranchOperationsHandler) HandleGetBranches(c *gin.Context) {
	ctx := c.Request.Context()

	// Extract URL parameters
	owner := c.Param("owner")
	repo := c.Param("repo")

	// 파라미터 추출 및 검증
	limitStr := c.Query("limit")
	skipStr := c.Query("skip")
	searchQuery := c.Query("search")              // 브랜치 이름 검색 (대소문자 무관 부분 일치)
	sortParam := c.DefaultQuery("sort", "newest") // "newest" (기본), "oldest", "name"
	sort := ParseBranchSort(sortParam)
	emailFilter := c.Query("email")   // committer email 필터 (대소문자 무관)
	statusFilter := c.Query("status") // "active" 또는 "stale"

	// status 파라미터 검증
	if statusFilter != "" && statusFilter != "active" && statusFilter != "stale" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid status: must be 'active' or 'stale'"})
		return
	}

	// limit 파라미터 파싱 및 검증
	limit := DefaultBranchesLimit
	if limitStr != "" {
		parsedLimit, err := strconv.Atoi(limitStr)
		if err != nil || parsedLimit < MinBranchesLimit {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": fmt.Sprintf("invalid limit: must be >= %d", MinBranchesLimit)})
			return
		}
		if parsedLimit > MaxBranchesLimit {
			limit = MaxBranchesLimit
		} else {
			limit = parsedLimit
		}
	}

	// skip 파라미터 파싱 및 검증
	skip := 0
	if skipStr != "" {
		parsedSkip, err := strconv.Atoi(skipStr)
		if err != nil || parsedSkip < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid skip: must be >= 0"})
			return
		}
		skip = parsedSkip
	}

	// 저장소 경로 검증
	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return
	}

	// native git으로 모든 브랜치 가져오기 (정렬 적용)
	allBranches, err := h.service.GetBranches(ctx, repoPath, sort)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "repository corrupted"})
		return
	}

	// nil 체크: 빈 배열 보장
	if allBranches == nil {
		allBranches = []BranchInfo{}
	}

	// search 필터 적용 (대소문자 무관 부분 일치)
	if searchQuery != "" {
		searchLower := strings.ToLower(searchQuery)
		filtered := make([]BranchInfo, 0, len(allBranches))
		for _, b := range allBranches {
			if strings.Contains(strings.ToLower(b.Name), searchLower) {
				filtered = append(filtered, b)
			}
		}
		allBranches = filtered
	}

	// email 필터 적용 (대소문자 무관)
	if emailFilter != "" {
		emailLower := strings.ToLower(emailFilter)
		filtered := make([]BranchInfo, 0, len(allBranches))
		for _, b := range allBranches {
			if strings.EqualFold(b.CommitterEmail, emailLower) {
				filtered = append(filtered, b)
			}
		}
		allBranches = filtered
	}

	// status 필터 적용 (active: 90일 미만, stale: 90일 이상)
	if statusFilter != "" {
		filtered := make([]BranchInfo, 0, len(allBranches))
		for _, b := range allBranches {
			daysSinceLastCommit := int(time.Since(b.Date).Hours() / 24)
			isStale := daysSinceLastCommit >= StaleDaysThreshold
			if (statusFilter == "stale" && isStale) || (statusFilter == "active" && !isStale) {
				filtered = append(filtered, b)
			}
		}
		allBranches = filtered
	}

	totalCount := len(allBranches)

	// 페이지네이션 적용
	var branches []BranchInfo
	if skip >= totalCount {
		// skip이 전체 개수보다 크면 빈 배열
		branches = []BranchInfo{}
	} else {
		end := skip + limit
		if end > totalCount {
			end = totalCount
		}
		branches = allBranches[skip:end]
	}

	// 페이지네이션 정보 계산
	currentPage := 1
	if limit > 0 {
		currentPage = skip/limit + 1
	}

	// 페이지네이션 정보와 함께 응답 생성
	response := GetBranchesResponse{
		Success: true,
		Results: branches,
		Pagination: PaginationInfo{
			Count:       totalCount,
			Next:        buildPagination(totalCount, currentPage, limit, c.Request.URL.String()).Next,
			Previous:    buildPagination(totalCount, currentPage, limit, c.Request.URL.String()).Previous,
			CurrentPage: currentPage,
			TotalPages:  buildPagination(totalCount, currentPage, limit, c.Request.URL.String()).TotalPages,
			PageSize:    limit,
		},
	}

	c.JSON(http.StatusOK, response)
}

// HandleBranchStats handles POST /repos/{owner}/{repo}/branches/stats
// 요청된 브랜치들의 ahead/behind를 default branch 기준으로 배치 계산합니다.
func (h *BranchOperationsHandler) HandleBranchStats(c *gin.Context) {
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")

	var req BranchStatsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": fmt.Sprintf("invalid request: %v", err)})
		return
	}

	if len(req.Branches) > MaxBranchesLimit {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": fmt.Sprintf("too many branches: max %d", MaxBranchesLimit)})
		return
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return
	}

	// default branch 확인
	headRef := nativeGetHead(ctx, repoPath)
	defaultBranch := strings.TrimPrefix(headRef, "refs/heads/")
	if defaultBranch == "" {
		defaultBranch = "main"
	}

	// goroutine 병렬 계산
	type result struct {
		name string
		item BranchStatsItem
	}

	resultCh := make(chan result, len(req.Branches))

	for _, branch := range req.Branches {
		go func(name string) {
			if name == defaultBranch {
				resultCh <- result{name: name, item: BranchStatsItem{Ahead: 0, Behind: 0}}
				return
			}
			ab, err := NativeCalculateAheadBehind(ctx, repoPath, name, defaultBranch)
			if err != nil {
				resultCh <- result{name: name, item: BranchStatsItem{Ahead: 0, Behind: 0}}
				return
			}
			resultCh <- result{name: name, item: BranchStatsItem{Ahead: ab.Ahead, Behind: ab.Behind}}
		}(branch)
	}

	stats := make(map[string]BranchStatsItem, len(req.Branches))
	for range req.Branches {
		r := <-resultCh
		stats[r.name] = r.item
	}

	c.JSON(http.StatusOK, BranchStatsResponse{
		Success: true,
		Stats:   stats,
	})
}

// buildPagination은 페이지네이션 정보를 생성합니다.
func buildPagination(totalCount, currentPage, pageSize int, baseURL string) PaginationInfo {
	totalPages := (totalCount + pageSize - 1) / pageSize
	if totalPages == 0 {
		totalPages = 1
	}

	var next, previous *string

	// limit과 skip을 사용하므로 URL 파라미터도 limit/skip으로 생성
	if currentPage < totalPages {
		nextSkip := (currentPage) * pageSize
		nextURL := fmt.Sprintf("%s?limit=%d&skip=%d", baseURL, pageSize, nextSkip)
		next = &nextURL
	}

	if currentPage > 1 {
		prevSkip := (currentPage - 2) * pageSize
		prevURL := fmt.Sprintf("%s?limit=%d&skip=%d", baseURL, pageSize, prevSkip)
		previous = &prevURL
	}

	return PaginationInfo{
		Count:       totalCount,
		Next:        next,
		Previous:    previous,
		CurrentPage: currentPage,
		TotalPages:  totalPages,
		PageSize:    pageSize,
	}
}

// HandleResolveBranch handles GET /repos/{owner}/{repo}/branches/resolve/*ref — single branch → commit SHA.
func (h *BranchOperationsHandler) HandleResolveBranch(c *gin.Context) {
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")
	ref := strings.TrimPrefix(protocol.SafeParam(c, "ref"), "/")
	if ref == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "branch name is required"})
		return
	}

	// 저장소 경로 검증
	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return
	}

	// native git으로 브랜치 존재 여부 확인
	exists, hash, err := NativeCheckBranchExists(ctx, repoPath, ref)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "repository corrupted"})
		return
	}
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": fmt.Sprintf("branch '%s' not found", ref)})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success":    true,
		"name":       ref,
		"commit_sha": hash,
		"ref_path":   "refs/heads/" + ref,
	})
}

// HandleValidateReopen handles POST /repos/{owner}/{repo}/branches/validate-reopen.
// CR reopen 전 source/target 브랜치 존재 여부와 lineage(계보) 검증을 한 번에 수행합니다.
func (h *BranchOperationsHandler) HandleValidateReopen(c *gin.Context) {
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")

	var req ValidateReopenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid request body: " + err.Error()})
		return
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return
	}

	// 1) source/target 브랜치 존재 확인 (병렬)
	type branchResult struct {
		exists bool
		hash   string
		err    error
	}
	sourceCh := make(chan branchResult, 1)
	targetCh := make(chan branchResult, 1)

	go func() {
		exists, hash, err := NativeCheckBranchExists(ctx, repoPath, req.SourceBranchName)
		sourceCh <- branchResult{exists, hash, err}
	}()
	go func() {
		exists, hash, err := NativeCheckBranchExists(ctx, repoPath, req.TargetBranchName)
		targetCh <- branchResult{exists, hash, err}
	}()

	sourceRes := <-sourceCh
	targetRes := <-targetCh

	if sourceRes.err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "failed to check source branch"})
		return
	}
	if !sourceRes.exists {
		c.JSON(http.StatusConflict, gin.H{"success": false, "error": fmt.Sprintf("source branch '%s' no longer exists", req.SourceBranchName)})
		return
	}
	if targetRes.err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "failed to check target branch"})
		return
	}
	if !targetRes.exists {
		c.JSON(http.StatusConflict, gin.H{"success": false, "error": fmt.Sprintf("target branch '%s' no longer exists", req.TargetBranchName)})
		return
	}

	currentSourceTip := sourceRes.hash
	currentTargetTip := targetRes.hash

	// 2) lineage 검증: stored SHA와 현재 tip이 다르면 ancestor 관계 확인
	lineageValid := true
	if req.StoredSourceCommitSHA != "" && currentSourceTip != req.StoredSourceCommitSHA {
		mergeBase, err := NativeMergeBase(ctx, repoPath, req.StoredSourceCommitSHA, currentSourceTip)
		if err != nil || mergeBase != req.StoredSourceCommitSHA {
			lineageValid = false
		}
	}

	if !lineageValid {
		c.JSON(http.StatusConflict, gin.H{
			"success": false,
			"error":   "source branch has been recreated from a different merge base",
		})
		return
	}

	c.JSON(http.StatusOK, ValidateReopenResponse{
		Success:         true,
		SourceCommitSHA: currentSourceTip,
		TargetCommitSHA: currentTargetTip,
		LineageValid:    true,
	})
}

// HandleCreateBranch handles POST /repos/{owner}/{repo}/branches for creating new branches.
func (h *BranchOperationsHandler) HandleCreateBranch(c *gin.Context) {
	ctx := c.Request.Context()

	// Extract URL parameters
	owner := c.Param("owner")
	repo := c.Param("repo")

	h.logger.LogInfo(c.Request.Context(), "Processing branch creation request",
		zap.String("event", "branch_create_request"),
		zap.String("owner", owner),
		zap.String("repo", repo))

	// Parse and validate JSON request
	var req CreateBranchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Invalid JSON: %v", err),
		})
		return
	}

	// Validate branch name
	if err := h.validateBranchName(req.Name); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Invalid branch name: %v", err),
		})
		return
	}

	// 공통 검증 및 저장소 열기
	repoCtx, ok := h.validateAndGetRepoPath(ctx, c, owner, repo)
	if !ok {
		return
	}

	// Call service to create branch (native git — uses repoPath only)
	result, err := h.service.CreateBranch(c.Request.Context(), repoCtx.repoPath, req)
	if err != nil {
		// Determine appropriate status code based on error message
		statusCode := http.StatusInternalServerError
		if strings.Contains(err.Error(), "already exists") {
			statusCode = http.StatusConflict
		} else if strings.Contains(err.Error(), "failed to resolve") {
			statusCode = http.StatusBadRequest
		}
		c.JSON(statusCode, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	response := CreateBranchResponse{
		Success:     true,
		BranchName:  result.BranchName,
		CommitSHA:   result.CommitSHA,
		Source:      result.Source,
		RefPath:     result.RefPath,
		IsNewBranch: result.IsNewBranch,
		Message:     result.Message,
		CreatedAt:   result.CreatedAt,
		Details: CreateBranchDetails{
			Checkout:  result.Details.Checkout,
			ForceUsed: result.Details.ForceUsed,
			Author:    result.Details.Author,
		},
	}
	c.JSON(http.StatusOK, response)
}

// HandleUpdateBranch handles PATCH /repos/{owner}/{repo}/branches/{branch} for updating branch pointers
func (h *BranchOperationsHandler) HandleUpdateBranch(c *gin.Context) {
	ctx := c.Request.Context()

	// Extract URL parameters
	owner := c.Param("owner")
	repo := c.Param("repo")

	// Extract branch name from wildcard parameter (remove leading slash)
	branchName := strings.TrimPrefix(protocol.SafeParam(c, "branch"), "/")

	h.logger.LogInfo(c.Request.Context(), "Processing branch update request",
		zap.String("event", "branch_update_request"),
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.String("branch", branchName))

	// Parse and validate JSON request
	var req UpdateBranchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Invalid JSON: %v", err),
		})
		return
	}

	// Validate that either target or new_name is provided, but not both
	hasTarget := req.Target != ""
	hasNewName := req.NewName != ""
	if !hasTarget && !hasNewName {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "Either 'target' (for commit update) or 'new_name' (for rename) must be provided",
		})
		return
	}
	if hasTarget && hasNewName {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "Cannot provide both 'target' and 'new_name'. Use 'target' for commit update or 'new_name' for rename",
		})
		return
	}

	// Validate branch name
	if err := h.validateBranchName(branchName); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Invalid branch name: %v", err),
		})
		return
	}

	// Validate new branch name if rename operation
	if hasNewName {
		if err := h.validateBranchName(req.NewName); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   fmt.Sprintf("Invalid new branch name: %v", err),
			})
			return
		}
	}

	// 공통 검증 및 저장소 열기
	repoCtx, ok := h.validateAndGetRepoPath(ctx, c, owner, repo)
	if !ok {
		return
	}

	// Call service to update branch (native git — uses repoPath only)
	result, err := h.service.UpdateBranch(c.Request.Context(), repoCtx.repoPath, branchName, req)
	if err != nil {
		// Determine appropriate status code based on error message
		statusCode := http.StatusInternalServerError
		if strings.Contains(err.Error(), "does not exist") {
			statusCode = http.StatusNotFound
		} else if strings.Contains(err.Error(), "cannot be the same") {
			statusCode = http.StatusBadRequest
		} else if strings.Contains(err.Error(), "already exists") {
			statusCode = http.StatusConflict
		} else if strings.Contains(err.Error(), "not a fast-forward") || strings.Contains(err.Error(), "must be provided") {
			statusCode = http.StatusBadRequest
		}
		c.JSON(statusCode, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// Publish branch rename event to Redis Stream (non-blocking)
	if result.Operation == "rename" && h.eventsClient != nil {
		relativePath := events.GetRelativeRepoPath(repoCtx.repoPath, h.pathConfig.GetRepoRoot())
		userID := c.GetHeader("X-USER-ID")
		event := &events.CommitEvent{
			RepoPath:      relativePath,
			EventType:     events.EventTypeRename,
			CommitHash:    result.CommitHash,
			OldRev:        result.CommitHash,
			RefName:       result.RefPath,
			OldRefName:    "refs/heads/" + result.OldBranchName,
			UserID:        userID,
			CommitMessage: "",
			Timestamp:     time.Now(),
		}
		go func() {
			evCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if pubErr := h.eventsClient.AddCommitEventToStream(evCtx, event); pubErr != nil {
				h.logger.Warn("Failed to publish branch rename event",
					zap.String("repo_path", repoCtx.repoPath),
					zap.String("old_branch", result.OldBranchName),
					zap.String("new_branch", result.NewBranchName),
					zap.Error(pubErr))
			}
		}()
	}

	// Prepare response data
	responseData := map[string]interface{}{
		"branch_name":   result.BranchName,
		"commit_hash":   result.CommitHash,
		"previous_hash": result.PreviousHash,
		"operation":     result.Operation,
		"fast_forward":  result.FastForward,
	}

	// Only include "forced" field if currentCommit != targetCommit (v1 compatibility)
	// When currentCommit == targetCommit, v1 omits the "forced" field
	if result.PreviousHash != result.CommitHash {
		responseData["forced"] = result.Forced
	}

	// Add rename-specific fields
	if result.Operation == "rename" {
		responseData["old_branch_name"] = result.OldBranchName
		responseData["new_branch_name"] = result.NewBranchName
		responseData["ref_path"] = result.RefPath
		responseData["head_updated"] = result.HeadUpdated
	}

	if result.Message != "" {
		responseData["message"] = result.Message
	}

	response := gin.H{"success": true}
	for k, v := range responseData {
		response[k] = v
	}
	c.JSON(http.StatusOK, response)
}

func (h *BranchOperationsHandler) HandleDeleteBranch(c *gin.Context) {
	ctx := c.Request.Context()

	// Extract URL parameters
	owner := c.Param("owner")
	repo := c.Param("repo")

	// Extract branch name from wildcard parameter (remove leading slash)
	branchName := strings.TrimPrefix(protocol.SafeParam(c, "branch"), "/")

	h.logger.LogInfo(c.Request.Context(), "Processing branch deletion request",
		zap.String("event", "branch_delete_request"),
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.String("branch", branchName),
		zap.String("raw_branch_param", c.Param("branch")),
		zap.String("request_path", c.Request.URL.Path))

	// Validate branch name
	if err := h.validateBranchName(branchName); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Invalid branch name: %v", err),
		})
		return
	}

	// 공통 검증 및 저장소 열기
	repoCtx, ok := h.validateAndGetRepoPath(ctx, c, owner, repo)
	if !ok {
		return
	}

	// Call service to delete branch (native git — uses repoPath only)
	result, err := h.service.DeleteBranch(c.Request.Context(), repoCtx.repoPath, branchName)
	if err != nil {
		// Determine appropriate status code based on error message
		statusCode := http.StatusInternalServerError
		if strings.Contains(err.Error(), "does not exist") {
			statusCode = http.StatusNotFound
		}
		c.JSON(statusCode, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// Publish branch delete event to Redis Stream (non-blocking)
	if h.eventsClient != nil {
		relativePath := events.GetRelativeRepoPath(repoCtx.repoPath, h.pathConfig.GetRepoRoot())
		userID := c.GetHeader("X-USER-ID")
		event := &events.CommitEvent{
			RepoPath:      relativePath,
			EventType:     events.EventTypeBranchDelete,
			CommitHash:    result.CommitHash,
			OldRev:        result.CommitHash,
			RefName:       "refs/heads/" + branchName,
			UserID:        userID,
			CommitMessage: "",
			Timestamp:     time.Now(),
		}
		go func() {
			evCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if pubErr := h.eventsClient.AddCommitEventToStream(evCtx, event); pubErr != nil {
				h.logger.Warn("Failed to publish branch delete event",
					zap.String("repo_path", repoCtx.repoPath),
					zap.String("branch", branchName),
					zap.Error(pubErr))
			}
		}()
	}

	response := gin.H{
		"success":     true,
		"branch_name": result.BranchName,
		"commit_hash": result.CommitHash,
		"operation":   "delete",
	}
	c.JSON(http.StatusOK, response)
}

// HandleBulkDeleteBranches handles DELETE /repos/{owner}/{repo}/branches/bulk
// 여러 브랜치를 한 번에 삭제합니다.
func (h *BranchOperationsHandler) HandleBulkDeleteBranches(c *gin.Context) {
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")

	var req BulkDeleteBranchesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid request body: branches array is required"})
		return
	}

	repoCtx, ok := h.validateAndGetRepoPath(ctx, c, owner, repo)
	if !ok {
		return
	}

	results := make([]BulkDeleteBranchResult, 0, len(req.Branches))
	succeeded := 0
	failed := 0

	for _, branchName := range req.Branches {
		if err := h.validateBranchName(branchName); err != nil {
			results = append(results, BulkDeleteBranchResult{
				BranchName: branchName,
				Success:    false,
				Error:      fmt.Sprintf("invalid branch name: %v", err),
			})
			failed++
			continue
		}

		result, err := h.service.DeleteBranch(ctx, repoCtx.repoPath, branchName)
		if err != nil {
			results = append(results, BulkDeleteBranchResult{
				BranchName: branchName,
				Success:    false,
				Error:      err.Error(),
			})
			failed++
			continue
		}

		results = append(results, BulkDeleteBranchResult{
			BranchName: result.BranchName,
			CommitHash: result.CommitHash,
			Success:    true,
		})
		succeeded++

		// Publish branch delete event (non-blocking)
		if h.eventsClient != nil {
			relativePath := events.GetRelativeRepoPath(repoCtx.repoPath, h.pathConfig.GetRepoRoot())
			userID := c.GetHeader("X-USER-ID")
			event := &events.CommitEvent{
				RepoPath:   relativePath,
				EventType:  events.EventTypeBranchDelete,
				CommitHash: result.CommitHash,
				OldRev:     result.CommitHash,
				RefName:    "refs/heads/" + branchName,
				UserID:     userID,
				Timestamp:  time.Now(),
			}
			go func(e *events.CommitEvent, bn string) {
				evCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if pubErr := h.eventsClient.AddCommitEventToStream(evCtx, e); pubErr != nil {
					h.logger.Warn("Failed to publish bulk branch delete event",
						zap.String("branch", bn),
						zap.Error(pubErr))
				}
			}(event, branchName)
		}
	}

	c.JSON(http.StatusOK, BulkDeleteBranchesResponse{
		Success:   failed == 0,
		Total:     len(req.Branches),
		Succeeded: succeeded,
		Failed:    failed,
		Results:   results,
	})
}
