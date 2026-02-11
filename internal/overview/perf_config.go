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
// Larger directories get shorter budgets to ensure UI consistency.
//
// Bucket rules (override with OVERVIEW_TREE_COMMITS_TIME_BUDGET_MS env):
//
//	N <=  50  → 500ms  (small dir, almost always complete)
//	N <= 200  → 400ms
//	N <= 500  → 300ms
//	N <= 1000 → 200ms
//	N > 1000  → 150ms  (huge dir, aggressive partial)
func timeBudgetForEntryCount(n int) time.Duration {
	// Allow a global override for ops tuning.
	if v := strings.TrimSpace(os.Getenv("OVERVIEW_TREE_COMMITS_TIME_BUDGET_MS")); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}

	switch {
	case n <= 50:
		return 500 * time.Millisecond
	case n <= 200:
		return 400 * time.Millisecond
	case n <= 500:
		return 300 * time.Millisecond
	case n <= 1000:
		return 200 * time.Millisecond
	default:
		return 150 * time.Millisecond
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
