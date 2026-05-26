package overview

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/gin-gonic/gin"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestHandlerWithBlobConfig 는 setupTestHandler 와 동일하나 Blob preview
// 임계치를 명시한다. 임계치를 0 으로 두면 가드 비활성화이므로 가드 동작
// 회귀 테스트에는 양수가 필요.
func setupTestHandlerWithBlobConfig(t *testing.T, blobCfg config.BlobConfig) (*RepositoryOverviewHandler, string, func()) {
	t.Helper()

	tempDir, err := os.MkdirTemp("", "overview-preview-test-*")
	require.NoError(t, err)

	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
		Git:     config.GitConfig{RepoRoot: tempDir},
		Events:  config.EventsConfig{Enabled: false},
		Blob:    blobCfg,
	}

	handler, err := NewRepositoryOverviewHandler(cfg)
	require.NoError(t, err)

	cleanup := func() { _ = os.RemoveAll(tempDir) }
	return handler, tempDir, cleanup
}

// createTestRepoWithFiles 는 임의의 path→content 매핑을 가진 bare repo 를
// 생성한다. preview 가드 회귀 테스트용.
func createTestRepoWithFiles(t *testing.T, tempDir, owner, repo string, files map[string][]byte) {
	t.Helper()

	repoPath := filepath.Join(tempDir, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	tempWorkDir, err := os.MkdirTemp("", "overview-preview-work-*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempWorkDir) }()

	workRepo, err := gogit.PlainInit(tempWorkDir, false)
	require.NoError(t, err)
	worktree, err := workRepo.Worktree()
	require.NoError(t, err)

	for relPath, content := range files {
		full := filepath.Join(tempWorkDir, relPath)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
		require.NoError(t, os.WriteFile(full, content, 0644))
		_, err = worktree.Add(relPath)
		require.NoError(t, err)
	}

	_, err = worktree.Commit("fixture", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Unix(1700000000, 0).UTC(),
		},
	})
	require.NoError(t, err)

	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	head, err := workRepo.Head()
	require.NoError(t, err)
	commit, err := workRepo.CommitObject(head.Hash())
	require.NoError(t, err)
	commitObj, err := workRepo.Storer.EncodedObject(plumbing.CommitObject, head.Hash())
	require.NoError(t, err)
	_, err = gitRepo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)
	require.NoError(t, copyTreeRecursiveForHandlerTest(workRepo, gitRepo, commit.TreeHash))

	mainRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), head.Hash())
	require.NoError(t, gitRepo.Storer.SetReference(mainRef))
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))
	require.NoError(t, gitRepo.Storer.SetReference(headRef))
}

func fetchBlobOverview(t *testing.T, handler *RepositoryOverviewHandler, owner, repo, path string) BlobOverviewResponse {
	t.Helper()
	router := gin.New()
	router.GET("/repos/:owner/:repo/overview/blob", adapt(handler.HandleGetBlobOverview))

	req := httptest.NewRequest(http.MethodGet,
		"/repos/"+owner+"/"+repo+"/overview/blob?ref=main&path="+path, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	var resp BlobOverviewResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp
}

// TestPreview_LongLine_BlocksInlineContent 는 Issue-3144 의 정확한 시나리오를
// 회귀한다 — 전체 size 는 작지만 한 줄이 매우 긴 파일에서 Content 가 비고
// PreviewState=long_lines 가 반환되어야 한다.
func TestPreview_LongLine_BlocksInlineContent(t *testing.T) {
	cfg := config.BlobConfig{
		PreviewMaxBytes:      256 * 1024,
		PreviewMaxLines:      10000,
		PreviewMaxLineLength: 4096,
	}
	handler, tempDir, cleanup := setupTestHandlerWithBlobConfig(t, cfg)
	defer cleanup()

	// 한 줄 8KB — long_lines 임계치 (4KB) 초과, 전체 size 는 임계치 (256KB) 미만.
	// generated 휴리스틱은 매칭 안 되도록 sourcemap 마커는 넣지 않는다.
	bigLine := strings.Repeat("x", 8192)
	files := map[string][]byte{
		"src/widefile.txt": []byte("ok\n" + bigLine + "\nfin\n"),
	}
	createTestRepoWithFiles(t, tempDir, "o", "r", files)

	resp := fetchBlobOverview(t, handler, "o", "r", "src/widefile.txt")

	assert.Equal(t, PreviewStateLongLines, resp.PreviewState)
	assert.NotEmpty(t, resp.PreviewReason)
	assert.Empty(t, resp.Content, "Content must be empty when long_lines is set")
	assert.Zero(t, resp.Lines)
	assert.Equal(t, 8192, resp.MaxLineLength)
}

// TestPreview_SourcemapDataURI_BlocksInlineContent 는 inline sourcemap data URI
// 가 들어간 파일 (Issue-3144 실제 케이스) 에서 PreviewState=generated 가
// 우선 매칭되어야 함을 검증한다.
func TestPreview_SourcemapDataURI_BlocksInlineContent(t *testing.T) {
	cfg := config.BlobConfig{
		PreviewMaxBytes:      256 * 1024,
		PreviewMaxLines:      10000,
		PreviewMaxLineLength: 4096,
	}
	handler, tempDir, cleanup := setupTestHandlerWithBlobConfig(t, cfg)
	defer cleanup()

	body := "export const x = 1;\n" +
		"//# sourceMappingURL=data:application/json;base64,eyJ2IjozfQ==\n"
	files := map[string][]byte{"src/main.tsx": []byte(body)}
	createTestRepoWithFiles(t, tempDir, "o", "r", files)

	resp := fetchBlobOverview(t, handler, "o", "r", "src/main.tsx")

	assert.Equal(t, PreviewStateGenerated, resp.PreviewState)
	assert.Contains(t, resp.PreviewReason, "inline source map")
	assert.Empty(t, resp.Content)
}

// TestPreview_TooLargeBytes_BlocksInlineContent 는 size 임계치 초과 시
// PreviewState=too_large 분기를 검증.
func TestPreview_TooLargeBytes_BlocksInlineContent(t *testing.T) {
	cfg := config.BlobConfig{
		PreviewMaxBytes:      8192, // 8KB
		PreviewMaxLines:      10000,
		PreviewMaxLineLength: 4096,
	}
	handler, tempDir, cleanup := setupTestHandlerWithBlobConfig(t, cfg)
	defer cleanup()

	// 라인당 100 chars × 200 lines = 20KB → 8KB 임계치 초과.
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString(strings.Repeat("a", 100))
		b.WriteByte('\n')
	}
	files := map[string][]byte{"big.txt": []byte(b.String())}
	createTestRepoWithFiles(t, tempDir, "o", "r", files)

	resp := fetchBlobOverview(t, handler, "o", "r", "big.txt")

	assert.Equal(t, PreviewStateTooLarge, resp.PreviewState)
	assert.Contains(t, resp.PreviewReason, "threshold")
	assert.Empty(t, resp.Content)
}

// TestPreview_RegularFile_OK 는 정상 파일이 PreviewStateOK 로 Content 를
// 그대로 노출하는지 (기존 동작 회귀) 검증한다.
func TestPreview_RegularFile_OK(t *testing.T) {
	cfg := config.BlobConfig{
		PreviewMaxBytes:      256 * 1024,
		PreviewMaxLines:      10000,
		PreviewMaxLineLength: 4096,
	}
	handler, tempDir, cleanup := setupTestHandlerWithBlobConfig(t, cfg)
	defer cleanup()

	files := map[string][]byte{"README.md": []byte("# Hi\nworld\n")}
	createTestRepoWithFiles(t, tempDir, "o", "r", files)

	resp := fetchBlobOverview(t, handler, "o", "r", "README.md")

	assert.Equal(t, PreviewStateOK, resp.PreviewState)
	assert.Empty(t, resp.PreviewReason)
	assert.Equal(t, "# Hi\nworld\n", resp.Content)
	assert.Equal(t, 2, resp.Lines)
	assert.Equal(t, 5, resp.MaxLineLength) // "world" = 5 chars
}

// TestPreview_GuardsDisabled_WhenThresholdsZero 는 임계치가 0 (운영 escape
// hatch) 일 때 가드가 비활성화되고 PreviewStateOK 로 떨어지는지 확인.
func TestPreview_GuardsDisabled_WhenThresholdsZero(t *testing.T) {
	cfg := config.BlobConfig{
		PreviewMaxBytes:      0,
		PreviewMaxLines:      0,
		PreviewMaxLineLength: 0,
	}
	handler, tempDir, cleanup := setupTestHandlerWithBlobConfig(t, cfg)
	defer cleanup()

	// 8KB 한 줄 — 임계치가 있었으면 long_lines 였을 것.
	files := map[string][]byte{"wide.txt": []byte(strings.Repeat("x", 8192) + "\n")}
	createTestRepoWithFiles(t, tempDir, "o", "r", files)

	resp := fetchBlobOverview(t, handler, "o", "r", "wide.txt")

	assert.Equal(t, PreviewStateOK, resp.PreviewState)
	assert.NotEmpty(t, resp.Content)
}
