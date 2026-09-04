// Package branches 는 Git REST API 엔드포인트의 브랜치 작업을 제공합니다.
// 브랜치 생성, 업데이트, 삭제 등의 Git 브랜치 관리 기능을 구현합니다.
package branches

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/api"
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
		eventsClient = events.NewLazyRedisStreamClient(cfg.Events, logger.Logger)
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
		return nil, api.NewCoded(api.ErrCodeInvalidInput, fmt.Sprintf("Invalid path: %v", err))
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return nil, api.NewCoded(api.ErrCodeRepositoryNotFound)
	}

	// 경로 안전성 검증과 별개로 저장소 실제 존재 여부를 확인한다.
	// GetAndValidateRepoPath 는 path traversal 등만 막고 존재는 검사하지 않음.
	if !repoExists(repoPath) {
		return nil, api.NewCoded(api.ErrCodeRepositoryNotFound)
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
		return ValidateBranchNameResponse{}, api.NewCoded(api.ErrCodeRepositoryNotFound)
	}
	if !repoExists(repoPath) {
		return ValidateBranchNameResponse{}, api.NewCoded(api.ErrCodeRepositoryNotFound)
	}

	exists, _, err := NativeCheckBranchExists(ctx, repoPath, name)
	if err != nil {
		return ValidateBranchNameResponse{}, api.NewCoded(api.ErrCodeRepositoryCorrupted)
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

// branchListHTTPError maps a GetBranches failure to a coded error: a deadline
// (listing too slow under load — the repo is fine) → TIMEOUT(429); a repo that
// vanished on disk → REPOSITORY_NOT_FOUND(404); anything else (repo present but
// git failed unexpectedly) → REPOSITORY_CORRUPTED(500). No 422 (Issue-5558/5577:
// missing/corrupted are not client errors).
//
// Why 429 and NOT 5xx for the timeout: the orchestrator BFF auto-retries idempotent GETs on
// >=500 (stop_after_attempt(3), base.py) — a 5xx here would re-run the same
// expensive 5s for-each-ref up to 3× on an already-contended git-executor,
// amplifying the very overload that caused the timeout (cf. INC-002). 429 is a
// transient/back-off signal the BFF does NOT auto-retry (its predicate only
// retries >=500 + connect errors), so it degrades honestly without a retry
// storm; the web client backs off at user pace.
func branchListHTTPError(err error) *typed.HTTPError {
	if errors.Is(err, context.DeadlineExceeded) {
		return api.NewCoded(api.ErrCodeTimeout, "branch listing timed out, retry shortly")
	}
	// The repo vanished on disk between the existence gate and for-each-ref (a
	// concurrent delete), so git reports "not a git repository". That is a
	// missing resource, not corruption — 404, not the 422 reserved for a repo
	// that is present but genuinely damaged (Issue-5558). Neither is auto-retried
	// by the BFF.
	if isRepoMissingErr(err) {
		return api.NewCoded(api.ErrCodeRepositoryNotFound)
	}
	return api.NewCoded(api.ErrCodeRepositoryCorrupted)
}

// isRepoMissingErr reports whether a git command error means the on-disk
// repository is absent / not a valid git dir (as opposed to a present-but-broken
// repo or an unrelated failure). git prints "not a git repository" (exit 128)
// when the --git-dir path is missing or lacks the basic HEAD/objects/refs layout.
func isRepoMissingErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not a git repository") ||
		strings.Contains(msg, "repository does not exist") ||
		strings.Contains(msg, "does not contain a valid git repository")
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
		return GetBranchesResponse{}, api.NewCoded(api.ErrCodeInvalidInput, "invalid status: must be 'active' or 'stale'")
	}

	limit := DefaultBranchesLimit
	if limitStr != "" {
		parsedLimit, err := strconv.Atoi(limitStr)
		if err != nil || parsedLimit < MinBranchesLimit {
			return GetBranchesResponse{}, api.NewCoded(api.ErrCodeInvalidInput, fmt.Sprintf("invalid limit: must be >= %d", MinBranchesLimit))
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
			return GetBranchesResponse{}, api.NewCoded(api.ErrCodeInvalidInput, "invalid skip: must be >= 0")
		}
		skip = parsedSkip
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return GetBranchesResponse{}, api.NewCoded(api.ErrCodeRepositoryNotFound)
	}

	// GetAndValidateRepoPath only validates the path format, not that the repo
	// exists on disk. Gate on existence here — as the sibling ValidateBranchName
	// / ResolveBranch handlers already do — so a missing/never-materialized repo
	// (orphan DB record with no on-disk git dir) returns a clean 404 instead of
	// falling through to for-each-ref, which fails "not a git repository" and was
	// mislabelled 422 "repository corrupted" with an ERROR+stacktrace log per poll
	// (Issue-5558). 404 is honest (the git resource is absent) and, like 429, is
	// not auto-retried by the BFF.
	if !repoExists(repoPath) {
		return GetBranchesResponse{}, api.NewCoded(api.ErrCodeRepositoryNotFound)
	}

	// Cost optimization (Issue-5071): committer email is the only for-each-ref
	// field that forces reading every branch tip's commit object. With no email
	// filter we scan cheaply without it, paginate, then enrich only the returned
	// page — O(page) object reads instead of O(all branches). An email filter
	// needs the email for every branch, so it falls back to the full scan.
	lightScan := emailFilter == ""
	// committerdate is only needed up-front when we sort by it or filter by it
	// (status = active/stale is a date threshold). For name sort with no status
	// filter, omit it from the wide scan and enrich the page instead — the same
	// deferral the email path uses (Issue-5618).
	includeDate := sort != BranchSortName || statusFilter != ""
	var allBranches []BranchInfo
	if lightScan {
		allBranches, err = h.service.GetBranchesLight(ctx, repoPath, sort, includeDate)
	} else {
		allBranches, err = h.service.GetBranches(ctx, repoPath, sort)
	}
	if err != nil {
		// A deadline hit means the listing was too slow under load (storage
		// contention on a busy repo), NOT that the repo is corrupted. Log it
		// and return 429 (transient, NOT auto-retried by the BFF — avoids a
		// retry storm on the contended backend) instead of the blanket 422
		// "repository corrupted" that mislabels healthy repos and hides the real
		// cause (Issue-5071). Genuine git failures keep the 422 path — now logged.
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			h.logger.LogWarning(ctx, "branch listing timed out",
				zap.String("repo", repoPath),
				zap.String("sort", string(sort)),
				zap.Error(err))
		case isRepoMissingErr(err):
			// Repo deleted mid-request (raced past the existence gate). An
			// expected data-hygiene condition, not a server fault — WARN without a
			// stacktrace so it does not spam ERROR logs per poll (Issue-5558).
			h.logger.LogWarning(ctx, "branch listing: repository missing on disk",
				zap.String("repo", repoPath))
		default:
			// Repo is present but git failed — genuine corruption. Keep ERROR.
			h.logger.LogError(ctx, err, "branch listing failed",
				zap.String("repo", repoPath))
		}
		return GetBranchesResponse{}, branchListHTTPError(err)
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

	// Light scan omitted committer emails; fill them in for just the page
	// (O(page) object reads). Enrichment failure is non-fatal — return the
	// branches with empty emails rather than failing the whole list.
	if lightScan && len(branches) > 0 {
		if eerr := h.service.EnrichCommitterEmails(ctx, repoPath, branches); eerr != nil {
			h.logger.LogWarning(ctx, "committer email enrichment failed; returning branches without emails",
				zap.String("repo", repoPath),
				zap.Error(eerr))
		}
		// Wide scan omitted committerdate (name sort, no date filter); fill it in
		// for just the page (O(page)). Non-fatal — a zero Date on failure.
		if !includeDate {
			if derr := h.service.EnrichCommitterDates(ctx, repoPath, branches); derr != nil {
				h.logger.LogWarning(ctx, "committer date enrichment failed; returning branches without dates",
					zap.String("repo", repoPath),
					zap.Error(derr))
			}
		}
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
		return BranchStatsResponse{}, api.NewCoded(api.ErrCodeInvalidInput, fmt.Sprintf("invalid request: %v", err))
	}

	if len(req.Branches) > MaxBranchesLimit {
		return BranchStatsResponse{}, api.NewCoded(api.ErrCodeInvalidInput, fmt.Sprintf("too many branches: max %d", MaxBranchesLimit))
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return BranchStatsResponse{}, api.NewCoded(api.ErrCodeRepositoryNotFound)
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
		return ResolveBranchResponse{}, api.NewCoded(api.ErrCodeInvalidInput, "branch name is required")
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return ResolveBranchResponse{}, api.NewCoded(api.ErrCodeRepositoryNotFound)
	}
	if !repoExists(repoPath) {
		return ResolveBranchResponse{}, api.NewCoded(api.ErrCodeRepositoryNotFound)
	}

	exists, hash, err := NativeCheckBranchExists(ctx, repoPath, ref)
	if err != nil {
		return ResolveBranchResponse{}, api.NewCoded(api.ErrCodeRepositoryCorrupted)
	}
	if !exists {
		return ResolveBranchResponse{}, api.NewCodedWithParams(api.ErrCodeRefNotFound,
			fmt.Sprintf("branch '%s' not found", ref), map[string]string{"ref": ref})
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
		return ValidateReopenResponse{}, api.NewCoded(api.ErrCodeInvalidInput, "invalid request body: "+err.Error())
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return ValidateReopenResponse{}, api.NewCoded(api.ErrCodeRepositoryNotFound)
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
		return ValidateReopenResponse{}, api.NewCoded(api.ErrCodeInternalError, "failed to check source branch")
	}
	if !sourceRes.exists {
		return ValidateReopenResponse{}, api.NewCoded(api.ErrCodeConflict, fmt.Sprintf("source branch '%s' no longer exists", req.SourceBranchName))
	}
	if targetRes.err != nil {
		return ValidateReopenResponse{}, api.NewCoded(api.ErrCodeInternalError, "failed to check target branch")
	}
	if !targetRes.exists {
		return ValidateReopenResponse{}, api.NewCoded(api.ErrCodeConflict, fmt.Sprintf("target branch '%s' no longer exists", req.TargetBranchName))
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
		return ValidateReopenResponse{}, api.NewCoded(api.ErrCodeConflict, "source branch has been recreated from a different merge base")
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
		return CreateBranchResponse{}, api.NewCoded(api.ErrCodeInvalidInput, fmt.Sprintf("Invalid JSON: %v", err))
	}

	if err := h.validateBranchName(req.Name); err != nil {
		return CreateBranchResponse{}, api.NewCoded(api.ErrCodeInvalidInput, fmt.Sprintf("Invalid branch name: %v", err))
	}

	repoCtx, err := h.validateAndGetRepoPath(ctx, owner, repo)
	if err != nil {
		return CreateBranchResponse{}, err
	}

	result, err := h.service.CreateBranch(c.Request.Context(), repoCtx.repoPath, req)
	if err != nil {
		code := api.ErrCodeInternalError
		if strings.Contains(err.Error(), "already exists") {
			code = api.ErrCodeBranchAlreadyExists
		} else if strings.Contains(err.Error(), "failed to resolve") {
			code = api.ErrCodeInvalidInput
		}
		return CreateBranchResponse{}, api.NewCoded(code, err.Error())
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
		return UpdateBranchResponse{}, api.NewCoded(api.ErrCodeInvalidInput, fmt.Sprintf("Invalid JSON: %v", err))
	}

	hasTarget := req.Target != ""
	hasNewName := req.NewName != ""
	if !hasTarget && !hasNewName {
		return UpdateBranchResponse{}, api.NewCoded(api.ErrCodeInvalidInput, "Either 'target' (for commit update) or 'new_name' (for rename) must be provided")
	}
	if hasTarget && hasNewName {
		return UpdateBranchResponse{}, api.NewCoded(api.ErrCodeInvalidInput, "Cannot provide both 'target' and 'new_name'. Use 'target' for commit update or 'new_name' for rename")
	}

	if err := h.validateBranchName(branchName); err != nil {
		return UpdateBranchResponse{}, api.NewCoded(api.ErrCodeInvalidInput, fmt.Sprintf("Invalid branch name: %v", err))
	}

	if hasNewName {
		if err := h.validateBranchName(req.NewName); err != nil {
			return UpdateBranchResponse{}, api.NewCoded(api.ErrCodeInvalidInput, fmt.Sprintf("Invalid new branch name: %v", err))
		}
	}

	repoCtx, err := h.validateAndGetRepoPath(ctx, owner, repo)
	if err != nil {
		return UpdateBranchResponse{}, err
	}

	result, err := h.service.UpdateBranch(c.Request.Context(), repoCtx.repoPath, branchName, req)
	if err != nil {
		code := api.ErrCodeInternalError
		if strings.Contains(err.Error(), "does not exist") {
			code = api.ErrCodeRefNotFound
		} else if strings.Contains(err.Error(), "cannot be the same") {
			code = api.ErrCodeInvalidInput
		} else if strings.Contains(err.Error(), "already exists") {
			code = api.ErrCodeBranchAlreadyExists
		} else if strings.Contains(err.Error(), "not a fast-forward") || strings.Contains(err.Error(), "must be provided") {
			code = api.ErrCodeInvalidInput
		}
		return UpdateBranchResponse{}, api.NewCoded(code, err.Error())
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
		return DeleteBranchResponse{}, api.NewCoded(api.ErrCodeInvalidInput, fmt.Sprintf("Invalid branch name: %v", err))
	}

	repoCtx, err := h.validateAndGetRepoPath(ctx, owner, repo)
	if err != nil {
		return DeleteBranchResponse{}, err
	}

	result, err := h.service.DeleteBranch(c.Request.Context(), repoCtx.repoPath, branchName)
	if err != nil {
		code := api.ErrCodeInternalError
		if strings.Contains(err.Error(), "does not exist") {
			code = api.ErrCodeRefNotFound
		}
		return DeleteBranchResponse{}, api.NewCoded(code, err.Error())
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
		return BulkDeleteBranchesResponse{}, api.NewCoded(api.ErrCodeInvalidInput, "invalid request body: branches array is required")
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
