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

// validateAndGetRepoPath는 저장소 검증 및 경로를 가져오는 공통 로직입니다.
func (h *CommitsHandler) validateAndGetRepoPath(ctx context.Context, c *gin.Context, owner, repo, ref string) (*repositoryContext, bool) {
	if ref == "" {
		ref = "HEAD"
	}

	// 경로 구성 요소 검증
	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo)); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return nil, false
	}

	// 저장소 경로 가져오기 및 존재 확인
	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return nil, false
	}

	return &repositoryContext{
		owner:    owner,
		repo:     repo,
		ref:      ref,
		repoPath: repoPath,
	}, true
}

// HandleCommitsAPI는 GET /repos/:owner/:repo/commits 요청을 처리합니다.
func (h *CommitsHandler) HandleCommitsAPI(c *gin.Context) {
	ctx := c.Request.Context()

	params, err := h.validateAndExtractCommitsParams(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	repoCtx, ok := h.validateAndGetRepoPath(ctx, c, params.Owner, params.Repo, params.Ref)
	if !ok {
		return
	}

	commits, totalCount, mergeBase, err := h.getCommitHistory(ctx, repoCtx.repoPath, params)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	if commits == nil {
		commits = []Commit{}
	}

	grouped := h.groupCommitsByDate(commits)
	if grouped == nil {
		grouped = []CommitsByDate{}
	}

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

	c.JSON(http.StatusOK, response)
}

// HandleMultiFileCommit는 POST /repos/{owner}/{repo}/commits 요청을 처리합니다.
func (h *CommitsHandler) HandleMultiFileCommit(c *gin.Context) {
	ctx := c.Request.Context()

	owner := c.Param("owner")
	repo := c.Param("repo")

	var req MultiFileCommitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": fmt.Sprintf("Invalid JSON: %v", err)})
		return
	}

	if len(req.Operations) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "At least one operation is required"})
		return
	}

	for i, op := range req.Operations {
		if err := validateMultiFileOperation(&op); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": fmt.Sprintf("Invalid operation %d: %v", i, err)})
			return
		}
	}

	branch := req.Branch

	repoCtx, ok := h.validateAndGetRepoPath(ctx, c, owner, repo, branch)
	if !ok {
		return
	}

	userID := c.GetHeader("X-USER-ID")

	h.logger.LogDebug(ctx, fmt.Sprintf("Extracting user ID from header for commit creation: user_id=%s, owner=%s, repo=%s", userID, owner, repo))

	response, err := h.service.ExecuteMultiFileCommitOrCreateBranch(ctx, repoCtx.repoPath, &req, owner, repo, userID)
	if err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "failed to resolve") {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": errMsg})
			return
		}
		if strings.Contains(errMsg, "not found") {
			c.JSON(http.StatusNotFound, gin.H{"success": false, "error": errMsg})
		} else if strings.Contains(errMsg, "already exists") {
			c.JSON(http.StatusConflict, gin.H{"success": false, "error": errMsg})
		} else if strings.Contains(errMsg, "validation failed") || strings.Contains(errMsg, "operation") {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": errMsg})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": errMsg})
		}
		return
	}

	c.JSON(http.StatusOK, response)
}
