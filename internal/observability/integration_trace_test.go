package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/collabops/git-executor/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

// Issue-7835 — 로그에 trace_id 가 실리려면 ginTracingMiddleware 가 만든 span 이 실제로
// gin.Context.Request 에 반영되어야 한다. 기존 TestManager_GinTracingMiddleware_WithTracer
// (integration_test.go)는 HTTP 200 만 확인해서, otelhttp 가 넘겨준 span 포함
// *http.Request 를 클로저가 버리고 원본 c.Request 로 c.Next() 를 부르는 결함을 잡지
// 못했다 — span 은 매 요청 생성·종료되지만 gin 핸들러/이후 미들웨어에는 절대 보이지
// 않는다.
//
// 이 테스트는 핸들러 "안에서" trace.SpanFromContext(c.Request.Context()) 로 유효한
// span 을 실제로 관찰할 수 있는지를 직접 확인한다.
//
// 대조 검증: ginTracingMiddleware 클로저의 `c.Request = r` 대입을 지우고 원래
// 버그 상태(`c.Next()`만 호출)로 되돌리면 이 테스트는 FAIL 한다.
func TestManager_GinTracingMiddleware_PropagatesSpanToRequestContext(t *testing.T) {
	cfg := newManagerTestConfig(t)
	cfg.Observability.Tracing = config.TracingConfig{Enabled: true}
	m, err := NewManager(cfg, "v1.0.0")
	require.NoError(t, err)
	defer func() { _ = m.Shutdown(context.Background()) }()

	router := gin.New()
	router.Use(m.ginTracingMiddleware())

	var sawTraceID string
	router.GET("/ping", func(c *gin.Context) {
		sc := trace.SpanFromContext(c.Request.Context()).SpanContext()
		if sc.HasTraceID() {
			sawTraceID = sc.TraceID().String()
		}
		c.String(http.StatusOK, "ok")
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	router.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.NotEmpty(t, sawTraceID,
		"핸들러 내부에서 c.Request.Context() 로부터 유효한 span 을 찾지 못했다 — "+
			"ginTracingMiddleware 가 otelhttp 의 span 포함 request 를 c.Request 에 반영하지 않고 있다")
}
