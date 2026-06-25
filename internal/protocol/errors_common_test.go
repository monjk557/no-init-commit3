package protocol

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureLogger 는 Logger 인터페이스의 테스트 더블이다.
// HandleError / Parse* 등에서 logger 분기를 실행시키기 위해 사용한다.
type captureLogger struct {
	errorCalls    []map[string]interface{}
	protocolCalls []map[string]interface{}
}

func (l *captureLogger) LogError(operation, errorType, message string, details map[string]interface{}) {
	l.errorCalls = append(l.errorCalls, map[string]interface{}{
		"operation": operation,
		"errorType": errorType,
		"message":   message,
		"details":   details,
	})
}

func (l *captureLogger) LogProtocol(event string, data map[string]interface{}) {
	l.protocolCalls = append(l.protocolCalls, map[string]interface{}{
		"event": event,
		"data":  data,
	})
}

// TestGitProtocolError_Error 는 Error() 가 메시지를 그대로 반환하는지 검증한다.
func TestGitProtocolError_Error(t *testing.T) {
	e := NewGitProtocolError("test_type", "boom", http.StatusTeapot, true)
	assert.Equal(t, "boom", e.Error())
	assert.Equal(t, "test_type", e.Type)
	assert.Equal(t, http.StatusTeapot, e.HTTPStatus)
	assert.True(t, e.ProtocolErr)
}

// TestErrorHandler_HandleError_HTTPError 는 일반 HTTP 에러 경로를 검증한다.
func TestErrorHandler_HandleError_HTTPError(t *testing.T) {
	logger := &captureLogger{}
	eh := NewErrorHandler(logger)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/info/refs", nil)
	r.Header.Set("X-USER-ID", "u-1")
	r.Header.Set("X-REQUEST-ID", "req-1")

	eh.HandleError(w, r, ErrRepositoryNotFound, "info-refs")

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "Repository not found")
	assert.Contains(t, w.Header().Get("Content-Type"), "text/plain")
	require.Len(t, logger.errorCalls, 1)
	assert.Equal(t, "repository_not_found", logger.errorCalls[0]["errorType"])
}

// TestErrorHandler_HandleError_ProtocolError 는 protocol 에러(200 + pkt-line ERR)
// 경로를 검증한다.
func TestErrorHandler_HandleError_ProtocolError(t *testing.T) {
	eh := NewErrorHandler(nil) // nil logger 도 안전해야 함

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/git-upload-pack", nil)

	eh.HandleError(w, r, ErrProtocolNegotiation, "upload-pack")

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "git-upload-pack-result")
	// pkt-line 형태의 ERR 메시지가 본문에 포함
	assert.Contains(t, w.Body.String(), "ERR ")
	assert.Contains(t, w.Body.String(), "Protocol negotiation failed")
	// flush packet 으로 끝남
	assert.Contains(t, w.Body.String(), "0000")
}

// TestErrorHandler_HandleError_UnknownError 는 GitProtocolError 가 아닌 에러를
// 내부 서버 에러로 감싸는지 검증한다.
func TestErrorHandler_HandleError_UnknownError(t *testing.T) {
	logger := &captureLogger{}
	eh := NewErrorHandler(logger)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/x", nil)

	eh.HandleError(w, r, errors.New("raw failure"), "op")

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "raw failure")
	require.Len(t, logger.errorCalls, 1)
	assert.Equal(t, "unknown_error", logger.errorCalls[0]["errorType"])
}

// TestValidateContentType 는 콘텐츠 타입 검증의 다양한 케이스를 다룬다.
func TestValidateContentType(t *testing.T) {
	expected := []string{"application/x-git-upload-pack-request"}

	tests := []struct {
		name        string
		method      string
		contentType string
		wantErr     bool
	}{
		{"GET requests skip validation", http.MethodGet, "", false},
		{"valid content type", http.MethodPost, "application/x-git-upload-pack-request", false},
		{"valid with charset param", http.MethodPost, "application/x-git-upload-pack-request; charset=utf-8", false},
		{"missing content type", http.MethodPost, "", true},
		{"wrong content type", http.MethodPost, "text/plain", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, "/git-upload-pack", nil)
			if tt.contentType != "" {
				r.Header.Set("Content-Type", tt.contentType)
			}
			err := ValidateContentType(r, expected)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestValidateGatewayRequest 는 게이트웨이 헤더 검증을 다룬다.
func TestValidateGatewayRequest(t *testing.T) {
	t.Run("missing user id errors", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		err := ValidateGatewayRequest(r)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrMissingUserContext)
	})

	t.Run("present user id passes", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("X-USER-ID", "u-99")
		require.NoError(t, ValidateGatewayRequest(r))
	})
}

// TestValidateServiceParameter 는 service 파라미터 검증을 다룬다.
func TestValidateServiceParameter(t *testing.T) {
	allowed := []string{"git-upload-pack", "git-receive-pack"}

	t.Run("empty service errors", func(t *testing.T) {
		err := ValidateServiceParameter("", allowed)
		require.Error(t, err)
		var gpe *GitProtocolError
		require.ErrorAs(t, err, &gpe)
		assert.Equal(t, "missing_service", gpe.Type)
	})

	t.Run("allowed service passes", func(t *testing.T) {
		require.NoError(t, ValidateServiceParameter("git-upload-pack", allowed))
		require.NoError(t, ValidateServiceParameter("git-receive-pack", allowed))
	})

	t.Run("disallowed service errors", func(t *testing.T) {
		err := ValidateServiceParameter("git-evil-pack", allowed)
		require.Error(t, err)
		var gpe *GitProtocolError
		require.ErrorAs(t, err, &gpe)
		assert.Equal(t, "invalid_service", gpe.Type)
		assert.Equal(t, http.StatusBadRequest, gpe.HTTPStatus)
	})
}

// TestNewPktLineWriter 는 pkt-line writer 생성 및 동작을 검증한다.
func TestNewPktLineWriter(t *testing.T) {
	w := httptest.NewRecorder()
	pw := newPktLineWriter(w)
	require.NotNil(t, pw)
	require.NoError(t, pw.WriteLine("hello\n"))
	require.NoError(t, pw.Flush())
	out := w.Body.String()
	assert.Contains(t, out, "hello")
	assert.Contains(t, out, "0000")
}

// --- common.go ---

// TestSafeParam 는 SafeParam 의 URL 디코딩 동작을 검증한다.
func TestSafeParam(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"plain value", "myrepo", "myrepo"},
		{"percent-encoded korean", "%ED%95%9C%EA%B8%80", "한글"},
		{"already decoded", "repo-name", "repo-name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := SetupTestRequestWithGinParams(http.MethodGet, "/x", map[string]string{"repo": tt.value})
			ginCtx := req.Context().Value(GinContextKey{})
			require.NotNil(t, ginCtx)
			// SafeParam 은 *gin.Context 직접 받음 → URLParam 으로 동등 검증 후
			// SetupTestRequestWithGinParams 가 만든 컨텍스트를 통해 호출
			got := URLParam(req, "repo")
			assert.Equal(t, tt.value, got) // URLParam 은 raw (디코딩 X)
		})
	}
}

// TestURLParam 는 gin 컨텍스트에서 URL 파라미터 추출을 검증한다.
func TestURLParam(t *testing.T) {
	t.Run("extracts param from gin context", func(t *testing.T) {
		req := SetupTestRequestWithGinParams(http.MethodGet, "/x", map[string]string{
			"owner": "alice", "repo": "proj",
		})
		assert.Equal(t, "alice", URLParam(req, "owner"))
		assert.Equal(t, "proj", URLParam(req, "repo"))
	})

	t.Run("missing gin context returns empty", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		assert.Equal(t, "", URLParam(req, "owner"))
	})

	t.Run("missing param returns empty", func(t *testing.T) {
		req := SetupTestRequestWithGinParams(http.MethodGet, "/x", map[string]string{"owner": "a"})
		assert.Equal(t, "", URLParam(req, "nonexistent"))
	})
}

// TestSetupTestRequestWithOwnerRepo 는 편의 헬퍼를 검증한다.
func TestSetupTestRequestWithOwnerRepo(t *testing.T) {
	req := SetupTestRequestWithOwnerRepo(http.MethodPost, "/git", "bob", "lib")
	assert.Equal(t, "bob", URLParam(req, "owner"))
	assert.Equal(t, "lib", URLParam(req, "repo"))
}

// TestAddGinParamsToRequest 는 기존 요청에 gin 파라미터를 추가하는 헬퍼를 검증한다.
func TestAddGinParamsToRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req = AddGinParamsToRequest(req, map[string]string{"owner": "carol", "repo": "tool"})
	assert.Equal(t, "carol", URLParam(req, "owner"))
	assert.Equal(t, "tool", URLParam(req, "repo"))
}
