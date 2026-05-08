// tree 도메인의 typed 라우트 등록.
//
// server.go (runtime) 와 specs/spec_routes.go (spec 생성) 가 모두 이 함수를
// 호출하여 단일 source of truth 보장.
package tree

import (
	"errors"
	"net/http"
	"strings"

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

	// GET /tree: ref/path/recursive 는 TreeAPIRequest 로 바인딩. "path is not
	// a tree" → 400, 그 외 raw → 404. RegisterGetWithQuery 로 spec 에 query
	// parameter 를 expand 하여 orchestrator auto-gen client 가 인자 누락 없이
	// 호출하도록 한다 (Issue-2327).
	typed.RegisterGetWithQuery(g, "/tree", tag,
		treeAPI,
		typed.WithSummary("HandleTreeAPI"),
		typed.WithErrorMapper(treeAPIErrorMapper),
	)

	// GET /tree/search: TreeSearchRequest 로 바인딩. "failed to resolve
	// reference" → 404, 그 외 raw → 500.
	typed.RegisterGetWithQuery(g, "/tree/search", tag,
		treeSearch,
		typed.WithSummary("HandleTreeSearch"),
		typed.WithErrorMapper(treeSearchErrorMapper),
	)
}

// treeAPIErrorMapper 는 HandleTreeAPI 의 raw 에러를 status 로 분류한다.
// "path is not a tree" → 400, 그 외 → 404. *typed.HTTPError 우선.
func treeAPIErrorMapper(err error) (int, any) {
	var he *typed.HTTPError
	if errors.As(err, &he) {
		return he.Status, gin.H{"success": false, "error": he.Message}
	}
	errMsg := err.Error()
	if strings.Contains(errMsg, "path is not a tree") {
		return http.StatusBadRequest, gin.H{"success": false, "error": errMsg}
	}
	return http.StatusNotFound, gin.H{"success": false, "error": errMsg}
}

// treeSearchErrorMapper 는 HandleTreeSearch 의 raw 에러를 status 로 분류한다.
// "failed to resolve reference" → 404, 그 외 → 500. *typed.HTTPError 우선.
func treeSearchErrorMapper(err error) (int, any) {
	var he *typed.HTTPError
	if errors.As(err, &he) {
		return he.Status, gin.H{"success": false, "error": he.Message}
	}
	errMsg := err.Error()
	if strings.Contains(errMsg, "failed to resolve reference") {
		return http.StatusNotFound, gin.H{"success": false, "error": errMsg}
	}
	return http.StatusInternalServerError, gin.H{"success": false, "error": errMsg}
}
