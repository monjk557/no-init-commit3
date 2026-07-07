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
// 단순 {success, error} 가 아닌 conflicts/safety_warning 등 추가 필드를 가진
// 응답을 반환할 때 사용. mergeErrorMapper 가 status + body 를 그대로 응답에
// 사용하되, code/params 가 채워져 있으면 flat 하게 body 에 주입한다.
//
// code/params 는 언어 중립 machine error code + 보간용 key-value 로,
// orchestrator(O2) 가 `GIT_<code>` 카탈로그 조회 + params 보간에 사용한다.
// git-executor 자체는 localization 을 하지 않는다.
type responseErr struct {
	status int
	body   gin.H
	// code 는 언어 중립 machine error code (비면 body 에 미주입).
	code string
	// params 는 orchestrator 보간용 key-value (비면 body 에 미주입).
	params map[string]string
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
		return rich.status, mergeRichBody(rich)
	}
	var he *typed.HTTPError
	if errors.As(err, &he) {
		return he.Status, httpErrorBody(he)
	}
	return http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()}
}

// mergeRichBody 는 responseErr.body 에 flat code/params 를 주입한 새 gin.H 를
// 반환한다 (원본 body 를 mutate 하지 않음). code/params 가 비어 있으면 기존
// body 와 동일한 키 집합을 유지한다.
func mergeRichBody(rich *responseErr) gin.H {
	if rich.code == "" && len(rich.params) == 0 {
		return rich.body
	}
	body := make(gin.H, len(rich.body)+2)
	for k, v := range rich.body {
		body[k] = v
	}
	if rich.code != "" {
		body["code"] = rich.code
	}
	if len(rich.params) > 0 {
		body["params"] = rich.params
	}
	return body
}

// httpErrorBody 는 typed.HTTPError 를 flat error envelope 로 변환한다 — code/
// params 가 있으면 함께 싣는다 (writeErrorWithCode 와 동일 shape).
func httpErrorBody(he *typed.HTTPError) gin.H {
	body := gin.H{"success": false, "error": he.Message}
	if he.Code != "" {
		body["code"] = he.Code
	}
	if len(he.Params) > 0 {
		body["params"] = he.Params
	}
	return body
}

// mergeDiffErrorMapper 는 HandleMergeDiff 의 에러를 매핑한다. mergeErrorMapper
// 와 동일 로직 (DiffTooLarge 413, REF_NOT_FOUND 404 모두 responseErr 로
// 전달됨).
func mergeDiffErrorMapper(err error) (int, any) {
	return mergeErrorMapper(err)
}
