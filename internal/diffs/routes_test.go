package diffs

import (
	"net/http"
	"testing"

	"github.com/collabops/git-executor/internal/api/typed"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDiffRoutes_PatchEndpoint_RegisteredAsRawTextPlain 는 /diff/patch/:base/:head
// 가 typed.RegisterRaw 를 통해 text/plain content-type 으로 registry 에 capture
// 되는지 검증한다 — orchestrator auto-gen 클라이언트가 raw text endpoint 로
// 인식할 수 있는 spec 발현의 단일 source of truth.
//
// Issue-2327 의 회귀 방지: /diff/patch 가 typed wrapper 를 우회해 ad-hoc gin
// 등록으로 돌아가면 spec 에서 dead method 가 emit 된다.
func TestDiffRoutes_PatchEndpoint_RegisteredAsRawTextPlain(t *testing.T) {
	reg := typed.NewRegistry()
	restore := typed.SetDefaultRegistry(reg)
	defer restore()

	gin.SetMode(gin.TestMode)
	e := gin.New()
	g := e.Group("/repos/:owner/:repo")

	RegisterRoutes(g, nil)

	var patchEP *typed.Endpoint
	for _, ep := range reg.Snapshot() {
		if ep.Method == http.MethodGet && ep.Path == "/repos/{owner}/{repo}/diff/patch/{base}/{head}" {
			ep := ep
			patchEP = &ep
			break
		}
	}

	require.NotNil(t, patchEP, "/diff/patch endpoint must be registered")
	assert.Equal(t, "Diff", patchEP.Tag)
	assert.Equal(t, "HandlePatch", patchEP.Summary)
	assert.Equal(t, typed.LocationRawBytes, patchEP.ReqLocation)
	assert.Equal(t, "text/plain", patchEP.ContentType)

	require.Len(t, patchEP.Responses, 1)
	assert.Equal(t, http.StatusOK, patchEP.Responses[0].Status)
	assert.Equal(t, "text/plain", patchEP.Responses[0].ContentType)
}
