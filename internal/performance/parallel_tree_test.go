package performance

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewParallelTreeProcessor_WorkerBounds(t *testing.T) {
	ptp := NewParallelTreeProcessor(testLogger())
	wc := ptp.GetOptimalWorkerCount()
	assert.GreaterOrEqual(t, wc, 2)
	assert.LessOrEqual(t, wc, 8)
}

func TestParallelTreeProcessor_ProcessTreeForLanguages(t *testing.T) {
	tree := newTreeWithFiles(t, map[string]string{
		"main.go":   "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n",
		"util.go":   "package main\n\nfunc helper() int { return 42 }\n",
		"script.py": "def add(a, b):\n    return a + b\n\nprint(add(1, 2))\n",
	})

	ptp := NewParallelTreeProcessor(testLogger())
	stats, totalBytes, totalFiles, err := ptp.ProcessTreeForLanguages(context.Background(), tree)
	require.NoError(t, err)

	assert.Greater(t, totalFiles, 0)
	assert.Greater(t, totalBytes, int64(0))
	require.NotEmpty(t, stats)

	// Percentages should sum to ~100.
	var sum float64
	for _, s := range stats {
		sum += s.Percentage
		assert.Greater(t, s.Bytes, int64(0))
	}
	assert.InDelta(t, 100.0, sum, 0.01)
}

func TestParallelTreeProcessor_SkipsLargeFiles(t *testing.T) {
	// A file larger than LanguageStatsMaxFileSize (1MB) must be skipped.
	bigContent := "package main\n" + strings.Repeat("// padding line\n", (1024*1024)/16+10)
	require.Greater(t, int64(len(bigContent)), LanguageStatsMaxFileSize)

	tree := newTreeWithFiles(t, map[string]string{
		"small.go": "package main\n\nfunc main() {}\n",
		"huge.go":  bigContent,
	})

	ptp := NewParallelTreeProcessor(testLogger())
	stats, totalBytes, totalFiles, err := ptp.ProcessTreeForLanguages(context.Background(), tree)
	require.NoError(t, err)

	// Only the small file is counted.
	assert.Equal(t, 1, totalFiles)
	assert.Less(t, totalBytes, LanguageStatsMaxFileSize)
	require.NotEmpty(t, stats)
}

func TestParallelTreeProcessor_ContextCancelled(t *testing.T) {
	tree := newTreeWithFiles(t, map[string]string{
		"main.go": "package main\nfunc main() {}\n",
	})

	ptp := NewParallelTreeProcessor(testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	// Should return without hanging; results may be empty.
	_, _, _, err := ptp.ProcessTreeForLanguages(ctx, tree)
	require.NoError(t, err)
}
