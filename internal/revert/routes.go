// revert 도메인의 typed 라우트 등록.
//
// server.go (runtime) 와 specs/spec_routes.go (spec 생성) 가 모두 이 함수를
// 호출하여 단일 source of truth 보장.
package revert

import (
	"reflect"

	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/api/typed"
)

const tag = "Revert"

// RegisterRoutes 는 revert 도메인의 모든 endpoint 를 등록한다. h 가 nil 이면
// spec-only 모드 (등록만, 호출 없음).
func RegisterRoutes(g gin.IRouter, h *RevertHandler) {
	var handleRevert func(*gin.Context) (RevertResponse, error)
	if h != nil {
		handleRevert = h.HandleRevert
	}

	// POST /revert: lenient bind. HandleRevert 가 classifyRevertError 로
	// 모든 에러를 카탈로그 code 를 가진 *typed.HTTPError 로 미리 변환하므로
	// 커스텀 ErrorMapper 없이 wrapper 의 default *HTTPError 해석으로 충분.
	typed.RegisterNoBody(g, "POST", "/revert", tag,
		handleRevert,
		typed.WithSummary("HandleRevert"),
		typed.WithRequestBody(reflect.TypeOf(RevertRequest{})),
	)
}
