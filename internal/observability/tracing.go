// Package observability provides OpenTelemetry distributed tracing capabilities
// for comprehensive request tracing across HTTP handlers, Git operations, and external API calls.
package observability

import (
	"context"
	"fmt"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const (
	// TracerName is the name of the tracer used throughout the application
	TracerName = "github.com/collabops/git-executor"

	// Service attributes
	ServiceName    = "collabops-git-executor"
	ServiceVersion = "1.0.0" // This could be injected at build time
)

// TracingConfig holds configuration for OpenTelemetry tracing
type TracingConfig struct {
	Enabled          bool    `mapstructure:"enabled"`
	ServiceName      string  `mapstructure:"service_name"`
	ServiceVersion   string  `mapstructure:"service_version"`
	Environment      string  `mapstructure:"environment"`
	ExporterType     string  `mapstructure:"exporter_type"` // stdout, jaeger, otlp, etc.
	ExporterEndpoint string  `mapstructure:"exporter_endpoint"`
	SamplingRate     float64 `mapstructure:"sampling_rate"`
}

// DefaultTracingConfig returns default tracing configuration
func DefaultTracingConfig() TracingConfig {
	return TracingConfig{
		Enabled:        false,
		ServiceName:    ServiceName,
		ServiceVersion: ServiceVersion,
		Environment:    "development",
		ExporterType:   "stdout",
		SamplingRate:   1.0, // 100% sampling for development
	}
}

// TracingManager manages OpenTelemetry tracing setup and lifecycle
type TracingManager struct {
	config         TracingConfig
	tracerProvider *trace.TracerProvider
	tracer         oteltrace.Tracer
}

// NewTracingManager creates a new tracing manager with the given configuration
func NewTracingManager(config TracingConfig) (*TracingManager, error) {
	if !config.Enabled {
		// Return a no-op manager when tracing is disabled
		return &TracingManager{
			config: config,
			tracer: otel.Tracer(TracerName),
		}, nil
	}

	// Create resource with service information
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			"",
			attribute.String("service.name", config.ServiceName),
			attribute.String("service.version", config.ServiceVersion),
			attribute.String("deployment.environment", config.Environment),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	// Create exporter based on configuration
	exporter, err := createExporter(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create exporter: %w", err)
	}

	// Create tracer provider
	tp := trace.NewTracerProvider(
		trace.WithBatcher(exporter),
		trace.WithResource(res),
		trace.WithSampler(trace.TraceIDRatioBased(config.SamplingRate)),
	)

	// Set global tracer provider
	otel.SetTracerProvider(tp)

	// Set global propagator to ensure proper context propagation
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return &TracingManager{
		config:         config,
		tracerProvider: tp,
		tracer:         tp.Tracer(TracerName),
	}, nil
}

// createExporter creates a trace exporter based on configuration
func createExporter(config TracingConfig) (trace.SpanExporter, error) {
	switch config.ExporterType {
	case "stdout":
		return stdouttrace.New(stdouttrace.WithPrettyPrint())
	default:
		// Default to stdout for now
		// In production, you'd want to add support for Jaeger, OTLP, etc.
		return stdouttrace.New(stdouttrace.WithPrettyPrint())
	}
}

// Shutdown gracefully shuts down the tracing manager
func (tm *TracingManager) Shutdown(ctx context.Context) error {
	if tm.tracerProvider != nil {
		return tm.tracerProvider.Shutdown(ctx)
	}
	return nil
}

// HTTPMiddleware returns OpenTelemetry HTTP middleware for automatic request tracing
func (tm *TracingManager) HTTPMiddleware() func(http.Handler) http.Handler {
	if !tm.config.Enabled {
		// Return pass-through middleware when tracing is disabled
		return func(next http.Handler) http.Handler {
			return next
		}
	}

	return func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(next, "http-request")
	}
}

// GitOperationTracer provides tracing for Git operations
type GitOperationTracer struct {
	tracer oteltrace.Tracer
}

// NewGitOperationTracer creates a new Git operation tracer
func NewGitOperationTracer() *GitOperationTracer {
	return &GitOperationTracer{
		tracer: otel.Tracer(TracerName),
	}
}

// TraceGitOperation wraps a Git operation with tracing
func (gt *GitOperationTracer) TraceGitOperation(ctx context.Context, operationType string, fn func(context.Context) error) error {
	ctx, span := gt.tracer.Start(ctx, fmt.Sprintf("git.%s", operationType),
		oteltrace.WithAttributes(
			attribute.String("git.operation", operationType),
			attribute.String("component", "git"),
		),
	)
	defer span.End()

	// Execute the operation
	err := fn(ctx)

	// Record the result
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}

	return err
}

// TraceRepositoryAccess traces repository access operations
func (gt *GitOperationTracer) TraceRepositoryAccess(ctx context.Context, owner, repo, operation string, fn func(context.Context) error) error {
	ctx, span := gt.tracer.Start(ctx, fmt.Sprintf("git.repository.%s", operation),
		oteltrace.WithAttributes(
			attribute.String("git.repository.owner", owner),
			attribute.String("git.repository.name", repo),
			attribute.String("git.operation", operation),
			attribute.String("component", "git"),
		),
	)
	defer span.End()

	// Execute the operation
	err := fn(ctx)

	// Record the result
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}

	return err
}

// APIServiceTracer provides tracing for external API service calls
type APIServiceTracer struct {
	tracer oteltrace.Tracer
}

// NewAPIServiceTracer creates a new API service tracer
func NewAPIServiceTracer() *APIServiceTracer {
	return &APIServiceTracer{
		tracer: otel.Tracer(TracerName),
	}
}

// TraceAPICall wraps an API service call with tracing
func (at *APIServiceTracer) TraceAPICall(ctx context.Context, serviceName, endpoint string, fn func(context.Context) error) error {
	ctx, span := at.tracer.Start(ctx, fmt.Sprintf("api.%s.%s", serviceName, endpoint),
		oteltrace.WithAttributes(
			attribute.String("api.service", serviceName),
			attribute.String("api.endpoint", endpoint),
			attribute.String("component", "api_client"),
		),
	)
	defer span.End()

	// Execute the API call
	err := fn(ctx)

	// Record the result
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}

	return err
}

// TracePermissionCheck traces permission check operations
func (at *APIServiceTracer) TracePermissionCheck(ctx context.Context, userID, owner, repo string, fn func(context.Context) (bool, error)) (bool, error) {
	ctx, span := at.tracer.Start(ctx, "permission.check",
		oteltrace.WithAttributes(
			attribute.String("permission.user_id", userID),
			attribute.String("permission.owner", owner),
			attribute.String("permission.repo", repo),
			attribute.String("component", "permission_client"),
		),
	)
	defer span.End()

	// Execute the permission check
	hasAccess, err := fn(ctx)

	// Record the result
	span.SetAttributes(
		attribute.Bool("permission.granted", hasAccess),
	)

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}

	return hasAccess, err
}

// CustomSpanTracer provides utilities for creating custom spans
type CustomSpanTracer struct {
	tracer oteltrace.Tracer
}

// NewCustomSpanTracer creates a new custom span tracer
func NewCustomSpanTracer() *CustomSpanTracer {
	return &CustomSpanTracer{
		tracer: otel.Tracer(TracerName),
	}
}

// StartSpan starts a new span with the given name and attributes
func (ct *CustomSpanTracer) StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, oteltrace.Span) {
	return ct.tracer.Start(ctx, name, oteltrace.WithAttributes(attrs...))
}

// TraceFunction wraps a function execution with a custom span
func (ct *CustomSpanTracer) TraceFunction(ctx context.Context, functionName string, fn func(context.Context) error, attrs ...attribute.KeyValue) error {
	ctx, span := ct.tracer.Start(ctx, functionName, oteltrace.WithAttributes(attrs...))
	defer span.End()

	err := fn(ctx)

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}

	return err
}

// AddSpanEvent adds an event to the current span
func (ct *CustomSpanTracer) AddSpanEvent(ctx context.Context, name string, attrs ...attribute.KeyValue) {
	span := oteltrace.SpanFromContext(ctx)
	if span != nil {
		span.AddEvent(name, oteltrace.WithAttributes(attrs...))
	}
}

// SetSpanAttributes sets attributes on the current span
func (ct *CustomSpanTracer) SetSpanAttributes(ctx context.Context, attrs ...attribute.KeyValue) {
	span := oteltrace.SpanFromContext(ctx)
	if span != nil {
		span.SetAttributes(attrs...)
	}
}

// TraceSpan is a convenience function to trace any operation
func TraceSpan(ctx context.Context, spanName string, fn func(context.Context) error, attrs ...attribute.KeyValue) error {
	tracer := otel.Tracer(TracerName)
	ctx, span := tracer.Start(ctx, spanName, oteltrace.WithAttributes(attrs...))
	defer span.End()

	err := fn(ctx)

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}

	return err
}

// ExtractTraceID extracts the trace ID from the current span context
func ExtractTraceID(ctx context.Context) string {
	span := oteltrace.SpanFromContext(ctx)
	if span != nil && span.SpanContext().IsValid() {
		return span.SpanContext().TraceID().String()
	}
	return ""
}

// ExtractSpanID extracts the span ID from the current span context
func ExtractSpanID(ctx context.Context) string {
	span := oteltrace.SpanFromContext(ctx)
	if span != nil && span.SpanContext().IsValid() {
		return span.SpanContext().SpanID().String()
	}
	return ""
}

// InjectTraceIDIntoHeaders injects trace information into HTTP headers
func InjectTraceIDIntoHeaders(ctx context.Context, headers http.Header) {
	traceID := ExtractTraceID(ctx)
	spanID := ExtractSpanID(ctx)

	if traceID != "" {
		headers.Set("X-Trace-ID", traceID)
	}
	if spanID != "" {
		headers.Set("X-Span-ID", spanID)
	}
}
