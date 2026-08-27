// tree 도메인의 typed 라우트 등록.
//
// server.go (runtime) 와 specs/spec_routes.go (spec 생성) 가 모두 이 함수를
// 호출하여 단일 source of truth 보장.
package tree

import (
	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/api/typed"
)

const tag = "Tree"

// RegisterRoutes 는 tree 도메인의 모든 endpoint 를 등록한다. h 가 nil 이면
// spec-only 모드 (등록만, 호출 없음).
func RegisterRoutes(g gin.IRouter, h *TreeHandler) {
	var (
		treeAPI    func(*gin.Context, TreeAPIRequest) (TreeEntriesRecursiveResponse, error)
		treeSearch func(*gin.Context, TreeSearchRequest) (TreeSearchResponse, error)
	)
	if h != nil {
		treeAPI = h.HandleTreeAPI
		treeSearch = h.HandleTreeSearch
	}

	// GET /tree: ref/path/recursive 는 TreeAPIRequest 로 바인딩.
	// classifyTreeError 가 카탈로그 code 를 가진 *typed.HTTPError 를 이미
	// 반환하므로 커스텀 ErrorMapper 불필요. RegisterGetWithQuery 로 spec 에
	// query parameter 를 expand 하여 orchestrator auto-gen client 가 인자
	// 누락 없이 호출하도록 한다 (Issue-2327).
	typed.RegisterGetWithQuery(g, "/tree", tag,
		treeAPI,
		typed.WithSummary("HandleTreeAPI"),
	)

	// GET /tree/search: TreeSearchRequest 로 바인딩. classifyTreeSearchError
	// 가 카탈로그 code 를 가진 *typed.HTTPError 를 이미 반환하므로 커스텀
	// ErrorMapper 불필요.
	typed.RegisterGetWithQuery(g, "/tree/search", tag,
		treeSearch,
		typed.WithSummary("HandleTreeSearch"),
	)
}
