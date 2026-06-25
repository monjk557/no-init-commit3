package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/collabops/git-executor/internal/config"
)

// newBufferLogger builds a *Logger writing JSON to an in-memory buffer at debug
// level so tests can assert on emitted log lines.
func newBufferLogger(t *testing.T) (*Logger, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	encoderCfg := zap.NewProductionEncoderConfig()
	encoder := zapcore.NewJSONEncoder(encoderCfg)
	core := zapcore.NewCore(encoder, zapcore.AddSync(buf), zapcore.DebugLevel)
	return &Logger{Logger: zap.New(core)}, buf
}

// --- errors.go ---

func TestAppErrorErrorAndUnwrap(t *testing.T) {
	base := errors.New("boom")
	e := &AppError{Code: "C1", Message: "msg", Cause: base}
	assert.Equal(t, "C1: msg (caused by: boom)", e.Error())
	assert.Equal(t, base, e.Unwrap())

	e2 := &AppError{Code: "C2", Message: "no cause"}
	assert.Equal(t, "C2: no cause", e2.Error())
	assert.Nil(t, e2.Unwrap())
}

func TestAppErrorWithMethods(t *testing.T) {
	e := &AppError{Code: "C", Message: "m"}
	cause := errors.New("c")
	e.WithContext("k", "v").
		WithCause(cause).
		WithRequestID("rid").
		WithDetails("d")
	assert.Equal(t, "v", e.Context["k"])
	assert.Equal(t, cause, e.Cause)
	assert.Equal(t, "rid", e.RequestID)
	assert.Equal(t, "d", e.Details)
}

func TestErrorBuilderFull(t *testing.T) {
	cause := errors.New("c")
	e := NewErrorBuilder(ErrorTypeConflict, "CODE", "message").
		WithDetails("details").
		WithCause(cause).
		WithContext("key", 1).
		WithStatusCode(http.StatusTeapot).
		WithRetryable(true).
		WithUserMessage("user").
		Build()
	assert.Equal(t, ErrorTypeConflict, e.Type)
	assert.Equal(t, "CODE", e.Code)
	assert.Equal(t, "message", e.Message)
	assert.Equal(t, "details", e.Details)
	assert.Equal(t, cause, e.Cause)
	assert.Equal(t, 1, e.Context["key"])
	assert.Equal(t, http.StatusTeapot, e.StatusCode)
	assert.True(t, e.Retryable)
	assert.Equal(t, "user", e.UserMessage)
}

func TestErrorBuilderDefaultStatusCode(t *testing.T) {
	// No explicit status -> default from type.
	e := NewErrorBuilder(ErrorTypeNotFound, "NF", "missing").Build()
	assert.Equal(t, http.StatusNotFound, e.StatusCode)
}

func TestGetDefaultStatusCode(t *testing.T) {
	cases := map[ErrorType]int{
		ErrorTypeValidation:   http.StatusBadRequest,
		ErrorTypeNotFound:     http.StatusNotFound,
		ErrorTypeUnauthorized: http.StatusUnauthorized,
		ErrorTypeForbidden:    http.StatusForbidden,
		ErrorTypeConflict:     http.StatusConflict,
		ErrorTypeTimeout:      http.StatusRequestTimeout,
		ErrorTypeRateLimit:    http.StatusTooManyRequests,
		ErrorTypeExternal:     http.StatusBadGateway,
		ErrorTypeInternal:     http.StatusInternalServerError,
		ErrorType("other"):    http.StatusInternalServerError,
	}
	for typ, want := range cases {
		assert.Equal(t, want, getDefaultStatusCode(typ), "type=%s", typ)
	}
}

func TestCommonErrorConstructors(t *testing.T) {
	assert.Equal(t, ErrorTypeValidation, NewValidationError("V", "m").Type)
	assert.Equal(t, ErrorTypeNotFound, NewNotFoundError("N", "m").Type)
	assert.Equal(t, ErrorTypeUnauthorized, NewUnauthorizedError("U", "m").Type)
	assert.Equal(t, ErrorTypeForbidden, NewForbiddenError("F", "m").Type)

	internal := NewInternalError("I", "m")
	assert.Equal(t, ErrorTypeInternal, internal.Type)
	assert.True(t, internal.Retryable)

	external := NewExternalError("E", "m")
	assert.Equal(t, ErrorTypeExternal, external.Type)
	assert.True(t, external.Retryable)
}

func TestErrorHandlerHandleAppError(t *testing.T) {
	logger, buf := newBufferLogger(t)
	h := NewErrorHandler(logger)

	ctx := context.WithValue(context.Background(), RequestIDKey, "req-1")
	rec := httptest.NewRecorder()
	appErr := NewValidationError("VAL", "bad input").WithDetails("field x")

	h.Handle(ctx, rec, appErr)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "req-1", body["request_id"])
	errObj := body["error"].(map[string]interface{})
	assert.Equal(t, "VAL", errObj["code"])
	assert.Equal(t, "field x", errObj["details"])
	// Client error -> logged as warning.
	assert.Contains(t, buf.String(), "Client error")
}

func TestErrorHandlerHandleUnknownError(t *testing.T) {
	logger, buf := newBufferLogger(t)
	h := NewErrorHandler(logger)

	rec := httptest.NewRecorder()
	h.Handle(context.Background(), rec, errors.New("raw failure"))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	errObj := body["error"].(map[string]interface{})
	// Internal error must NOT leak details when none set.
	_, hasDetails := errObj["details"]
	assert.False(t, hasDetails)
	assert.Contains(t, buf.String(), "Server error")
}

func TestErrorHandlerHandleDefaultLevel(t *testing.T) {
	logger, buf := newBufferLogger(t)
	h := NewErrorHandler(logger)
	rec := httptest.NewRecorder()
	// Timeout type falls into the default switch branch.
	appErr := NewErrorBuilder(ErrorTypeTimeout, "TO", "timed out").Build()
	h.Handle(context.Background(), rec, appErr)
	assert.Equal(t, http.StatusRequestTimeout, rec.Code)
	assert.Contains(t, buf.String(), "Application error")
}

func TestErrorHandlerHandlePanic(t *testing.T) {
	logger, _ := newBufferLogger(t)
	h := NewErrorHandler(logger)
	rec := httptest.NewRecorder()

	func() {
		defer h.HandlePanic(context.Background(), rec)
		panic("kaboom")
	}()

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "PANIC")
}

func TestErrorHandlerHandlePanicWithError(t *testing.T) {
	logger, _ := newBufferLogger(t)
	h := NewErrorHandler(logger)
	rec := httptest.NewRecorder()

	func() {
		defer h.HandlePanic(context.Background(), rec)
		panic(errors.New("typed panic"))
	}()
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestErrorHandlerHandlePanicNoPanic(t *testing.T) {
	logger, _ := newBufferLogger(t)
	h := NewErrorHandler(logger)
	rec := httptest.NewRecorder()
	// Should be a no-op when nothing panicked.
	h.HandlePanic(context.Background(), rec)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestGlobalErrorHandler(t *testing.T) {
	globalErrorHandler = nil
	logger, _ := newBufferLogger(t)
	InitGlobalErrorHandler(logger)
	assert.NotNil(t, GetGlobalErrorHandler())

	// Reset and rely on lazy construction.
	globalErrorHandler = nil
	assert.NotNil(t, GetGlobalErrorHandler())

	rec := httptest.NewRecorder()
	HandleError(context.Background(), rec, NewNotFoundError("NF", "nope"))
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// HandlePanic (package-level) with no active panic is a no-op.
	rec2 := httptest.NewRecorder()
	HandlePanic(context.Background(), rec2)
	assert.Equal(t, http.StatusOK, rec2.Code)
}

// --- logger.go ---

func TestNewLoggerFormats(t *testing.T) {
	jsonLogger, err := NewLogger(config.LoggingConfig{Level: "info", Format: "json", Output: "stdout"})
	require.NoError(t, err)
	assert.NotNil(t, jsonLogger)

	textLogger, err := NewLogger(config.LoggingConfig{Level: "debug", Format: "text", Output: "stderr", Development: true})
	require.NoError(t, err)
	assert.NotNil(t, textLogger)
}

func TestNewLoggerFileOutput(t *testing.T) {
	dir := t.TempDir()
	logger, err := NewLogger(config.LoggingConfig{Level: "info", Format: "json", Output: dir + "/app.log"})
	require.NoError(t, err)
	require.NotNil(t, logger)
}

func TestNewLoggerErrors(t *testing.T) {
	_, err := NewLogger(config.LoggingConfig{Level: "info", Format: "xml", Output: "stdout"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported log format")

	_, err = NewLogger(config.LoggingConfig{Level: "bogus", Format: "json", Output: "stdout"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid log level")
}

func TestDefaultRequestIDGenerator(t *testing.T) {
	g := &DefaultRequestIDGenerator{}
	id1 := g.Generate()
	assert.NotEmpty(t, id1)
}

func TestLoggerWithRequestAndCorrelationID(t *testing.T) {
	logger, buf := newBufferLogger(t)
	logger.WithRequestID("rid").Info("a")
	logger.WithCorrelationID("cid").Info("b")
	out := buf.String()
	assert.Contains(t, out, "rid")
	assert.Contains(t, out, "cid")
}

func TestLoggerWithContext(t *testing.T) {
	logger, buf := newBufferLogger(t)
	ctx := context.WithValue(context.Background(), RequestIDKey, "req-9")
	ctx = context.WithValue(ctx, CorrelationIDKey, "corr-9")
	logger.WithContext(ctx).Info("hello")
	out := buf.String()
	assert.Contains(t, out, "req-9")
	assert.Contains(t, out, "corr-9")

	// Empty context -> no request/correlation fields.
	buf.Reset()
	logger.WithContext(context.Background()).Info("plain")
	assert.NotContains(t, buf.String(), "request_id")
}

func TestLoggerHTTPRequestLevels(t *testing.T) {
	logger, buf := newBufferLogger(t)
	ctx := context.Background()
	logger.HTTPRequest(ctx, "GET", "/ok", "agent", "1.2.3.4", 200, time.Millisecond)
	logger.HTTPRequest(ctx, "GET", "/bad", "agent", "1.2.3.4", 404, time.Millisecond)
	logger.HTTPRequest(ctx, "GET", "/err", "agent", "1.2.3.4", 500, time.Millisecond)
	out := buf.String()
	assert.Contains(t, out, "HTTP request completed")
	assert.Contains(t, out, "HTTP request client error")
	assert.Contains(t, out, "HTTP request failed")
}

func TestLoggerLogHelpers(t *testing.T) {
	logger, buf := newBufferLogger(t)
	ctx := context.Background()
	logger.LogError(ctx, errors.New("e"), "an error", zap.String("x", "y"))
	logger.LogWarning(ctx, "a warning")
	logger.LogInfo(ctx, "an info")
	logger.LogDebug(ctx, "a debug")
	out := buf.String()
	assert.Contains(t, out, "an error")
	assert.Contains(t, out, "a warning")
	assert.Contains(t, out, "an info")
	assert.Contains(t, out, "a debug")
}

func TestLoggerSyncAndClose(t *testing.T) {
	logger, _ := newBufferLogger(t)
	// Sync on a buffer-backed core returns nil.
	assert.NoError(t, logger.Close())
}

func TestGlobalLoggerFunctions(t *testing.T) {
	globalLogger = nil
	// Lazy fallback construction.
	assert.NotNil(t, GetGlobalLogger())

	logger, _ := newBufferLogger(t)
	SetGlobal(logger)
	assert.Equal(t, logger, GetGlobal())

	require.NoError(t, InitGlobalLogger(config.LoggingConfig{Level: "info", Format: "json", Output: "stdout"}))

	err := InitGlobalLogger(config.LoggingConfig{Level: "info", Format: "bad", Output: "stdout"})
	require.Error(t, err)

	ctx := context.Background()
	Info(ctx, "info-msg")
	Debug(ctx, "debug-msg")
	Warn(ctx, "warn-msg")
	Error(ctx, errors.New("e"), "error-msg")
	HTTPRequest(ctx, "GET", "/p", "ua", "addr", 200, time.Millisecond)
}

// --- middleware.go (net/http) ---

func TestResponseWriterWrapper(t *testing.T) {
	rec := httptest.NewRecorder()
	ww := NewWrapResponseWriter(rec)
	// Default status before WriteHeader.
	assert.Equal(t, 200, ww.Status())

	ww.WriteHeader(http.StatusCreated)
	n, err := ww.Write([]byte("hello"))
	require.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, http.StatusCreated, ww.Status())
	assert.Equal(t, 5, ww.BytesWritten())
}

func TestRequestIDMiddleware(t *testing.T) {
	var gotID string
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID, _ = r.Context().Value(RequestIDKey).(string)
	})

	// nil generator -> default.
	h := RequestIDMiddleware(nil)(final)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)
	assert.NotEmpty(t, gotID)
	assert.NotEmpty(t, rec.Header().Get("X-Request-ID"))
}

func TestCorrelationIDMiddleware(t *testing.T) {
	// With explicit header.
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	h := CorrelationIDMiddleware()(final)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Correlation-ID", "abc")
	h.ServeHTTP(rec, req)
	assert.Equal(t, "abc", rec.Header().Get("X-Correlation-ID"))

	// Without header but with request ID in context.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2 = req2.WithContext(context.WithValue(req2.Context(), RequestIDKey, "rid-derived"))
	h.ServeHTTP(rec2, req2)
	assert.Equal(t, "rid-derived", rec2.Header().Get("X-Correlation-ID"))
}

func TestLoggingMiddleware(t *testing.T) {
	logger, buf := newBufferLogger(t)
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	h := LoggingMiddleware(logger)(final)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	h.ServeHTTP(rec, req)
	assert.Contains(t, buf.String(), "/x")
}

func TestErrorHandlingMiddleware(t *testing.T) {
	logger, buf := newBufferLogger(t)
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(errors.New("middleware panic"))
	})
	h := ErrorHandlingMiddleware(logger)(final)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, buf.String(), "HTTP request panic")
}

func TestConditionalLoggingMiddleware(t *testing.T) {
	logger, buf := newBufferLogger(t)
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := ConditionalLoggingMiddleware(logger)(final)

	// Not skipped -> logs.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/logged", nil)
	h.ServeHTTP(rec, req)
	assert.Contains(t, buf.String(), "/logged")

	// Skipped -> no log.
	buf.Reset()
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/skip", nil)
	req2 = req2.WithContext(context.WithValue(req2.Context(), SkipLoggingKey, true))
	h.ServeHTTP(rec2, req2)
	assert.NotContains(t, buf.String(), "/skip")
}

func TestGetLoggerFromContext(t *testing.T) {
	logger, _ := newBufferLogger(t)
	SetGlobal(logger)
	l := GetLoggerFromContext(context.WithValue(context.Background(), RequestIDKey, "rid"))
	assert.NotNil(t, l)
}

// --- gin_request_logger.go ---

func setupGin(logger *Logger) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(GinRequestLogger(logger))
	return r
}

func TestGinRequestLoggerBasic(t *testing.T) {
	logger, buf := newBufferLogger(t)
	r := setupGin(logger)
	r.GET("/api/thing", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/thing?q=1", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotEmpty(t, rec.Header().Get(HeaderRequestID))
	out := buf.String()
	assert.Contains(t, out, "http_request")
	assert.Contains(t, out, "/api/thing?q=1")
	assert.Contains(t, out, "request completed")
}

func TestGinRequestLoggerWithHeaders(t *testing.T) {
	logger, buf := newBufferLogger(t)
	r := setupGin(logger)
	r.POST("/api/create", func(c *gin.Context) {
		// Consume body to verify it was restored after the middleware peeked it.
		data, _ := io.ReadAll(c.Request.Body)
		assert.Contains(t, string(data), "name")
		c.JSON(http.StatusCreated, gin.H{"ok": true})
	})

	body := `{"name":"x","value":2}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/create", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderRequestID, "client-rid")
	req.Header.Set(HeaderGlobalTraceID, "global-trace")
	req.ContentLength = int64(len(body))
	r.ServeHTTP(rec, req)

	assert.Equal(t, "client-rid", rec.Header().Get(HeaderRequestID))
	out := buf.String()
	assert.Contains(t, out, "client-rid")
	assert.Contains(t, out, "global-trace")
	assert.Contains(t, out, "request_body_keys")
}

func TestGinRequestLoggerErrorLevels(t *testing.T) {
	logger, buf := newBufferLogger(t)
	r := setupGin(logger)
	r.GET("/client", func(c *gin.Context) { c.Status(http.StatusBadRequest) })
	r.GET("/server", func(c *gin.Context) { c.Status(http.StatusInternalServerError) })

	for _, p := range []string{"/client", "/server"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, p, nil)
		r.ServeHTTP(rec, req)
	}
	out := buf.String()
	assert.Contains(t, out, "request error")
}

func TestGinRequestLoggerSkipsInfraEndpoints(t *testing.T) {
	logger, buf := newBufferLogger(t)
	r := setupGin(logger)
	for _, p := range []string{"/health", "/health/live", "/metrics", "/ready"} {
		r.GET(p, func(c *gin.Context) { c.Status(http.StatusOK) })
	}
	for _, p := range []string{"/health", "/health/live", "/metrics", "/ready"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, p, nil)
		r.ServeHTTP(rec, req)
	}
	assert.Empty(t, strings.TrimSpace(buf.String()))
}

func TestLevelFromStatus(t *testing.T) {
	assert.Equal(t, "info", levelFromStatus(200))
	assert.Equal(t, "warn", levelFromStatus(404))
	assert.Equal(t, "error", levelFromStatus(503))
}

func TestShouldPeekBody(t *testing.T) {
	// nil body.
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Body = nil
	assert.False(t, shouldPeekBody(r))

	// GET method -> false.
	r2 := httptest.NewRequest(http.MethodGet, "/", strings.NewReader("{}"))
	r2.ContentLength = 2
	assert.False(t, shouldPeekBody(r2))

	// POST non-json -> false.
	r3 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("abc"))
	r3.ContentLength = 3
	r3.Header.Set("Content-Type", "text/plain")
	assert.False(t, shouldPeekBody(r3))

	// POST json -> true.
	r4 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	r4.ContentLength = 2
	r4.Header.Set("Content-Type", "application/json; charset=utf-8")
	assert.True(t, shouldPeekBody(r4))

	// Too large -> false.
	r5 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	r5.ContentLength = maxBodySizeForKeys + 1
	r5.Header.Set("Content-Type", "application/json")
	assert.False(t, shouldPeekBody(r5))
}

func TestPeekRequestBodyKeys(t *testing.T) {
	// Valid JSON.
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"a":1,"b":2}`))
	keys, restored := peekRequestBodyKeys(r)
	assert.True(t, restored)
	assert.ElementsMatch(t, []string{"a", "b"}, keys)
	// Body restored.
	rest, _ := io.ReadAll(r.Body)
	assert.Contains(t, string(rest), "a")

	// Invalid JSON -> no keys, restored true.
	r2 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("not json"))
	keys2, restored2 := peekRequestBodyKeys(r2)
	assert.Nil(t, keys2)
	assert.True(t, restored2)

	// nil body.
	r3 := httptest.NewRequest(http.MethodPost, "/", nil)
	r3.Body = nil
	keys3, restored3 := peekRequestBodyKeys(r3)
	assert.Nil(t, keys3)
	assert.False(t, restored3)
}
