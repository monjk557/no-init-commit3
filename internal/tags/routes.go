// tags 도메인의 typed 라우트 등록.
//
// server.go (runtime) 와 specs/spec_routes.go (spec 생성) 가 모두 이 함수를
// 호출하여 단일 source of truth 보장.
package tags

import (
	"reflect"

	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/api/typed"
)

const tag = "Tags"

// RegisterRoutes 는 tags 도메인의 모든 endpoint 를 등록한다. h 가 nil 이면
// spec-only 모드 (등록만, 호출 없음).
func RegisterRoutes(g gin.IRouter, h *TagsHandler) {
	var (
		getTags    func(*gin.Context, GetTagsRequest) (TagsResponse, error)
		createTag  func(*gin.Context) (CreateTagResponse, error)
		deleteTag  func(*gin.Context) (DeleteTagResponse, error)
		bulkDelete func(*gin.Context) (BulkDeleteTagsResponse, error)
	)
	if h != nil {
		getTags = h.HandleGetTags
		createTag = h.HandleCreateTag
		deleteTag = h.HandleDeleteTag
		bulkDelete = h.HandleBulkDeleteTags
	}

	// GET /tags: limit/skip/sort 를 GetTagsRequest query struct 로 바인딩
	// (브랜치 list 와 동일 패턴). spec generator 가 query parameter 로 expand.
	// classifyGetTagsError 가 카탈로그 code 를 가진 *typed.HTTPError 를 이미
	// 반환하므로 커스텀 ErrorMapper 불필요.
	typed.RegisterGetWithQuery(g, "/tags", tag,
		getTags,
		typed.WithSummary("HandleGetTags"),
	)

	// POST /tags: lenient bind. classifyCreateTagError 가 다중 status 를
	// 카탈로그 code 로 이미 변환하므로 커스텀 ErrorMapper 불필요.
	typed.RegisterNoBody(g, "POST", "/tags", tag,
		createTag,
		typed.WithSummary("HandleCreateTag"),
		typed.WithRequestBody(reflect.TypeOf(CreateTagRequest{})),
	)

	// POST /tags/bulk-delete: lenient bind. 부분 실패는 정상 응답이라 mapper
	// 불필요 — *typed.HTTPError 만 발생하므로 default 매핑으로 충분.
	typed.RegisterNoBody(g, "POST", "/tags/bulk-delete", tag,
		bulkDelete,
		typed.WithSummary("HandleBulkDeleteTags"),
		typed.WithRequestBody(reflect.TypeOf(BulkDeleteTagsRequest{})),
	)

	// DELETE /tags/*tag: wildcard. classifyDeleteTagError 가 다중 status 를
	// 카탈로그 code 로 이미 변환하므로 커스텀 ErrorMapper 불필요.
	typed.RegisterNoBody(g, "DELETE", "/tags/*tag", tag,
		deleteTag,
		typed.WithSummary("HandleDeleteTag"),
	)
}
