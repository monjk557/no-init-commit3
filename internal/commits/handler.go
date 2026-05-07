package commits

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/collabops/git-executor/internal/api/typed"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
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
	if cfg.Events.Enabled && cfg.Events.RedisURL != "" {
		eventsClient, err = events.NewRedisStreamClient(cfg.Events.RedisURL, logger.Logger)
		if err != nil {
			_ = err
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
		return nil, typed.NewHTTPError(http.StatusNotFound, "repository not found")
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return nil, typed.NewHTTPError(http.StatusNotFound, "repository not found")
	}

	return &repositoryContext{
		owner:    owner,
		repo:     repo,
		ref:      ref,
		repoPath: repoPath,
	}, nil
}

// HandleCommitsAPI는 GET /repos/{owner}/{repo}/commits 요청을 처리합니다.
func (h *CommitsHandler) HandleCommitsAPI(c *gin.Context) (CommitsResponse, error) {
	ctx := c.Request.Context()

	params, err := h.validateAndExtractCommitsParams(c)
	if err != nil {
		return CommitsResponse{}, typed.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	repoCtx, err := h.validateAndGetRepoPath(params.Owner, params.Repo, params.Ref)
	if err != nil {
		return CommitsResponse{}, err
	}

	commitList, totalCount, mergeBase, err := h.getCommitHistory(ctx, repoCtx.repoPath, params)
	if err != nil {
		return CommitsResponse{}, typed.NewHTTPError(http.StatusBadRequest, err.Error())
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
			return ComparisonSummaryResponse{}, typed.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("failed to resolve source ref '%s'", sourceRef))
		}
	}
	targetHash, err := nativeResolveRef(ctx, repoCtx.repoPath, targetRef)
	if err != nil {
		targetHash, err = nativeResolveRef(ctx, repoCtx.repoPath, "refs/heads/"+targetRef)
		if err != nil {
			return ComparisonSummaryResponse{}, typed.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("failed to resolve target ref '%s'", targetRef))
		}
	}

	mergeBase, err := nativeMergeBase(ctx, repoCtx.repoPath, sourceHash, targetHash)
	if err != nil {
		return ComparisonSummaryResponse{}, typed.NewHTTPError(http.StatusBadRequest, "no common ancestor found between the two refs")
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
		return ComparisonSummaryResponse{}, typed.NewHTTPError(http.StatusInternalServerError, commitResult.err.Error())
	}
	if fileResult.err != nil {
		return ComparisonSummaryResponse{}, typed.NewHTTPError(http.StatusInternalServerError, fileResult.err.Error())
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
		return CommitDetailResponse{}, typed.NewHTTPError(http.StatusBadRequest, "commit sha is required")
	}

	repoCtx, err := h.validateAndGetRepoPath(owner, repo, "HEAD")
	if err != nil {
		return CommitDetailResponse{}, err
	}

	commit, err := nativeGetCommitInfo(ctx, repoCtx.repoPath, sha)
	if err != nil {
		if strings.Contains(err.Error(), "commit not found") {
			return CommitDetailResponse{}, typed.NewHTTPError(http.StatusNotFound, fmt.Sprintf("commit not found: %s", sha))
		}
		return CommitDetailResponse{}, typed.NewHTTPError(http.StatusInternalServerError, err.Error())
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
// → 400, default → 500) 을 모두 inline 으로 typed.NewHTTPError 처리.
func (h *CommitsHandler) HandleMultiFileCommit(c *gin.Context) (MultiFileCommitResponse, error) {
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")

	var req MultiFileCommitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return MultiFileCommitResponse{}, typed.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Invalid JSON: %v", err))
	}

	if len(req.Operations) == 0 {
		return MultiFileCommitResponse{}, typed.NewHTTPError(http.StatusBadRequest, "At least one operation is required")
	}

	for i, op := range req.Operations {
		if err := validateMultiFileOperation(&op); err != nil {
			return MultiFileCommitResponse{}, typed.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Invalid operation %d: %v", i, err))
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
			return MultiFileCommitResponse{}, typed.NewHTTPError(http.StatusBadRequest, errMsg)
		case strings.Contains(errMsg, "not found"):
			return MultiFileCommitResponse{}, typed.NewHTTPError(http.StatusNotFound, errMsg)
		case strings.Contains(errMsg, "already exists"):
			return MultiFileCommitResponse{}, typed.NewHTTPError(http.StatusConflict, errMsg)
		case strings.Contains(errMsg, "validation failed") || strings.Contains(errMsg, "operation"):
			return MultiFileCommitResponse{}, typed.NewHTTPError(http.StatusBadRequest, errMsg)
		default:
			return MultiFileCommitResponse{}, typed.NewHTTPError(http.StatusInternalServerError, errMsg)
		}
	}

	return *response, nil
}
