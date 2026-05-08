package tree

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/collabops/git-executor/internal/api/typed"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTreeRoutes_RegisterAsGetWithQuery 는 /tree 와 /tree/search 가
// typed.RegisterGetWithQuery 로 등록되어 query parameter 가 spec 에 expand
// 되도록 보장한다. RegisterGet 으로 회귀하면 orchestrator auto-gen client 에
// 인자가 누락되는 dead method 가 발생한다 (Issue-2327).
func TestTreeRoutes_RegisterAsGetWithQuery(t *testing.T) {
	reg := typed.NewRegistry()
	restore := typed.SetDefaultRegistry(reg)
	defer restore()

	gin.SetMode(gin.TestMode)
	e := gin.New()
	g := e.Group("/repos/:owner/:repo")

	RegisterRoutes(g, nil)

	endpointsByPath := map[string]*typed.Endpoint{}
	for _, ep := range reg.Snapshot() {
		ep := ep
		endpointsByPath[ep.Path] = &ep
	}

	t.Run("/tree exposes ref/path/recursive query params", func(t *testing.T) {
		ep := endpointsByPath["/repos/{owner}/{repo}/tree"]
		require.NotNil(t, ep)
		assert.Equal(t, http.MethodGet, ep.Method)
		assert.Equal(t, "Tree", ep.Tag)
		assert.Equal(t, "HandleTreeAPI", ep.Summary)
		assert.Equal(t, typed.LocationQuery, ep.ReqLocation)
		require.NotNil(t, ep.ReqType)
		assert.Equal(t, reflect.TypeOf(TreeAPIRequest{}), ep.ReqType)

		paramNames := make([]string, 0, len(ep.QueryParams))
		for _, p := range ep.QueryParams {
			paramNames = append(paramNames, p.Name)
		}
		assert.ElementsMatch(t, []string{"ref", "path", "recursive"}, paramNames)
	})

	t.Run("/tree/search exposes ref/query/limit query params", func(t *testing.T) {
		ep := endpointsByPath["/repos/{owner}/{repo}/tree/search"]
		require.NotNil(t, ep)
		assert.Equal(t, http.MethodGet, ep.Method)
		assert.Equal(t, "Tree", ep.Tag)
		assert.Equal(t, "HandleTreeSearch", ep.Summary)
		assert.Equal(t, typed.LocationQuery, ep.ReqLocation)
		require.NotNil(t, ep.ReqType)
		assert.Equal(t, reflect.TypeOf(TreeSearchRequest{}), ep.ReqType)

		paramNames := make([]string, 0, len(ep.QueryParams))
		for _, p := range ep.QueryParams {
			paramNames = append(paramNames, p.Name)
		}
		assert.ElementsMatch(t, []string{"ref", "query", "limit"}, paramNames)
	})
}
