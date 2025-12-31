package overview

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTTLLRUCache_GetSet_Hit(t *testing.T) {
	c := newTTLLRUCache[string](10, 200*time.Millisecond)
	c.now = time.Now

	c.Set("k", "v")
	v, ok := c.Get("k")

	require.True(t, ok)
	require.Equal(t, "v", v)
}

func TestTTLLRUCache_GetSet_Expire(t *testing.T) {
	c := newTTLLRUCache[string](10, 30*time.Millisecond)
	c.now = time.Now

	c.Set("k", "v")
	time.Sleep(60 * time.Millisecond)
	_, ok := c.Get("k")

	require.False(t, ok)
}

func TestOverviewCache_CommitCount_CachesByKey(t *testing.T) {
	oc := NewOverviewCache(10, 500*time.Millisecond)

	oc.SetCommitCount("/repo", "hash", 123)
	v, ok := oc.GetCommitCount("/repo", "hash")

	require.True(t, ok)
	require.Equal(t, 123, v)
}
