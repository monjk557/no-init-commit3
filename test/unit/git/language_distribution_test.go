package git_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/collabops/git-executor/internal/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestRepositoryWithLanguages creates a test repository with Go, JavaScript, and Python files
func setupTestRepositoryWithLanguages(t *testing.T) string {
	// Create temporary directory
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "testrepo")

	// Initialize Git repository
	err := exec.Command("git", "init", repoPath).Run()
	require.NoError(t, err, "Failed to init git repo")

	// Configure Git user
	cmd := exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err, "Failed to set git user.name")

	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err, "Failed to set git user.email")

	// Set default branch to main
	cmd = exec.Command("git", "branch", "-M", "main")
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err, "Failed to set default branch to main")

	// Create Go file
	goFile := filepath.Join(repoPath, "main.go")
	goContent := `package main

import "fmt"

func main() {
	fmt.Println("Hello, World!")
}
`
	err = os.WriteFile(goFile, []byte(goContent), 0644)
	require.NoError(t, err, "Failed to create Go file")

	// Create JavaScript file
	jsFile := filepath.Join(repoPath, "app.js")
	jsContent := `console.log('Hello, World!');

function greet(name) {
    return 'Hello, ' + name + '!';
}

module.exports = { greet };
`
	err = os.WriteFile(jsFile, []byte(jsContent), 0644)
	require.NoError(t, err, "Failed to create JavaScript file")

	// Create Python file
	pyFile := filepath.Join(repoPath, "main.py")
	pyContent := `#!/usr/bin/env python3

def greet(name):
    """Greet someone by name."""
    return f"Hello, {name}!"

if __name__ == "__main__":
    print(greet("World"))
`
	err = os.WriteFile(pyFile, []byte(pyContent), 0644)
	require.NoError(t, err, "Failed to create Python file")

	// Create README.md (should be detected as Markdown)
	readmeFile := filepath.Join(repoPath, "README.md")
	readmeContent := `# Test Repository

This is a test repository with multiple languages.
`
	err = os.WriteFile(readmeFile, []byte(readmeContent), 0644)
	require.NoError(t, err, "Failed to create README file")

	// Add all files and commit
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err, "Failed to git add files")

	cmd = exec.Command("git", "commit", "-m", "Initial commit: Add Go, JavaScript, and Python files")
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err, "Failed to git commit files")

	return repoPath
}

func TestGetLanguageDistribution(t *testing.T) {
	// Setup test repository
	repoPath := setupTestRepositoryWithLanguages(t)

	// Open repository
	gitRepo, err := git.OpenRepository(repoPath)
	require.NoError(t, err, "Failed to open repository")
	defer gitRepo.Close()

	// Get language distribution
	ctx := context.Background()
	dist, err := gitRepo.GetLanguageDistribution(ctx, "main")
	require.NoError(t, err, "Failed to get language distribution")
	require.NotNil(t, dist, "Language distribution should not be nil")

	// Verify total stats
	assert.Greater(t, dist.TotalBytes, int64(0), "Total bytes should be greater than 0")
	assert.Greater(t, dist.TotalFiles, 0, "Total files should be greater than 0")

	// Create a map of languages for easier lookup
	langMap := make(map[string]*git.LanguageStats)
	for i := range dist.Languages {
		langMap[dist.Languages[i].Language] = &dist.Languages[i]
	}

	// Verify Go language is detected
	goLang, hasGo := langMap["Go"]
	require.True(t, hasGo, "Go language should be detected")
	assert.Greater(t, goLang.Bytes, int64(0), "Go bytes should be greater than 0")
	assert.Equal(t, 1, goLang.Files, "Go should have 1 file")
	assert.Greater(t, goLang.Percentage, 0.0, "Go percentage should be greater than 0")

	// Verify JavaScript language is detected
	jsLang, hasJS := langMap["JavaScript"]
	require.True(t, hasJS, "JavaScript language should be detected")
	assert.Greater(t, jsLang.Bytes, int64(0), "JavaScript bytes should be greater than 0")
	assert.Equal(t, 1, jsLang.Files, "JavaScript should have 1 file")
	assert.Greater(t, jsLang.Percentage, 0.0, "JavaScript percentage should be greater than 0")

	// Verify Python language is detected
	pyLang, hasPy := langMap["Python"]
	require.True(t, hasPy, "Python language should be detected")
	assert.Greater(t, pyLang.Bytes, int64(0), "Python bytes should be greater than 0")
	assert.Equal(t, 1, pyLang.Files, "Python should have 1 file")
	assert.Greater(t, pyLang.Percentage, 0.0, "Python percentage should be greater than 0")

	// Verify Markdown is detected (README.md)
	mdLang, hasMarkdown := langMap["Markdown"]
	if hasMarkdown {
		assert.Greater(t, mdLang.Bytes, int64(0), "Markdown bytes should be greater than 0")
		assert.Greater(t, mdLang.Percentage, 0.0, "Markdown percentage should be greater than 0")
	}

	// Verify percentages sum to approximately 100% (allowing for small rounding errors)
	totalPercentage := 0.0
	for _, lang := range dist.Languages {
		totalPercentage += lang.Percentage
	}
	assert.InDelta(t, 100.0, totalPercentage, 1.0, "Total percentage should be approximately 100%%")

	// Verify languages are sorted by bytes (descending)
	for i := 1; i < len(dist.Languages); i++ {
		assert.GreaterOrEqual(t, dist.Languages[i-1].Bytes, dist.Languages[i].Bytes,
			"Languages should be sorted by bytes in descending order")
	}

	// Log results for debugging
	t.Logf("Language Distribution:")
	t.Logf("  Total Bytes: %d", dist.TotalBytes)
	t.Logf("  Total Files: %d", dist.TotalFiles)
	for _, lang := range dist.Languages {
		t.Logf("  - %s: %d bytes (%.2f%%), %d files",
			lang.Language, lang.Bytes, lang.Percentage, lang.Files)
	}
}

func TestGetLanguageDistribution_EmptyRepository(t *testing.T) {
	// Create empty repository
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "emptyrepo")

	err := exec.Command("git", "init", repoPath).Run()
	require.NoError(t, err)

	cmd := exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err)

	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err)

	cmd = exec.Command("git", "commit", "--allow-empty", "-m", "Initial empty commit")
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err)

	// Open repository
	gitRepo, err := git.OpenRepository(repoPath)
	require.NoError(t, err)
	defer gitRepo.Close()

	// Get language distribution
	ctx := context.Background()
	dist, err := gitRepo.GetLanguageDistribution(ctx, "main")
	require.NoError(t, err)
	require.NotNil(t, dist)

	// Verify empty distribution
	assert.Equal(t, int64(0), dist.TotalBytes, "Total bytes should be 0 for empty repo")
	assert.Equal(t, 0, dist.TotalFiles, "Total files should be 0 for empty repo")
	assert.Equal(t, 0, len(dist.Languages), "Languages should be empty for empty repo")
}

func TestGetLanguageDistribution_BinaryFilesExcluded(t *testing.T) {
	// Create test repository
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "testrepo")

	err := exec.Command("git", "init", repoPath).Run()
	require.NoError(t, err)

	cmd := exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err)

	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err)

	// Create a Go file (should be included)
	goFile := filepath.Join(repoPath, "main.go")
	goContent := `package main

func main() {}
`
	err = os.WriteFile(goFile, []byte(goContent), 0644)
	require.NoError(t, err)

	// Create a binary-like file (should be excluded)
	// Write some binary data that looks like a binary file
	binaryFile := filepath.Join(repoPath, "binary.dat")
	binaryContent := []byte{0x00, 0x01, 0x02, 0x03, 0xFF, 0xFE, 0xFD}
	err = os.WriteFile(binaryFile, binaryContent, 0644)
	require.NoError(t, err)

	// Add and commit
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err)

	cmd = exec.Command("git", "commit", "-m", "Add files")
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err)

	// Open repository
	gitRepo, err := git.OpenRepository(repoPath)
	require.NoError(t, err)
	defer gitRepo.Close()

	// Get language distribution
	ctx := context.Background()
	dist, err := gitRepo.GetLanguageDistribution(ctx, "main")
	require.NoError(t, err)
	require.NotNil(t, dist)

	// Verify binary file is excluded
	langMap := make(map[string]*git.LanguageStats)
	for i := range dist.Languages {
		langMap[dist.Languages[i].Language] = &dist.Languages[i]
	}

	// Go should be detected
	goLang, hasGo := langMap["Go"]
	require.True(t, hasGo, "Go language should be detected")
	assert.Equal(t, 1, goLang.Files, "Go should have 1 file")

	// Binary file should not be counted
	assert.Equal(t, 1, dist.TotalFiles, "Only Go file should be counted, binary file should be excluded")
}
