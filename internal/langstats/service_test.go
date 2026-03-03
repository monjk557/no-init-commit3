package langstats

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// createTestBareRepo creates a bare git repository with test files for language detection.
func createTestBareRepo(t *testing.T) string {
	t.Helper()

	// Create a temporary working directory for the initial commit
	workDir := t.TempDir()

	// Init a normal repo, commit files, then clone as bare
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = workDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("command %v failed: %v\n%s", args, err, out)
		}
	}

	run("git", "init")
	run("git", "checkout", "-b", "main")

	// Create test files with various extensions
	files := map[string]string{
		"main.go":           "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n",
		"util.go":           "package main\n\nfunc add(a, b int) int {\n\treturn a + b\n}\n",
		"app.ts":            "const greeting: string = 'hello';\nconsole.log(greeting);\n",
		"style.css":         "body { margin: 0; padding: 0; }\n",
		"README.md":         "# Test Project\n\nThis is a test.\n",
		"vendor/lib.go":     "package lib\n",
		".hidden":           "secret\n",
		"docs/guide.md":     "# Guide\n",
		"config.yaml":       "key: value\n",
		"src/index.js":      "console.log('hello');\n",
		"src/component.tsx": "export const App = () => <div>Hello</div>;\n",
		"src/component.jsx": "export const App = () => <div>Hello</div>;\n",
	}

	for path, content := range files {
		dir := filepath.Dir(filepath.Join(workDir, path))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workDir, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run("git", "add", "-A")
	run("git", "commit", "-m", "initial commit")

	// Clone as bare repo
	bareDir := t.TempDir()
	cmd := exec.Command("git", "clone", "--bare", workDir, bareDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git clone --bare failed: %v\n%s", err, out)
	}

	return bareDir
}

func TestParseLsTreeLong(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantMode string
		wantType string
		wantHash string
		wantSize int64
		wantPath string
		wantOK   bool
	}{
		{
			name:     "normal blob",
			line:     "100644 blob abc123def456 1234\tsrc/main.go",
			wantMode: "100644",
			wantType: "blob",
			wantHash: "abc123def456",
			wantSize: 1234,
			wantPath: "src/main.go",
			wantOK:   true,
		},
		{
			name:     "tree entry with dash size",
			line:     "040000 tree abc123def456 -\tsrc",
			wantMode: "040000",
			wantType: "tree",
			wantHash: "abc123def456",
			wantSize: 0,
			wantPath: "src",
			wantOK:   true,
		},
		{
			name:     "symlink",
			line:     "120000 blob abc123def456 10\tlink.txt",
			wantMode: "120000",
			wantType: "blob",
			wantHash: "abc123def456",
			wantSize: 10,
			wantPath: "link.txt",
			wantOK:   true,
		},
		{
			name:   "invalid line - no tab",
			line:   "100644 blob abc123def456 1234 src/main.go",
			wantOK: false,
		},
		{
			name:   "invalid line - too few fields",
			line:   "100644 blob\tsrc/main.go",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, entryType, hash, size, path, ok := parseLsTreeLong(tt.line)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if mode != tt.wantMode {
				t.Errorf("mode = %q, want %q", mode, tt.wantMode)
			}
			if entryType != tt.wantType {
				t.Errorf("type = %q, want %q", entryType, tt.wantType)
			}
			if hash != tt.wantHash {
				t.Errorf("hash = %q, want %q", hash, tt.wantHash)
			}
			if size != tt.wantSize {
				t.Errorf("size = %d, want %d", size, tt.wantSize)
			}
			if path != tt.wantPath {
				t.Errorf("path = %q, want %q", path, tt.wantPath)
			}
		})
	}
}

func TestFinalize(t *testing.T) {
	t.Run("empty stats", func(t *testing.T) {
		langs, totalBytes, totalFiles := finalize(map[string]*langAccum{})
		if len(langs) != 0 {
			t.Errorf("expected empty languages, got %d", len(langs))
		}
		if totalBytes != 0 || totalFiles != 0 {
			t.Errorf("expected zero totals, got bytes=%d files=%d", totalBytes, totalFiles)
		}
	})

	t.Run("single language", func(t *testing.T) {
		stats := map[string]*langAccum{
			"Go": {bytes: 10000, files: 5},
		}
		langs, totalBytes, totalFiles := finalize(stats)
		if len(langs) != 1 {
			t.Fatalf("expected 1 language, got %d", len(langs))
		}
		if langs[0].Language != "Go" {
			t.Errorf("expected Go, got %s", langs[0].Language)
		}
		if langs[0].Percentage != 100.0 {
			t.Errorf("expected 100%%, got %.1f%%", langs[0].Percentage)
		}
		if totalBytes != 10000 || totalFiles != 5 {
			t.Errorf("unexpected totals: bytes=%d files=%d", totalBytes, totalFiles)
		}
	})

	t.Run("language group mapping (TSX → TypeScript)", func(t *testing.T) {
		stats := map[string]*langAccum{
			"TSX":        {bytes: 5000, files: 3},
			"TypeScript": {bytes: 5000, files: 2},
		}
		langs, totalBytes, _ := finalize(stats)

		if totalBytes != 10000 {
			t.Errorf("expected 10000 total bytes, got %d", totalBytes)
		}

		// TSX should be merged into TypeScript
		found := false
		for _, l := range langs {
			if l.Language == "TypeScript" {
				found = true
				if l.Bytes != 10000 {
					t.Errorf("expected 10000 bytes for TypeScript, got %d", l.Bytes)
				}
			}
			if l.Language == "TSX" {
				t.Error("TSX should be merged into TypeScript")
			}
		}
		if !found {
			t.Error("TypeScript not found in results")
		}
	})

	t.Run("others grouping", func(t *testing.T) {
		stats := map[string]*langAccum{
			"Go":         {bytes: 50000, files: 20},
			"TypeScript": {bytes: 30000, files: 10},
			"Python":     {bytes: 10000, files: 5},
			"Rust":       {bytes: 5000, files: 3},
			"Java":       {bytes: 3000, files: 2},
			"Ruby":       {bytes: 100, files: 1}, // < 1%
			"Perl":       {bytes: 50, files: 1},  // < 1%
		}
		langs, _, _ := finalize(stats)

		// Should have top 5 + Others
		hasOthers := false
		for _, l := range langs {
			if l.Language == "Others" {
				hasOthers = true
				if l.Bytes != 150 {
					t.Errorf("expected Others bytes=150, got %d", l.Bytes)
				}
			}
		}
		if !hasOthers {
			t.Error("expected Others category")
		}
		if len(langs) > maxLanguagesToShow+1 {
			t.Errorf("expected at most %d languages, got %d", maxLanguagesToShow+1, len(langs))
		}
	})
}

func TestComputeLanguageStats_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Check if git is available
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	bareDir := createTestBareRepo(t)
	ctx := context.Background()

	resp, err := ComputeLanguageStats(ctx, bareDir, "HEAD")
	if err != nil {
		t.Fatalf("ComputeLanguageStats failed: %v", err)
	}

	if !resp.Success {
		t.Error("expected success=true")
	}
	if resp.CommitSHA == "" {
		t.Error("expected non-empty commit SHA")
	}
	if resp.TotalBytes == 0 {
		t.Error("expected non-zero total bytes")
	}
	if resp.TotalFiles == 0 {
		t.Error("expected non-zero total files")
	}
	if len(resp.Languages) == 0 {
		t.Error("expected at least one language")
	}

	// Verify percentages sum to ~100%
	var totalPct float64
	for _, l := range resp.Languages {
		totalPct += l.Percentage
		if l.Bytes <= 0 {
			t.Errorf("language %s has non-positive bytes: %d", l.Language, l.Bytes)
		}
		if l.Files <= 0 {
			t.Errorf("language %s has non-positive files: %d", l.Language, l.Files)
		}
	}
	if totalPct < 99.0 || totalPct > 101.0 {
		t.Errorf("percentages sum to %.1f%%, expected ~100%%", totalPct)
	}

	// Verify meta
	if resp.Meta.ElapsedMs < 0 {
		t.Error("expected non-negative elapsed_ms")
	}
	if resp.Meta.TotalFilesScanned == 0 {
		t.Error("expected non-zero total_files_scanned")
	}

	// Go should be detected (we have main.go and util.go)
	foundGo := false
	for _, l := range resp.Languages {
		if l.Language == "Go" {
			foundGo = true
			break
		}
	}
	if !foundGo {
		t.Errorf("expected Go in results, got: %v", resp.Languages)
	}
}

func TestStreamTreeAndClassify_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	bareDir := createTestBareRepo(t)
	ctx := context.Background()

	result, err := StreamTreeAndClassify(ctx, bareDir, "HEAD")
	if err != nil {
		t.Fatalf("StreamTreeAndClassify failed: %v", err)
	}

	// Should have detected some files by extension
	if len(result.langStats) == 0 {
		t.Error("expected non-empty langStats")
	}

	// Go files should be in langStats (extension-based, safe)
	goStats, ok := result.langStats["Go"]
	if !ok {
		t.Error("expected Go in langStats")
	} else if goStats.files < 2 {
		t.Errorf("expected at least 2 Go files, got %d", goStats.files)
	}

	// Vendor files, dotfiles, docs, configs should be skipped
	if result.totalSkipped == 0 {
		t.Error("expected some files to be skipped")
	}
}
