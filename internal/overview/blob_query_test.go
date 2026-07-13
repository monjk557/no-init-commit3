package overview

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/api/typed"
)

// Test_RegisterRoutes_BlobOverview_ExposesRefPathQueryParams 는 GET
// /overview/blob 이 ref/path/include_all_commits query parameter 를
// OpenAPI spec 에 노출하는지 검증한다 (Issue-5761). handler 는 이 값들을
// c.Query 로 실제로 읽지만, RegisterGet(LocationNone) 으로 등록돼 있어
// spec generator 가 이 파라미터들을 전혀 emit 하지 않던 drift.
func Test_RegisterRoutes_BlobOverview_ExposesRefPathQueryParams(t *testing.T) {
	reg := typed.NewRegistry()
	restore := typed.SetDefaultRegistry(reg)
	defer restore()

	gin.SetMode(gin.TestMode)
	e := gin.New()
	g := e.Group("/repos/:owner/:repo")

	RegisterRoutes(g, nil)

	var found *typed.Endpoint
	for _, ep := range reg.Snapshot() {
		if ep.Method == http.MethodGet && ep.Path == "/repos/{owner}/{repo}/overview/blob" {
			epCopy := ep
			found = &epCopy
		}
	}
	require.NotNil(t, found, "blob overview endpoint not registered")

	names := map[string]bool{}
	for _, p := range found.QueryParams {
		names[p.Name] = true
	}
	assert.True(t, names["ref"], "ref query param must be exposed in spec")
	assert.True(t, names["path"], "path query param must be exposed in spec")
	assert.True(t, names["include_all_commits"], "include_all_commits query param must be exposed in spec")
}

// Test_HandleGetBlobOverview_MissingPath_Returns400WithOriginalMessage 는
// BlobOverviewRequest 에 `binding:"required"` 를 걸지 않은 선택(schema.go
// 주석 참고)이 실제로 handler 의 기존 커스텀 400 메시지를 그대로 보존하는지
// — gin 의 기본 required-field validator 에러로 대체되지 않는지 — 회귀
// 검증한다. RegisterGetWithQuery 전환 시 가장 깨지기 쉬운 지점.
func Test_HandleGetBlobOverview_MissingPath_Returns400WithOriginalMessage(t *testing.T) {
	handler, _, cleanup := setupTestHandler(t)
	defer cleanup()

	router := gin.New()
	router.GET("/repos/:owner/:repo/overview/blob", adaptQuery(handler.HandleGetBlobOverview))

	req := httptest.NewRequest(http.MethodGet, "/repos/o/r/overview/blob?ref=main", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "body=%s", w.Body.String())
	assert.JSONEq(t, `{"success":false,"error":"Missing required parameter: path"}`, w.Body.String())
}
