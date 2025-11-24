package blob

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
		writer.Close()

		hash, err := storer.SetEncodedObject(blob)
		require.NoError(t, err)
		blobHashes[path] = hash
	}

	// 트리 생성 (간단한 평면 구조)
	entries := make([]object.TreeEntry, 0, len(blobHashes))
	for path, hash := range blobHashes {
		// 경로에서 파일명만 추출
		parts := filepath.SplitList(path)
		fileName := parts[len(parts)-1]
		entries = append(entries, object.TreeEntry{
			Name: fileName,
			Mode: filemode.Regular,
			Hash: hash,
		})
	}

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
		assert.Equal(t, "application/javascript", w.Header().Get("Content-Type"))
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
		{"JavaScript", "main.js", "application/javascript", "console.log();"},
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
			assert.Equal(t, tc.expectedCT, w.Header().Get("Content-Type"))
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
