// Git 프로토콜의 공통 유틸리티 함수들
package protocol

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"

	"github.com/gin-gonic/gin"
)

// SafeParam extracts a Gin path parameter and defensively URL-decodes it.
// This handles double-encoded URLs where the client sends percent-encoded
// Korean characters that remain encoded after Go's net/http single decode pass.
func SafeParam(c *gin.Context, key string) string {
	v := c.Param(key)
	if decoded, err := url.PathUnescape(v); err == nil {
		return decoded
	}
	return v
}

var (
	ginTestModeOnce sync.Once
)

// ensureGinTestMode - Gin을 테스트 모드로 안전하게 한 번만 설정
func ensureGinTestMode() {
	ginTestModeOnce.Do(func() {
		gin.SetMode(gin.TestMode)
	})
}

// GinContextKey - 요청 컨텍스트에 Gin 컨텍스트를 저장하기 위한 키
type GinContextKey struct{}

// URLParam - 요청 컨텍스트에 저장된 Gin 컨텍스트에서 URL 파라미터 추출
// 순수 Gin 구현 (Chi 호환성 불필요)
func URLParam(r *http.Request, key string) string {
	// Extract Gin context from request context
	if ginCtx := r.Context().Value(GinContextKey{}); ginCtx != nil {
		if ctx, ok := ginCtx.(*gin.Context); ok {
			return ctx.Param(key)
		}
	}

	return ""
}

// SetupTestRequestWithGinParams - Gin 컨텍스트와 URL 파라미터로 테스트 요청 생성
// 테스트에서 Chi 컨텍스트 설정을 순수 Gin 방식으로 대체
func SetupTestRequestWithGinParams(method, url string, params map[string]string) *http.Request {
	req := httptest.NewRequest(method, url, nil)

	// Create a Gin context for testing
	ensureGinTestMode()
	w := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(w)
	ginCtx.Request = req

	// Set the URL parameters in Gin context
	for key, value := range params {
		ginCtx.AddParam(key, value)
	}

	// Store Gin context in request context
	req = req.WithContext(context.WithValue(req.Context(), GinContextKey{}, ginCtx))

	return req
}

// SetupTestRequestWithOwnerRepo is a convenience helper for the common owner/repo pattern
// This makes migration from Chi easier by providing default parameter structure
func SetupTestRequestWithOwnerRepo(method, url, owner, repo string) *http.Request {
	params := map[string]string{
		"owner": owner,
		"repo":  repo,
	}
	return SetupTestRequestWithGinParams(method, url, params)
}

// AddGinParamsToRequest adds Gin context with URL parameters to an existing request
// This is useful for migrating existing test setups without recreating the request
func AddGinParamsToRequest(req *http.Request, params map[string]string) *http.Request {
	// Create a Gin context for testing
	ensureGinTestMode()
	w := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(w)
	ginCtx.Request = req

	// Set the URL parameters in Gin context
	for key, value := range params {
		ginCtx.AddParam(key, value)
	}

	// Store Gin context in request context
	return req.WithContext(context.WithValue(req.Context(), GinContextKey{}, ginCtx))
}
