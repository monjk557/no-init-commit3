package performance

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMultiTierCache_RecordL2Hit covers the recordL2Hit accounting helper,
// which the GetRepository path cannot reach because deserializeRepository is a
// nil-returning placeholder.
func TestMultiTierCache_RecordL2Hit(t *testing.T) {
	mtc := NewMultiTierCache(newFakeL1(), nil, testMultiTierConfig(), testLogger())
	defer mtc.Close()

	mtc.recordL2Hit()
	mtc.recordL2Hit()

	assert.Equal(t, int64(2), mtc.GetStats().L2Hits)
}

// TestMultiTierCache_GetRepository_L2Lookup drives the L2 branch of
// GetRepository: L1 misses, L2 returns bytes for the key, deserialize returns
// the nil placeholder, so the call falls through to an L2 miss.
func TestMultiTierCache_GetRepository_L2Lookup(t *testing.T) {
	l1 := newFakeL1()
	l2 := newFakeL2()
	mtc := NewMultiTierCache(l1, l2, testMultiTierConfig(), testLogger())
	defer mtc.Close()

	ctx := context.Background()
	key := mtc.getL2Key("owner/repo")
	// Seed L2 so the Get returns non-empty data and enters the deserialize path.
	require.NoError(t, l2.Set(ctx, key, []byte(`{"cached_at":1}`), time.Minute))

	_, found := mtc.GetRepository(ctx, "owner/repo")
	// deserializeRepository returns nil by design -> not found, recorded as L2 miss.
	assert.False(t, found)
	assert.GreaterOrEqual(t, mtc.GetStats().L2Misses, int64(1))
}
