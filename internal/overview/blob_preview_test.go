package overview

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/blob"
)

// setupPreviewRepo 는 미리보기 상한 위/아래 파일이 섞인 bare 저장소를 만든다.
func setupPreviewRepo(t *testing.T, repoRoot, owner, repo string) {
	t.Helper()

	work := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")

	// 미리보기 상한을 넘는 바이너리 (Go 빌드 산출물 대역) — 압축되지 않게 랜덤.
	oversize := make([]byte, blob.MaxInlinePreviewBytes+1)
	_, err := rand.Read(oversize)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(work, "app"), oversize, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(work, "huge.png"), oversize, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(work, "main.go"), []byte("package main\n"), 0o644))

	// zip 은 mime DB 가 아니라 내용 sniff 로만 맞힐 수 있는 타입 — 상한 위/아래
	// 두 벌을 둬서 판정이 크기에 좌우되지 않는지 본다.
	zipMagic := []byte{'P', 'K', 0x03, 0x04}
	require.NoError(t, os.WriteFile(filepath.Join(work, "small.zip"), append(zipMagic, oversize[:64]...), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(work, "big.zip"), append(zipMagic, oversize...), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(work, "dir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(work, "dir", "f.txt"), []byte("x\n"), 0o644))
	run("add", ".")
	run("commit", "-qm", "init")

	repoPath := filepath.Join(repoRoot, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0o755))
	run("clone", "--bare", "-q", work, repoPath)
}

func getBlobOverview(t *testing.T, h *RepositoryOverviewHandler, path string) (int, BlobOverviewResponse) {
	t.Helper()

	router := gin.New()
	router.GET("/repos/:owner/:repo/overview/blob", adaptQuery(h.HandleGetBlobOverview))

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/repos/own/rp/overview/blob?ref=main&path="+path, nil))

	var resp BlobOverviewResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w.Code, resp
}

// Test_BlobOverview_OversizeBinary_IsNotFoundRegression 은 미리보기 상한을 넘는
// 바이너리가 404 "파일 없음" 으로 오분류되던 회귀를 막는다.
//
// 원인은 "내용을 다 읽은 뒤에 binary 판정" 이라는 순서였다 — 판정에 도달하기
// 전에 출력 상한에 걸리고, 그 에러가 FILE_NOT_FOUND 로 뭉개졌다. 파일은
// 존재하므로 200 이어야 하고, 내용 없이 메타데이터만 내려가야 한다.
func Test_BlobOverview_OversizeBinary_IsNotFoundRegression(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()
	setupPreviewRepo(t, tempDir, "own", "rp")

	code, resp := getBlobOverview(t, handler, "app")

	require.Equal(t, http.StatusOK, code)
	assert.True(t, resp.IsBinary, "상한 초과 blob 은 inline 미리보기 대상이 아니다")
	assert.Empty(t, resp.Content, "내용을 읽지 않아야 한다")
	assert.Equal(t, blob.MaxInlinePreviewBytes+1, resp.Size, "size 는 실제 크기여야 한다")
	assert.Equal(t, "application/octet-stream", resp.ContentType,
		"판정은 앞부분 sniff — 확장자 없는 바이너리는 octet-stream")
}

// Test_BlobOverview_OversizeImage_KeepsContentType 은 내용을 읽지 않는 경로에서도
// content_type 이 유지되는지 확인한다 — client 는 content_type 이 image/* 인지로
// raw 미리보기 여부를 정하므로, 여기서 octet-stream 으로 떨어지면 큰 이미지의
// 미리보기가 사라진다.
func Test_BlobOverview_OversizeImage_KeepsContentType(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()
	setupPreviewRepo(t, tempDir, "own", "rp")

	code, resp := getBlobOverview(t, handler, "huge.png")

	require.Equal(t, http.StatusOK, code)
	assert.True(t, resp.IsBinary)
	assert.Equal(t, "image/png", resp.ContentType)
}

// Test_BlobOverview_ContentTypeIsSizeIndependent 는 같은 종류 파일이 크기에
// 따라 다른 content_type 을 받지 않는지 확인한다. 확장자가 mime DB 에 없는
// 타입(zip)은 내용 sniff 로만 맞힐 수 있으므로, 상한 초과 경로에서도 앞부분을
// 읽어 같은 판정을 태워야 한다.
func Test_BlobOverview_ContentTypeIsSizeIndependent(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()
	setupPreviewRepo(t, tempDir, "own", "rp")

	_, small := getBlobOverview(t, handler, "small.zip")
	_, big := getBlobOverview(t, handler, "big.zip")

	assert.Equal(t, "application/zip", small.ContentType)
	assert.Equal(t, small.ContentType, big.ContentType,
		"상한 초과라고 content_type 이 달라지면 안 된다")
	assert.True(t, big.IsBinary)
	assert.Empty(t, big.Content)
}

// Test_BlobOverview_SmallText_StillPreviews 는 상한 이하 텍스트의 기존 동작이
// 그대로인지 확인한다.
func Test_BlobOverview_SmallText_StillPreviews(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()
	setupPreviewRepo(t, tempDir, "own", "rp")

	code, resp := getBlobOverview(t, handler, "main.go")

	require.Equal(t, http.StatusOK, code)
	assert.False(t, resp.IsBinary)
	assert.Equal(t, "package main\n", resp.Content)
	assert.Equal(t, int64(13), resp.Size)
}

// Test_BlobOverview_ErrorsAreClassified 는 실패 원인이 status 로 구분되는지
// 확인한다 — 이전에는 없음/디렉토리/상한초과가 모두 404 였다.
func Test_BlobOverview_ErrorsAreClassified(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()
	setupPreviewRepo(t, tempDir, "own", "rp")

	t.Run("missing path is 404", func(t *testing.T) {
		code, _ := getBlobOverview(t, handler, "nope.txt")
		assert.Equal(t, http.StatusNotFound, code)
	})

	t.Run("directory is 400", func(t *testing.T) {
		code, _ := getBlobOverview(t, handler, "dir")
		assert.Equal(t, http.StatusBadRequest, code)
	})
}
