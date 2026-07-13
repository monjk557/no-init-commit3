// Package maintenance owns Git repository housekeeping: the periodic scheduler
// (scheduler.go) plus this synchronous on-demand HTTP entry point that forces a
// full repack + bitmap + commit-graph on a single repository (Doc-743 §6.4).
package maintenance

import (
	"errors"
	"fmt"
	"os"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
)

// Handler serves the on-demand maintenance endpoint.
type Handler struct {
	logger        *logging.Logger
	pathConfig    *repositories.PathConfig
	pathValidator *repositories.GitAPIPathValidator
	onDemand      *OnDemand
}

// NewHandler builds the maintenance HTTP handler from the server config. It
// derives the maintenance package config the same way main.go wires the
// scheduler (DefaultMaintenanceConfig), so on-demand runs use the same repack /
// bitmap / pack-memory behaviour as scheduled ones.
func NewHandler(cfg *config.Config) (*Handler, error) {
	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	maintCfg := DefaultMaintenanceConfig()

	return &Handler{
		logger:        logger,
		pathConfig:    pathConfig,
		pathValidator: repositories.NewGitAPIPathValidator(pathConfig),
		onDemand:      NewOnDemand(maintCfg, logger),
	}, nil
}

// HandleMaintainRepo forces full maintenance on {owner}/{repo} and returns the
// per-task report. Errors map to: 400 (invalid path), 404 (repo missing),
// 409 (maintenance already in progress).
func (h *Handler) HandleMaintainRepo(c *gin.Context) (MaintainRepoResponse, error) {
	owner := c.Param("owner")
	repo := c.Param("repo")

	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo)); err != nil {
		return MaintainRepoResponse{}, api.NewCoded(api.ErrCodeInvalidURLParameters,
			fmt.Sprintf("Invalid repository path: %v", err))
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return MaintainRepoResponse{}, api.NewCoded(api.ErrCodeInvalidURLParameters,
			fmt.Sprintf("Invalid repository path: %v", err))
	}
	if _, statErr := os.Stat(repoPath); statErr != nil {
		return MaintainRepoResponse{}, api.NewCoded(api.ErrCodeRepositoryNotFound,
			fmt.Sprintf("Repository not found: %s/%s", owner, repo))
	}

	h.logger.Info("on-demand maintenance requested",
		zap.String("owner", owner), zap.String("repo", repo))

	result, err := h.onDemand.MaintainNow(c.Request.Context(), repoPath)
	if err != nil {
		if errors.Is(err, ErrMaintenanceInProgress) {
			return MaintainRepoResponse{}, api.NewCoded(api.ErrCodeConflict,
				"Maintenance already in progress for repository")
		}
		return MaintainRepoResponse{}, api.NewCoded(api.ErrCodeInternalError,
			fmt.Sprintf("Maintenance failed: %v", err))
	}

	return toMaintainRepoResponse(owner, repo, result), nil
}

// toMaintainRepoResponse maps the internal ExecutionResult onto the wire schema.
func toMaintainRepoResponse(owner, repo string, result *ExecutionResult) MaintainRepoResponse {
	resp := MaintainRepoResponse{
		Success:    result.Success,
		Owner:      owner,
		Repo:       repo,
		DurationMs: result.CompletedAt.Sub(result.StartedAt).Milliseconds(),
		Tasks:      make([]MaintainTaskResult, 0, len(result.Tasks)),
	}
	for _, t := range result.Tasks {
		tr := MaintainTaskResult{
			Task:       t.Task.String(),
			Success:    t.Err == nil,
			DurationMs: t.Duration.Milliseconds(),
		}
		if t.Err != nil {
			tr.Error = t.Err.Error()
		}
		resp.Tasks = append(resp.Tasks, tr)
	}
	return resp
}
