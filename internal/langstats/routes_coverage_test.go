package langstats

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// routes_coverage_test.go — RegisterRoutes 를 spec-only(nil) 및 실제 handler
// 두 경로로 커버한다. (helper buildLangRepo/langHandler 는
// handler_extra_coverage_test.go 에 정의되어 있다 — 같은 package.)

func TestRegisterRoutes_SpecOnlyMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	grp := r.Group("/repos/:owner/:repo")
	// nil handler → 등록만 수행, panic 없어야 한다.
	assert.NotPanics(t, func() { RegisterRoutes(grp, nil) })
}

func TestRegisterRoutes_WithHandler_ServesLanguages(t *testing.T) {
	gin.SetMode(gin.TestMode)

	root := t.TempDir()
	buildLangRepo(t, root, "acme", "proj")
	h := langHandler(t, root)

	r := gin.New()
	grp := r.Group("/repos/:owner/:repo")
	RegisterRoutes(grp, h)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/repos/acme/proj/languages", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	assert.True(t, strings.Contains(w.Body.String(), "success"))
}
