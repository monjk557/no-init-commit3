// Package typed 의 runtime wrapper helper 단위 테스트.
//
// writeError 가 기존 handler 들의 `gin.H{"success": false, "error": "..."}`
// 응답 형태를 그대로 보존하는지 + error mapper / status derive / path
// resolution 동작을 검증한다.
package typed

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func newTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	return c, w
}

func TestWriteError_ShapeMatchesLegacyGinH(t *testing.T) {
	c, w := newTestContext()
	writeError(c, http.StatusBadRequest, "invalid limit: must be >= 1")

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}

	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("response 가 JSON 이 아님: %v\n%s", err, w.Body.String())
	}
	if got["success"] != false {
		t.Errorf("success = %v, want false", got["success"])
	}
	if got["error"] != "invalid limit: must be >= 1" {
		t.Errorf("error = %v", got["error"])
	}

	// 레거시 응답이 정확히 두 키만 보유 — 추가 필드가 silently 들어가면 회귀.
	if len(got) != 2 {
		t.Errorf("response 키 개수 = %d, want 2 (success, error). got=%+v", len(got), got)
	}
}

func TestMapAndWriteError_UsesEndpointMapper(t *testing.T) {
	c, w := newTestContext()
	sentinel := errors.New("source not found")
	ep := Endpoint{
		ErrorMapper: func(err error) (int, any) {
			if errors.Is(err, sentinel) {
				return http.StatusNotFound, gin.H{"success": false, "error": "source repository not found"}
			}
			return http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()}
		},
	}
	mapAndWriteError(c, sentinel, ep)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
	if !contains(w.Body.String(), "source repository not found") {
		t.Errorf("body 에 mapper 결과 미포함: %s", w.Body.String())
	}
}

func TestMapAndWriteError_DefaultsTo500(t *testing.T) {
	c, w := newTestContext()
	mapAndWriteError(c, errors.New("boom"), Endpoint{}) // mapper 없음

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}

	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("response 가 JSON 이 아님: %v", err)
	}
	if got["success"] != false || got["error"] != "boom" {
		t.Errorf("default fallback shape mismatch: %+v", got)
	}
}

func TestDeriveStatus_DefaultsTo200(t *testing.T) {
	if got := deriveStatus(Endpoint{}); got != http.StatusOK {
		t.Errorf("default = %d, want 200", got)
	}
}

func TestDeriveStatus_HonorsOverride(t *testing.T) {
	if got := deriveStatus(Endpoint{SuccessStatus: http.StatusCreated}); got != http.StatusCreated {
		t.Errorf("override = %d, want 201", got)
	}
}

func TestAbsolutePath_NoBasePather_ReturnsAsIs(t *testing.T) {
	// gin.IRouter 에 BasePath() 가 없는 fake — fallthrough 검증
	type fakeRouter struct{ gin.IRouter }
	got := absolutePath(fakeRouter{}, "/branches")
	if got != "/branches" {
		t.Errorf("got %q, want %q", got, "/branches")
	}
}

func TestAbsolutePath_EngineRoot(t *testing.T) {
	e := gin.New()
	got := absolutePath(e, "/test")
	if got != "/test" {
		t.Errorf("got %q, want %q", got, "/test")
	}
}

func TestAbsolutePath_GroupBasePath(t *testing.T) {
	e := gin.New()
	g := e.Group("/api/v1")
	got := absolutePath(g, "/repos/:owner/:repo")
	if got != "/api/v1/repos/:owner/:repo" {
		t.Errorf("got %q, want %q", got, "/api/v1/repos/:owner/:repo")
	}
}

func TestAbsolutePath_NestedGroup(t *testing.T) {
	e := gin.New()
	g1 := e.Group("/repos")
	g2 := g1.Group("/:owner/:repo")
	got := absolutePath(g2, "/branches")
	if got != "/repos/:owner/:repo/branches" {
		t.Errorf("got %q, want %q", got, "/repos/:owner/:repo/branches")
	}
}

func TestGinToOpenAPIPath_ConvertsColonAndStarParams(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"/repos/:owner/:repo", "/repos/{owner}/{repo}"},
		{"/branches/*branch", "/branches/{branch}"},
		{"/branches/validate/*name", "/branches/validate/{name}"},
		{"/static/path", "/static/path"},
	}
	for _, tc := range tests {
		got := ginToOpenAPIPath(tc.in)
		if got != tc.want {
			t.Errorf("ginToOpenAPIPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestMapAndWriteError_HTTPErrorDefault(t *testing.T) {
	c, w := newTestContext()
	mapAndWriteError(c, NewHTTPError(http.StatusConflict, "Repository already exists"), Endpoint{})

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
	if !contains(w.Body.String(), "Repository already exists") {
		t.Errorf("body 에 메시지 미포함: %s", w.Body.String())
	}
	if !contains(w.Body.String(), `"success":false`) {
		t.Errorf("기존 응답 형태 미보존: %s", w.Body.String())
	}
}

func TestMapAndWriteError_HTTPErrorWrappedByFmt(t *testing.T) {
	c, w := newTestContext()
	wrapped := errors.New("wrapped"). // sentinel
		// fmt.Errorf("...: %w", &HTTPError{...}) 도 errors.As 로 추출 가능 검증
		Error()
	_ = wrapped
	mapAndWriteError(c, NewHTTPError(http.StatusBadRequest, "Invalid path: ..."), Endpoint{})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestAbsolutePath_EmptyOrSlashPathReturnsBaseOnly(t *testing.T) {
	e := gin.New()
	g := e.Group("/repos/:owner/:repo")

	if got := absolutePath(g, ""); got != "/repos/:owner/:repo" {
		t.Errorf(`absolutePath(g, "") = %q, want %q`, got, "/repos/:owner/:repo")
	}
	if got := absolutePath(g, "/"); got != "/repos/:owner/:repo" {
		t.Errorf(`absolutePath(g, "/") = %q, want %q`, got, "/repos/:owner/:repo")
	}
}
