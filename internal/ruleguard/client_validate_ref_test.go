package ruleguard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ValidatePushRef is the single rule-factor check shared by the SSH pre-receive
// hook (validateRef) and the HTTP receive-pack handler (validatePushFacts). These
// tests pin the ref→target mapping, the allow/deny/fail-open contract, and the
// "not a ruleset target" / nil-client short-circuits that both callers rely on.

// captureAndReply serves a validate endpoint that records the request body and
// replies with the given status. It reports whether it was ever hit.
func captureAndReply(t *testing.T, status int, into *map[string]any, hit *bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hit = true
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, into)
		w.WriteHeader(status)
	}))
}

func TestValidatePushRef_BranchAllowed(t *testing.T) {
	repo, first := initRepo(t)
	var captured map[string]any
	var hit bool
	srv := captureAndReply(t, http.StatusOK, &captured, &hit)
	defer srv.Close()

	c := NewClient(srv.URL, 5*time.Second)
	denied, err := ValidatePushRef(context.Background(), c,
		PushTarget{RepositoryID: "7", DecisionToken: "tok"},
		repo, zeroOID, first, "refs/heads/main")
	if err != nil || denied != nil {
		t.Fatalf("branch allow: denied=%v err=%v", denied, err)
	}
	if captured["target_type"] != "branch" {
		t.Fatalf("expected target_type=branch, got %v", captured["target_type"])
	}
	ctxMap, _ := captured["context"].(map[string]any)
	if b, _ := ctxMap["branch"].(map[string]any); b == nil || b["name"] != "main" {
		t.Fatalf("expected context.branch.name=main, got %v", ctxMap)
	}
}

func TestValidatePushRef_TagDenied(t *testing.T) {
	repo, first := initRepo(t)
	var captured map[string]any
	var hit bool
	srv := captureAndReply(t, http.StatusUnprocessableEntity, &captured, &hit)
	defer srv.Close()

	c := NewClient(srv.URL, 5*time.Second)
	denied, err := ValidatePushRef(context.Background(), c,
		PushTarget{RepositoryID: "7", DecisionToken: "tok"},
		repo, zeroOID, first, "refs/tags/v1.0.0")
	if err != nil {
		t.Fatalf("tag deny should not surface a transport error: %v", err)
	}
	if denied == nil {
		t.Fatal("expected *Denied for a 422 rule violation, got nil")
	}
	if denied.Branch != "v1.0.0" {
		t.Fatalf("expected Denied.Branch=v1.0.0, got %q", denied.Branch)
	}
	if captured["target_type"] != "tag" {
		t.Fatalf("expected target_type=tag, got %v", captured["target_type"])
	}
}

func TestValidatePushRef_NonRulesetRefSkipsServer(t *testing.T) {
	var captured map[string]any
	var hit bool
	srv := captureAndReply(t, http.StatusOK, &captured, &hit)
	defer srv.Close()

	c := NewClient(srv.URL, 5*time.Second)
	// refs/notes/* is neither a branch nor a tag → allow without calling the
	// orchestrator (and without touching repoPath/BuildPushFacts).
	denied, err := ValidatePushRef(context.Background(), c,
		PushTarget{RepositoryID: "7", DecisionToken: "tok"},
		"", zeroOID, zeroOID, "refs/notes/commits")
	if err != nil || denied != nil {
		t.Fatalf("non-ruleset ref: denied=%v err=%v", denied, err)
	}
	if hit {
		t.Fatal("orchestrator must not be called for a non-branch/tag ref")
	}
}

func TestValidatePushRef_NilClient(t *testing.T) {
	denied, err := ValidatePushRef(context.Background(), nil,
		PushTarget{}, "", zeroOID, zeroOID, "refs/heads/main")
	if err != nil || denied != nil {
		t.Fatalf("nil client must no-op: denied=%v err=%v", denied, err)
	}
}

func TestValidatePushRef_TransportErrorFailsOpen(t *testing.T) {
	repo, first := initRepo(t)
	var captured map[string]any
	var hit bool
	// 500 is neither allow (2xx) nor a rule violation (403/409/422) → surfaced as
	// an error so both callers fail-open and log.
	srv := captureAndReply(t, http.StatusInternalServerError, &captured, &hit)
	defer srv.Close()

	c := NewClient(srv.URL, 5*time.Second)
	denied, err := ValidatePushRef(context.Background(), c,
		PushTarget{RepositoryID: "7", DecisionToken: "tok"},
		repo, zeroOID, first, "refs/heads/main")
	if denied != nil {
		t.Fatalf("transport error must not produce a Denied, got %v", denied)
	}
	if err == nil {
		t.Fatal("expected a transport error for HTTP 500")
	}
}
