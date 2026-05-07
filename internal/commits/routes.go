// commits 도메인의 typed 라우트 등록.
//
// server.go (runtime) 와 specs/spec_routes.go (spec 생성) 가 모두 이 함수를
// 호출하여 단일 source of truth 보장.
package commits

import (
	"reflect"

	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/api/typed"
)

const tag = "Commits"

// RegisterRoutes 는 commits 도메인의 모든 endpoint 를 등록한다. h 가 nil 이면
// spec-only 모드 (등록만, 호출 없음). 모든 status 매핑은 handler 본문에서
// inline 으로 typed.NewHTTPError 처리하므로 routes.go 의 ErrorMapper 불필요
// (default *HTTPError 매핑이 그대로 사용됨).
func RegisterRoutes(g gin.IRouter, h *CommitsHandler) {
	var (
		listCommits       func(*gin.Context) (CommitsResponse, error)
		comparisonSummary func(*gin.Context, ComparisonSummaryRequest) (ComparisonSummaryResponse, error)
		commitDetail      func(*gin.Context) (CommitDetailResponse, error)
		multiFileCommit   func(*gin.Context) (MultiFileCommitResponse, error)
	)
	if h != nil {
		listCommits = h.HandleCommitsAPI
		comparisonSummary = h.HandleComparisonSummary
		commitDetail = h.HandleGetCommitDetail
		multiFileCommit = h.HandleMultiFileCommit
	}

	// GET /commits — list with pagination.
	typed.RegisterGet(g, "/commits", tag,
		listCommits,
		typed.WithSummary("HandleCommitsAPI"),
	)

	// GET /commits/compare/summary — lightweight comparison.
	// /:sha 라우트와 충돌 가능성이 있으나 "compare/summary" 는 sha 패턴이
	// 아니므로 gin 의 path matching 에서 정확히 분기됨.
	//
	// query 파라미터는 RegisterGetWithQuery 로 등록 — wrapper 가 struct
	// field 의 form 태그를 reflect 하여 Endpoint.QueryParams 에 채우고, spec
	// generator 가 OpenAPI parameters (in: query) 로 expand 한다.
	// orchestrator auto-gen client 가 source_ref/target_ref 인자를 받도록
	// 노출하기 위함 (Issue-2317).
	typed.RegisterGetWithQuery(g, "/commits/compare/summary", tag,
		comparisonSummary,
		typed.WithSummary("HandleComparisonSummary"),
	)

	// GET /commits/:sha — single commit detail.
	typed.RegisterGet(g, "/commits/:sha", tag,
		commitDetail,
		typed.WithSummary("HandleGetCommitDetail"),
	)

	// POST /commits — multi-file commit. lenient bind (custom "Invalid JSON"
	// message) → RegisterNoBody + WithRequestBody.
	typed.RegisterNoBody(g, "POST", "/commits", tag,
		multiFileCommit,
		typed.WithSummary("HandleMultiFileCommit"),
		typed.WithRequestBody(reflect.TypeOf(MultiFileCommitRequest{})),
	)
}
