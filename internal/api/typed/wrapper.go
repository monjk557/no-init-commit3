// Package typed 의 runtime wrapper helper.
//
// Register* 함수 본문에서 호출되는 작은 도우미들. 외부 동작을 기존 handler
// 들과 byte-equivalent 로 유지하기 위해 응답 형태 (`gin.H{"success": false,
// "error": "..."}`) 를 그대로 보존한다.
package typed

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
)

// HTTPError 는 handler 가 명시적 HTTP status + 사용자용 메시지를 함께 반환할
// 때 사용한다. Endpoint.ErrorMapper 가 미설정이어도 typed wrapper 의 default
// 매핑이 이 타입을 인식하여 (status, gin.H{success:false,error:msg}) 로
// 변환한다.
//
// 기존 handler 들의 'c.JSON(status, gin.H{"success":false,"error":<msg>})'
// 패턴을 typed signature 로 옮길 때 가장 간결한 도구.
type HTTPError struct {
	Status  int
	Message string
	// Code 는 언어 중립 machine error code (예: "REF_NOT_FOUND"). 비어 있으면
	// 응답 body 에 `code` 필드가 생략된다.
	Code string
	// Params 는 orchestrator 가 카탈로그 메시지를 보간할 때 쓰는 key-value.
	// 비어 있으면 응답 body 에 `params` 필드가 생략된다.
	Params map[string]string
}

// Error 는 error 인터페이스 만족용. wrapper 가 사용자에게 노출하는 텍스트.
func (e *HTTPError) Error() string {
	return e.Message
}

// NewHTTPError 는 status + message 페어로 *HTTPError 를 만든다.
func NewHTTPError(status int, message string) *HTTPError {
	return &HTTPError{Status: status, Message: message}
}

// NewHTTPErrorWithCode 는 status + message 에 더해 언어 중립 machine code 와
// 보간용 params 를 함께 실은 *HTTPError 를 만든다. wrapper 가 응답 body 에
// `code` (+ 비어 있지 않으면 `params`) 를 flat 하게 추가한다 — git-executor 는
// localization 을 하지 않고 orchestrator 가 `GIT_<code>` 로 카탈로그 조회.
func NewHTTPErrorWithCode(status int, message, code string, params map[string]string) *HTTPError {
	return &HTTPError{Status: status, Message: message, Code: code, Params: params}
}

// writeError 는 단순 에러 응답을 작성한다. 기존 handler 들의
// `c.JSON(status, gin.H{"success": false, "error": <msg>})` 형태와 byte
// equivalent — 회귀 보호를 위해 키 순서·개수 모두 유지.
func writeError(c *gin.Context, status int, msg string) {
	writeErrorWithCode(c, status, msg, "", nil)
}

// writeErrorWithCode 는 error envelope 에 optional machine code + params 를
// 실어 응답한다. code 가 빈 문자열이고 params 가 비어 있으면 legacy 와 byte
// equivalent (`{success, error}` 2 키) 를 유지하고, 값이 있을 때만 flat 하게
// `code`/`params` 키를 추가한다.
func writeErrorWithCode(c *gin.Context, status int, msg, code string, params map[string]string) {
	body := gin.H{"success": false, "error": msg}
	if code != "" {
		body["code"] = code
	}
	if len(params) > 0 {
		body["params"] = params
	}
	c.JSON(status, body)
}

// mapAndWriteError 는 handler 가 반환한 error 를 응답으로 변환한다.
//
// 우선순위:
//  1. Endpoint.ErrorMapper 가 설정되어 있으면 그 결과 사용.
//  2. err 가 *HTTPError 를 wrap 하고 있으면 그 status + message 사용.
//  3. 그 외에는 500 + err.Error() 로 fallback (writeError).
func mapAndWriteError(c *gin.Context, err error, ep Endpoint) {
	if ep.ErrorMapper != nil {
		status, body := ep.ErrorMapper(err)
		c.JSON(status, body)
		return
	}
	var he *HTTPError
	if errors.As(err, &he) {
		writeErrorWithCode(c, he.Status, he.Message, he.Code, he.Params)
		return
	}
	writeError(c, http.StatusInternalServerError, err.Error())
}

// deriveStatus 는 정상 응답에 사용할 HTTP status 를 반환한다. SuccessStatus
// 가 명시되지 않으면 (0) 200 OK.
func deriveStatus(ep Endpoint) int {
	if ep.SuccessStatus == 0 {
		return http.StatusOK
	}
	return ep.SuccessStatus
}

// basePather 는 gin.RouterGroup 와 gin.Engine 이 만족하는 좁은 인터페이스 —
// type assertion 으로 BasePath 를 추출한다.
type basePather interface {
	BasePath() string
}

// absolutePath 는 gin RouterGroup 의 BasePath 와 path 를 합쳐 절대 경로를
// 반환한다. router 가 BasePath() 를 노출하지 않으면 path 그대로 반환.
//
// 입력은 gin path 형식 (`:owner`, `*branch`) 그대로 — OpenAPI 형식 (`{}`)
// 변환은 ginToOpenAPIPath 가 별도로 담당.
//
// 빈 path ("" 또는 "/") 는 group base path 자체에 등록하는 의미로
// 해석하여 base 만 반환한다 (예: `POST("", h)` 는 group 의 base path 에 직접
// 등록 → spec path 는 group base path 와 동일, trailing slash 없음).
func absolutePath(r gin.IRouter, path string) string {
	bp, ok := r.(basePather)
	if !ok {
		return path
	}
	base := strings.TrimRight(bp.BasePath(), "/")
	if path == "" || path == "/" {
		if base == "" {
			return "/"
		}
		return base
	}
	if base == "" {
		// Engine 의 BasePath = "/" → 그대로 path 만 반환.
		if !strings.HasPrefix(path, "/") {
			return "/" + path
		}
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}

var (
	colonParamRE = regexp.MustCompile(`:([a-zA-Z0-9_]+)`)
	starParamRE  = regexp.MustCompile(`\*([a-zA-Z0-9_]+)`)
)

// ginToOpenAPIPath 는 gin path syntax 를 OpenAPI path 형식으로 변환한다.
//
//	":owner"  → "{owner}"
//	"*branch" → "{branch}"  (catch-all wildcard 는 description 에서 별도
//	                          명시; OpenAPI 자체에는 일반 path param 으로 노출)
//
// `specs/generate.go:routeToSpecPath` 와 의미 동등성 보장 — Phase 1 에서
// 이 함수가 그 역할을 흡수한다.
func ginToOpenAPIPath(ginPath string) string {
	out := starParamRE.ReplaceAllString(ginPath, "{$1}")
	out = colonParamRE.ReplaceAllString(out, "{$1}")
	return out
}
