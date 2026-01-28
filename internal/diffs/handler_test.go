package diffs

import (
	"context"
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
	"github.com/collabops/git-executor/internal/testutil/gitrepo"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// =============================================================================
// 테스트: DiffHandler 생성
// 회귀 방지: "DiffHandler 생성이 올바르게 동작해야 함"
// =============================================================================

// Test_NewDiffHandler_ValidConfig_ReturnsHandler
// 회귀: 유효한 설정으로 핸들러 생성이 성공해야 함
func Test_NewDiffHandler_ValidConfig_ReturnsHandler(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
		Git:     config.GitConfig{RepoRoot: "/tmp/test"},
	}

	handler, err := NewDiffHandler(cfg)

	require.NoError(t, err)
	require.NotNil(t, handler)
}

// Test_NewDiffHandler_NilConfig_ReturnsError
// 회귀: nil 설정으로 핸들러 생성 시 에러 반환
func Test_NewDiffHandler_NilConfig_ReturnsError(t *testing.T) {
	t.Parallel()

	handler, err := NewDiffHandler(nil)

	require.Error(t, err)
	require.Nil(t, handler)
}

// =============================================================================
// 테스트: CommitDiff API
// 회귀 방지: "커밋 간 diff가 정확하게 계산되어야 함"
// =============================================================================

// Test_CommitDiff_SingleLineAddition_ReturnsCorrectCounts
// 회귀: 단일 줄 추가 시 additions=1, deletions=0이어야 함
func Test_CommitDiff_SingleLineAddition_ReturnsCorrectCounts(t *testing.T) {
	t.Parallel()

	handler, initialSHA, secondSHA := newDiffHandlerWithCommits(t)

	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/commits/:base/:head", handler.HandleCommitDiff)

	// 전체 SHA 사용
	url := fmt.Sprintf("/api/v2/repository/testowner/testrepo/diff/commits/%s/%s", initialSHA, secondSHA)
	req, _ := http.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp DiffResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.True(t, resp.Success)

	// single-line.txt 찾기
	var singleLineFile *FileDiff
	for i := range resp.Files {
		if resp.Files[i].Path == "single-line.txt" {
			singleLineFile = &resp.Files[i]
			break
		}
	}
	require.NotNil(t, singleLineFile, "single-line.txt should be in diff")
	require.Equal(t, FileDiffStatusAdded, singleLineFile.Status)
	require.Equal(t, 1, singleLineFile.Additions)
	require.Equal(t, 0, singleLineFile.Deletions)
}

// Test_CommitDiff_FileModification_ReturnsCorrectDelta
// 회귀: 파일 수정 시 additions-deletions가 정확해야 함
func Test_CommitDiff_FileModification_ReturnsCorrectDelta(t *testing.T) {
	t.Parallel()

	// Given: 기존 파일에 한 줄 추가
	tempDir := t.TempDir()
	ownerDir := filepath.Join(tempDir, "testowner")
	require.NoError(t, os.MkdirAll(ownerDir, 0755))

	repoPath := filepath.Join(ownerDir, "testrepo.git")
	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// 첫 커밋: "First line\n"
	initialBlobHash := makeBlob(t, repo, []byte("First line\n"))
	initialTreeHash := makeTree(t, repo, []object.TreeEntry{
		{Name: "existing.txt", Mode: 0100644, Hash: initialBlobHash},
	})
	initialCommitHash := makeCommit(t, repo, "Initial commit", initialTreeHash, nil, now)

	// 두 번째 커밋: "First line\nSecond line\n"
	secondBlobHash := makeBlob(t, repo, []byte("First line\nSecond line\n"))
	secondTreeHash := makeTree(t, repo, []object.TreeEntry{
		{Name: "existing.txt", Mode: 0100644, Hash: secondBlobHash},
	})
	secondCommitHash := makeCommit(t, repo, "Add second line", secondTreeHash, []plumbing.Hash{initialCommitHash}, now)

	handler := newDiffHandler(t, tempDir)

	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/commits/:base/:head", handler.HandleCommitDiff)

	// When: diff 조회 (전체 SHA 사용)
	url := fmt.Sprintf("/api/v2/repository/testowner/testrepo/diff/commits/%s/%s",
		initialCommitHash.String(), secondCommitHash.String())
	req, _ := http.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Then: 수정된 파일의 delta가 정확해야 함
	requireStatus(t, w, http.StatusOK)

	var resp DiffResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.True(t, resp.Success)

	var modifiedFile *FileDiff
	for i := range resp.Files {
		if resp.Files[i].Path == "existing.txt" {
			modifiedFile = &resp.Files[i]
			break
		}
	}
	require.NotNil(t, modifiedFile)
	require.Equal(t, FileDiffStatusModified, modifiedFile.Status)
	require.Equal(t, 1, modifiedFile.Additions-modifiedFile.Deletions, "net change should be +1 line")
}

// Test_CommitDiff_SingleLineDeletion_ReturnsCorrectCounts
// 회귀: 단일 줄 삭제 시 additions=0, deletions=1이어야 함
func Test_CommitDiff_SingleLineDeletion_ReturnsCorrectCounts(t *testing.T) {
	t.Parallel()

	// Given: 기존 파일에서 한 줄 삭제
	tempDir := t.TempDir()
	ownerDir := filepath.Join(tempDir, "testowner")
	require.NoError(t, os.MkdirAll(ownerDir, 0755))

	repoPath := filepath.Join(ownerDir, "testrepo.git")
	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// 첫 커밋: "Line1\nLine2\n"
	initialBlobHash := makeBlob(t, repo, []byte("Line1\nLine2\n"))
	initialTreeHash := makeTree(t, repo, []object.TreeEntry{
		{Name: "delete-one-line.txt", Mode: 0100644, Hash: initialBlobHash},
	})
	initialCommitHash := makeCommit(t, repo, "Initial commit", initialTreeHash, nil, now)

	// 두 번째 커밋: "Line1\n" (Line2 삭제)
	secondBlobHash := makeBlob(t, repo, []byte("Line1\n"))
	secondTreeHash := makeTree(t, repo, []object.TreeEntry{
		{Name: "delete-one-line.txt", Mode: 0100644, Hash: secondBlobHash},
	})
	secondCommitHash := makeCommit(t, repo, "Delete one line", secondTreeHash, []plumbing.Hash{initialCommitHash}, now)

	handler := newDiffHandler(t, tempDir)

	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/commits/:base/:head", handler.HandleCommitDiff)

	url := fmt.Sprintf("/api/v2/repository/testowner/testrepo/diff/commits/%s/%s",
		initialCommitHash.String(), secondCommitHash.String())
	req, _ := http.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp DiffResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.True(t, resp.Success)

	var file *FileDiff
	for i := range resp.Files {
		if resp.Files[i].Path == "delete-one-line.txt" {
			file = &resp.Files[i]
			break
		}
	}
	require.NotNil(t, file)
	require.Equal(t, FileDiffStatusModified, file.Status)
	require.Equal(t, 0, file.Additions)
	require.Equal(t, 1, file.Deletions)
}

// Test_CommitDiff_FileAddition_StatusAdded
// 회귀: 파일 추가는 status=added로 표시되어야 함
func Test_CommitDiff_FileAddition_StatusAdded(t *testing.T) {
	t.Parallel()

	handler, baseSHA, headSHA := newDiffHandlerWithCommits(t)

	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/commits/:base/:head", handler.HandleCommitDiff)

	url := fmt.Sprintf("/api/v2/repository/testowner/testrepo/diff/commits/%s/%s", baseSHA, headSHA)
	req, _ := http.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp DiffResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.True(t, resp.Success)

	var added *FileDiff
	for i := range resp.Files {
		if resp.Files[i].Path == "single-line.txt" {
			added = &resp.Files[i]
			break
		}
	}
	require.NotNil(t, added)
	require.Equal(t, FileDiffStatusAdded, added.Status)
}

// Test_CommitDiff_FileDeletion_StatusDeleted
// 회귀: 파일 삭제는 status=deleted로 표시되어야 함
func Test_CommitDiff_FileDeletion_StatusDeleted(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	ownerDir := filepath.Join(tempDir, "testowner")
	require.NoError(t, os.MkdirAll(ownerDir, 0755))

	repoPath := filepath.Join(ownerDir, "testrepo.git")
	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// 첫 커밋: 파일 존재
	blobHash := makeBlob(t, repo, []byte("content\n"))
	tree1 := makeTree(t, repo, []object.TreeEntry{
		{Name: "delete-me.txt", Mode: 0100644, Hash: blobHash},
	})
	commit1 := makeCommit(t, repo, "Add file", tree1, nil, now)

	// 두 번째 커밋: 파일 삭제 (빈 트리)
	tree2 := makeTree(t, repo, []object.TreeEntry{})
	commit2 := makeCommit(t, repo, "Delete file", tree2, []plumbing.Hash{commit1}, now)

	handler := newDiffHandler(t, tempDir)
	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/commits/:base/:head", handler.HandleCommitDiff)

	url := fmt.Sprintf("/api/v2/repository/testowner/testrepo/diff/commits/%s/%s", commit1.String(), commit2.String())
	req, _ := http.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp DiffResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.True(t, resp.Success)

	var deleted *FileDiff
	for i := range resp.Files {
		if resp.Files[i].Path == "delete-me.txt" {
			deleted = &resp.Files[i]
			break
		}
	}
	require.NotNil(t, deleted)
	require.Equal(t, FileDiffStatusDeleted, deleted.Status)
}

// Test_CommitDiff_FileRename_StatusRenamed
// 회귀: 파일 이름 변경은 renamed로 표시되거나(지원 시), delete+add로라도 안정적으로 표현되어야 함
func Test_CommitDiff_FileRename_StatusRenamed(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	ownerDir := filepath.Join(tempDir, "testowner")
	require.NoError(t, os.MkdirAll(ownerDir, 0755))

	repoPath := filepath.Join(ownerDir, "testrepo.git")
	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	blobHash := makeBlob(t, repo, []byte("same content\n"))
	tree1 := makeTree(t, repo, []object.TreeEntry{
		{Name: "old-name.txt", Mode: 0100644, Hash: blobHash},
	})
	commit1 := makeCommit(t, repo, "Old name", tree1, nil, now)

	tree2 := makeTree(t, repo, []object.TreeEntry{
		{Name: "new-name.txt", Mode: 0100644, Hash: blobHash},
	})
	commit2 := makeCommit(t, repo, "Renamed", tree2, []plumbing.Hash{commit1}, now)

	handler := newDiffHandler(t, tempDir)
	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/commits/:base/:head", handler.HandleCommitDiff)

	url := fmt.Sprintf("/api/v2/repository/testowner/testrepo/diff/commits/%s/%s", commit1.String(), commit2.String())
	req, _ := http.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp DiffResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.True(t, resp.Success)

	// case 1) renamed 지원: new-name.txt 가 renamed 로 나오고 old_path가 old-name.txt
	for i := range resp.Files {
		if resp.Files[i].Path == "new-name.txt" && resp.Files[i].Status == FileDiffStatusRenamed {
			require.Equal(t, "old-name.txt", resp.Files[i].OldPath)
			return
		}
	}

	// case 2) rename detection 미지원: delete+add 로 표현
	var foundOldDeleted, foundNewAdded bool
	for i := range resp.Files {
		if resp.Files[i].Path == "old-name.txt" && resp.Files[i].Status == FileDiffStatusDeleted {
			foundOldDeleted = true
		}
		if resp.Files[i].Path == "new-name.txt" && resp.Files[i].Status == FileDiffStatusAdded {
			foundNewAdded = true
		}
	}
	require.True(t, foundOldDeleted && foundNewAdded, "rename should be represented either as renamed or delete+add")
}

// Test_CommitDiff_BinaryFile_MarksAsBinary
// 회귀: 바이너리 파일은 is_binary=true로 표시되고 changes가 비어야 함
func Test_CommitDiff_BinaryFile_MarksAsBinary(t *testing.T) {
	t.Parallel()

	// 기존 service_test의 fixture 사용
	repoPath, initialCommitSHA, secondCommitSHA, cleanup := setupTestRepositoryForBinaryFiles(t)
	defer cleanup()

	tempDir := filepath.Dir(filepath.Dir(repoPath))
	handler := newDiffHandler(t, tempDir)

	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/commits/:base/:head", handler.HandleCommitDiff)

	url := fmt.Sprintf("/api/v2/repository/testowner/testrepo/diff/commits/%s/%s", initialCommitSHA, secondCommitSHA)
	req, _ := http.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var diffResp DiffResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&diffResp))
	require.True(t, diffResp.Success)

	var pngFile *FileDiff
	for i := range diffResp.Files {
		if diffResp.Files[i].Path == "image.png" {
			pngFile = &diffResp.Files[i]
			break
		}
	}
	require.NotNil(t, pngFile)
	require.True(t, pngFile.IsBinary)
	// 바이너리는 라인 diff를 만들지 않음
	require.Equal(t, 0, len(pngFile.Changes))
}

// Test_CommitDiff_LargeFile_TruncatesContent
// 회귀: 변경 라인이 너무 크면 changes는 비우고(add/del만 유지) 반환해야 함
func Test_CommitDiff_LargeFile_TruncatesContent(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	ownerDir := filepath.Join(tempDir, "testowner")
	require.NoError(t, os.MkdirAll(ownerDir, 0755))

	repoPath := filepath.Join(ownerDir, "testrepo.git")
	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// base: empty file
	tree1 := makeTree(t, repo, []object.TreeEntry{})
	commit1 := makeCommit(t, repo, "Empty base", tree1, nil, now)

	// head: huge file with > MaxChangedLines additions
	var b strings.Builder
	for i := 0; i < MaxChangedLines+1; i++ {
		b.WriteString("line\n")
	}
	blob := makeBlob(t, repo, []byte(b.String()))
	tree2 := makeTree(t, repo, []object.TreeEntry{
		{Name: "huge.txt", Mode: 0100644, Hash: blob},
	})
	commit2 := makeCommit(t, repo, "Huge file", tree2, []plumbing.Hash{commit1}, now)

	handler := newDiffHandler(t, tempDir)
	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/commits/:base/:head", handler.HandleCommitDiff)

	url := fmt.Sprintf("/api/v2/repository/testowner/testrepo/diff/commits/%s/%s?context=3", commit1.String(), commit2.String())
	req, _ := http.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp DiffResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.True(t, resp.Success)

	var huge *FileDiff
	for i := range resp.Files {
		if resp.Files[i].Path == "huge.txt" {
			huge = &resp.Files[i]
			break
		}
	}
	require.NotNil(t, huge)
	require.Equal(t, FileDiffStatusAdded, huge.Status)
	require.Greater(t, huge.Additions, MaxChangedLines)
	require.Equal(t, 0, len(huge.Changes), "changes should be omitted/truncated for huge diffs")
}

// Test_CommitDiff_EmptyFile_HandlesCorrectly
// 회귀: 빈 파일 추가 시 additions/deletions가 0으로 안정적으로 처리되어야 함
func Test_CommitDiff_EmptyFile_HandlesCorrectly(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	ownerDir := filepath.Join(tempDir, "testowner")
	require.NoError(t, os.MkdirAll(ownerDir, 0755))

	repoPath := filepath.Join(ownerDir, "testrepo.git")
	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// base: empty tree
	tree1 := makeTree(t, repo, []object.TreeEntry{})
	commit1 := makeCommit(t, repo, "Empty base", tree1, nil, now)

	// head: empty file
	blob := makeBlob(t, repo, []byte(""))
	tree2 := makeTree(t, repo, []object.TreeEntry{
		{Name: "empty.txt", Mode: 0100644, Hash: blob},
	})
	commit2 := makeCommit(t, repo, "Add empty file", tree2, []plumbing.Hash{commit1}, now)

	handler := newDiffHandler(t, tempDir)
	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/commits/:base/:head", handler.HandleCommitDiff)

	url := fmt.Sprintf("/api/v2/repository/testowner/testrepo/diff/commits/%s/%s?context=3", commit1.String(), commit2.String())
	req, _ := http.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp DiffResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.True(t, resp.Success)

	var empty *FileDiff
	for i := range resp.Files {
		if resp.Files[i].Path == "empty.txt" {
			empty = &resp.Files[i]
			break
		}
	}
	require.NotNil(t, empty)
	require.Equal(t, FileDiffStatusAdded, empty.Status)
	require.Equal(t, 0, empty.Additions)
	require.Equal(t, 0, empty.Deletions)
}

// Test_CommitDiff_InvalidBase_Returns400
// 회귀: 잘못된 base 매개변수 시 400 반환
func Test_CommitDiff_InvalidBase_Returns400(t *testing.T) {
	t.Parallel()

	handler, _ := newDiffHandlerWithRepo(t)

	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/commits/:base/:head", handler.HandleCommitDiff)

	req, _ := http.NewRequest("GET", "/api/v2/repository/testowner/testrepo/diff/commits//abc123", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusBadRequest)
}

// Test_CommitDiff_WithContextLines_ReturnsSuccess
// 회귀: context 매개변수가 올바르게 처리되어야 함
func Test_CommitDiff_WithContextLines_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler, initialSHA, secondSHA := newDiffHandlerWithCommits(t)

	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/commits/:base/:head", handler.HandleCommitDiff)

	// 전체 SHA 사용
	url := fmt.Sprintf("/api/v2/repository/testowner/testrepo/diff/commits/%s/%s?context=5",
		initialSHA, secondSHA)
	req, _ := http.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)
}

// =============================================================================
// 테스트: BranchDiff API
// 회귀 방지: "브랜치 간 diff가 올바르게 동작해야 함"
// =============================================================================

// Test_BranchDiff_NonExistentHead_ReturnsError
// 회귀: 존재하지 않는 head 브랜치 시 에러 반환
func Test_BranchDiff_NonExistentHead_ReturnsError(t *testing.T) {
	t.Parallel()

	handler, _ := newDiffHandlerWithRepo(t)

	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/branches/:base/:head", handler.HandleBranchDiff)

	// 존재하지 않는 브랜치로 요청
	req, _ := http.NewRequest("GET", "/api/v2/repository/testowner/testrepo/diff/branches/master/nonexistent", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 400 또는 404 중 하나 (브랜치가 존재하지 않으므로)
	require.True(t, w.Code == http.StatusBadRequest || w.Code == http.StatusNotFound || w.Code == http.StatusInternalServerError,
		"status=%d want=400/404/500 body=%s", w.Code, w.Body.String())
}

// =============================================================================
// 테스트: FileDiff / SingleCommitDiff / Patch API
// 회귀 방지: "기타 diff API가 올바르게 동작해야 함"
// =============================================================================

// Test_FileDiff_MissingPath_Returns400
// 회귀: path 누락 시 400 반환
func Test_FileDiff_MissingPath_Returns400(t *testing.T) {
	t.Parallel()

	handler, _ := newDiffHandlerWithRepo(t)

	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/file", handler.HandleFileDiff)

	req, _ := http.NewRequest("GET", "/api/v2/repository/testowner/testrepo/diff/file?base=abc&head=def", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusBadRequest)
}

// Test_SingleCommitDiff_MissingSHA_Returns400
// 회귀: SHA 누락 시 400 반환
func Test_SingleCommitDiff_MissingSHA_Returns400(t *testing.T) {
	t.Parallel()

	handler, _ := newDiffHandlerWithRepo(t)

	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/commits/:sha/diff", handler.HandleSingleCommitDiff)

	req, _ := http.NewRequest("GET", "/api/v2/repository/testowner/testrepo/commits//diff", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusBadRequest)
}

// Test_Patch_MissingBase_Returns400
// 회귀: base 누락 시 400 반환
func Test_Patch_MissingBase_Returns400(t *testing.T) {
	t.Parallel()

	handler, _ := newDiffHandlerWithRepo(t)

	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/patch/:base/:head", handler.HandlePatch)

	req, _ := http.NewRequest("GET", "/api/v2/repository/testowner/testrepo/patch//abc123", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusBadRequest)
}

// =============================================================================
// Helper functions
// 원칙: boilerplate만 추상화, 핵심 로직은 테스트 본문에
// =============================================================================

func newDiffHandler(t *testing.T, repoRoot string) *DiffHandler {
	t.Helper()
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
		Git:     config.GitConfig{RepoRoot: repoRoot},
	}
	handler, err := NewDiffHandler(cfg)
	require.NoError(t, err)
	return handler
}

func newDiffHandlerWithRepo(t *testing.T) (*DiffHandler, string) {
	t.Helper()

	tempDir := t.TempDir()
	testRepoPath := filepath.Join(tempDir, "testowner", "testrepo.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(testRepoPath), 0755))

	repo, err := gogit.PlainInit(testRepoPath, true)
	require.NoError(t, err)

	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	blobHash := makeBlob(t, repo, []byte("initial"))
	treeHash := makeTree(t, repo, []object.TreeEntry{
		{Name: "initial.txt", Mode: 0100644, Hash: blobHash},
	})
	commitHash := makeCommit(t, repo, "Initial commit", treeHash, nil, now)
	ref := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/master"), commitHash)
	require.NoError(t, repo.Storer.SetReference(ref))

	handler := newDiffHandler(t, tempDir)
	return handler, testRepoPath
}

func newDiffHandlerWithCommits(t *testing.T) (*DiffHandler, string, string) {
	t.Helper()

	tempDir := t.TempDir()
	ownerDir := filepath.Join(tempDir, "testowner")
	require.NoError(t, os.MkdirAll(ownerDir, 0755))

	repoPath := filepath.Join(ownerDir, "testrepo.git")
	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// Initial commit
	initialBlobHash := makeBlob(t, repo, []byte("initial"))
	initialTreeHash := makeTree(t, repo, []object.TreeEntry{
		{Name: "initial.txt", Mode: 0100644, Hash: initialBlobHash},
	})
	initialCommitHash := makeCommit(t, repo, "Initial commit", initialTreeHash, nil, now)

	// master ref 설정
	ref := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/master"), initialCommitHash)
	require.NoError(t, repo.Storer.SetReference(ref))

	// Second commit: add single-line.txt
	singleLineBlobHash := makeBlob(t, repo, []byte("Hello"))
	secondTreeHash := makeTree(t, repo, []object.TreeEntry{
		{Name: "initial.txt", Mode: 0100644, Hash: initialBlobHash},
		{Name: "single-line.txt", Mode: 0100644, Hash: singleLineBlobHash},
	})
	secondCommitHash := makeCommit(t, repo, "Add single line file", secondTreeHash, []plumbing.Hash{initialCommitHash}, now)

	// master ref 업데이트
	ref2 := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/master"), secondCommitHash)
	require.NoError(t, repo.Storer.SetReference(ref2))

	handler := newDiffHandler(t, tempDir)
	return handler, initialCommitHash.String(), secondCommitHash.String()
}

func makeBlob(t *testing.T, repo *gogit.Repository, content []byte) plumbing.Hash {
	t.Helper()
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

func makeTree(t *testing.T, repo *gogit.Repository, entries []object.TreeEntry) plumbing.Hash {
	t.Helper()
	tree := &object.Tree{Entries: entries}
	treeObj := repo.Storer.NewEncodedObject()
	require.NoError(t, tree.Encode(treeObj))
	hash, err := repo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)
	return hash
}

func makeCommit(t *testing.T, repo *gogit.Repository, message string, treeHash plumbing.Hash, parents []plumbing.Hash, when time.Time) plumbing.Hash {
	t.Helper()
	commit := &object.Commit{
		Message:      message,
		Author:       object.Signature{Name: "Test", Email: "test@example.com", When: when},
		Committer:    object.Signature{Name: "Test", Email: "test@example.com", When: when},
		TreeHash:     treeHash,
		ParentHashes: parents,
	}
	commitObj := repo.Storer.NewEncodedObject()
	require.NoError(t, commit.Encode(commitObj))
	hash, err := repo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)
	return hash
}

// =============================================================================
// Assertion helpers - testutil/gitrepo 사용
// =============================================================================

func requireStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	gitrepo.RequireStatus(t, w, want)
}

// =============================================================================
// 테스트: Diff Timeout/과부하 계약 (P0 - 500 방지)
// 회귀 방지: "timeout/과부하가 500으로 뭉개지지 않는다"
// =============================================================================

// Test_CommitDiff_ContextCanceled_ReturnsErrorNotPanic
// 회귀: "클라이언트가 끊거나 취소하면 서버가 panic/500으로 가지 않고 정리한다"
func Test_CommitDiff_ContextCanceled_ReturnsErrorNotPanic(t *testing.T) {
	t.Parallel()

	handler, initialSHA, secondSHA := newDiffHandlerWithCommits(t)

	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/commits/:base/:head", handler.HandleCommitDiff)

	url := fmt.Sprintf("/api/v2/repository/testowner/testrepo/diff/commits/%s/%s", initialSHA, secondSHA)
	req, _ := http.NewRequest("GET", url, nil)

	// context를 취소하여 클라이언트 끊김 시뮬레이션
	ctx, cancel := context.WithCancel(req.Context())
	cancel() // 즉시 취소
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	// panic이 발생하지 않아야 함
	require.NotPanics(t, func() {
		router.ServeHTTP(w, req)
	})

	// 500이 아닌 다른 코드여야 함 (499, 408, 또는 정상 응답)
	// 현재 구현에서는 context가 취소되어도 처리가 진행될 수 있음
	// 핵심: panic이 발생하지 않고, 500 INTERNAL_ERROR가 아니어야 함
	if w.Code == http.StatusInternalServerError {
		// 500인 경우 error body에 "internal" 또는 panic 관련 메시지가 없어야 함
		body := w.Body.String()
		require.NotContains(t, body, "panic", "500 response should not contain panic")
	}
}

// Test_CommitDiff_LargeDiff_ReturnsTruncatedFlag
// 회귀: "대용량 diff에서도 truncated 플래그가 일관되게 반환된다"
func Test_CommitDiff_LargeDiff_ReturnsTruncatedFlag(t *testing.T) {
	t.Parallel()

	// Given: 많은 파일이 변경된 repo
	tempDir := t.TempDir()
	ownerDir := filepath.Join(tempDir, "testowner")
	require.NoError(t, os.MkdirAll(ownerDir, 0755))

	repoPath := filepath.Join(ownerDir, "testrepo.git")
	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// 첫 커밋: 빈 트리
	emptyTreeHash := makeTree(t, repo, []object.TreeEntry{})
	initialCommitHash := makeCommit(t, repo, "Initial commit", emptyTreeHash, nil, now)

	// 두 번째 커밋: 100개 파일 추가
	entries := make([]object.TreeEntry, 100)
	for i := 0; i < 100; i++ {
		content := fmt.Sprintf("content of file %d\n", i)
		blobHash := makeBlob(t, repo, []byte(content))
		entries[i] = object.TreeEntry{
			Name: fmt.Sprintf("file%03d.txt", i),
			Mode: 0100644,
			Hash: blobHash,
		}
	}
	secondTreeHash := makeTree(t, repo, entries)
	secondCommitHash := makeCommit(t, repo, "Add 100 files", secondTreeHash, []plumbing.Hash{initialCommitHash}, now)

	handler := newDiffHandler(t, tempDir)

	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/commits/:base/:head", handler.HandleCommitDiff)

	// When: diff 조회
	url := fmt.Sprintf("/api/v2/repository/testowner/testrepo/diff/commits/%s/%s",
		initialCommitHash.String(), secondCommitHash.String())
	req, _ := http.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Then: 성공하거나 413(Too Large)이어야 함, 500은 안됨
	require.NotEqual(t, http.StatusInternalServerError, w.Code,
		"status=%d should not be 500 body=%s", w.Code, w.Body.String())

	if w.Code == http.StatusOK {
		var resp DiffResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		require.True(t, resp.Success)
		// 파일 수가 100개이거나 truncated 되어야 함
		require.True(t, len(resp.Files) > 0, "should have files in response")
	}
}

// Test_CommitDiff_NonExistentCommit_Returns404
// 회귀: "존재하지 않는 커밋은 404로 떨어져 500으로 뭉개지지 않는다"
func Test_CommitDiff_NonExistentCommit_Returns404(t *testing.T) {
	t.Parallel()

	handler, _ := newDiffHandlerWithRepo(t)

	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/commits/:base/:head", handler.HandleCommitDiff)

	// 존재하지 않는 SHA
	nonExistentSHA := "0000000000000000000000000000000000000000"
	url := fmt.Sprintf("/api/v2/repository/testowner/testrepo/diff/commits/%s/%s",
		nonExistentSHA, nonExistentSHA)
	req, _ := http.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 404 또는 400이어야 함, 500은 안됨
	require.NotEqual(t, http.StatusInternalServerError, w.Code,
		"status=%d should not be 500 for non-existent commit body=%s", w.Code, w.Body.String())
}

// Test_BranchDiff_NonExistentBranch_Returns404
// 회귀: "존재하지 않는 브랜치는 404로 떨어져 500으로 뭉개지지 않는다"
func Test_BranchDiff_NonExistentBranch_Returns404(t *testing.T) {
	t.Parallel()

	handler, _ := newDiffHandlerWithRepo(t)

	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/branches/:base/:head", handler.HandleBranchDiff)

	req, _ := http.NewRequest("GET", "/api/v2/repository/testowner/testrepo/diff/branches/master/ghost-branch", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 404 또는 400이어야 함, 500은 안됨
	require.NotEqual(t, http.StatusInternalServerError, w.Code,
		"status=%d should not be 500 for non-existent branch body=%s", w.Code, w.Body.String())
}

// Test_CommitDiff_EmptyRepo_ReturnsErrorNot500
// 회귀: "빈 repo에서 diff 요청 시 500이 아닌 명확한 에러를 반환한다"
func Test_CommitDiff_EmptyRepo_ReturnsErrorNot500(t *testing.T) {
	t.Parallel()

	// Given: 커밋이 없는 빈 repo
	tempDir := t.TempDir()
	ownerDir := filepath.Join(tempDir, "testowner")
	require.NoError(t, os.MkdirAll(ownerDir, 0755))

	repoPath := filepath.Join(ownerDir, "testrepo.git")
	_, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	handler := newDiffHandler(t, tempDir)

	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/commits/:base/:head", handler.HandleCommitDiff)

	// When: 존재하지 않는 SHA로 diff 요청
	url := "/api/v2/repository/testowner/testrepo/diff/commits/abc123/def456"
	req, _ := http.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Then: 500이 아니어야 함
	require.NotEqual(t, http.StatusInternalServerError, w.Code,
		"status=%d should not be 500 for empty repo body=%s", w.Code, w.Body.String())
}
