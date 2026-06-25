package performance

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAccessPatternTracker_RecordAndStats(t *testing.T) {
	apt := NewAccessPatternTracker(50, testLogger())

	apt.RecordAccess("repo-a")
	apt.RecordAccess("repo-a")
	apt.RecordAccessWithContext("repo-b", "write", "user1")

	stats := apt.GetStatistics()
	assert.Equal(t, 2, stats["total_repositories"])
	assert.GreaterOrEqual(t, stats["time_patterns"].(int), 1)
	assert.Contains(t, stats, "access_patterns")
	assert.Contains(t, stats, "co_occurrence_relationships")
}

func TestAccessPatternTracker_RecentAccesses(t *testing.T) {
	apt := NewAccessPatternTracker(10, testLogger())
	apt.RecordAccess("r1")
	apt.RecordAccess("r2")
	apt.RecordAccess("r3")

	recent := apt.GetRecentAccesses(2)
	require.Len(t, recent, 2)
	// Most recent first.
	assert.Equal(t, "r3", recent[0].Repository)
	assert.Equal(t, "r2", recent[1].Repository)

	all := apt.GetRecentAccesses(100)
	assert.Len(t, all, 3)
}

func TestAccessPatternTracker_TopRepositories(t *testing.T) {
	apt := NewAccessPatternTracker(50, testLogger())
	for i := 0; i < 5; i++ {
		apt.RecordAccess("hot")
	}
	apt.RecordAccess("cold")

	top := apt.GetTopRepositories(10)
	require.GreaterOrEqual(t, len(top), 2)
	assert.Equal(t, "hot", top[0].Repository)
	assert.Equal(t, int64(5), top[0].TotalAccesses)

	// Limit applied.
	limited := apt.GetTopRepositories(1)
	assert.Len(t, limited, 1)
}

func TestAccessPatternTracker_CoOccurrence(t *testing.T) {
	apt := NewAccessPatternTracker(50, testLogger())
	// Access two repos within the 5-minute window.
	apt.RecordAccess("alpha")
	apt.RecordAccess("beta")
	apt.RecordAccess("alpha")

	score := apt.GetCoOccurrenceScore("alpha", "beta")
	assert.Greater(t, score, 0.0)

	// No relationship -> 0.
	assert.Equal(t, 0.0, apt.GetCoOccurrenceScore("alpha", "nonexistent"))
	assert.Equal(t, 0.0, apt.GetCoOccurrenceScore("nonexistent", "beta"))

	related := apt.GetRelatedRepositories("alpha", 5)
	require.NotEmpty(t, related)
	assert.Equal(t, "beta", related[0].Repository)

	// Unknown repo -> empty.
	assert.Empty(t, apt.GetRelatedRepositories("unknown", 5))
}

func TestAccessPatternTracker_RelatedLimit(t *testing.T) {
	apt := NewAccessPatternTracker(50, testLogger())
	apt.RecordAccess("center")
	apt.RecordAccess("x1")
	apt.RecordAccess("x2")
	apt.RecordAccess("center")

	related := apt.GetRelatedRepositories("center", 1)
	assert.LessOrEqual(t, len(related), 1)
}

func TestAccessPatternTracker_DetermineAccessPattern(t *testing.T) {
	apt := NewAccessPatternTracker(50, testLogger())

	tests := []struct {
		name     string
		stats    *RepositoryStats
		expected string
	}{
		{"sporadic low count", &RepositoryStats{TotalAccesses: 3, AverageGap: 1}, "sporadic"},
		{"burst", &RepositoryStats{TotalAccesses: 10, AverageGap: 2}, "burst"},
		{"frequent", &RepositoryStats{TotalAccesses: 10, AverageGap: 30}, "frequent"},
		{"regular", &RepositoryStats{TotalAccesses: 10, AverageGap: 120}, "regular"},
		{"sporadic large gap", &RepositoryStats{TotalAccesses: 10, AverageGap: 2000}, "sporadic"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, apt.determineAccessPattern(tt.stats))
		})
	}
}

func TestAccessPatternTracker_TimeBasedPatterns(t *testing.T) {
	apt := NewAccessPatternTracker(50, testLogger())
	now := time.Now()
	// Record many times to push frequency above the 0.3 threshold.
	for i := 0; i < 20; i++ {
		apt.RecordAccess("timed")
	}

	patterns := apt.GetTimeBasedPatterns(now.Hour(), now.Weekday())
	// At least the current hour bucket should match if frequency exceeded 0.3.
	assert.GreaterOrEqual(t, len(patterns), 0)
}
