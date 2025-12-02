// Package protocol은 Git 저장소 생성 작업을 처리합니다.
// 새로운 Git 저장소를 만들고 초기 커밋을 설정하는 REST API 기능을 제공합니다.
package protocol

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/git"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repository"
	"github.com/collabops/git-executor/internal/schemas/responses"
	"github.com/gin-gonic/gin"
	gogit "github.com/go-git/go-git/v5"
	"go.uber.org/zap"
)

// RepositoryCreationHandler는 저장소 생성 작업을 처리합니다.
// Git 저장소 초기화, 기본 브랜치 설정, 초기 파일 생성 등을 처리합니다.
type RepositoryCreationHandler struct {
	config             *config.Config
	logger             *logging.Logger
	pathConfig         *repository.PathConfig
	pathValidator      *repository.GitAPIPathValidator
	objectManager      *git.GitObjectManager
	repoManager        *git.DefaultRepositoryManager
	responseMiddleware *api.JSONResponseMiddleware
	errorMapper        *api.GitErrorMapper
}

// NewRepositoryCreationHandler는 새로운 저장소 생성 핸들러를 생성합니다.
// 설정을 바탕으로 필요한 컴포너트들을 초기화합니다.
func NewRepositoryCreationHandler(cfg *config.Config) (*RepositoryCreationHandler, error) {
	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	pathConfig, err := repository.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	pathValidator := repository.NewGitAPIPathValidator(pathConfig)

	objectManager, err := git.NewGitObjectManager(pathConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create object manager: %w", err)
	}

	responseMiddleware := api.NewJSONResponseMiddleware(logger, "v1")
	errorMapper := api.NewGitErrorMapper()

	// Create repository manager
	repoManager, err := git.NewRepositoryManager(pathConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create repository manager: %w", err)
	}

	return &RepositoryCreationHandler{
		config:             cfg,
		logger:             logger,
		pathConfig:         pathConfig,
		pathValidator:      pathValidator,
		objectManager:      objectManager,
		repoManager:        repoManager,
		responseMiddleware: responseMiddleware,
		errorMapper:        errorMapper,
	}, nil
}

// CreateRepositoryRequest는 저장소 생성을 위한 JSON 페이로드를 나타냅니다.
// 기본 브랜치, 초기 파일 및 메타데이터를 포함합니다.
type CreateRepositoryRequest struct {
	Private       bool                   `json:"private,omitempty"`
	InitialCommit bool                   `json:"initial_commit,omitempty"`
	DefaultBranch string                 `json:"default_branch,omitempty"`
	Author        *CommitAuthor          `json:"author,omitempty"`
	InitialFiles  map[string]string      `json:"initial_files,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}

// Note: CommitAuthor is already defined in file_operations.go

// HandleCreateRepository handles POST /repos/{owner}/{repo} for creating new repositories
func (h *RepositoryCreationHandler) HandleCreateRepository(c *gin.Context) {
	ctx := c.Request.Context()

	// Extract URL parameters
	owner := c.Param("owner")
	repo := c.Param("repo")

	h.logger.Info("Processing repository creation request",
		zap.String("owner", owner),
		zap.String("repo", repo))

	// Validate path components
	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Invalid path: %v", err),
		})
		return
	}

	// Parse and validate JSON request
	var req CreateRepositoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Allow empty body - use defaults
		req = CreateRepositoryRequest{
			InitialCommit: true,
			DefaultBranch: "main",
		}
	}

	// Set defaults
	if req.DefaultBranch == "" {
		req.DefaultBranch = "main"
	}

	// Check if repository already exists
	repoPath := h.pathConfig.FormatRepoPath(owner, repo)
	if _, err := os.Stat(repoPath); !os.IsNotExist(err) {
		c.JSON(http.StatusConflict, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Repository %s/%s already exists", owner, repo),
		})
		return
	}

	// Create repository
	createdRepo, err := h.createRepository(ctx, owner, repo, &req)
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

	// Return success response
	c.JSON(http.StatusCreated, createdRepo)
}

// createRepository creates a new Git repository with optional initial commit
func (h *RepositoryCreationHandler) createRepository(ctx context.Context, owner, repo string, req *CreateRepositoryRequest) (*responses.CreateRepositoryResponse, error) {
	repoPath := h.pathConfig.FormatRepoPath(owner, repo)

	// Prepare author information
	var author *git.CommitAuthor
	if req.Author != nil {
		author = &git.CommitAuthor{
			Name:  req.Author.Name,
			Email: req.Author.Email,
			Date:  time.Now(),
		}
	}

	// Create repository options
	options := &git.CreateRepositoryOptions{
		Path:          repoPath,
		Bare:          true, // Always create bare repositories for server-side storage
		DefaultBranch: req.DefaultBranch,
		InitialCommit: req.InitialCommit,
		Author:        author,
		InitialFiles:  req.InitialFiles,
	}

	// Use repository manager to create the repository
	repoInfo, err := h.repoManager.CreateRepository(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("failed to create repository: %w", err)
	}

	response := &responses.CreateRepositoryResponse{
		Success:       true,
		Repository:    fmt.Sprintf("%s/%s", owner, repo),
		Owner:         owner,
		Name:          repo,
		Path:          repoInfo.Path,
		DefaultBranch: repoInfo.DefaultBranch,
		CreatedAt:     repoInfo.CreatedAt,
		Metadata:      req.Metadata,
	}

	// Set initial commit hash if one was created
	if req.InitialCommit {
		// We need to get the commit hash from the repository
		gitRepo, err := gogit.PlainOpen(repoPath)
		if err == nil {
			if head, err := gitRepo.Head(); err == nil {
				response.InitialCommit = head.Hash().String()
			}
		}
	}

	h.logger.Info("Repository created successfully",
		zap.String("repository", response.Repository),
		zap.String("path", repoPath),
		zap.String("initial_commit", response.InitialCommit))

	return response, nil
}
