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

// captureServer returns an httptest server that unmarshals the request body
// into the given map and responds 200 (allowed).
func captureServer(t *testing.T, into *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, into)
		w.WriteHeader(http.StatusOK)
	}))
}

func contextActor(captured map[string]any) map[string]any {
	ctxMap, _ := captured["context"].(map[string]any)
	actor, _ := ctxMap["actor"].(map[string]any)
	return actor
}

// RulesetBypass 매칭이 동작하려면 validate context.actor 가 push 주체를 담아야 한다.
func TestValidatePush_InjectsActorID(t *testing.T) {
	var captured map[string]any
	srv := captureServer(t, &captured)
	defer srv.Close()

	c := NewClient(srv.URL, 5*time.Second)
	if err := c.ValidatePush(context.Background(), PushTarget{
		RepositoryID:  "7",
		DecisionToken: "tok",
		UserID:        "user-uuid-1",
		Branch:        "main",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	actor := contextActor(captured)
	if actor == nil || actor["id"] != "user-uuid-1" {
		t.Fatalf("expected actor.id=user-uuid-1, got context=%v", captured["context"])
	}
}

func TestValidatePush_NoActorWhenNoUserID(t *testing.T) {
	var captured map[string]any
	srv := captureServer(t, &captured)
	defer srv.Close()

	c := NewClient(srv.URL, 5*time.Second)
	_ = c.ValidatePush(context.Background(), PushTarget{
		RepositoryID:  "7",
		DecisionToken: "tok",
		Branch:        "main",
	})

	if actor := contextActor(captured); actor != nil {
		t.Fatalf("expected no actor when UserID empty, got %v", actor)
	}
}
