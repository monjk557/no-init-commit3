package branches

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/api/typed"
)

// invokeTyped 는 typed handler 를 직접 호출하고 결과/에러를 gin.Context 에
// 쓴다. typed.RegisterX wrapper 의 default 매핑 mirror.
func invokeTyped[Resp any](c *gin.Context, fn func(*gin.Context) (Resp, error)) {
	resp, err := fn(c)
	if err != nil {
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

// adapt 는 typed handler 를 gin.HandlerFunc 로 변환한다.
func adapt[Resp any](fn func(*gin.Context) (Resp, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		invokeTyped(c, fn)
	}
}

// adaptQuery 는 RegisterGetWithQuery 시그니처 (Req struct 를 받는 typed
// handler) 를 gin.HandlerFunc 로 변환한다. wrapper 의 c.ShouldBindQuery
// 단계를 mirror.
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
