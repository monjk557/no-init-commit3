package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

const (
	// HeaderGlobalTraceID is the header name for frontend-generated global trace ID
	HeaderGlobalTraceID = "X-Global-Trace-ID"
	// HeaderRequestID is the header name for per-service request ID
	HeaderRequestID = "X-Request-ID"
	// maxBodySizeForKeys is the maximum request body size (bytes) to parse for request_body_keys
	maxBodySizeForKeys = 64 * 1024
	// defaultReadSampleRate mirrors the orchestrator's access-log policy
	// (RequestLoggingMiddleware, _READ_SAMPLE_RATE=0.10): successful READ access
	// logs are sampled to curb volume, while writes and errors are logged 100%.
	defaultReadSampleRate = 0.10
)

// readSampleRate is the fraction of successful read-request access logs to keep.
// Overridable via GIT_EXECUTOR_LOG_READ_SAMPLE_RATE ("1" logs all, "0" drops all
// successful reads). Errors (>=400) and writes are never sampled.
var readSampleRate = loadReadSampleRate()

func loadReadSampleRate() float64 {
	if v := strings.TrimSpace(os.Getenv("GIT_EXECUTOR_LOG_READ_SAMPLE_RATE")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
			return f
		}
	}
	return defaultReadSampleRate
}

// isReadRequest reports whether a request is a read for access-log sampling.
// GET/HEAD/OPTIONS are reads; git smart-HTTP upload-pack (clone/fetch) and the
// ref advertisement are reads too despite using POST, whereas receive-pack
// (push) is a write and is never sampled.
func isReadRequest(method, path string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return strings.HasSuffix(path, "/git-upload-pack") || strings.HasSuffix(path, "/info/refs")
}

// sampleOutAccessLog reports whether this successful request's access log should
// be dropped by sampling. Only successful reads are eligible; per-request METRICS
// (transfer stats etc.) are emitted separately and unaffected.
func sampleOutAccessLog(method, path string, status int) bool {
	if status >= 400 {
		return false // always log errors
	}
	if !isReadRequest(method, path) {
		return false // always log writes (push, mutations)
	}
	if readSampleRate >= 1 {
		return false
	}
	if readSampleRate <= 0 {
		return true
	}
	return rand.Float64() >= readSampleRate
}

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

		// Skip logging for infrastructure endpoints to reduce noise
		if p := c.Request.URL.Path; p == "/health" || strings.HasPrefix(p, "/health/") || p == "/metrics" || p == "/ready" {
			return
		}

		durationMs := time.Since(start).Milliseconds()
		status := c.Writer.Status()

		// Access-log sampling (mirrors orchestrator V.1.4): keep all writes and
		// errors, sample successful reads (incl. clone/fetch) to curb log volume.
		// Uses the query-less path so suffix matching (…/git-upload-pack,
		// …/info/refs) is unaffected by the query string. Transfer/CPU metrics
		// are emitted elsewhere and are unaffected.
		if sampleOutAccessLog(method, c.Request.URL.Path, status) {
			return
		}

		level := levelFromStatus(status)
		msg := "request completed"
		if status >= 400 {
			msg = "request error"
		}

		metadata := map[string]interface{}{}

		// OpenTelemetry trace context 추출 — Loki↔Tempo 연동
		var traceID, spanID string
		span := trace.SpanFromContext(c.Request.Context())
		sc := span.SpanContext()
		if sc.HasTraceID() {
			traceID = sc.TraceID().String()
			spanID = sc.SpanID().String()
		}

		fields := []zap.Field{
			zap.String("event", "http_request"),
			zap.String("request_id", requestID),
			zap.String("global_trace_id", globalTraceID),
			zap.String("trace_id", traceID),
			zap.String("span_id", spanID),
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
