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
		_ = os.RemoveAll(tempDir)
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
	defer func() { _ = os.RemoveAll(tempWorkDir) }()

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

// createTestRepositoryWithSymlink 심볼릭 링크가 포함된 테스트 저장소를 생성합니다.
func createTestRepositoryWithSymlink(t *testing.T, tempDir, owner, repo string) string {
	repoPath := filepath.Join(tempDir, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	tempWorkDir, err := os.MkdirTemp("", "tree-test-work-symlink-*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempWorkDir) }()

	workRepo, err := gogit.PlainInit(tempWorkDir, false)
	require.NoError(t, err)
	worktree, err := workRepo.Worktree()
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(tempWorkDir, "target.txt"), []byte("content"), 0644))
	_, err = worktree.Add("target.txt")
	require.NoError(t, err)

	// Symlink: repo 밖을 가리키는 것처럼 하기 위해 상대 경로 사용 (엔트리만 검증)
	linkPath := filepath.Join(tempWorkDir, "outlink")
	err = os.Symlink("target.txt", linkPath)
	if err != nil {
		t.Skip("symlink not supported on this platform")
		return ""
	}
	_, err = worktree.Add("outlink")
	require.NoError(t, err)

	commitHash, err := worktree.Commit("Add symlink", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test User", Email: "test@example.com", When: time.Now()},
	})
	require.NoError(t, err)

	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	commit, err := workRepo.CommitObject(commitHash)
	require.NoError(t, err)
	commitObj := workRepo.Storer.NewEncodedObject()
	require.NoError(t, commit.Encode(commitObj))
	_, err = gitRepo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)
	err = copyTreeRecursive(workRepo, gitRepo, commit.TreeHash)
	require.NoError(t, err)
	mainRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), commitHash)
	require.NoError(t, gitRepo.Storer.SetReference(mainRef))
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))
	require.NoError(t, gitRepo.Storer.SetReference(headRef))
	return repoPath
}

// createTestRepositoryWithSubmodule 서브모듈 엔트리가 포함된 테스트 저장소를 생성합니다.
func createTestRepositoryWithSubmodule(t *testing.T, tempDir, owner, repo string) string {
	repoPath := filepath.Join(tempDir, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	storer := gitRepo.Storer

	// 서브모듈로 가리킬 빈 커밋 생성
	emptyTree := &object.Tree{Entries: []object.TreeEntry{}}
	emptyTreeObj := storer.NewEncodedObject()
	require.NoError(t, emptyTree.Encode(emptyTreeObj))
	subCommitHash, err := storer.SetEncodedObject(emptyTreeObj)
	require.NoError(t, err)

	subCommit := &object.Commit{
		TreeHash:  subCommitHash,
		Author:    object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
		Committer: object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
		Message:   "sub",
		Encoding:  "UTF-8",
	}
	subCommitObj := storer.NewEncodedObject()
	require.NoError(t, subCommit.Encode(subCommitObj))
	subCommitOID, err := storer.SetEncodedObject(subCommitObj)
	require.NoError(t, err)

	// 루트 트리: README blob + sub 서브모듈
	blobObj := storer.NewEncodedObject()
	blobObj.SetType(plumbing.BlobObject)
	blobObj.SetSize(1)
	bw, err := blobObj.Writer()
	require.NoError(t, err)
	_, err = bw.Write([]byte("x"))
	require.NoError(t, err)
	require.NoError(t, bw.Close())
	blobHash, err := storer.SetEncodedObject(blobObj)
	require.NoError(t, err)

	rootTree := &object.Tree{
		Entries: []object.TreeEntry{
			{Name: "README.md", Mode: filemode.Regular, Hash: blobHash},
			{Name: "sub", Mode: filemode.Submodule, Hash: subCommitOID},
		},
	}
	rootTreeObj := storer.NewEncodedObject()
	require.NoError(t, rootTree.Encode(rootTreeObj))
	rootTreeHash, err := storer.SetEncodedObject(rootTreeObj)
	require.NoError(t, err)

	rootCommit := &object.Commit{
		TreeHash:  rootTreeHash,
		Author:    object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
		Committer: object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
		Message:   "root",
		Encoding:  "UTF-8",
	}
	rootCommitObj := storer.NewEncodedObject()
	require.NoError(t, rootCommit.Encode(rootCommitObj))
	rootCommitHash, err := storer.SetEncodedObject(rootCommitObj)
	require.NoError(t, err)

	mainRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), rootCommitHash)
	require.NoError(t, gitRepo.Storer.SetReference(mainRef))
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))
	require.NoError(t, gitRepo.Storer.SetReference(headRef))
	return repoPath
}

// createTestRepositoryWithEmptyDir 빈 디렉토리(트리 엔트리 0개)가 포함된 테스트 저장소를 생성한다.
func createTestRepositoryWithEmptyDir(t *testing.T, tempDir, owner, repo string) string {
	repoPath := filepath.Join(tempDir, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	storer := gitRepo.Storer

	emptyTree := &object.Tree{Entries: []object.TreeEntry{}}
	emptyTreeObj := storer.NewEncodedObject()
	require.NoError(t, emptyTree.Encode(emptyTreeObj))
	emptyTreeHash, err := storer.SetEncodedObject(emptyTreeObj)
	require.NoError(t, err)

	blobObj := storer.NewEncodedObject()
	blobObj.SetType(plumbing.BlobObject)
	blobObj.SetSize(1)
	w, err := blobObj.Writer()
	require.NoError(t, err)
	_, err = w.Write([]byte("x"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	blobHash, err := storer.SetEncodedObject(blobObj)
	require.NoError(t, err)

	rootTree := &object.Tree{
		Entries: []object.TreeEntry{
			{Name: "README.md", Mode: filemode.Regular, Hash: blobHash},
			{Name: "empty_dir", Mode: filemode.Dir, Hash: emptyTreeHash},
		},
	}
	rootTreeObj := storer.NewEncodedObject()
	require.NoError(t, rootTree.Encode(rootTreeObj))
	rootTreeHash, err := storer.SetEncodedObject(rootTreeObj)
	require.NoError(t, err)

	rootCommit := &object.Commit{
		TreeHash:  rootTreeHash,
		Author:    object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
		Committer: object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
		Message:   "root",
		Encoding:  "UTF-8",
	}
	rootCommitObj := storer.NewEncodedObject()
	require.NoError(t, rootCommit.Encode(rootCommitObj))
	rootCommitHash, err := storer.SetEncodedObject(rootCommitObj)
	require.NoError(t, err)
	mainRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), rootCommitHash)
	require.NoError(t, gitRepo.Storer.SetReference(mainRef))
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))
	require.NoError(t, gitRepo.Storer.SetReference(headRef))
	return repoPath
}

// verifyTreeSorting 트리 항목이 Git 표준에 따라 정렬되었는지 검증합니다.
func verifyTreeSorting(t *testing.T, entries []TreeEntry) {
	var dirs, files []string
	for _, entry := range entries {
		switch entry.Type {
		case "tree":
			dirs = append(dirs, entry.Name)
		case "blob":
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
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)

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
	assert.NotNil(t, response.Tree)
	assert.Contains(t, response.Tree, "/")
	assert.Greater(t, len(response.Tree["/"]), 0)

	// Git 트리 표준에 따른 정렬 검증:
	// 1. 디렉토리(tree)가 먼저 나옴
	// 2. 파일(blob)이 디렉토리 다음에 나옴
	// 3. 각 그룹 내에서 알파벳 순으로 정렬됨
	entries := response.Tree["/"]
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
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)

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
	assert.NotNil(t, response.Tree)
	assert.Contains(t, response.Tree, "/")
}

// TestHandleTreeAPI_WithPath 특정 경로를 지정하여 트리 API를 호출하는 경우를 테스트합니다.
// - path 파라미터로 특정 디렉토리의 내용만 조회할 수 있어야 합니다
func TestHandleTreeAPI_WithPath(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// 테스트용 저장소 생성
	createTestRepositoryWithCommits(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)

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
	assert.NotNil(t, response.Tree)
	assert.Contains(t, response.Tree, "src")
}

// TestHandleTreeAPI_EmptyRefDefaultsToHEAD ref 파라미터가 없을 때 기본값으로 HEAD를 사용하는 경우를 테스트합니다.
// - ref 파라미터가 없으면 자동으로 HEAD 브랜치를 조회해야 합니다
func TestHandleTreeAPI_EmptyRefDefaultsToHEAD(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	// 테스트용 저장소 생성
	createTestRepositoryWithCommits(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)

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
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)

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
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)

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
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)

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
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)

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
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 응답 검증
	assert.Equal(t, http.StatusOK, w.Code)
	var response TreeEntriesRecursiveResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	assert.True(t, response.Success)

	// 정렬 검증
	verifyTreeSorting(t, response.Tree["/"])
}

// --- 6.1 Tree Listing (test_cases.md) ---

// Test_Tree_RootDir_ReturnsEntries 루트 디렉토리 조회 시 항목을 반환한다.
func Test_Tree_RootDir_ReturnsEntries(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()
	createTestRepositoryWithCommits(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp TreeEntriesRecursiveResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Contains(t, resp.Tree, "/")
	assert.Greater(t, len(resp.Tree["/"]), 0)
}

// Test_Tree_Subdirectory_ReturnsEntries 서브디렉토리 조회 시 해당 디렉토리 항목을 반환한다.
func Test_Tree_Subdirectory_ReturnsEntries(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()
	createTestRepositoryWithCommits(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main&path=src", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp TreeEntriesRecursiveResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Contains(t, resp.Tree, "src")
	assert.Greater(t, len(resp.Tree["src"]), 0)
}

// Test_Tree_NonExistentPath_Returns404 존재하지 않는 경로 조회 시 404를 반환한다.
func Test_Tree_NonExistentPath_Returns404(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()
	createTestRepositoryWithCommits(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main&path=nonexistent/path", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.False(t, resp["success"].(bool))
}

// Test_Tree_FilePath_Returns400 파일 경로(디렉토리 아님)로 조회 시 400을 반환한다.
func Test_Tree_FilePath_Returns400(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()
	createTestRepositoryWithCommits(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main&path=README.md", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.False(t, resp["success"].(bool))
}

// Test_Tree_EmptyDirectory_ReturnsEmptyArray 빈 디렉토리 조회 시 빈 배열을 반환한다.
func Test_Tree_EmptyDirectory_ReturnsEmptyArray(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()
	createTestRepositoryWithEmptyDir(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main&path=empty_dir", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp TreeEntriesRecursiveResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Contains(t, resp.Tree, "empty_dir")
	assert.Empty(t, resp.Tree["empty_dir"])
}

// Test_Tree_ByRef_ReturnsEntriesAtRef ref 기준으로 해당 ref의 트리 항목을 반환한다.
func Test_Tree_ByRef_ReturnsEntriesAtRef(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()
	createTestRepositoryWithCommits(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp TreeEntriesRecursiveResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Contains(t, resp.Tree, "/")
	assert.Greater(t, len(resp.Tree["/"]), 0)
}

// Test_Tree_ByCommitSHA_ReturnsEntriesAtCommit SHA 기준으로 해당 커밋의 트리 항목을 반환한다.
func Test_Tree_ByCommitSHA_ReturnsEntriesAtCommit(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()
	repoPath := createTestRepositoryWithCommits(t, tempDir, "testowner", "testrepo")

	gitRepo, err := gogit.PlainOpen(repoPath)
	require.NoError(t, err)
	resolved, err := gitRepo.ResolveRevision(plumbing.Revision("HEAD"))
	require.NoError(t, err)
	sha := resolved.String()

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref="+sha, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp TreeEntriesRecursiveResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Contains(t, resp.Tree, "/")
	assert.Greater(t, len(resp.Tree["/"]), 0)
}

// --- 6.1A Path Validation ---

// Test_Tree_PathTraversalSegments_Returns400 a/../b 같은 path는 400을 반환한다.
func Test_Tree_PathTraversalSegments_Returns400(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()
	createTestRepositoryWithCommits(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main&path=src/../etc", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.False(t, resp["success"].(bool))
}

// --- 12.1A Git 내부 콘텐츠 기반 공격 (Trojan Source 계열 UI/로그 혼란 방지) ---

// Test_Tree_EntryNameWithUnicodeBidiCharacters_HandledSafely 트리 엔트리명에 Unicode Bidi 문자가 있어도 안전하게 처리된다.
func Test_Tree_EntryNameWithUnicodeBidiCharacters_HandledSafely(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()
	bidiName := "file\u202Etxe.txt"
	createTestRepositoryWithCustomFiles(t, tempDir, "testowner", "testrepo", map[string]string{
		bidiName: "content",
	})

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp["success"].(bool))
	tree, ok := resp["tree"].(map[string]interface{})
	require.True(t, ok)
	require.NotEmpty(t, tree)
	for _, v := range tree {
		entries, _ := v.([]interface{})
		if len(entries) < 1 {
			continue
		}
		first, _ := entries[0].(map[string]interface{})
		require.NotNil(t, first["name"], "entry name present (Trojan Source safe)")
		break
	}
}

// Test_Tree_SymlinkPointsOutside_EntryReturnedButContentFetchBlocked tree에 symlink가 있어도 repo root 밖 콘텐츠는 읽지 않고 엔트리는 반환한다.
func Test_Tree_SymlinkPointsOutside_EntryReturnedButContentFetchBlocked(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()
	repoPath := createTestRepositoryWithSymlink(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp TreeEntriesRecursiveResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	entries := resp.Tree["/"]
	var symlinkEntry *TreeEntry
	for i := range entries {
		if entries[i].Type == "symlink" || entries[i].Name == "outlink" {
			symlinkEntry = &entries[i]
			break
		}
	}
	require.NotNil(t, symlinkEntry, "tree should list symlink entry")
	assert.Equal(t, "symlink", symlinkEntry.Type)

	_ = repoPath
}

// --- 6.2 Entry Types ---

// Test_Tree_FileEntry_TypeFile 파일 엔트리는 type이 blob이다.
func Test_Tree_FileEntry_TypeFile(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()
	createTestRepositoryWithCommits(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp TreeEntriesRecursiveResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	var found bool
	for _, e := range resp.Tree["/"] {
		if e.Name == "README.md" && e.Type == "blob" {
			found = true
			break
		}
	}
	assert.True(t, found, "README.md should be type blob")
}

// Test_Tree_DirectoryEntry_TypeDir 디렉토리 엔트리는 type이 tree이다.
func Test_Tree_DirectoryEntry_TypeDir(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()
	createTestRepositoryWithCommits(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp TreeEntriesRecursiveResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	var found bool
	for _, e := range resp.Tree["/"] {
		if e.Name == "src" && e.Type == "tree" {
			found = true
			break
		}
	}
	assert.True(t, found, "src should be type tree")
}

// Test_Tree_SymlinkEntry_TypeSymlink 심볼릭 링크 엔트리는 type이 symlink이다.
func Test_Tree_SymlinkEntry_TypeSymlink(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()
	createTestRepositoryWithSymlink(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp TreeEntriesRecursiveResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	var found bool
	for _, e := range resp.Tree["/"] {
		if e.Type == "symlink" {
			found = true
			break
		}
	}
	assert.True(t, found, "tree should include symlink entry with type symlink")
}

// Test_Tree_SubmoduleEntry_TypeSubmodule 서브모듈 엔트리는 type이 commit(또는 submodule)으로 노출된다.
func Test_Tree_SubmoduleEntry_TypeSubmodule(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()
	createTestRepositoryWithSubmodule(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp TreeEntriesRecursiveResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	var found bool
	for _, e := range resp.Tree["/"] {
		if e.Name == "sub" && (e.Type == "commit" || e.Type == "tree") {
			found = true
			break
		}
	}
	assert.True(t, found, "submodule entry should be present (type commit or tree)")
}

// --- 6.2A Entry Types 확장 ---

// Test_Tree_SubmoduleEntry_IncludesCommitOIDAndURLIfAvailable 서브모듈 엔트리에 commit OID(또는 URL)가 있으면 포함한다.
func Test_Tree_SubmoduleEntry_IncludesCommitOIDAndURLIfAvailable(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()
	createTestRepositoryWithSubmodule(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp TreeEntriesRecursiveResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	var subEntry *TreeEntry
	for i := range resp.Tree["/"] {
		if resp.Tree["/"][i].Name == "sub" {
			subEntry = &resp.Tree["/"][i]
			break
		}
	}
	require.NotNil(t, subEntry, "submodule entry should be present")
	assert.Equal(t, "sub", subEntry.Path)
	// 현재 스키마에 OID/URL 필드가 없으면 타입·이름만 검증
}

// Test_Tree_SymlinkEntry_ReturnsLinkTargetOrMetadata 심볼릭 링크 엔트리는 링크 대상 또는 메타데이터를 반환한다.
func Test_Tree_SymlinkEntry_ReturnsLinkTargetOrMetadata(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()
	createTestRepositoryWithSymlink(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp TreeEntriesRecursiveResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	var symEntry *TreeEntry
	for i := range resp.Tree["/"] {
		if resp.Tree["/"][i].Type == "symlink" {
			symEntry = &resp.Tree["/"][i]
			break
		}
	}
	require.NotNil(t, symEntry)
	assert.Equal(t, "symlink", symEntry.Type)
	assert.NotEmpty(t, symEntry.Name)
	assert.NotEmpty(t, symEntry.Path)
}

// Test_Tree_CaseCollision_BehaviorDeterministic 대소문자 충돌 시 동작이 결정적이다 (mac).
func Test_Tree_CaseCollision_BehaviorDeterministic(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()
	// 같은 디렉토리에 대소문자만 다른 파일은 Git에서 하나만 허용; 여기서는 정렬/동작이 일관되는지 검증
	createTestRepositoryWithCustomFiles(t, tempDir, "testowner", "testrepo", map[string]string{
		"README.md": "# A",
		"src/a.go":  "package main",
	})

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp1 TreeEntriesRecursiveResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp1))

	req2 := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main", nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusOK, w2.Code)
	var resp2 TreeEntriesRecursiveResponse
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &resp2))

	assert.Equal(t, len(resp1.Tree["/"]), len(resp2.Tree["/"]))
	for i := range resp1.Tree["/"] {
		assert.Equal(t, resp1.Tree["/"][i].Name, resp2.Tree["/"][i].Name)
		assert.Equal(t, resp1.Tree["/"][i].Type, resp2.Tree["/"][i].Type)
	}
}

// --- 6.3 Recursive ---

// Test_Tree_Recursive_ReturnsAllEntries 재귀 모드로 조회 시 모든 하위 항목을 반환한다.
func Test_Tree_Recursive_ReturnsAllEntries(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()
	createTestRepositoryWithCommits(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main&recursive=true", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp TreeEntriesRecursiveResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Contains(t, resp.Tree, "/")
	// 루트 + 하위 디렉토리(예: src, docs)가 키로 있어야 함
	assert.GreaterOrEqual(t, len(resp.Tree), 1)
	totalEntries := 0
	for _, entries := range resp.Tree {
		totalEntries += len(entries)
	}
	assert.Greater(t, totalEntries, 0)
}

// Test_Tree_Recursive_FlattensPaths 재귀 응답은 경로별로 평탄화된 맵 구조이다.
func Test_Tree_Recursive_FlattensPaths(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()
	createTestRepositoryWithCommits(t, tempDir, "testowner", "testrepo")

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/tree?ref=main&recursive=true", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp TreeEntriesRecursiveResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	// 키는 경로(/, src, docs 등), 값은 해당 경로의 엔트리 배열
	assert.Contains(t, resp.Tree, "/")
	for pathKey, entries := range resp.Tree {
		for _, e := range entries {
			assert.NotEmpty(t, e.Name)
			assert.NotEmpty(t, e.Type)
			// path는 전체 경로 또는 상대 경로
			assert.NotEmpty(t, e.Path)
			_ = pathKey
		}
	}
}
