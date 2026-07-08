package maintenance

import (
	"context"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/collabops/git-executor/internal/logging"
)

// Scheduler orchestrates periodic maintenance across all repositories.
// It scans the repository root, analyzes each repo, and runs maintenance
// concurrently with bounded parallelism.
type Scheduler struct {
	config   *MaintenanceConfig
	repoRoot string
	analyzer *Analyzer
	executor *Executor
	logger   *logging.Logger

	stopChan chan struct{}
	wg       sync.WaitGroup

	// Metrics
	totalRuns            atomic.Int64
	totalReposMaintained atomic.Int64
	totalErrors          atomic.Int64
	lastRunAt            atomic.Value // time.Time
}

// NewScheduler creates a new maintenance scheduler.
func NewScheduler(config *MaintenanceConfig, repoRoot string, logger *logging.Logger) *Scheduler {
	return &Scheduler{
		config:   config,
		repoRoot: repoRoot,
		analyzer: NewAnalyzer(config),
		executor: NewExecutor(config, logger),
		logger:   logger,
		stopChan: make(chan struct{}),
	}
}

// Start begins the periodic maintenance scheduler in the background.
func (s *Scheduler) Start() {
	if !s.config.Enabled {
		s.logger.Info("git maintenance scheduler is disabled")
		return
	}

	s.wg.Add(1)
	go s.loop()

	s.logger.Info("git maintenance scheduler started",
		zap.Duration("scan_interval", s.config.ScanInterval),
		zap.Duration("cooldown_period", s.config.CooldownPeriod),
		zap.Int("max_concurrent_jobs", s.config.MaxConcurrentJobs),
		zap.Int("loose_objects_threshold", s.config.Thresholds.LooseObjectsThreshold),
		zap.Int("pack_files_threshold", s.config.Thresholds.PackFilesThreshold))
}

// Stop gracefully shuts down the scheduler and waits for in-progress jobs.
func (s *Scheduler) Stop() {
	select {
	case <-s.stopChan:
		return // Already stopped
	default:
		close(s.stopChan)
	}
	s.wg.Wait()
	s.logger.Info("git maintenance scheduler stopped")
}

// Stats returns current scheduler statistics.
func (s *Scheduler) Stats() map[string]interface{} {
	var lastRun time.Time
	if v := s.lastRunAt.Load(); v != nil {
		lastRun = v.(time.Time)
	}

	return map[string]interface{}{
		"total_runs":             s.totalRuns.Load(),
		"total_repos_maintained": s.totalReposMaintained.Load(),
		"total_errors":           s.totalErrors.Load(),
		"last_run_at":            lastRun,
	}
}

// loop is the main scheduler loop that runs scans at the configured interval.
func (s *Scheduler) loop() {
	defer s.wg.Done()

	// Run once at startup after a brief delay to let the server stabilize. A
	// fresh pod re-evaluates every repository at once, so its first scan is the
	// moment the most repacks fire together — the "startup repack wave" behind
	// the largest memory spikes (Issue-5414). Two mitigations:
	//   1. Per-pod startup jitter spreads that wave across replicas so they do
	//      not all repack the same repo set (and contend on repo locks) at once.
	//   2. The first scan runs single-jobbed (maxJobs=1) so, while every repo is
	//      eligible, at most one repack executes at a time during cold start;
	//      steady-state scans use the configured concurrency.
	select {
	case <-time.After(s.config.InitialDelay + s.startupJitter()):
		s.runScan(1)
	case <-s.stopChan:
		return
	}

	ticker := time.NewTicker(s.config.ScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.runScan(s.config.MaxConcurrentJobs)
		case <-s.stopChan:
			return
		}
	}
}

// startupJitter returns a deterministic per-pod delay in [0, ScanInterval/4),
// derived from the hostname, that offsets replicas' startup scans so they do
// not repack in lockstep. Deterministic (not random) so a pod restart lands on
// the same offset instead of reshuffling the whole fleet.
func (s *Scheduler) startupJitter() time.Duration {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return 0
	}
	// Spread across whole seconds of the span to avoid overflow/precision traps
	// of nanosecond modulo (a raw uint32 of ns is only ~4.3s, far short of span).
	spanSec := int64(s.config.ScanInterval / 4 / time.Second)
	if spanSec <= 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(host))
	return time.Duration(int64(h.Sum32())%spanSec) * time.Second
}

// runScan discovers all repositories and runs maintenance on those that need it.
// maxJobs bounds how many repositories are maintained concurrently.
func (s *Scheduler) runScan(maxJobs int) {
	s.totalRuns.Add(1)
	s.lastRunAt.Store(time.Now())

	repos, err := s.discoverRepositories()
	if err != nil {
		s.logger.Error("failed to discover repositories", zap.Error(err))
		s.totalErrors.Add(1)
		return
	}

	if len(repos) == 0 {
		s.logger.Debug("no repositories found for maintenance scan")
		return
	}

	s.logger.Info("maintenance scan started",
		zap.Int("repository_count", len(repos)))

	// Use a semaphore to limit concurrent maintenance jobs.
	if maxJobs < 1 {
		maxJobs = 1
	}
	sem := make(chan struct{}, maxJobs)
	var scanWg sync.WaitGroup

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Listen for stop signal to cancel in-progress jobs.
	go func() {
		select {
		case <-s.stopChan:
			cancel()
		case <-ctx.Done():
		}
	}()

	for _, repoPath := range repos {
		// Check if we should stop.
		select {
		case <-s.stopChan:
			break
		default:
		}

		scanWg.Add(1)
		sem <- struct{}{} // Acquire semaphore slot

		go func(rp string) {
			defer scanWg.Done()
			defer func() { <-sem }() // Release semaphore slot

			s.maintainRepository(ctx, rp)
		}(repoPath)
	}

	scanWg.Wait()
	s.logger.Info("maintenance scan completed")
}

// maintainRepository analyzes and optionally maintains a single repository.
func (s *Scheduler) maintainRepository(ctx context.Context, repoPath string) {
	state, err := s.analyzer.AnalyzeRepository(ctx, repoPath)
	if err != nil {
		s.logger.Warn("failed to analyze repository",
			zap.String("repo", repoPath),
			zap.Error(err))
		s.totalErrors.Add(1)
		return
	}

	plan := s.analyzer.Plan(state)
	if !plan.NeedsMaintenance {
		s.logger.Debug("repository does not need maintenance",
			zap.String("repo", repoPath),
			zap.Int("loose_objects", state.EstimatedLooseObjects),
			zap.Int("pack_files", state.PackFileCount))
		return
	}

	// Coordinate across replicas: only one runner may repack a repo at a time
	// (Issue-5071). Acquire just before the mutating Execute so read-only
	// analysis never contends. A lock-infra error must not stall maintenance,
	// so proceed best-effort in that case.
	lock, acquired, lerr := tryLockRepo(repoPath)
	switch {
	case lerr != nil:
		s.logger.Warn("maintenance lock error, proceeding best-effort",
			zap.String("repo", repoPath), zap.Error(lerr))
	case !acquired:
		s.logger.Debug("repository maintained by another instance, skipping",
			zap.String("repo", repoPath))
		return
	default:
		defer func() { _ = lock.unlock() }()
	}

	result := s.executor.Execute(ctx, repoPath, plan)
	s.totalReposMaintained.Add(1)
	if !result.Success {
		s.totalErrors.Add(1)
	}
}

// discoverRepositories walks the repository root to find bare git repositories.
// Structure: {repoRoot}/{owner}/{repo}.git/
func (s *Scheduler) discoverRepositories() ([]string, error) {
	var repos []string

	ownerEntries, err := os.ReadDir(s.repoRoot)
	if err != nil {
		return nil, err
	}

	for _, ownerEntry := range ownerEntries {
		if !ownerEntry.IsDir() {
			continue
		}

		ownerPath := filepath.Join(s.repoRoot, ownerEntry.Name())
		repoEntries, err := os.ReadDir(ownerPath)
		if err != nil {
			s.logger.Warn("failed to read owner directory",
				zap.String("path", ownerPath),
				zap.Error(err))
			continue
		}

		for _, repoEntry := range repoEntries {
			if !repoEntry.IsDir() {
				continue
			}

			repoPath := filepath.Join(ownerPath, repoEntry.Name())

			// Verify this is actually a git repository by checking for HEAD file.
			if !isBareRepo(repoPath) {
				continue
			}

			// Only include .git directories (bare repos).
			if strings.HasSuffix(repoEntry.Name(), ".git") {
				repos = append(repos, repoPath)
			}
		}
	}

	return repos, nil
}

// isBareRepo checks if a directory looks like a bare git repository.
func isBareRepo(path string) bool {
	headPath := filepath.Join(path, "HEAD")
	_, err := os.Stat(headPath)
	return err == nil
}
