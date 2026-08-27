package api

// 이 파일은 OpenAPI spec generator(specs/generate.go) 가 스캔하는 schema
// source 다 (schemaFilePaths() 목록에 등록됨). 런타임 로직은 두지 않는다 —
// error envelope 와 error.code enum 의 single source of truth 만 정의한다.

// ErrorCodeEnum 은 git-executor 가 HTTP 에러 응답 body 의 flat `code` 필드로
// 실제 내려보내는 머신 판독용 에러 코드 값의 집합이다.
//
// 이 alias + 아래 const 블록은 spec generator 가 `components/schemas/
// ErrorCodeEnum` (type: string enum) 으로 emit 하는 SoT 다. 접미사가 `Enum`
// 이므로 generator 가 inline 이 아니라 named component 로 승격하고, error
// envelope 의 `code` 필드가 이를 `$ref` 로 참조한다 (Doc-500: error code enum
// sync — orchestrator 가 이 enum 을 카탈로그 sync 의 SoT 로 소비).
//
// 규약 (Doc-500):
//   - 값은 raw 그대로. prefix(PIPER_/APISVC_ 등) 나 이름 규칙을 붙이지 않는다.
//     namespace prefix 는 orchestrator 가 카탈로그 조회 시 내부에서만 부여.
//   - 새 error.code 를 응답에 추가하면 반드시 여기에도 추가한다. orchestrator
//     의 카탈로그 완전성 guard 가 spec 에 없는 코드를 CI 에서 차단한다.
//
// 값은 알파벳 순으로 유지한다 (generator 가 source 순서를 그대로 enum 순서로
// emit 하므로 결정성 보장).
type ErrorCodeEnum string

const (
	// ErrCodeBranchAlreadyExists: 생성하려는 브랜치가 이미 존재 (revert 409).
	ErrCodeBranchAlreadyExists ErrorCodeEnum = "BRANCH_ALREADY_EXISTS"
	// ErrCodeCommitNotFound: 대상 commit SHA 를 찾을 수 없음 (revert 404).
	ErrCodeCommitNotFound ErrorCodeEnum = "COMMIT_NOT_FOUND"
	// ErrCodeConcurrentLimitExceeded: 동시 요청 한도 초과 (rate limiter 429).
	ErrCodeConcurrentLimitExceeded ErrorCodeEnum = "CONCURRENT_LIMIT_EXCEEDED"
	// ErrCodeConcurrentUpdate: 동시 ref 업데이트 충돌 (merge 409).
	ErrCodeConcurrentUpdate ErrorCodeEnum = "CONCURRENT_UPDATE"
	// ErrCodeConflict: merge 충돌 — 수동 해소 필요 (merge 409).
	ErrCodeConflict ErrorCodeEnum = "CONFLICT"
	// ErrCodeFileNotFound: 대상 파일을 찾을 수 없음 (diffs 404).
	ErrCodeFileNotFound ErrorCodeEnum = "FILE_NOT_FOUND"
	// ErrCodeForbidden: 접근 권한 없음 (blob 403).
	ErrCodeForbidden ErrorCodeEnum = "FORBIDDEN"
	// ErrCodeInternalError: 내부 서버 에러 (merge 500).
	ErrCodeInternalError ErrorCodeEnum = "INTERNAL_ERROR"
	// ErrCodeInvalidInput: 요청 입력 검증 실패 (merge 400).
	ErrCodeInvalidInput ErrorCodeEnum = "INVALID_INPUT"
	// ErrCodeInvalidParent: 잘못된 parent 지정 (revert 400).
	ErrCodeInvalidParent ErrorCodeEnum = "INVALID_PARENT"
	// ErrCodeInvalidRequestBody: 요청 body 파싱/검증 실패 (validation 400).
	ErrCodeInvalidRequestBody ErrorCodeEnum = "INVALID_REQUEST_BODY"
	// ErrCodeInvalidURLParameters: URL 파라미터 검증 실패 (validation 400).
	ErrCodeInvalidURLParameters ErrorCodeEnum = "INVALID_URL_PARAMETERS"
	// ErrCodeIPBanned: 차단된 IP (rate limiter 429).
	ErrCodeIPBanned ErrorCodeEnum = "IP_BANNED"
	// ErrCodeLocked: 리소스 잠김 (merge 423).
	ErrCodeLocked ErrorCodeEnum = "LOCKED"
	// ErrCodeMergeConflict: merge 결과 파일 충돌 — 수동 해소 필요 (merge 409).
	// CONFLICT 와 구분: MERGE_CONFLICT 는 source→target 병합 시 실제 파일 단위
	// 충돌이 감지된 경우로, params(source_ref/target_ref/conflict_count) 를 함께
	// 실어 orchestrator 가 GIT_MERGE_CONFLICT 카탈로그로 보간하도록 한다.
	ErrCodeMergeConflict ErrorCodeEnum = "MERGE_CONFLICT"
	// ErrCodeNotFound: 리소스를 찾을 수 없음 (merge 404).
	ErrCodeNotFound ErrorCodeEnum = "NOT_FOUND"
	// ErrCodeObjectNotFound: git object(commit SHA) 를 찾을 수 없음 (diffs 404).
	ErrCodeObjectNotFound ErrorCodeEnum = "OBJECT_NOT_FOUND"
	// ErrCodeOverloaded: 서버 과부하 — 요청 거부 (server 503).
	ErrCodeOverloaded ErrorCodeEnum = "OVERLOADED"
	// ErrCodePanic: 패닉 복구 후 변환된 에러 (500).
	ErrCodePanic ErrorCodeEnum = "PANIC"
	// ErrCodeRangeNotSatisfiable: Range 헤더가 유효하지 않음 (blob 416).
	ErrCodeRangeNotSatisfiable ErrorCodeEnum = "RANGE_NOT_SATISFIABLE"
	// ErrCodeRateLimitExceeded: 요청 빈도 한도 초과 (rate limiter 429).
	ErrCodeRateLimitExceeded ErrorCodeEnum = "RATE_LIMIT_EXCEEDED"
	// ErrCodeRefNotFound: branch/tag/ref 를 찾을 수 없음 (merge/diffs 404).
	ErrCodeRefNotFound ErrorCodeEnum = "REF_NOT_FOUND"
	// ErrCodeRepositoryCorrupted: repo 는 디스크에 존재하나 git 이 예기치 않게
	// 실패 — 진짜 손상/불변식 위반 (server 500). 과거 여러 핸들러가 "not a git
	// repository"(=없음) 와 손상을 뭉뚱그려 422 로 냈으나, 없음은 404
	// (REPOSITORY_NOT_FOUND), 손상만 이 코드로 분리한다 (Issue-5577).
	ErrCodeRepositoryCorrupted ErrorCodeEnum = "REPOSITORY_CORRUPTED"
	// ErrCodeRepositoryEmpty: repo 는 존재하나 커밋이 0 (unborn HEAD). 에러가
	// 아니라 유효 상태이므로 200 + empty/null marker 로 응답한다. 200 계열이라
	// 에러 envelope 에는 거의 쓰이지 않으나 카탈로그 완전성을 위해 등록.
	ErrCodeRepositoryEmpty ErrorCodeEnum = "REPOSITORY_EMPTY"
	// ErrCodeRepositoryNotFound: repo 의 on-disk git dir 가 없음 (never
	// materialized/orphan). NOT_FOUND 와 구분되는 저장소 부재 신호 (404).
	ErrCodeRepositoryNotFound ErrorCodeEnum = "REPOSITORY_NOT_FOUND"
	// ErrCodeRequestTooLarge: 요청 본문 크기 초과 (validation 413).
	ErrCodeRequestTooLarge ErrorCodeEnum = "REQUEST_TOO_LARGE"
	// ErrCodeRevertConflict: revert 적용 충돌 (revert 409).
	ErrCodeRevertConflict ErrorCodeEnum = "REVERT_CONFLICT"
	// ErrCodeTagAlreadyExists: 생성하려는 tag 가 이미 존재 (tags 409).
	ErrCodeTagAlreadyExists ErrorCodeEnum = "TAG_ALREADY_EXISTS"
	// ErrCodeTimeout: 작업이 deadline 초과 — 부하로 느려짐 (server 429, retry
	// 유도지만 BFF auto-retry 는 안 함). Issue-5071 retry-storm 회피.
	ErrCodeTimeout ErrorCodeEnum = "TIMEOUT"
	// ErrCodeUnauthorized: 인증 필요/실패 (blob 401).
	ErrCodeUnauthorized ErrorCodeEnum = "UNAUTHORIZED"
	// ErrCodeUnknown: 분류되지 않은 에러 (merge 500).
	ErrCodeUnknown ErrorCodeEnum = "UNKNOWN"
	// ErrCodeUnknownError: 래핑되지 않은 미상 에러 (internal 500).
	ErrCodeUnknownError ErrorCodeEnum = "UNKNOWN_ERROR"
)

// AllErrorCodes 는 정의된 모든 ErrorCodeEnum 값의 목록이다 (알파벳 순, const
// 선언과 동일). 에러 카탈로그 완전성 guard 와 spec generator 가 단일 원천으로
// 사용한다 — 새 코드를 추가하면 여기에도 반드시 등록한다.
var AllErrorCodes = []ErrorCodeEnum{
	ErrCodeBranchAlreadyExists,
	ErrCodeCommitNotFound,
	ErrCodeConcurrentLimitExceeded,
	ErrCodeConcurrentUpdate,
	ErrCodeConflict,
	ErrCodeFileNotFound,
	ErrCodeForbidden,
	ErrCodeInternalError,
	ErrCodeInvalidInput,
	ErrCodeInvalidParent,
	ErrCodeInvalidRequestBody,
	ErrCodeInvalidURLParameters,
	ErrCodeIPBanned,
	ErrCodeLocked,
	ErrCodeMergeConflict,
	ErrCodeNotFound,
	ErrCodeObjectNotFound,
	ErrCodeOverloaded,
	ErrCodePanic,
	ErrCodeRangeNotSatisfiable,
	ErrCodeRateLimitExceeded,
	ErrCodeRefNotFound,
	ErrCodeRepositoryCorrupted,
	ErrCodeRepositoryEmpty,
	ErrCodeRepositoryNotFound,
	ErrCodeRequestTooLarge,
	ErrCodeRevertConflict,
	ErrCodeTagAlreadyExists,
	ErrCodeTimeout,
	ErrCodeUnauthorized,
	ErrCodeUnknown,
	ErrCodeUnknownError,
}

// ErrorResponse 는 REST 엔드포인트가 반환하는 표준 에러 envelope 이다.
// `success: false` + 사람이 읽는 `error` 메시지 + 머신 판독용 `code`(선택) +
// 언어 중립 보간용 `params`(선택).
//
// generator 가 `components/schemas/ErrorResponse` 로 emit 하며, template 의
// responses(BadRequest/Unauthorized/NotFound/...) 가 이 schema 를 `$ref` 로
// 참조한다. `code` 는 omitempty — 모든 핸들러가 코드를 채우지는 않으므로
// (예: typed wrapper 기본 응답은 `{success, error}` 만) optional 로 둔다.
//
// wire shape 는 flat 이다: git-executor 는 localization 을 하지 않고, 언어
// 중립 machine code + params 만 실어 보낸다. upstream orchestrator(O2) 가
// `GIT_<code>` 를 i18n 카탈로그에서 조회하고 `params` 를 보간한다.
type ErrorResponse struct {
	// Success 는 항상 false (에러 응답).
	Success bool `json:"success"`
	// Error 는 사람이 읽을 수 있는 에러 메시지 (개발자용, 비현지화).
	Error string `json:"error"`
	// Code 는 머신 판독용 에러 코드. 일부 핸들러만 채운다.
	Code ErrorCodeEnum `json:"code,omitempty"`
	// Params 는 orchestrator 가 카탈로그 메시지를 보간할 때 쓰는 언어 중립
	// key-value 집합. 비어 있으면 생략된다 (예: MERGE_CONFLICT 는
	// source_ref/target_ref/conflict_count 를 싣는다).
	Params map[string]string `json:"params,omitempty"`
}
