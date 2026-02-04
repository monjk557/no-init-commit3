package commits

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
)

// CommitsHandler는 Git 커밋 로그 REST API 엔드포인트를 처리합니다.
// 커밋 히스토리 조회 요청을 받아서 페이지네이션과 함께 응답합니다.
type CommitsHandler struct {
	config        *config.Config
	pathConfig    *repositories.PathConfig
	logger        *logging.Logger
	pathValidator *repositories.GitAPIPathValidator
	repoManager   *repositories.DefaultRepositoryManager
	service       *CommitsService
}

// NewCommitsHandler는 새로운 commits 핸들러를 생성합니다.
// 설정과 권한 클라이언트를 사용하여 초기화합니다.
func NewCommitsHandler(cfg *config.Config) (*CommitsHandler, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	// 경로 설정 생성
	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	pathValidator := repositories.NewGitAPIPathValidator(pathConfig)

	repoManager, err := repositories.NewRepositoryManager(pathConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create repository manager: %w", err)
	}

	transactionManager := NewTransactionManager(logger)

	// Initialize events client if enabled
	var eventsClient events.RedisStreamClient
	if cfg.Events.Enabled && cfg.Events.RedisURL != "" {
		eventsClient, err = events.NewRedisStreamClient(cfg.Events.RedisURL, logger.Logger)
		if err != nil {
			// Log warning but continue without events
			_ = err
		}
	}

	service := NewCommitsService(logger, transactionManager, eventsClient)

	return &CommitsHandler{
		config:        cfg,
		pathConfig:    pathConfig,
		logger:        logger,
		pathValidator: pathValidator,
		repoManager:   repoManager,
		service:       service,
	}, nil
}

// repositoryContext는 저장소 관련 공통 컨텍스트를 담는 구조체입니다.
type repositoryContext struct {
	owner       string
	repo        string
	ref         string
	repoPath    string
	repoWrapper *repositories.RepositoryWrapper
}

// validateAndOpenRepository는 저장소 검증 및 열기를 수행하는 공통 로직입니다.
func (h *CommitsHandler) validateAndOpenRepository(ctx context.Context, c *gin.Context, owner, repo, ref string) (*repositoryContext, bool) {
	// ref가 제공되지 않으면 기본값으로 HEAD 사용
	if ref == "" {
		ref = "HEAD"
	}

	// 경로 구성 요소 검증
	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo)); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return nil, false
	}

	// 저장소 경로 가져오기
	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return nil, false
	}

	// 저장소 열기 (존재 여부는 OpenRepository에서 확인)
	repoWrapper, err := h.repoManager.OpenRepository(repoPath)
	if err != nil {
		// repository not found 또는 corrupted
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return nil, false
	}

	return &repositoryContext{
		owner:       owner,
		repo:        repo,
		ref:         ref,
		repoPath:    repoPath,
		repoWrapper: repoWrapper,
	}, true
}

// HandleCommitsAPI는 GET /repos/:owner/:repo/commits?ref=:ref&limit=:limit&skip=:skip 요청을 처리합니다.
func (h *CommitsHandler) HandleCommitsAPI(c *gin.Context) {
	ctx := c.Request.Context()

	// 파라미터 추출 및 검증
	params, err := h.validateAndExtractCommitsParams(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	// 공통 검증 및 저장소 열기
	repoCtx, ok := h.validateAndOpenRepository(ctx, c, params.Owner, params.Repo, params.Ref)
	if !ok {
		return
	}
	defer func() { _ = repoCtx.repoWrapper.Close() }()

	// 커밋 히스토리 조회
	commits, totalCount, mergeBase, err := h.getCommitHistory(ctx, repoCtx.repoWrapper, params)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	// nil 체크: 커밋 히스토리가 nil이면 빈 배열로 변환
	if commits == nil {
		commits = []Commit{}
	}

	// 무조건 날짜별로 커밋 그룹화
	grouped := h.groupCommitsByDate(commits)
	if grouped == nil {
		grouped = []CommitsByDate{}
	}

	// 페이지네이션 정보와 함께 응답 생성
	response := CommitsResponse{
		Success:   true,
		Results:   grouped,
		Message:   "Commits fetched successfully",
		MergeBase: mergeBase,
		Pagination: PaginationInfo{
			Count:      totalCount,
			Next:       buildPagination(totalCount, params.Skip/params.Limit+1, params.Limit, c.Request.URL.String()).Next,
			Previous:   buildPagination(totalCount, params.Skip/params.Limit+1, params.Limit, c.Request.URL.String()).Previous,
			TotalPages: buildPagination(totalCount, params.Skip/params.Limit+1, params.Limit, c.Request.URL.String()).TotalPages,
			PageSize:   params.Limit,
		},
	}

	// JSON 응답 전송
	c.JSON(http.StatusOK, response)
}

// HandleMultiFileCommit는 POST /repos/{owner}/{repo}/commits 요청을 처리하여 다중 파일 작업을 수행합니다.
func (h *CommitsHandler) HandleMultiFileCommit(c *gin.Context) {
	ctx := c.Request.Context()

	// 경로 파라미터 추출
	owner := c.Param("owner")
	repo := c.Param("repo")

	// JSON 요청 파싱 및 검증
	var req MultiFileCommitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": fmt.Sprintf("Invalid JSON: %v", err)})
		return
	}

	// 작업 검증
	if len(req.Operations) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "At least one operation is required"})
		return
	}

	// 각 작업 검증
	for i, op := range req.Operations {
		if err := validateMultiFileOperation(&op); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": fmt.Sprintf("Invalid operation %d: %v", i, err)})
			return
		}
	}

	// 기본 브랜치: 비어 있으면 서비스에서 repo의 default branch(HEAD 기준) 사용
	branch := req.Branch

	// 공통 검증 및 저장소 열기
	repoCtx, ok := h.validateAndOpenRepository(ctx, c, owner, repo, branch)
	if !ok {
		return
	}
	defer func() { _ = repoCtx.repoWrapper.Close() }()

	repository := repoCtx.repoWrapper

	// Extract user ID from X-USER-ID header
	userID := c.GetHeader("X-USER-ID")
	if userID == "" {
		userID = "" // Empty string if not provided
	}

	// Debug log for user ID extraction
	h.logger.LogDebug(ctx, fmt.Sprintf("Extracting user ID from header for commit creation: user_id=%s, owner=%s, repo=%s", userID, owner, repo))

	// 서비스를 통해 비즈니스 로직 실행
	response, err := h.service.ExecuteMultiFileCommitOrCreateBranch(ctx, repository, &req, owner, repo, userID)
	if err != nil {
		// 에러 타입에 따라 적절한 HTTP 상태 코드 반환
		errMsg := err.Error()
		if strings.Contains(errMsg, "failed to resolve") {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": errMsg})
			return
		}
		if strings.Contains(errMsg, "not found") {
			if strings.Contains(errMsg, "Branch") {
				c.JSON(http.StatusNotFound, gin.H{"success": false, "error": errMsg})
			} else if strings.Contains(errMsg, "File") {
				c.JSON(http.StatusNotFound, gin.H{"success": false, "error": errMsg})
			} else {
				c.JSON(http.StatusNotFound, gin.H{"success": false, "error": errMsg})
			}
		} else if strings.Contains(errMsg, "already exists") {
			c.JSON(http.StatusConflict, gin.H{"success": false, "error": errMsg})
		} else if strings.Contains(errMsg, "validation failed") || strings.Contains(errMsg, "operation") {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": errMsg})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": errMsg})
		}
		return
	}

	// 성공 응답 전송
	c.JSON(http.StatusOK, response)
}
