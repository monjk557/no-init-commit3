// overview 도메인의 typed 라우트 등록.
//
// server.go (runtime) 와 specs/spec_routes.go (spec 생성) 가 모두 이 함수를
// 호출하여 단일 source of truth 보장.
package overview

import (
	"reflect"

	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/api/typed"
)

const tag = "Overview"

// RegisterRoutes 는 overview 도메인의 모든 endpoint 를 등록한다. h 가 nil 이면
// spec-only 모드. 모든 status 매핑은 handler 본문에서 inline 으로
// typed.NewHTTPError 처리하므로 routes-level ErrorMapper 불필요.
func RegisterRoutes(g gin.IRouter, h *RepositoryOverviewHandler) {
	var (
		overviewMain   func(*gin.Context) (RepositoryOverviewResponse, error)
		treeOverview   func(*gin.Context) (TreeOverviewResponse, error)
		blobOverview   func(*gin.Context, BlobOverviewRequest) (BlobOverviewResponse, error)
		treeWithCommit func(*gin.Context, TreeWithCommitOverviewRequest) (TreeWithCommitOverviewResponse, error)
		latestCommit   func(*gin.Context, LatestCommitOverviewRequest) (LatestCommitOverviewResponse, error)
		batchCommits   func(*gin.Context) (LatestCommitsBatchResponse, error)
	)
	if h != nil {
		overviewMain = h.HandleGetRepositoryOverview
		treeOverview = h.HandleGetTreeOverview
		blobOverview = h.HandleGetBlobOverview
		treeWithCommit = h.HandleGetTreeWithCommitOverview
		latestCommit = h.HandleGetLatestCommitOverview
		batchCommits = h.HandlePostLatestCommitsBatch
	}

	typed.RegisterGet(g, "/overview", tag,
		overviewMain,
		typed.WithSummary("HandleGetRepositoryOverview"),
	)

	typed.RegisterGet(g, "/overview/tree", tag,
		treeOverview,
		typed.WithSummary("HandleGetTreeOverview"),
	)

	// GET /overview/blob?ref=&path=&include_all_commits= — query params 를
	// spec 에 노출하기 위해 RegisterGetWithQuery 사용 (Issue-5761, Issue-2317
	// 후속 누락분).
	typed.RegisterGetWithQuery(g, "/overview/blob", tag,
		blobOverview,
		typed.WithSummary("HandleGetBlobOverview"),
	)

	// GET /overview/tree/commits?ref=&path= — query params 를 spec 에 노출하기
	// 위해 RegisterGetWithQuery 사용 (Issue-2317 후속).
	typed.RegisterGetWithQuery(g, "/overview/tree/commits", tag,
		treeWithCommit,
		typed.WithSummary("HandleGetTreeWithCommitOverview"),
	)

	// GET /overview/latest-commit?ref=&path=.
	typed.RegisterGetWithQuery(g, "/overview/latest-commit", tag,
		latestCommit,
		typed.WithSummary("HandleGetLatestCommitOverview"),
	)

	typed.RegisterNoBody(g, "POST", "/overview/latest-commits", tag,
		batchCommits,
		typed.WithSummary("HandlePostLatestCommitsBatch"),
		typed.WithRequestBody(reflect.TypeOf(LatestCommitsBatchRequest{})),
	)
}
