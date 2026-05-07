// merge 도메인의 typed 라우트 등록.
//
// server.go (runtime) 와 specs/spec_routes.go (spec 생성) 가 모두 이 함수를
// 호출하여 단일 source of truth 보장. merge 는 두 receiver 를 사용한다 —
// MergeOperationsHandler (POST /merges) 와 MergePreviewHandler (GET preview/diff).
package merge

import (
	"errors"
	"net/http"
	"reflect"

	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/api/typed"
	"github.com/collabops/git-executor/internal/diffs"
)

const tag = "Merge"

// responseErr 는 multi-field error response 를 위한 wrapper. handler 가
// 단순 {success, error} 가 아닌 conflicts/safety_warning/code 등 추가 필드
// 를 가진 응답을 반환할 때 사용. mergeErrorMapper 가 status + body 를 그대로
// 응답에 사용한다.
type responseErr struct {
	status int
	body   gin.H
}

func (e *responseErr) Error() string {
	if msg, ok := e.body["error"].(string); ok {
		return msg
	}
	return "response error"
}

// RegisterRoutes 는 merge 도메인의 모든 endpoint 를 등록한다. opsH 또는
// previewH 가 nil 이면 spec-only 모드 (등록만, 호출 없음).
func RegisterRoutes(g gin.IRouter, opsH *MergeOperationsHandler, previewH *MergePreviewHandler) {
	var (
		mergeOp   func(*gin.Context) (MergeResponse, error)
		mergePrev func(*gin.Context, MergePreviewRequest) (MergePreviewResponse, error)
		mergeDiff func(*gin.Context, MergeDiffRequest) (diffs.DiffResponse, error)
	)
	if opsH != nil {
		mergeOp = opsH.HandleMerge
	}
	if previewH != nil {
		mergePrev = previewH.HandleMergePreview
		mergeDiff = previewH.HandleMergeDiff
	}

	typed.RegisterNoBody(g, "POST", "/merges", tag,
		mergeOp,
		typed.WithSummary("HandleMerge"),
		typed.WithRequestBody(reflect.TypeOf(MergeRequest{})),
		typed.WithErrorMapper(mergeErrorMapper),
	)

	// query 파라미터는 RegisterGetWithQuery 로 등록 — wrapper 가 struct
	// field 의 form 태그를 reflect 하여 Endpoint.QueryParams 에 채우고, spec
	// generator 가 OpenAPI parameters (in: query) 로 expand 한다.
	// orchestrator auto-gen client 가 source_ref/target_ref 등 인자를 받도록
	// 노출하기 위함 (Issue-2296 / Issue-2317).
	typed.RegisterGetWithQuery(g, "/merges/preview", tag,
		mergePrev,
		typed.WithSummary("HandleMergePreview"),
	)

	typed.RegisterGetWithQuery(g, "/merges/diff", tag,
		mergeDiff,
		typed.WithSummary("HandleMergeDiff"),
		typed.WithErrorMapper(mergeDiffErrorMapper),
	)
}

// mergeErrorMapper 는 HandleMerge 의 에러를 매핑한다.
//   - *responseErr → 그대로 status + body (multi-field 응답)
//   - *typed.HTTPError → 단순 {success, error}
//   - 그 외 → 500 + err.Error()
func mergeErrorMapper(err error) (int, any) {
	var rich *responseErr
	if errors.As(err, &rich) {
		return rich.status, rich.body
	}
	var he *typed.HTTPError
	if errors.As(err, &he) {
		return he.Status, gin.H{"success": false, "error": he.Message}
	}
	return http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()}
}

// mergeDiffErrorMapper 는 HandleMergeDiff 의 에러를 매핑한다. mergeErrorMapper
// 와 동일 로직 (DiffTooLarge 413, REF_NOT_FOUND 404 모두 responseErr 로
// 전달됨).
func mergeDiffErrorMapper(err error) (int, any) {
	return mergeErrorMapper(err)
}
