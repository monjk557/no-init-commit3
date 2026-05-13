package gc

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/api/typed"
)

const tag = "LFS"

// RegisterRoutes 는 LFS GC scan endpoint 를 typed registry + gin 라우터에 등록한다.
//
// h 가 nil 이면 spec-only 모드 (specs/spec_routes.go 가 호출).
func RegisterRoutes(g gin.IRouter, h *Handler) {
	var op func(*gin.Context) (ScanReachableOIDsResponse, error)
	if h != nil {
		op = h.HandleScanReachableOIDs
	}

	typed.RegisterNoBody(
		g, http.MethodPost, "/lfs/reachable-oids", tag, op,
		typed.WithSummary("HandleScanReachableOIDs"),
		typed.WithExcludeFromSpec(), // internal endpoint — orchestrator GC cron only
	)
}
