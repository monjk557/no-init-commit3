// branches 도메인의 typed 라우트 등록.
//
// server.go (runtime) 와 specs/spec_routes.go (spec 생성) 가 모두 이 함수를
// 호출하여 단일 source of truth 보장.
package branches

import (
	"reflect"

	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/api/typed"
)

const tag = "Branches"

// RegisterRoutes 는 branches 도메인의 모든 endpoint 를 등록한다. h 가 nil 이면
// spec-only 모드 (등록만, 호출 없음). 모든 status 매핑은 handler 본문에서
// inline 으로 typed.NewHTTPError 처리하므로 routes-level ErrorMapper 불필요.
//
// 등록 순서는 gin 의 wildcard 라우트 매칭 우선순위와 무관하지만 (gin tree 가
// path 길이/구체성으로 분기), legacy server.go 의 등록 순서를 그대로 보존
// 한다.
func RegisterRoutes(g gin.IRouter, h *BranchOperationsHandler) {
	var (
		validateName  func(*gin.Context) (ValidateBranchNameResponse, error)
		resolveBranch func(*gin.Context) (ResolveBranchResponse, error)
		validateRopen func(*gin.Context) (ValidateReopenResponse, error)
		listBranches  func(*gin.Context, GetBranchesRequest) (GetBranchesResponse, error)
		createBranch  func(*gin.Context) (CreateBranchResponse, error)
		updateBranch  func(*gin.Context) (UpdateBranchResponse, error)
		stats         func(*gin.Context) (BranchStatsResponse, error)
		bulkDelete    func(*gin.Context) (BulkDeleteBranchesResponse, error)
		deleteBranch  func(*gin.Context) (DeleteBranchResponse, error)
	)
	if h != nil {
		validateName = h.HandleValidateBranchName
		resolveBranch = h.HandleResolveBranch
		validateRopen = h.HandleValidateReopen
		listBranches = h.HandleGetBranches
		createBranch = h.HandleCreateBranch
		updateBranch = h.HandleUpdateBranch
		stats = h.HandleBranchStats
		bulkDelete = h.HandleBulkDeleteBranches
		deleteBranch = h.HandleDeleteBranch
	}

	// GET /branches/validate/*name (wildcard).
	typed.RegisterGet(g, "/branches/validate/*name", tag,
		validateName,
		typed.WithSummary("HandleValidateBranchName"),
	)

	// GET /branches/resolve/*ref (wildcard).
	typed.RegisterGet(g, "/branches/resolve/*ref", tag,
		resolveBranch,
		typed.WithSummary("HandleResolveBranch"),
	)

	// POST /branches/validate-reopen.
	typed.RegisterNoBody(g, "POST", "/branches/validate-reopen", tag,
		validateRopen,
		typed.WithSummary("HandleValidateReopen"),
		typed.WithRequestBody(reflect.TypeOf(ValidateReopenRequest{})),
	)

	// GET /branches.
	//
	// query 파라미터는 RegisterGetWithQuery 로 등록 — wrapper 가 struct
	// field 의 form 태그를 reflect 하여 Endpoint.QueryParams 에 채우고, spec
	// generator 가 OpenAPI parameters (in: query) 로 expand 한다.
	// orchestrator auto-gen client 가 limit/skip/search/sort/email/status
	// 인자를 받도록 노출하기 위함 (Issue-2317 후속).
	typed.RegisterGetWithQuery(g, "/branches", tag,
		listBranches,
		typed.WithSummary("HandleGetBranches"),
	)

	// POST /branches — lenient bind ("Invalid JSON" 친화 메시지).
	typed.RegisterNoBody(g, "POST", "/branches", tag,
		createBranch,
		typed.WithSummary("HandleCreateBranch"),
		typed.WithRequestBody(reflect.TypeOf(CreateBranchRequest{})),
	)

	// PATCH /branches/*branch — lenient bind.
	typed.RegisterNoBody(g, "PATCH", "/branches/*branch", tag,
		updateBranch,
		typed.WithSummary("HandleUpdateBranch"),
		typed.WithRequestBody(reflect.TypeOf(UpdateBranchRequest{})),
	)

	// POST /branches/stats.
	typed.RegisterNoBody(g, "POST", "/branches/stats", tag,
		stats,
		typed.WithSummary("HandleBranchStats"),
		typed.WithRequestBody(reflect.TypeOf(BranchStatsRequest{})),
	)

	// POST /branches/bulk-delete.
	typed.RegisterNoBody(g, "POST", "/branches/bulk-delete", tag,
		bulkDelete,
		typed.WithSummary("HandleBulkDeleteBranches"),
		typed.WithRequestBody(reflect.TypeOf(BulkDeleteBranchesRequest{})),
	)

	// DELETE /branches/*branch (wildcard).
	typed.RegisterNoBody(g, "DELETE", "/branches/*branch", tag,
		deleteBranch,
		typed.WithSummary("HandleDeleteBranch"),
	)
}
