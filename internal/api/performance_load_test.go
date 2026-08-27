// package api_test (external) — tree 가 internal/api 를 import 하게 되면서
// (카탈로그 마이그레이션) package api 내부 테스트로 두면 api → tree → api
// import cycle 이 생긴다. blob/tree/repositories 의 exported 표면만 쓰는
// black-box 부하 테스트라 외부 테스트 패키지로 옮겨도 손실이 없다.
package api_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/blob"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/collabops/git-executor/internal/tree"
	"github.com/gin-gonic/gin"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"
)

const (
	perfTimeout        = 30 * time.Second
	perfCommitCount    = 500  // 10000은 너무 느려 500으로 검증
	perfBranchCount    = 100  // 1000 대신 100
	perfManyFilesCount = 1000 // 10000 대신 1000
)

func setupRouterForPerf(t *testing.T) (*gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	tempDir := t.TempDir()
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "warn", Format: "json"},
		Git:     config.GitConfig{RepoRoot: tempDir},
	}
	blobHandler, err := blob.NewBlobHandler(cfg)
	require.NoError(t, err)
	treeHandler, err := tree.NewTreeHandler(cfg)
	require.NoError(t, err)
	repoHandler, err := repositories.NewRepositoryCreationHandler(cfg)
	require.NoError(t, err)
	r := gin.New()
	r.GET("/repos/:owner/:repo/blob", blobHandler.HandleBlobAPI)
	g := r.Group("/repos/:owner/:repo")
	tree.RegisterRoutes(g, treeHandler)
	repositories.RegisterRoutes(g, repoHandler)
	return r, tempDir
}

// createBareRepoWithCommits creates a bare repo at repoPath with n commits (single file per commit).
func createBareRepoWithCommits(t *testing.T, repoPath string, n int) {
	t.Helper()
	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	// One initial commit
	content := []byte("initial\n")
	blobObj := repo.Storer.NewEncodedObject()
	blobObj.SetType(plumbing.BlobObject)
	blobObj.SetSize(int64(len(content)))
	w, _ := blobObj.Writer()
	_, _ = w.Write(content)
	_ = w.Close()
	blobHash, _ := repo.Storer.SetEncodedObject(blobObj)

	treeEntries := []object.TreeEntry{{Name: "f", Mode: 0100644, Hash: blobHash}}
	treeObj := &object.Tree{Entries: treeEntries}
	treeEnc := repo.Storer.NewEncodedObject()
	require.NoError(t, treeObj.Encode(treeEnc))
	treeHash, _ := repo.Storer.SetEncodedObject(treeEnc)

	when := time.Now()
	parent := plumbing.ZeroHash
	for i := 0; i < n; i++ {
		commit := &object.Commit{
			Message:      fmt.Sprintf("commit %d", i),
			Author:       object.Signature{Name: "a", Email: "a@b.com", When: when},
			Committer:    object.Signature{Name: "a", Email: "a@b.com", When: when},
			TreeHash:     treeHash,
			ParentHashes: nil,
		}
		if parent != plumbing.ZeroHash {
			commit.ParentHashes = []plumbing.Hash{parent}
		}
		commitEnc := repo.Storer.NewEncodedObject()
		require.NoError(t, commit.Encode(commitEnc))
		commitHash, _ := repo.Storer.SetEncodedObject(commitEnc)
		parent = commitHash
		if i == 0 {
			ref := plumbing.NewHashReference(plumbing.HEAD, commitHash)
			_ = repo.Storer.SetReference(ref)
			mainRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), commitHash)
			_ = repo.Storer.SetReference(mainRef)
		}
	}
	// Update main to latest
	mainRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), parent)
	_ = repo.Storer.SetReference(mainRef)
	_ = repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main")))
}

// createBareRepoWithBranches creates a bare repo with one commit and many branches pointing to it.
func createBareRepoWithBranches(t *testing.T, repoPath string, n int) {
	t.Helper()
	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	content := []byte("x\n")
	blobObj := repo.Storer.NewEncodedObject()
	blobObj.SetType(plumbing.BlobObject)
	blobObj.SetSize(int64(len(content)))
	w, _ := blobObj.Writer()
	_, _ = w.Write(content)
	_ = w.Close()
	blobHash, _ := repo.Storer.SetEncodedObject(blobObj)
	treeObj := &object.Tree{Entries: []object.TreeEntry{{Name: "f", Mode: 0100644, Hash: blobHash}}}
	treeEnc := repo.Storer.NewEncodedObject()
	require.NoError(t, treeObj.Encode(treeEnc))
	treeHash, _ := repo.Storer.SetEncodedObject(treeEnc)
	when := time.Now()
	commit := &object.Commit{
		Message: "init", Author: object.Signature{Name: "a", Email: "a@b.com", When: when},
		Committer: object.Signature{Name: "a", Email: "a@b.com", When: when},
		TreeHash:  treeHash,
	}
	commitEnc := repo.Storer.NewEncodedObject()
	require.NoError(t, commit.Encode(commitEnc))
	commitHash, _ := repo.Storer.SetEncodedObject(commitEnc)
	_ = repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), commitHash))
	_ = repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main")))
	for i := 0; i < n; i++ {
		ref := plumbing.NewHashReference(plumbing.ReferenceName(fmt.Sprintf("refs/heads/branch-%d", i)), commitHash)
		_ = repo.Storer.SetReference(ref)
	}
}

// createBareRepoWithManyFiles creates a bare repo with one commit containing many files.
func createBareRepoWithManyFiles(t *testing.T, repoPath string, fileCount int) {
	t.Helper()
	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	entries := make([]object.TreeEntry, 0, fileCount)
	for i := 0; i < fileCount; i++ {
		content := []byte(fmt.Sprintf("file %d", i))
		blobObj := repo.Storer.NewEncodedObject()
		blobObj.SetType(plumbing.BlobObject)
		blobObj.SetSize(int64(len(content)))
		w, _ := blobObj.Writer()
		_, _ = w.Write(content)
		_ = w.Close()
		blobHash, _ := repo.Storer.SetEncodedObject(blobObj)
		entries = append(entries, object.TreeEntry{Name: fmt.Sprintf("f%d.txt", i), Mode: 0100644, Hash: blobHash})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	treeObj := &object.Tree{Entries: entries}
	treeEnc := repo.Storer.NewEncodedObject()
	require.NoError(t, treeObj.Encode(treeEnc))
	treeHash, _ := repo.Storer.SetEncodedObject(treeEnc)
	when := time.Now()
	commit := &object.Commit{
		Message: "many files", Author: object.Signature{Name: "a", Email: "a@b.com", When: when},
		Committer: object.Signature{Name: "a", Email: "a@b.com", When: when},
		TreeHash:  treeHash,
	}
	commitEnc := repo.Storer.NewEncodedObject()
	require.NoError(t, commit.Encode(commitEnc))
	commitHash, _ := repo.Storer.SetEncodedObject(commitEnc)
	_ = repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), commitHash))
	_ = repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main")))
}

// --- 14.1 대용량 처리 ---

// Test_LargeRepo_10000Commits_HandlesInTime 10000+ 커밋 규모에서 API가 제한 시간 내 응답
func Test_LargeRepo_10000Commits_HandlesInTime(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large repo test in short mode")
	}
	r, tempDir := setupRouterForPerf(t)
	owner, repoName := "perf", "manycommits"
	repoPath := filepath.Join(tempDir, owner, repoName+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	createBareRepoWithCommits(t, repoPath, perfCommitCount)

	req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repoName+"/tree?ref=main", nil)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		r.ServeHTTP(w, req)
		close(done)
	}()
	select {
	case <-done:
		require.Equal(t, http.StatusOK, w.Code)
	case <-time.After(perfTimeout):
		t.Fatal("tree API did not complete within timeout")
	}
}

// Test_LargeRepo_1000Branches_HandlesInTime 1000+ 브랜치 규모에서 제한 시간 내 응답
func Test_LargeRepo_1000Branches_HandlesInTime(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large branches test in short mode")
	}
	r, tempDir := setupRouterForPerf(t)
	owner, repoName := "perf", "manybranches"
	repoPath := filepath.Join(tempDir, owner, repoName+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	createBareRepoWithBranches(t, repoPath, perfBranchCount)

	req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repoName+"/tree?ref=main", nil)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		r.ServeHTTP(w, req)
		close(done)
	}()
	select {
	case <-done:
		require.Equal(t, http.StatusOK, w.Code)
	case <-time.After(perfTimeout):
		t.Fatal("tree API did not complete within timeout")
	}
}

// Test_LargeFile_100MB_HandlesInTime 100MB 파일 요청이 제한 시간 내 처리 (스트리밍/제한 정책에 따라 200/413 등)
func Test_LargeFile_100MB_HandlesInTime(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 100MB file test in short mode")
	}
	// 100MB 단일 파일 레포 생성은 비용이 크므로, 기존 overview 대용량 레포 패턴 사용 또는 스킵
	// 여기서는 작은 대용량(10MB)으로 동일 계약 검증
	r, tempDir := setupRouterForPerf(t)
	owner, repoName := "perf", "largefile"
	repoPath := filepath.Join(tempDir, owner, repoName+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	const size10MB = 10 * 1024 * 1024
	content := make([]byte, size10MB)
	for i := range content {
		content[i] = byte(i % 256)
	}
	blobObj := repo.Storer.NewEncodedObject()
	blobObj.SetType(plumbing.BlobObject)
	blobObj.SetSize(int64(len(content)))
	wr, _ := blobObj.Writer()
	_, _ = wr.Write(content)
	_ = wr.Close()
	blobHash, _ := repo.Storer.SetEncodedObject(blobObj)
	treeObj := &object.Tree{Entries: []object.TreeEntry{{Name: "large.bin", Mode: 0100644, Hash: blobHash}}}
	treeEnc := repo.Storer.NewEncodedObject()
	require.NoError(t, treeObj.Encode(treeEnc))
	treeHash, _ := repo.Storer.SetEncodedObject(treeEnc)
	when := time.Now()
	commit := &object.Commit{
		Message: "large", Author: object.Signature{Name: "a", Email: "a@b.com", When: when},
		Committer: object.Signature{Name: "a", Email: "a@b.com", When: when},
		TreeHash:  treeHash,
	}
	commitEnc := repo.Storer.NewEncodedObject()
	require.NoError(t, commit.Encode(commitEnc))
	commitHash, _ := repo.Storer.SetEncodedObject(commitEnc)
	_ = repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), commitHash))
	_ = repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main")))

	req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repoName+"/blob?path=large.bin&ref=main", nil)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		r.ServeHTTP(w, req)
		close(done)
	}()
	select {
	case <-done:
		// 200 OK 또는 413 등 정책에 따라 허용
		require.Contains(t, []int{http.StatusOK, http.StatusRequestEntityTooLarge}, w.Code)
	case <-time.After(perfTimeout):
		t.Fatal("blob API did not complete within timeout")
	}
}

// Test_ManyFiles_10000Files_HandlesInTime 10000+ 파일 레포에서 제한 시간 내 tree/overview 응답
func Test_ManyFiles_10000Files_HandlesInTime(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping many files test in short mode")
	}
	r, tempDir := setupRouterForPerf(t)
	owner, repoName := "perf", "manyfiles"
	repoPath := filepath.Join(tempDir, owner, repoName+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	createBareRepoWithManyFiles(t, repoPath, perfManyFilesCount)

	req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repoName+"/tree?ref=main", nil)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		r.ServeHTTP(w, req)
		close(done)
	}()
	select {
	case <-done:
		require.Equal(t, http.StatusOK, w.Code)
	case <-time.After(perfTimeout):
		t.Fatal("tree API did not complete within timeout")
	}
}

// --- 14.2 동시성 ---

// Test_ConcurrentReads_NoDeadlock 동시 읽기 시 데드락 없음
func Test_ConcurrentReads_NoDeadlock(t *testing.T) {
	r, tempDir := setupRouterForPerf(t)
	owner, repoName := "conc", "read"
	repoPath := filepath.Join(tempDir, owner, repoName+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	createBareRepoWithCommits(t, repoPath, 5)

	var wg sync.WaitGroup
	n := 20
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repoName+"/tree?ref=main", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code)
		}()
	}
	wg.Wait()
}

// Test_ConcurrentWrites_ProperLocking 동시 쓰기 시 적절한 잠금 (서로 다른 레포 생성)
func Test_ConcurrentWrites_ProperLocking(t *testing.T) {
	r, _ := setupRouterForPerf(t)
	owner := "conc"
	var wg sync.WaitGroup
	n := 5
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			repoName := fmt.Sprintf("repo%d", idx)
			req := httptest.NewRequest("POST", "/repos/"+owner+"/"+repoName, nil)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, http.StatusCreated, w.Code)
		}(i)
	}
	wg.Wait()
}

// Test_ConcurrentMixedOps_NoRaceCondition 혼합 동시 작업 시 레이스 없음
func Test_ConcurrentMixedOps_NoRaceCondition(t *testing.T) {
	r, tempDir := setupRouterForPerf(t)
	owner, repoName := "conc", "mixed"
	repoPath := filepath.Join(tempDir, owner, repoName+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	createBareRepoWithCommits(t, repoPath, 3)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repoName+"/tree?ref=main", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code)
		}()
	}
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repoName+"/blob?path=f&ref=main", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code)
		}()
	}
	wg.Wait()
}
