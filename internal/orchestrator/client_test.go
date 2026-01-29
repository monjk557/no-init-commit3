package orchestrator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestResolveUserIDFromSSHKey_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	defer srv.Close()

	c, err := NewClient(srv.URL, "/ssh/keys/validate", 500*time.Millisecond, false)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}

	userID, err := c.ResolveUserIDFromSSHKey(context.Background(), "SHA256:abc", "test/test2", "")
	if err != nil {
		t.Fatalf("ResolveUserIDFromSSHKey error: %v", err)
	}
	if userID != "u-123" {
		t.Fatalf("unexpected user_id: %s", userID)
	}
}

func TestResolveUserIDFromSSHKey_MissingUserID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"user_id":""}`))
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "/ssh/keys/validate", 500*time.Millisecond, false)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}

	_, err = c.ResolveUserIDFromSSHKey(context.Background(), "SHA256:abc", "test/test2", "")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}
