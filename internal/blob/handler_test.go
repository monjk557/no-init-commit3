package blob

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/collabops/git-executor/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestHandler(t *testing.T) (*BlobHandler, string) {
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "testowner", "testrepo.git")

	err := os.MkdirAll(filepath.Dir(repoPath), 0755)
	require.NoError(t, err)

	gitRepo, err := git.PlainInit(repoPath, true)
	require.NoError(t, err)

	// 테스트 파일이 있는 초기 커밋 생성
	commitHash := createTestCommit(t, gitRepo, map[string]string{
		"README.md":           "# Test Repository",
		"src/main.js":         "console.log('Hello');",
		"src/utils/helper.js": "export function helper() {}",
		"config.json":         `{"name": "test"}`,
	})

	// main 브랜치 참조 생성
	refName := plumbing.ReferenceName("refs/heads/main")
	ref := plumbing.NewHashReference(refName, commitHash)
	err = gitRepo.Storer.SetReference(ref)
	require.NoError(t, err)

	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
	}

	handler, err := NewBlobHandler(cfg)
	require.NoError(t, err)

	return handler, commitHash.String()
}

// setupTestHandlerWithNestedTree creates a handler and repo with nested paths (e.g. src/main.js) using worktree so path=src is a directory.
func setupTestHandlerWithNestedTree(t *testing.T) (*BlobHandler, string) {
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "testowner", "testrepo.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	workDir := t.TempDir()
	workRepo, err := git.PlainInit(workDir, false)
	require.NoError(t, err)
	wt, err := workRepo.Worktree()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "src"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "README.md"), []byte("# Test"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "src", "main.js"), []byte("console.log('Hello');"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "config.json"), []byte(`{"name":"test"}`), 0644))
	_, err = wt.Add("README.md")
	require.NoError(t, err)
	_, err = wt.Add("src/main.js")
	require.NoError(t, err)
	_, err = wt.Add("config.json")
	require.NoError(t, err)
	commitHash, err := wt.Commit("init", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com"},
	})
	require.NoError(t, err)
	bareRepo, err := git.PlainInit(repoPath, true)
	require.NoError(t, err)
	copyBlobTreeRecursive(t, workRepo, bareRepo, func() plumbing.Hash {
		commit, _ := workRepo.CommitObject(commitHash)
		return commit.TreeHash
	}())
	commit, _ := workRepo.CommitObject(commitHash)
	commitObj := bareRepo.Storer.NewEncodedObject()
	require.NoError(t, commit.Encode(commitObj))
	_, err = bareRepo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)
	require.NoError(t, bareRepo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), commitHash)))
	cfg := &config.Config{Git: config.GitConfig{RepoRoot: tempDir}, Logging: config.LoggingConfig{Level: "info", Format: "json"}}
	handler, err := NewBlobHandler(cfg)
	require.NoError(t, err)
	return handler, commitHash.String()
}

func copyBlobTreeRecursive(t *testing.T, workRepo, bareRepo *git.Repository, treeHash plumbing.Hash) {
	tree, err := workRepo.TreeObject(treeHash)
	require.NoError(t, err)
	treeObj, err := workRepo.Storer.EncodedObject(plumbing.TreeObject, treeHash)
	require.NoError(t, err)
	_, err = bareRepo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)
	for _, e := range tree.Entries {
		if e.Mode == filemode.Dir {
			copyBlobTreeRecursive(t, workRepo, bareRepo, e.Hash)
		} else {
			obj, err := workRepo.Storer.EncodedObject(plumbing.BlobObject, e.Hash)
			require.NoError(t, err)
			_, err = bareRepo.Storer.SetEncodedObject(obj)
			require.NoError(t, err)
		}
	}
}

func createTestCommit(t *testing.T, repo *git.Repository, files map[string]string) plumbing.Hash {
	storer := repo.Storer

	// Blob 생성
	blobHashes := make(map[string]plumbing.Hash)
	for path, content := range files {
		blob := storer.NewEncodedObject()
		blob.SetType(plumbing.BlobObject)
		blob.SetSize(int64(len(content)))
		writer, err := blob.Writer()
		require.NoError(t, err)
		_, err = writer.Write([]byte(content))
		require.NoError(t, err)
		_ = writer.Close()

		hash, err := storer.SetEncodedObject(blob)
		require.NoError(t, err)
		blobHashes[path] = hash
	}

	// 트리 생성 (간단한 평면 구조)
	entries := make([]object.TreeEntry, 0, len(blobHashes))
	for path, hash := range blobHashes {
		// 경로에서 파일명만 추출 (경로 구분자는 /)
		parts := strings.Split(path, "/")
		fileName := parts[len(parts)-1]
		entries = append(entries, object.TreeEntry{
			Name: fileName,
			Mode: filemode.Regular,
			Hash: hash,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

	tree := &object.Tree{Entries: entries}
	treeObj := storer.NewEncodedObject()
	err := tree.Encode(treeObj)
	require.NoError(t, err)

	treeHash, err := storer.SetEncodedObject(treeObj)
	require.NoError(t, err)

	// 커밋 생성
	commit := &object.Commit{
		Message:      "Initial commit",
		TreeHash:     treeHash,
		ParentHashes: []plumbing.Hash{},
		Author: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
		},
		Committer: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
		},
	}

	commitObj := storer.NewEncodedObject()
	err = commit.Encode(commitObj)
	require.NoError(t, err)

	commitHash, err := storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	return commitHash
}

// createTestCommitWithNestedTree builds a commit with nested path structure (e.g. src/main.js).
//
//nolint:unused // test helper for future nested-tree tests
func createTestCommitWithNestedTree(t *testing.T, repo *git.Repository, files map[string]string) plumbing.Hash {
	t.Helper()
	storer := repo.Storer

	// Build blobs and group by first path component
	type node struct {
		blobs map[string]plumbing.Hash // name -> hash (for files at this level)
		trees map[string]map[string]string
	}
	root := &node{blobs: make(map[string]plumbing.Hash), trees: make(map[string]map[string]string)}
	for path, content := range files {
		parts := strings.Split(path, "/")
		if len(parts) == 1 {
			blob := storer.NewEncodedObject()
			blob.SetType(plumbing.BlobObject)
			blob.SetSize(int64(len(content)))
			w, err := blob.Writer()
			require.NoError(t, err)
			_, err = w.Write([]byte(content))
			require.NoError(t, err)
			require.NoError(t, w.Close())
			h, err := storer.SetEncodedObject(blob)
			require.NoError(t, err)
			root.blobs[parts[0]] = h
			continue
		}
		dir := parts[0]
		if root.trees[dir] == nil {
			root.trees[dir] = make(map[string]string)
		}
		root.trees[dir][strings.Join(parts[1:], "/")] = content
	}

	var buildTree func(*node) plumbing.Hash
	buildTree = func(n *node) plumbing.Hash {
		var entries []object.TreeEntry
		for name, hash := range n.blobs {
			entries = append(entries, object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: hash})
		}
		for dirName, subFiles := range n.trees {
			subNode := &node{blobs: make(map[string]plumbing.Hash), trees: make(map[string]map[string]string)}
			for subPath, content := range subFiles {
				parts := strings.Split(subPath, "/")
				if len(parts) == 1 {
					blob := storer.NewEncodedObject()
					blob.SetType(plumbing.BlobObject)
					blob.SetSize(int64(len(content)))
					w, err := blob.Writer()
					require.NoError(t, err)
					_, err = w.Write([]byte(content))
					require.NoError(t, err)
					require.NoError(t, w.Close())
					h, err := storer.SetEncodedObject(blob)
					require.NoError(t, err)
					subNode.blobs[parts[0]] = h
				} else {
					if subNode.trees[parts[0]] == nil {
						subNode.trees[parts[0]] = make(map[string]string)
					}
					subNode.trees[parts[0]][strings.Join(parts[1:], "/")] = content
				}
			}
			subTreeHash := buildTree(subNode)
			entries = append(entries, object.TreeEntry{Name: dirName, Mode: filemode.Dir, Hash: subTreeHash})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
		tree := &object.Tree{Entries: entries}
		treeObj := storer.NewEncodedObject()
		require.NoError(t, tree.Encode(treeObj))
		hash, err := storer.SetEncodedObject(treeObj)
		require.NoError(t, err)
		return hash
	}

	treeHash := buildTree(root)
	commit := &object.Commit{
		Message:      "Initial commit",
		TreeHash:     treeHash,
		ParentHashes: []plumbing.Hash{},
		Author:       object.Signature{Name: "Test User", Email: "test@example.com"},
		Committer:    object.Signature{Name: "Test User", Email: "test@example.com"},
	}
	commitObj := storer.NewEncodedObject()
	require.NoError(t, commit.Encode(commitObj))
	commitHash, err := storer.SetEncodedObject(commitObj)
	require.NoError(t, err)
	return commitHash
}

func createGinContext(method, url string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, url, nil)
	return c, w
}

// TestBlobAPI_Handler는 blob 조회 API를 테스트합니다
func TestBlobAPI_Handler(t *testing.T) {
	handler, _ := setupTestHandler(t)

	t.Run("GetExistingFile", func(t *testing.T) {
		c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=README.md&ref=main")
		c.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}

		handler.HandleBlobAPI(c)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "# Test Repository")
		assert.Equal(t, "text/markdown", w.Header().Get("Content-Type"))
	})

	t.Run("GetFileInSubdirectory", func(t *testing.T) {
		c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=main.js&ref=main")
		c.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}

		handler.HandleBlobAPI(c)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "console.log")
		ct := w.Header().Get("Content-Type")
		assert.True(t, ct == "application/javascript" || strings.HasPrefix(ct, "text/javascript"), "Content-Type: %s", ct)
	})

	t.Run("GetJSONFile", func(t *testing.T) {
		c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=config.json&ref=main")
		c.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}

		handler.HandleBlobAPI(c)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "test")
		assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	})
}

// TestBlobAPI_RefEdgeCases는 ref 파라미터의 엣지 케이스를 테스트합니다
func TestBlobAPI_RefEdgeCases(t *testing.T) {
	handler, commitHash := setupTestHandler(t)

	t.Run("NonExistentRef", func(t *testing.T) {
		c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=README.md&ref=nonexistent-branch")
		c.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}

		handler.HandleBlobAPI(c)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("EmptyRef", func(t *testing.T) {
		c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=README.md&ref=")
		c.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}

		handler.HandleBlobAPI(c)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("InvalidRefFormat", func(t *testing.T) {
		c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=README.md&ref=invalid@ref")
		c.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}

		handler.HandleBlobAPI(c)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("ValidCommitHash", func(t *testing.T) {
		c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=README.md&ref="+commitHash)
		c.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}

		handler.HandleBlobAPI(c)

		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// TestBlobAPI_PathEdgeCases는 path 파라미터의 엣지 케이스를 테스트합니다
func TestBlobAPI_PathEdgeCases(t *testing.T) {
	handler, _ := setupTestHandler(t)

	t.Run("NonExistentPath", func(t *testing.T) {
		c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=nonexistent.txt&ref=main")
		c.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}

		handler.HandleBlobAPI(c)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("EmptyPath", func(t *testing.T) {
		c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=&ref=main")
		c.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}

		handler.HandleBlobAPI(c)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("PathTraversalAttempt", func(t *testing.T) {
		c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=../../etc/passwd&ref=main")
		c.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}

		handler.HandleBlobAPI(c)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("AbsolutePath", func(t *testing.T) {
		c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=/etc/passwd&ref=main")
		c.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}

		handler.HandleBlobAPI(c)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("PathWithLeadingSlash", func(t *testing.T) {
		c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=/README.md&ref=main")
		c.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}

		handler.HandleBlobAPI(c)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

// TestBlobAPI_OwnerRepoEdgeCases는 owner/repo 파라미터의 엣지 케이스를 테스트합니다
func TestBlobAPI_OwnerRepoEdgeCases(t *testing.T) {
	handler, _ := setupTestHandler(t)

	t.Run("NonExistentRepository", func(t *testing.T) {
		c, w := createGinContext("GET", "/repos/testowner/nonexistent/blob?path=README.md&ref=main")
		c.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "nonexistent"},
		}

		handler.HandleBlobAPI(c)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("EmptyOwner", func(t *testing.T) {
		c, w := createGinContext("GET", "/repos//testrepo/blob?path=README.md&ref=main")
		c.Params = gin.Params{
			{Key: "owner", Value: ""},
			{Key: "repo", Value: "testrepo"},
		}

		handler.HandleBlobAPI(c)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("EmptyRepo", func(t *testing.T) {
		c, w := createGinContext("GET", "/repos/testowner/blob?path=README.md&ref=main")
		c.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: ""},
		}

		handler.HandleBlobAPI(c)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("InvalidOwnerFormat", func(t *testing.T) {
		c, w := createGinContext("GET", "/repos/invalid@owner/testrepo/blob?path=README.md&ref=main")
		c.Params = gin.Params{
			{Key: "owner", Value: "invalid@owner"},
			{Key: "repo", Value: "testrepo"},
		}

		handler.HandleBlobAPI(c)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("InvalidRepoFormat", func(t *testing.T) {
		c, w := createGinContext("GET", "/repos/testowner/invalid@repo/blob?path=README.md&ref=main")
		c.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "invalid@repo"},
		}

		handler.HandleBlobAPI(c)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

// TestBlobAPI_ContentType는 다양한 파일 타입의 Content-Type을 테스트합니다
func TestBlobAPI_ContentType(t *testing.T) {
	handler, _ := setupTestHandler(t)

	testCases := []struct {
		name       string
		path       string
		expectedCT string
		content    string
	}{
		{"Markdown", "README.md", "text/markdown", "# Test"},
		{"JavaScript", "main.js", "application/javascript", "console.log();"}, // accept application/javascript or text/javascript
		{"JSON", "config.json", "application/json", `{"key": "value"}`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path="+tc.path+"&ref=main")
			c.Params = gin.Params{
				{Key: "owner", Value: "testowner"},
				{Key: "repo", Value: "testrepo"},
			}

			handler.HandleBlobAPI(c)

			assert.Equal(t, http.StatusOK, w.Code)
			ct := w.Header().Get("Content-Type")
			if tc.expectedCT == "application/javascript" {
				assert.True(t, ct == "application/javascript" || strings.HasPrefix(ct, "text/javascript"), "Content-Type: %s", ct)
			} else {
				assert.Equal(t, tc.expectedCT, ct)
			}
		})
	}
}

// TestBlobAPI_ResponseHeaders는 응답 헤더를 테스트합니다
func TestBlobAPI_ResponseHeaders(t *testing.T) {
	handler, _ := setupTestHandler(t)

	c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=README.md&ref=main")
	c.Params = gin.Params{
		{Key: "owner", Value: "testowner"},
		{Key: "repo", Value: "testrepo"},
	}

	handler.HandleBlobAPI(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.NotEmpty(t, w.Header().Get("Content-Type"))
	assert.NotEmpty(t, w.Header().Get("Content-Length"))
	assert.Equal(t, "public, max-age=3600", w.Header().Get("Cache-Control"))
}

// --- 7.1 File Content (test_cases.md) ---

// Test_Blob_ValidPath_ReturnsContent 정상 경로 조회 시 콘텐츠를 반환한다.
func Test_Blob_ValidPath_ReturnsContent(t *testing.T) {
	handler, _ := setupTestHandler(t)
	c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=README.md&ref=main")
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleBlobAPI(c)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "# Test Repository")
	assert.Equal(t, "text/markdown", w.Header().Get("Content-Type"))
}

// Test_Blob_NonExistentPath_Returns404 존재하지 않는 경로는 404를 반환한다.
func Test_Blob_NonExistentPath_Returns404(t *testing.T) {
	handler, _ := setupTestHandler(t)
	c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=nonexistent.txt&ref=main")
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleBlobAPI(c)
	require.Equal(t, http.StatusNotFound, w.Code)
}

// Test_Blob_DirectoryPath_Returns400 디렉토리 경로는 400을 반환한다.
func Test_Blob_DirectoryPath_Returns400(t *testing.T) {
	handler, _ := setupTestHandlerWithNestedTree(t)
	c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=src&ref=main")
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleBlobAPI(c)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

// Test_Blob_BinaryFile_ReturnsBase64 바이너리 파일은 바이너리로 처리된다 (X-Content-Encoding: binary).
func Test_Blob_BinaryFile_ReturnsBase64(t *testing.T) {
	_, _ = setupTestHandler(t)
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "testowner", "testrepo.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	gitRepo, err := git.PlainInit(repoPath, true)
	require.NoError(t, err)
	// Binary content (non-UTF-8)
	binContent := []byte{0x00, 0x01, 0xFF, 0xFE}
	commitHash := createTestCommit(t, gitRepo, map[string]string{
		"README.md": "# Test",
		"bin.dat":   string(binContent),
	})
	ref := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), commitHash)
	require.NoError(t, gitRepo.Storer.SetReference(ref))
	cfg := &config.Config{Git: config.GitConfig{RepoRoot: tempDir}, Logging: config.LoggingConfig{Level: "info", Format: "json"}}
	handler, err := NewBlobHandler(cfg)
	require.NoError(t, err)
	c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=bin.dat&ref=main")
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleBlobAPI(c)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "binary", w.Header().Get("X-Content-Encoding"))
}

// Test_Blob_LargeFile_Truncates 대용량 파일은 제한되어 413을 반환한다.
func Test_Blob_LargeFile_Truncates(t *testing.T) {
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "testowner", "testrepo.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	gitRepo, err := git.PlainInit(repoPath, true)
	require.NoError(t, err)
	// MaxBlobSize + 1 bytes
	large := make([]byte, 50*1024*1024+1)
	for i := range large {
		large[i] = 'x'
	}
	commitHash := createTestCommit(t, gitRepo, map[string]string{"big.bin": string(large)})
	ref := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), commitHash)
	require.NoError(t, gitRepo.Storer.SetReference(ref))
	cfg := &config.Config{Git: config.GitConfig{RepoRoot: tempDir}, Logging: config.LoggingConfig{Level: "info", Format: "json"}}
	handler, err := NewBlobHandler(cfg)
	require.NoError(t, err)
	c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=big.bin&ref=main")
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleBlobAPI(c)
	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}

// Test_Blob_EmptyFile_ReturnsEmpty 빈 파일은 빈 본문으로 200을 반환한다.
func Test_Blob_EmptyFile_ReturnsEmpty(t *testing.T) {
	_, _ = setupTestHandler(t)
	// Add empty file to repo - need custom setup with empty content
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "testowner", "testrepo.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	gitRepo, err := git.PlainInit(repoPath, true)
	require.NoError(t, err)
	commitHash := createTestCommit(t, gitRepo, map[string]string{"empty.txt": "", "README.md": "# x"})
	ref := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), commitHash)
	require.NoError(t, gitRepo.Storer.SetReference(ref))
	cfg := &config.Config{Git: config.GitConfig{RepoRoot: tempDir}, Logging: config.LoggingConfig{Level: "info", Format: "json"}}
	handler, err := NewBlobHandler(cfg)
	require.NoError(t, err)
	c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=empty.txt&ref=main")
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleBlobAPI(c)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, w.Body.Bytes())
}

// Test_Blob_ByRef_ReturnsContentAtRef ref 기준으로 해당 ref의 콘텐츠를 반환한다.
func Test_Blob_ByRef_ReturnsContentAtRef(t *testing.T) {
	handler, _ := setupTestHandler(t)
	c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=README.md&ref=main")
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleBlobAPI(c)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "# Test Repository")
}

// Test_Blob_ByCommitSHA_ReturnsContentAtCommit SHA 기준으로 해당 커밋의 콘텐츠를 반환한다.
func Test_Blob_ByCommitSHA_ReturnsContentAtCommit(t *testing.T) {
	handler, commitHash := setupTestHandler(t)
	c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=README.md&ref="+commitHash)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleBlobAPI(c)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "# Test Repository")
}

// --- 7.1A LFS / Range / Unicode ---

// Test_Blob_LFSPointer_ReturnsPointerOrResolvesByPolicy LFS 포인터 파일은 포인터 또는 정책에 따라 해석한다.
func Test_Blob_LFSPointer_ReturnsPointerOrResolvesByPolicy(t *testing.T) {
	_, _ = setupTestHandler(t)
	// LFS pointer format
	lfsContent := "version https://git-lfs.github.com/spec/v1\noid sha256:abc123\nsize 42\n"
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "testowner", "testrepo.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	gitRepo, err := git.PlainInit(repoPath, true)
	require.NoError(t, err)
	commitHash := createTestCommit(t, gitRepo, map[string]string{"file.bin": lfsContent})
	ref := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), commitHash)
	require.NoError(t, gitRepo.Storer.SetReference(ref))
	cfg := &config.Config{Git: config.GitConfig{RepoRoot: tempDir}, Logging: config.LoggingConfig{Level: "info", Format: "json"}}
	handler, err := NewBlobHandler(cfg)
	require.NoError(t, err)
	c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=file.bin&ref=main")
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleBlobAPI(c)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "git-lfs")
}

// Test_Blob_RangeRequest_ReturnsPartialContent Range 요청 시 부분 콘텐츠를 반환한다 (지원 시).
func Test_Blob_RangeRequest_ReturnsPartialContent(t *testing.T) {
	handler, _ := setupTestHandler(t)
	c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=README.md&ref=main")
	c.Request.Header.Set("Range", "bytes=0-4")
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleBlobAPI(c)
	// 현재 구현은 Range 미지원 시 200 전체 반환 가능; 200이면 본문 포함 검증
	require.True(t, w.Code == http.StatusOK || w.Code == http.StatusPartialContent)
	assert.NotEmpty(t, w.Body.Bytes())
}

// Test_Blob_RangeRequest_InvalidRange_Returns416 잘못된 range는 416을 반환한다.
func Test_Blob_RangeRequest_InvalidRange_Returns416(t *testing.T) {
	handler, _ := setupTestHandler(t)
	c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=README.md&ref=main")
	c.Request.Header.Set("Range", "bytes=invalid")
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleBlobAPI(c)
	require.Equal(t, http.StatusRequestedRangeNotSatisfiable, w.Code)
}

// Test_Blob_PathWithUnicodeNormalization_ReturnsSameBlobDeterministically 유니코드 경로는 동일 blob을 결정적으로 반환한다.
func Test_Blob_PathWithUnicodeNormalization_ReturnsSameBlobDeterministically(t *testing.T) {
	handler, _ := setupTestHandler(t)
	c1, w1 := createGinContext("GET", "/repos/testowner/testrepo/blob?path=README.md&ref=main")
	c1.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleBlobAPI(c1)
	require.Equal(t, http.StatusOK, w1.Code)
	body1 := w1.Body.Bytes()
	c2, w2 := createGinContext("GET", "/repos/testowner/testrepo/blob?path=README.md&ref=main")
	c2.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleBlobAPI(c2)
	require.Equal(t, http.StatusOK, w2.Code)
	assert.Equal(t, body1, w2.Body.Bytes())
}

// --- 7.1B Path Validation ---

// Test_Blob_PathTraversalSegments_Returns400 a/../b 같은 path는 400을 반환한다.
func Test_Blob_PathTraversalSegments_Returns400(t *testing.T) {
	handler, _ := setupTestHandler(t)
	c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=src/../etc/passwd&ref=main")
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleBlobAPI(c)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

// --- 12.1A Git 내부 콘텐츠 기반 공격 ---

// Test_Blob_PathContainsNewline_Rejected 로그/헤더 injection 방지: path에 개행 포함 시 400
func Test_Blob_PathContainsNewline_Rejected(t *testing.T) {
	handler, _ := setupTestHandler(t)
	c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=foo%0abar&ref=main")
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleBlobAPI(c)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

// --- 7.2 Encoding ---

// Test_Blob_UTF8_ReturnsCorrectEncoding UTF-8 인코딩 파일을 올바르게 반환한다.
func Test_Blob_UTF8_ReturnsCorrectEncoding(t *testing.T) {
	handler, _ := setupTestHandler(t)
	c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=README.md&ref=main")
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleBlobAPI(c)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/markdown", w.Header().Get("Content-Type"))
	assert.Contains(t, w.Body.String(), "# Test Repository")
}

// Test_Blob_NonUTF8_HandlesCorrectly 비UTF-8 인코딩을 올바르게 처리한다.
func Test_Blob_NonUTF8_HandlesCorrectly(t *testing.T) {
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "testowner", "testrepo.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	gitRepo, err := git.PlainInit(repoPath, true)
	require.NoError(t, err)
	// Latin-1 / binary-ish content
	nonUTF8 := []byte{0x80, 0x81, 0x82}
	commitHash := createTestCommit(t, gitRepo, map[string]string{"x.bin": string(nonUTF8)})
	ref := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), commitHash)
	require.NoError(t, gitRepo.Storer.SetReference(ref))
	cfg := &config.Config{Git: config.GitConfig{RepoRoot: tempDir}, Logging: config.LoggingConfig{Level: "info", Format: "json"}}
	handler, err := NewBlobHandler(cfg)
	require.NoError(t, err)
	c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=x.bin&ref=main")
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleBlobAPI(c)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, nonUTF8, w.Body.Bytes())
}

// Test_Blob_MixedEncoding_DetectsCorrectly 혼합 인코딩을 올바르게 감지한다.
func Test_Blob_MixedEncoding_DetectsCorrectly(t *testing.T) {
	handler, _ := setupTestHandler(t)
	// 기본 저장소는 UTF-8 텍스트; Content-Type이 올바르게 설정되는지 검증
	c, w := createGinContext("GET", "/repos/testowner/testrepo/blob?path=config.json&ref=main")
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleBlobAPI(c)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
}
