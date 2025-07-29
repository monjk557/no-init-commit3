package protocol

import (
	"context"
	"net/http"
	"net/http/httptest"

	"github.com/gin-gonic/gin"
)

// GinContextKey for storing Gin context in request context
type GinContextKey struct{}

// URLParam extracts URL parameters from Gin context stored in request context
// Pure Gin implementation - no Chi compatibility needed
func URLParam(r *http.Request, key string) string {
	// Extract Gin context from request context
	if ginCtx := r.Context().Value(GinContextKey{}); ginCtx != nil {
		if ctx, ok := ginCtx.(*gin.Context); ok {
			return ctx.Param(key)
		}
	}

	return ""
}

// SetupTestRequestWithGinParams creates a test request with Gin context and URL parameters
// This replaces Chi context setup in tests with pure Gin approach
func SetupTestRequestWithGinParams(method, url string, params map[string]string) *http.Request {
	req := httptest.NewRequest(method, url, nil)

	// Create a Gin context for testing
	gin.SetMode(gin.TestMode)
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
	gin.SetMode(gin.TestMode)
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
