//go:build gogit

package merge

import (
	"github.com/gin-gonic/gin"
)

// adapt 는 typed handler 를 gin.HandlerFunc 로 변환한다 — 테스트에서
// router.METHOD(path, adapt(handler.HandleX)) 형태로 사용.
//
// 호출자가 모두 gogit 태그 테스트 (status_codes_test.go 등) 라서 이 헬퍼도
// 같은 태그 아래 둔다. 기본 빌드에서는 컴파일되지 않으므로 unused 대상이
// 되지 않는다.
func adapt[Resp any](fn func(*gin.Context) (Resp, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		invokeTyped(c, fn)
	}
}
