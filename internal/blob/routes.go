// blob 도메인의 typed 라우트 등록.
//
// /blob 은 binary streaming endpoint 이므로 typed.RegisterRaw 를 사용하여
// application/octet-stream content type 을 spec 에 노출한다. JSON wrapper /
// error mapper 는 적용되지 않으며 handler 가 c.Data / c.JSON (에러 응답) 을
// 직접 다룬다.
//
// LFS pointer 메타데이터는 /overview/blob (BlobOverviewResponse.IsLFSPointer
// + LFS 필드) 에서 제공한다 — Issue-2327 에서 raw /blob 의 polymorphic LFS
// JSON 분기를 제거해 책임을 overview 도메인으로 이전했다.
//
// server.go (runtime) 와 specs/spec_routes.go (spec 생성) 가 모두 이 함수를
// 호출하여 단일 source of truth 보장.
package blob

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/api/typed"
)

const tag = "Blob"

// RegisterRoutes 는 blob 도메인의 모든 endpoint 를 typed registry + gin
// 라우터에 등록한다. h 가 nil 이면 spec-only 모드.
func RegisterRoutes(g gin.IRouter, h *BlobHandler) {
	var handler gin.HandlerFunc
	if h != nil {
		handler = h.HandleBlobAPI
	} else {
		handler = func(c *gin.Context) {}
	}

	typed.RegisterRaw(
		g, http.MethodGet, "/blob", tag,
		handler,
		"application/octet-stream",
		typed.WithSummary("HandleBlobAPI"),
	)
}
