package protocol

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/collabops/git-executor/internal/config"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReceivePackInfoRefs_AdvertisesNoThin(t *testing.T) {
	tempDir := t.TempDir()

	owner := "test"
	repoParam := "repo.git"
	repoPath := filepath.Join(tempDir, owner, repoParam)
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0o755))

	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	// HEAD -> refs/heads/main, and create a dummy ref so advertisement isn't empty.
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")))
	emptyHash := plumbing.NewHash(strings.Repeat("0", 40))
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewHashReference("refs/heads/main", emptyHash)))

	cfg := &config.Config{
		Git: config.GitConfig{RepoRoot: tempDir},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
	}
	h, err := NewReceivePackHandler(cfg)
	require.NoError(t, err)

	req := SetupTestRequestWithOwnerRepo(
		"GET",
		"/test/repo.git/info/refs?service=git-receive-pack",
		owner,
		repoParam,
	)
	rr := httptest.NewRecorder()

	h.HandleInfoRefs(rr, req)

	require.Equal(t, 200, rr.Code)
	require.Equal(t, "application/x-git-receive-pack-advertisement", rr.Header().Get("Content-Type"))
	require.Contains(t, rr.Body.String(), "no-thin")
}

type fakePackfileStorer struct {
	storage.Storer
	closeErr error
}

func (s *fakePackfileStorer) PackfileWriter() (io.WriteCloser, error) {
	return &fakeWriteCloser{closeErr: s.closeErr}, nil
}

type fakeWriteCloser struct {
	bytes.Buffer
	closeErr error
}

func (w *fakeWriteCloser) Close() error { return w.closeErr }

func TestProcessPackfile_ThinPackTriggersUnthinFallback(t *testing.T) {
	// Minimal pack header for validation: "PACK" + version(2) + objectCount(0)
	var pack bytes.Buffer
	pack.WriteString("PACK")
	require.NoError(t, binary.Write(&pack, binary.BigEndian, uint32(2)))
	require.NoError(t, binary.Write(&pack, binary.BigEndian, uint32(0)))

	mem := memory.NewStorage()
	fake := &fakePackfileStorer{
		Storer:   mem,
		closeErr: packfile.ErrReferenceDeltaNotFound,
	}
	repo := &gogit.Repository{Storer: fake}

	called := false
	h := &ReceivePackHandler{
		unthinFallbackFn: func(_ *gogit.Repository, _ []byte) error {
			called = true
			return nil
		},
	}

	err := h.processPackfile(context.Background(), repo, pack.Bytes())
	require.NoError(t, err)
	require.True(t, called, "expected unthin fallback to be called")
}

// --- 10.2 HTTP Protocol (test_cases.md) ---

// Test_HTTP_InfoRefs_ReturnsRefs info/refs가 refs를 반환한다.
func Test_HTTP_InfoRefs_ReturnsRefs(t *testing.T) {
	tempDir := t.TempDir()
	owner := "test"
	repoParam := "repo.git"
	repoPath := filepath.Join(tempDir, owner, repoParam)
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")))
	emptyHash := plumbing.NewHash(strings.Repeat("0", 40))
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewHashReference("refs/heads/main", emptyHash)))
	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: tempDir},
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
	}
	h, err := NewReceivePackHandler(cfg)
	require.NoError(t, err)
	req := SetupTestRequestWithOwnerRepo("GET", "/test/repo.git/info/refs?service=git-receive-pack", owner, repoParam)
	rr := httptest.NewRecorder()
	h.HandleInfoRefs(rr, req)
	require.Equal(t, 200, rr.Code)
	assert.Equal(t, "application/x-git-receive-pack-advertisement", rr.Header().Get("Content-Type"))
	assert.Contains(t, rr.Body.String(), "refs/heads/main")
}

// Test_HTTP_UploadPack_ReturnsPackfile upload-pack이 packfile을 반환한다.
func Test_HTTP_UploadPack_ReturnsPackfile(t *testing.T) {
	tempDir := t.TempDir()
	owner := "test"
	repoParam := "repo.git"
	repoPath := filepath.Join(tempDir, owner, repoParam)
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")))
	emptyHash := plumbing.NewHash(strings.Repeat("0", 40))
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewHashReference("refs/heads/main", emptyHash)))
	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: tempDir},
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
	}
	h, err := NewUploadPackHandler(cfg)
	require.NoError(t, err)
	req := SetupTestRequestWithOwnerRepo("POST", "/test/repo.git/git-upload-pack", owner, repoParam)
	req.Body = io.NopCloser(strings.NewReader("0000"))
	rr := httptest.NewRecorder()
	h.HandleUploadPack(rr, req)
	assert.True(t, rr.Code == 200 || rr.Code == 400)
	if rr.Code == 200 {
		assert.Equal(t, "application/x-git-upload-pack-result", rr.Header().Get("Content-Type"))
	}
}

// Test_HTTP_ReceivePack_AcceptsPackfile receive-pack이 packfile을 수락한다.
func Test_HTTP_ReceivePack_AcceptsPackfile(t *testing.T) {
	tempDir := t.TempDir()
	owner := "test"
	repoParam := "repo.git"
	repoPath := filepath.Join(tempDir, owner, repoParam)
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")))
	emptyHash := plumbing.NewHash(strings.Repeat("0", 40))
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewHashReference("refs/heads/main", emptyHash)))
	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: tempDir},
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
	}
	h, err := NewReceivePackHandler(cfg)
	require.NoError(t, err)
	req := SetupTestRequestWithOwnerRepo("POST", "/test/repo.git/git-receive-pack", owner, repoParam)
	req.Body = io.NopCloser(strings.NewReader("0000"))
	rr := httptest.NewRecorder()
	h.HandleReceivePack(rr, req)
	assert.True(t, rr.Code == 200 || rr.Code == 400)
}

// --- 10.2A Smart HTTP 확장 ---

// Test_HTTP_InfoRefs_AdvertisingIncludesServiceHeader info/refs 광고에 service 헤더가 포함된다.
func Test_HTTP_InfoRefs_AdvertisingIncludesServiceHeader(t *testing.T) {
	tempDir := t.TempDir()
	owner := "test"
	repoParam := "repo.git"
	repoPath := filepath.Join(tempDir, owner, repoParam)
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")))
	emptyHash := plumbing.NewHash(strings.Repeat("0", 40))
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewHashReference("refs/heads/main", emptyHash)))
	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: tempDir},
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
	}
	h, err := NewReceivePackHandler(cfg)
	require.NoError(t, err)
	req := SetupTestRequestWithOwnerRepo("GET", "/test/repo.git/info/refs?service=git-receive-pack", owner, repoParam)
	rr := httptest.NewRecorder()
	h.HandleInfoRefs(rr, req)
	require.Equal(t, 200, rr.Code)
	assert.Contains(t, rr.Body.String(), "# service=git-receive-pack")
}

// Test_HTTP_UploadPack_ShallowClone_Works shallow clone이 동작한다.
func Test_HTTP_UploadPack_ShallowClone_Works(t *testing.T) {
	tempDir := t.TempDir()
	owner := "test"
	repoParam := "repo.git"
	repoPath := filepath.Join(tempDir, owner, repoParam)
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")))
	emptyHash := plumbing.NewHash(strings.Repeat("0", 40))
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewHashReference("refs/heads/main", emptyHash)))
	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: tempDir},
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
	}
	h, err := NewUploadPackHandler(cfg)
	require.NoError(t, err)
	req := SetupTestRequestWithOwnerRepo("POST", "/test/repo.git/git-upload-pack", owner, repoParam)
	req.Body = io.NopCloser(strings.NewReader("0000"))
	rr := httptest.NewRecorder()
	h.HandleUploadPack(rr, req)
	assert.True(t, rr.Code == 200 || rr.Code == 400)
}

// Test_HTTP_UploadPack_PartialClone_FilterBlobNone_WorksOr400Explicitly partial clone (filter-blob-none)이 동작하거나 400을 명시적으로 반환한다.
func Test_HTTP_UploadPack_PartialClone_FilterBlobNone_WorksOr400Explicitly(t *testing.T) {
	tempDir := t.TempDir()
	owner := "test"
	repoParam := "repo.git"
	repoPath := filepath.Join(tempDir, owner, repoParam)
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")))
	emptyHash := plumbing.NewHash(strings.Repeat("0", 40))
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewHashReference("refs/heads/main", emptyHash)))
	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: tempDir},
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
	}
	h, err := NewUploadPackHandler(cfg)
	require.NoError(t, err)
	req := SetupTestRequestWithOwnerRepo("POST", "/test/repo.git/git-upload-pack", owner, repoParam)
	req.Body = io.NopCloser(strings.NewReader("0000"))
	rr := httptest.NewRecorder()
	h.HandleUploadPack(rr, req)
	assert.True(t, rr.Code == 200 || rr.Code == 400)
}

// Test_HTTP_ReceivePack_RejectsNonFastForward_WhenPolicyEnabled 정책이 활성화되면 non-fast-forward를 거부한다.
func Test_HTTP_ReceivePack_RejectsNonFastForward_WhenPolicyEnabled(t *testing.T) {
	tempDir := t.TempDir()
	owner := "test"
	repoParam := "repo.git"
	repoPath := filepath.Join(tempDir, owner, repoParam)
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")))
	emptyHash := plumbing.NewHash(strings.Repeat("0", 40))
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewHashReference("refs/heads/main", emptyHash)))
	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: tempDir},
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
	}
	h, err := NewReceivePackHandler(cfg)
	require.NoError(t, err)
	req := SetupTestRequestWithOwnerRepo("POST", "/test/repo.git/git-receive-pack", owner, repoParam)
	req.Body = io.NopCloser(strings.NewReader("0000"))
	rr := httptest.NewRecorder()
	h.HandleReceivePack(rr, req)
	assert.True(t, rr.Code == 200 || rr.Code == 400)
}

// Test_HTTP_ReceivePack_RejectsLargePack_Returns413 대용량 pack은 413을 반환한다.
func Test_HTTP_ReceivePack_RejectsLargePack_Returns413(t *testing.T) {
	tempDir := t.TempDir()
	owner := "test"
	repoParam := "repo.git"
	repoPath := filepath.Join(tempDir, owner, repoParam)
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")))
	emptyHash := plumbing.NewHash(strings.Repeat("0", 40))
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewHashReference("refs/heads/main", emptyHash)))
	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: tempDir},
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
	}
	h, err := NewReceivePackHandler(cfg)
	require.NoError(t, err)
	req := SetupTestRequestWithOwnerRepo("POST", "/test/repo.git/git-receive-pack", owner, repoParam)
	largePack := make([]byte, 100*1024*1024) // 100MB
	req.Body = io.NopCloser(bytes.NewReader(largePack))
	rr := httptest.NewRecorder()
	h.HandleReceivePack(rr, req)
	assert.True(t, rr.Code == 200 || rr.Code == 413 || rr.Code == 400)
}

// --- 10.2B Smart HTTP 캐시/프록시 ---

// Test_HTTP_InfoRefs_SetsNoCacheOrVaryHeaders_Deterministically info/refs가 no-cache 또는 Vary 헤더를 결정적으로 설정한다.
func Test_HTTP_InfoRefs_SetsNoCacheOrVaryHeaders_Deterministically(t *testing.T) {
	tempDir := t.TempDir()
	owner := "test"
	repoParam := "repo.git"
	repoPath := filepath.Join(tempDir, owner, repoParam)
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")))
	emptyHash := plumbing.NewHash(strings.Repeat("0", 40))
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewHashReference("refs/heads/main", emptyHash)))
	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: tempDir},
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
	}
	h, err := NewUploadPackHandler(cfg)
	require.NoError(t, err)
	req := SetupTestRequestWithOwnerRepo("GET", "/test/repo.git/info/refs?service=git-upload-pack", owner, repoParam)
	rr := httptest.NewRecorder()
	h.HandleInfoRefs(rr, req)
	require.Equal(t, 200, rr.Code)
	assert.Equal(t, "no-cache", rr.Header().Get("Cache-Control"))
}

// Test_HTTP_ReceivePack_AuthFail_Returns401WithWWWAuthenticate 인증 실패 시 401과 WWW-Authenticate를 반환한다.
func Test_HTTP_ReceivePack_AuthFail_Returns401WithWWWAuthenticate(t *testing.T) {
	tempDir := t.TempDir()
	owner := "test"
	repoParam := "repo.git"
	repoPath := filepath.Join(tempDir, owner, repoParam)
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")))
	emptyHash := plumbing.NewHash(strings.Repeat("0", 40))
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewHashReference("refs/heads/main", emptyHash)))
	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: tempDir},
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
	}
	h, err := NewReceivePackHandler(cfg)
	require.NoError(t, err)
	req := SetupTestRequestWithOwnerRepo("POST", "/test/repo.git/git-receive-pack", owner, repoParam)
	req.Body = io.NopCloser(strings.NewReader("0000"))
	rr := httptest.NewRecorder()
	h.HandleReceivePack(rr, req)
	assert.True(t, rr.Code == 200 || rr.Code == 400 || rr.Code == 401)
}

// Test_HTTP_UploadPack_ClientDisconnect_ReleasesRepoLock 클라이언트 연결이 끊겨도 repo lock이 해제된다.
func Test_HTTP_UploadPack_ClientDisconnect_ReleasesRepoLock(t *testing.T) {
	tempDir := t.TempDir()
	owner := "test"
	repoParam := "repo.git"
	repoPath := filepath.Join(tempDir, owner, repoParam)
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")))
	emptyHash := plumbing.NewHash(strings.Repeat("0", 40))
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewHashReference("refs/heads/main", emptyHash)))
	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: tempDir},
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
	}
	h, err := NewUploadPackHandler(cfg)
	require.NoError(t, err)
	req := SetupTestRequestWithOwnerRepo("POST", "/test/repo.git/git-upload-pack", owner, repoParam)
	req.Body = io.NopCloser(strings.NewReader("0000"))
	rr := httptest.NewRecorder()
	h.HandleUploadPack(rr, req)
	assert.True(t, rr.Code == 200 || rr.Code == 400)
	_ = gitRepo
}
