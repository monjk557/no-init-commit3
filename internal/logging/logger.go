package logging

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	lumberjack "gopkg.in/natefinch/lumberjack.v2"

	"github.com/collabops/git-executor/internal/config"
)

// Logger wraps zap.Logger with additional functionality
type Logger struct {
	*zap.Logger
	config *config.Config
}

// ContextKey is used for context keys to avoid collisions
type ContextKey string

const (
	// RequestIDKey is the context key for request IDs
	RequestIDKey ContextKey = "request_id"
	// CorrelationIDKey is the context key for correlation IDs
	CorrelationIDKey ContextKey = "correlation_id"
	// SkipLoggingKey is the context key for skipping logging
	SkipLoggingKey ContextKey = "skip_logging"
)

// RequestIDGenerator generates unique request IDs
type RequestIDGenerator interface {
	Generate() string
}

// DefaultRequestIDGenerator generates simple timestamp-based IDs
type DefaultRequestIDGenerator struct{}

// Generate creates a new request ID
func (g *DefaultRequestIDGenerator) Generate() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// NewLogger creates a new logger instance with the given configuration
func NewLogger(cfg *config.Config) (*Logger, error) {
	// Create encoder config
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "timestamp",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    "function",
		MessageKey:     "message",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	// Create encoder based on format
	var encoder zapcore.Encoder
	switch cfg.Log.Format {
	case "json":
		encoder = zapcore.NewJSONEncoder(encoderConfig)
	case "text":
		encoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		encoderConfig.EncodeTime = zapcore.RFC3339TimeEncoder
		encoder = zapcore.NewConsoleEncoder(encoderConfig)
	default:
		return nil, fmt.Errorf("unsupported log format: %s", cfg.Log.Format)
	}

	// Parse log level
	level, err := zapcore.ParseLevel(cfg.Log.Level)
	if err != nil {
		return nil, fmt.Errorf("invalid log level %s: %w", cfg.Log.Level, err)
	}

	// Create output writers
	var writeSyncer zapcore.WriteSyncer
	switch cfg.Log.Output {
	case "stdout":
		writeSyncer = zapcore.AddSync(os.Stdout)
	case "stderr":
		writeSyncer = zapcore.AddSync(os.Stderr)
	default:
		// File output with rotation
		logRotator := &lumberjack.Logger{
			Filename:   cfg.Log.Output,
			MaxSize:    100, // 100 MB
			MaxBackups: 5,
			MaxAge:     30, // 30 days
			Compress:   true,
		}
		writeSyncer = zapcore.AddSync(logRotator)
	}

	// Create core
	core := zapcore.NewCore(encoder, writeSyncer, level)

	// Create logger with additional options
	options := []zap.Option{
		zap.AddCaller(),
		zap.AddStacktrace(zapcore.ErrorLevel),
	}

	if cfg.Log.Development {
		options = append(options, zap.Development())
	}

	zapLogger := zap.New(core, options...)

	return &Logger{
		Logger: zapLogger,
		config: cfg,
	}, nil
}

// WithRequestID returns a logger with request ID context
func (l *Logger) WithRequestID(requestID string) *zap.Logger {
	return l.With(zap.String("request_id", requestID))
}

// WithCorrelationID returns a logger with correlation ID context
func (l *Logger) WithCorrelationID(correlationID string) *zap.Logger {
	return l.With(zap.String("correlation_id", correlationID))
}

// WithContext returns a logger with context values
func (l *Logger) WithContext(ctx context.Context) *zap.Logger {
	logger := l.Logger

	// Add request ID if present
	if requestID, ok := ctx.Value(RequestIDKey).(string); ok && requestID != "" {
		logger = logger.With(zap.String("request_id", requestID))
	}

	// Add correlation ID if present
	if correlationID, ok := ctx.Value(CorrelationIDKey).(string); ok && correlationID != "" {
		logger = logger.With(zap.String("correlation_id", correlationID))
	}

	return logger
}

// HTTPRequest logs HTTP request details
func (l *Logger) HTTPRequest(ctx context.Context, method, path, userAgent, remoteAddr string, statusCode int, duration time.Duration) {
	logger := l.WithContext(ctx)

	fields := []zap.Field{
		zap.String("method", method),
		zap.String("path", path),
		zap.String("user_agent", userAgent),
		zap.String("remote_addr", remoteAddr),
		zap.Int("status_code", statusCode),
		zap.Duration("duration", duration),
	}

	if statusCode >= 500 {
		logger.Error("HTTP request failed", fields...)
	} else if statusCode >= 400 {
		logger.Warn("HTTP request client error", fields...)
	} else {
		logger.Info("HTTP request completed", fields...)
	}
}

// LogError logs an error with context and additional fields
func (l *Logger) LogError(ctx context.Context, err error, message string, fields ...zap.Field) {
	logger := l.WithContext(ctx)

	errorFields := []zap.Field{
		zap.Error(err),
	}
	errorFields = append(errorFields, fields...)

	logger.Error(message, errorFields...)
}

// LogWarning logs a warning with context and additional fields
func (l *Logger) LogWarning(ctx context.Context, message string, fields ...zap.Field) {
	logger := l.WithContext(ctx)
	logger.Warn(message, fields...)
}

// LogInfo logs an info message with context and additional fields
func (l *Logger) LogInfo(ctx context.Context, message string, fields ...zap.Field) {
	logger := l.WithContext(ctx)
	logger.Info(message, fields...)
}

// LogDebug logs a debug message with context and additional fields
func (l *Logger) LogDebug(ctx context.Context, message string, fields ...zap.Field) {
	logger := l.WithContext(ctx)
	logger.Debug(message, fields...)
}

// Sync flushes any buffered log entries
func (l *Logger) Sync() error {
	return l.Logger.Sync()
}

// Close ensures all logs are flushed
func (l *Logger) Close() error {
	return l.Sync()
}

// Global logger instance
var globalLogger *Logger

// InitGlobalLogger initializes the global logger
func InitGlobalLogger(cfg *config.Config) error {
	logger, err := NewLogger(cfg)
	if err != nil {
		return fmt.Errorf("failed to initialize global logger: %w", err)
	}
	globalLogger = logger
	return nil
}

// GetGlobalLogger returns the global logger instance
func GetGlobalLogger() *Logger {
	if globalLogger == nil {
		// Fallback to development logger if not initialized
		config := &config.Config{
			Log: config.LogConfig{
				Level:       "info",
				Format:      "text",
				Output:      "stdout",
				Development: true,
			},
		}
		logger, _ := NewLogger(config)
		globalLogger = logger
	}
	return globalLogger
}

// Helper functions for global logger
func Info(ctx context.Context, message string, fields ...zap.Field) {
	GetGlobalLogger().LogInfo(ctx, message, fields...)
}

func Debug(ctx context.Context, message string, fields ...zap.Field) {
	GetGlobalLogger().LogDebug(ctx, message, fields...)
}

func Warn(ctx context.Context, message string, fields ...zap.Field) {
	GetGlobalLogger().LogWarning(ctx, message, fields...)
}

func Error(ctx context.Context, err error, message string, fields ...zap.Field) {
	GetGlobalLogger().LogError(ctx, err, message, fields...)
}

func HTTPRequest(ctx context.Context, method, path, userAgent, remoteAddr string, statusCode int, duration time.Duration) {
	GetGlobalLogger().HTTPRequest(ctx, method, path, userAgent, remoteAddr, statusCode, duration)
}
