package commits

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// CommitsHandler는 Git 커밋 로그 REST API 엔드포인트를 처리합니다.
type CommitsHandler struct {
	config        *config.Config
	pathConfig    *repositories.PathConfig
	logger        *logging.Logger
	pathValidator *repositories.GitAPIPathValidator
	service       *CommitsService
}

// NewCommitsHandler는 새로운 commits 핸들러를 생성합니다.
func NewCommitsHandler(cfg *config.Config) (*CommitsHandler, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	pathValidator := repositories.NewGitAPIPathValidator(pathConfig)

	transactionManager := NewTransactionManager(logger)

	var eventsClient events.RedisStreamClient
	if cfg.Events.Enabled {
		eventsClient, err = events.NewRedisStreamClientFromConfig(cfg.Events, logger.Logger)
		if err != nil {
			logger.Warn("Failed to initialize Redis events client, continuing without events",
				zap.Error(err))
		}
	}

	service := NewCommitsService(logger, transactionManager, eventsClient)

	return &CommitsHandler{
		config:        cfg,
		pathConfig:    pathConfig,
		logger:        logger,
		pathValidator: pathValidator,
		service:       service,
	}, nil
}

// repositoryContext는 저장소 관련 공통 컨텍스트를 담는 구조체입니다.
type repositoryContext struct {
	owner    string
	repo     string
	ref      string
	repoPath string
}

// validateAndGetRepoPath 는 저장소 검증 + 경로 조회를 수행한다. 실패 시
// *typed.HTTPError 를 반환하여 wrapper 의 default 매핑 (404 + {success,error})
// 으로 처리.
func (h *CommitsHandler) validateAndGetRepoPath(owner, repo, ref string) (*repositoryContext, error) {
	if ref == "" {
		ref = "HEAD"
	}

	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo)); err != nil {
		return nil, api.NewCoded(api.ErrCodeRepositoryNotFound)
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return nil, api.NewCoded(api.ErrCodeRepositoryNotFound)
	}

	return &repositoryContext{
		owner:    owner,
		repo:     repo,
		ref:      ref,
		repoPath: repoPath,
	}, nil
}

// HandleCommitsAPI는 GET /repos/{owner}/{repo}/commits 요청을 처리합니다.
//
// query string 은 typed.RegisterGetWithQuery wrapper 가 CommitsListRequest
// 로 bind 한 뒤 본 함수에 전달한다 — wrapper 가 spec generator 에 query
// parameter 메타데이터를 자동 노출하므로 orchestrator auto-gen client 가
// ref/source_ref/target_ref/limit/skip/group_by 인자를 누락하지 않는다
// (Issue-2317 후속).
func (h *CommitsHandler) HandleCommitsAPI(c *gin.Context, req CommitsListRequest) (CommitsResponse, error) {
	ctx := c.Request.Context()

	params, err := h.validateAndExtractCommitsParams(c, req)
	if err != nil {
		return CommitsResponse{}, api.NewCoded(api.ErrCodeInvalidInput, err.Error())
	}

	repoCtx, err := h.validateAndGetRepoPath(params.Owner, params.Repo, params.Ref)
	if err != nil {
		return CommitsResponse{}, err
	}

	commitList, totalCount, mergeBase, err := h.getCommitHistory(ctx, repoCtx.repoPath, params)
	if err != nil {
		return CommitsResponse{}, api.NewCoded(api.ErrCodeInvalidInput, err.Error())
	}

	if commitList == nil {
		commitList = []Commit{}
	}

	hasNext := len(commitList) > params.Limit
	if hasNext {
		commitList = commitList[:params.Limit]
	}

	grouped := h.groupCommitsByDate(commitList, params.GroupBy)
	if grouped == nil {
		grouped = []CommitsByDate{}
	}

	pagination := buildPagination(totalCount, params.Skip/params.Limit+1, params.Limit, c.Request.URL.String())

	return CommitsResponse{
		Success:   true,
		Results:   grouped,
		HasNext:   hasNext,
		Message:   "Commits fetched successfully",
		MergeBase: mergeBase,
		Pagination: PaginationInfo{
			Count:      totalCount,
			Next:       pagination.Next,
			Previous:   pagination.Previous,
			TotalPages: pagination.TotalPages,
			PageSize:   params.Limit,
		},
	}, nil
}

// HandleCommitActivity는 GET /repos/{owner}/{repo}/commits/activity 요청을 처리합니다.
//
// 최근 N일(default 30) 일자별 커밋 수 히스토그램을 반환한다 — repo-list
// sparkline 용 경량 API. full commit 메타를 내리지 않고 --since 윈도우로 커밋
// 날짜만 집계하므로 비용이 O(commits-in-window) 이다 (Issue-5247).
func (h *CommitsHandler) HandleCommitActivity(c *gin.Context, req CommitActivityRequest) (CommitActivityResponse, error) {
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")

	ref := strings.TrimSpace(req.Ref)
	if ref == "" {
		ref = "HEAD"
	}

	days, err := parseActivityDays(req.Days)
	if err != nil {
		return CommitActivityResponse{}, api.NewCoded(api.ErrCodeInvalidInput, err.Error())
	}

	if req.GroupBy != "" && req.GroupBy != "committer_date" {
		return CommitActivityResponse{}, api.NewCoded(api.ErrCodeInvalidInput,
			"invalid group_by parameter: must be empty or 'committer_date'")
	}
	useCommitterDate := req.GroupBy == "committer_date"

	repoCtx, err := h.validateAndGetRepoPath(owner, repo, ref)
	if err != nil {
		return CommitActivityResponse{}, err
	}

	dates, err := nativeCommitActivityDates(ctx, repoCtx.repoPath, ref, days, useCommitterDate)
	if err != nil {
		return CommitActivityResponse{}, api.NewCoded(api.ErrCodeInvalidInput, err.Error())
	}

	return buildActivityResponse(ref, days, dates), nil
}

// parseActivityDays 는 days query 파라미터를 파싱/검증한다. 빈 값이면 default.
func parseActivityDays(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultActivityDays, nil
	}
	days, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid days parameter: must be an integer")
	}
	if days < MinActivityDays || days > MaxActivityDays {
		return 0, fmt.Errorf("invalid days parameter: must be between %d and %d", MinActivityDays, MaxActivityDays)
	}
	return days, nil
}

// buildActivityResponse 는 커밋 날짜 목록을 [since, until] 윈도우의 dense
// 일자별 히스토그램으로 조립한다 (오래된→최신, 커밋 없는 날 0, 길이 == days).
// until = 오늘(UTC), since = until - (days-1), 둘 다 inclusive.
func buildActivityResponse(ref string, days int, dates []time.Time) CommitActivityResponse {
	today := time.Now().UTC().Truncate(24 * time.Hour) // midnight UTC (epoch is midnight UTC)
	start := today.AddDate(0, 0, -(days - 1))

	counts := make(map[string]int, len(dates))
	for _, t := range dates {
		counts[t.UTC().Format("2006-01-02")]++
	}

	buckets := make([]ActivityDayCount, 0, days)
	total := 0
	for i := 0; i < days; i++ {
		key := start.AddDate(0, 0, i).Format("2006-01-02")
		cnt := counts[key]
		total += cnt
		buckets = append(buckets, ActivityDayCount{Date: key, Count: cnt})
	}

	return CommitActivityResponse{
		Success: true,
		Ref:     ref,
		Days:    days,
		Since:   start.Format("2006-01-02"),
		Until:   today.Format("2006-01-02"),
		Total:   total,
		Buckets: buckets,
	}
}

// HandleComparisonSummary는 GET /repos/{owner}/{repo}/commits/compare/summary 요청을 처리합니다.
// merge base 기준으로 commit count와 changed file count만 반환하는 경량 API입니다.
//
// query string 은 typed.RegisterGetWithQuery wrapper 가 ComparisonSummaryRequest
// 로 bind 한 뒤 본 함수에 전달한다 — wrapper 가 spec generator 에 query
// parameter 메타데이터를 자동 노출하므로 orchestrator auto-gen client 가
// source_ref/target_ref 인자를 누락하지 않는다 (Issue-2317).
func (h *CommitsHandler) HandleComparisonSummary(c *gin.Context, req ComparisonSummaryRequest) (ComparisonSummaryResponse, error) {
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")
	sourceRef := req.SourceRef
	targetRef := req.TargetRef

	repoCtx, err := h.validateAndGetRepoPath(owner, repo, "HEAD")
	if err != nil {
		return ComparisonSummaryResponse{}, err
	}

	sourceHash, err := nativeResolveRef(ctx, repoCtx.repoPath, sourceRef)
	if err != nil {
		sourceHash, err = nativeResolveRef(ctx, repoCtx.repoPath, "refs/heads/"+sourceRef)
		if err != nil {
			return ComparisonSummaryResponse{}, api.NewCoded(api.ErrCodeInvalidInput, fmt.Sprintf("failed to resolve source ref '%s'", sourceRef))
		}
	}
	targetHash, err := nativeResolveRef(ctx, repoCtx.repoPath, targetRef)
	if err != nil {
		targetHash, err = nativeResolveRef(ctx, repoCtx.repoPath, "refs/heads/"+targetRef)
		if err != nil {
			return ComparisonSummaryResponse{}, api.NewCoded(api.ErrCodeInvalidInput, fmt.Sprintf("failed to resolve target ref '%s'", targetRef))
		}
	}

	mergeBase, err := nativeMergeBase(ctx, repoCtx.repoPath, sourceHash, targetHash)
	if err != nil {
		return ComparisonSummaryResponse{}, api.NewCoded(api.ErrCodeInvalidInput, "no common ancestor found between the two refs")
	}

	type countResult struct {
		count int
		err   error
	}
	commitCh := make(chan countResult, 1)
	fileCh := make(chan countResult, 1)

	go func() {
		count, err := nativeRevListCountRange(ctx, repoCtx.repoPath, sourceHash, mergeBase)
		commitCh <- countResult{count, err}
	}()
	go func() {
		count, err := nativeDiffNameOnlyCount(ctx, repoCtx.repoPath, mergeBase, sourceHash)
		fileCh <- countResult{count, err}
	}()

	commitResult := <-commitCh
	fileResult := <-fileCh

	if commitResult.err != nil {
		return ComparisonSummaryResponse{}, api.NewCoded(api.ClassifyGitError(commitResult.err), commitResult.err.Error())
	}
	if fileResult.err != nil {
		return ComparisonSummaryResponse{}, api.NewCoded(api.ClassifyGitError(fileResult.err), fileResult.err.Error())
	}

	return ComparisonSummaryResponse{
		Success:          true,
		MergeBase:        mergeBase,
		SourceSha:        sourceHash,
		TargetSha:        targetHash,
		CommitCount:      commitResult.count,
		ChangedFileCount: fileResult.count,
	}, nil
}

// HandleGetCommitDetail은 GET /repos/{owner}/{repo}/commits/{sha} 요청을 처리합니다.
// 단일 커밋의 메타데이터(메시지, 작성자, 커미터, 생성일 등)를 반환합니다.
func (h *CommitsHandler) HandleGetCommitDetail(c *gin.Context) (CommitDetailResponse, error) {
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")
	sha := c.Param("sha")

	if sha == "" {
		return CommitDetailResponse{}, api.NewCoded(api.ErrCodeInvalidInput, "commit sha is required")
	}

	repoCtx, err := h.validateAndGetRepoPath(owner, repo, "HEAD")
	if err != nil {
		return CommitDetailResponse{}, err
	}

	commit, err := nativeGetCommitInfo(ctx, repoCtx.repoPath, sha)
	if err != nil {
		if strings.Contains(err.Error(), "commit not found") {
			return CommitDetailResponse{}, api.NewCoded(api.ErrCodeCommitNotFound, fmt.Sprintf("commit not found: %s", sha))
		}
		return CommitDetailResponse{}, api.NewCoded(api.ClassifyGitError(err), err.Error())
	}

	fullMessage, err := nativeGetCommitFullMessage(ctx, repoCtx.repoPath, sha)
	if err != nil {
		fullMessage = commit.Message
	}

	parents := commit.ParentHashes
	if parents == nil {
		parents = []string{}
	}

	return CommitDetailResponse{
		Success: true,
		Sha:     commit.Sha,
		Message: fullMessage,
		Author: CommitAuthorDetail{
			Name:  commit.Author.Name,
			Email: commit.Author.Email,
			Date:  commit.Author.Date.UTC().Format("2006-01-02T15:04:05Z"),
		},
		Committer: CommitAuthorDetail{
			Name:  commit.Committer.Name,
			Email: commit.Committer.Email,
			Date:  commit.Committer.Date.UTC().Format("2006-01-02T15:04:05Z"),
		},
		Parents:  parents,
		TreeHash: commit.TreeHash,
	}, nil
}

// HandleMultiFileCommit는 POST /repos/{owner}/{repo}/commits 요청을 처리합니다.
//
// Typed handler — bind error 의 친화 메시지 + 다중 status 매핑 (failed to
// resolve → 400, not found → 404, already exists → 409, validation/operation
// → 400, default → 500) 을 모두 inline 으로 api.NewCoded 처리.
func (h *CommitsHandler) HandleMultiFileCommit(c *gin.Context) (MultiFileCommitResponse, error) {
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")

	var req MultiFileCommitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return MultiFileCommitResponse{}, api.NewCoded(api.ErrCodeInvalidInput, fmt.Sprintf("Invalid JSON: %v", err))
	}

	if len(req.Operations) == 0 {
		return MultiFileCommitResponse{}, api.NewCoded(api.ErrCodeInvalidInput, "At least one operation is required")
	}

	for i, op := range req.Operations {
		if err := validateMultiFileOperation(&op); err != nil {
			return MultiFileCommitResponse{}, api.NewCoded(api.ErrCodeInvalidInput, fmt.Sprintf("Invalid operation %d: %v", i, err))
		}
	}

	branch := req.Branch

	repoCtx, err := h.validateAndGetRepoPath(owner, repo, branch)
	if err != nil {
		return MultiFileCommitResponse{}, err
	}

	userID := c.GetHeader("X-USER-ID")

	h.logger.LogDebug(ctx, fmt.Sprintf("Extracting user ID from header for commit creation: user_id=%s, owner=%s, repo=%s", userID, owner, repo))

	response, err := h.service.ExecuteMultiFileCommitOrCreateBranch(ctx, repoCtx.repoPath, &req, owner, repo, userID)
	if err != nil {
		errMsg := err.Error()
		switch {
		case strings.Contains(errMsg, "failed to resolve"):
			return MultiFileCommitResponse{}, api.NewCoded(api.ErrCodeInvalidInput, errMsg)
		case strings.Contains(errMsg, "not found"):
			return MultiFileCommitResponse{}, api.NewCoded(api.ErrCodeNotFound, errMsg)
		case strings.Contains(errMsg, "already exists"):
			return MultiFileCommitResponse{}, api.NewCoded(api.ErrCodeConflict, errMsg)
		case strings.Contains(errMsg, "validation failed") || strings.Contains(errMsg, "operation"):
			return MultiFileCommitResponse{}, api.NewCoded(api.ErrCodeInvalidInput, errMsg)
		default:
			return MultiFileCommitResponse{}, api.NewCoded(api.ErrCodeInternalError, errMsg)
		}
	}

	return *response, nil
}
