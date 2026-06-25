package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// newTestLogger 는 테스트용 실로거를 만든다.
func newTestLogger(t *testing.T) *logging.Logger {
	t.Helper()
	logger, err := logging.NewLogger(config.LoggingConfig{
		Level:       "error",
		Format:      "text",
		Output:      "stdout",
		Development: true,
	})
	require.NoError(t, err)
	require.NotNil(t, logger)
	return logger
}

func TestNewJSONResponseMiddleware(t *testing.T) {
	t.Run("with explicit logger and version", func(t *testing.T) {
		logger := newTestLogger(t)
		m := NewJSONResponseMiddleware(logger, "2.5")
		require.NotNil(t, m)
		assert.Equal(t, "2.5", m.version)
		assert.Equal(t, logger, m.logger)
	})

	t.Run("nil logger and empty version fall back to defaults", func(t *testing.T) {
		// 회귀: nil logger -> 기본 로거 생성, "" version -> "1.0".
		m := NewJSONResponseMiddleware(nil, "")
		require.NotNil(t, m)
		assert.Equal(t, "1.0", m.version)
		require.NotNil(t, m.logger)
	})
}

// newTestContext 는 ResponseRecorder 에 묶인 gin.Context 를 만든다.
func newTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/test", nil)
	return c, w
}

func TestSendSuccessResponse(t *testing.T) {
	m := NewJSONResponseMiddleware(newTestLogger(t), "9.9")
	c, w := newTestContext()
	c.Set("request_id", "rid-123")
	c.Set("start_time", time.Now().Add(-5*time.Millisecond))

	m.SendSuccessResponse(c, map[string]any{"foo": "bar"}, "ok")

	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "ok", body["message"])
	// flat 병합: payload 가 최상위에 올라온다.
	assert.Equal(t, "bar", body["foo"])
	meta, ok := body["meta"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "rid-123", meta["request_id"])
	assert.Equal(t, "9.9", meta["version"])
}

func TestSendSuccessResponse_NilPayload(t *testing.T) {
	m := NewJSONResponseMiddleware(newTestLogger(t), "1.0")
	c, w := newTestContext()

	m.SendSuccessResponse(c, nil, "no payload")

	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "no payload", body["message"])
}

func TestSendSuccessResponseWithPagination(t *testing.T) {
	m := NewJSONResponseMiddleware(newTestLogger(t), "3.0")
	c, w := newTestContext()
	c.Set("request_id", "rid-page")
	c.Set("start_time", time.Now())

	pg := CreatePagination(2, 10, 35)
	m.SendSuccessResponseWithPagination(c, map[string]any{"items": []int{1, 2}}, pg, "paged")

	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, true, body["success"])
	meta := body["meta"].(map[string]any)
	pagination, ok := meta["pagination"].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, 2, pagination["page"])
	assert.EqualValues(t, 4, pagination["total_pages"])
	assert.Equal(t, true, pagination["has_next"])
	assert.Equal(t, true, pagination["has_prev"])
}

func TestSendSuccessResponseWithPagination_NilPayload(t *testing.T) {
	m := NewJSONResponseMiddleware(newTestLogger(t), "3.0")
	c, w := newTestContext()

	m.SendSuccessResponseWithPagination(c, nil, CreatePagination(1, 10, 0), "empty")

	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, true, body["success"])
}

func TestSendErrorResponse(t *testing.T) {
	m := NewJSONResponseMiddleware(newTestLogger(t), "1.0")
	c, w := newTestContext()

	apiErr := logging.NewNotFoundError("NOT_FOUND", "resource missing")
	m.SendErrorResponse(c, apiErr, "rid-err", 42)

	assert.Equal(t, http.StatusNotFound, w.Code)
	var resp APIResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.False(t, resp.Success)
	require.NotNil(t, resp.Error)
	assert.Equal(t, "NOT_FOUND", resp.Error.Code)
	assert.Equal(t, "resource missing", resp.Error.Message)
	// 회귀: error 에 RequestID 없으면 인자의 requestID 로 채운다.
	assert.Equal(t, "rid-err", resp.Error.RequestID)
	require.NotNil(t, resp.Meta)
	assert.Equal(t, "rid-err", resp.Meta.RequestID)
	assert.EqualValues(t, 42, resp.Meta.Duration)
}

func TestSendErrorResponse_PreservesExistingRequestID(t *testing.T) {
	m := NewJSONResponseMiddleware(newTestLogger(t), "1.0")
	c, w := newTestContext()

	apiErr := logging.NewValidationError("INVALID_INPUT", "bad")
	apiErr.RequestID = "already-set"
	m.SendErrorResponse(c, apiErr, "fallback-rid", 0)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp APIResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// error 자체 RequestID 는 보존, fallback 으로 덮어쓰지 않음.
	assert.Equal(t, "already-set", resp.Error.RequestID)
}

// setupErrorRouter 는 ErrorHandler 미들웨어 + c.Error 를 호출하는 라우트를 단다.
func setupErrorRouter(m *JSONResponseMiddleware) *gin.Engine {
	r := gin.New()
	r.Use(m.RequestMetadataMiddleware())
	r.Use(m.ErrorHandler())
	return r
}

func TestErrorHandler_PublicError_AppError(t *testing.T) {
	m := NewJSONResponseMiddleware(newTestLogger(t), "1.0")
	r := setupErrorRouter(m)
	r.GET("/pub", func(c *gin.Context) {
		appErr := logging.NewNotFoundError("REF_NOT_FOUND", "branch missing")
		_ = c.Error(appErr).SetType(gin.ErrorTypePublic)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/pub", nil))

	assert.Equal(t, http.StatusNotFound, w.Code)
	var resp APIResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.False(t, resp.Success)
	require.NotNil(t, resp.Error)
	assert.Equal(t, "REF_NOT_FOUND", resp.Error.Code)
}

func TestErrorHandler_PublicError_PlainError(t *testing.T) {
	// 회귀: public 이지만 AppError 가 아니면 UNKNOWN_ERROR validation 으로 래핑.
	m := NewJSONResponseMiddleware(newTestLogger(t), "1.0")
	r := setupErrorRouter(m)
	r.GET("/pubplain", func(c *gin.Context) {
		_ = c.Error(assertAnError("some public message")).SetType(gin.ErrorTypePublic)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/pubplain", nil))

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp APIResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "UNKNOWN_ERROR", resp.Error.Code)
	assert.Equal(t, logging.ErrorTypeValidation, resp.Error.Type)
}

func TestErrorHandler_BindError(t *testing.T) {
	m := NewJSONResponseMiddleware(newTestLogger(t), "1.0")
	r := setupErrorRouter(m)
	r.GET("/bind", func(c *gin.Context) {
		_ = c.Error(assertAnError("field required")).SetType(gin.ErrorTypeBind)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/bind", nil))

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp APIResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "UNKNOWN_ERROR", resp.Error.Code)
	assert.Equal(t, "Invalid request format or missing required fields", resp.Error.Message)
	// WithDetails 로 원본 에러가 details 에 들어간다.
	assert.Contains(t, resp.Error.Details, "field required")
}

func TestErrorHandler_InternalError(t *testing.T) {
	// 기본 type (private) -> internal 처리, 내부 디테일 노출 안 함.
	m := NewJSONResponseMiddleware(newTestLogger(t), "1.0")
	r := setupErrorRouter(m)
	r.GET("/internal", func(c *gin.Context) {
		_ = c.Error(assertAnError("secret stack trace details"))
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/internal", nil))

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	var resp APIResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "UNKNOWN_ERROR", resp.Error.Code)
	assert.Equal(t, "An internal server error occurred", resp.Error.Message)
	assert.NotContains(t, w.Body.String(), "secret stack trace")
}

func TestErrorHandler_NoErrors(t *testing.T) {
	// c.Errors 가 비면 미들웨어는 아무것도 안 한다.
	m := NewJSONResponseMiddleware(newTestLogger(t), "1.0")
	r := setupErrorRouter(m)
	r.GET("/clean", func(c *gin.Context) {
		c.JSON(http.StatusTeapot, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/clean", nil))

	assert.Equal(t, http.StatusTeapot, w.Code)
	assert.Contains(t, w.Body.String(), `"ok":true`)
}

func TestRequestMetadataMiddleware(t *testing.T) {
	t.Run("generates request id when none present", func(t *testing.T) {
		m := NewJSONResponseMiddleware(newTestLogger(t), "1.0")
		r := gin.New()
		r.Use(m.RequestMetadataMiddleware())
		var capturedID any
		var hasStart bool
		r.GET("/m", func(c *gin.Context) {
			capturedID, _ = c.Get("request_id")
			_, hasStart = c.Get("start_time")
			c.Status(http.StatusOK)
		})

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/m", nil))

		id, ok := capturedID.(string)
		require.True(t, ok)
		assert.NotEmpty(t, id)
		assert.True(t, hasStart)
		assert.Equal(t, id, w.Header().Get("X-Request-ID"))
	})

	t.Run("uses X-Request-ID header when present", func(t *testing.T) {
		m := NewJSONResponseMiddleware(newTestLogger(t), "1.0")
		r := gin.New()
		r.Use(m.RequestMetadataMiddleware())
		r.GET("/m", func(c *gin.Context) { c.Status(http.StatusOK) })

		req := httptest.NewRequest(http.MethodGet, "/m", nil)
		req.Header.Set("X-Request-ID", "from-header")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		assert.Equal(t, "from-header", w.Header().Get("X-Request-ID"))
	})

	t.Run("preserves request_id already set in context", func(t *testing.T) {
		m := NewJSONResponseMiddleware(newTestLogger(t), "1.0")
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set("request_id", "preset-id")
			c.Next()
		})
		r.Use(m.RequestMetadataMiddleware())
		r.GET("/m", func(c *gin.Context) { c.Status(http.StatusOK) })

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/m", nil))

		assert.Equal(t, "preset-id", w.Header().Get("X-Request-ID"))
	})
}

func TestSendJSON(t *testing.T) {
	c, w := newTestContext()
	SendJSON(c, http.StatusCreated, gin.H{"hello": "world"})

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.Contains(t, w.Body.String(), `"hello":"world"`)
}

func TestSendError(t *testing.T) {
	c, w := newTestContext()
	c.Set("request_id", "rid-pkg")
	c.Set("start_time", time.Now())

	SendError(c, logging.NewForbiddenError("FORBIDDEN", "nope"))

	assert.Equal(t, http.StatusForbidden, w.Code)
	var resp APIResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.False(t, resp.Success)
	assert.Equal(t, "FORBIDDEN", resp.Error.Code)
}

func TestSendSuccess(t *testing.T) {
	c, w := newTestContext()
	SendSuccess(c, map[string]any{"k": "v"}, "done")

	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "v", body["k"])
	assert.Equal(t, "done", body["message"])
}

func TestCreatePagination(t *testing.T) {
	tests := []struct {
		name                                   string
		page, pageSize, total                  int
		wantPage, wantPageSize, wantTotalPages int
		wantHasNext, wantHasPrev               bool
	}{
		{"first page of many", 1, 10, 35, 1, 10, 4, true, false},
		{"middle page", 2, 10, 35, 2, 10, 4, true, true},
		{"last page", 4, 10, 35, 4, 10, 4, false, true},
		{"exact division", 2, 5, 10, 2, 5, 2, false, true},
		{"zero total clamps total pages to 1", 1, 10, 0, 1, 10, 1, false, false},
		{"page below 1 clamps to 1", 0, 10, 20, 1, 10, 2, true, false},
		{"pageSize below 1 clamps to 10", 1, 0, 20, 1, 10, 2, true, false},
		{"single item", 1, 10, 1, 1, 10, 1, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pg := CreatePagination(tt.page, tt.pageSize, tt.total)
			require.NotNil(t, pg)
			assert.Equal(t, tt.wantPage, pg.Page)
			assert.Equal(t, tt.wantPageSize, pg.PageSize)
			assert.Equal(t, tt.total, pg.Total)
			assert.Equal(t, tt.wantTotalPages, pg.TotalPages)
			assert.Equal(t, tt.wantHasNext, pg.HasNext)
			assert.Equal(t, tt.wantHasPrev, pg.HasPrev)
		})
	}
}

func TestGenerateRequestID(t *testing.T) {
	id1 := generateRequestID()
	assert.NotEmpty(t, id1)
	assert.True(t, strings.HasPrefix(id1, "req_"), "id should have req_ prefix: %s", id1)

	// 여러 호출이 서로 다른 값을 내는지 확인. UnixNano 해상도가 낮은 환경에서
	// 인접 호출이 같은 nano 를 반환할 수 있으므로 최소 한 쌍이 다르면 통과.
	seen := map[string]struct{}{id1: {}}
	distinct := false
	for i := 0; i < 1000; i++ {
		id := generateRequestID()
		if _, ok := seen[id]; !ok {
			distinct = true
			break
		}
		seen[id] = struct{}{}
	}
	assert.True(t, distinct, "expected generateRequestID to produce distinct values")
}

// assertAnError 는 메시지를 담은 단순 error 를 만든다 (AppError 가 아님).
type plainError struct{ msg string }

func (e plainError) Error() string { return e.msg }

func assertAnError(msg string) error { return plainError{msg: msg} }
