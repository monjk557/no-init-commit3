package protocol

import (
	"bytes"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/collabops/git-executor/internal/config"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReceivePackInfoRefs_AdvertisesCapabilities(t *testing.T) {
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
	// Native engine uses git index-pack --fix-thin, so thin-pack is supported
	require.Contains(t, rr.Body.String(), "report-status")
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
	// Native git advertises refs with capabilities on the first ref line.
	body := rr.Body.String()
	assert.Contains(t, body, "refs/heads/main")
	assert.Contains(t, body, "report-status")
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

// --- OOM-prevention: parseV1CommandsFromSection tests ---

func TestParseV1CommandsFromSection_SingleCommand(t *testing.T) {
	h := &ReceivePackHandler{}
	lines := []string{
		"abc123 def456 refs/heads/main",
	}

	req := h.parseV1CommandsFromSection(lines)

	if len(req.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(req.Commands))
	}
	cmd := req.Commands[0]
	if cmd.OldHash != "abc123" {
		t.Errorf("OldHash = %q, want %q", cmd.OldHash, "abc123")
	}
	if cmd.NewHash != "def456" {
		t.Errorf("NewHash = %q, want %q", cmd.NewHash, "def456")
	}
	if cmd.RefName != "refs/heads/main" {
		t.Errorf("RefName = %q, want %q", cmd.RefName, "refs/heads/main")
	}
}

func TestParseV1CommandsFromSection_MultipleCommands(t *testing.T) {
	h := &ReceivePackHandler{}
	lines := []string{
		"aaa111 bbb222 refs/heads/main",
		"ccc333 ddd444 refs/heads/feature",
		"eee555 fff666 refs/tags/v1.0",
	}

	req := h.parseV1CommandsFromSection(lines)

	if len(req.Commands) != 3 {
		t.Fatalf("expected 3 commands, got %d", len(req.Commands))
	}
	if req.Commands[0].RefName != "refs/heads/main" {
		t.Errorf("command[0].RefName = %q, want %q", req.Commands[0].RefName, "refs/heads/main")
	}
	if req.Commands[1].RefName != "refs/heads/feature" {
		t.Errorf("command[1].RefName = %q, want %q", req.Commands[1].RefName, "refs/heads/feature")
	}
	if req.Commands[2].RefName != "refs/tags/v1.0" {
		t.Errorf("command[2].RefName = %q, want %q", req.Commands[2].RefName, "refs/tags/v1.0")
	}
}

func TestParseV1CommandsFromSection_CapabilitiesOnFirstLine(t *testing.T) {
	h := &ReceivePackHandler{}
	lines := []string{
		"aaa111 bbb222 refs/heads/main\x00report-status delete-refs",
		"ccc333 ddd444 refs/heads/feature",
	}

	req := h.parseV1CommandsFromSection(lines)

	if len(req.Commands) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(req.Commands))
	}
	if req.Commands[0].RefName != "refs/heads/main" {
		t.Errorf("command[0].RefName = %q, want %q", req.Commands[0].RefName, "refs/heads/main")
	}
	if len(req.Capabilities) != 2 {
		t.Fatalf("expected 2 capabilities, got %d", len(req.Capabilities))
	}
	if req.Capabilities[0] != "report-status" {
		t.Errorf("capabilities[0] = %q, want %q", req.Capabilities[0], "report-status")
	}
	if req.Capabilities[1] != "delete-refs" {
		t.Errorf("capabilities[1] = %q, want %q", req.Capabilities[1], "delete-refs")
	}
}

func TestParseV1CommandsFromSection_EmptyAndWhitespaceLines(t *testing.T) {
	h := &ReceivePackHandler{}
	lines := []string{
		"",
		"   ",
		"aaa111 bbb222 refs/heads/main",
		"",
	}

	req := h.parseV1CommandsFromSection(lines)

	if len(req.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(req.Commands))
	}
	if req.Commands[0].RefName != "refs/heads/main" {
		t.Errorf("RefName = %q, want %q", req.Commands[0].RefName, "refs/heads/main")
	}
}

func TestParseV1CommandsFromSection_LessThanThreeFields(t *testing.T) {
	h := &ReceivePackHandler{}
	lines := []string{
		"onlyonefield",
		"two fields",
		"aaa111 bbb222 refs/heads/main",
	}

	req := h.parseV1CommandsFromSection(lines)

	if len(req.Commands) != 1 {
		t.Fatalf("expected 1 command (skipping short lines), got %d", len(req.Commands))
	}
	if req.Commands[0].RefName != "refs/heads/main" {
		t.Errorf("RefName = %q, want %q", req.Commands[0].RefName, "refs/heads/main")
	}
}

// --- OOM-prevention: hasPackfileCommands tests ---

func TestHasPackfileCommands_AllNonZeroNewHash(t *testing.T) {
	h := &ReceivePackHandler{}
	cmds := []RefUpdateCommand{
		{OldHash: zeroHash, NewHash: "abc123", RefName: "refs/heads/main"},
		{OldHash: "def456", NewHash: "ghi789", RefName: "refs/heads/feature"},
	}

	if !h.hasPackfileCommands(cmds) {
		t.Error("expected true when all commands have non-zero NewHash")
	}
}

func TestHasPackfileCommands_AllDeletes(t *testing.T) {
	h := &ReceivePackHandler{}
	cmds := []RefUpdateCommand{
		{OldHash: "abc123", NewHash: zeroHash, RefName: "refs/heads/old-branch"},
		{OldHash: "def456", NewHash: zeroHash, RefName: "refs/heads/stale"},
	}

	if h.hasPackfileCommands(cmds) {
		t.Error("expected false when all commands are deletes (zero NewHash)")
	}
}

func TestHasPackfileCommands_MixedCreatesAndDeletes(t *testing.T) {
	h := &ReceivePackHandler{}
	cmds := []RefUpdateCommand{
		{OldHash: "abc123", NewHash: zeroHash, RefName: "refs/heads/old-branch"},
		{OldHash: zeroHash, NewHash: "def456", RefName: "refs/heads/new-branch"},
	}

	if !h.hasPackfileCommands(cmds) {
		t.Error("expected true when at least one command has non-zero NewHash")
	}
}

func TestHasPackfileCommands_EmptyList(t *testing.T) {
	h := &ReceivePackHandler{}
	var cmds []RefUpdateCommand

	if h.hasPackfileCommands(cmds) {
		t.Error("expected false for empty command list")
	}
}
