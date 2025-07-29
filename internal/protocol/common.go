package protocol

import (
	"net/http"
)

// ChiRouteCtxKey and ChiRouteContext types for gin-to-chi compatibility
type ChiRouteCtxKey struct{}
type ChiRouteContext struct {
	URLParams map[string]string
}

// URLParam extracts URL parameters from the request context
// This provides chi compatibility for our Gin-based server
func URLParam(r *http.Request, key string) string {
	// Try to extract from our gin-to-chi compatibility context
	if rctx := r.Context().Value(ChiRouteCtxKey{}); rctx != nil {
		if routeCtx, ok := rctx.(*ChiRouteContext); ok {
			return routeCtx.URLParams[key]
		}
	}
	return ""
}
