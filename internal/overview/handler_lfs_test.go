package overview

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestRepoWithLFSPointer는 LFS pointer 형식 콘텐츠를 가진 파일을 포함하는
// 테스트 저장소를 생성한다. lfsPath 는 LFS pointer 파일 경로, plainPath 는
// 일반 텍스트 파일 경로 (회귀 검증용).
func createTestRepoWithLFSPointer(t *testing.T, tempDir, owner, repo, lfsPath, plainPath string) {
	t.Helper()

	repoPath := filepath.Join(tempDir, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	tempWorkDir, err := os.MkdirTemp("", "overview-lfs-work-*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempWorkDir) }()

	workRepo, err := gogit.PlainInit(tempWorkDir, false)
	require.NoError(t, err)

	worktree, err := workRepo.Worktree()
	require.NoError(t, err)

	// 유효한 LFS pointer (oid 64자 hex + size).
	lfsContent := "version https://git-lfs.github.com/spec/v1\n" +
		"oid sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n" +
		"size 1024\n"
	require.NoError(t, os.WriteFile(filepath.Join(tempWorkDir, lfsPath), []byte(lfsContent), 0644))
	_, err = worktree.Add(lfsPath)
	require.NoError(t, err)

	// 일반 텍스트 파일.
	require.NoError(t, os.WriteFile(filepath.Join(tempWorkDir, plainPath), []byte("# README\n"), 0644))
	_, err = worktree.Add(plainPath)
	require.NoError(t, err)

	when := time.Unix(1700000000, 0).UTC()
	_, err = worktree.Commit("LFS fixture", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  when,
		},
	})
	require.NoError(t, err)

	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	head, err := workRepo.Head()
	require.NoError(t, err)

	commitObj, err := workRepo.Storer.EncodedObject(plumbing.CommitObject, head.Hash())
	require.NoError(t, err)
	_, err = gitRepo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	commit, err := workRepo.CommitObject(head.Hash())
	require.NoError(t, err)
	require.NoError(t, copyTreeRecursiveForHandlerTest(workRepo, gitRepo, commit.TreeHash))

	mainRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), head.Hash())
	require.NoError(t, gitRepo.Storer.SetReference(mainRef))
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))
	require.NoError(t, gitRepo.Storer.SetReference(headRef))
}

// TestHandleGetBlobOverview_LFSPointer 는 /overview/blob 응답이 LFS pointer 를
// 감지해 IsLFSPointer + LFS 메타데이터를 노출하고 raw pointer 텍스트를 Content
// 에 복사하지 않는지 검증한다 (Issue-2327 Phase 1 — /blob raw streaming 화의
// 선결 조건).
func TestHandleGetBlobOverview_LFSPointer(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	owner := "lfsowner"
	repo := "lfsrepo"
	createTestRepoWithLFSPointer(t, tempDir, owner, repo, "asset.bin", "README.md")

	router := gin.New()
	router.GET("/repos/:owner/:repo/overview/blob", adaptQuery(handler.HandleGetBlobOverview))

	req := httptest.NewRequest(http.MethodGet,
		"/repos/"+owner+"/"+repo+"/overview/blob?ref=main&path=asset.bin", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	var resp BlobOverviewResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	require.True(t, resp.Success)
	assert.True(t, resp.IsLFSPointer, "IsLFSPointer should be true for LFS pointer file")
	require.NotNil(t, resp.LFS, "LFS field should be populated")
	assert.Equal(t,
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		resp.LFS.Oid)
	assert.Equal(t, int64(1024), resp.LFS.Size)
	assert.Empty(t, resp.Content, "Content should not include raw pointer text")
	assert.Zero(t, resp.Lines, "Lines should be 0 for LFS pointer")
	// raw pointer 가 그대로 새는 것 방지 — 응답 body 자체에 oid sha256 prefix 등장 X.
	assert.NotContains(t, w.Body.String(), "git-lfs.github.com",
		"raw pointer text must not appear in JSON response")
}

// TestHandleGetBlobOverview_RegularFile_LFSFieldsZero 는 일반 텍스트 파일 응답이
// IsLFSPointer=false / LFS=nil (omitempty) 이어서 기존 client 와 backward
// compatible 한지 검증한다.
func TestHandleGetBlobOverview_RegularFile_LFSFieldsZero(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	owner := "lfsowner2"
	repo := "lfsrepo2"
	createTestRepoWithLFSPointer(t, tempDir, owner, repo, "asset.bin", "README.md")

	router := gin.New()
	router.GET("/repos/:owner/:repo/overview/blob", adaptQuery(handler.HandleGetBlobOverview))

	req := httptest.NewRequest(http.MethodGet,
		"/repos/"+owner+"/"+repo+"/overview/blob?ref=main&path=README.md", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	var resp BlobOverviewResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	require.True(t, resp.Success)
	assert.False(t, resp.IsLFSPointer)
	assert.Nil(t, resp.LFS)
	assert.Contains(t, resp.Content, "# README")
}
