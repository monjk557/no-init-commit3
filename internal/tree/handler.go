package tree

import (
	"fmt"
	"net/http"
	"strconv"

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
// Typed handler — substring 기반 status 매핑 ("path is not a tree" → 400) 은
// routes.go 의 ErrorMapper 가 담당. 단순 4xx 는 typed.NewHTTPError.
func (h *TreeHandler) HandleTreeAPI(c *gin.Context) (TreeEntriesRecursiveResponse, error) {
	ctx := c.Request.Context()

	// Context 취소 확인 (Handler에서 관리)
	select {
	case <-ctx.Done():
		return TreeEntriesRecursiveResponse{}, typed.NewHTTPError(http.StatusRequestTimeout, "Request cancelled or timeout")
	default:
	}

	owner := c.Param("owner")
	repo := c.Param("repo")
	ref := c.Query("ref")
	path := c.Query("path")
	recursiveStr := c.Query("recursive")
	recursive := false
	if recursiveStr != "" {
		recursive, _ = strconv.ParseBool(recursiveStr)
	}

	if ref == "" {
		ref = "HEAD"
	}

	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return TreeEntriesRecursiveResponse{}, typed.NewHTTPError(http.StatusNotFound, "Invalid repository path")
	}

	exists, err := h.repoManager.RepositoryExists(repoPath)
	if err != nil {
		return TreeEntriesRecursiveResponse{}, typed.NewHTTPError(http.StatusNotFound, "Repository does not exist")
	}
	if !exists {
		return TreeEntriesRecursiveResponse{}, typed.NewHTTPError(http.StatusNotFound, "Repository not found")
	}

	if path != "" {
		if err := h.pathValidator.ValidateFilePath(path); err != nil {
			return TreeEntriesRecursiveResponse{}, typed.NewHTTPError(http.StatusBadRequest, err.Error())
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
// Typed handler — "failed to resolve reference" → 404, 그 외 raw 는 500. 단순
// 4xx 는 typed.NewHTTPError.
func (h *TreeHandler) HandleTreeSearch(c *gin.Context) (TreeSearchResponse, error) {
	owner := c.Param("owner")
	repo := c.Param("repo")
	ref := c.Query("ref")
	query := c.Query("query")

	if query == "" {
		return TreeSearchResponse{}, typed.NewHTTPError(http.StatusBadRequest, "query parameter is required")
	}

	if ref == "" {
		ref = "HEAD"
	}

	limit := DefaultSearchLimit
	if limitStr := c.Query("limit"); limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 {
			if parsed > MaxSearchLimit {
				limit = MaxSearchLimit
			} else {
				limit = parsed
			}
		}
	}

	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return TreeSearchResponse{}, typed.NewHTTPError(http.StatusNotFound, "Invalid repository path")
	}

	exists, err := h.repoManager.RepositoryExists(repoPath)
	if err != nil || !exists {
		return TreeSearchResponse{}, typed.NewHTTPError(http.StatusNotFound, "Repository not found")
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
