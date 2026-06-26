package overview

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func Test_RegisterRoutes_NilHandler_SpecOnly(t *testing.T) {
	t.Parallel()

	r := gin.New()
	// h 가 nil 이면 spec-only 모드 — panic 없이 등록만 되어야 한다.
	require.NotPanics(t, func() {
		RegisterRoutes(r, nil)
	})

	routes := r.Routes()
	paths := map[string]bool{}
	for _, route := range routes {
		paths[route.Path] = true
	}
	assert.True(t, paths["/overview"])
	assert.True(t, paths["/overview/tree"])
	assert.True(t, paths["/overview/blob"])
	assert.True(t, paths["/overview/tree/commits"])
	assert.True(t, paths["/overview/latest-commit"])
	assert.True(t, paths["/overview/latest-commits"])
}

func Test_RegisterRoutes_RealHandler_RegistersAll(t *testing.T) {
	t.Parallel()

	h, _, cleanup := setupTestHandler(t)
	t.Cleanup(cleanup)

	r := gin.New()
	require.NotPanics(t, func() {
		RegisterRoutes(r, h)
	})

	// 6 endpoint 가 등록되어야 한다.
	assert.GreaterOrEqual(t, len(r.Routes()), 6)
}
