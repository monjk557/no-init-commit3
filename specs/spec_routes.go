// setupSpecRoutes 는 typed handler 로 마이그레이션된 도메인들의 RegisterRoutes
// 를 spec-only 모드로 호출하여 registry 를 채운다.
//
// spec-only 모드: gin.Engine 은 만들어지지만 절대 ServeHTTP 가 호출되지
// 않으므로 도메인 RegisterRoutes 가 nil handler 를 넘겨도 안전하다 (gin 라우트
// 는 등록되지만 invoke 가 일어나지 않음).
//
// 도메인이 마이그레이션될 때마다 import + Register 호출을 추가한다 (Phase 2).
package main

import (
	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/api/typed"
	"github.com/collabops/git-executor/internal/blob"
	"github.com/collabops/git-executor/internal/branches"
	"github.com/collabops/git-executor/internal/commits"
	repocontext "github.com/collabops/git-executor/internal/context"
	"github.com/collabops/git-executor/internal/diffs"
	"github.com/collabops/git-executor/internal/fork"
	"github.com/collabops/git-executor/internal/langstats"
	"github.com/collabops/git-executor/internal/merge"
	"github.com/collabops/git-executor/internal/overview"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/collabops/git-executor/internal/revert"
	"github.com/collabops/git-executor/internal/state"
	"github.com/collabops/git-executor/internal/tags"
	"github.com/collabops/git-executor/internal/tree"
)

func setupSpecRoutes(reg *typed.Registry) {
	if reg == nil {
		return
	}
	restore := typed.SetDefaultRegistry(reg)
	defer restore()

	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	apiGroup := engine.Group("/repos/:owner/:repo")

	// 마이그레이션된 도메인 (Phase 2 진행):
	repositories.RegisterRoutes(apiGroup, nil)
	state.RegisterRoutes(apiGroup, nil)
	fork.RegisterRoutes(apiGroup, nil)
	langstats.RegisterRoutes(apiGroup, nil)
	repocontext.RegisterRoutes(apiGroup, nil)
	revert.RegisterRoutes(apiGroup, nil)
	tags.RegisterRoutes(apiGroup, nil)
	tree.RegisterRoutes(apiGroup, nil)
	commits.RegisterRoutes(apiGroup, nil)
	merge.RegisterRoutes(apiGroup, nil, nil)
	overview.RegisterRoutes(apiGroup, nil)
	branches.RegisterRoutes(apiGroup, nil)
	diffs.RegisterRoutes(apiGroup, nil)
	blob.RegisterRoutes(apiGroup, nil)

	// 아직 legacy switch 경로:
	// lfs
	_ = engine
}
