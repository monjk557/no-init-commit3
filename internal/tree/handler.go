package tree

import (
	"fmt"
	"net/http"

	"github.com/collabops/git-executor/internal/api/typed"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
)

// TreeHandler는 Git tree REST API 엔드포인트를 처리합니다.
// 디렉토리 구조 조회 요청을 받아서 JSON 형식으로 응답합니다.
type TreeHandler struct {
	pathConfig    *repositories.PathConfig
	pathValidator *repositories.GitAPIPathValidator
	repoManager   *repositories.DefaultRepositoryManager
	logger        *logging.Logger
}

// NewTreeHandler는 새로운 tree 핸들러를 생성합니다.
// 설정과 권한 클라이언트를 사용하여 초기화합니다.
func NewTreeHandler(cfg *config.Config) (*TreeHandler, error) {
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

	repoManager, err := repositories.NewRepositoryManager(pathConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create repository manager: %w", err)
	}

	return &TreeHandler{
		pathConfig:    pathConfig,
		pathValidator: pathValidator,
		repoManager:   repoManager,
		logger:        logger,
	}, nil
}

// HandleTreeAPI handles GET /repos/{owner}/{repo}/tree?ref=&path=&recursive=
//
// Typed handler — query string 은 typed.RegisterGetWithQuery wrapper 가
// TreeAPIRequest 로 자동 바인딩한다. substring 기반 status 매핑
// ("path is not a tree" → 400) 은 routes.go 의 ErrorMapper 가 담당.
// 단순 4xx 는 api.NewCoded.
func (h *TreeHandler) HandleTreeAPI(c *gin.Context, req TreeAPIRequest) (TreeEntriesRecursiveResponse, error) {
	ctx := c.Request.Context()

	// Context 취소 확인 (Handler에서 관리)
	select {
	case <-ctx.Done():
		return TreeEntriesRecursiveResponse{}, typed.NewHTTPErrorWithCode(http.StatusTooManyRequests, "Request cancelled or timeout", "TIMEOUT", nil)
	default:
	}

	owner := c.Param("owner")
	repo := c.Param("repo")
	ref := req.Ref
	path := req.Path
	recursive := req.Recursive

	if ref == "" {
		ref = "HEAD"
	}

	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return TreeEntriesRecursiveResponse{}, typed.NewHTTPErrorWithCode(http.StatusNotFound, "Invalid repository path", "REPOSITORY_NOT_FOUND", nil)
	}

	exists, err := h.repoManager.RepositoryExists(repoPath)
	if err != nil {
		return TreeEntriesRecursiveResponse{}, typed.NewHTTPErrorWithCode(http.StatusNotFound, "Repository does not exist", "REPOSITORY_NOT_FOUND", nil)
	}
	if !exists {
		return TreeEntriesRecursiveResponse{}, typed.NewHTTPErrorWithCode(http.StatusNotFound, "Repository not found", "REPOSITORY_NOT_FOUND", nil)
	}

	if path != "" {
		if err := h.pathValidator.ValidateFilePath(path); err != nil {
			return TreeEntriesRecursiveResponse{}, typed.NewHTTPErrorWithCode(http.StatusBadRequest, err.Error(), "INVALID_INPUT", nil)
		}
	}

	if recursive {
		treeMap, err := h.getTreeStructureRecursive(repoPath, ref, path)
		if err != nil {
			return TreeEntriesRecursiveResponse{}, err
		}
		return TreeEntriesRecursiveResponse{
			Success: true,
			Tree:    treeMap,
		}, nil
	}

	treeEntries, err := h.getTreeStructure(repoPath, ref, path)
	if err != nil {
		return TreeEntriesRecursiveResponse{}, err
	}

	pathKey := path
	if pathKey == "" {
		pathKey = "/"
	}

	return TreeEntriesRecursiveResponse{
		Success: true,
		Tree: map[string][]TreeEntry{
			pathKey: treeEntries.Entries,
		},
	}, nil
}

// HandleTreeSearch handles GET /repos/{owner}/{repo}/tree/search?ref=&query=&limit=
//
// Typed handler — query string 은 typed.RegisterGetWithQuery wrapper 가
// TreeSearchRequest 로 자동 바인딩한다. "failed to resolve reference" → 404,
// 그 외 raw 는 500. 단순 4xx 는 api.NewCoded.
func (h *TreeHandler) HandleTreeSearch(c *gin.Context, req TreeSearchRequest) (TreeSearchResponse, error) {
	owner := c.Param("owner")
	repo := c.Param("repo")
	ref := req.Ref
	query := req.Query

	if query == "" {
		return TreeSearchResponse{}, typed.NewHTTPErrorWithCode(http.StatusBadRequest, "query parameter is required", "INVALID_INPUT", nil)
	}

	if ref == "" {
		ref = "HEAD"
	}

	limit := DefaultSearchLimit
	if req.Limit > 0 {
		if req.Limit > MaxSearchLimit {
			limit = MaxSearchLimit
		} else {
			limit = req.Limit
		}
	}

	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return TreeSearchResponse{}, typed.NewHTTPErrorWithCode(http.StatusNotFound, "Invalid repository path", "REPOSITORY_NOT_FOUND", nil)
	}

	exists, err := h.repoManager.RepositoryExists(repoPath)
	if err != nil || !exists {
		return TreeSearchResponse{}, typed.NewHTTPErrorWithCode(http.StatusNotFound, "Repository not found", "REPOSITORY_NOT_FOUND", nil)
	}

	entries, totalMatches, err := SearchFiles(repoPath, ref, query, limit)
	if err != nil {
		return TreeSearchResponse{}, err
	}

	return TreeSearchResponse{
		Success:    true,
		Query:      query,
		Entries:    entries,
		TotalCount: totalMatches,
		Truncated:  totalMatches > limit,
	}, nil
}
