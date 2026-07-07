package performance

import (
	"context"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// coverageFixtureFiles builds a tree containing files that trigger each
// classification branch used by the language-detection paths.
func coverageFixtureFiles(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"node_modules/lib.js": "var x = 1;\n",                        // vendor
		".hidden":             "secret value\n",                      // dotfile
		"README.md":           "# Title\n\nSome documentation.\n",    // documentation
		"app.ini":             "[section]\nkey = value\n",            // configuration
		"Dockerfile":          "FROM scratch\nCMD [\"run\"]\n",       // extension-confident
		"mystery":             "just some random words go here\n",    // unknown language
		"data.bin":            string([]byte{0, 1, 2, 0, 3, 'a', 0}), // binary
		"main.go":             "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n",
		"big.go":              "package main\n" + strings.Repeat("// padding line\n", 200000), // > 1MB
	}
}

func fileFromTree(t *testing.T, tree *object.Tree, name string) *object.File {
	t.Helper()
	f, err := tree.File(name)
	require.NoError(t, err)
	return f
}

func TestDetectLanguageSmartly_Branches(t *testing.T) {
	tree := newTreeWithFiles(t, coverageFixtureFiles(t))
	sld := NewSmartLanguageDetector(testLogger())
	ctx := context.Background()

	tests := []struct {
		name        string
		file        string
		wantSkipped bool
		skipReason  string
	}{
		{"vendor", "node_modules/lib.js", true, "vendor"},
		{"dotfile", ".hidden", true, "dotfile"},
		{"documentation", "README.md", true, "documentation"},
		{"configuration", "app.ini", true, "configuration"},
		{"too_large", "big.go", true, "too_large"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := sld.detectLanguageSmartly(ctx, fileFromTree(t, tree, tt.file))
			assert.True(t, res.Skipped)
			assert.Equal(t, tt.skipReason, res.SkipReason)
		})
	}

	t.Run("extension confident", func(t *testing.T) {
		res := sld.detectLanguageSmartly(ctx, fileFromTree(t, tree, "Dockerfile"))
		assert.False(t, res.Skipped)
		assert.Equal(t, "Dockerfile", res.Language)
		assert.Equal(t, DetectionByExtension, res.Method)
	})

	t.Run("sampling fallback unknown", func(t *testing.T) {
		res := sld.detectLanguageSmartly(ctx, fileFromTree(t, tree, "mystery"))
		assert.True(t, res.Skipped)
		assert.Equal(t, "unknown_language", res.SkipReason)
	})
}

func TestDetectBySampling_Branches(t *testing.T) {
	tree := newTreeWithFiles(t, coverageFixtureFiles(t))
	sld := NewSmartLanguageDetector(testLogger())

	t.Run("binary", func(t *testing.T) {
		res := sld.detectBySampling(fileFromTree(t, tree, "data.bin"))
		assert.True(t, res.Skipped)
		assert.Equal(t, "binary", res.SkipReason)
	})

	t.Run("unknown language", func(t *testing.T) {
		res := sld.detectBySampling(fileFromTree(t, tree, "mystery"))
		assert.True(t, res.Skipped)
		assert.Equal(t, "unknown_language", res.SkipReason)
	})

	t.Run("detected", func(t *testing.T) {
		res := sld.detectBySampling(fileFromTree(t, tree, "main.go"))
		assert.False(t, res.Skipped)
		assert.Equal(t, "Go", res.Language)
		assert.Equal(t, DetectionBySampling, res.Method)
		assert.Greater(t, res.Confidence, 0.0)
	})
}

func TestProcessFile_Branches(t *testing.T) {
	tree := newTreeWithFiles(t, coverageFixtureFiles(t))
	ptp := NewParallelTreeProcessor(testLogger())
	ctx := context.Background()

	tests := []struct {
		name        string
		file        string
		wantSkipped bool
		skipReason  string
	}{
		{"vendor", "node_modules/lib.js", true, "vendor"},
		{"dotfile", ".hidden", true, "dotfile"},
		{"documentation", "README.md", true, "documentation"},
		{"configuration", "app.ini", true, "configuration"},
		{"too_large", "big.go", true, "too_large"},
		{"binary", "data.bin", true, "binary"},
		{"unknown", "mystery", true, "unknown_language"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := ptp.processFile(ctx, fileFromTree(t, tree, tt.file))
			assert.True(t, res.Skipped)
			assert.Equal(t, tt.skipReason, res.SkipReason)
		})
	}

	t.Run("detected", func(t *testing.T) {
		res := ptp.processFile(ctx, fileFromTree(t, tree, "main.go"))
		assert.False(t, res.Skipped)
		assert.Equal(t, "Go", res.Language)
		assert.Greater(t, res.Bytes, int64(0))
	})

	t.Run("context cancelled", func(t *testing.T) {
		cctx, cancel := context.WithCancel(context.Background())
		cancel()
		res := ptp.processFile(cctx, fileFromTree(t, tree, "main.go"))
		assert.Error(t, res.Error)
	})
}
