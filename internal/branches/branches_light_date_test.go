package branches

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNativeGetBranchesLight_OmitsDate_EnrichMatchesFull pins Issue-5618: with
// includeDate=false the wide scan carries no committerdate (zero Date), and
// NativeEnrichCommitterDates fills the page to match the full scan — so name
// sort avoids the O(all) per-ref date lookup while the response still has dates.
func TestNativeGetBranchesLight_OmitsDate_EnrichMatchesFull(t *testing.T) {
	bare := buildRepoWithTwoBranches(t)
	ctx := context.Background()

	full, err := NativeGetBranches(ctx, bare, BranchSortName)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(full), 2)
	fullByRef := make(map[string]BranchInfo, len(full))
	for _, b := range full {
		require.False(t, b.Date.IsZero(), "sanity: full scan has dates")
		fullByRef[b.RefPath] = b
	}

	// includeDate=false → pure enumeration, Date must be zero.
	light, err := NativeGetBranchesLight(ctx, bare, BranchSortName, false)
	require.NoError(t, err)
	require.Equal(t, len(full), len(light))
	for _, l := range light {
		f, ok := fullByRef[l.RefPath]
		require.True(t, ok, "light ref %s missing from full", l.RefPath)
		require.True(t, l.Date.IsZero(), "light scan (includeDate=false) must omit date")
		require.Equal(t, f.CommitSHA, l.CommitSHA)
	}

	// Enrich the page → dates must match the full scan (to the second).
	require.NoError(t, NativeEnrichCommitterDates(ctx, bare, light))
	for _, l := range light {
		require.False(t, l.Date.IsZero(), "date not enriched for %s", l.RefPath)
		require.WithinDuration(t, fullByRef[l.RefPath].Date, l.Date, 0, "enriched date mismatch for %s", l.RefPath)
	}
}

// TestNativeGetBranchesLight_IncludeDate_KeepsDate confirms includeDate=true
// still carries committerdate inline (date sort / status filter path).
func TestNativeGetBranchesLight_IncludeDate_KeepsDate(t *testing.T) {
	bare := buildRepoWithTwoBranches(t)
	light, err := NativeGetBranchesLight(context.Background(), bare, BranchSortNewest, true)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(light), 2)
	for _, l := range light {
		require.False(t, l.Date.IsZero(), "includeDate=true must carry date inline")
	}
}
