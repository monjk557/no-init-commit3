package maintenance

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/collabops/git-executor/internal/logging"
)

// Executor runs git maintenance commands on repositories.
// It executes targeted low-level git commands rather than raw `git gc`,
// following the approach of GitHub, GitLab, and Bitbucket.
type Executor struct {
	config *MaintenanceConfig
	logger *logging.Logger
}

// NewExecutor creates a new maintenance executor.
func NewExecutor(config *MaintenanceConfig, logger *logging.Logger) *Executor {
	return &Executor{
		config: config,
		logger: logger,
	}
}

// TaskResult captures the outcome of a single maintenance task.
type TaskResult struct {
	Task     MaintenanceTask
	Duration time.Duration
	Output   string
	Err      error
}

// ExecutionResult captures the outcome of an entire maintenance run on one repository.
type ExecutionResult struct {
	RepoPath    string
	StartedAt   time.Time
	CompletedAt time.Time
	Tasks       []TaskResult
	Success     bool
}

// Execute runs the given maintenance plan on a repository.
// Tasks are executed sequentially in the order specified by the plan,
// since some tasks depend on prior results (e.g., MIDX after repack).
func (e *Executor) Execute(ctx context.Context, repoPath string, plan *MaintenancePlan) *ExecutionResult {
	result := &ExecutionResult{
		RepoPath:  repoPath,
		StartedAt: time.Now(),
		Success:   true,
	}

	e.logger.Info("starting repository maintenance",
		zap.String("repo", repoPath),
		zap.Int("task_count", len(plan.Tasks)),
		zap.Strings("reasons", plan.Reasons))

	for _, task := range plan.Tasks {
		taskCtx, cancel := context.WithTimeout(ctx, e.config.OperationTimeout)

		taskResult := e.executeTask(taskCtx, repoPath, task)
		cancel()

		result.Tasks = append(result.Tasks, taskResult)

		if taskResult.Err != nil {
			e.logger.Error("maintenance task failed",
				zap.String("repo", repoPath),
				zap.String("task", task.String()),
				zap.Duration("duration", taskResult.Duration),
				zap.Error(taskResult.Err))
			result.Success = false
			// Continue with remaining tasks; a pack-refs failure shouldn't block repack.
		} else {
			e.logger.Info("maintenance task completed",
				zap.String("repo", repoPath),
				zap.String("task", task.String()),
				zap.Duration("duration", taskResult.Duration))
		}
	}

	// Update maintenance marker regardless of partial failures
	// so cooldown period applies.
	_ = touchMaintenanceMarker(repoPath)

	result.CompletedAt = time.Now()

	e.logger.Info("repository maintenance finished",
		zap.String("repo", repoPath),
		zap.Bool("success", result.Success),
		zap.Duration("total_duration", result.CompletedAt.Sub(result.StartedAt)))

	return result
}

// executeTask dispatches a single maintenance task.
func (e *Executor) executeTask(ctx context.Context, repoPath string, task MaintenanceTask) TaskResult {
	start := time.Now()
	var output string
	var err error

	switch task {
	case TaskPackRefs:
		output, err = e.runPackRefs(ctx, repoPath)
	case TaskGeometricRepack:
		output, err = e.runGeometricRepack(ctx, repoPath)
	case TaskCommitGraph:
		output, err = e.runCommitGraph(ctx, repoPath)
	case TaskMultiPackIndex:
		output, err = e.runMultiPackIndex(ctx, repoPath)
	case TaskPrune:
		output, err = e.runPrune(ctx, repoPath)
	default:
		err = fmt.Errorf("unknown maintenance task: %d", task)
	}

	return TaskResult{
		Task:     task,
		Duration: time.Since(start),
		Output:   output,
		Err:      err,
	}
}

// runPackRefs consolidates loose refs into packed-refs.
// Used by all major git services (GitHub, GitLab, Bitbucket).
// Command: git pack-refs --all
func (e *Executor) runPackRefs(ctx context.Context, repoPath string) (string, error) {
	return e.runGit(ctx, repoPath, "pack-refs", "--all")
}

// runGeometricRepack performs geometric repacking, the strategy pioneered by GitHub
// and adopted by GitLab since version 16.0.
//
// Geometric repacking maintains packs in a geometric progression where each successive
// pack has at least N times the objects of the previous pack. This means:
//   - Only a small subset of packs is rewritten on each run
//   - Pack count grows logarithmically, never unbounded
//   - Average repack time drops dramatically (GitHub saw 60s → 15s)
//
// Note: --geometric is incompatible with --cruft on git < 2.40.
// We run geometric repack for incremental pack consolidation (like GitHub/GitLab),
// and separately handle unreachable objects via prune or a full cruft repack.
//
// Command: git repack --geometric=<factor> -d
func (e *Executor) runGeometricRepack(ctx context.Context, repoPath string) (string, error) {
	args := []string{
		"repack",
		"--geometric=" + strconv.Itoa(e.config.Repack.GeometricFactor),
		"-d", // Remove redundant packs after repacking
	}

	return e.runGit(ctx, repoPath, args...)
}

// runCommitGraph writes or updates the commit-graph with split strategy and changed-paths bloom filters.
// GitLab uses --split --changed-paths for incremental commit-graph updates.
// Changed-paths bloom filters accelerate file-level history queries.
//
// Command: git commit-graph write --reachable --split --changed-paths
func (e *Executor) runCommitGraph(ctx context.Context, repoPath string) (string, error) {
	return e.runGit(ctx, repoPath,
		"commit-graph", "write",
		"--reachable",
		"--split",
		"--changed-paths",
	)
}

// runMultiPackIndex writes a multi-pack-index spanning all pack files,
// optionally with bitmap for fast fetch operations.
// GitHub pioneered multi-pack bitmaps for repos with many packs.
//
// Command: git multi-pack-index write [--bitmap]
func (e *Executor) runMultiPackIndex(ctx context.Context, repoPath string) (string, error) {
	args := []string{"multi-pack-index", "write"}
	if e.config.Repack.WriteBitmap {
		args = append(args, "--bitmap")
	}
	return e.runGit(ctx, repoPath, args...)
}

// runPrune removes unreachable loose objects older than the configured expire age.
// Only used when cruft packs are disabled (cruft packs handle this internally).
// Bitbucket uses 72h expire window; git default is 2 weeks.
//
// Command: git prune --expire=<time>
func (e *Executor) runPrune(ctx context.Context, repoPath string) (string, error) {
	expireArg := "--expire=" + formatDurationForGit(e.config.Prune.ExpireAge)
	return e.runGit(ctx, repoPath, "prune", expireArg)
}

// runGit executes a git command against the given repository.
func (e *Executor) runGit(ctx context.Context, repoPath string, args ...string) (string, error) {
	fullArgs := append([]string{"--git-dir", repoPath}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)

	// Disable auto-detach to prevent background gc from escaping lock control.
	// This is a critical server-side setting recommended by git documentation.
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=gc.autoDetach",
		"GIT_CONFIG_VALUE_0=false",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	output := strings.TrimSpace(stdout.String() + stderr.String())

	if err != nil {
		return output, fmt.Errorf("git %s failed: %s: %w",
			strings.Join(args, " "), output, err)
	}

	return output, nil
}

// formatDurationForGit converts a Go duration to git's time format (e.g., "72.hours.ago").
func formatDurationForGit(d time.Duration) string {
	hours := int(d.Hours())
	if hours >= 24 && hours%24 == 0 {
		return fmt.Sprintf("%d.days.ago", hours/24)
	}
	return fmt.Sprintf("%d.hours.ago", hours)
}

// touchMaintenanceMarker updates the maintenance marker file's mtime.
func touchMaintenanceMarker(repoPath string) error {
	markerPath := maintenanceMarkerPath(repoPath)
	f, err := os.Create(markerPath)
	if err != nil {
		return err
	}
	return f.Close()
}
