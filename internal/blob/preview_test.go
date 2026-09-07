package blob

import (
	"context"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupPreviewRepo 는 판정 경계를 덮는 파일들이 든 bare 저장소를 만든다.
func setupPreviewRepo(t *testing.T) string {
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
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")

	// 상한 이하 바이너리 — 판정만 하고 본문은 읽지 않아야 하는 대상.
	underCap := make([]byte, MaxInlinePreviewBytes/2)
	_, err := rand.Read(underCap)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(work, "under-cap.bin"), underCap, 0o644))

	// 상한 초과.
	overCap := make([]byte, MaxInlinePreviewBytes+1)
	_, err = rand.Read(overCap)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(work, "over-cap.bin"), overCap, 0o644))

	require.NoError(t, os.WriteFile(filepath.Join(work, "main.go"), []byte("package main\n"), 0o644))
	run("add", ".")
	run("commit", "-qm", "init")

	bare := filepath.Join(t.TempDir(), "repo.git")
	run("clone", "--bare", "-q", work, bare)
	return bare
}

func statOf(t *testing.T, repoPath, path string) BlobStat {
	t.Helper()
	stat, err := NativeStatBlob(context.Background(), repoPath, "main", path)
	require.NoError(t, err)
	return stat
}

// Test_PreviewBlob_BinaryUnderCap_SkipsContent 는 상한 이하 바이너리라도 본문을
// 메모리에 올리지 않는지 확인한다 — 판정은 앞 SniffBytes 로 끝나므로 "미리보기
// 하지 않을 파일" 을 위해 나머지를 읽을 이유가 없다.
func Test_PreviewBlob_BinaryUnderCap_SkipsContent(t *testing.T) {
	repoPath := setupPreviewRepo(t)
	stat := statOf(t, repoPath, "under-cap.bin")
	require.Less(t, stat.Size, MaxInlinePreviewBytes, "상한 이하 fixture 여야 한다")

	preview, err := PreviewBlob(context.Background(), repoPath, stat, "under-cap.bin")

	require.NoError(t, err)
	assert.True(t, preview.IsBinary)
	assert.Empty(t, preview.Content, "바이너리는 본문을 읽지 않는다")
}

// Test_PreviewBlob_OverCap_SkipsContent 는 상한 초과 blob 이 종류와 무관하게
// inline 렌더 대상에서 빠지는지 확인한다.
func Test_PreviewBlob_OverCap_SkipsContent(t *testing.T) {
	repoPath := setupPreviewRepo(t)
	stat := statOf(t, repoPath, "over-cap.bin")

	preview, err := PreviewBlob(context.Background(), repoPath, stat, "over-cap.bin")

	require.NoError(t, err)
	assert.True(t, preview.IsBinary)
	assert.Empty(t, preview.Content)
}

// Test_PreviewBlob_Text_ReadsContent 는 텍스트만 본문이 채워지는지 확인한다.
func Test_PreviewBlob_Text_ReadsContent(t *testing.T) {
	repoPath := setupPreviewRepo(t)
	stat := statOf(t, repoPath, "main.go")

	preview, err := PreviewBlob(context.Background(), repoPath, stat, "main.go")

	require.NoError(t, err)
	assert.False(t, preview.IsBinary)
	assert.Equal(t, "package main\n", string(preview.Content))
	assert.True(t, strings.HasPrefix(preview.ContentType, "text/"), "got %q", preview.ContentType)
}
