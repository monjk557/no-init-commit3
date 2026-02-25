package overview

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// No feature flags: production behavior should be stable.
// These are tuning knobs only.

// latestCommitsTimeBudget returns the time budget for the batch endpoint.
func latestCommitsTimeBudget() time.Duration {
	ms := 300
	if v := strings.TrimSpace(os.Getenv("OVERVIEW_LATEST_COMMITS_TIME_BUDGET_MS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			ms = n
		}
	}
	return time.Duration(ms) * time.Millisecond
}

func latestCommitsMaxCommits() int {
	max := 10_000
	if v := strings.TrimSpace(os.Getenv("OVERVIEW_LATEST_COMMITS_MAX_COMMITS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			max = n
		}
	}
	return max
}

// timeBudgetForEntryCount returns an N-based (tree entry count) time budget.
// Must be long enough for at least one DiffTree + several commit scans so that
// more than one path gets LatestCommit filled (avoids "only one latest commit").
// Override with OVERVIEW_TREE_COMMITS_TIME_BUDGET_MS env.
//
// Bucket rules:
//
//	N <=  50  → 5s   (small dir, aim for full resolve)
//	N <= 200  → 4s
//	N <= 500  → 3s
//	N <= 1000 → 2s
//	N > 1000  → 1.5s (huge dir, partial still possible)
func timeBudgetForEntryCount(n int) time.Duration {
	// Allow a global override for ops tuning.
	if v := strings.TrimSpace(os.Getenv("OVERVIEW_TREE_COMMITS_TIME_BUDGET_MS")); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}

	switch {
	case n <= 50:
		return 5 * time.Second
	case n <= 200:
		return 4 * time.Second
	case n <= 500:
		return 3 * time.Second
	case n <= 1000:
		return 2 * time.Second
	default:
		return 1500 * time.Millisecond
	}
}

// maxCommitsForEntryCount returns an N-based max-commits cap.
// Override with OVERVIEW_TREE_COMMITS_MAX_COMMITS env.
func maxCommitsForEntryCount(n int) int {
	if v := strings.TrimSpace(os.Getenv("OVERVIEW_TREE_COMMITS_MAX_COMMITS")); v != "" {
		if mc, err := strconv.Atoi(v); err == nil && mc > 0 {
			return mc
		}
	}

	switch {
	case n <= 50:
		return 20_000
	case n <= 200:
		return 15_000
	case n <= 500:
		return 10_000
	default:
		return 5_000
	}
}

// blobSizeParallelism returns worker concurrency for BlobObject size fill.
// 1 means sequential.
func blobSizeParallelism() int {
	n := 1
	if v := strings.TrimSpace(os.Getenv("OVERVIEW_BLOB_SIZE_PARALLELISM")); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			n = parsed
		}
	}
	return n
}
