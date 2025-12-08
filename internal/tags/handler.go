package tags

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
)

type TagsHandler struct {
	repoManager *repositories.DefaultRepositoryManager
	pathConfig  *repositories.PathConfig
}

func NewTagsHandler(cfg *config.Config) (*TagsHandler, error) {
	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}
	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}
	repoManager, err := repositories.NewRepositoryManager(pathConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create repository manager: %w", err)
	}
	return &TagsHandler{repoManager: repoManager, pathConfig: pathConfig}, nil
}

func (h *TagsHandler) HandleGetTags(c *gin.Context) {
	owner := c.Param("owner")
	repo := c.Param("repo")

	repoPath := h.pathConfig.FormatRepoPath(owner, repo)

	repoWrapper, err := h.repoManager.OpenRepository(repoPath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "Repository not found"})
		return
	}

	tags, err := GetTags(repoWrapper)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "Failed to get tags", "details": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "tags": tags})
}

func (h *TagsHandler) HandleCreateTag(c *gin.Context) {
	owner := c.Param("owner")
	repo := c.Param("repo")

	var req CreateTagRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "Invalid request body", "details": err.Error()})
		return
	}

	repoPath := h.pathConfig.FormatRepoPath(owner, repo)
	repoWrapper, err := h.repoManager.OpenRepository(repoPath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "Repository not found"})
		return
	}

	tag, err := CreateTag(repoWrapper, &req)
	if err != nil {
		// 에러 메시지에 따라 적절한 상태 코드 선택
		statusCode := http.StatusBadRequest
		errMsg := err.Error()
		if strings.Contains(errMsg, "not found") || strings.Contains(errMsg, "commit not found") {
			statusCode = http.StatusNotFound
		}
		c.JSON(statusCode, gin.H{"success": false, "error": "Failed to create tag", "details": errMsg})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "tag": tag})
}

func (h *TagsHandler) HandleDeleteTag(c *gin.Context) {
	owner := c.Param("owner")
	repo := c.Param("repo")
	tagName := c.Param("tag")

	repoPath := h.pathConfig.FormatRepoPath(owner, repo)
	repoWrapper, err := h.repoManager.OpenRepository(repoPath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "Repository not found"})
		return
	}

	err = DeleteTag(repoWrapper, tagName)
	if err != nil {
		// 에러 메시지에 따라 적절한 상태 코드 선택
		statusCode := http.StatusBadRequest
		errMsg := err.Error()
		if strings.Contains(errMsg, "not found") {
			statusCode = http.StatusNotFound
		}
		c.JSON(statusCode, gin.H{"success": false, "error": "Failed to delete tag", "details": errMsg})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "tag": tagName})
}
