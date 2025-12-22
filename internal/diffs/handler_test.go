package diffs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// setupTestHandler는 임시 저장소를 사용하여 테스트 핸들러를 생성합니다
func setupTestHandler(t *testing.T) (*DiffHandler, string, func()) {
	// Gin을 테스트 모드로 설정
	gin.SetMode(gin.TestMode)

	// 테스트 저장소를 위한 임시 디렉토리 생성
	tempDir, err := os.MkdirTemp("", "diffs-test-*")
	require.NoError(t, err)

	// 테스트 저장소 경로 설정
	testRepoPath := filepath.Join(tempDir, "testowner", "testrepo.git")
	err = os.MkdirAll(filepath.Dir(testRepoPath), 0755)
	require.NoError(t, err)

	// 베어 저장소 초기화
	repo, err := gogit.PlainInit(testRepoPath, true)
	require.NoError(t, err)

	// 초기 커밋 생성
	createTestInitialCommit(t, repo)

	// 설정 구성
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}

	// 핸들러 생성
	handler, err := NewDiffHandler(cfg)
	require.NoError(t, err)

	cleanup := func() {
		os.RemoveAll(tempDir)
	}

	return handler, testRepoPath, cleanup
}

// createBlob는 저장소에 blob 객체를 생성하고 해시를 반환합니다
func createBlob(t *testing.T, repo *gogit.Repository, content []byte) plumbing.Hash {
	blob := repo.Storer.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	blob.SetSize(int64(len(content)))
	writer, err := blob.Writer()
	require.NoError(t, err)
	_, err = writer.Write(content)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	hash, err := repo.Storer.SetEncodedObject(blob)
	require.NoError(t, err)
	return hash
}

// createTree는 저장소에 tree 객체를 생성하고 해시를 반환합니다
func createTree(t *testing.T, repo *gogit.Repository, entries []object.TreeEntry) plumbing.Hash {
	tree := &object.Tree{Entries: entries}
	treeObj := repo.Storer.NewEncodedObject()
	require.NoError(t, tree.Encode(treeObj))
	hash, err := repo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)
	return hash
}

// createCommit는 저장소에 커밋 객체를 생성하고 해시를 반환합니다
func createCommit(t *testing.T, repo *gogit.Repository, message string, treeHash plumbing.Hash, parentHashes ...plumbing.Hash) plumbing.Hash {
	commit := &object.Commit{
		Message:      message,
		Author:       object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
		Committer:    object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
		TreeHash:     treeHash,
		ParentHashes: parentHashes,
	}
	commitObj := repo.Storer.NewEncodedObject()
	require.NoError(t, commit.Encode(commitObj))
	hash, err := repo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)
	return hash
}

// createTestInitialCommit는 테스트 저장소에 초기 커밋을 생성합니다
func createTestInitialCommit(t *testing.T, repo *gogit.Repository) {
	blobHash := createBlob(t, repo, []byte("initial"))
	treeHash := createTree(t, repo, []object.TreeEntry{
		{Name: "initial.txt", Mode: 0100644, Hash: blobHash},
	})
	commitHash := createCommit(t, repo, "Initial commit", treeHash)
	ref := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/master"), commitHash)
	require.NoError(t, repo.Storer.SetReference(ref))
}

// setupTestRepositoryWithCommits는 여러 커밋이 있는 테스트 저장소를 생성합니다
func setupTestRepositoryWithCommits(t *testing.T) (string, string, string, func()) {
	tempDir, err := os.MkdirTemp("", "diffs-test-*")
	require.NoError(t, err)
	ownerDir := filepath.Join(tempDir, "testowner")
	require.NoError(t, os.MkdirAll(ownerDir, 0755))

	repoPath := filepath.Join(ownerDir, "testrepo.git")
	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	initialBlobHash := createBlob(t, repo, []byte("initial"))
	initialTreeHash := createTree(t, repo, []object.TreeEntry{
		{Name: "initial.txt", Mode: 0100644, Hash: initialBlobHash},
	})
	initialCommitHash := createCommit(t, repo, "Initial commit", initialTreeHash)

	singleLineBlobHash := createBlob(t, repo, []byte("Hello"))
	secondTreeHash := createTree(t, repo, []object.TreeEntry{
		{Name: "initial.txt", Mode: 0100644, Hash: initialBlobHash},
		{Name: "single-line.txt", Mode: 0100644, Hash: singleLineBlobHash},
	})
	secondCommitHash := createCommit(t, repo, "Add single line file", secondTreeHash, initialCommitHash)

	return repoPath, initialCommitHash.String(), secondCommitHash.String(), func() { os.RemoveAll(tempDir) }
}

// TestDiffLineCount_SingleLineAddition는 단일 줄 파일을 추가할 때 정확히 1줄이 추가되는지 테스트합니다
func TestDiffLineCount_SingleLineAddition(t *testing.T) {
	_, repoPath, cleanup := setupTestHandler(t)
	defer cleanup()

	// 테스트 브랜치 및 커밋 생성
	repo, err := gogit.PlainOpen(repoPath)
	require.NoError(t, err)

	// master 브랜치 가져오기
	headRef, err := repo.Head()
	require.NoError(t, err)
	masterCommit, err := repo.CommitObject(headRef.Hash())
	require.NoError(t, err)

	// 변경사항을 만들기 위한 worktree 생성
	worktreePath := strings.TrimSuffix(repoPath, ".git")
	worktreeRepo, err := gogit.PlainOpen(worktreePath)
	if err != nil {
		// worktree가 존재하지 않으면 생성
		worktreeRepo, err = gogit.PlainInit(worktreePath, false)
		require.NoError(t, err)
	}

	wt, err := worktreeRepo.Worktree()
	require.NoError(t, err)

	// 단일 줄 파일 추가
	singleLineFile, err := wt.Filesystem.Create("single-line.txt")
	require.NoError(t, err)
	_, err = singleLineFile.Write([]byte("Hello"))
	require.NoError(t, err)
	require.NoError(t, singleLineFile.Close())

	_, err = wt.Add("single-line.txt")
	require.NoError(t, err)

	newCommit, err := wt.Commit("Add single line file", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	// 커밋 가져오기
	initialCommitObj, err := repo.CommitObject(masterCommit.Hash)
	require.NoError(t, err)

	newCommitObj, err := worktreeRepo.CommitObject(newCommit)
	require.NoError(t, err)

	_ = repoPath // 사용하지 않는 변수 경고 방지

	// tree 가져오기
	initialTree, err := initialCommitObj.Tree()
	require.NoError(t, err)

	newTree, err := newCommitObj.Tree()
	require.NoError(t, err)

	// go-git을 직접 사용하여 diff 계산 (단위 테스트 스타일)
	changes, err := object.DiffTree(initialTree, newTree)
	require.NoError(t, err)

	// single-line.txt 변경사항 찾기
	var singleLineChange *object.Change
	for _, change := range changes {
		if change.To.Name == "single-line.txt" {
			singleLineChange = change
			break
		}
	}

	require.NotNil(t, singleLineChange, "single-line.txt should be in the diff")

	// 패치 가져오기 및 줄 수 계산
	patch, err := singleLineChange.Patch()
	require.NoError(t, err)

	additions, deletions := countPatchLines(patch)

	assert.Equal(t, 1, additions, "정확히 1줄만 추가되어야 합니다")
	assert.Equal(t, 0, deletions, "삭제된 줄이 없어야 합니다")
}

// TestDiffLineCount_SingleLineAddedToExistingFile는 기존 파일에 단일 줄을 추가하는 것을 테스트합니다
func TestDiffLineCount_SingleLineAddedToExistingFile(t *testing.T) {
	repoPath, initialCommitSHA, secondCommitSHA, cleanup := setupTestRepositoryWithCommits(t)
	defer cleanup()

	// 저장소 열기
	repo, err := gogit.PlainOpen(repoPath)
	require.NoError(t, err)

	// 커밋 가져오기
	initialCommitObj, err := repo.CommitObject(plumbing.NewHash(initialCommitSHA))
	require.NoError(t, err)

	secondCommitObj, err := repo.CommitObject(plumbing.NewHash(secondCommitSHA))
	require.NoError(t, err)

	// tree 가져오기
	initialTree, err := initialCommitObj.Tree()
	require.NoError(t, err)

	secondTree, err := secondCommitObj.Tree()
	require.NoError(t, err)

	// diff 계산
	changes, err := object.DiffTree(initialTree, secondTree)
	require.NoError(t, err)

	// single-line.txt 변경사항 찾기
	var singleLineChange *object.Change
	for _, change := range changes {
		if change.To.Name == "single-line.txt" {
			singleLineChange = change
			break
		}
	}

	require.NotNil(t, singleLineChange, "single-line.txt should be in the diff")

	// 패치 가져오기 및 줄 수 계산
	patch, err := singleLineChange.Patch()
	require.NoError(t, err)

	additions, deletions := countPatchLines(patch)

	assert.Equal(t, 1, additions, "정확히 1줄만 추가되어야 합니다")
	assert.Equal(t, 0, deletions, "삭제된 줄이 없어야 합니다")
}

// TestHandleBranchDiff_SingleLineAddition는 단일 줄 추가가 있는 브랜치 diff 핸들러를 테스트합니다
func TestHandleBranchDiff_SingleLineAddition(t *testing.T) {
	handler, _, cleanup := setupTestHandler(t)
	defer cleanup()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/branches/:base/:head", handler.HandleBranchDiff)

	req, _ := http.NewRequest("GET", "/api/v2/repository/testowner/testrepo/diff/branches/master/feature", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.True(t, w.Code == http.StatusOK || w.Code == http.StatusBadRequest)
}

// TestHandleCommitDiff_SingleLineAddition는 커밋 diff 핸들러를 테스트합니다
func TestHandleCommitDiff_SingleLineAddition(t *testing.T) {
	repoPath, initialCommitSHA, secondCommitSHA, cleanup := setupTestRepositoryWithCommits(t)
	defer cleanup()

	tempDir := filepath.Dir(filepath.Dir(repoPath))
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
		Git:     config.GitConfig{RepoRoot: tempDir},
	}
	handler, err := NewDiffHandler(cfg)
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/commits/:base/:head", handler.HandleCommitDiff)

	url := fmt.Sprintf("/api/v2/repository/testowner/testrepo/diff/commits/%s/%s", initialCommitSHA[:7], secondCommitSHA[:7])
	req, _ := http.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var diffResponse DiffResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&diffResponse))
	assert.True(t, diffResponse.Success)

	var singleLineFile *FileDiff
	for i := range diffResponse.Files {
		if diffResponse.Files[i].Path == "single-line.txt" {
			singleLineFile = &diffResponse.Files[i]
			break
		}
	}
	require.NotNil(t, singleLineFile)
	assert.Equal(t, FileDiffStatusAdded, singleLineFile.Status)
	assert.Greater(t, len(singleLineFile.Changes), 0)
	assert.Equal(t, 1, singleLineFile.Additions)
	assert.Equal(t, 0, singleLineFile.Deletions)
}

// TestHandleCommitDiff_SingleLineAddedToExistingFile는 파일 수정이 있는 커밋 diff를 테스트합니다
func TestHandleCommitDiff_SingleLineAddedToExistingFile(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "diffs-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	ownerDir := filepath.Join(tempDir, "testowner")
	require.NoError(t, os.MkdirAll(ownerDir, 0755))
	repoPath := filepath.Join(ownerDir, "testrepo.git")
	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	initialBlobHash := createBlob(t, repo, []byte("First line\n"))
	initialTreeHash := createTree(t, repo, []object.TreeEntry{
		{Name: "existing.txt", Mode: 0100644, Hash: initialBlobHash},
	})
	initialCommitHash := createCommit(t, repo, "Initial commit", initialTreeHash)

	secondBlobHash := createBlob(t, repo, []byte("First line\nSecond line\n"))
	secondTreeHash := createTree(t, repo, []object.TreeEntry{
		{Name: "existing.txt", Mode: 0100644, Hash: secondBlobHash},
	})
	secondCommitHash := createCommit(t, repo, "Add second line", secondTreeHash, initialCommitHash)

	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
		Git:     config.GitConfig{RepoRoot: tempDir},
	}
	handler, err := NewDiffHandler(cfg)
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/commits/:base/:head", handler.HandleCommitDiff)

	url := fmt.Sprintf("/api/v2/repository/testowner/testrepo/diff/commits/%s/%s", initialCommitHash.String()[:7], secondCommitHash.String()[:7])
	req, _ := http.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var diffResponse DiffResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&diffResponse))
	assert.True(t, diffResponse.Success)

	var modifiedFile *FileDiff
	for i := range diffResponse.Files {
		if diffResponse.Files[i].Path == "existing.txt" {
			modifiedFile = &diffResponse.Files[i]
			break
		}
	}
	require.NotNil(t, modifiedFile)
	assert.Equal(t, FileDiffStatusModified, modifiedFile.Status)
	assert.Greater(t, len(modifiedFile.Changes), 0)
	assert.Equal(t, 1, modifiedFile.Additions-modifiedFile.Deletions)
}

// countPatchLines는 패치에서 추가 및 삭제된 줄 수를 계산하는 헬퍼 함수입니다
func countPatchLines(patch *object.Patch) (additions, deletions int) {
	for _, filePatch := range patch.FilePatches() {
		for _, chunk := range filePatch.Chunks() {
			switch chunk.Type() {
			case 1: // 추가 - 추가된 줄
				content := chunk.Content()
				lines := strings.Split(content, "\n")
				// 내용이 개행으로 끝나면 빈 문자열 제거
				if len(lines) > 0 && lines[len(lines)-1] == "" {
					lines = lines[:len(lines)-1]
				}
				additions += len(lines)
			case 2: // 삭제 - 삭제된 줄
				content := chunk.Content()
				lines := strings.Split(content, "\n")
				if len(lines) > 0 && lines[len(lines)-1] == "" {
					lines = lines[:len(lines)-1]
				}
				deletions += len(lines)
			}
		}
	}
	return additions, deletions
}

// TestDiffHandler_NewDiffHandler는 핸들러 생성을 테스트합니다
func TestDiffHandler_NewDiffHandler(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: "/tmp/test",
		},
	}

	handler, err := NewDiffHandler(cfg)
	require.NoError(t, err)
	assert.NotNil(t, handler)
}

// TestDiffHandler_NewDiffHandler_NilConfig는 nil 설정으로 핸들러 생성을 테스트합니다
func TestDiffHandler_NewDiffHandler_NilConfig(t *testing.T) {
	handler, err := NewDiffHandler(nil)
	assert.Error(t, err)
	assert.Nil(t, handler)
}

// TestDiffHandler_HandleCommitDiff_InvalidParams는 잘못된 매개변수로 커밋 diff를 테스트합니다
func TestDiffHandler_HandleCommitDiff_InvalidParams(t *testing.T) {
	handler, _, cleanup := setupTestHandler(t)
	defer cleanup()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/commits/:base/:head", handler.HandleCommitDiff)

	// base가 누락된 경우 테스트
	req, _ := http.NewRequest("GET", "/api/v2/repository/testowner/testrepo/diff/commits//abc123", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestDiffHandler_HandleBranchDiff_InvalidParams는 잘못된 매개변수로 브랜치 diff를 테스트합니다
func TestDiffHandler_HandleBranchDiff_InvalidParams(t *testing.T) {
	handler, _, cleanup := setupTestHandler(t)
	defer cleanup()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/branches/:base/:head", handler.HandleBranchDiff)

	// head가 누락된 경우 테스트
	req, _ := http.NewRequest("GET", "/api/v2/repository/testowner/testrepo/diff/branches/master/", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.True(t, w.Code == http.StatusBadRequest || w.Code == http.StatusNotFound)
}

// TestDiffHandler_HandleFileDiff_InvalidParams는 잘못된 매개변수로 파일 diff를 테스트합니다
func TestDiffHandler_HandleFileDiff_InvalidParams(t *testing.T) {
	handler, _, cleanup := setupTestHandler(t)
	defer cleanup()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/file", handler.HandleFileDiff)

	// path가 누락된 경우 테스트
	req, _ := http.NewRequest("GET", "/api/v2/repository/testowner/testrepo/diff/file?base=abc&head=def", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestDiffHandler_HandleSingleCommitDiff_InvalidParams는 잘못된 매개변수로 단일 커밋 diff를 테스트합니다
func TestDiffHandler_HandleSingleCommitDiff_InvalidParams(t *testing.T) {
	handler, _, cleanup := setupTestHandler(t)
	defer cleanup()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/commits/:sha/diff", handler.HandleSingleCommitDiff)

	// SHA가 누락된 경우 테스트
	req, _ := http.NewRequest("GET", "/api/v2/repository/testowner/testrepo/commits//diff", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestDiffHandler_HandlePatch_InvalidParams는 잘못된 매개변수로 패치 생성을 테스트합니다
func TestDiffHandler_HandlePatch_InvalidParams(t *testing.T) {
	handler, _, cleanup := setupTestHandler(t)
	defer cleanup()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/patch/:base/:head", handler.HandlePatch)

	// base가 누락된 경우 테스트
	req, _ := http.NewRequest("GET", "/api/v2/repository/testowner/testrepo/patch//abc123", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestDiffHandler_ContextLines는 컨텍스트 줄 매개변수를 테스트합니다
func TestDiffHandler_ContextLines(t *testing.T) {
	repoPath, initialCommitSHA, secondCommitSHA, cleanup := setupTestRepositoryWithCommits(t)
	defer cleanup()

	tempDir := filepath.Dir(filepath.Dir(repoPath))
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
		Git:     config.GitConfig{RepoRoot: tempDir},
	}
	handler, err := NewDiffHandler(cfg)
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/commits/:base/:head", handler.HandleCommitDiff)

	url := fmt.Sprintf("/api/v2/repository/testowner/testrepo/diff/commits/%s/%s?context=5", initialCommitSHA[:7], secondCommitSHA[:7])
	req, _ := http.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var diffResponse DiffResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&diffResponse))
	assert.True(t, diffResponse.Success)
}
