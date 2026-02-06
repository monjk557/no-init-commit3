package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	// HeaderGlobalTraceID is the header name for frontend-generated global trace ID
	HeaderGlobalTraceID = "X-Global-Trace-ID"
	// HeaderRequestID is the header name for per-service request ID
	HeaderRequestID = "X-Request-ID"
	// maxBodySizeForKeys is the maximum request body size (bytes) to parse for request_body_keys
	maxBodySizeForKeys = 64 * 1024
)

// GinRequestLogger returns a Gin middleware that logs each request as a single JSON event
// with: event, request_id, global_trace_id, msg, method, path, metadata, duration_ms,
// http (status), request_size_bytes, request_content_type, request_body_keys.
func GinRequestLogger(logger *Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		// Request ID: from header or generate
		requestID := c.GetHeader(HeaderRequestID)
		if requestID == "" {
			requestID = (&DefaultRequestIDGenerator{}).Generate()
		}
		c.Set("request_id", requestID)
		c.Header(HeaderRequestID, requestID)

		// Inject request_id (and global_trace_id) into request context so handlers/services get them when logging
		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, RequestIDKey, requestID)
		globalTraceID := c.GetHeader(HeaderGlobalTraceID)
		c.Set("global_trace_id", globalTraceID)
		if globalTraceID != "" {
			ctx = context.WithValue(ctx, CorrelationIDKey, globalTraceID)
		}
		c.Request = c.Request.WithContext(ctx)

		method := c.Request.Method
		path := c.Request.URL.Path
		if c.Request.URL.RawQuery != "" {
			path = path + "?" + c.Request.URL.RawQuery
		}
		requestContentType := c.GetHeader("Content-Type")
		requestSizeBytes := int64(0)
		if c.Request.ContentLength > 0 {
			requestSizeBytes = c.Request.ContentLength
		}

		var requestBodyKeys []string
		if shouldPeekBody(c.Request) {
			requestBodyKeys, _ = peekRequestBodyKeys(c.Request)
		}

		c.Next()

		// Skip logging for health check endpoints to reduce noise
		if p := c.Request.URL.Path; p == "/health" || strings.HasPrefix(p, "/health/") {
			return
		}

		durationMs := time.Since(start).Milliseconds()
		status := c.Writer.Status()
		level := levelFromStatus(status)
		msg := "request completed"
		if status >= 400 {
			msg = "request error"
		}

		metadata := map[string]interface{}{}

		fields := []zap.Field{
			zap.String("event", "http_request"),
			zap.String("request_id", requestID),
			zap.String("global_trace_id", globalTraceID),
			zap.String("method", method),
			zap.String("path", path),
			zap.Any("metadata", metadata),
			zap.Int64("duration_ms", durationMs),
			zap.Int("http", status),
			zap.Int64("request_size_bytes", requestSizeBytes),
			zap.String("request_content_type", requestContentType),
			zap.Strings("request_body_keys", requestBodyKeys),
		}

		switch level {
		case "error":
			logger.Error(msg, fields...)
		case "warn":
			logger.Warn(msg, fields...)
		default:
			logger.Info(msg, fields...)
		}
	}
}

func levelFromStatus(status int) string {
	if status >= 500 {
		return "error"
	}
	if status >= 400 {
		return "warn"
	}
	return "info"
}

func shouldPeekBody(r *http.Request) bool {
	if r.Body == nil || r.ContentLength <= 0 || r.ContentLength > maxBodySizeForKeys {
		return false
	}
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		break
	default:
		return false
	}
	ct := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])
	return ct == "application/json"
}

// peekRequestBodyKeys reads the body (up to maxBodySizeForKeys), parses JSON top-level keys,
// then restores r.Body so handlers can read it. Call only when ContentLength <= maxBodySizeForKeys.
func peekRequestBodyKeys(r *http.Request) (keys []string, bodyRestored bool) {
	if r.Body == nil {
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodySizeForKeys+1))
	if err != nil {
		return nil, false
	}
	// Always restore so downstream handlers can read the body
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if len(raw) > maxBodySizeForKeys {
		return nil, true
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, true
	}
	for k := range m {
		keys = append(keys, k)
	}
	return keys, true
}
