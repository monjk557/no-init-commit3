// fork 도메인의 typed 라우트 등록.
//
// archetype #2 (과거) — sentinel 에러를 status code 로 매핑하던 WithErrorMapper
// 패턴을 classify*Error 헬퍼 + 카탈로그로 이관했다.
package fork

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/api/typed"
)

const tag = "Repository Management"

// RegisterRoutes 는 fork 도메인의 모든 endpoint 를 등록한다. h 가 nil 이면
// spec-only 모드 (등록만, 호출 없음).
func RegisterRoutes(g gin.IRouter, h *ForkHandler) {
	var (
		forkRepo func(*gin.Context, ForkRepositoryRequest) (ForkRepositoryResponse, error)
		getInfo  func(*gin.Context) (ForkInfoResponse, error)
		sync     func(*gin.Context) (SyncUpstreamResponse, error)
	)
	if h != nil {
		forkRepo = h.HandleForkRepository
		getInfo = h.HandleGetForkInfo
		sync = h.HandleSyncUpstream
	}

	// 모든 fork endpoint 는 현재 기능에서 빠져있어 외부 OpenAPI spec 에는
	// 노출하지 않는다 (typed.WithExcludeFromSpec). 단, gin 런타임 라우트는
	// 그대로 등록되어 기존 호출은 계속 작동한다.
	hidden := typed.WithExcludeFromSpec()

	// POST /forks: 201 Created. classifyForkRepositoryError 가 카탈로그
	// code 를 가진 *typed.HTTPError 를 이미 반환하므로 커스텀 ErrorMapper
	// 불필요.
	typed.RegisterJSON(g, "POST", "/forks", tag,
		forkRepo,
		typed.WithSummary("HandleForkRepository"),
		typed.WithStatus(http.StatusCreated),
		typed.WithResponse(http.StatusNotFound, nil, "Source repository not found"),
		typed.WithResponse(http.StatusConflict, nil, "Fork target already exists"),
		hidden,
	)

	// GET /fork: HTTPError default 매핑.
	typed.RegisterGet(g, "/fork", tag,
		getInfo,
		typed.WithSummary("HandleGetForkInfo"),
		typed.WithResponse(http.StatusNotFound, nil, "Repository not found"),
		hidden,
	)

	// POST /fork/sync: classifySyncUpstreamError 가 카탈로그 code 를 가진
	// *typed.HTTPError 를 이미 반환하므로 커스텀 ErrorMapper 불필요.
	typed.RegisterNoBody(g, "POST", "/fork/sync", tag,
		sync,
		typed.WithSummary("HandleSyncUpstream"),
		typed.WithResponse(http.StatusNotFound, nil, "Repository or upstream not found"),
		hidden,
	)
}
