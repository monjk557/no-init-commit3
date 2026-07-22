package merge

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/api/typed"
)

// invokeTyped 는 typed handler 를 직접 호출하고 결과/에러를 gin.Context 에
// 쓴다. typed.RegisterX wrapper 의 default 매핑 + ErrorMapper (mergeError /
// mergeDiffError) 를 mirror — multi-field 응답 (responseErr) 도 처리.
//
// 테스트가 router 우회 + 직접 handler 호출 패턴을 유지할 수 있게 해준다.
func invokeTyped[Resp any](c *gin.Context, fn func(*gin.Context) (Resp, error)) {
	resp, err := fn(c)
	if err != nil {
		var rich *responseErr
		if errors.As(err, &rich) {
			c.JSON(rich.status, rich.body)
			return
		}
		var he *typed.HTTPError
		if errors.As(err, &he) {
			c.JSON(he.Status, gin.H{"success": false, "error": he.Message})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, resp)
}

// adaptQuery 는 RegisterGetWithQuery 시그니처 (Req struct 를 받는 typed
// handler) 를 gin.HandlerFunc 로 변환한다. wrapper 의 c.ShouldBindQuery
// 단계를 mirror 하여 테스트가 router.X(path, adaptQuery(h.HandleY)) 패턴을
// 그대로 사용 가능 (HandleMergePreview 가 Req 형태로 전환된 후 도입 —
// Issue-2296).
func adaptQuery[Req any, Resp any](fn func(*gin.Context, Req) (Resp, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req Req
		if err := c.ShouldBindQuery(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
			return
		}
		invokeTyped(c, func(ctx *gin.Context) (Resp, error) {
			return fn(ctx, req)
		})
	}
}
