// Package branches 는 Git REST API 엔드포인트의 브랜치 작업을 제공합니다.
// 브랜치 생성, 업데이트, 삭제 등의 Git 브랜치 관리 기능을 구현합니다.
package branches

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/api/typed"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/protocol"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// BranchOperationsHandler는 Git 브랜치 작업을 처리합니다.
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
func NewBranchOperationsHandler(cfg *config.Config) (*BranchOperationsHandler, error) {
	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	pathValidator := repositories.NewGitAPIPathValidator(pathConfig)

	branchNamePattern, err := regexp.Compile(`^[^\x00-\x20\x7f~^:?*\[\\]+$`)
	if err != nil {
		return nil, fmt.Errorf("failed to compile branch name pattern: %w", err)
	}

	service := NewBranchService(logger)

	var eventsClient events.RedisStreamClient
	if cfg.Events.Enabled {
		eventsClient, err = events.NewRedisStreamClientFromConfig(cfg.Events, logger.Logger)
		if err != nil {
			logger.Warn("Failed to initialize Redis events client, continuing without events",
				zap.Error(err))
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

// validateAndGetRepoPath 는 저장소 검증 + 경로 조회를 수행한다. 실패 시
// *typed.HTTPError 반환 (path validation → 400, repo not found → 404).
func (h *BranchOperationsHandler) validateAndGetRepoPath(_ context.Context, owner, repo string) (*repositoryContext, error) {
	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo)); err != nil {
		return nil, typed.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Invalid path: %v", err))
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return nil, typed.NewHTTPError(http.StatusNotFound, "repository not found")
	}

	// 경로 안전성 검증과 별개로 저장소 실제 존재 여부를 확인한다.
	// GetAndValidateRepoPath 는 path traversal 등만 막고 존재는 검사하지 않음.
	if !repoExists(repoPath) {
		return nil, typed.NewHTTPError(http.StatusNotFound, "repository not found")
	}

	return &repositoryContext{
		owner:    owner,
		repo:     repo,
		repoPath: repoPath,
	}, nil
}

// repoExists 는 저장소 경로가 파일시스템에 실제로 존재하는지 확인한다.
func repoExists(repoPath string) bool {
	_, err := os.Stat(repoPath)
	return err == nil
}

// suggestBranchMaxAttempts는 대체 브랜치 이름을 찾기 위한 최대 시도 횟수입니다.
const suggestBranchMaxAttempts = 5

// randomSuffix는 4바이트(8 hex chars) 난수 suffix를 생성합니다.
func randomSuffix() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
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

// HandleValidateBranchName handles GET /branches/validate/*name.
func (h *BranchOperationsHandler) HandleValidateBranchName(c *gin.Context) (ValidateBranchNameResponse, error) {
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")
	name := strings.TrimPrefix(protocol.SafeParam(c, "name"), "/")

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return ValidateBranchNameResponse{}, typed.NewHTTPError(http.StatusNotFound, "repository not found")
	}
	if !repoExists(repoPath) {
		return ValidateBranchNameResponse{}, typed.NewHTTPError(http.StatusNotFound, "repository not found")
	}

	exists, _, err := NativeCheckBranchExists(ctx, repoPath, name)
	if err != nil {
		return ValidateBranchNameResponse{}, typed.NewHTTPError(http.StatusUnprocessableEntity, "repository corrupted")
	}

	if !exists {
		return ValidateBranchNameResponse{
			Success:     true,
			IsAvailable: true,
		}, nil
	}

	return ValidateBranchNameResponse{
		Success:       true,
		IsAvailable:   false,
		SuggestedName: suggestAvailableName(ctx, repoPath, name),
	}, nil
}

// HandleGetBranches handles GET /branches.
// Supports optional ?search=<keyword> to filter branches by name (case-insensitive substring match).
//
// query string 은 typed.RegisterGetWithQuery wrapper 가 GetBranchesRequest 로
// bind 한 뒤 본 함수에 전달한다 — wrapper 가 spec generator 에 query parameter
// 메타데이터를 자동 노출하므로 orchestrator auto-gen client 가 limit/skip/
// search/sort/email/status 인자를 누락하지 않는다 (Issue-2317 후속).
func (h *BranchOperationsHandler) HandleGetBranches(c *gin.Context, req GetBranchesRequest) (GetBranchesResponse, error) {
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")

	limitStr := req.Limit
	skipStr := req.Skip
	searchQuery := req.Search
	sortParam := req.Sort
	if sortParam == "" {
		sortParam = "newest"
	}
	sort := ParseBranchSort(sortParam)
	emailFilter := req.Email
	statusFilter := req.Status

	if statusFilter != "" && statusFilter != "active" && statusFilter != "stale" {
		return GetBranchesResponse{}, typed.NewHTTPError(http.StatusBadRequest, "invalid status: must be 'active' or 'stale'")
	}

	limit := DefaultBranchesLimit
	if limitStr != "" {
		parsedLimit, err := strconv.Atoi(limitStr)
		if err != nil || parsedLimit < MinBranchesLimit {
			return GetBranchesResponse{}, typed.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("invalid limit: must be >= %d", MinBranchesLimit))
		}
		if parsedLimit > MaxBranchesLimit {
			limit = MaxBranchesLimit
		} else {
			limit = parsedLimit
		}
	}

	skip := 0
	if skipStr != "" {
		parsedSkip, err := strconv.Atoi(skipStr)
		if err != nil || parsedSkip < 0 {
			return GetBranchesResponse{}, typed.NewHTTPError(http.StatusBadRequest, "invalid skip: must be >= 0")
		}
		skip = parsedSkip
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return GetBranchesResponse{}, typed.NewHTTPError(http.StatusNotFound, "repository not found")
	}

	allBranches, err := h.service.GetBranches(ctx, repoPath, sort)
	if err != nil {
		return GetBranchesResponse{}, typed.NewHTTPError(http.StatusUnprocessableEntity, "repository corrupted")
	}

	if allBranches == nil {
		allBranches = []BranchInfo{}
	}

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

	var branches []BranchInfo
	if skip >= totalCount {
		branches = []BranchInfo{}
	} else {
		end := skip + limit
		if end > totalCount {
			end = totalCount
		}
		branches = allBranches[skip:end]
	}

	currentPage := 1
	if limit > 0 {
		currentPage = skip/limit + 1
	}

	pagination := buildPagination(totalCount, currentPage, limit, c.Request.URL.String())

	return GetBranchesResponse{
		Success: true,
		Results: branches,
		Pagination: PaginationInfo{
			Count:       totalCount,
			Next:        pagination.Next,
			Previous:    pagination.Previous,
			CurrentPage: currentPage,
			TotalPages:  pagination.TotalPages,
			PageSize:    limit,
		},
	}, nil
}

// HandleBranchStats handles POST /branches/stats — 요청된 브랜치들의 ahead/behind 를
// default branch 기준으로 배치 계산.
func (h *BranchOperationsHandler) HandleBranchStats(c *gin.Context) (BranchStatsResponse, error) {
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")

	var req BranchStatsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return BranchStatsResponse{}, typed.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("invalid request: %v", err))
	}

	if len(req.Branches) > MaxBranchesLimit {
		return BranchStatsResponse{}, typed.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("too many branches: max %d", MaxBranchesLimit))
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return BranchStatsResponse{}, typed.NewHTTPError(http.StatusNotFound, "repository not found")
	}

	headRef := nativeGetHead(ctx, repoPath)
	defaultBranch := strings.TrimPrefix(headRef, "refs/heads/")
	if defaultBranch == "" {
		defaultBranch = "main"
	}

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

	return BranchStatsResponse{
		Success: true,
		Stats:   stats,
	}, nil
}

// buildPagination은 페이지네이션 정보를 생성합니다.
func buildPagination(totalCount, currentPage, pageSize int, baseURL string) PaginationInfo {
	totalPages := (totalCount + pageSize - 1) / pageSize
	if totalPages == 0 {
		totalPages = 1
	}

	var next, previous *string

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

// HandleResolveBranch handles GET /branches/resolve/*ref — single branch → commit SHA.
func (h *BranchOperationsHandler) HandleResolveBranch(c *gin.Context) (ResolveBranchResponse, error) {
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")
	ref := strings.TrimPrefix(protocol.SafeParam(c, "ref"), "/")
	if ref == "" {
		return ResolveBranchResponse{}, typed.NewHTTPError(http.StatusBadRequest, "branch name is required")
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return ResolveBranchResponse{}, typed.NewHTTPError(http.StatusNotFound, "repository not found")
	}
	if !repoExists(repoPath) {
		return ResolveBranchResponse{}, typed.NewHTTPError(http.StatusNotFound, "repository not found")
	}

	exists, hash, err := NativeCheckBranchExists(ctx, repoPath, ref)
	if err != nil {
		return ResolveBranchResponse{}, typed.NewHTTPError(http.StatusUnprocessableEntity, "repository corrupted")
	}
	if !exists {
		return ResolveBranchResponse{}, typed.NewHTTPError(http.StatusNotFound, fmt.Sprintf("branch '%s' not found", ref))
	}

	return ResolveBranchResponse{
		Success:   true,
		Name:      ref,
		CommitSHA: hash,
		RefPath:   "refs/heads/" + ref,
	}, nil
}

// HandleValidateReopen handles POST /branches/validate-reopen.
// CR reopen 전 source/target 브랜치 존재 여부와 lineage(계보) 검증을 한 번에 수행합니다.
func (h *BranchOperationsHandler) HandleValidateReopen(c *gin.Context) (ValidateReopenResponse, error) {
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")

	var req ValidateReopenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return ValidateReopenResponse{}, typed.NewHTTPError(http.StatusBadRequest, "invalid request body: "+err.Error())
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return ValidateReopenResponse{}, typed.NewHTTPError(http.StatusNotFound, "repository not found")
	}

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
		return ValidateReopenResponse{}, typed.NewHTTPError(http.StatusUnprocessableEntity, "failed to check source branch")
	}
	if !sourceRes.exists {
		return ValidateReopenResponse{}, typed.NewHTTPError(http.StatusConflict, fmt.Sprintf("source branch '%s' no longer exists", req.SourceBranchName))
	}
	if targetRes.err != nil {
		return ValidateReopenResponse{}, typed.NewHTTPError(http.StatusUnprocessableEntity, "failed to check target branch")
	}
	if !targetRes.exists {
		return ValidateReopenResponse{}, typed.NewHTTPError(http.StatusConflict, fmt.Sprintf("target branch '%s' no longer exists", req.TargetBranchName))
	}

	currentSourceTip := sourceRes.hash
	currentTargetTip := targetRes.hash

	lineageValid := true
	if req.StoredSourceCommitSHA != "" && currentSourceTip != req.StoredSourceCommitSHA {
		mergeBase, err := NativeMergeBase(ctx, repoPath, req.StoredSourceCommitSHA, currentSourceTip)
		if err != nil || mergeBase != req.StoredSourceCommitSHA {
			lineageValid = false
		}
	}

	if !lineageValid {
		return ValidateReopenResponse{}, typed.NewHTTPError(http.StatusConflict, "source branch has been recreated from a different merge base")
	}

	return ValidateReopenResponse{
		Success:         true,
		SourceCommitSHA: currentSourceTip,
		TargetCommitSHA: currentTargetTip,
		LineageValid:    true,
	}, nil
}

// HandleCreateBranch handles POST /branches.
func (h *BranchOperationsHandler) HandleCreateBranch(c *gin.Context) (CreateBranchResponse, error) {
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")

	h.logger.LogInfo(c.Request.Context(), "Processing branch creation request",
		zap.String("event", "branch_create_request"),
		zap.String("owner", owner),
		zap.String("repo", repo))

	var req CreateBranchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return CreateBranchResponse{}, typed.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Invalid JSON: %v", err))
	}

	if err := h.validateBranchName(req.Name); err != nil {
		return CreateBranchResponse{}, typed.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Invalid branch name: %v", err))
	}

	repoCtx, err := h.validateAndGetRepoPath(ctx, owner, repo)
	if err != nil {
		return CreateBranchResponse{}, err
	}

	result, err := h.service.CreateBranch(c.Request.Context(), repoCtx.repoPath, req)
	if err != nil {
		statusCode := http.StatusInternalServerError
		if strings.Contains(err.Error(), "already exists") {
			statusCode = http.StatusConflict
		} else if strings.Contains(err.Error(), "failed to resolve") {
			statusCode = http.StatusBadRequest
		}
		return CreateBranchResponse{}, typed.NewHTTPError(statusCode, err.Error())
	}

	return CreateBranchResponse{
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
	}, nil
}

// HandleUpdateBranch handles PATCH /branches/*branch — 브랜치 포인터 업데이트
// 또는 rename 작업.
func (h *BranchOperationsHandler) HandleUpdateBranch(c *gin.Context) (UpdateBranchResponse, error) {
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")

	branchName := strings.TrimPrefix(protocol.SafeParam(c, "branch"), "/")

	h.logger.LogInfo(c.Request.Context(), "Processing branch update request",
		zap.String("event", "branch_update_request"),
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.String("branch", branchName))

	var req UpdateBranchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return UpdateBranchResponse{}, typed.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Invalid JSON: %v", err))
	}

	hasTarget := req.Target != ""
	hasNewName := req.NewName != ""
	if !hasTarget && !hasNewName {
		return UpdateBranchResponse{}, typed.NewHTTPError(http.StatusBadRequest, "Either 'target' (for commit update) or 'new_name' (for rename) must be provided")
	}
	if hasTarget && hasNewName {
		return UpdateBranchResponse{}, typed.NewHTTPError(http.StatusBadRequest, "Cannot provide both 'target' and 'new_name'. Use 'target' for commit update or 'new_name' for rename")
	}

	if err := h.validateBranchName(branchName); err != nil {
		return UpdateBranchResponse{}, typed.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Invalid branch name: %v", err))
	}

	if hasNewName {
		if err := h.validateBranchName(req.NewName); err != nil {
			return UpdateBranchResponse{}, typed.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Invalid new branch name: %v", err))
		}
	}

	repoCtx, err := h.validateAndGetRepoPath(ctx, owner, repo)
	if err != nil {
		return UpdateBranchResponse{}, err
	}

	result, err := h.service.UpdateBranch(c.Request.Context(), repoCtx.repoPath, branchName, req)
	if err != nil {
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
		return UpdateBranchResponse{}, typed.NewHTTPError(statusCode, err.Error())
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

	resp := UpdateBranchResponse{
		Success:      true,
		BranchName:   result.BranchName,
		CommitHash:   result.CommitHash,
		PreviousHash: result.PreviousHash,
		Operation:    result.Operation,
		FastForward:  result.FastForward,
	}

	// "forced" 는 commit update 에서 prev != current 일 때만 노출 (legacy 호환).
	if result.PreviousHash != result.CommitHash {
		forced := result.Forced
		resp.Forced = &forced
	}

	// rename-specific 필드.
	if result.Operation == "rename" {
		resp.OldBranchName = result.OldBranchName
		resp.NewBranchName = result.NewBranchName
		resp.RefPath = result.RefPath
		headUpdated := result.HeadUpdated
		resp.HeadUpdated = &headUpdated
	}

	if result.Message != "" {
		resp.Message = result.Message
	}

	return resp, nil
}

// HandleDeleteBranch handles DELETE /branches/*branch.
func (h *BranchOperationsHandler) HandleDeleteBranch(c *gin.Context) (DeleteBranchResponse, error) {
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")

	branchName := strings.TrimPrefix(protocol.SafeParam(c, "branch"), "/")

	h.logger.LogInfo(c.Request.Context(), "Processing branch deletion request",
		zap.String("event", "branch_delete_request"),
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.String("branch", branchName),
		zap.String("raw_branch_param", c.Param("branch")),
		zap.String("request_path", c.Request.URL.Path))

	if err := h.validateBranchName(branchName); err != nil {
		return DeleteBranchResponse{}, typed.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Invalid branch name: %v", err))
	}

	repoCtx, err := h.validateAndGetRepoPath(ctx, owner, repo)
	if err != nil {
		return DeleteBranchResponse{}, err
	}

	result, err := h.service.DeleteBranch(c.Request.Context(), repoCtx.repoPath, branchName)
	if err != nil {
		statusCode := http.StatusInternalServerError
		if strings.Contains(err.Error(), "does not exist") {
			statusCode = http.StatusNotFound
		}
		return DeleteBranchResponse{}, typed.NewHTTPError(statusCode, err.Error())
	}

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

	return DeleteBranchResponse{
		Success:    true,
		BranchName: result.BranchName,
		CommitHash: result.CommitHash,
		Operation:  "delete",
	}, nil
}

// HandleBulkDeleteBranches handles POST /branches/bulk-delete.
func (h *BranchOperationsHandler) HandleBulkDeleteBranches(c *gin.Context) (BulkDeleteBranchesResponse, error) {
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")

	var req BulkDeleteBranchesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return BulkDeleteBranchesResponse{}, typed.NewHTTPError(http.StatusBadRequest, "invalid request body: branches array is required")
	}

	repoCtx, err := h.validateAndGetRepoPath(ctx, owner, repo)
	if err != nil {
		return BulkDeleteBranchesResponse{}, err
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

	return BulkDeleteBranchesResponse{
		Success:   failed == 0,
		Total:     len(req.Branches),
		Succeeded: succeeded,
		Failed:    failed,
		Results:   results,
	}, nil
}
