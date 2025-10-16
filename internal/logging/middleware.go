package logging

import (
	"context"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// ResponseWriter는 신팩된 response writer들을 위한 인터페이스입니다.
type ResponseWriter interface {
	http.ResponseWriter
	Status() int
	BytesWritten() int
}

// responseWriterWrapper는 HTTP 응답의 상태 코드와 전송 바이트를 캡처하기 위해 http.ResponseWriter를 감시니다.
type responseWriterWrapper struct {
	http.ResponseWriter
	status       int
	bytesWritten int
}

func (w *responseWriterWrapper) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseWriterWrapper) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	w.bytesWritten += n
	return n, err
}

func (w *responseWriterWrapper) Status() int {
	if w.status == 0 {
		return 200
	}
	return w.status
}

func (w *responseWriterWrapper) BytesWritten() int {
	return w.bytesWritten
}

// NewWrapResponseWriter creates a wrapped response writer
func NewWrapResponseWriter(w http.ResponseWriter) ResponseWriter {
	return &responseWriterWrapper{
		ResponseWriter: w,
		status:         0,
		bytesWritten:   0,
	}
}

// RequestIDMiddleware는 요청 ID를 생성하고 컨텍스트에 추가하는 미들웨어입니다.
func RequestIDMiddleware(generator RequestIDGenerator) func(http.Handler) http.Handler {
	if generator == nil {
		generator = &DefaultRequestIDGenerator{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestID := generator.Generate()

			// Add request ID to context
			ctx := context.WithValue(r.Context(), RequestIDKey, requestID)

			// Add request ID to response header for tracing
			w.Header().Set("X-Request-ID", requestID)

			// Continue with the request
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// CorrelationIDMiddleware는 헤더에서 상관 ID를 추출하고 컨텍스트에 추가하는 미들웨어입니다.
func CorrelationIDMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extract correlation ID from header
			correlationID := r.Header.Get("X-Correlation-ID")
			if correlationID == "" {
				// If not provided, use request ID as correlation ID
				if requestID, ok := r.Context().Value(RequestIDKey).(string); ok {
					correlationID = requestID
				}
			}

			// Add correlation ID to context
			ctx := context.WithValue(r.Context(), CorrelationIDKey, correlationID)

			// Add correlation ID to response header
			w.Header().Set("X-Correlation-ID", correlationID)

			// Continue with the request
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// LoggingMiddleware는 컨텍스트와 함께 HTTP 요청을 로깅하는 미들웨어입니다.
func LoggingMiddleware(logger *Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Wrap response writer to capture status code
			ww := NewWrapResponseWriter(w)

			// Continue with the request
			next.ServeHTTP(ww, r)

			// Log the request
			duration := time.Since(start)
			logger.HTTPRequest(
				r.Context(),
				r.Method,
				r.URL.Path,
				r.UserAgent(),
				r.RemoteAddr,
				ww.Status(),
				duration,
			)
		})
	}
}

// ErrorHandlingMiddleware는 중앙집중식 에러 처리와 로깅을 제공하는 미들웨어입니다.
func ErrorHandlingMiddleware(logger *Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					// Log the panic
					logger.LogError(
						r.Context(),
						err.(error),
						"HTTP request panic",
						zap.String("method", r.Method),
						zap.String("path", r.URL.Path),
						zap.String("user_agent", r.UserAgent()),
						zap.String("remote_addr", r.RemoteAddr),
					)

					// Return 500 Internal Server Error
					http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// ConditionalLoggingMiddleware only logs if the context doesn't have skip_logging
func ConditionalLoggingMiddleware(logger *Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Wrap response writer to capture status code
			ww := NewWrapResponseWriter(w)

			// Continue with the request
			next.ServeHTTP(ww, r)

			// Only log if not marked to skip
			if skip, ok := r.Context().Value(SkipLoggingKey).(bool); !ok || !skip {
				duration := time.Since(start)
				logger.HTTPRequest(
					r.Context(),
					r.Method,
					r.URL.Path,
					r.UserAgent(),
					r.RemoteAddr,
					ww.Status(),
					duration,
				)
			}
		})
	}
}

// GetLoggerFromContext extracts logger from request context
func GetLoggerFromContext(ctx context.Context) *zap.Logger {
	logger := GetGlobalLogger()
	return logger.WithContext(ctx)
}
