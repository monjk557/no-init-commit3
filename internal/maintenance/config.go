// Package maintenance provides intelligent Git repository maintenance
// inspired by GitHub's geometric repacking, GitLab's heuristic-based housekeeping,
// and Bitbucket's threshold-driven approach.
//
// Key design decisions (benchmarked from production git services):
//   - GitHub: geometric repacking (--geometric=2), cruft packs, multi-pack-index with bitmaps
//   - GitLab (Gitaly): heuristic-based triggers, geometric repack (since 16.0), commit-graph with changed-paths
//   - Bitbucket: threshold-based triggers (>50 packs, >6700 loose objects), 16h cooldown, 72h prune safety
//   - All major services avoid raw `git gc` in favor of targeted lower-level commands
package maintenance

import (
	"os"
	"strings"
	"time"
)

// envBool reads a boolean env var, returning def when unset/blank.
func envBool(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if v == "" {
		return def
	}
	return v == "true" || v == "1" || v == "yes" || v == "on"
}

// MaintenanceConfig controls the repository maintenance scheduler behavior.
type MaintenanceConfig struct {
	// Enabled controls whether the maintenance scheduler is active.
	Enabled bool `json:"enabled"`

	// ScanInterval is how often the scheduler scans all repositories for maintenance needs.
	// GitLab triggers after N pushes; we use a periodic scan since we serve many repos.
	ScanInterval time.Duration `json:"scan_interval"`

	// CooldownPeriod prevents re-running maintenance on the same repo within this window.
	// Bitbucket uses 16 hours; we default to 6 hours for faster convergence.
	CooldownPeriod time.Duration `json:"cooldown_period"`

	// MaxConcurrentJobs limits how many repos can be maintained simultaneously
	// to avoid CPU/IO contention during maintenance windows.
	MaxConcurrentJobs int `json:"max_concurrent_jobs"`

	// OperationTimeout is the maximum duration for a single maintenance operation
	// on one repository before it is killed.
	OperationTimeout time.Duration `json:"operation_timeout"`

	// InitialDelay is how long to wait after startup before the first scan.
	// Allows the server to stabilize before maintenance begins.
	InitialDelay time.Duration `json:"initial_delay"`

	// Thresholds control when maintenance is triggered for a repository.
	Thresholds ThresholdConfig `json:"thresholds"`

	// Repack controls the repacking strategy.
	Repack RepackConfig `json:"repack"`

	// Prune controls object pruning behavior.
	Prune PruneConfig `json:"prune"`
}

// ThresholdConfig defines heuristic thresholds that determine when maintenance is needed.
// Inspired by GitLab's opportunistic housekeeping and Bitbucket's automatic triggers.
type ThresholdConfig struct {
	// LooseObjectsThreshold triggers repacking when estimated loose objects exceed this count.
	// Git default: 6700, Bitbucket: 6700, we use 1024 for earlier maintenance.
	LooseObjectsThreshold int `json:"loose_objects_threshold"`

	// PackFilesThreshold triggers pack consolidation when pack count exceeds this.
	// Git default: 50, Bitbucket: 50, we use 30 for geometric repacking efficiency.
	PackFilesThreshold int `json:"pack_files_threshold"`

	// LooseRefsThreshold triggers pack-refs when loose ref count exceeds this.
	// Higher threshold than objects since pack-refs is relatively cheap.
	LooseRefsThreshold int `json:"loose_refs_threshold"`

	// CommitGraphStaleness triggers commit-graph rewrite if the existing graph
	// is older than this duration. 0 means always rewrite when other maintenance runs.
	CommitGraphStaleness time.Duration `json:"commit_graph_staleness"`
}

// RepackConfig controls how repacking is performed.
type RepackConfig struct {
	// GeometricFactor controls the geometric progression factor for pack merging.
	// GitHub and GitLab both use 2 (doubling). Each successive pack must have
	// at least GeometricFactor times the objects of the previous pack.
	GeometricFactor int `json:"geometric_factor"`

	// WriteBitmap enables multi-pack-index bitmap writing for faster fetch operations.
	// GitHub pioneered this with multi-pack bitmaps.
	WriteBitmap bool `json:"write_bitmap"`

	// WriteMultiPackIndex enables MIDX for spanning multiple pack files.
	WriteMultiPackIndex bool `json:"write_multi_pack_index"`

	// UseCruftPacks stores unreachable objects in cruft packs instead of loose files.
	// Introduced by GitHub in git 2.37 to avoid loose object explosion during gc.
	UseCruftPacks bool `json:"use_cruft_packs"`

	// AtomicBitmap writes the multi-pack-index and its reachability bitmap as
	// part of the geometric repack itself (git repack --write-midx
	// --write-bitmap-index) instead of a separate `multi-pack-index write
	// --bitmap` step. The separate step regularly fails with "Packfile doesn't
	// have full closure (object … is missing)" on busy repos because it sees a
	// pack set that changed since the repack; writing the bitmap atomically
	// with the repack computes closure over the pack set repack just produced.
	// Without a working bitmap, upload-pack cannot reuse existing pack bytes
	// and every clone re-traverses + re-deltas the whole object graph — the
	// dominant per-clone memory cost behind pod OOMs (Issue-5071). Default ON
	// (the separate step is broken); when true the separate MIDX step is
	// skipped. GIT_MAINTENANCE_ATOMIC_BITMAP=false disables it as an escape hatch.
	AtomicBitmap bool `json:"atomic_bitmap"`

	// BitmapFsckDiagnostic runs `git fsck --connectivity-only` and logs the
	// missing/broken objects when a bitmap write fails with a closure error.
	// Read-only; surfaces the root cause into logs when direct pod exec is not
	// available. Enabled by default.
	BitmapFsckDiagnostic bool `json:"bitmap_fsck_diagnostic"`
}

// PruneConfig controls pruning of unreachable objects.
type PruneConfig struct {
	// ExpireAge is the minimum age of unreachable objects before they can be pruned.
	// Bitbucket uses 72 hours. Git default is 2 weeks. We use 72 hours matching Bitbucket
	// since server-side repos have more predictable access patterns.
	ExpireAge time.Duration `json:"expire_age"`

	// CruftExpireAge is the expiration for objects in cruft packs.
	// Only used when RepackConfig.UseCruftPacks is true.
	CruftExpireAge time.Duration `json:"cruft_expire_age"`
}

// DefaultMaintenanceConfig returns production-ready defaults derived from
// benchmarking GitHub, GitLab, Gitea, and Bitbucket strategies.
func DefaultMaintenanceConfig() *MaintenanceConfig {
	return &MaintenanceConfig{
		Enabled:      true,
		ScanInterval: 1 * time.Hour,
		// CooldownPeriod raised 6h→16h (Bitbucket's value, Issue-5414). Now that
		// atomic bitmap writes land reliably (Issue-5273/5414), every repack
		// produces a full-coverage bitmap, so the stale-bitmap re-trigger fires
		// far less and there is no need to repack a repo as eagerly. A longer
		// cooldown cuts repack frequency — and the unbounded-memory spark each
		// repack used to cause — while bitmap freshness stays well within the
		// window an actively-pushed repo tolerates.
		CooldownPeriod:    16 * time.Hour,
		MaxConcurrentJobs: 2,
		OperationTimeout:  30 * time.Minute,
		InitialDelay:      30 * time.Second,
		Thresholds: ThresholdConfig{
			LooseObjectsThreshold: 1024,
			// PackFilesThreshold raised 30→50 (git/Bitbucket default, Issue-5414):
			// with reliable bitmaps, letting a few more packs accumulate before a
			// geometric repack is cheap for clones (the MIDX bitmap still spans
			// them) but meaningfully reduces repack frequency and its memory spike.
			PackFilesThreshold:   50,
			LooseRefsThreshold:   512,
			CommitGraphStaleness: 24 * time.Hour,
		},
		Repack: RepackConfig{
			GeometricFactor:     2,
			WriteBitmap:         true,
			WriteMultiPackIndex: true,
			UseCruftPacks:       true,
			// Default ON: the separate `multi-pack-index write --bitmap` step is
			// demonstrably broken on busy repos (a snapshot race — fsck reports no
			// missing objects), so it writes no bitmaps and every clone falls back
			// to the expensive full path. Atomic writing is strictly better, not a
			// tunable feature; the env is only an emergency off-switch
			// (GIT_MAINTENANCE_ATOMIC_BITMAP=false). See Issue-5071.
			AtomicBitmap:         envBool("GIT_MAINTENANCE_ATOMIC_BITMAP", true),
			BitmapFsckDiagnostic: envBool("GIT_MAINTENANCE_BITMAP_FSCK_DIAGNOSTIC", true),
		},
		Prune: PruneConfig{
			ExpireAge:      72 * time.Hour,
			CruftExpireAge: 72 * time.Hour,
		},
	}
}
