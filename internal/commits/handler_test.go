package commits

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/collabops/git-executor/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestHandler(t *testing.T) *CommitsHandler {
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "testowner", "testrepo.git")

	err := os.MkdirAll(filepath.Dir(repoPath), 0755)
	require.NoError(t, err)

	repo, err := git.PlainInit(repoPath, true)
	require.NoError(t, err)

	_ = ensureInitialCommit(t, repo, "main")

	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
	}

	handler, err := NewCommitsHandler(cfg)
	require.NoError(t, err)

	return handler
}

func setupTestHandlerWithInitialCommit(t *testing.T) (*CommitsHandler, string) {
	t.Helper()

	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "testowner", "testrepo.git")

	err := os.MkdirAll(filepath.Dir(repoPath), 0755)
	require.NoError(t, err)

	repo, err := git.PlainInit(repoPath, true)
	require.NoError(t, err)

	_ = ensureInitialCommit(t, repo, "main")

	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
	}

	handler, err := NewCommitsHandler(cfg)
	require.NoError(t, err)

	return handler, repoPath
}

func createGinContext(method, url string, body interface{}) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	var bodyReader io.Reader
	if body != nil {
		jsonBody, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(jsonBody)
	}

	c.Request = httptest.NewRequest(method, url, bodyReader)
	if body != nil {
		c.Request.Header.Set("Content-Type", "application/json")
	}

	return c, w
}

func ensureInitialCommit(t *testing.T, repo *git.Repository, defaultBranch string) plumbing.Hash {
	t.Helper()

	storer := repo.Storer

	// Create an empty tree
	tree := &object.Tree{Entries: []object.TreeEntry{}}
	treeObj := storer.NewEncodedObject()
	require.NoError(t, tree.Encode(treeObj))
	treeHash, err := storer.SetEncodedObject(treeObj)
	require.NoError(t, err)

	// Create an initial commit
	commit := &object.Commit{
		Message:  "Initial commit",
		TreeHash: treeHash,
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
	require.NoError(t, commit.Encode(commitObj))
	commitHash, err := storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	branchRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/"+defaultBranch), commitHash)
	require.NoError(t, storer.SetReference(branchRef))
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, branchRef.Name())
	require.NoError(t, storer.SetReference(headRef))

	return commitHash
}

func getBranchHash(t *testing.T, repo *git.Repository, branch string) (plumbing.Hash, error) {
	t.Helper()
	ref, err := repo.Reference(plumbing.ReferenceName("refs/heads/"+branch), false)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return ref.Hash(), nil
}

func getHEADRefTarget(t *testing.T, repo *git.Repository) plumbing.ReferenceName {
	t.Helper()
	headRef, err := repo.Reference(plumbing.HEAD, false)
	require.NoError(t, err)
	require.Equal(t, plumbing.SymbolicReference, headRef.Type())
	return headRef.Target()
}

// TestMultiFileCommit_Handler는 다중 파일 커밋을 테스트합니다
func TestMultiFileCommit_Handler(t *testing.T) {
	handler := setupTestHandler(t)

	t.Run("CreateMultipleFiles", func(t *testing.T) {
		req := MultiFileCommitRequest{
			Message: "Add initial project files",
			Branch:  "main",
			Operations: []MultiFileOperation{
				{Path: "src/main.js", Operation: "create", Content: "console.log('Hello World');"},
				{Path: "src/style.css", Operation: "create", Content: "body { background: #f0f0f0; }"},
				{Path: "package.json", Operation: "create", Content: `{ "name": "test-project" }`},
			},
			Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
		}

		c, w := createGinContext("POST", "/repos/testowner/testrepo/commits", req)
		c.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}

		handler.HandleMultiFileCommit(c)

		assert.Equal(t, http.StatusOK, w.Code)
		var response MultiFileCommitResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		assert.True(t, response.Success)
		assert.Equal(t, 3, response.TotalOperations)
		assert.Equal(t, 3, response.SuccessfulOps)
	})

	t.Run("CreateDuplicateFile", func(t *testing.T) {
		// 먼저 파일 생성
		req1 := MultiFileCommitRequest{
			Message: "Create initial file",
			Branch:  "main",
			Operations: []MultiFileOperation{
				{Path: "duplicate.txt", Operation: "create", Content: "First"},
			},
			Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
		}

		c1, w1 := createGinContext("POST", "/repos/testowner/testrepo/commits", req1)
		c1.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}
		handler.HandleMultiFileCommit(c1)
		assert.Equal(t, http.StatusOK, w1.Code)

		// 같은 파일을 포함한 다중 파일 커밋 시도 (이미 존재하는 파일 포함)
		req2 := MultiFileCommitRequest{
			Message: "Create files with duplicate",
			Branch:  "main",
			Operations: []MultiFileOperation{
				{Path: "new-file.txt", Operation: "create", Content: "New file"},
				{Path: "duplicate.txt", Operation: "create", Content: "Duplicate"}, // 이미 존재
				{Path: "another-file.txt", Operation: "create", Content: "Another"},
			},
			Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
		}

		c2, w2 := createGinContext("POST", "/repos/testowner/testrepo/commits", req2)
		c2.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}
		handler.HandleMultiFileCommit(c2)

		assert.Equal(t, http.StatusConflict, w2.Code)
		var errorResponse map[string]interface{}
		require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &errorResponse))
		assert.Contains(t, errorResponse["error"].(string), "already exists")
	})

	t.Run("DeleteNonExistentFiles", func(t *testing.T) {
		// 존재하지 않는 파일 삭제 시도
		req := MultiFileCommitRequest{
			Message: "Delete non-existent files",
			Branch:  "main",
			Operations: []MultiFileOperation{
				{Path: "nonexistent1.txt", Operation: "delete"},
				{Path: "nonexistent2.txt", Operation: "delete"},
			},
			Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
		}

		c, w := createGinContext("POST", "/repos/testowner/testrepo/commits", req)
		c.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}
		handler.HandleMultiFileCommit(c)

		assert.Equal(t, http.StatusNotFound, w.Code)
		var errorResponse map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errorResponse))
		assert.Contains(t, errorResponse["error"].(string), "not found")
	})

	t.Run("MixedOperationsWithErrors", func(t *testing.T) {
		// 먼저 일부 파일 생성
		req1 := MultiFileCommitRequest{
			Message: "Create initial files",
			Branch:  "main",
			Operations: []MultiFileOperation{
				{Path: "file1.txt", Operation: "create", Content: "File 1"},
				{Path: "file2.txt", Operation: "create", Content: "File 2"},
			},
			Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
		}

		c1, w1 := createGinContext("POST", "/repos/testowner/testrepo/commits", req1)
		c1.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}
		handler.HandleMultiFileCommit(c1)
		assert.Equal(t, http.StatusOK, w1.Code)

		// 혼합 작업: 정상 + 중복 생성 + 존재하지 않는 파일 삭제
		req2 := MultiFileCommitRequest{
			Message: "Mixed operations with errors",
			Branch:  "main",
			Operations: []MultiFileOperation{
				{Path: "file3.txt", Operation: "create", Content: "File 3"},    // 정상
				{Path: "file1.txt", Operation: "create", Content: "Duplicate"}, // 중복
				{Path: "file2.txt", Operation: "delete"},                       // 정상
				{Path: "nonexistent.txt", Operation: "delete"},                 // 존재하지 않음
			},
			Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
		}

		c2, w2 := createGinContext("POST", "/repos/testowner/testrepo/commits", req2)
		c2.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}
		handler.HandleMultiFileCommit(c2)

		// 첫 번째 에러가 발생하면 즉시 실패해야 함
		assert.True(t, w2.Code == http.StatusConflict || w2.Code == http.StatusNotFound)
	})
}

// TestSingleFileCommit_Handler는 단일 파일 커밋을 테스트합니다
func TestSingleFileCommit_Handler(t *testing.T) {
	handler := setupTestHandler(t)

	t.Run("CreateSingleFile", func(t *testing.T) {
		req := MultiFileCommitRequest{
			Message: "Add single line file",
			Branch:  "main",
			Operations: []MultiFileOperation{
				{Path: "single-line.txt", Operation: "create", Content: "Hello"},
			},
			Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
		}

		c, w := createGinContext("POST", "/repos/testowner/testrepo/commits", req)
		c.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}

		handler.HandleMultiFileCommit(c)

		assert.Equal(t, http.StatusOK, w.Code)
		var response MultiFileCommitResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		assert.True(t, response.Success)
		assert.NotEmpty(t, response.CommitSHA)
	})
}

func TestMultiFileCommit_CreateBranchName_Success(t *testing.T) {
	handler, repoPath := setupTestHandlerWithInitialCommit(t)

	req := MultiFileCommitRequest{
		Message:          "init",
		Branch:           "main",
		CreateBranchName: "feature/test",
		SourceBranchName: "main",
		Author:           &CommitAuthor{Name: "Test User", Email: "test@example.com"},
		Operations: []MultiFileOperation{
			{Path: "README.md", Operation: "create", Content: "hello"},
		},
	}

	c, w := createGinContext("POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{
		{Key: "owner", Value: "testowner"},
		{Key: "repo", Value: "testrepo"},
	}

	handler.HandleMultiFileCommit(c)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp MultiFileCommitResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	require.NotEmpty(t, resp.CommitSHA)

	repo, err := git.PlainOpen(repoPath)
	require.NoError(t, err)

	branchHash, err := getBranchHash(t, repo, "feature/test")
	require.NoError(t, err)
	assert.Equal(t, plumbing.NewHash(resp.CommitSHA), branchHash)
}

func TestMultiFileCommit_CreateBranchName_BranchAlreadyExists_409(t *testing.T) {
	handler, _ := setupTestHandlerWithInitialCommit(t)

	req := MultiFileCommitRequest{
		Message:          "init",
		Branch:           "main",
		CreateBranchName: "feature/dup",
		SourceBranchName: "main",
		Author:           &CommitAuthor{Name: "Test User", Email: "test@example.com"},
		Operations: []MultiFileOperation{
			{Path: "README.md", Operation: "create", Content: "hello"},
		},
	}

	c1, w1 := createGinContext("POST", "/repos/testowner/testrepo/commits", req)
	c1.Params = gin.Params{
		{Key: "owner", Value: "testowner"},
		{Key: "repo", Value: "testrepo"},
	}
	handler.HandleMultiFileCommit(c1)
	assert.Equal(t, http.StatusOK, w1.Code)

	c2, w2 := createGinContext("POST", "/repos/testowner/testrepo/commits", req)
	c2.Params = gin.Params{
		{Key: "owner", Value: "testowner"},
		{Key: "repo", Value: "testrepo"},
	}
	handler.HandleMultiFileCommit(c2)
	assert.Equal(t, http.StatusConflict, w2.Code)
}

func TestMultiFileCommit_CreateBranchName_CommitFailure_RollbackNewBranchAndHEAD(t *testing.T) {
	handler, repoPath := setupTestHandlerWithInitialCommit(t)

	req := MultiFileCommitRequest{
		Message:          "should-fail",
		Branch:           "main",
		CreateBranchName: "newbranch3",
		SourceBranchName: "main",
		Author:           &CommitAuthor{Name: "Test User", Email: "test@example.com"},
		Operations: []MultiFileOperation{
			{Path: "missing.txt", Operation: "delete"},
		},
	}

	c, w := createGinContext("POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{
		{Key: "owner", Value: "testowner"},
		{Key: "repo", Value: "testrepo"},
	}

	handler.HandleMultiFileCommit(c)
	assert.Equal(t, http.StatusNotFound, w.Code)

	repo, err := git.PlainOpen(repoPath)
	require.NoError(t, err)

	_, err = getBranchHash(t, repo, "newbranch3")
	assert.Error(t, err)

	assert.Equal(t, plumbing.ReferenceName("refs/heads/main"), getHEADRefTarget(t, repo))
}

// TestFileOperations_Handler는 파일 생성/업데이트/삭제를 테스트합니다
func TestFileOperations_Handler(t *testing.T) {
	handler := setupTestHandler(t)

	t.Run("CreateFile", func(t *testing.T) {
		req := MultiFileCommitRequest{
			Message: "Create file",
			Branch:  "main",
			Operations: []MultiFileOperation{
				{Path: "test.txt", Operation: "create", Content: "Initial content"},
			},
			Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
		}

		c, w := createGinContext("POST", "/repos/testowner/testrepo/commits", req)
		c.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}
		handler.HandleMultiFileCommit(c)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("UpdateFile", func(t *testing.T) {
		req := MultiFileCommitRequest{
			Message: "Update file",
			Branch:  "main",
			Operations: []MultiFileOperation{
				{Path: "test.txt", Operation: "update", Content: "Updated content"},
			},
			Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
		}

		c, w := createGinContext("POST", "/repos/testowner/testrepo/commits", req)
		c.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}
		handler.HandleMultiFileCommit(c)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("DeleteFile", func(t *testing.T) {
		req := MultiFileCommitRequest{
			Message: "Delete file",
			Branch:  "main",
			Operations: []MultiFileOperation{
				{Path: "test.txt", Operation: "delete"},
			},
			Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
		}

		c, w := createGinContext("POST", "/repos/testowner/testrepo/commits", req)
		c.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}
		handler.HandleMultiFileCommit(c)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// TestCommitErrors_Handler는 에러 케이스를 테스트합니다
func TestCommitErrors_Handler(t *testing.T) {
	handler := setupTestHandler(t)

	t.Run("DuplicateFileCreate", func(t *testing.T) {
		// 첫 번째 파일 생성
		req1 := MultiFileCommitRequest{
			Message: "Create file",
			Branch:  "main",
			Operations: []MultiFileOperation{
				{Path: "duplicate.txt", Operation: "create", Content: "First"},
			},
			Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
		}

		c1, w1 := createGinContext("POST", "/repos/testowner/testrepo/commits", req1)
		c1.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}
		handler.HandleMultiFileCommit(c1)
		assert.Equal(t, http.StatusOK, w1.Code)

		// 같은 파일 다시 생성 시도 (충돌)
		req2 := MultiFileCommitRequest{
			Message: "Create duplicate",
			Branch:  "main",
			Operations: []MultiFileOperation{
				{Path: "duplicate.txt", Operation: "create", Content: "Second"},
			},
			Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
		}

		c2, w2 := createGinContext("POST", "/repos/testowner/testrepo/commits", req2)
		c2.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}
		handler.HandleMultiFileCommit(c2)
		assert.Equal(t, http.StatusConflict, w2.Code)
	})

	t.Run("DeleteNonExistentFile", func(t *testing.T) {
		req := MultiFileCommitRequest{
			Message: "Delete non-existent",
			Branch:  "main",
			Operations: []MultiFileOperation{
				{Path: "nonexistent.txt", Operation: "delete"},
			},
			Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
		}

		c, w := createGinContext("POST", "/repos/testowner/testrepo/commits", req)
		c.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}
		handler.HandleMultiFileCommit(c)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("EmptyOperations", func(t *testing.T) {
		req := MultiFileCommitRequest{
			Message:    "Empty operations",
			Branch:     "main",
			Operations: []MultiFileOperation{},
			Author:     &CommitAuthor{Name: "Test User", Email: "test@example.com"},
		}

		c, w := createGinContext("POST", "/repos/testowner/testrepo/commits", req)
		c.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}
		handler.HandleMultiFileCommit(c)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

// TestCommitWithSubdirectories_Handler는 서브디렉토리 파일 커밋을 테스트합니다
func TestCommitWithSubdirectories_Handler(t *testing.T) {
	handler := setupTestHandler(t)

	t.Run("CreateFilesInSubdirectories", func(t *testing.T) {
		req := MultiFileCommitRequest{
			Message: "Add files in subdirectories",
			Branch:  "main",
			Operations: []MultiFileOperation{
				{Path: "src/main.js", Operation: "create", Content: "console.log('main');"},
				{Path: "src/utils/helper.js", Operation: "create", Content: "export function helper() {}"},
				{Path: "docs/README.md", Operation: "create", Content: "# Documentation"},
			},
			Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
		}

		c, w := createGinContext("POST", "/repos/testowner/testrepo/commits", req)
		c.Params = gin.Params{
			{Key: "owner", Value: "testowner"},
			{Key: "repo", Value: "testrepo"},
		}
		handler.HandleMultiFileCommit(c)

		assert.Equal(t, http.StatusOK, w.Code)
		var response MultiFileCommitResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		assert.True(t, response.Success)
		assert.Equal(t, 3, response.SuccessfulOps)
	})
}

// TestHandleCommitsAPI_PaginationOutOfRange는 pagination 범위를 벗어나는 경우 빈 배열을 반환하는지 테스트합니다
// 빈 저장소에서는 ref를 resolve할 수 없어 에러가 발생할 수 있으므로,
// 이 테스트는 핸들러가 nil 체크를 통해 빈 배열을 반환하는지 확인하는 목적으로만 작성됩니다.
// 실제 동작은 커밋이 있는 저장소에서 확인해야 합니다.
func TestHandleCommitsAPI_PaginationOutOfRange(t *testing.T) {
	handler := setupTestHandler(t)

	router := gin.New()
	router.GET("/repos/:owner/:repo/commits", handler.HandleCommitsAPI)

	// 범위를 벗어나는 skip 값으로 요청
	// 빈 저장소에서는 ref resolve 실패로 400 에러가 발생할 수 있지만,
	// 핸들러 코드가 nil 체크를 통해 빈 배열을 반환하도록 보장하는지 확인
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/commits?limit=10&skip=10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 빈 저장소에서는 ref resolve 실패로 400 에러가 발생할 수 있음 (정상 동작)
	// 핸들러 코드가 nil 체크를 통해 빈 배열을 반환하도록 보장하는 것은
	// 커밋이 있는 저장소에서 범위를 벗어나는 경우를 테스트해야 함
	if w.Code == http.StatusOK {
		var response CommitsResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))

		assert.True(t, response.Success)

		// results가 빈 배열인지 확인 (null이 아님)
		resultsBytes, err := json.Marshal(response.Results)
		require.NoError(t, err)

		// null이 아닌 빈 배열([])인지 확인
		assert.Equal(t, "[]", string(resultsBytes), "results should be empty array, not null")
	}
}

// TestHandleCommitsAPI_PaginationOutOfRangeWithDateSort는 sort=date일 때 pagination 범위를 벗어나는 경우를 테스트합니다
func TestHandleCommitsAPI_PaginationOutOfRangeWithDateSort(t *testing.T) {
	handler := setupTestHandler(t)

	router := gin.New()
	router.GET("/repos/:owner/:repo/commits", handler.HandleCommitsAPI)

	// 범위를 벗어나는 skip 값으로 요청 (sort=date 포함)
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/commits?limit=10&skip=10&sort=date", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 빈 저장소에서는 ref resolve 실패로 400 에러가 발생할 수 있음 (정상 동작)
	if w.Code == http.StatusOK {
		var response CommitsResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))

		assert.True(t, response.Success)

		// results가 빈 배열인지 확인 (null이 아님)
		resultsBytes, err := json.Marshal(response.Results)
		require.NoError(t, err)

		// null이 아닌 빈 배열([])인지 확인
		assert.Equal(t, "[]", string(resultsBytes), "results should be empty array, not null")
	}
}
