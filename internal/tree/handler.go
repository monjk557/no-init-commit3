package tree

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

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

	// Create path configuration
	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	// Create logger
	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	// Create path validator
	pathValidator := repositories.NewGitAPIPathValidator(pathConfig)

	// Create repository manager
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

// HandleTreeAPI handles GET /repos/:owner/:repo/tree?ref=:ref
func (h *TreeHandler) HandleTreeAPI(c *gin.Context) {
	ctx := c.Request.Context()

	// Context 취소 확인 (Handler에서 관리)
	select {
	case <-ctx.Done():
		c.JSON(http.StatusRequestTimeout, gin.H{
			"success": false,
			"error":   "Request cancelled or timeout",
		})
		return
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

	// ref가 비어있으면 HEAD(기본 브랜치)로 설정
	if ref == "" {
		ref = "HEAD"
	}

	// 1. parameter 조합 검증
	// Validate permissions and get repository path
	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(404, gin.H{"success": false, "error": "Invalid repository path"})
		return
	}

	// 2. repository 존재 확인
	exists, err := h.repoManager.RepositoryExists(repoPath)
	if err != nil {
		c.JSON(404, gin.H{"success": false, "error": "Repository does not exist"})
		return
	}
	if !exists {
		c.JSON(404, gin.H{"success": false, "error": "Repository not found"})
		return
	}

	// Validate path when provided (path traversal, invalid chars, etc.)
	if path != "" {
		if err := h.pathValidator.ValidateFilePath(path); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
			return
		}
	}

	// Get tree structure
	if recursive {
		treeMap, err := h.getTreeStructureRecursive(repoPath, ref, path)
		if err != nil {
			if strings.Contains(err.Error(), "path is not a tree") {
				c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
				return
			}
			c.JSON(404, gin.H{"success": false, "error": err.Error()})
			return
		}
		// Wrap in response structure
		response := &TreeEntriesRecursiveResponse{
			Success: true,
			Data:    treeMap,
		}
		// Send JSON response with map structure
		c.JSON(http.StatusOK, response)
	} else {
		treeEntries, err := h.getTreeStructure(repoPath, ref, path)
		if err != nil {
			if strings.Contains(err.Error(), "path is not a tree") {
				c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
				return
			}
			c.JSON(404, gin.H{"success": false, "error": err.Error()})
			return
		}
		// Wrap non-recursive response to match recursive format
		pathKey := path
		if pathKey == "" {
			pathKey = "/"
		}

		response := &TreeEntriesRecursiveResponse{
			Success: true,
			Data: map[string][]TreeEntry{
				pathKey: treeEntries.Data,
			},
		}

		// Send JSON response
		c.JSON(http.StatusOK, response)
	}
}
