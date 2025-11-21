package tree

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// setupTestHandler 테스트용 TreeHandler와 임시 디렉토리를 생성합니다.
// 반환값: handler, 임시 디렉토리 경로, 정리 함수
func setupTestHandler(t *testing.T) (*TreeHandler, string, func()) {
	gin.SetMode(gin.TestMode)

	// 테스트용 임시 디렉토리 생성
	tempDir, err := os.MkdirTemp("", "tree-test-*")
	require.NoError(t, err)

	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}

	handler, err := NewTreeHandler(cfg)
	require.NoError(t, err)

	// 테스트 종료 후 임시 디렉토리 정리 함수
	cleanup := func() {
		os.RemoveAll(tempDir)
	}

	return handler, tempDir, cleanup
}

// copyTreeRecursive 트리 객체와 모든 하위 트리, 블롭 객체를 재귀적으로 복사합니다.
func copyTreeRecursive(workRepo, gitRepo *gogit.Repository, treeHash plumbing.Hash) error {
	tree, err := workRepo.TreeObject(treeHash)
	if err != nil {
		return err
	}

	// 트리 객체 복사
	treeObj, err := workRepo.Storer.EncodedObject(plumbing.TreeObject, treeHash)
	if err != nil {
		return err
	}
	_, err = gitRepo.Storer.SetEncodedObject(treeObj)
	if err != nil {
		return err
	}

	// 모든 엔트리 처리
	for _, entry := range tree.Entries {
		if entry.Mode == filemode.Dir {
			// 서브트리 재귀적으로 복사
			if err := copyTreeRecursive(workRepo, gitRepo, entry.Hash); err != nil {
				return err
			}
		} else {
			// 블롭 객체 복사
			blobObj, err := workRepo.Storer.EncodedObject(plumbing.BlobObject, entry.Hash)
			if err != nil {
				return err
			}
			_, err = gitRepo.Storer.SetEncodedObject(blobObj)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// createTestRepository 테스트용 Git 저장소를 생성하고 커밋을 추가합니다.
// testFiles가 nil이면 기본 테스트 파일 목록을 사용합니다.
func createTestRepository(t *testing.T, tempDir, owner, repo string, testFiles map[string]string) string {
	repoPath := filepath.Join(tempDir, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	// 기본 테스트 파일 목록 (testFiles가 nil인 경우)
	defaultFiles := map[string]string{
		"README.md":     "# Test Repository",
		"package.json":  `{"name": "test-repo", "version": "1.0.0"}`,
		"src/main.js":   "console.log('Hello World');",
		"src/utils.js":  "export function util() {}",
		"docs/guide.md": "# Guide",
		"docs/api.md":   "# API",
	}

	if testFiles == nil {
		testFiles = defaultFiles
	}

	// 작업 디렉토리에서 일반 저장소 생성
	tempWorkDir, err := os.MkdirTemp("", "tree-test-work-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempWorkDir)

	workRepo, err := gogit.PlainInit(tempWorkDir, false)
	require.NoError(t, err)

	worktree, err := workRepo.Worktree()
	require.NoError(t, err)

	// 파일과 디렉토리 생성
	for filePath, content := range testFiles {
		if content == "" {
			// 디렉토리
			fullPath := filepath.Join(tempWorkDir, filePath)
			require.NoError(t, os.MkdirAll(fullPath, 0755))
			require.NoError(t, os.WriteFile(filepath.Join(fullPath, ".gitkeep"), []byte(""), 0644))
		} else {
			// 파일
			fullPath := filepath.Join(tempWorkDir, filePath)
			require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0755))
			require.NoError(t, os.WriteFile(fullPath, []byte(content), 0644))
		}
		_, err = worktree.Add(filePath)
		require.NoError(t, err)
	}

	commitHash, err := worktree.Commit("Test commit", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test User", Email: "test@example.com", When: time.Now()},
	})
	require.NoError(t, err)

	// 베어 저장소 생성 및 객체 복사
	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	commit, err := workRepo.CommitObject(commitHash)
	require.NoError(t, err)

	commitObj := workRepo.Storer.NewEncodedObject()
	require.NoError(t, commit.Encode(commitObj))
	_, err = gitRepo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	// 모든 트리 객체와 블롭 객체를 재귀적으로 복사
	err = copyTreeRecursive(workRepo, gitRepo, commit.TreeHash)
	require.NoError(t, err)

	mainRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), commitHash)
	require.NoError(t, gitRepo.Storer.SetReference(mainRef))

	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))
	require.NoError(t, gitRepo.Storer.SetReference(headRef))

	return repoPath
}

// createTestRepositoryWithCommits 테스트용 Git 저장소를 생성하고 초기 커밋을 추가합니다.
// 기본 테스트 파일 목록을 사용합니다.
func createTestRepositoryWithCommits(t *testing.T, tempDir, owner, repo string) string {
	return createTestRepository(t, tempDir, owner, repo, nil)
}

// createTestRepositoryWithCustomFiles 커스텀 파일 목록으로 테스트용 Git 저장소를 생성합니다.
func createTestRepositoryWithCustomFiles(t *testing.T, tempDir, owner, repo string, testFiles map[string]string) string {
	return createTestRepository(t, tempDir, owner, repo, testFiles)
}

// verifyTreeSorting 트리 항목이 Git 표준에 따라 정렬되었는지 검증합니다.
func verifyTreeSorting(t *testing.T, entries []TreeEntry) {
	var dirs, files []string
	for _, entry := range entries {
		if entry.Type == "tree" {
			dirs = append(dirs, entry.Name)
		} else if entry.Type == "blob" {
			files = append(files, entry.Name)
		}
	}

	// 디렉토리가 파일보다 앞에 있는지 검증
	if len(dirs) > 0 && len(files) > 0 {
		lastDirIndex, firstFileIndex := -1, -1
		for i, entry := range entries {
			if entry.Type == "tree" {
				lastDirIndex = i
			} else if entry.Type == "blob" && firstFileIndex == -1 {
				firstFileIndex = i
			}
		}
		assert.Less(t, lastDirIndex, firstFileIndex, "모든 디렉토리는 모든 파일보다 앞에 있어야 합니다")
	}

	// 디렉토리 알파벳 순 정렬 검증
	for i := 0; i < len(dirs)-1; i++ {
		assert.LessOrEqual(t, dirs[i], dirs[i+1],
			"디렉토리는 알파벳 순으로 정렬되어야 합니다: %s가 %s보다 앞에 있어야 합니다", dirs[i], dirs[i+1])
	}

	// 파일 알파벳 순 정렬 검증
	for i := 0; i < len(files)-1; i++ {
		assert.LessOrEqual(t, files[i], files[i+1],
			"파일은 알파벳 순으로 정렬되어야 합니다: %s가 %s보다 앞에 있어야 합니다", files[i], files[i+1])
	}
}

// TestHandleTreeAPI_Success_NonRecursive 비재귀 모드로 트리 API를 성공적으로 호출하는 경우를 테스트합니다.
// - 루트 경로(/)의 트리 항목을 반환해야 합니다
// - Git 표준에 따라 정렬되어야 합니다 (디렉토리 먼저, 그 다음 파일, 각 그룹 내 알파벳 순)
func TestHandleTreeAPI_Success_NonRecursive(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// 테스트용 저장소 생성
	createTestRepositoryWithCommits(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	router.GET("/repos/:owner/:repo/tree", handler.HandleTreeAPI)

	// 비재귀 모드로 트리 조회 요청
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 응답 검증
	assert.Equal(t, http.StatusOK, w.Code)

	var response TreeEntriesRecursiveResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.True(t, response.Success)
	assert.NotNil(t, response.Data)
	assert.Contains(t, response.Data, "/")
	assert.Greater(t, len(response.Data["/"]), 0)

	// Git 트리 표준에 따른 정렬 검증:
	// 1. 디렉토리(tree)가 먼저 나옴
	// 2. 파일(blob)이 디렉토리 다음에 나옴
	// 3. 각 그룹 내에서 알파벳 순으로 정렬됨
	entries := response.Data["/"]
	assert.Greater(t, len(entries), 0, "최소 하나의 항목이 있어야 합니다")

	// 디렉토리와 파일의 경계 찾기
	lastDirIndex := -1
	firstFileIndex := -1
	for i, entry := range entries {
		if entry.Type == "tree" {
			lastDirIndex = i
		} else if entry.Type == "blob" && firstFileIndex == -1 {
			firstFileIndex = i
		}
	}

	// 디렉토리와 파일이 모두 존재하면, 디렉토리가 파일보다 먼저 나와야 함
	if lastDirIndex >= 0 && firstFileIndex >= 0 {
		assert.Less(t, lastDirIndex, firstFileIndex,
			"모든 디렉토리는 모든 파일보다 앞에 있어야 합니다. 마지막 디렉토리 인덱스: %d, 첫 파일 인덱스: %d",
			lastDirIndex, firstFileIndex)
	}

	// 디렉토리 내 알파벳 순 정렬 검증
	if lastDirIndex >= 0 {
		for i := 0; i < lastDirIndex; i++ {
			if entries[i].Type == "tree" && entries[i+1].Type == "tree" {
				assert.LessOrEqual(t, entries[i].Name, entries[i+1].Name,
					"디렉토리는 알파벳 순으로 정렬되어야 합니다: %s가 %s보다 앞에 있어야 합니다",
					entries[i].Name, entries[i+1].Name)
			}
		}
	}

	// 파일 내 알파벳 순 정렬 검증
	if firstFileIndex >= 0 {
		for i := firstFileIndex; i < len(entries)-1; i++ {
			if entries[i].Type == "blob" && entries[i+1].Type == "blob" {
				assert.LessOrEqual(t, entries[i].Name, entries[i+1].Name,
					"파일은 알파벳 순으로 정렬되어야 합니다: %s가 %s보다 앞에 있어야 합니다",
					entries[i].Name, entries[i+1].Name)
			}
		}
	}
}

// TestHandleTreeAPI_Success_Recursive 재귀 모드로 트리 API를 성공적으로 호출하는 경우를 테스트합니다.
// - recursive=true 파라미터로 모든 하위 디렉토리의 항목을 포함하여 반환해야 합니다
func TestHandleTreeAPI_Success_Recursive(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// 테스트용 저장소 생성
	createTestRepositoryWithCommits(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	router.GET("/repos/:owner/:repo/tree", handler.HandleTreeAPI)

	// 재귀 모드로 트리 조회 요청
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main&recursive=true", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 응답 검증
	assert.Equal(t, http.StatusOK, w.Code)

	var response TreeEntriesRecursiveResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.True(t, response.Success)
	assert.NotNil(t, response.Data)
	assert.Contains(t, response.Data, "/")
}

// TestHandleTreeAPI_WithPath 특정 경로를 지정하여 트리 API를 호출하는 경우를 테스트합니다.
// - path 파라미터로 특정 디렉토리의 내용만 조회할 수 있어야 합니다
func TestHandleTreeAPI_WithPath(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// 테스트용 저장소 생성
	createTestRepositoryWithCommits(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	router.GET("/repos/:owner/:repo/tree", handler.HandleTreeAPI)

	// src 디렉토리 경로로 트리 조회 요청
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main&path=src", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 응답 검증
	assert.Equal(t, http.StatusOK, w.Code)

	var response TreeEntriesRecursiveResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.True(t, response.Success)
	assert.NotNil(t, response.Data)
	assert.Contains(t, response.Data, "src")
}

// TestHandleTreeAPI_EmptyRefDefaultsToHEAD ref 파라미터가 없을 때 기본값으로 HEAD를 사용하는 경우를 테스트합니다.
// - ref 파라미터가 없으면 자동으로 HEAD 브랜치를 조회해야 합니다
func TestHandleTreeAPI_EmptyRefDefaultsToHEAD(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// 테스트용 저장소 생성
	createTestRepositoryWithCommits(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	router.GET("/repos/:owner/:repo/tree", handler.HandleTreeAPI)

	// ref 파라미터 없이 요청 (기본값 HEAD 사용)
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 응답 검증
	assert.Equal(t, http.StatusOK, w.Code)

	var response TreeEntriesRecursiveResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.True(t, response.Success)
}

// TestHandleTreeAPI_RepositoryNotFound 존재하지 않는 저장소를 조회할 때 404 에러를 반환하는 경우를 테스트합니다.
func TestHandleTreeAPI_RepositoryNotFound(t *testing.T) {
	handler, _, cleanup := setupTestHandler(t)
	defer cleanup()

	router := gin.New()
	router.GET("/repos/:owner/:repo/tree", handler.HandleTreeAPI)

	// 존재하지 않는 저장소 조회 요청
	req := httptest.NewRequest("GET", "/repos/nonexistent/norepo/tree?ref=main", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 404 에러 응답 검증
	assert.Equal(t, http.StatusNotFound, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.False(t, response["success"].(bool))
	assert.Contains(t, response["error"].(string), "not found")
}

// TestHandleTreeAPI_PathNotFound 존재하지 않는 경로를 조회할 때 404 에러를 반환하는 경우를 테스트합니다.
func TestHandleTreeAPI_PathNotFound(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// 테스트용 저장소 생성
	createTestRepositoryWithCommits(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	router.GET("/repos/:owner/:repo/tree", handler.HandleTreeAPI)

	// 존재하지 않는 경로 조회 요청
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main&path=nonexistent/path", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 404 에러 응답 검증
	assert.Equal(t, http.StatusNotFound, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.False(t, response["success"].(bool))
	assert.Contains(t, response["error"].(string), "not found")
}

// TestHandleTreeAPI_ContextCancellation 컨텍스트가 취소되었을 때 적절한 에러를 반환하는 경우를 테스트합니다.
// - 요청 컨텍스트가 취소되면 요청 타임아웃 에러를 반환해야 합니다
func TestHandleTreeAPI_ContextCancellation(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// 테스트용 저장소 생성
	createTestRepositoryWithCommits(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	router.GET("/repos/:owner/:repo/tree", handler.HandleTreeAPI)

	// 이미 취소된 컨텍스트 생성
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main", nil)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 요청 타임아웃 에러 응답 검증
	assert.Equal(t, http.StatusRequestTimeout, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.False(t, response["success"].(bool))
	assert.Contains(t, response["error"].(string), "cancelled")
}

// TestHandleTreeAPI_InvalidRef 유효하지 않은 브랜치/참조를 조회할 때 404 에러를 반환하는 경우를 테스트합니다.
func TestHandleTreeAPI_InvalidRef(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// 테스트용 저장소 생성
	createTestRepositoryWithCommits(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	router.GET("/repos/:owner/:repo/tree", handler.HandleTreeAPI)

	// 유효하지 않은 브랜치 조회 요청
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=invalid-ref", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 404 에러 응답 검증
	assert.Equal(t, http.StatusNotFound, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.False(t, response["success"].(bool))
}

// TestHandleTreeAPI_TreeSortingOrder 트리 항목이 Git 표준에 따라 정렬되는지 테스트합니다:
// 1. 디렉토리(tree)가 먼저 나옴
// 2. 파일(blob)이 디렉토리 다음에 나옴
// 3. 각 그룹 내에서 알파벳 순(ASCII 순서)으로 정렬됨
func TestHandleTreeAPI_TreeSortingOrder(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// 의도적으로 정렬되지 않은 순서로 파일과 디렉토리 생성
	testFiles := map[string]string{
		"zzz-file.txt": "content",
		"aaa-file.txt": "content",
		"README.md":    "# Readme", // ASCII에서 'R' < 'a'
		"docs":         "",         // 디렉토리
		"src":          "",         // 디렉토리
		"aaa-dir":      "",         // 디렉토리
		"package.json": `{"name": "test"}`,
		"bbb-file.txt": "content",
	}

	createTestRepositoryWithCustomFiles(t, tempDir, "testowner", "testrepo", testFiles)

	// API 요청
	router := gin.New()
	router.GET("/repos/:owner/:repo/tree", handler.HandleTreeAPI)
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 응답 검증
	assert.Equal(t, http.StatusOK, w.Code)
	var response TreeEntriesRecursiveResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	assert.True(t, response.Success)

	// 정렬 검증
	verifyTreeSorting(t, response.Data["/"])
}
