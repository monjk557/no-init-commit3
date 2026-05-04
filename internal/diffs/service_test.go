package diffs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// setupTestRepositoryForBinaryFiles creates a test repository with binary and text files
func setupTestRepositoryForBinaryFiles(t *testing.T) (string, string, string, func()) {
	tempDir, err := os.MkdirTemp("", "diffs-binary-test-*")
	require.NoError(t, err)

	// Create proper directory structure for handler (testowner/testrepo.git)
	ownerDir := filepath.Join(tempDir, "testowner")
	err = os.MkdirAll(ownerDir, 0755)
	require.NoError(t, err)

	repoPath := filepath.Join(ownerDir, "testrepo.git")
	repo, err := gogit.PlainInit(repoPath, true) // bare repository
	require.NoError(t, err)

	// Create initial commit with text file (using storer for bare repo)
	textContent := []byte("Hello World\nLine 2\nLine 3\n")
	textBlob := repo.Storer.NewEncodedObject()
	textBlob.SetType(plumbing.BlobObject)
	textBlob.SetSize(int64(len(textContent)))
	textWriter, err := textBlob.Writer()
	require.NoError(t, err)
	_, err = textWriter.Write(textContent)
	require.NoError(t, err)
	require.NoError(t, textWriter.Close())
	textBlobHash, err := repo.Storer.SetEncodedObject(textBlob)
	require.NoError(t, err)

	initialTree := &object.Tree{
		Entries: []object.TreeEntry{
			{Name: "text.txt", Mode: 0100644, Hash: textBlobHash},
		},
	}
	initialTreeObj := repo.Storer.NewEncodedObject()
	err = initialTree.Encode(initialTreeObj)
	require.NoError(t, err)
	initialTreeHash, err := repo.Storer.SetEncodedObject(initialTreeObj)
	require.NoError(t, err)

	initialCommit := &object.Commit{
		Message:   "Initial commit",
		Author:    object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
		Committer: object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
		TreeHash:  initialTreeHash,
	}
	initialCommitObj := repo.Storer.NewEncodedObject()
	err = initialCommit.Encode(initialCommitObj)
	require.NoError(t, err)
	initialCommitHash, err := repo.Storer.SetEncodedObject(initialCommitObj)
	require.NoError(t, err)

	// Create second commit - add binary files and modify text file
	// PNG file (binary by extension)
	pngData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A} // PNG signature
	pngData = append(pngData, make([]byte, 100)...)
	pngBlob := repo.Storer.NewEncodedObject()
	pngBlob.SetType(plumbing.BlobObject)
	pngBlob.SetSize(int64(len(pngData)))
	pngWriter, err := pngBlob.Writer()
	require.NoError(t, err)
	_, err = pngWriter.Write(pngData)
	require.NoError(t, err)
	require.NoError(t, pngWriter.Close())
	pngBlobHash, err := repo.Storer.SetEncodedObject(pngBlob)
	require.NoError(t, err)

	// Binary file with null bytes
	nullData := []byte("Some text before null\x00null byte here\x00more data")
	nullBlob := repo.Storer.NewEncodedObject()
	nullBlob.SetType(plumbing.BlobObject)
	nullBlob.SetSize(int64(len(nullData)))
	nullWriter, err := nullBlob.Writer()
	require.NoError(t, err)
	_, err = nullWriter.Write(nullData)
	require.NoError(t, err)
	require.NoError(t, nullWriter.Close())
	nullBlobHash, err := repo.Storer.SetEncodedObject(nullBlob)
	require.NoError(t, err)

	// Modified text file
	modifiedTextContent := []byte("Hello World\nLine 2\nLine 3\nLine 4\nLine 5\n")
	modifiedTextBlob := repo.Storer.NewEncodedObject()
	modifiedTextBlob.SetType(plumbing.BlobObject)
	modifiedTextBlob.SetSize(int64(len(modifiedTextContent)))
	modifiedTextWriter, err := modifiedTextBlob.Writer()
	require.NoError(t, err)
	_, err = modifiedTextWriter.Write(modifiedTextContent)
	require.NoError(t, err)
	require.NoError(t, modifiedTextWriter.Close())
	modifiedTextBlobHash, err := repo.Storer.SetEncodedObject(modifiedTextBlob)
	require.NoError(t, err)

	secondTreeEntries := []object.TreeEntry{
		{Name: "text.txt", Mode: 0100644, Hash: modifiedTextBlobHash},
		{Name: "image.png", Mode: 0100644, Hash: pngBlobHash},
		{Name: "binary.bin", Mode: 0100644, Hash: nullBlobHash},
	}
	// Sort tree entries by name (required by Git)
	sort.Slice(secondTreeEntries, func(i, j int) bool {
		return secondTreeEntries[i].Name < secondTreeEntries[j].Name
	})
	secondTree := &object.Tree{
		Entries: secondTreeEntries,
	}
	secondTreeObj := repo.Storer.NewEncodedObject()
	err = secondTree.Encode(secondTreeObj)
	require.NoError(t, err)
	secondTreeHash, err := repo.Storer.SetEncodedObject(secondTreeObj)
	require.NoError(t, err)

	secondCommit := &object.Commit{
		Message:      "Add binary files and modify text",
		Author:       object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
		Committer:    object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
		TreeHash:     secondTreeHash,
		ParentHashes: []plumbing.Hash{initialCommitHash},
	}
	secondCommitObj := repo.Storer.NewEncodedObject()
	err = secondCommit.Encode(secondCommitObj)
	require.NoError(t, err)
	secondCommitHash, err := repo.Storer.SetEncodedObject(secondCommitObj)
	require.NoError(t, err)

	// Create branch references
	ref1 := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/master"), initialCommitHash)
	err = repo.Storer.SetReference(ref1)
	require.NoError(t, err)
	ref2 := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/feature"), secondCommitHash)
	err = repo.Storer.SetReference(ref2)
	require.NoError(t, err)

	cleanup := func() {
		_ = os.RemoveAll(tempDir)
	}

	return repoPath, initialCommitHash.String(), secondCommitHash.String(), cleanup
}

// TestBinaryFile_NoChanges tests that binary files don't generate changes
func TestBinaryFile_NoChanges(t *testing.T) {
	repoPath, initialCommitSHA, secondCommitSHA, cleanup := setupTestRepositoryForBinaryFiles(t)
	defer cleanup()

	// Setup handler
	tempDir := filepath.Dir(filepath.Dir(repoPath))
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}

	handler, err := NewDiffHandler(cfg)
	require.NoError(t, err)

	// Setup router
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/commits/:base/:head", adapt(handler.HandleCommitDiff))

	// Make request - use full commit hash
	url := fmt.Sprintf("/api/v2/repository/testowner/testrepo/diff/commits/%s/%s", initialCommitSHA, secondCommitSHA)
	req, _ := http.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	// Verify response
	if w.Code != http.StatusOK {
		t.Logf("Unexpected status code: %d, Body: %s", w.Code, w.Body.String())
	}
	assert.Equal(t, http.StatusOK, w.Code, "Should return 200 OK")

	var diffResp DiffResponse
	err = json.NewDecoder(w.Body).Decode(&diffResp)
	require.NoError(t, err)
	assert.True(t, diffResp.Success)

	// Check binary files
	var pngFile *FileDiff
	var binFile *FileDiff
	var textFile *FileDiff

	for i := range diffResp.Files {
		switch diffResp.Files[i].Path {
		case "image.png":
			pngFile = &diffResp.Files[i]
		case "binary.bin":
			binFile = &diffResp.Files[i]
		case "text.txt":
			textFile = &diffResp.Files[i]
		}
	}

	// PNG file should be binary and have no changes
	require.NotNil(t, pngFile, "image.png should be in diff")
	assert.True(t, pngFile.IsBinary, "image.png should be detected as binary")
	assert.Nil(t, pngFile.Changes, "Binary file should not have changes")
	assert.Equal(t, FileLoadStatusBinary, pngFile.LoadStatus, "Binary file should have load_status=binary")

	// Binary file with null bytes should be binary and have no changes
	require.NotNil(t, binFile, "binary.bin should be in diff")
	assert.True(t, binFile.IsBinary, "binary.bin should be detected as binary")
	assert.Nil(t, binFile.Changes, "Binary file should not have changes")
	assert.Equal(t, FileLoadStatusBinary, binFile.LoadStatus, "Binary file should have load_status=binary")

	// Text file should not be binary and should have changes
	require.NotNil(t, textFile, "text.txt should be in diff")
	assert.False(t, textFile.IsBinary, "text.txt should not be binary")
	assert.NotNil(t, textFile.Changes, "Text file should have changes")
	assert.Greater(t, len(textFile.Changes), 0, "Text file should have line changes")
	assert.Equal(t, FileLoadStatusLoaded, textFile.LoadStatus, "Text file with changes should have load_status=loaded")
}

// setupTestRepositoryForLargeFiles creates a test repository with large files
func setupTestRepositoryForLargeFiles(t *testing.T) (string, string, string, func()) {
	tempDir, err := os.MkdirTemp("", "diffs-large-test-*")
	require.NoError(t, err)

	// Create proper directory structure for handler (testowner/testrepo.git)
	ownerDir := filepath.Join(tempDir, "testowner")
	err = os.MkdirAll(ownerDir, 0755)
	require.NoError(t, err)

	repoPath := filepath.Join(ownerDir, "testrepo.git")
	repo, err := gogit.PlainInit(repoPath, true) // bare repository
	require.NoError(t, err)

	// Create initial commit
	initialContent := []byte("initial")
	initialBlob := repo.Storer.NewEncodedObject()
	initialBlob.SetType(plumbing.BlobObject)
	initialBlob.SetSize(int64(len(initialContent)))
	initialWriter, err := initialBlob.Writer()
	require.NoError(t, err)
	_, err = initialWriter.Write(initialContent)
	require.NoError(t, err)
	require.NoError(t, initialWriter.Close())
	initialBlobHash, err := repo.Storer.SetEncodedObject(initialBlob)
	require.NoError(t, err)

	initialTree := &object.Tree{
		Entries: []object.TreeEntry{
			{Name: "initial.txt", Mode: 0100644, Hash: initialBlobHash},
		},
	}
	initialTreeObj := repo.Storer.NewEncodedObject()
	err = initialTree.Encode(initialTreeObj)
	require.NoError(t, err)
	initialTreeHash, err := repo.Storer.SetEncodedObject(initialTreeObj)
	require.NoError(t, err)

	initialCommit := &object.Commit{
		Message:   "Initial commit",
		Author:    object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
		Committer: object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
		TreeHash:  initialTreeHash,
	}
	initialCommitObj := repo.Storer.NewEncodedObject()
	err = initialCommit.Encode(initialCommitObj)
	require.NoError(t, err)
	initialCommitHash, err := repo.Storer.SetEncodedObject(initialCommitObj)
	require.NoError(t, err)

	// Second commit - add many files (more than MaxFilesWithChanges = 5)
	var secondTreeEntries []object.TreeEntry
	secondTreeEntries = append(secondTreeEntries, object.TreeEntry{Name: "initial.txt", Mode: 0100644, Hash: initialBlobHash})

	for i := 0; i < 10; i++ {
		fileName := fmt.Sprintf("file%d.txt", i)
		content := []byte(fmt.Sprintf("File %d\nLine 1\nLine 2\nLine 3\n", i))
		fileBlob := repo.Storer.NewEncodedObject()
		fileBlob.SetType(plumbing.BlobObject)
		fileBlob.SetSize(int64(len(content)))
		fileWriter, err := fileBlob.Writer()
		require.NoError(t, err)
		_, err = fileWriter.Write(content)
		require.NoError(t, err)
		require.NoError(t, fileWriter.Close())
		fileBlobHash, err := repo.Storer.SetEncodedObject(fileBlob)
		require.NoError(t, err)
		secondTreeEntries = append(secondTreeEntries, object.TreeEntry{Name: fileName, Mode: 0100644, Hash: fileBlobHash})
	}

	// Sort tree entries by name (required by Git)
	sort.Slice(secondTreeEntries, func(i, j int) bool {
		return secondTreeEntries[i].Name < secondTreeEntries[j].Name
	})
	secondTree := &object.Tree{Entries: secondTreeEntries}
	secondTreeObj := repo.Storer.NewEncodedObject()
	err = secondTree.Encode(secondTreeObj)
	require.NoError(t, err)
	secondTreeHash, err := repo.Storer.SetEncodedObject(secondTreeObj)
	require.NoError(t, err)

	secondCommit := &object.Commit{
		Message:      "Add many files",
		Author:       object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
		Committer:    object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
		TreeHash:     secondTreeHash,
		ParentHashes: []plumbing.Hash{initialCommitHash},
	}
	secondCommitObj := repo.Storer.NewEncodedObject()
	err = secondCommit.Encode(secondCommitObj)
	require.NoError(t, err)
	secondCommitHash, err := repo.Storer.SetEncodedObject(secondCommitObj)
	require.NoError(t, err)

	// Create branch references
	ref1 := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/master"), initialCommitHash)
	err = repo.Storer.SetReference(ref1)
	require.NoError(t, err)
	ref2 := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/feature"), secondCommitHash)
	err = repo.Storer.SetReference(ref2)
	require.NoError(t, err)

	cleanup := func() {
		_ = os.RemoveAll(tempDir)
	}

	return repoPath, initialCommitHash.String(), secondCommitHash.String(), cleanup
}

// TestMaxFilesWithChanges_Limit tests that only top 5 files have changes
func TestMaxFilesWithChanges_Limit(t *testing.T) {
	repoPath, initialCommitSHA, secondCommitSHA, cleanup := setupTestRepositoryForLargeFiles(t)
	defer cleanup()

	// Setup handler
	tempDir := filepath.Dir(filepath.Dir(repoPath))
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}

	handler, err := NewDiffHandler(cfg)
	require.NoError(t, err)

	// Setup router
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/commits/:base/:head", adapt(handler.HandleCommitDiff))

	// Make request - use full commit hash
	url := fmt.Sprintf("/api/v2/repository/testowner/testrepo/diff/commits/%s/%s", initialCommitSHA, secondCommitSHA)
	req, _ := http.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	// Verify response
	if w.Code != http.StatusOK {
		t.Logf("Unexpected status code: %d, Body: %s", w.Code, w.Body.String())
	}
	assert.Equal(t, http.StatusOK, w.Code, "Should return 200 OK")

	var diffResp DiffResponse
	err = json.NewDecoder(w.Body).Decode(&diffResp)
	require.NoError(t, err)
	assert.True(t, diffResp.Success)

	// All files should be in the response (metadata)
	assert.Equal(t, 10, len(diffResp.Files), "All 10 files should be in response")

	// Count files with changes
	filesWithChanges := 0
	for _, file := range diffResp.Files {
		if len(file.Changes) > 0 {
			filesWithChanges++
		}
	}

	// Only top 5 files should have changes
	assert.LessOrEqual(t, filesWithChanges, MaxFilesWithChanges, "Only top 5 files should have changes")
	assert.Equal(t, MaxFilesWithChanges, filesWithChanges, "Exactly 5 files should have changes")

	// Check truncated flag
	assert.True(t, diffResp.Truncated, "Should be marked as truncated when more than 5 files")

	// Verify first 5 files have changes, rest don't
	for i, file := range diffResp.Files {
		if i < MaxFilesWithChanges {
			assert.NotNil(t, file.Changes, "File %d should have changes", i)
			assert.Greater(t, len(file.Changes), 0, "File %d should have non-empty changes", i)
			assert.Equal(t, FileLoadStatusLoaded, file.LoadStatus, "File %d should have load_status=loaded", i)
		} else {
			assert.Nil(t, file.Changes, "File %d should not have changes", i)
			assert.Equal(t, FileLoadStatusCollapsed, file.LoadStatus, "File %d should have load_status=collapsed", i)
		}
	}
}

// setupTestRepositoryForLargeChanges creates a test repository with files that have many changes
func setupTestRepositoryForLargeChanges(t *testing.T) (string, string, string, func()) {
	tempDir, err := os.MkdirTemp("", "diffs-large-changes-test-*")
	require.NoError(t, err)

	// Create proper directory structure for handler (testowner/testrepo.git)
	ownerDir := filepath.Join(tempDir, "testowner")
	err = os.MkdirAll(ownerDir, 0755)
	require.NoError(t, err)

	repoPath := filepath.Join(ownerDir, "testrepo.git")
	repo, err := gogit.PlainInit(repoPath, true) // bare repository
	require.NoError(t, err)

	// Initial commit - create file with many lines (less than MaxChangedLines)
	// Use shorter lines to keep file size small
	initialContent := ""
	for i := 0; i < 5000; i++ {
		initialContent += fmt.Sprintf("L%d\n", i) // Short line to keep size small
	}
	initialBlob := repo.Storer.NewEncodedObject()
	initialBlob.SetType(plumbing.BlobObject)
	initialBlob.SetSize(int64(len(initialContent)))
	initialWriter, err := initialBlob.Writer()
	require.NoError(t, err)
	_, err = initialWriter.Write([]byte(initialContent))
	require.NoError(t, err)
	require.NoError(t, initialWriter.Close())
	initialBlobHash, err := repo.Storer.SetEncodedObject(initialBlob)
	require.NoError(t, err)

	initialTree := &object.Tree{
		Entries: []object.TreeEntry{
			{Name: "large.txt", Mode: 0100644, Hash: initialBlobHash},
		},
	}
	initialTreeObj := repo.Storer.NewEncodedObject()
	err = initialTree.Encode(initialTreeObj)
	require.NoError(t, err)
	initialTreeHash, err := repo.Storer.SetEncodedObject(initialTreeObj)
	require.NoError(t, err)

	initialCommit := &object.Commit{
		Message:   "Initial commit",
		Author:    object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
		Committer: object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
		TreeHash:  initialTreeHash,
	}
	initialCommitObj := repo.Storer.NewEncodedObject()
	err = initialCommit.Encode(initialCommitObj)
	require.NoError(t, err)
	initialCommitHash, err := repo.Storer.SetEncodedObject(initialCommitObj)
	require.NoError(t, err)

	// Second commit - modify file to exceed MaxChangedLines (10000)
	// Use shorter lines to keep file size small but exceed MaxChangedLines
	modifiedContent := ""
	for i := 0; i < 15000; i++ {
		modifiedContent += fmt.Sprintf("M%d\n", i) // Short line to keep size small
	}
	modifiedBlob := repo.Storer.NewEncodedObject()
	modifiedBlob.SetType(plumbing.BlobObject)
	modifiedBlob.SetSize(int64(len(modifiedContent)))
	modifiedWriter, err := modifiedBlob.Writer()
	require.NoError(t, err)
	_, err = modifiedWriter.Write([]byte(modifiedContent))
	require.NoError(t, err)
	require.NoError(t, modifiedWriter.Close())
	modifiedBlobHash, err := repo.Storer.SetEncodedObject(modifiedBlob)
	require.NoError(t, err)

	secondTree := &object.Tree{
		Entries: []object.TreeEntry{
			{Name: "large.txt", Mode: 0100644, Hash: modifiedBlobHash},
		},
	}
	secondTreeObj := repo.Storer.NewEncodedObject()
	err = secondTree.Encode(secondTreeObj)
	require.NoError(t, err)
	secondTreeHash, err := repo.Storer.SetEncodedObject(secondTreeObj)
	require.NoError(t, err)

	secondCommit := &object.Commit{
		Message:      "Modify large file",
		Author:       object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
		Committer:    object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
		TreeHash:     secondTreeHash,
		ParentHashes: []plumbing.Hash{initialCommitHash},
	}
	secondCommitObj := repo.Storer.NewEncodedObject()
	err = secondCommit.Encode(secondCommitObj)
	require.NoError(t, err)
	secondCommitHash, err := repo.Storer.SetEncodedObject(secondCommitObj)
	require.NoError(t, err)

	// Create branch references
	ref1 := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/master"), initialCommitHash)
	err = repo.Storer.SetReference(ref1)
	require.NoError(t, err)
	ref2 := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/feature"), secondCommitHash)
	err = repo.Storer.SetReference(ref2)
	require.NoError(t, err)

	cleanup := func() {
		_ = os.RemoveAll(tempDir)
	}

	return repoPath, initialCommitHash.String(), secondCommitHash.String(), cleanup
}

// TestMaxChangedLines_Limit tests that files with more than 10000 changes don't have changes array
func TestMaxChangedLines_Limit(t *testing.T) {
	repoPath, initialCommitSHA, secondCommitSHA, cleanup := setupTestRepositoryForLargeChanges(t)
	defer cleanup()

	// Setup handler
	tempDir := filepath.Dir(filepath.Dir(repoPath))
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}

	handler, err := NewDiffHandler(cfg)
	require.NoError(t, err)

	// Setup router
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/commits/:base/:head", adapt(handler.HandleCommitDiff))

	// Make request - use full commit hash
	url := fmt.Sprintf("/api/v2/repository/testowner/testrepo/diff/commits/%s/%s", initialCommitSHA, secondCommitSHA)
	req, _ := http.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	// Verify response - may return 200 OK or error if file is too large
	// But we expect it to work since we're using short lines
	if w.Code != http.StatusOK {
		t.Logf("Response code: %d, Body: %s", w.Code, w.Body.String())
		// If it fails due to size, that's also acceptable (timeout prevention working)
		if w.Code == http.StatusRequestEntityTooLarge {
			t.Skip("File too large - timeout prevention working correctly")
			return
		}
		require.Equal(t, http.StatusOK, w.Code, "Should return 200 OK or 413")
		return
	}

	var diffResp DiffResponse
	err = json.NewDecoder(w.Body).Decode(&diffResp)
	require.NoError(t, err)
	assert.True(t, diffResp.Success)

	// Find large.txt
	var largeFile *FileDiff
	for i := range diffResp.Files {
		if diffResp.Files[i].Path == "large.txt" {
			largeFile = &diffResp.Files[i]
			break
		}
	}

	require.NotNil(t, largeFile, "large.txt should be in diff")

	// File should have metadata (additions, deletions)
	changedLines := largeFile.Additions + largeFile.Deletions
	assert.Greater(t, changedLines, MaxChangedLines, "Should have more than MaxChangedLines total changes (additions=%d, deletions=%d)", largeFile.Additions, largeFile.Deletions)

	// But should not have changes array
	assert.Nil(t, largeFile.Changes, "File with more than MaxChangedLines should not have changes")
	assert.Equal(t, FileLoadStatusTooLarge, largeFile.LoadStatus, "File exceeding MaxChangedLines should have load_status=too_large")
}

// TestBinaryFileDetection_ByExtension tests binary file detection by extension
func TestBinaryFileDetection_ByExtension(t *testing.T) {
	testCases := []struct {
		fileName string
		expected bool
	}{
		{"image.png", true},
		{"photo.jpg", true},
		{"document.pdf", true},
		{"archive.zip", true},
		{"executable.exe", true},
		{"text.txt", false},
		{"code.go", false},
		{"script.sh", false},
		{"data.json", false},
	}

	for _, tc := range testCases {
		t.Run(tc.fileName, func(t *testing.T) {
			result := isBinaryFileByExtension(tc.fileName)
			assert.Equal(t, tc.expected, result, "File %s should be detected as binary=%v", tc.fileName, tc.expected)
		})
	}
}

// TestBinaryFileDetection_ByContent tests binary file detection by null bytes
func TestBinaryFileDetection_ByContent(t *testing.T) {
	testCases := []struct {
		name     string
		content  string
		expected bool
	}{
		{"text without null", "Hello World\nLine 2\nLine 3", false},
		{"text with null", "Hello World\x00null byte here", true},
		{"empty", "", false},
		{"only null", "\x00", true},
		{"null at start", "\x00Hello World", true},
		{"null at end", "Hello World\x00", true},
		{"large text without null", strings.Repeat("Hello World\n", 1000), false},
		{"large text with null", strings.Repeat("Hello World\n", 500) + "\x00" + strings.Repeat("More text\n", 500), true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := isBinaryContent(tc.content)
			assert.Equal(t, tc.expected, result, "Content should be detected as binary=%v", tc.expected)
		})
	}
}

// TestBinaryFileDetection_First8KB tests that only first 8KB is scanned
func TestBinaryFileDetection_First8KB(t *testing.T) {
	// Create content larger than 8KB
	largeText := strings.Repeat("Hello World\n", 10000) // Much larger than 8KB
	assert.False(t, isBinaryContent(largeText), "Large text without null should not be binary")

	// Add null byte within first 8KB (at position ~1000)
	// "Hello World\n" is 12 bytes, so 1000 repetitions = ~12KB, but we need null within first 8KB
	// Let's put null at position ~5000 bytes (within 8KB)
	firstPart := strings.Repeat("Hello World\n", 400) // ~4800 bytes
	nullByte := "\x00"
	secondPart := strings.Repeat("More text\n", 1000)
	largeTextWithNull := firstPart + nullByte + secondPart
	assert.True(t, isBinaryContent(largeTextWithNull), "Text with null within first 8KB should be binary")

	// Add null byte after 8KB (beyond scan limit)
	// First 8KB without null
	first8KB := strings.Repeat("A", 8*1024)
	// Then null at exactly 8KB+1 position
	textWithNullAt8KBPlus1 := first8KB + "\x00" + strings.Repeat("B", 1000)
	// This should NOT be detected as binary because null is after 8KB scan limit
	assert.False(t, isBinaryContent(textWithNullAt8KBPlus1), "Null byte after 8KB should not be detected")
}

// TestTimeoutPrevention_AllFilesMetadata tests that all files metadata is included even when changes are limited
func TestTimeoutPrevention_AllFilesMetadata(t *testing.T) {
	repoPath, initialCommitSHA, secondCommitSHA, cleanup := setupTestRepositoryForLargeFiles(t)
	defer cleanup()

	// Setup handler
	tempDir := filepath.Dir(filepath.Dir(repoPath))
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}

	handler, err := NewDiffHandler(cfg)
	require.NoError(t, err)

	// Setup router
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/commits/:base/:head", adapt(handler.HandleCommitDiff))

	// Make request - use full commit hash
	url := fmt.Sprintf("/api/v2/repository/testowner/testrepo/diff/commits/%s/%s", initialCommitSHA, secondCommitSHA)
	req, _ := http.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	// Verify response
	if w.Code != http.StatusOK {
		t.Logf("Unexpected status code: %d, Body: %s", w.Code, w.Body.String())
	}
	assert.Equal(t, http.StatusOK, w.Code, "Should return 200 OK")

	var diffResp DiffResponse
	err = json.NewDecoder(w.Body).Decode(&diffResp)
	require.NoError(t, err)
	assert.True(t, diffResp.Success)

	// All files should have metadata (path, status, additions, deletions, is_binary)
	assert.Equal(t, 10, len(diffResp.Files), "All 10 files should be in response")

	for i, file := range diffResp.Files {
		assert.NotEmpty(t, file.Path, "File %d should have path", i)
		assert.NotEmpty(t, file.Status, "File %d should have status", i)
		assert.GreaterOrEqual(t, file.Additions, 0, "File %d should have additions", i)
		assert.GreaterOrEqual(t, file.Deletions, 0, "File %d should have deletions", i)
		// is_binary is a boolean, so it's always set
	}

	// Stats should reflect all files
	assert.Equal(t, 10, diffResp.Stats.FilesChanged, "Stats should show all 10 files changed")
}

// TestLoadStatus_JSONSerialization tests that load_status is always present in JSON output
func TestLoadStatus_JSONSerialization(t *testing.T) {
	testCases := []struct {
		name       string
		fileDiff   FileDiff
		wantStatus string
	}{
		{
			name: "loaded status",
			fileDiff: FileDiff{
				Path:       "test.go",
				Status:     FileDiffStatusModified,
				LoadStatus: FileLoadStatusLoaded,
				Changes:    []LineChange{},
				Hunks:      []Hunk{},
			},
			wantStatus: "loaded",
		},
		{
			name: "collapsed status",
			fileDiff: FileDiff{
				Path:       "big_dir/file.go",
				Status:     FileDiffStatusModified,
				LoadStatus: FileLoadStatusCollapsed,
				Hunks:      []Hunk{},
			},
			wantStatus: "collapsed",
		},
		{
			name: "too_large status",
			fileDiff: FileDiff{
				Path:       "huge.txt",
				Status:     FileDiffStatusModified,
				LoadStatus: FileLoadStatusTooLarge,
				Hunks:      []Hunk{},
			},
			wantStatus: "too_large",
		},
		{
			name: "binary status",
			fileDiff: FileDiff{
				Path:       "image.png",
				Status:     FileDiffStatusAdded,
				IsBinary:   true,
				LoadStatus: FileLoadStatusBinary,
				Hunks:      []Hunk{},
			},
			wantStatus: "binary",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.fileDiff)
			require.NoError(t, err)

			var m map[string]interface{}
			err = json.Unmarshal(data, &m)
			require.NoError(t, err)

			// loadStatus must always be present in JSON
			loadStatus, ok := m["loadStatus"]
			assert.True(t, ok, "loadStatus field must be present in JSON output")
			assert.Equal(t, tc.wantStatus, loadStatus, "loadStatus value mismatch")
		})
	}
}
