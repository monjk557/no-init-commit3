// repositories 도메인의 typed 라우트 등록.
//
// server.go (runtime) 와 specs/spec_routes.go (spec 생성) 양쪽이 호출하여
// 단일 source of truth 보장.
package repositories

import (
	"reflect"

	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/api/typed"
)

const tag = "Repository Management"

// RegisterRoutes 는 repository 도메인의 모든 endpoint 를 등록한다. h 가 nil
// 이면 spec-only 모드 (등록만, 호출 없음).
func RegisterRoutes(g gin.IRouter, h *RepositoryCreationHandler) {
	var createRepo func(*gin.Context) (CreateRepositoryResponse, error)
	if h != nil {
		createRepo = h.HandleCreateRepository
	}

	// POST /repos/{owner}/{repo}: handler 본문에서 lenient JSON bind 를 직접
	// 수행하기 때문에 RegisterNoBody + WithRequestBody 조합. spec 에는 정상
	// 적으로 requestBody schema 가 노출된다.
	typed.RegisterNoBody(g, "POST", "", tag,
		createRepo,
		typed.WithSummary("HandleCreateRepository"),
		typed.WithStatus(201),
		typed.WithRequestBody(reflect.TypeOf(CreateRepositoryRequest{})),
	)

	// PUT /repos/{owner}/{repo}/head: default branch 지정. body 는 handler 본문
	// 에서 직접 bind 하므로 (친화 에러 메시지 보존) RegisterNoBody + WithRequestBody.
	var setHead func(*gin.Context) (SetDefaultBranchResponse, error)
	if h != nil {
		setHead = h.HandleSetDefaultBranch
	}
	typed.RegisterNoBody(g, "PUT", "/head", tag,
		setHead,
		typed.WithSummary("HandleSetDefaultBranch"),
		typed.WithRequestBody(reflect.TypeOf(SetDefaultBranchRequest{})),
	)
}
