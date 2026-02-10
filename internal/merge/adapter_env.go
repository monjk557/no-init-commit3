package merge

import (
	"context"
	"time"

	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	gogit "github.com/go-git/go-git/v5"
)

// env.go - Layer 2: Adapters
// MergeEnv는 "환경"만 담고, 도메인 입력(MergeRequest)은 별도로 전달

// MergeEnv contains environmental dependencies for merge operations
// This is NOT a God Object - it only contains:
// - Context (for cancellation)
// - Repository (for git operations)
// - Now (for deterministic timestamps)
// - Logger (for observability)
// - PathConfig (for path resolution)
//
// Domain inputs (MergeRequest, analysis results) should be passed separately
type MergeEnv struct {
	Ctx        context.Context
	Repo       *gogit.Repository
	Now        time.Time
	Logger     *logging.Logger
	PathConfig *repositories.PathConfig
}

// NewMergeEnv creates a new MergeEnv
func NewMergeEnv(
	ctx context.Context,
	repo *gogit.Repository,
	logger *logging.Logger,
	pathConfig *repositories.PathConfig,
) *MergeEnv {
	return &MergeEnv{
		Ctx:        ctx,
		Repo:       repo,
		Now:        time.Now(),
		Logger:     logger,
		PathConfig: pathConfig,
	}
}

// NewMergeEnvWithTime creates a new MergeEnv with a specific time
// This is useful for deterministic testing
func NewMergeEnvWithTime(
	ctx context.Context,
	repo *gogit.Repository,
	now time.Time,
	logger *logging.Logger,
	pathConfig *repositories.PathConfig,
) *MergeEnv {
	return &MergeEnv{
		Ctx:        ctx,
		Repo:       repo,
		Now:        now,
		Logger:     logger,
		PathConfig: pathConfig,
	}
}

// IsCancelled checks if the context has been cancelled
func (e *MergeEnv) IsCancelled() bool {
	select {
	case <-e.Ctx.Done():
		return true
	default:
		return false
	}
}
