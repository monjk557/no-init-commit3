package repositories

import "errors"

// Ref Resolution Errors
// 이 에러들은 HTTP status code와 error.code 매핑에 사용됩니다.
// 문서: docs/test_guides/test_cases.md - 0.1 Ref Input Contract, 13.3 Error Contract

var (
	// ErrInvalidRef: ref 문법 오류 (빈 문자열, 공백, hex 아님 등)
	// HTTP 400 + error.code: INVALID_REF
	ErrInvalidRef = errors.New("invalid ref format")

	// ErrUnsupportedRef: 지원하지 않는 ref 형식 (refs/heads/*, HEAD, main^, main:path 등)
	// HTTP 400 + error.code: UNSUPPORTED_REF
	ErrUnsupportedRef = errors.New("unsupported ref format")

	// ErrAmbiguousRef: branch/tag 이름 충돌
	// HTTP 400 + error.code: AMBIGUOUS_REF
	ErrAmbiguousRef = errors.New("ambiguous ref")

	// ErrObjectNotFound: commit SHA가 존재하지 않음
	// HTTP 404 + error.code: OBJECT_NOT_FOUND
	ErrObjectNotFound = errors.New("object not found")

	// ErrRefNotFound: branch/tag가 존재하지 않음
	// HTTP 404 + error.code: REF_NOT_FOUND
	// 참고: go-git의 plumbing.ErrReferenceNotFound도 같은 의미
	ErrRefNotFound = errors.New("ref not found")
)

// RefErrorCode returns the machine-readable error code for the given error
func RefErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrInvalidRef):
		return "INVALID_REF"
	case errors.Is(err, ErrUnsupportedRef):
		return "UNSUPPORTED_REF"
	case errors.Is(err, ErrAmbiguousRef):
		return "AMBIGUOUS_REF"
	case errors.Is(err, ErrObjectNotFound):
		return "OBJECT_NOT_FOUND"
	case errors.Is(err, ErrRefNotFound):
		return "REF_NOT_FOUND"
	default:
		return "INTERNAL_ERROR"
	}
}
