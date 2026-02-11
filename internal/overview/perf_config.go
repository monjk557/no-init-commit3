package overview

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// No feature flags: production behavior should be stable.
// These are tuning knobs only.

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
