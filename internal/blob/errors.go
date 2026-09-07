package blob

import "errors"

// blob 조회 실패 카테고리. err.Error() 문구 매칭 대신 errors.Is 로 판별해,
// 메시지 문구가 바뀌어도 classifyBlobError 의 카탈로그 code 매핑이 깨지지
// 않게 한다.
var (
	ErrBlobNotFound    = errors.New("blob not found")
	ErrBlobIsDirectory = errors.New("path is a directory")
)
