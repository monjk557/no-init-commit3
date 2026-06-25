package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/collabops/git-executor/internal/logging"
)

// nopLogger는 출력 없는 테스트 전용 로거를 만든다 (zap.NewNop 임베딩).
func nopLogger() *logging.Logger {
	return &logging.Logger{Logger: zap.NewNop()}
}

// newACM은 기본 설정 + nop 로거로 접근 제어 미들웨어를 생성하는 헬퍼.
func newACM(t *testing.T, cfg *AccessControlConfig) *AccessControlMiddleware {
	t.Helper()
	acm, err := NewAccessControlMiddleware(cfg, nopLogger())
	require.NoError(t, err)
	require.NotNil(t, acm)
	return acm
}

// serveACM은 미들웨어를 통과시켜 응답 레코더를 돌려준다.
// next 핸들러는 200 OK 를 쓴다 — 통과 여부 판별용.
func serveACM(acm *AccessControlMiddleware, method, path, source string) *httptest.ResponseRecorder {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("passed"))
	})
	h := acm.Middleware(next)

	req := httptest.NewRequest(method, path, nil)
	if source != "" {
		req.Header.Set("X-Source", source)
	}
	req.Header.Set("X-Request-ID", "req-123")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestNewAccessControlMiddleware(t *testing.T) {
	t.Run("nil config uses defaults", func(t *testing.T) {
		acm, err := NewAccessControlMiddleware(nil, nopLogger())
		require.NoError(t, err)
		require.NotNil(t, acm)
		assert.NotNil(t, acm.config)
		assert.True(t, acm.config.DenyByDefault)
	})

	t.Run("nil logger is rejected", func(t *testing.T) {
		acm, err := NewAccessControlMiddleware(nil, nil)
		assert.Error(t, err)
		assert.Nil(t, acm)
	})

	t.Run("custom config is retained", func(t *testing.T) {
		cfg := &AccessControlConfig{
			Rules:             []AccessControlRule{{SourceHTTPGateway, EndpointGitProtocol, true}},
			DenyByDefault:     false,
			LogSecurityEvents: false,
		}
		acm := newACM(t, cfg)
		assert.Same(t, cfg, acm.config)
	})
}

func TestDefaultAccessControlConfig(t *testing.T) {
	cfg := DefaultAccessControlConfig()
	require.NotNil(t, cfg)
	assert.True(t, cfg.DenyByDefault)
	assert.True(t, cfg.LogSecurityEvents)
	assert.Len(t, cfg.Rules, 12)
}

func TestAccessControl_Middleware(t *testing.T) {
	acm := newACM(t, nil)

	t.Run("missing X-Source returns 403", func(t *testing.T) {
		w := serveACM(acm, http.MethodGet, "/o/r/info/refs", "")
		assert.Equal(t, http.StatusForbidden, w.Code)
		assert.Contains(t, w.Body.String(), "X-Source header required")
		assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	})

	t.Run("invalid X-Source returns 403", func(t *testing.T) {
		w := serveACM(acm, http.MethodGet, "/o/r/info/refs", "bogus-source")
		assert.Equal(t, http.StatusForbidden, w.Code)
		assert.Contains(t, w.Body.String(), "Invalid X-Source header")
	})

	t.Run("http-gateway allowed on git-protocol", func(t *testing.T) {
		w := serveACM(acm, http.MethodGet, "/owner/repo/info/refs", string(SourceHTTPGateway))
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "passed", w.Body.String())
	})

	t.Run("http-gateway denied on rest-api", func(t *testing.T) {
		w := serveACM(acm, http.MethodGet, "/repos/owner/repo/tree", string(SourceHTTPGateway))
		assert.Equal(t, http.StatusForbidden, w.Code)
		assert.Contains(t, w.Body.String(), "cannot access")
	})

	t.Run("ssh-gateway allowed on ssh-protocol", func(t *testing.T) {
		w := serveACM(acm, http.MethodPost, "/ssh/upload-pack", string(SourceSSHGateway))
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("orchestrator allowed on rest-api", func(t *testing.T) {
		w := serveACM(acm, http.MethodGet, "/repos/owner/repo/commits", string(SourceOrchestrator))
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("common endpoint allowed for any valid source", func(t *testing.T) {
		w := serveACM(acm, http.MethodGet, "/health", string(SourceHTTPGateway))
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("unknown endpoint denied by default", func(t *testing.T) {
		w := serveACM(acm, http.MethodGet, "/totally/unknown/route", string(SourceOrchestrator))
		assert.Equal(t, http.StatusForbidden, w.Code)
	})
}

func TestAccessControl_DenyByDefaultFalse(t *testing.T) {
	// DenyByDefault=false 이면 규칙에 없는 조합도 허용된다 (allow-by-default 경로).
	cfg := &AccessControlConfig{
		Rules:             nil,
		DenyByDefault:     false,
		LogSecurityEvents: true,
	}
	acm := newACM(t, cfg)
	w := serveACM(acm, http.MethodGet, "/repos/owner/repo/tree", string(SourceHTTPGateway))
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAccessControl_LogSecurityEventsDisabled(t *testing.T) {
	// LogSecurityEvents=false 경로 (logSecurityViolation 의 early-return) 커버.
	cfg := &AccessControlConfig{
		Rules:             DefaultAccessControlConfig().Rules,
		DenyByDefault:     true,
		LogSecurityEvents: false,
	}
	acm := newACM(t, cfg)
	w := serveACM(acm, http.MethodGet, "/o/r/info/refs", "")
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestAccessControl_isValidSource(t *testing.T) {
	acm := newACM(t, nil)
	cases := []struct {
		src  SourceType
		want bool
	}{
		{SourceHTTPGateway, true},
		{SourceSSHGateway, true},
		{SourceOrchestrator, true},
		{SourceType("unknown"), false},
		{SourceType(""), false},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, acm.isValidSource(c.src), "source=%q", c.src)
	}
}

func TestAccessControl_classifyEndpoint(t *testing.T) {
	acm := newACM(t, nil)
	cases := []struct {
		path string
		want EndpointType
	}{
		{"/owner/repo/info/refs", EndpointGitProtocol},
		{"/owner/repo/git-upload-pack", EndpointGitProtocol},
		{"/owner/repo/git-receive-pack", EndpointGitProtocol},
		{"/ssh/upload-pack", EndpointSSHProtocol},
		{"/ssh/receive-pack", EndpointSSHProtocol},
		{"/repos/owner/repo/tree", EndpointRESTAPI},
		{"/repos/owner/repo/blob", EndpointRESTAPI},
		{"/repos/owner/repo/diff", EndpointRESTAPI},
		{"/health", EndpointCommon},
		{"/metrics", EndpointCommon},
		{"/", EndpointCommon},
		{"/something/else", EndpointType("unknown")},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, acm.classifyEndpoint(c.path), "path=%q", c.path)
	}
}

func TestAccessControl_isAccessAllowed(t *testing.T) {
	acm := newACM(t, nil)

	// 명시 규칙: allow & deny.
	assert.True(t, acm.isAccessAllowed(SourceHTTPGateway, EndpointGitProtocol))
	assert.False(t, acm.isAccessAllowed(SourceHTTPGateway, EndpointRESTAPI))

	// 규칙에 없는 조합 → DenyByDefault=true 이므로 거부.
	assert.False(t, acm.isAccessAllowed(SourceHTTPGateway, EndpointType("unknown")))
}

func TestAccessControl_writeErrorResponse_EscapesQuotes(t *testing.T) {
	acm := newACM(t, nil)
	w := httptest.NewRecorder()
	acm.writeErrorResponse(w, http.StatusBadRequest, `bad "quoted" value`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	// 따옴표가 이스케이프되어 JSON 이 깨지지 않아야 한다.
	assert.Contains(t, w.Body.String(), `\"quoted\"`)
	assert.Contains(t, w.Body.String(), `"code": 400`)
}

func TestAccessControl_logHelpers_DoNotPanic(t *testing.T) {
	// logSecurityViolation / logAccessAttempt 가 nop 로거로 동작하는지 (panic 없음).
	acm := newACM(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/o/r/info/refs", nil)
	req.Header.Set("User-Agent", "test-agent")
	assert.NotPanics(t, func() {
		acm.logSecurityViolation(req, "http-gateway", "git-protocol", "test reason")
		acm.logAccessAttempt(req, "http-gateway", "git-protocol", "allowed")
	})
}
