package repositories

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// RepositoryCreationHandler는 저장소 생성 작업을 처리합니다.
type RepositoryCreationHandler struct {
	config        *config.Config
	logger        *logging.Logger
	pathConfig    PathConfig
	pathValidator GitAPIPathValidator
	repoManager   *DefaultRepositoryManager
}

// NewRepositoryCreationHandler는 새로운 저장소 생성 핸들러를 생성합니다.
func NewRepositoryCreationHandler(cfg *config.Config) (*RepositoryCreationHandler, error) {
	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	pathConfig, err := NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	pathValidator := NewGitAPIPathValidator(pathConfig)

	repoManager, err := NewRepositoryManager(pathConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create repository manager: %w", err)
	}

	return &RepositoryCreationHandler{
		config:        cfg,
		logger:        logger,
		pathConfig:    *pathConfig,
		pathValidator: *pathValidator,
		repoManager:   repoManager,
	}, nil
}

// HandleCreateRepository handles POST /repos/{owner}/{repo} for creating new repositories
func (h *RepositoryCreationHandler) HandleCreateRepository(c *gin.Context) {
	owner := c.Param("owner")
	repo := c.Param("repo")

	h.logger.Info("Processing repository creation request",
		zap.String("owner", owner),
		zap.String("repo", repo))

	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Invalid path: %v", err),
		})
		return
	}

	var req CreateRepositoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		req = CreateRepositoryRequest{
			InitialCommit: true,
			DefaultBranch: "main",
		}
	}

	if req.DefaultBranch == "" {
		req.DefaultBranch = "main"
	}

	repoPath := h.pathConfig.FormatRepoPath(owner, repo)
	if _, err := os.Stat(repoPath); !os.IsNotExist(err) {
		c.JSON(http.StatusConflict, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Repository %s/%s already exists", owner, repo),
		})
		return
	}

	createdRepo, err := h.createRepository(owner, repo, &req)
	if err != nil {
		h.logger.Error("Failed to create repository",
			zap.String("owner", owner),
			zap.String("repo", repo),
			zap.Error(err))

		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Failed to create repository: %v", err),
		})
		return
	}

	c.JSON(http.StatusCreated, createdRepo)
}

// createRepository creates a new Git repository with optional initial commit
func (h *RepositoryCreationHandler) createRepository(owner, repo string, req *CreateRepositoryRequest) (*CreateRepositoryResponse, error) {
	repoPath := h.pathConfig.FormatRepoPath(owner, repo)

	var author *CommitAuthor
	if req.Author != nil {
		author = &CommitAuthor{
			Name:  req.Author.Name,
			Email: req.Author.Email,
			Date:  time.Now(),
		}
	}

	options := &CreateRepositoryOptions{
		Path:          repoPath,
		Bare:          true,
		DefaultBranch: req.DefaultBranch,
		InitialCommit: req.InitialCommit,
		Author:        author,
		InitialFiles:  req.InitialFiles,
	}

	repoInfo, err := h.repoManager.CreateRepository(options)
	if err != nil {
		return nil, fmt.Errorf("failed to create repository: %w", err)
	}

	response := &CreateRepositoryResponse{
		Success:       true,
		Repository:    fmt.Sprintf("%s/%s", owner, repo),
		Owner:         owner,
		Name:          repo,
		Path:          repoInfo.Path,
		DefaultBranch: repoInfo.DefaultBranch,
		CreatedAt:     repoInfo.CreatedAt,
		Metadata:      req.Metadata,
	}

	// Get initial commit hash using native git
	if req.InitialCommit {
		ctx := context.Background()
		hash, err := nativeResolveRef(ctx, repoPath, "HEAD")
		if err == nil {
			response.InitialCommit = hash
		}
	}

	h.logger.Info("Repository created successfully",
		zap.String("repository", response.Repository),
		zap.String("path", repoPath),
		zap.String("initial_commit", response.InitialCommit))

	return response, nil
}
