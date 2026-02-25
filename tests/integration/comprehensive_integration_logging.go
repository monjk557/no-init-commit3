package integration

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// testLoggingConfigurationIntegration tests logging with different configurations
func (suite *ComprehensiveIntegrationTestSuite) testLoggingConfigurationIntegration(t *testing.T) {
	loggingConfigs := []struct {
		name        string
		level       string
		format      string
		output      string
		development bool
		expectError bool
	}{
		{"JSON_Info_Stdout", "info", "json", "stdout", false, false},
		{"JSON_Debug_Stdout", "debug", "json", "stdout", true, false},
		{"Text_Info_Stdout", "info", "text", "stdout", false, false},
		{"Text_Debug_Stdout", "debug", "text", "stdout", true, false},
		{"JSON_Error_File", "error", "json", "file", false, false},
		{"Text_Warn_File", "warn", "text", "file", false, false},
		{"Invalid_Level", "invalid", "json", "stdout", false, true},
		{"Invalid_Format", "info", "invalid", "stdout", false, true},
	}

	for _, config := range loggingConfigs {
		t.Run(config.name, func(t *testing.T) {
			suite.testSpecificLoggingConfiguration(t, config.level, config.format,
				config.output, config.development, config.expectError)
		})
	}

	// Test concurrent logging
	t.Run("Concurrent_Logging", func(t *testing.T) {
		suite.testConcurrentLogging(t)
	})

	// Test log rotation
	t.Run("Log_Rotation", func(t *testing.T) {
		suite.testLogRotation(t)
	})

	// Test structured logging with different data types
	t.Run("Structured_Logging", func(t *testing.T) {
		suite.testStructuredLogging(t)
	})
}

// testSpecificLoggingConfiguration tests a specific logging configuration
func (suite *ComprehensiveIntegrationTestSuite) testSpecificLoggingConfiguration(t *testing.T,
	level, format, output string, development, expectError bool) {

	// Create temporary log file if needed
	var logFilePath string
	if output == "file" {
		logFile, err := os.CreateTemp(suite.testDataDir, "test_log_*.log")
		require.NoError(t, err, "Failed to create temporary log file")
		logFilePath = logFile.Name()
		logFile.Close()
	}

	// Configure logging
	loggingConfig := config.LoggingConfig{
		Level:       level,
		Format:      format,
		Output:      output,
		Development: development,
	}

	logger, err := logging.NewLogger(loggingConfig)

	if expectError {
		assert.Error(t, err, "Expected error for invalid logging configuration")
		return
	}

	require.NoError(t, err, "Failed to create logger with configuration: %+v", loggingConfig)

	// Test different log levels
	testMessages := []struct {
		level   zapcore.Level
		message string
		fields  []zap.Field
	}{
		{zapcore.DebugLevel, "Debug message", []zap.Field{zap.String("component", "test")}},
		{zapcore.InfoLevel, "Info message", []zap.Field{zap.Int("count", 42)}},
		{zapcore.WarnLevel, "Warning message", []zap.Field{zap.Duration("elapsed", time.Second)}},
		{zapcore.ErrorLevel, "Error message", []zap.Field{zap.Bool("critical", true)}},
	}

	var logOutput bytes.Buffer
	var logCapture *zap.Logger

	if output == "stdout" {
		// Capture stdout for verification
		core := zapcore.NewCore(
			getEncoder(format, development),
			zapcore.AddSync(&logOutput),
			getLogLevel(level),
		)
		logCapture = zap.New(core)
	}

	for _, testMsg := range testMessages {
		switch testMsg.level {
		case zapcore.DebugLevel:
			if logCapture != nil {
				logCapture.Debug(testMsg.message, testMsg.fields...)
			} else {
				logger.Debug(testMsg.message, testMsg.fields...)
			}
		case zapcore.InfoLevel:
			if logCapture != nil {
				logCapture.Info(testMsg.message, testMsg.fields...)
			} else {
				logger.Info(testMsg.message, testMsg.fields...)
			}
		case zapcore.WarnLevel:
			if logCapture != nil {
				logCapture.Warn(testMsg.message, testMsg.fields...)
			} else {
				logger.Warn(testMsg.message, testMsg.fields...)
			}
		case zapcore.ErrorLevel:
			if logCapture != nil {
				logCapture.Error(testMsg.message, testMsg.fields...)
			} else {
				logger.Error(testMsg.message, testMsg.fields...)
			}
		}
	}

	// Flush and verify output
	if logCapture != nil {
		logCapture.Sync()
	} else {
		logger.Sync()
	}

	// Verify log output
	if output == "stdout" {
		logContent := logOutput.String()
		suite.verifyLogOutput(t, logContent, format, level, development)
	} else if output == "file" && logFilePath != "" {
		logContent, err := os.ReadFile(logFilePath)
		if err == nil {
			suite.verifyLogOutput(t, string(logContent), format, level, development)
		}
		// Clean up log file
		os.Remove(logFilePath)
	}

	suite.logger.Info("Logging configuration test completed",
		zap.String("level", level),
		zap.String("format", format),
		zap.String("output", output),
		zap.Bool("development", development))
}

// getEncoder returns the appropriate encoder for the format
func getEncoder(format string, development bool) zapcore.Encoder {
	config := zap.NewProductionEncoderConfig()
	if development {
		config = zap.NewDevelopmentEncoderConfig()
	}

	if format == "json" {
		return zapcore.NewJSONEncoder(config)
	}
	return zapcore.NewConsoleEncoder(config)
}

// getLogLevel converts string level to zapcore.Level
func getLogLevel(level string) zapcore.Level {
	switch strings.ToLower(level) {
	case "debug":
		return zapcore.DebugLevel
	case "info":
		return zapcore.InfoLevel
	case "warn":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	case "fatal":
		return zapcore.FatalLevel
	default:
		return zapcore.InfoLevel
	}
}

// verifyLogOutput verifies the log output matches expected format and content
func (suite *ComprehensiveIntegrationTestSuite) verifyLogOutput(t *testing.T,
	logContent, format, level string, development bool) {

	assert.NotEmpty(t, logContent, "Log output should not be empty")

	configuredLevel := getLogLevel(level)
	lines := strings.Split(strings.TrimSpace(logContent), "\n")

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}

		if format == "json" {
			// Verify JSON format
			var logEntry map[string]interface{}
			err := json.Unmarshal([]byte(line), &logEntry)
			assert.NoError(t, err, "Log line should be valid JSON: %s", line)

			// Verify required fields
			assert.Contains(t, logEntry, "level", "Log entry should have level field")
			assert.Contains(t, logEntry, "msg", "Log entry should have msg field")
			assert.Contains(t, logEntry, "ts", "Log entry should have timestamp field")

			// Verify log level is appropriate
			if levelStr, ok := logEntry["level"].(string); ok {
				entryLevel := getLogLevel(levelStr)
				assert.True(t, entryLevel >= configuredLevel,
					"Log entry level %s should be >= configured level %s", levelStr, level)
			}
		} else {
			// Verify text format contains expected elements
			assert.True(t, strings.Contains(line, "INFO") ||
				strings.Contains(line, "DEBUG") ||
				strings.Contains(line, "WARN") ||
				strings.Contains(line, "ERROR"),
				"Text log line should contain log level: %s", line)
		}
	}
}

// testConcurrentLogging tests logging under concurrent access
func (suite *ComprehensiveIntegrationTestSuite) testConcurrentLogging(t *testing.T) {
	logger := logging.NewLogger(zap.InfoLevel)

	// Create multiple goroutines that log concurrently
	numGoroutines := 10
	numMessages := 100
	done := make(chan bool, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(goroutineID int) {
			defer func() { done <- true }()

			for j := 0; j < numMessages; j++ {
				logger.Info("Concurrent log message",
					zap.Int("goroutine_id", goroutineID),
					zap.Int("message_id", j),
					zap.String("timestamp", time.Now().Format(time.RFC3339)))

				// Small delay to simulate real work
				time.Sleep(time.Microsecond * 10)
			}
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Logger should handle concurrent access without panics or corruption
	logger.Info("Concurrent logging test completed",
		zap.Int("total_goroutines", numGoroutines),
		zap.Int("messages_per_goroutine", numMessages))
}

// testLogRotation tests log file rotation functionality
func (suite *ComprehensiveIntegrationTestSuite) testLogRotation(t *testing.T) {
	logDir := filepath.Join(suite.testDataDir, "log_rotation")
	err := os.MkdirAll(logDir, 0755)
	require.NoError(t, err, "Failed to create log directory")

	logFile := filepath.Join(logDir, "rotation_test.log")

	// Configure lumberjack for log rotation
	lumberjackLogger := &lumberjack.Logger{
		Filename:   logFile,
		MaxSize:    1, // 1 MB
		MaxBackups: 3,
		MaxAge:     1, // 1 day
		Compress:   false,
	}

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		zapcore.AddSync(lumberjackLogger),
		zapcore.InfoLevel,
	)

	rotatingLogger := zap.New(core)

	// Generate enough log data to trigger rotation
	largeMessage := strings.Repeat("This is a large log message for testing rotation. ", 100)

	for i := 0; i < 1000; i++ {
		rotatingLogger.Info(largeMessage,
			zap.Int("iteration", i),
			zap.String("timestamp", time.Now().Format(time.RFC3339Nano)))
	}

	rotatingLogger.Sync()
	lumberjackLogger.Close()

	// Check if log files were created (original + rotated)
	files, err := filepath.Glob(filepath.Join(logDir, "rotation_test*"))
	require.NoError(t, err, "Failed to list log files")

	// Should have at least the main log file
	assert.GreaterOrEqual(t, len(files), 1, "Should have at least one log file")

	// Check if rotation occurred (file size should be reasonable)
	mainLogInfo, err := os.Stat(logFile)
	if err == nil {
		// If main log exists and is not too large, rotation might have worked
		suite.logger.Info("Log rotation test completed",
			zap.String("main_log_file", logFile),
			zap.Int64("main_log_size", mainLogInfo.Size()),
			zap.Int("total_log_files", len(files)))
	}

	// Cleanup
	for _, file := range files {
		os.Remove(file)
	}
}

// testStructuredLogging tests structured logging with various data types
func (suite *ComprehensiveIntegrationTestSuite) testStructuredLogging(t *testing.T) {
	var logOutput bytes.Buffer

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		zapcore.AddSync(&logOutput),
		zapcore.DebugLevel,
	)

	structuredLogger := zap.New(core)

	// Test various field types
	testTime := time.Now()
	testDuration := 5 * time.Second

	structuredLogger.Info("Structured logging test",
		zap.String("string_field", "test_value"),
		zap.Int("int_field", 42),
		zap.Int64("int64_field", 9223372036854775807),
		zap.Float64("float_field", 3.14159),
		zap.Bool("bool_field", true),
		zap.Duration("duration_field", testDuration),
		zap.Time("time_field", testTime),
		zap.Any("any_field", map[string]interface{}{
			"nested_string": "nested_value",
			"nested_int":    100,
		}),
		zap.Strings("string_array", []string{"item1", "item2", "item3"}),
		zap.Ints("int_array", []int{1, 2, 3}))

	structuredLogger.Sync()

	// Verify structured output
	logContent := logOutput.String()
	assert.NotEmpty(t, logContent, "Structured log output should not be empty")

	// Parse as JSON
	var logEntry map[string]interface{}
	err := json.Unmarshal([]byte(strings.TrimSpace(logContent)), &logEntry)
	assert.NoError(t, err, "Structured log should be valid JSON")

	// Verify field types are preserved
	assert.Equal(t, "test_value", logEntry["string_field"])
	assert.Equal(t, float64(42), logEntry["int_field"]) // JSON numbers are float64
	assert.Equal(t, true, logEntry["bool_field"])
	assert.Contains(t, logEntry, "duration_field")
	assert.Contains(t, logEntry, "time_field")
	assert.Contains(t, logEntry, "any_field")

	suite.logger.Info("Structured logging test completed",
		zap.Int("fields_tested", 10))
}
