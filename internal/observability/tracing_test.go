package observability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/trace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultTracingConfig(t *testing.T) {
	cfg := DefaultTracingConfig()
	assert.False(t, cfg.Enabled)
	assert.Equal(t, ServiceName, cfg.ServiceName)
	assert.Equal(t, ServiceVersion, cfg.ServiceVersion)
	assert.Equal(t, "stdout", cfg.ExporterType)
	assert.Equal(t, 1.0, cfg.SamplingRate)
}

func TestNewTracingManager_Disabled(t *testing.T) {
	tm, err := NewTracingManager(TracingConfig{Enabled: false})
	require.NoError(t, err)
	require.NotNil(t, tm)
	require.NotNil(t, tm.tracer)
	// disabled 매니저는 tracerProvider 가 nil → Shutdown 은 no-op.
	assert.NoError(t, tm.Shutdown(context.Background()))
}

func TestNewTracingManager_Enabled(t *testing.T) {
	tm, err := NewTracingManager(TracingConfig{
		Enabled:        true,
		ServiceName:    "test-svc",
		ServiceVersion: "0.0.1",
		Environment:    "test",
		ExporterType:   "stdout",
		SamplingRate:   1.0,
	})
	require.NoError(t, err)
	require.NotNil(t, tm)
	require.NotNil(t, tm.tracerProvider)

	assert.NoError(t, tm.Shutdown(context.Background()))
}

func TestCreateExporter(t *testing.T) {
	// stdout 와 미지정(default) 두 경로 모두 stdout exporter 를 반환한다.
	exp1, err := createExporter(TracingConfig{ExporterType: "stdout"})
	require.NoError(t, err)
	require.NotNil(t, exp1)

	exp2, err := createExporter(TracingConfig{ExporterType: "unknown"})
	require.NoError(t, err)
	require.NotNil(t, exp2)
}

// createExporter("otlp") 가 실제로 OTLP gRPC exporter 를 반환하는지 확인한다
// (Issue-7835 — exporter_type 에 "otlp" 케이스 자체가 없어 span 이 Tempo 로
// 전혀 나가지 못하던 결함).
func TestCreateExporter_OTLP(t *testing.T) {
	exp, err := createExporter(TracingConfig{
		ExporterType:     "otlp",
		ExporterEndpoint: "127.0.0.1:4317",
	})
	require.NoError(t, err)
	require.NotNil(t, exp)
	assert.IsType(t, &otlptrace.Exporter{}, exp)
}

func TestTracingManager_HTTPMiddleware_Disabled(t *testing.T) {
	tm, err := NewTracingManager(TracingConfig{Enabled: false})
	require.NoError(t, err)

	mw := tm.HTTPMiddleware()
	require.NotNil(t, mw)

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	wrapped := mw(next)
	require.NotNil(t, wrapped)

	// disabled 면 pass-through → 래핑 없이 그대로 호출돼야 한다.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	wrapped.ServeHTTP(rr, req)
	assert.True(t, called)
}

func TestTracingManager_HTTPMiddleware_Enabled(t *testing.T) {
	tm, err := NewTracingManager(TracingConfig{
		Enabled:      true,
		ExporterType: "stdout",
		SamplingRate: 1.0,
	})
	require.NoError(t, err)
	defer func() { _ = tm.Shutdown(context.Background()) }()

	mw := tm.HTTPMiddleware()
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	wrapped := mw(next)
	require.NotNil(t, wrapped)

	// enabled 면 otelhttp 핸들러로 래핑되지만, 요청 시 inner 가 호출돼야 한다.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	wrapped.ServeHTTP(rr, req)
	assert.True(t, called)
}

func TestGitOperationTracer(t *testing.T) {
	gt := NewGitOperationTracer()
	require.NotNil(t, gt)

	// success
	err := gt.TraceGitOperation(context.Background(), "clone", func(ctx context.Context) error {
		return nil
	})
	assert.NoError(t, err)

	// error 경로
	want := errors.New("clone failed")
	err = gt.TraceGitOperation(context.Background(), "clone", func(ctx context.Context) error {
		return want
	})
	assert.ErrorIs(t, err, want)
}

func TestGitOperationTracer_TraceRepositoryAccess(t *testing.T) {
	gt := NewGitOperationTracer()

	err := gt.TraceRepositoryAccess(context.Background(), "owner", "repo", "read", func(ctx context.Context) error {
		return nil
	})
	assert.NoError(t, err)

	want := errors.New("access denied")
	err = gt.TraceRepositoryAccess(context.Background(), "owner", "repo", "write", func(ctx context.Context) error {
		return want
	})
	assert.ErrorIs(t, err, want)
}

func TestAPIServiceTracer(t *testing.T) {
	at := NewAPIServiceTracer()
	require.NotNil(t, at)

	err := at.TraceAPICall(context.Background(), "perm-svc", "check", func(ctx context.Context) error {
		return nil
	})
	assert.NoError(t, err)

	want := errors.New("call failed")
	err = at.TraceAPICall(context.Background(), "perm-svc", "check", func(ctx context.Context) error {
		return want
	})
	assert.ErrorIs(t, err, want)
}

func TestAPIServiceTracer_TracePermissionCheck(t *testing.T) {
	at := NewAPIServiceTracer()

	// granted
	ok, err := at.TracePermissionCheck(context.Background(), "u1", "owner", "repo", func(ctx context.Context) (bool, error) {
		return true, nil
	})
	assert.True(t, ok)
	assert.NoError(t, err)

	// error
	want := errors.New("perm check failed")
	ok, err = at.TracePermissionCheck(context.Background(), "u1", "owner", "repo", func(ctx context.Context) (bool, error) {
		return false, want
	})
	assert.False(t, ok)
	assert.ErrorIs(t, err, want)
}

func TestCustomSpanTracer(t *testing.T) {
	ct := NewCustomSpanTracer()
	require.NotNil(t, ct)

	ctx, span := ct.StartSpan(context.Background(), "custom-op", attribute.String("k", "v"))
	require.NotNil(t, span)
	span.End()

	// TraceFunction success + error
	assert.NoError(t, ct.TraceFunction(ctx, "fn", func(ctx context.Context) error { return nil }))
	want := errors.New("fn failed")
	assert.ErrorIs(t, ct.TraceFunction(ctx, "fn", func(ctx context.Context) error { return want }, attribute.Int("n", 1)), want)

	// AddSpanEvent / SetSpanAttributes 는 패닉 없이 실행.
	assert.NotPanics(t, func() {
		ct.AddSpanEvent(ctx, "event", attribute.String("a", "b"))
		ct.SetSpanAttributes(ctx, attribute.String("c", "d"))
	})
}

func TestTraceSpan(t *testing.T) {
	assert.NoError(t, TraceSpan(context.Background(), "span", func(ctx context.Context) error { return nil }))

	want := errors.New("span failed")
	assert.ErrorIs(t, TraceSpan(context.Background(), "span", func(ctx context.Context) error { return want },
		attribute.String("k", "v")), want)
}

func TestExtractTraceID_AndSpanID(t *testing.T) {
	// span 이 없는 context → 빈 문자열.
	assert.Empty(t, ExtractTraceID(context.Background()))
	assert.Empty(t, ExtractSpanID(context.Background()))

	// 유효한 span context 를 가진 ctx → 유효한 ID.
	ct := NewCustomSpanTracer()
	ctx, span := ct.StartSpan(context.Background(), "op")
	defer span.End()

	if span.SpanContext().IsValid() {
		assert.NotEmpty(t, ExtractTraceID(ctx))
		assert.NotEmpty(t, ExtractSpanID(ctx))
	}
}

func TestInjectTraceIDIntoHeaders(t *testing.T) {
	headers := http.Header{}
	// span 없는 ctx → 헤더 세팅 안 됨.
	InjectTraceIDIntoHeaders(context.Background(), headers)
	assert.Empty(t, headers.Get("X-Trace-ID"))

	// 유효한 span context.
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
		SpanID:     trace.SpanID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	headers2 := http.Header{}
	InjectTraceIDIntoHeaders(ctx, headers2)
	assert.NotEmpty(t, headers2.Get("X-Trace-ID"))
	assert.NotEmpty(t, headers2.Get("X-Span-ID"))
}
