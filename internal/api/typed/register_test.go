// Package typed 의 Register* generic 함수 단위 테스트.
//
// 각 Register* 가 (1) registry 에 정확한 Endpoint 를 capture 하고 (2) gin
// 라우터 동작이 기존 handler 들의 외부 동작과 일치하는지 검증한다.
package typed

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// resetDefaultRegistryFor 는 테스트 동안 DefaultRegistry 를 임시 빈
// Registry 로 교체하고 t.Cleanup 으로 원복한다. 테스트 격리용 helper.
func resetDefaultRegistryFor(t *testing.T) *Registry {
	t.Helper()
	orig := defaultRegistry
	fresh := NewRegistry()
	defaultRegistry = fresh
	t.Cleanup(func() { defaultRegistry = orig })
	return fresh
}

type sampleReq struct {
	Limit int `json:"limit"`
}

type sampleResp struct {
	OK bool `json:"ok"`
}

// ─── RegisterJSON ──────────────────────────────────────────────────────────

func TestRegisterJSON_CapturesRegistryAndRoutes(t *testing.T) {
	reg := resetDefaultRegistryFor(t)

	e := gin.New()
	g := e.Group("/api")
	RegisterJSON(g, http.MethodPost, "/test", "Test",
		func(_ *gin.Context, req sampleReq) (sampleResp, error) {
			return sampleResp{OK: req.Limit > 0}, nil
		},
	)

	// Registry capture
	snap := reg.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snap len = %d, want 1", len(snap))
	}
	ep := snap[0]
	if ep.Method != "POST" || ep.Path != "/api/test" || ep.Tag != "Test" {
		t.Errorf("registry mismatch: %+v", ep)
	}
	if ep.ReqType != reflect.TypeOf(sampleReq{}) {
		t.Errorf("ReqType = %v", ep.ReqType)
	}
	if len(ep.Responses) != 1 || ep.Responses[0].Status != 200 ||
		ep.Responses[0].RespType != reflect.TypeOf(sampleResp{}) {
		t.Errorf("Responses[0] = %+v", ep.Responses[0])
	}
	if ep.ReqLocation != LocationBodyJSON {
		t.Errorf("ReqLocation = %v", ep.ReqLocation)
	}

	// 실제 라우트 동작
	req := httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(`{"limit": 5}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got sampleResp
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("response parse: %v", err)
	}
	if !got.OK {
		t.Errorf("expected ok:true, got %+v", got)
	}
}

func TestRegisterJSON_BindFailureReturns400(t *testing.T) {
	resetDefaultRegistryFor(t)
	e := gin.New()
	RegisterJSON(e, http.MethodPost, "/x", "Test",
		func(*gin.Context, sampleReq) (sampleResp, error) {
			return sampleResp{}, nil
		},
	)

	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{not json`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["success"] != false || got["error"] == nil {
		t.Errorf("body shape mismatch: %+v", got)
	}
}

func TestRegisterJSON_HandlerErrorRoutedThroughMapper(t *testing.T) {
	resetDefaultRegistryFor(t)
	e := gin.New()
	sentinel := errors.New("conflict")
	RegisterJSON(e, http.MethodPost, "/x", "Test",
		func(*gin.Context, sampleReq) (sampleResp, error) {
			return sampleResp{}, sentinel
		},
		WithErrorMapper(func(err error) (int, any) {
			if errors.Is(err, sentinel) {
				return http.StatusConflict, gin.H{"success": false, "error": "conflict"}
			}
			return http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()}
		}),
		WithResponse(http.StatusConflict, nil, "Conflict"),
	)

	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
}

func TestRegisterJSON_WithStatusOverride(t *testing.T) {
	resetDefaultRegistryFor(t)
	e := gin.New()
	RegisterJSON(e, http.MethodPost, "/x", "Test",
		func(*gin.Context, sampleReq) (sampleResp, error) {
			return sampleResp{OK: true}, nil
		},
		WithStatus(http.StatusCreated),
	)

	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"limit":1}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", w.Code)
	}
}

// receiver method 가 method value 형태로 등록 가능한지 검증.
type sampleService struct{ multiplier int }

func (s *sampleService) HandleEcho(_ *gin.Context, req sampleReq) (sampleResp, error) {
	return sampleResp{OK: req.Limit*s.multiplier > 0}, nil
}

func TestRegisterJSON_AcceptsReceiverMethodValue(t *testing.T) {
	resetDefaultRegistryFor(t)
	e := gin.New()
	svc := &sampleService{multiplier: 2}
	RegisterJSON(e, http.MethodPost, "/echo", "Echo", svc.HandleEcho)

	req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(`{"limit":3}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got sampleResp
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if !got.OK {
		t.Errorf("receiver method value 호출 실패: %+v", got)
	}
}

// ─── RegisterGet ───────────────────────────────────────────────────────────

func TestRegisterGet_NoBody_ReturnsResp(t *testing.T) {
	reg := resetDefaultRegistryFor(t)
	e := gin.New()
	RegisterGet(e, "/state", "State",
		func(*gin.Context) (sampleResp, error) {
			return sampleResp{OK: true}, nil
		},
	)

	snap := reg.Snapshot()
	if len(snap) != 1 || snap[0].Method != "GET" || snap[0].Path != "/state" {
		t.Errorf("registry mismatch: %+v", snap)
	}
	if snap[0].ReqType != nil {
		t.Errorf("GET 의 ReqType 은 nil 이어야 함, got %v", snap[0].ReqType)
	}
	if snap[0].ReqLocation != LocationNone {
		t.Errorf("ReqLocation = %v, want LocationNone", snap[0].ReqLocation)
	}

	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/state", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
}

// ─── RegisterGetWithQuery ──────────────────────────────────────────────────

type sampleQuery struct {
	Name string `form:"name"`
}

func TestRegisterGetWithQuery_BindsQueryStruct(t *testing.T) {
	reg := resetDefaultRegistryFor(t)
	e := gin.New()
	RegisterGetWithQuery(e, "/search", "Search",
		func(_ *gin.Context, q sampleQuery) (sampleResp, error) {
			return sampleResp{OK: q.Name == "alice"}, nil
		},
	)

	snap := reg.Snapshot()
	if snap[0].ReqType != reflect.TypeOf(sampleQuery{}) {
		t.Errorf("ReqType = %v", snap[0].ReqType)
	}
	if snap[0].ReqLocation != LocationQuery {
		t.Errorf("ReqLocation = %v, want LocationQuery", snap[0].ReqLocation)
	}

	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/search?name=alice", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var got sampleResp
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if !got.OK {
		t.Errorf("query bind 실패: %+v", got)
	}
}

// ─── RegisterNoBody ────────────────────────────────────────────────────────

func TestRegisterNoBody_DELETE(t *testing.T) {
	reg := resetDefaultRegistryFor(t)
	e := gin.New()
	called := false
	RegisterNoBody(e, http.MethodDelete, "/items/:id", "Items",
		func(*gin.Context) (sampleResp, error) {
			called = true
			return sampleResp{OK: true}, nil
		},
	)

	snap := reg.Snapshot()
	if snap[0].Method != "DELETE" || snap[0].Path != "/items/{id}" {
		t.Errorf("path/method capture mismatch: %+v", snap[0])
	}
	if snap[0].ReqType != nil || snap[0].ReqLocation != LocationNone {
		t.Errorf("DELETE 의 Req 메타데이터 mismatch: %+v", snap[0])
	}

	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/items/42", nil))
	if w.Code != http.StatusOK || !called {
		t.Errorf("status=%d, called=%v", w.Code, called)
	}
}

// ─── RegisterRaw ───────────────────────────────────────────────────────────

func TestRegisterRaw_CapturesContentTypeAndPassesThrough(t *testing.T) {
	reg := resetDefaultRegistryFor(t)
	e := gin.New()

	RegisterRaw(e, http.MethodGet, "/blob/raw", "Blob",
		func(c *gin.Context) {
			c.Data(http.StatusOK, "application/octet-stream", []byte{0x42, 0x43})
		},
		"application/octet-stream",
	)

	snap := reg.Snapshot()
	if snap[0].ReqLocation != LocationRawBytes {
		t.Errorf("ReqLocation = %v, want LocationRawBytes", snap[0].ReqLocation)
	}
	if snap[0].ContentType != "application/octet-stream" {
		t.Errorf("ContentType = %q", snap[0].ContentType)
	}
	if len(snap[0].Responses) != 1 ||
		snap[0].Responses[0].ContentType != "application/octet-stream" {
		t.Errorf("Responses ContentType mismatch: %+v", snap[0].Responses)
	}

	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/blob/raw", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if w.Body.String() != "BC" {
		t.Errorf("raw body = %q", w.Body.String())
	}
}

// ─── 공통: gin path → OpenAPI path 변환이 등록 시점에 일어나는지 확인 ─────

func TestRegister_PathConversionToOpenAPI(t *testing.T) {
	reg := resetDefaultRegistryFor(t)
	e := gin.New()
	g := e.Group("/repos/:owner/:repo")
	RegisterJSON(g, http.MethodPost, "/branches/stats", "Branches",
		func(*gin.Context, sampleReq) (sampleResp, error) { return sampleResp{}, nil },
	)

	snap := reg.Snapshot()
	if snap[0].Path != "/repos/{owner}/{repo}/branches/stats" {
		t.Errorf("path = %q, want %q", snap[0].Path, "/repos/{owner}/{repo}/branches/stats")
	}
}
