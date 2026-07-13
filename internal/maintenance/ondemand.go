package maintenance

import (
	"context"
	"errors"

	"go.uber.org/zap"

	"github.com/collabops/git-executor/internal/logging"
)

// ondemand.go adds a synchronous, single-repository maintenance entry point that
// bypasses the periodic scheduler's threshold/cooldown gating.
//
// The periodic scheduler (scheduler.go) only maintains a repo once its loose
// object / pack heuristics trip. A freshly ingested repository — the large
// initial mirror-push at the end of an SVN→Git import (Doc-743 §6.4) — starts
// with a single giant pack and *no* reachability bitmap, so the very first clone
// re-deltas the whole graph and is slow or 502s until the next scan happens to
// pick it up (the maintenance dead-zone behind Issue-5273). This lets the ingest
// path force that repack + bitmap + commit-graph immediately, instead of waiting
// for the scan.

// ErrMaintenanceInProgress is returned when another runner (the periodic
// scheduler or a concurrent on-demand call) already holds the repo's
// maintenance lock. The caller should treat it as "try again later", not a
// failure of the repository itself.
var ErrMaintenanceInProgress = errors.New("maintenance already in progress for repository")

// OnDemand runs forced, full maintenance on one repository on request. It reuses
// the same Executor and per-repo lock as the periodic scheduler, so an on-demand
// run never collides with a scheduled one.
type OnDemand struct {
	executor *Executor
	logger   *logging.Logger
}

// NewOnDemand creates an on-demand maintainer bound to the given config.
func NewOnDemand(config *MaintenanceConfig, logger *logging.Logger) *OnDemand {
	return &OnDemand{
		executor: NewExecutor(config, logger),
		logger:   logger,
	}
}

// ForceFullPlan returns a plan that unconditionally runs the tasks that make a
// freshly ingested repository fast to clone: pack loose refs, geometric repack
// (which, under atomic-bitmap mode, also writes the MIDX + reachability bitmap),
// commit-graph, and the multi-pack-index bitmap.
//
// It deliberately omits TaskPrune: right after an ingest there are no aged
// unreachable objects to expire, and pruning would only add risk of racing a
// concurrent ref update. Object expiry stays the periodic scheduler's job.
func ForceFullPlan() *MaintenancePlan {
	return &MaintenancePlan{
		NeedsMaintenance: true,
		Tasks: []MaintenanceTask{
			TaskPackRefs,
			TaskGeometricRepack,
			TaskCommitGraph,
			TaskMultiPackIndex,
		},
		Reasons: []string{"on-demand full maintenance (post-ingest)"},
	}
}

// MaintainNow forces full maintenance on repoPath and blocks until it finishes.
//
// It acquires the per-repo maintenance lock so it serialises against the
// periodic scheduler and other on-demand runs (Issue-5071). If the lock is held
// it returns ErrMaintenanceInProgress without running. A lock-infrastructure
// error is non-fatal — like the scheduler, it proceeds best-effort so an FS/lock
// glitch never blocks maintenance the caller explicitly asked for.
func (o *OnDemand) MaintainNow(ctx context.Context, repoPath string) (*ExecutionResult, error) {
	lock, acquired, lerr := tryLockRepo(repoPath)
	switch {
	case lerr != nil:
		o.logger.Warn("on-demand maintenance lock error, proceeding best-effort",
			zap.String("repo", repoPath), zap.Error(lerr))
	case !acquired:
		o.logger.Debug("on-demand maintenance skipped, repo locked by another runner",
			zap.String("repo", repoPath))
		return nil, ErrMaintenanceInProgress
	default:
		defer func() { _ = lock.unlock() }()
	}

	return o.executor.Execute(ctx, repoPath, ForceFullPlan()), nil
}
