package overview

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Languages cache
// ---------------------------------------------------------------------------

func Test_OverviewCache_Languages_SetAndGet(t *testing.T) {
	t.Parallel()
	oc := NewOverviewCache(10, 30*time.Second)

	dist := LanguageDistribution{
		TotalBytes: 100,
		TotalFiles: 2,
		Languages:  []LanguageStats{{Language: "Go", Bytes: 100, Files: 2, Percentage: 100}},
	}
	oc.SetLanguages("/repo", "ref1", dist)

	got, ok := oc.GetLanguages("/repo", "ref1")
	require.True(t, ok)
	assert.Equal(t, int64(100), got.TotalBytes)
	assert.Equal(t, "Go", got.Languages[0].Language)
}

func Test_OverviewCache_Languages_Miss(t *testing.T) {
	t.Parallel()
	oc := NewOverviewCache(10, 30*time.Second)

	_, ok := oc.GetLanguages("/repo", "missing")
	assert.False(t, ok)
}

// ---------------------------------------------------------------------------
// TreeEntries cache
// ---------------------------------------------------------------------------

func Test_OverviewCache_TreeEntries_SetAndGet(t *testing.T) {
	t.Parallel()
	oc := NewOverviewCache(10, 30*time.Second)

	entries := []GitTreeEntryWithCommit{
		{Type: "blob", Name: "a.go", Path: "a.go"},
		{Type: "tree", Name: "src", Path: "src"},
	}
	oc.SetTreeEntries("/repo", "ref1", "", entries)

	got, ok := oc.GetTreeEntries("/repo", "ref1", "")
	require.True(t, ok)
	require.Len(t, got, 2)
	assert.Equal(t, "a.go", got[0].Name)
}

func Test_OverviewCache_TreeEntries_DifferentPath_Miss(t *testing.T) {
	t.Parallel()
	oc := NewOverviewCache(10, 30*time.Second)

	oc.SetTreeEntries("/repo", "ref1", "", []GitTreeEntryWithCommit{{Name: "x"}})

	_, ok := oc.GetTreeEntries("/repo", "ref1", "src")
	assert.False(t, ok)
}

// ---------------------------------------------------------------------------
// LatestCommitsBatch (complete) cache
// ---------------------------------------------------------------------------

func Test_OverviewCache_LatestCommitsBatch_SetAndGet(t *testing.T) {
	t.Parallel()
	oc := NewOverviewCache(10, 30*time.Second)

	commits := map[string]*GitCommit{"a.go": {SHA: "abc"}}
	oc.SetLatestCommitsBatch("/repo", "ref1", "key1", commits)

	got, ok := oc.GetLatestCommitsBatch("/repo", "ref1", "key1")
	require.True(t, ok)
	assert.Equal(t, "abc", got["a.go"].SHA)
}

func Test_OverviewCache_GetLatestCommitsBatchAny_NotFound(t *testing.T) {
	t.Parallel()
	oc := NewOverviewCache(10, 30*time.Second)

	_, _, found := oc.GetLatestCommitsBatchAny("/repo", "ref1", "nope")
	assert.False(t, found)
}

// ---------------------------------------------------------------------------
// Do (singleflight)
// ---------------------------------------------------------------------------

func Test_OverviewCache_Do_ExecutesFn(t *testing.T) {
	t.Parallel()
	oc := NewOverviewCache(10, 30*time.Second)

	v, err, _ := oc.Do("k", func() (any, error) {
		return 42, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 42, v)
}

func Test_OverviewCache_Do_PropagatesError(t *testing.T) {
	t.Parallel()
	oc := NewOverviewCache(10, 30*time.Second)

	sentinel := errors.New("boom")
	_, err, _ := oc.Do("k2", func() (any, error) {
		return nil, sentinel
	})
	assert.ErrorIs(t, err, sentinel)
}

func Test_OverviewCache_Do_NilCache_StillExecutes(t *testing.T) {
	t.Parallel()
	var oc *OverviewCache // nil receiver

	v, err, shared := oc.Do("k", func() (any, error) {
		return "ok", nil
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", v)
	assert.False(t, shared)
}

// ---------------------------------------------------------------------------
// ttlLRUCache: disabled / Len edge cases
// ---------------------------------------------------------------------------

func Test_TTLLRUCache_Disabled_WhenZeroMaxEntries(t *testing.T) {
	t.Parallel()
	c := newTTLLRUCache[int](0, time.Second)

	c.Set("k", 1)
	_, ok := c.Get("k")
	assert.False(t, ok)
	assert.Equal(t, 0, c.Len())
}

func Test_TTLLRUCache_NilReceiver(t *testing.T) {
	t.Parallel()
	var c *ttlLRUCache[int]

	_, ok := c.Get("k")
	assert.False(t, ok)
	c.Set("k", 1) // no panic
	assert.Equal(t, 0, c.Len())
}

func Test_TTLLRUCache_Len_CountsEntries(t *testing.T) {
	t.Parallel()
	c := newTTLLRUCache[int](10, time.Second)

	c.Set("a", 1)
	c.Set("b", 2)
	assert.Equal(t, 2, c.Len())
}
