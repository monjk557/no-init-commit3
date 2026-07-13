// maintenance 도메인의 typed 라우트 등록.
//
// server.go (runtime) 와 specs/spec_routes.go (spec 생성) 가 모두 이 함수를
// 호출하여 단일 source of truth 를 보장한다.
package maintenance

import (
	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/api/typed"
)

const tag = "Maintenance"

// RegisterRoutes 는 maintenance 도메인의 endpoint 를 등록한다. h 가 nil 이면
// spec-only 모드 (등록만, 호출 없음). status 매핑은 handler 본문에서 inline
// typed.NewHTTPError 로 처리하므로 routes-level ErrorMapper 는 불필요하다.
func RegisterRoutes(g gin.IRouter, h *Handler) {
	var maintain func(*gin.Context) (MaintainRepoResponse, error)
	if h != nil {
		maintain = h.HandleMaintainRepo
	}

	// POST /repos/{owner}/{repo}/maintenance — 특정 repo 즉시 full 정비
	// (repack + bitmap + commit-graph). 대형 초기 ingest 직후 import-worker /
	// api-service 가 호출 (Doc-743 §6.4, Issue-5273 계열).
	typed.RegisterNoBody(g, "POST", "/maintenance", tag,
		maintain,
		typed.WithSummary("HandleMaintainRepo"),
	)
}
