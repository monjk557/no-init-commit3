package revert

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
)

// RevertHandler handles REST API operations for revert operations.
type RevertHandler struct {
	pathConfig   *repositories.PathConfig
	logger       *logging.Logger
	eventsClient events.RedisStreamClient
}

// NewRevertHandler creates a new revert handler.
func NewRevertHandler(cfg *config.Config) (*RevertHandler, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	var eventsClient events.RedisStreamClient
	if cfg.Events.Enabled && cfg.Events.RedisURL != "" {
		eventsClient, err = events.NewRedisStreamClient(cfg.Events.RedisURL, logger.Logger)
		if err != nil {
			_ = err
		}
	}

	return &RevertHandler{
		pathConfig:   pathConfig,
		logger:       logger,
		eventsClient: eventsClient,
	}, nil
}

// HandleRevert handles POST /repos/{owner}/{repo}/revert
// It creates a new branch with a revert commit that undoes the changes
// introduced by a previously merged commit.
func (h *RevertHandler) HandleRevert(c *gin.Context) {
	ctx := c.Request.Context()
	owner := c.Param("owner")
	repo := c.Param("repo")

	if err := h.validatePath(owner, repo); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	var req RevertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": fmt.Sprintf("Invalid JSON: %v", err)})
		return
	}

	if err := h.validateRevertRequest(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": err.Error()})
		return
	}

	// Serialize writes to prevent concurrent conflicts
	repoLock := repositories.GetRepoRWLock(repoPath)
	repoLock.Lock()
	defer repoLock.Unlock()

	result, err := performRevert(ctx, repoPath, &req)
	if err != nil {
		var branchExistsErr *BranchExistsError
		if errors.As(err, &branchExistsErr) {
			c.JSON(http.StatusConflict, gin.H{
				"success":       false,
				"error":         branchExistsErr.Error(),
				"code":          "BRANCH_ALREADY_EXISTS",
				"revert_branch": branchExistsErr.BranchName,
				"revert_commit": branchExistsErr.TipCommit,
			})
			return
		}

		errMsg := err.Error()
		switch {
		case strings.Contains(errMsg, "conflicts"):
			c.JSON(http.StatusConflict, gin.H{
				"success": false,
				"error":   errMsg,
				"code":    "REVERT_CONFLICT",
			})
		case strings.Contains(errMsg, "unknown revision") || strings.Contains(errMsg, "failed to read commit"):
			c.JSON(http.StatusNotFound, gin.H{
				"success": false,
				"error":   errMsg,
				"code":    "COMMIT_NOT_FOUND",
			})
		case strings.Contains(errMsg, "invalid parent_number"):
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   errMsg,
				"code":    "INVALID_PARENT",
			})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": errMsg})
		}
		return
	}

	// Publish revert commit event
	userID := c.GetHeader("X-USER-ID")
	if h.eventsClient != nil && result.RevertCommit != nil {
		h.publishRevertEvent(ctx, repoPath, result, req.TargetRef, owner, repo, userID)
	}

	c.JSON(http.StatusOK, result)
}

// validatePath validates the owner and repo path components.
func (h *RevertHandler) validatePath(owner, repo string) error {
	if owner == "" {
		return fmt.Errorf("owner parameter is required")
	}
	if repo == "" {
		return fmt.Errorf("repo parameter is required")
	}
	pathValidator := repositories.NewGitAPIPathValidator(h.pathConfig)
	return pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo))
}

// validateRevertRequest validates the revert request fields.
func (h *RevertHandler) validateRevertRequest(req *RevertRequest) error {
	if req.CommitSHA == "" {
		return fmt.Errorf("commit_sha is required")
	}
	if req.TargetRef == "" {
		return fmt.Errorf("target_ref is required")
	}
	if req.ParentNumber < 0 {
		return fmt.Errorf("parent_number must be non-negative")
	}
	return nil
}

// publishRevertEvent publishes a revert commit event to Redis Stream.
func (h *RevertHandler) publishRevertEvent(_ context.Context, repoPath string, result *RevertResponse, targetRef, owner, repoName, userID string) {
	if h.eventsClient == nil || result.RevertCommit == nil {
		return
	}

	fsPath, err := repositories.GetRepoPath(h.pathConfig, owner, repoName)
	if err != nil {
		return
	}
	relativePath := events.GetRelativeRepoPath(fsPath, h.pathConfig.GetRepoRoot())

	refName := "refs/heads/" + result.RevertBranch

	event := &events.CommitEvent{
		RepoPath:      relativePath,
		EventType:     events.EventTypeCommit,
		CommitHash:    result.RevertCommit.SHA,
		RefName:       refName,
		Pusher:        "git",
		UserID:        userID,
		CommitMessage: result.RevertCommit.Message,
		Timestamp:     result.RevertCommit.Author.Date,
		PushedAt:      time.Now(),
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.eventsClient.AddCommitEventToStream(ctx, event)
	}()
}
