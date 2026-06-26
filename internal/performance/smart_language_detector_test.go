package performance

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSmartLanguageDetector(t *testing.T) {
	sld := NewSmartLanguageDetector(testLogger())
	assert.Equal(t, getOptimalWorkerCount(), sld.workerCount)
	assert.Equal(t, LanguageStatsMaxFileSize, sld.maxFileSize)
	assert.Equal(t, 8*1024, sld.sampleSize)
}

func TestGetOptimalWorkerCount(t *testing.T) {
	assert.Equal(t, 12, getOptimalWorkerCount())
}

func TestSmartLanguageDetector_ProcessTreeWithSampling(t *testing.T) {
	tree := newTreeWithFiles(t, map[string]string{
		"main.go":  "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n",
		"app.py":   "import sys\n\ndef run():\n    print('run')\n",
		"index.js": "function greet(name) {\n  return 'hi ' + name;\n}\n",
	})

	sld := NewSmartLanguageDetector(testLogger())
	stats, totalBytes, totalFiles, err := sld.ProcessTreeWithSampling(context.Background(), tree)
	require.NoError(t, err)

	assert.Greater(t, totalFiles, 0)
	assert.Greater(t, totalBytes, int64(0))
	require.NotEmpty(t, stats)
}

func TestSmartLanguageDetector_SkipsLargeFiles(t *testing.T) {
	bigContent := "package main\n" + strings.Repeat("// pad\n", (1024*1024)/7+10)
	require.Greater(t, int64(len(bigContent)), LanguageStatsMaxFileSize)

	tree := newTreeWithFiles(t, map[string]string{
		"small.go": "package main\nfunc main() {}\n",
		"huge.go":  bigContent,
	})

	sld := NewSmartLanguageDetector(testLogger())
	_, totalBytes, totalFiles, err := sld.ProcessTreeWithSampling(context.Background(), tree)
	require.NoError(t, err)

	assert.Equal(t, 1, totalFiles)
	assert.Less(t, totalBytes, LanguageStatsMaxFileSize)
}

func TestSmartLanguageDetector_DetectByExtension(t *testing.T) {
	sld := NewSmartLanguageDetector(testLogger())

	// enry.GetLanguageByFilename resolves unambiguous full filenames (not bare
	// extensions, which are often shared across languages).
	lang, ok := sld.detectByExtension("Dockerfile")
	assert.True(t, ok)
	assert.Equal(t, "Dockerfile", lang)

	_, ok = sld.detectByExtension("noextension")
	assert.False(t, ok)
}

func TestSmartLanguageDetector_CalculateConfidence(t *testing.T) {
	sld := NewSmartLanguageDetector(testLogger())

	// Full read.
	assert.Equal(t, 1.0, sld.calculateConfidence(100, 100))
	assert.Equal(t, 1.0, sld.calculateConfidence(200, 100))

	// Sampled >= 50%.
	assert.Equal(t, 0.95, sld.calculateConfidence(6000, 10000))

	// Sampled >= 10% (between 0.8-0.95).
	c := sld.calculateConfidence(2000, 10000)
	assert.Greater(t, c, 0.8)
	assert.Less(t, c, 0.95)

	// Sampled < 10% but >= minConfidenceSize (1KB).
	c2 := sld.calculateConfidence(1100, 100000)
	assert.GreaterOrEqual(t, c2, 0.6)
	assert.Less(t, c2, 0.8)

	// Very small sample -> low confidence.
	assert.Equal(t, 0.4, sld.calculateConfidence(100, 100000))
}

func TestSmartLanguageDetector_ContextCancelled(t *testing.T) {
	tree := newTreeWithFiles(t, map[string]string{
		"main.go": "package main\nfunc main() {}\n",
	})
	sld := NewSmartLanguageDetector(testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, _, err := sld.ProcessTreeWithSampling(ctx, tree)
	require.NoError(t, err)
}
