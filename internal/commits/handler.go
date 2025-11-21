package commits

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
)

// CommitsHandler는 Git 커밋 로그 REST API 엔드포인트를 처리합니다.
// 커밋 히스토리 조회 요청을 받아서 페이지네이션과 함께 응답합니다.
type CommitsHandler struct {
	pathConfig *repositories.PathConfig
	logger     *logging.Logger
	service    *CommitsService
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

	transactionManager := NewTransactionManager(logger)
	service := NewCommitsService(logger, transactionManager)

	return &CommitsHandler{
		pathConfig: pathConfig,
		logger:     logger,
		service:    service,
	}, nil
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

	// 저장소 경로 조회 및 검증
	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	// 커밋 히스토리 조회
	commits, totalCount, err := h.getCommitHistory(ctx, repoPath, params)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	// sort=date 파라미터에 따라 응답 형식 결정
	var results interface{}
	if params.Sort == "date" {
		// 날짜별로 커밋 그룹화
		results = h.groupCommitsByDate(commits)
	} else {
		// 기본형: 커밋 목록을 그대로 반환
		results = commits
	}

	// 페이지네이션 정보와 함께 응답 생성
	response := CommitsResponse{
		Success: true,
		Results: results,
		Message: "Commits fetched successfully",
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

	// 기본 브랜치 설정
	branch := req.Branch
	if branch == "" {
		branch = "main"
	}

	// 저장소 경로 조회 및 검증
	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	// 저장소 열기
	repository, err := repositories.OpenRepositorySimple(repoPath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "Repository not found"})
		return
	}
	defer func() { _ = repository.Close() }()

	// 서비스를 통해 비즈니스 로직 실행
	response, err := h.service.ExecuteMultiFileCommit(ctx, repository, &req, branch)
	if err != nil {
		// 에러 타입에 따라 적절한 HTTP 상태 코드 반환
		errMsg := err.Error()
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
