// diffs 도메인의 typed 라우트 등록.
//
// server.go (runtime) 와 specs/spec_routes.go (spec 생성) 가 모두 이 함수를
// 호출하여 단일 source of truth 보장.
package diffs

import (
	"errors"
	"net/http"
	"reflect"

	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/api/typed"
)

const tag = "Diff"

// responseOverride 는 다중 필드 응답 (DiffTooLarge 413 with reason, NOT_FOUND
// 404 with code) 또는 alternate success shape (paginated PaginatedDiffResponse
// vs flat DiffResponse) 를 위한 wrapper.
//
// handler 는 nil-zero response + responseOverride 에러를 반환하고, mapper 는
// status + body 를 그대로 c.JSON 으로 응답.
type responseOverride struct {
	status int
	body   any
}

func (e *responseOverride) Error() string { return "response override" }

// RegisterRoutes 는 diffs 도메인의 모든 endpoint 를 등록한다. h 가 nil 이면
// spec-only 모드.
func RegisterRoutes(g gin.IRouter, h *DiffHandler) {
	var (
		commitDiff       func(*gin.Context) (DiffResponse, error)
		branchDiff       func(*gin.Context) (DiffResponse, error)
		branchDiffQuery  func(*gin.Context, BranchDiffQueryRequest) (DiffResponse, error)
		fileDiff         func(*gin.Context, FileDiffRequest) (DiffResponse, error)
		singleCommitDiff func(*gin.Context) (SingleCommitDiffResponse, error)
		expandContext    func(*gin.Context, ExpandContextRequest) (ExpandContextResponse, error)
		fileSearch       func(*gin.Context) (DiffFileSearchResponse, error)
		classifyAnchors  func(*gin.Context) (ClassifyAnchorsResponse, error)
		compareChanged   func(*gin.Context) (CompareChangedResponse, error)
	)
	if h != nil {
		commitDiff = h.HandleCommitDiff
		branchDiff = h.HandleBranchDiff
		branchDiffQuery = h.HandleBranchDiffQuery
		fileDiff = h.HandleFileDiff
		singleCommitDiff = h.HandleSingleCommitDiff
		expandContext = h.HandleExpandContext
		fileSearch = h.HandleDiffFileSearch
		classifyAnchors = h.HandleClassifyAnchors
		compareChanged = h.HandleCompareChanged
	}

	typed.RegisterGet(g, "/diff/commits/:base/:head", tag,
		commitDiff,
		typed.WithSummary("HandleCommitDiff"),
		typed.WithErrorMapper(diffErrorMapper),
	)

	typed.RegisterGet(g, "/diff/branches/:base/:head", tag,
		branchDiff,
		typed.WithSummary("HandleBranchDiff"),
		typed.WithErrorMapper(diffErrorMapper),
	)

	// GET /diff/branches?base=&head= — query params 를 RegisterGetWithQuery 로
	// 노출하여 orchestrator auto-gen client 가 base/head 인자를 받도록 한다
	// (Issue-2317 후속).
	typed.RegisterGetWithQuery(g, "/diff/branches", tag,
		branchDiffQuery,
		typed.WithSummary("HandleBranchDiffQuery"),
		typed.WithErrorMapper(diffErrorMapper),
	)

	// GET /diff/file?path=&base=&head=.
	typed.RegisterGetWithQuery(g, "/diff/file", tag,
		fileDiff,
		typed.WithSummary("HandleFileDiff"),
		typed.WithErrorMapper(diffErrorMapper),
	)

	typed.RegisterGet(g, "/diff/commit/:sha", tag,
		singleCommitDiff,
		typed.WithSummary("HandleSingleCommitDiff"),
		typed.WithErrorMapper(diffErrorMapper),
	)

	// GET /diff/expand-context — query 는 ExpandContextRequest 로 bind.
	typed.RegisterGetWithQuery(g, "/diff/expand-context", tag,
		expandContext,
		typed.WithSummary("HandleExpandContext"),
	)

	// GET /diff/patch/:base/:head — text/plain raw output.
	if h != nil {
		typed.RegisterRaw(g, "GET", "/diff/patch/:base/:head", tag,
			h.HandlePatch,
			"text/plain",
			typed.WithSummary("HandlePatch"),
		)
	} else {
		typed.RegisterRaw(g, "GET", "/diff/patch/:base/:head", tag,
			func(c *gin.Context) {},
			"text/plain",
			typed.WithSummary("HandlePatch"),
		)
	}

	typed.RegisterGet(g, "/diff/search/files", tag,
		fileSearch,
		typed.WithSummary("HandleDiffFileSearch"),
	)

	// POST /diff/classify-anchors — lenient bind archetype.
	typed.RegisterNoBody(g, "POST", "/diff/classify-anchors", tag,
		classifyAnchors,
		typed.WithSummary("HandleClassifyAnchors"),
		typed.WithRequestBody(reflect.TypeOf(ClassifyAnchorsRequest{})),
	)

	// POST /diff/compare-changed — 두 head 의 CR diff content 동일 여부 (Issue-5254).
	typed.RegisterNoBody(g, "POST", "/diff/compare-changed", tag,
		compareChanged,
		typed.WithSummary("HandleCompareChanged"),
		typed.WithRequestBody(reflect.TypeOf(CompareChangedRequest{})),
	)
}

// diffErrorMapper 는 *responseOverride / *typed.HTTPError / 기타 에러를
// 매핑한다.
//   - *responseOverride → status + body 그대로 (다중 필드 응답 또는 alternate success)
//   - *typed.HTTPError → 단순 {success, error}
//   - 그 외 → 500 + err.Error()
func diffErrorMapper(err error) (int, any) {
	var rich *responseOverride
	if errors.As(err, &rich) {
		return rich.status, rich.body
	}
	var he *typed.HTTPError
	if errors.As(err, &he) {
		return he.Status, gin.H{"success": false, "error": he.Message}
	}
	return http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()}
}
