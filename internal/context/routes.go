// context 도메인의 typed 라우트 등록.
//
// server.go (runtime) 와 specs/spec_routes.go (spec 생성) 가 모두 이 함수를
// 호출하여 단일 source of truth 보장.
package context

import (
	"reflect"

	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/api/typed"
)

const tag = "Repository Management"

// RegisterRoutes 는 context 도메인의 모든 endpoint 를 등록한다. h 가 nil 이면
// spec-only 모드 (등록만, 호출 없음).
func RegisterRoutes(g gin.IRouter, h *ContextHandler) {
	var handleContext func(*gin.Context) (ContextResponse, error)
	if h != nil {
		handleContext = h.HandleContext
	}

	// POST /context: handler 본문에서 lenient JSON bind 직접 수행 →
	// RegisterNoBody + WithRequestBody. spec 에는 ContextRequest schema 노출.
	typed.RegisterNoBody(g, "POST", "/context", tag,
		handleContext,
		typed.WithSummary("HandleContext"),
		typed.WithRequestBody(reflect.TypeOf(ContextRequest{})),
	)
}
