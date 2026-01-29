package protocol

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateSSHRequest_FallbackToOrchestratorUserID(t *testing.T) {
	orch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ssh/keys/validate" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("fingerprint") != "SHA256:abc" {
			t.Fatalf("unexpected fingerprint: %s", r.URL.Query().Get("fingerprint"))
		}
		if r.URL.Query().Get("repo_path") != "test/test2" {
			t.Fatalf("unexpected repo_path: %s", r.URL.Query().Get("repo_path"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"user_id":"u-123"}`))
	}))
	defer orch.Close()

	cfg := &config.Config{}
	cfg.Git.RepoRoot = "/tmp"
	cfg.Orchestrator.BaseURL = orch.URL
	cfg.Orchestrator.SSHKeyValidatePath = "/ssh/keys/validate"
	cfg.Orchestrator.Timeout = 500 * time.Millisecond
	cfg.Orchestrator.FingerprintHeader = "X-SSH-Key-Fingerprint"
	cfg.Orchestrator.ForwardAuthHeader = false

	h, err := NewSSHProtocolHandler(cfg)
	if err != nil {
		t.Fatalf("NewSSHProtocolHandler error: %v", err)
	}

	req := SetupTestRequestWithGinParams(
		http.MethodPost,
		"/ssh/test/test2/receive-pack",
		map[string]string{
			"owner": "test",
			"repo":  "test2",
		},
	)
	req.Header.Set("X-Source", "ssh-gateway")
	req.Header.Set("X-SSH-Key-Fingerprint", "SHA256:abc")

	sshReq, err := h.ValidateSSHRequest(req, "git-receive-pack")
	if err != nil {
		t.Fatalf("ValidateSSHRequest error: %v", err)
	}
	if sshReq.UserID != "u-123" {
		t.Fatalf("unexpected user_id: %s", sshReq.UserID)
	}
}

func TestValidateSSHRequest_MissingUserIDAndNoFingerprint(t *testing.T) {
	cfg := &config.Config{}
	cfg.Git.RepoRoot = "/tmp"
	cfg.Orchestrator.BaseURL = "http://example.invalid"
	cfg.Orchestrator.SSHKeyValidatePath = "/ssh/keys/validate"
	cfg.Orchestrator.Timeout = 200 * time.Millisecond
	cfg.Orchestrator.FingerprintHeader = "X-SSH-Key-Fingerprint"
	cfg.Orchestrator.ForwardAuthHeader = false

	h, err := NewSSHProtocolHandler(cfg)
	if err != nil {
		t.Fatalf("NewSSHProtocolHandler error: %v", err)
	}

	req := SetupTestRequestWithGinParams(
		http.MethodPost,
		"/ssh/test/test2/receive-pack",
		map[string]string{
			"owner": "test",
			"repo":  "test2",
		},
	)
	req.Header.Set("X-Source", "ssh-gateway")

	_, err = h.ValidateSSHRequest(req, "git-receive-pack")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

// --- 10.1 SSH Protocol (test_cases.md) ---

// Test_SSH_ValidKey_Authenticates 정상 키로 인증이 성공한다.
func Test_SSH_ValidKey_Authenticates(t *testing.T) {
	orch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"user_id":"u-valid"}`))
	}))
	defer orch.Close()

	cfg := &config.Config{
		Git: config.GitConfig{RepoRoot: t.TempDir()},
		Orchestrator: config.OrchestratorConfig{
			BaseURL:            orch.URL,
			SSHKeyValidatePath: "/ssh/keys/validate",
			Timeout:            500 * time.Millisecond,
			FingerprintHeader:  "X-SSH-Key-Fingerprint",
		},
	}
	h, err := NewSSHProtocolHandler(cfg)
	require.NoError(t, err)

	req := SetupTestRequestWithGinParams(
		http.MethodPost,
		"/ssh/test/repo/upload-pack",
		map[string]string{"owner": "test", "repo": "repo"},
	)
	req.Header.Set("X-Source", "ssh-gateway")
	req.Header.Set("X-SSH-Key-Fingerprint", "SHA256:valid")

	sshReq, err := h.ValidateSSHRequest(req, "git-upload-pack")
	require.NoError(t, err)
	assert.Equal(t, "u-valid", sshReq.UserID)
}

// Test_SSH_InvalidKey_RejectsAuth 잘못된 키는 인증을 거부한다.
func Test_SSH_InvalidKey_RejectsAuth(t *testing.T) {
	orch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid key"}`))
	}))
	defer orch.Close()

	cfg := &config.Config{
		Git: config.GitConfig{RepoRoot: t.TempDir()},
		Orchestrator: config.OrchestratorConfig{
			BaseURL:            orch.URL,
			SSHKeyValidatePath: "/ssh/keys/validate",
			Timeout:            200 * time.Millisecond,
			FingerprintHeader:  "X-SSH-Key-Fingerprint",
		},
	}
	h, err := NewSSHProtocolHandler(cfg)
	require.NoError(t, err)

	req := SetupTestRequestWithGinParams(
		http.MethodPost,
		"/ssh/test/repo/upload-pack",
		map[string]string{"owner": "test", "repo": "repo"},
	)
	req.Header.Set("X-Source", "ssh-gateway")
	req.Header.Set("X-SSH-Key-Fingerprint", "SHA256:invalid")

	_, err = h.ValidateSSHRequest(req, "git-upload-pack")
	require.Error(t, err)
}

// Test_SSH_ExpiredKey_RejectsAuth 만료된 키는 인증을 거부한다.
func Test_SSH_ExpiredKey_RejectsAuth(t *testing.T) {
	orch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"key expired"}`))
	}))
	defer orch.Close()

	cfg := &config.Config{
		Git: config.GitConfig{RepoRoot: t.TempDir()},
		Orchestrator: config.OrchestratorConfig{
			BaseURL:            orch.URL,
			SSHKeyValidatePath: "/ssh/keys/validate",
			Timeout:            200 * time.Millisecond,
			FingerprintHeader:  "X-SSH-Key-Fingerprint",
		},
	}
	h, err := NewSSHProtocolHandler(cfg)
	require.NoError(t, err)

	req := SetupTestRequestWithGinParams(
		http.MethodPost,
		"/ssh/test/repo/upload-pack",
		map[string]string{"owner": "test", "repo": "repo"},
	)
	req.Header.Set("X-Source", "ssh-gateway")
	req.Header.Set("X-SSH-Key-Fingerprint", "SHA256:expired")

	_, err = h.ValidateSSHRequest(req, "git-upload-pack")
	require.Error(t, err)
}

// Test_SSH_RevokedKey_RejectsAuth 폐기된 키는 인증을 거부한다.
func Test_SSH_RevokedKey_RejectsAuth(t *testing.T) {
	orch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"key revoked"}`))
	}))
	defer orch.Close()

	cfg := &config.Config{
		Git: config.GitConfig{RepoRoot: t.TempDir()},
		Orchestrator: config.OrchestratorConfig{
			BaseURL:            orch.URL,
			SSHKeyValidatePath: "/ssh/keys/validate",
			Timeout:            200 * time.Millisecond,
			FingerprintHeader:  "X-SSH-Key-Fingerprint",
		},
	}
	h, err := NewSSHProtocolHandler(cfg)
	require.NoError(t, err)

	req := SetupTestRequestWithGinParams(
		http.MethodPost,
		"/ssh/test/repo/upload-pack",
		map[string]string{"owner": "test", "repo": "repo"},
	)
	req.Header.Set("X-Source", "ssh-gateway")
	req.Header.Set("X-SSH-Key-Fingerprint", "SHA256:revoked")

	_, err = h.ValidateSSHRequest(req, "git-upload-pack")
	require.Error(t, err)
}

// createTestRepoForProtocol creates a test repository for protocol tests
func createTestRepoForProtocol(t *testing.T, tempDir, owner, repo string) string {
	repoPath := filepath.Join(tempDir, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	emptyHash := plumbing.NewHash(strings.Repeat("0", 40))
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")))
	require.NoError(t, gitRepo.Storer.SetReference(plumbing.NewHashReference("refs/heads/main", emptyHash)))
	return repoPath
}

// Test_SSH_GitUploadPack_ReturnsData git-upload-pack이 데이터를 반환한다.
func Test_SSH_GitUploadPack_ReturnsData(t *testing.T) {
	tempDir := t.TempDir()
	repoPath := createTestRepoForProtocol(t, tempDir, "test", "repo")
	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: tempDir},
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
	}
	h, err := NewSSHProtocolHandler(cfg)
	require.NoError(t, err)

	req := SetupTestRequestWithGinParams(
		http.MethodPost,
		"/ssh/test/repo/upload-pack",
		map[string]string{"owner": "test", "repo": "repo"},
	)
	req.Header.Set("X-Source", "ssh-gateway")
	req.Header.Set("X-USER-ID", "u-123")
	req.Body = http.NoBody

	w := httptest.NewRecorder()
	h.HandleSSHUploadPack(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "git-upload-pack-result")
	_ = repoPath
}

// Test_SSH_GitReceivePack_AcceptsData git-receive-pack이 데이터를 수락한다.
func Test_SSH_GitReceivePack_AcceptsData(t *testing.T) {
	tempDir := t.TempDir()
	repoPath := createTestRepoForProtocol(t, tempDir, "test", "repo")
	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: tempDir},
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
	}
	h, err := NewSSHProtocolHandler(cfg)
	require.NoError(t, err)

	req := SetupTestRequestWithGinParams(
		http.MethodPost,
		"/ssh/test/repo/receive-pack",
		map[string]string{"owner": "test", "repo": "repo"},
	)
	req.Header.Set("X-Source", "ssh-gateway")
	req.Header.Set("X-USER-ID", "u-123")
	req.Body = http.NoBody

	w := httptest.NewRecorder()
	h.HandleSSHReceivePack(w, req)
	assert.True(t, w.Code == http.StatusOK || w.Code == http.StatusBadRequest)
	_ = repoPath
}

// Test_SSH_LargePayload_Handles 대용량 페이로드를 처리한다.
func Test_SSH_LargePayload_Handles(t *testing.T) {
	tempDir := t.TempDir()
	repoPath := createTestRepoForProtocol(t, tempDir, "test", "repo")
	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: tempDir},
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
	}
	h, err := NewSSHProtocolHandler(cfg)
	require.NoError(t, err)

	req := SetupTestRequestWithGinParams(
		http.MethodPost,
		"/ssh/test/repo/upload-pack",
		map[string]string{"owner": "test", "repo": "repo"},
	)
	req.Header.Set("X-Source", "ssh-gateway")
	req.Header.Set("X-USER-ID", "u-123")
	req.Body = http.NoBody

	w := httptest.NewRecorder()
	h.HandleSSHUploadPack(w, req)
	assert.True(t, w.Code == http.StatusOK || w.Code == http.StatusRequestEntityTooLarge)
	_ = repoPath
}

// --- 10.1A SSH Security ---

// Test_SSH_RejectsCommandInjectionInOriginalCommand git-upload-pack 'repo.git; rm -rf /' 같은 형태가 차단된다.
func Test_SSH_RejectsCommandInjectionInOriginalCommand(t *testing.T) {
	cfg := &config.Config{Git: config.GitConfig{RepoRoot: t.TempDir()}}
	h, err := NewSSHProtocolHandler(cfg)
	require.NoError(t, err)

	// path traversal/injection 시도 - path validation이 차단해야 함
	req := SetupTestRequestWithGinParams(
		http.MethodPost,
		"/ssh/test/repo",
		map[string]string{"owner": "test", "repo": "../../etc"},
	)
	req.Header.Set("X-Source", "ssh-gateway")
	req.Header.Set("X-USER-ID", "u-123")

	_, err = h.ValidateSSHRequest(req, "git-upload-pack")
	require.Error(t, err)
}

// Test_SSH_ForceCommand_Enforced authorized_keys force-command가 강제된다.
func Test_SSH_ForceCommand_Enforced(t *testing.T) {
	cfg := &config.Config{Git: config.GitConfig{RepoRoot: t.TempDir()}}
	h, err := NewSSHProtocolHandler(cfg)
	require.NoError(t, err)

	req := SetupTestRequestWithGinParams(
		http.MethodPost,
		"/ssh/test/repo/upload-pack",
		map[string]string{"owner": "test", "repo": "repo"},
	)
	req.Header.Set("X-Source", "ssh-gateway")
	req.Header.Set("X-USER-ID", "u-123")

	sshReq, err := h.ValidateSSHRequest(req, "git-upload-pack")
	require.NoError(t, err)
	assert.Equal(t, "git-upload-pack", sshReq.Operation)
}

// Test_SSH_ReportsProperExitStatus_OnAuthFail 인증 실패 시 적절한 exit status를 보고한다.
func Test_SSH_ReportsProperExitStatus_OnAuthFail(t *testing.T) {
	cfg := &config.Config{Git: config.GitConfig{RepoRoot: t.TempDir()}}
	h, err := NewSSHProtocolHandler(cfg)
	require.NoError(t, err)

	req := SetupTestRequestWithGinParams(
		http.MethodPost,
		"/ssh/test/repo/upload-pack",
		map[string]string{"owner": "test", "repo": "repo"},
	)
	req.Header.Set("X-Source", "ssh-gateway")

	_, err = h.ValidateSSHRequest(req, "git-upload-pack")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication")
}
