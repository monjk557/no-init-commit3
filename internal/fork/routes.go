// fork 도메인의 typed 라우트 등록.
//
// archetype #2 — sentinel 에러를 status code 로 매핑하는 WithErrorMapper
// 패턴 검증 (POST /forks 의 404/409, POST /fork/sync 의 400/404).
package fork

import (
	"errors"
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

	// POST /forks: 201 Created. service ErrSourceNotFound / ErrForkAlreadyExists
	// 매핑.
	typed.RegisterJSON(g, "POST", "/forks", tag,
		forkRepo,
		typed.WithSummary("HandleForkRepository"),
		typed.WithStatus(http.StatusCreated),
		typed.WithErrorMapper(forkRepositoryErrorMapper),
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

	// POST /fork/sync: ErrNotAFork → 400, ErrUpstreamNotFound → 404.
	typed.RegisterNoBody(g, "POST", "/fork/sync", tag,
		sync,
		typed.WithSummary("HandleSyncUpstream"),
		typed.WithErrorMapper(syncUpstreamErrorMapper),
		typed.WithResponse(http.StatusNotFound, nil, "Repository or upstream not found"),
		hidden,
	)
}

// forkRepositoryErrorMapper 는 service 의 sentinel 에러를 status + 응답으로
// 변환한다. typed.HTTPError 가 이미 wrap 되어 있으면 그것을 우선 사용.
func forkRepositoryErrorMapper(err error) (int, any) {
	var he *typed.HTTPError
	if errors.As(err, &he) {
		return he.Status, gin.H{"success": false, "error": he.Message}
	}
	switch {
	case errors.Is(err, ErrSourceNotFound):
		return http.StatusNotFound, gin.H{"success": false, "error": err.Error()}
	case errors.Is(err, ErrForkAlreadyExists):
		return http.StatusConflict, gin.H{"success": false, "error": err.Error()}
	}
	return http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()}
}

// syncUpstreamErrorMapper 는 sync sentinel 매핑.
func syncUpstreamErrorMapper(err error) (int, any) {
	var he *typed.HTTPError
	if errors.As(err, &he) {
		return he.Status, gin.H{"success": false, "error": he.Message}
	}
	switch {
	case errors.Is(err, ErrNotAFork):
		return http.StatusBadRequest, gin.H{"success": false, "error": err.Error()}
	case errors.Is(err, ErrUpstreamNotFound):
		return http.StatusNotFound, gin.H{"success": false, "error": err.Error()}
	}
	return http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()}
}
