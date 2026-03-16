package maintenance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RepoState captures the current state of a repository's object database
// to determine what maintenance tasks are needed.
type RepoState struct {
	// RepoPath is the absolute path to the bare repository (.git directory).
	RepoPath string

	// EstimatedLooseObjects is the estimated count of loose objects.
	// Estimated by sampling objects/17/ directory and multiplying by 256,
	// matching the approach used by git gc --auto and Bitbucket.
	EstimatedLooseObjects int

	// PackFileCount is the number of .pack files in objects/pack/.
	PackFileCount int

	// LooseRefCount is the number of loose ref files under refs/.
	LooseRefCount int

	// HasCommitGraph indicates whether a commit-graph file exists.
	HasCommitGraph bool

	// CommitGraphAge is how old the commit-graph file is.
	CommitGraphAge time.Duration

	// LastMaintenanceAt records when maintenance was last performed (from marker file).
	LastMaintenanceAt time.Time

	// TotalPackSizeBytes is the combined size of all pack files.
	TotalPackSizeBytes int64

	// HasRefs indicates whether the repo has any refs (branches/tags).
	// Empty repos (no commits) don't need commit-graph or repack.
	HasRefs bool
}

// MaintenancePlan describes what maintenance tasks should be performed.
type MaintenancePlan struct {
	// NeedsMaintenance is true if any maintenance task is recommended.
	NeedsMaintenance bool

	// Tasks lists the maintenance tasks to run in order.
	Tasks []MaintenanceTask

	// Reason provides a human-readable explanation of why maintenance is needed.
	Reasons []string
}

// MaintenanceTask represents a specific maintenance operation.
type MaintenanceTask int

const (
	// TaskPackRefs consolidates loose refs into packed-refs.
	TaskPackRefs MaintenanceTask = iota

	// TaskGeometricRepack runs geometric repacking with cruft pack support.
	TaskGeometricRepack

	// TaskCommitGraph writes or updates the commit-graph.
	TaskCommitGraph

	// TaskMultiPackIndex writes or updates the multi-pack-index with bitmaps.
	TaskMultiPackIndex

	// TaskPrune prunes unreachable loose objects older than the expire threshold.
	TaskPrune
)

// String returns the task name for logging.
func (t MaintenanceTask) String() string {
	switch t {
	case TaskPackRefs:
		return "pack-refs"
	case TaskGeometricRepack:
		return "geometric-repack"
	case TaskCommitGraph:
		return "commit-graph"
	case TaskMultiPackIndex:
		return "multi-pack-index"
	case TaskPrune:
		return "prune"
	default:
		return "unknown"
	}
}

// Analyzer inspects repository state and determines what maintenance is needed.
type Analyzer struct {
	config *MaintenanceConfig
}

// NewAnalyzer creates a new maintenance analyzer.
func NewAnalyzer(config *MaintenanceConfig) *Analyzer {
	return &Analyzer{config: config}
}

// AnalyzeRepository inspects a repository and returns its current state.
func (a *Analyzer) AnalyzeRepository(_ context.Context, repoPath string) (*RepoState, error) {
	state := &RepoState{RepoPath: repoPath}

	// Estimate loose objects by sampling objects/17/ directory (git gc --auto technique).
	// Bitbucket and git itself use this: count entries in objects/17/ and multiply by 256.
	sampleDir := filepath.Join(repoPath, "objects", "17")
	if entries, err := os.ReadDir(sampleDir); err == nil {
		state.EstimatedLooseObjects = len(entries) * 256
	}

	// Count pack files in objects/pack/.
	packDir := filepath.Join(repoPath, "objects", "pack")
	if entries, err := os.ReadDir(packDir); err == nil {
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".pack") {
				state.PackFileCount++
				if info, err := entry.Info(); err == nil {
					state.TotalPackSizeBytes += info.Size()
				}
			}
		}
	}

	// Count loose refs under refs/ (excluding packed-refs).
	state.LooseRefCount = countLooseRefs(filepath.Join(repoPath, "refs"))

	// Check commit-graph existence and age.
	commitGraphPath := filepath.Join(repoPath, "objects", "info", "commit-graphs")
	if info, err := os.Stat(commitGraphPath); err == nil {
		state.HasCommitGraph = true
		state.CommitGraphAge = time.Since(info.ModTime())
	} else {
		// Also check single-file commit-graph (older format).
		singleGraphPath := filepath.Join(repoPath, "objects", "info", "commit-graph")
		if info, err := os.Stat(singleGraphPath); err == nil {
			state.HasCommitGraph = true
			state.CommitGraphAge = time.Since(info.ModTime())
		}
	}

	// Check if the repo has any refs (packed-refs or loose refs).
	packedRefsPath := filepath.Join(repoPath, "packed-refs")
	if _, err := os.Stat(packedRefsPath); err == nil {
		state.HasRefs = true
	}
	if state.LooseRefCount > 0 {
		state.HasRefs = true
	}

	// Read last maintenance timestamp from marker file.
	markerPath := maintenanceMarkerPath(repoPath)
	if info, err := os.Stat(markerPath); err == nil {
		state.LastMaintenanceAt = info.ModTime()
	}

	return state, nil
}

// Plan determines what maintenance tasks to run based on the repository state.
// This is the core heuristic engine, inspired by GitLab's opportunistic housekeeping.
func (a *Analyzer) Plan(state *RepoState) *MaintenancePlan {
	plan := &MaintenancePlan{}

	// Check cooldown period (Bitbucket uses 16h, we use configurable).
	if !state.LastMaintenanceAt.IsZero() &&
		time.Since(state.LastMaintenanceAt) < a.config.CooldownPeriod {
		return plan // Still in cooldown
	}

	// 1. Pack refs if too many loose refs exist.
	// GitLab checks this with a scaling heuristic; we use a simple threshold.
	if state.LooseRefCount > a.config.Thresholds.LooseRefsThreshold {
		plan.Tasks = append(plan.Tasks, TaskPackRefs)
		plan.Reasons = append(plan.Reasons,
			fmt.Sprintf("loose refs (%d) exceed threshold (%d)",
				state.LooseRefCount, a.config.Thresholds.LooseRefsThreshold))
	}

	// 2. Geometric repack if too many loose objects or too many pack files.
	// This is the core maintenance task used by GitHub and GitLab (since 16.0).
	needsRepack := false
	if state.EstimatedLooseObjects > a.config.Thresholds.LooseObjectsThreshold {
		needsRepack = true
		plan.Reasons = append(plan.Reasons,
			fmt.Sprintf("estimated loose objects (%d) exceed threshold (%d)",
				state.EstimatedLooseObjects, a.config.Thresholds.LooseObjectsThreshold))
	}
	if state.PackFileCount > a.config.Thresholds.PackFilesThreshold {
		needsRepack = true
		plan.Reasons = append(plan.Reasons,
			fmt.Sprintf("pack files (%d) exceed threshold (%d)",
				state.PackFileCount, a.config.Thresholds.PackFilesThreshold))
	}
	if needsRepack {
		plan.Tasks = append(plan.Tasks, TaskGeometricRepack)
	}

	// 3. Commit-graph write/update if stale or missing.
	// Both GitHub and GitLab maintain commit-graphs for fast traversal.
	// Skip for repos with no refs (empty repos have nothing to graph).
	needsCommitGraph := false
	if !state.HasCommitGraph && state.HasRefs {
		needsCommitGraph = true
		plan.Reasons = append(plan.Reasons, "commit-graph does not exist")
	} else if a.config.Thresholds.CommitGraphStaleness > 0 &&
		state.CommitGraphAge > a.config.Thresholds.CommitGraphStaleness {
		needsCommitGraph = true
		plan.Reasons = append(plan.Reasons,
			fmt.Sprintf("commit-graph is stale (age: %s, threshold: %s)",
				state.CommitGraphAge.Round(time.Minute),
				a.config.Thresholds.CommitGraphStaleness))
	}
	if needsCommitGraph {
		plan.Tasks = append(plan.Tasks, TaskCommitGraph)
	}

	// 4. Multi-pack-index after repack if enabled and multiple packs exist.
	if a.config.Repack.WriteMultiPackIndex && (needsRepack || state.PackFileCount > 1) {
		plan.Tasks = append(plan.Tasks, TaskMultiPackIndex)
	}

	// 5. Prune unreachable loose objects if not using cruft packs
	// (cruft packs handle this during repack).
	if !a.config.Repack.UseCruftPacks &&
		state.EstimatedLooseObjects > a.config.Thresholds.LooseObjectsThreshold {
		plan.Tasks = append(plan.Tasks, TaskPrune)
		plan.Reasons = append(plan.Reasons, "prune unreachable loose objects (non-cruft mode)")
	}

	plan.NeedsMaintenance = len(plan.Tasks) > 0
	return plan
}

// countLooseRefs recursively counts files under a refs directory.
func countLooseRefs(refsDir string) int {
	count := 0
	_ = filepath.WalkDir(refsDir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // Skip unreadable entries
		}
		if !d.IsDir() {
			count++
		}
		return nil
	})
	return count
}

// maintenanceMarkerPath returns the path to the maintenance timestamp marker file.
func maintenanceMarkerPath(repoPath string) string {
	return filepath.Join(repoPath, ".maintenance-last-run")
}
