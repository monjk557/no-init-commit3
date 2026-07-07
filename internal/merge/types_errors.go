package merge

import (
	"errors"
	"fmt"
)

// ErrConflict 는 merge 충돌 카테고리 sentinel. ConcurrentUpdateError.Unwrap()
// 이 이를 참조하므로 유지한다.
//
// (Issue-5360) 이전에 존재하던 error 택소노미 — ToAPIError / APIError /
// NotFoundError / ConflictError / ValidationError / LockedError / InternalError
// / EnvironmentConflictError / ComplexConflictError 및 그 sentinel/생성자 —
// 는 production call site 가 전무한 dead code 여서 제거되었다. 실제 에러 경로는
// routes.go 의 responseErr + api/typed 의 HTTPError(code/params) 로 통일된다.
var ErrConflict = errors.New("merge conflict")

// ConcurrentUpdateError 는 동시 ref 업데이트 충돌을 나타내는 typed error 다.
// HandleMerge 가 performMerge 결과에서 errors.As 로 감지하여 409
// CONCURRENT_UPDATE 로 매핑한다 (handler.go).
type ConcurrentUpdateError struct {
	Resource string
	Message  string
}

func (e ConcurrentUpdateError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("concurrent update on resource: %s", e.Resource)
}

func (e ConcurrentUpdateError) Unwrap() error {
	return ErrConflict
}
