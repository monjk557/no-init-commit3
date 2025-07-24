package logging

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/collabops/git-executor/internal/config"
)

func TestNewLogger(t *testing.T) {
	tests := []struct {
		name        string
		config      *config.Config
		wantErr     bool
		errContains string
	}{
		{
			name: "valid JSON format",
			config: config.LoggingConfig{
				Level:       "info",
				Format:      "json",
				Output:      "stdout",
				Development: false,
			},
			wantErr: false,
		},
		{
			name: "valid text format",
			config: config.LoggingConfig{
				Level:       "debug",
				Format:      "text",
				Output:      "stderr",
				Development: true,
			},
			wantErr: false,
		},
		{
			name: "invalid log level",
			config: config.LoggingConfig{
				Level:  "invalid",
				Format: "json",
				Output: "stdout",
			},
			wantErr:     true,
			errContains: "invalid log level",
		},
		{
			name: "invalid log format",
			config: config.LoggingConfig{
				Level:  "info",
				Format: "invalid",
				Output: "stdout",
			},
			wantErr:     true,
			errContains: "unsupported log format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, err := NewLogger(tt.config)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Nil(t, logger)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, logger)
				assert.NotNil(t, logger.Logger)
				assert.Equal(t, tt.config, logger.config)
			}
		})
	}
}

func TestLogger_WithContext(t *testing.T) {
	cfg := config.LoggingConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	}

	tests := []struct {
		name     string
		ctx      context.Context
		expected map[string]string
	}{
		{
			name: "context with request ID",
			ctx:  context.WithValue(context.Background(), RequestIDKey, "req-123"),
			expected: map[string]string{
				"request_id": "req-123",
			},
		},
		{
			name: "context with correlation ID",
			ctx:  context.WithValue(context.Background(), CorrelationIDKey, "corr-456"),
			expected: map[string]string{
				"correlation_id": "corr-456",
			},
		},
		{
			name: "context with both IDs",
			ctx: func() context.Context {
				ctx := context.WithValue(context.Background(), RequestIDKey, "req-123")
				return context.WithValue(ctx, CorrelationIDKey, "corr-456")
			}(),
			expected: map[string]string{
				"request_id":     "req-123",
				"correlation_id": "corr-456",
			},
		},
		{
			name:     "empty context",
			ctx:      context.Background(),
			expected: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture output
			var buf bytes.Buffer
			cfg.Log.Output = "stdout"

			// Create logger that writes to buffer
			core := zapcore.NewCore(
				zapcore.NewJSONEncoder(zapcore.EncoderConfig{
					MessageKey: "message",
				}),
				zapcore.AddSync(&buf),
				zapcore.InfoLevel,
			)
			testLogger := &Logger{
				Logger: zap.New(core),
				config: cfg,
			}

			contextLogger := testLogger.WithContext(tt.ctx)
			contextLogger.Info("test message")

			// Parse output
			output := buf.String()
			if len(tt.expected) == 0 {
				// Should not contain request_id or correlation_id fields
				assert.NotContains(t, output, "request_id")
				assert.NotContains(t, output, "correlation_id")
			} else {
				for key, value := range tt.expected {
					assert.Contains(t, output, fmt.Sprintf(`"%s":"%s"`, key, value))
				}
			}
		})
	}
}

func TestLogger_HTTPRequest(t *testing.T) {
	var buf bytes.Buffer

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zapcore.EncoderConfig{
			MessageKey:  "message",
			LevelKey:    "level",
			TimeKey:     "time",
			EncodeLevel: zapcore.LowercaseLevelEncoder,
			EncodeTime:  zapcore.ISO8601TimeEncoder,
		}),
		zapcore.AddSync(&buf),
		zapcore.InfoLevel,
	)

	logger := &Logger{
		Logger: zap.New(core),
		config: config.LoggingConfig{
			Level:  "info",
			Format: "json",
			Output: "stdout",
		},
	}

	tests := []struct {
		name          string
		statusCode    int
		expectedLevel string
		expectedMsg   string
	}{
		{
			name:          "successful request",
			statusCode:    200,
			expectedLevel: "info",
			expectedMsg:   "HTTP request completed",
		},
		{
			name:          "client error",
			statusCode:    404,
			expectedLevel: "warn",
			expectedMsg:   "HTTP request client error",
		},
		{
			name:          "server error",
			statusCode:    500,
			expectedLevel: "error",
			expectedMsg:   "HTTP request failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf.Reset()

			ctx := context.WithValue(context.Background(), RequestIDKey, "test-req")
			logger.HTTPRequest(ctx, "GET", "/test", "test-agent", "127.0.0.1", tt.statusCode, time.Millisecond*100)

			output := buf.String()
			assert.Contains(t, output, fmt.Sprintf(`"level":"%s"`, tt.expectedLevel))
			assert.Contains(t, output, fmt.Sprintf(`"message":"%s"`, tt.expectedMsg))
			assert.Contains(t, output, `"method":"GET"`)
			assert.Contains(t, output, `"path":"/test"`)
			assert.Contains(t, output, `"status_code":`+fmt.Sprintf("%d", tt.statusCode))
		})
	}
}

func TestDefaultRequestIDGenerator(t *testing.T) {
	generator := &DefaultRequestIDGenerator{}

	id1 := generator.Generate()
	time.Sleep(time.Nanosecond) // Ensure different timestamp
	id2 := generator.Generate()

	assert.NotEmpty(t, id1)
	assert.NotEmpty(t, id2)
	assert.NotEqual(t, id1, id2)

	// Should be numeric timestamp
	assert.Regexp(t, `^\d+$`, id1)
	assert.Regexp(t, `^\d+$`, id2)
}

func TestInitGlobalLogger(t *testing.T) {
	// Reset global logger
	globalLogger = nil

	cfg := config.LoggingConfig{
		Level:  "info",
		Format: "json",
		Output: "stdout",
	}

	err := InitGlobalLogger(cfg)
	assert.NoError(t, err)
	assert.NotNil(t, globalLogger)

	// Test invalid config
	globalLogger = nil
	invalidCfg := &config.Config{
		Log: config.LogConfig{
			Level:  "invalid",
			Format: "json",
			Output: "stdout",
		},
	}

	err = InitGlobalLogger(invalidCfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to initialize global logger")
}

func TestGetGlobalLogger(t *testing.T) {
	// Reset global logger
	globalLogger = nil

	logger := GetGlobalLogger()
	assert.NotNil(t, logger)
	assert.NotNil(t, logger.Logger)

	// Should return same instance
	logger2 := GetGlobalLogger()
	assert.Equal(t, logger, logger2)
}

func TestLoggerFileOutput(t *testing.T) {
	// Create temporary file
	tmpFile, err := os.CreateTemp("", "test-log-*.log")
	require.NoError(t, err)
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	_ = tmpFile.Close()

	cfg := &config.Config{
		Log: config.LogConfig{
			Level:  "info",
			Format: "json",
			Output: tmpFile.Name(),
		},
	}

	logger, err := NewLogger(cfg)
	require.NoError(t, err)

	// Log a message
	logger.LogInfo(context.Background(), "test file output")

	// Sync to ensure write
	err = logger.Sync()
	require.NoError(t, err)

	// Read file content
	content, err := os.ReadFile(tmpFile.Name())
	require.NoError(t, err)

	assert.Contains(t, string(content), "test file output")
	assert.Contains(t, string(content), `"level":"info"`)
}

func TestLogLevels(t *testing.T) {
	var buf bytes.Buffer

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zapcore.EncoderConfig{
			MessageKey:  "message",
			LevelKey:    "level",
			EncodeLevel: zapcore.LowercaseLevelEncoder,
		}),
		zapcore.AddSync(&buf),
		zapcore.DebugLevel, // Set to debug to capture all levels
	)

	logger := &Logger{
		Logger: zap.New(core),
		config: config.LoggingConfig{
			Level:  "info",
			Format: "json",
			Output: "stdout",
		},
	}

	ctx := context.Background()

	tests := []struct {
		name     string
		logFunc  func()
		expected string
	}{
		{
			name: "debug",
			logFunc: func() {
				logger.LogDebug(ctx, "debug message")
			},
			expected: `"level":"debug"`,
		},
		{
			name: "info",
			logFunc: func() {
				logger.LogInfo(ctx, "info message")
			},
			expected: `"level":"info"`,
		},
		{
			name: "warning",
			logFunc: func() {
				logger.LogWarning(ctx, "warning message")
			},
			expected: `"level":"warn"`,
		},
		{
			name: "error",
			logFunc: func() {
				logger.LogError(ctx, fmt.Errorf("test error"), "error message")
			},
			expected: `"level":"error"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf.Reset()
			tt.logFunc()

			output := buf.String()
			assert.Contains(t, output, tt.expected)
		})
	}
}

func TestGlobalLoggerFunctions(t *testing.T) {
	// Reset and setup global logger
	globalLogger = nil

	var buf bytes.Buffer
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zapcore.EncoderConfig{
			MessageKey:  "message",
			LevelKey:    "level",
			EncodeLevel: zapcore.LowercaseLevelEncoder,
		}),
		zapcore.AddSync(&buf),
		zapcore.DebugLevel,
	)

	globalLogger = &Logger{
		Logger: zap.New(core),
		config: config.LoggingConfig{
			Level:  "info",
			Format: "json",
			Output: "stdout",
		},
	}

	ctx := context.Background()

	// Test global functions
	Info(ctx, "info message")
	assert.Contains(t, buf.String(), `"level":"info"`)

	buf.Reset()
	Debug(ctx, "debug message")
	assert.Contains(t, buf.String(), `"level":"debug"`)

	buf.Reset()
	Warn(ctx, "warn message")
	assert.Contains(t, buf.String(), `"level":"warn"`)

	buf.Reset()
	Error(ctx, fmt.Errorf("test error"), "error message")
	assert.Contains(t, buf.String(), `"level":"error"`)

	buf.Reset()
	HTTPRequest(ctx, "GET", "/test", "agent", "127.0.0.1", 200, time.Millisecond)
	assert.Contains(t, buf.String(), `"method":"GET"`)
}

func TestLoggerSync(t *testing.T) {
	// Create temporary file for testing sync
	tmpFile, err := os.CreateTemp("", "test-sync-*.log")
	require.NoError(t, err)
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	_ = tmpFile.Close()

	cfg := &config.Config{
		Log: config.LogConfig{
			Level:  "info",
			Format: "json",
			Output: tmpFile.Name(),
		},
	}

	logger, err := NewLogger(cfg)
	require.NoError(t, err)

	// Log something
	logger.LogInfo(context.Background(), "test sync")

	// Should not error
	err = logger.Sync()
	assert.NoError(t, err)

	err = logger.Close()
	assert.NoError(t, err)
}
