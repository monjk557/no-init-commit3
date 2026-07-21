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

// tag push 는 tag-target ruleset 으로 검증돼야 한다 — body.target_type="tag" +
// context.tag.name 이 실려야 tag_name_pattern / restrict_* 규칙이 평가된다.
func TestValidatePush_TagTarget(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &captured)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 5*time.Second)
	if err := c.ValidatePush(context.Background(), PushTarget{
		RepositoryID:  "7",
		DecisionToken: "tok",
		TargetType:    "tag",
		Branch:        "v1.0.0",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if captured["target_type"] != "tag" {
		t.Fatalf("expected target_type=tag, got %v", captured["target_type"])
	}
	ctxMap, _ := captured["context"].(map[string]any)
	tag, _ := ctxMap["tag"].(map[string]any)
	if tag == nil || tag["name"] != "v1.0.0" {
		t.Fatalf("expected context.tag.name=v1.0.0, got context=%v", ctxMap)
	}
	if _, hasBranch := ctxMap["branch"]; hasBranch {
		t.Fatalf("tag push should not stamp branch.*, got %v", ctxMap)
	}
}

// 기본(TargetType 미지정)은 branch 로 동작한다 (기존 호출자 무회귀).
func TestValidatePush_DefaultsToBranch(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &captured)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 5*time.Second)
	_ = c.ValidatePush(context.Background(), PushTarget{
		RepositoryID:  "7",
		DecisionToken: "tok",
		Branch:        "main",
	})

	if captured["target_type"] != "branch" {
		t.Fatalf("expected default target_type=branch, got %v", captured["target_type"])
	}
	ctxMap, _ := captured["context"].(map[string]any)
	if branch, _ := ctxMap["branch"].(map[string]any); branch == nil || branch["name"] != "main" {
		t.Fatalf("expected context.branch.name=main, got %v", ctxMap)
	}
}
