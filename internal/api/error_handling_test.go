// package api_test (external) — tree 가 internal/api 를 import 하게 되면서
// (카탈로그 마이그레이션) package api 내부 테스트로 두면 api → tree → api
// import cycle 이 생긴다. 이 파일은 blob/tree/repositories 의 exported 표면만
// 쓰는 순수 black-box 통합 테스트라 외부 테스트 패키지로 옮겨도 손실이 없다.
package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/collabops/git-executor/internal/blob"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/collabops/git-executor/internal/tree"
	"github.com/gin-gonic/gin"
	gogit "github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/require"
)

func setupRouterForErrorTests(t *testing.T) (*gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	tempDir := t.TempDir()
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
		Git:     config.GitConfig{RepoRoot: tempDir},
	}

	blobHandler, err := blob.NewBlobHandler(cfg)
	require.NoError(t, err)
	treeHandler, err := tree.NewTreeHandler(cfg)
	require.NoError(t, err)
	repoHandler, err := repositories.NewRepositoryCreationHandler(cfg)
	require.NoError(t, err)

	r := gin.New()
	r.GET("/repos/:owner/:repo/blob", blobHandler.HandleBlobAPI)
	g := r.Group("/repos/:owner/:repo")
	tree.RegisterRoutes(g, treeHandler)
	repositories.RegisterRoutes(g, repoHandler)

	return r, tempDir
}

// --- 13.1A Status Code 테스트 ---

// Test_AllAPIs_NotFound_Returns404 리소스 없음 -> 404
func Test_AllAPIs_NotFound_Returns404(t *testing.T) {
	r, _ := setupRouterForErrorTests(t)

	// Blob: 존재하지 않는 저장소
	req := httptest.NewRequest("GET", "/repos/nonexistent/norepo/blob?path=README.md&ref=main", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)

	// Tree: 존재하지 않는 저장소
	req2 := httptest.NewRequest("GET", "/repos/nonexistent/norepo/tree?ref=main", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusNotFound, w2.Code)
}

// Test_AllAPIs_BadRequest_Returns400 잘못된 요청 -> 400
func Test_AllAPIs_BadRequest_Returns400(t *testing.T) {
	r, _ := setupRouterForErrorTests(t)

	// Blob: path 누락
	req := httptest.NewRequest("GET", "/repos/o/r/blob?ref=main", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// Blob: ref 누락
	req2 := httptest.NewRequest("GET", "/repos/o/r/blob?path=x.txt", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusBadRequest, w2.Code)
}

// Test_RefNotFound_Returns404_Not409 ref 없음은 404 (409 아님)
func Test_RefNotFound_Returns404_Not409(t *testing.T) {
	r, tempDir := setupRouterForErrorTests(t)
	// 저장소 하나 생성
	owner, repo := "testowner", "testrepo"
	repoPath := filepath.Join(tempDir, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))
	_, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repo+"/blob?path=README.md&ref=refs/heads/nonexistent-branch", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
	body := w.Body.String()
	require.NotContains(t, body, "409")
	require.NotContains(t, body, "CONFLICT")
	require.NotContains(t, body, "CONCURRENT")
}

// Test_AllAPIs_InternalError_Returns500 서버 에러 시 500 (동작 확인용: panic 시 500)
func Test_AllAPIs_InternalError_Returns500(t *testing.T) {
	// 실제 500을 유발하지 않고, 500 응답 형식 계약만 검증 (핸들러가 500 반환 시 code/형식)
	// 현재 코드베이스에서 500을 반환하는 경로가 없으면 스킵 가능
	t.Skip("500을 유발하는 통합 경로 없음; 계약 검증은 미들웨어/핸들러 추가 시 활성화")
}

// --- 13.2 Error Response 형식 ---

// Test_ErrorResponse_IncludesErrorField error 필드 포함
func Test_ErrorResponse_IncludesErrorField(t *testing.T) {
	r, _ := setupRouterForErrorTests(t)
	req := httptest.NewRequest("GET", "/repos/o/r/blob?ref=main", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &m))
	_, hasError := m["error"]
	require.True(t, hasError, "response should include error field: %s", w.Body.String())
}

// Test_ErrorResponse_IncludesSuccessFalse success: false 포함 (해당 시 400/404)
func Test_ErrorResponse_IncludesSuccessFalse(t *testing.T) {
	r, _ := setupRouterForErrorTests(t)
	req := httptest.NewRequest("GET", "/repos/nonexistent/norepo/tree?ref=main", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &m))
	success, ok := m["success"]
	require.True(t, ok, "response should include success field")
	require.False(t, success.(bool), "success should be false for error")
}

// Test_ErrorResponse_NoSensitiveInfo 민감 정보 노출 안 함
func Test_ErrorResponse_NoSensitiveInfo(t *testing.T) {
	r, _ := setupRouterForErrorTests(t)
	req := httptest.NewRequest("GET", "/repos/o/r/blob?path=foo%0abar&ref=main", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	body := w.Body.String()
	require.NotContains(t, body, "panic")
	require.NotContains(t, body, "runtime.")
	require.NotContains(t, body, ".go:")
	require.NotContains(t, body, "goroutine")
}

// Test_ErrorResponse_RedactsFilesystemPaths_AndInternalRefs 에러에 서버 내부 경로/.git/스택 노출 안 함
func Test_ErrorResponse_RedactsFilesystemPaths_AndInternalRefs(t *testing.T) {
	r, _ := setupRouterForErrorTests(t)
	req := httptest.NewRequest("GET", "/repos/../../etc/passwd/repo/blob?path=x&ref=main", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	body := strings.ToLower(w.Body.String())
	require.NotRegexp(t, `/[a-z]+/.*\.git`, body, "error should not expose .git paths")
	require.NotContains(t, body, "stack")
	require.NotContains(t, body, "trace")
}

// --- 13.2A Field Errors / Request ID ---

// Test_ValidationError_IncludesFieldErrors branchName/path/ref 등 필드 오류 정보
func Test_ValidationError_IncludesFieldErrors(t *testing.T) {
	r, _ := setupRouterForErrorTests(t)
	req := httptest.NewRequest("GET", "/repos/o/r/blob?path=&ref=main", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &m))
	errStr, _ := m["error"].(string)
	require.Contains(t, errStr, "path")
}

// Test_ExternalServiceError_IncludesRequestId_NoInternals 외부 서비스 에러 시 request_id 포함, 내부 상세 없음
func Test_ExternalServiceError_IncludesRequestId_NoInternals(t *testing.T) {
	// 현재 API가 X-Request-ID 미들웨어 없이 테스트 라우터를 쓰므로, request_id 검증은 통합 서버 테스트에서 수행
	t.Skip("request_id는 RequestMetadataMiddleware 사용 시 검증")
}

// --- 13.1C 503/429/499 Timeout/과부하 계약 (P0) ---

// Test_Timeout_Returns503_TIMEOUT_WithRetryAfter deadline 초과 시 503 TIMEOUT, Retry-After
func Test_Timeout_Returns503_TIMEOUT_WithRetryAfter(t *testing.T) {
	t.Skip("timeout 미들웨어/핸들러 구현 후 활성화")
}

// Test_Overload_Returns503_OVERLOADED_WithRetryAfter 과부하 시 503 OVERLOADED, Retry-After
func Test_Overload_Returns503_OVERLOADED_WithRetryAfter(t *testing.T) {
	t.Skip("overload 미들웨어 구현 후 활성화")
}

// Test_RateLimit_Returns429_RATE_LIMITED_WithRetryAfter rate limit 시 429, Retry-After (security 패키지에서 검증)
func Test_RateLimit_Returns429_RATE_LIMITED_WithRetryAfter(t *testing.T) {
	t.Skip("429/Retry-After는 internal/security/rate_limiter_test.go에서 검증")
}

// Test_DependencyTimeout_Returns503_DEPENDENCY_TIMEOUT upstream 타임아웃 시 503
func Test_DependencyTimeout_Returns503_DEPENDENCY_TIMEOUT(t *testing.T) {
	t.Skip("dependency timeout 핸들링 구현 후 활성화")
}

// Test_ClientCanceled_Returns499Or408_CANCELED 클라이언트 끊김 시 499 또는 408
func Test_ClientCanceled_Returns499Or408_CANCELED(t *testing.T) {
	t.Skip("client cancel 핸들링 구현 후 활성화")
}
