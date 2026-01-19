package protocol

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
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

