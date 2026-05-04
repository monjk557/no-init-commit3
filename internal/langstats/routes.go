// langstats 도메인의 typed 라우트 등록.
//
// server.go (runtime) 와 specs/spec_routes.go (spec 생성) 가 모두 이 함수를
// 호출하여 단일 source of truth 보장.
package langstats

import (
	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/api/typed"
)

const tag = "Repository Management"

// RegisterRoutes 는 langstats 도메인의 모든 endpoint 를 typed registry + gin
// 라우터에 등록한다. h 가 nil 이면 spec-only 모드 (등록만 수행, 실제 호출은
// 일어나지 않음 — spec 생성 시).
func RegisterRoutes(g gin.IRouter, h *Handler) {
	var getLanguages func(*gin.Context) (LanguageStatsResponse, error)
	if h != nil {
		getLanguages = h.HandleGetLanguageStats
	}

	typed.RegisterGet(g, "/languages", tag,
		getLanguages,
		typed.WithSummary("HandleGetLanguageStats"),
	)
}
