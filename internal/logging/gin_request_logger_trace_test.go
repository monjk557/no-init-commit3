package logging

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func init() { gin.SetMode(gin.TestMode) }

func fieldValue(entry observer.LoggedEntry, key string) (string, bool) {
	for _, f := range entry.Context {
		if f.Key == key {
			return f.String, true
		}
	}
	return "", false
}

func testSpanContext(t *testing.T) trace.SpanContext {
	t.Helper()
	traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	require.NoError(t, err)
	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
}

// Issue-7835 — GinRequestLogger 는 sc.HasTraceID() 가 참일 때만 trace_id/span_id 필드를
// 실어야 한다. 이 테스트는 실제 로그 라인을 캡처해 값이 span 의 trace_id/span_id 와
// 정확히 일치하는지 확인한다(다른 필드 형식은 안 건드렸는지도 같이 검증).
//
// 대조 검증: sc.HasTraceID() 가드 없이 무조건 zap.String("trace_id", traceID) 를
// 삽입하도록 되돌려도 이 테스트 자체는 통과한다(값이 있으므로) — 그래서 짝이 되는
// TestGinRequestLogger_OmitsTraceFieldsWithoutSpan 이 그 회귀를 잡는다.
func TestGinRequestLogger_EmitsTraceFieldsWithSpan(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	logger := &Logger{Logger: zap.New(core)}

	router := gin.New()
	router.Use(GinRequestLogger(logger))
	router.GET("/x", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	sc := testSpanContext(t)
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	req := httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(ctx)

	router.ServeHTTP(httptest.NewRecorder(), req)

	entries := logs.All()
	require.Len(t, entries, 1, "요청 1건에 로그 1건이 찍혀야 한다")

	gotTraceID, ok := fieldValue(entries[0], "trace_id")
	require.True(t, ok, "span 이 있으면 trace_id 키가 있어야 한다")
	require.Equal(t, sc.TraceID().String(), gotTraceID)

	gotSpanID, ok := fieldValue(entries[0], "span_id")
	require.True(t, ok, "span 이 있으면 span_id 키가 있어야 한다")
	require.Equal(t, sc.SpanID().String(), gotSpanID)
}

// 대조 검증: gin_request_logger.go 의 `if sc.HasTraceID() { ... }` 가드를 지우고
// 무조건 zap.String("trace_id", traceID) / zap.String("span_id", spanID) 를 삽입하는
// 원래 버그 상태로 되돌리면 이 테스트는 FAIL 한다 — 빈 문자열 키가 그대로 실리기
// 때문이다.
func TestGinRequestLogger_OmitsTraceFieldsWithoutSpan(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	logger := &Logger{Logger: zap.New(core)}

	router := gin.New()
	router.Use(GinRequestLogger(logger))
	router.GET("/x", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	req := httptest.NewRequest(http.MethodGet, "/x", nil) // span 없는 일반 요청
	router.ServeHTTP(httptest.NewRecorder(), req)

	entries := logs.All()
	require.Len(t, entries, 1)

	for _, key := range []string{"trace_id", "span_id"} {
		if v, ok := fieldValue(entries[0], key); ok {
			t.Fatalf("span 이 없으면 %s 키가 아예 없어야 하는데 값 %q 로 존재한다", key, v)
		}
	}
}
