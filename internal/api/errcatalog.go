package api

import (
	"net/http"

	"github.com/collabops/git-executor/internal/api/typed"
)

// errcatalog.go 는 git-executor 가 낼 수 있는 모든 에러 코드의 단일 진실
// 원천이다. code 를 primary key 로 하여 HTTP status·기본 메시지·retryable 을
// 파생한다 — 핸들러는 status 를 직접 고르지 않고 code 만 고른다 (Issue-5577).
//
// 불변식: **어떤 code 도 HTTP 422 로 매핑하지 않는다.** 422(Unprocessable
// Entity)는 "요청 내용이 의미상 틀림"=클라이언트 잘못을 뜻하는데, executor 의
// git 엔드포인트는 대부분 body 없는 GET 이라 실제로는 서버측 상태(없음/손상/
// 빈 repo)를 오분류하는 데 쓰였다. 없음→404, 빈 repo→200, 손상/예기치 못한
// 실패→500 으로 분해한다. TestCatalogHasNo422 가 이를 CI 에서 강제한다.
//
// 패키지 배치: 카탈로그는 api 에 두고 NewCoded 가 *typed.HTTPError 를
// 반환한다 (api→typed). typed 는 api 를 import 하지 않으므로 cycle 이 없다.

type catalogEntry struct {
	Status    int
	Message   string
	Retryable bool
}

// errorCatalog 은 code → HTTP 표현 매핑. 모든 ErrorCodeEnum 값이 여기 존재해야
// 하며(TestCatalogCoversAllCodes), 어느 것도 422 가 아니어야 한다.
var errorCatalog = map[ErrorCodeEnum]catalogEntry{
	ErrCodeBranchAlreadyExists:     {http.StatusConflict, "branch already exists", false},
	ErrCodeCommitNotFound:          {http.StatusNotFound, "commit not found", false},
	ErrCodeConcurrentLimitExceeded: {http.StatusTooManyRequests, "concurrent request limit exceeded", true},
	ErrCodeConcurrentUpdate:        {http.StatusConflict, "concurrent update conflict", true},
	ErrCodeConflict:                {http.StatusConflict, "conflict", false},
	ErrCodeFileNotFound:            {http.StatusNotFound, "file not found", false},
	ErrCodeInternalError:           {http.StatusInternalServerError, "internal error", false},
	ErrCodeInvalidInput:            {http.StatusBadRequest, "invalid input", false},
	ErrCodeInvalidParent:           {http.StatusBadRequest, "invalid parent", false},
	ErrCodeInvalidRequestBody:      {http.StatusBadRequest, "invalid request body", false},
	ErrCodeInvalidURLParameters:    {http.StatusBadRequest, "invalid URL parameters", false},
	ErrCodeIPBanned:                {http.StatusTooManyRequests, "IP banned", false},
	ErrCodeLocked:                  {http.StatusLocked, "resource locked", true},
	ErrCodeMergeConflict:           {http.StatusConflict, "merge conflict", false},
	ErrCodeNotFound:                {http.StatusNotFound, "not found", false},
	ErrCodeObjectNotFound:          {http.StatusNotFound, "object not found", false},
	ErrCodeOverloaded:              {http.StatusServiceUnavailable, "server overloaded", true},
	ErrCodePanic:                   {http.StatusInternalServerError, "internal error", false},
	ErrCodeRateLimitExceeded:       {http.StatusTooManyRequests, "rate limit exceeded", true},
	ErrCodeRefNotFound:             {http.StatusNotFound, "reference not found", false},
	ErrCodeRepositoryCorrupted:     {http.StatusInternalServerError, "repository corrupted", false},
	ErrCodeRepositoryEmpty:         {http.StatusOK, "repository is empty", false},
	ErrCodeRepositoryNotFound:      {http.StatusNotFound, "repository not found", false},
	ErrCodeRequestTooLarge:         {http.StatusRequestEntityTooLarge, "request too large", false},
	ErrCodeRevertConflict:          {http.StatusConflict, "revert conflict", false},
	ErrCodeTimeout:                 {http.StatusTooManyRequests, "operation timed out, retry shortly", true},
	ErrCodeUnknown:                 {http.StatusInternalServerError, "unknown error", false},
	ErrCodeUnknownError:            {http.StatusInternalServerError, "unknown error", false},
}

// lookupCatalog 은 code 의 카탈로그 엔트리를 반환한다. 미등록 code 는
// INTERNAL_ERROR(500) 로 안전 강등한다 — 절대 panic 하지 않는다.
func lookupCatalog(code ErrorCodeEnum) catalogEntry {
	if e, ok := errorCatalog[code]; ok {
		return e
	}
	return catalogEntry{Status: http.StatusInternalServerError, Message: "internal error", Retryable: false}
}

// NewCoded 는 카탈로그 code 로 *typed.HTTPError 를 만든다 — status·기본
// 메시지는 카탈로그에서 파생하고 code 를 body 에 싣는다. overrideMsg 를 주면
// 사용자용 메시지만 교체한다(status/code 는 카탈로그 유지).
//
// 이것이 핸들러의 표준 에러 생성 경로다 — status 를 직접 쓰는
// status 를 직접 지정하는 대신 이 함수로 status/code 일관성을 보장한다.
func NewCoded(code ErrorCodeEnum, overrideMsg ...string) *typed.HTTPError {
	e := lookupCatalog(code)
	msg := e.Message
	if len(overrideMsg) > 0 && overrideMsg[0] != "" {
		msg = overrideMsg[0]
	}
	return typed.NewHTTPErrorWithCode(e.Status, msg, string(code), nil)
}

// NewCodedWithParams 는 NewCoded 에 orchestrator 보간용 params 를 더한다
// (예: MERGE_CONFLICT 의 source_ref/target_ref/conflict_count).
func NewCodedWithParams(code ErrorCodeEnum, message string, params map[string]string) *typed.HTTPError {
	e := lookupCatalog(code)
	if message == "" {
		message = e.Message
	}
	return typed.NewHTTPErrorWithCode(e.Status, message, string(code), params)
}

// StatusForCode 는 code 의 표준 HTTP status 를 노출한다 (ErrorMapper 등에서 사용).
func StatusForCode(code ErrorCodeEnum) int {
	return lookupCatalog(code).Status
}
