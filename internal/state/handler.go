package state

import (
	"fmt"
	"net/http"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// StateHandler handles repository state requests
type StateHandler struct {
	config        *config.Config
	logger        *logging.Logger
	pathConfig    *repositories.PathConfig
	pathValidator *repositories.GitAPIPathValidator
	stateService  *StateService
}

// NewStateHandler creates a new state handler
func NewStateHandler(cfg *config.Config) (*StateHandler, error) {
	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	pathValidator := repositories.NewGitAPIPathValidator(pathConfig)

	stateService, err := NewStateService(logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create state service: %w", err)
	}

	return &StateHandler{
		config:        cfg,
		logger:        logger,
		pathConfig:    pathConfig,
		pathValidator: pathValidator,
		stateService:  stateService,
	}, nil
}

// HandleGetRepositoryState handles GET /repos/{owner}/{repo}/stats
func (h *StateHandler) HandleGetRepositoryState(c *gin.Context) {
	// Extract URL parameters
	owner := c.Param("owner")
	repo := c.Param("repo")

	h.logger.Info("Processing repository state request",
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

	// Get repository path
	repoPath := h.pathConfig.FormatRepoPath(owner, repo)

	// Get repository state
	state, err := h.stateService.GetRepositoryState(owner, repo, repoPath)
	if err != nil {
		h.logger.Error("Failed to get repository state",
			zap.String("owner", owner),
			zap.String("repo", repo),
			zap.Error(err))

		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Failed to get repository state: %v", err),
		})
		return
	}

	// Return success response
	c.JSON(http.StatusOK, state)
}
