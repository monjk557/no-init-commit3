// tags 도메인의 typed 라우트 등록.
//
// server.go (runtime) 와 specs/spec_routes.go (spec 생성) 가 모두 이 함수를
// 호출하여 단일 source of truth 보장.
package tags

import (
	"errors"
	"net/http"
	"reflect"
	"strings"

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
	// GetTags 실패는 details 첨부 응답.
	typed.RegisterGetWithQuery(g, "/tags", tag,
		getTags,
		typed.WithSummary("HandleGetTags"),
		typed.WithErrorMapper(getTagsErrorMapper),
	)

	// POST /tags: lenient bind + 다중 status 매핑 (409/404/400, 모두 details
	// 첨부).
	typed.RegisterNoBody(g, "POST", "/tags", tag,
		createTag,
		typed.WithSummary("HandleCreateTag"),
		typed.WithRequestBody(reflect.TypeOf(CreateTagRequest{})),
		typed.WithErrorMapper(createTagErrorMapper),
	)

	// POST /tags/bulk-delete: lenient bind. 부분 실패는 정상 응답이라 mapper
	// 불필요 — *typed.HTTPError 만 발생하므로 default 매핑으로 충분.
	typed.RegisterNoBody(g, "POST", "/tags/bulk-delete", tag,
		bulkDelete,
		typed.WithSummary("HandleBulkDeleteTags"),
		typed.WithRequestBody(reflect.TypeOf(BulkDeleteTagsRequest{})),
	)

	// DELETE /tags/*tag: wildcard. 다중 status 매핑 (404 not found, 400
	// default — 모두 details 첨부).
	typed.RegisterNoBody(g, "DELETE", "/tags/*tag", tag,
		deleteTag,
		typed.WithSummary("HandleDeleteTag"),
		typed.WithErrorMapper(deleteTagErrorMapper),
	)
}

// getTagsErrorMapper 는 GET /tags 의 raw 에러를 details 첨부 400 응답으로
// 매핑한다. *typed.HTTPError 는 우선 단순 {success, error} 응답.
func getTagsErrorMapper(err error) (int, any) {
	var he *typed.HTTPError
	if errors.As(err, &he) {
		return he.Status, gin.H{"success": false, "error": he.Message}
	}
	return http.StatusBadRequest, gin.H{
		"success": false,
		"error":   "Failed to get tags",
		"details": err.Error(),
	}
}

// createTagErrorMapper 는 POST /tags 의 다중 status 를 매핑한다.
//
//   - *typed.HTTPError → {success, error} (단순 4xx)
//   - *invalidBindErr → 400 + details (bind 실패)
//   - ErrTagAlreadyExists → 409 + details
//   - substring "not found"/"commit not found" → 404 + details
//   - default → 400 + details
func createTagErrorMapper(err error) (int, any) {
	var he *typed.HTTPError
	if errors.As(err, &he) {
		return he.Status, gin.H{"success": false, "error": he.Message}
	}

	var ibe *invalidBindErr
	if errors.As(err, &ibe) {
		return http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "Invalid request body",
			"details": ibe.Error(),
		}
	}

	if errors.Is(err, ErrTagAlreadyExists) {
		return http.StatusConflict, gin.H{
			"success": false,
			"error":   "Failed to create tag",
			"details": "tag already exists",
		}
	}

	errMsg := err.Error()
	if strings.Contains(errMsg, "not found") || strings.Contains(errMsg, "commit not found") {
		return http.StatusNotFound, gin.H{
			"success": false,
			"error":   "Failed to create tag",
			"details": errMsg,
		}
	}

	return http.StatusBadRequest, gin.H{
		"success": false,
		"error":   "Failed to create tag",
		"details": errMsg,
	}
}

// deleteTagErrorMapper 는 DELETE /tags/*tag 의 status 를 substring 으로
// 분류한다 (legacy 와 동일).
func deleteTagErrorMapper(err error) (int, any) {
	var he *typed.HTTPError
	if errors.As(err, &he) {
		return he.Status, gin.H{"success": false, "error": he.Message}
	}

	errMsg := err.Error()
	status := http.StatusBadRequest
	if strings.Contains(errMsg, "not found") {
		status = http.StatusNotFound
	}
	return status, gin.H{
		"success": false,
		"error":   "Failed to delete tag",
		"details": errMsg,
	}
}
