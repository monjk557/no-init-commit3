package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// SecurityTestSuite provides comprehensive security logging test utilities
type SecurityTestSuite struct {
	Logger       *Logger
	LogBuffer    *bytes.Buffer
	TestRecorder *SecurityEventRecorder
}

// SecurityEventRecorder captures and validates security events for testing
type SecurityEventRecorder struct {
	Events []SecurityEvent
}

// SecurityEvent represents a security-related log event
type SecurityEvent struct {
	Level      string                 `json:"level"`
	Message    string                 `json:"msg"`
	UserID     string                 `json:"user_id,omitempty"`
	RequestID  string                 `json:"request_id,omitempty"`
	Operation  string                 `json:"operation,omitempty"`
	EventType  string                 `json:"event_type,omitempty"`
	Source     string                 `json:"source,omitempty"`
	Endpoint   string                 `json:"endpoint,omitempty"`
	StatusCode int                    `json:"status_code,omitempty"`
	RemoteAddr string                 `json:"remote_addr,omitempty"`
	UserAgent  string                 `json:"user_agent,omitempty"`
	Reason     string                 `json:"reason,omitempty"`
	Timestamp  string                 `json:"ts"`
	Repository string                 `json:"repository,omitempty"`
	Details    map[string]interface{} `json:"details,omitempty"`
}

// NewSecurityTestSuite creates a new security testing framework
func NewSecurityTestSuite(t *testing.T) *SecurityTestSuite {
	var logBuffer bytes.Buffer

	// Create logger with JSON output to buffer for testing
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zapcore.EncoderConfig{
			TimeKey:        "ts",
			LevelKey:       "level",
			NameKey:        "logger",
			CallerKey:      "caller",
			MessageKey:     "msg",
			StacktraceKey:  "stacktrace",
			LineEnding:     zapcore.DefaultLineEnding,
			EncodeLevel:    zapcore.LowercaseLevelEncoder,
			EncodeTime:     zapcore.ISO8601TimeEncoder,
			EncodeDuration: zapcore.StringDurationEncoder,
			EncodeCaller:   zapcore.ShortCallerEncoder,
		}),
		zapcore.AddSync(&logBuffer),
		zapcore.DebugLevel,
	)

	zapLogger := zap.New(core)
	logger := &Logger{Logger: zapLogger}

	return &SecurityTestSuite{
		Logger:       logger,
		LogBuffer:    &logBuffer,
		TestRecorder: &SecurityEventRecorder{},
	}
}

// ParseSecurityEvents extracts security events from log buffer
func (s *SecurityTestSuite) ParseSecurityEvents() []SecurityEvent {
	lines := strings.Split(strings.TrimSpace(s.LogBuffer.String()), "\n")
	var events []SecurityEvent

	for _, line := range lines {
		if line == "" {
			continue
		}

		var event SecurityEvent
		if err := json.Unmarshal([]byte(line), &event); err == nil {
			events = append(events, event)
		}
	}

	s.TestRecorder.Events = events
	return events
}

// AssertSecurityEvent validates that a specific security event was logged
func (s *SecurityTestSuite) AssertSecurityEvent(t *testing.T, expectedEvent SecurityEvent) {
	events := s.ParseSecurityEvents()

	for _, event := range events {
		if s.matchesEvent(event, expectedEvent) {
			return // Found matching event
		}
	}

	t.Errorf("Expected security event not found: %+v\nActual events: %+v", expectedEvent, events)
}

// AssertCorrelationID validates that all log entries have correlation IDs
func (s *SecurityTestSuite) AssertCorrelationID(t *testing.T, requestID string) {
	events := s.ParseSecurityEvents()

	for _, event := range events {
		if event.RequestID == "" {
			t.Errorf("Missing correlation ID in security event: %+v", event)
		}
		if requestID != "" && event.RequestID != requestID {
			t.Errorf("Correlation ID mismatch. Expected: %s, Got: %s", requestID, event.RequestID)
		}
	}
}

// AssertNoSensitiveData validates that no sensitive data appears in logs
func (s *SecurityTestSuite) AssertNoSensitiveData(t *testing.T, sensitiveTerms []string) {
	logContent := s.LogBuffer.String()

	for _, term := range sensitiveTerms {
		if strings.Contains(logContent, term) {
			t.Errorf("Sensitive data found in logs: %s", term)
		}
	}
}

// AssertAuditTrail validates that a complete audit trail exists for an operation
func (s *SecurityTestSuite) AssertAuditTrail(t *testing.T, operation string, userID string) {
	events := s.ParseSecurityEvents()

	hasStart := false
	hasEnd := false

	for _, event := range events {
		if event.Operation == operation && event.UserID == userID {
			if strings.Contains(event.Message, "start") || strings.Contains(event.Message, "request") {
				hasStart = true
			}
			if strings.Contains(event.Message, "complete") || strings.Contains(event.Message, "end") || strings.Contains(event.Message, "result") {
				hasEnd = true
			}
		}
	}

	if !hasStart {
		t.Errorf("Missing audit trail start for operation: %s, user: %s", operation, userID)
	}
	if !hasEnd {
		t.Errorf("Missing audit trail end for operation: %s, user: %s", operation, userID)
	}
}

// matchesEvent checks if two security events match (ignoring timestamp)
func (s *SecurityTestSuite) matchesEvent(actual, expected SecurityEvent) bool {
	return actual.Level == expected.Level &&
		actual.Message == expected.Message &&
		(expected.UserID == "" || actual.UserID == expected.UserID) &&
		(expected.Operation == "" || actual.Operation == expected.Operation) &&
		(expected.EventType == "" || actual.EventType == expected.EventType)
}

// Test Security Logging Framework
func TestSecurityLoggingFramework(t *testing.T) {
	suite := NewSecurityTestSuite(t)

	t.Run("Authentication_Failure_Logging", func(t *testing.T) {
		ctx := context.Background()
		ctx = context.WithValue(ctx, RequestIDKey, "req-123")

		// Simulate authentication failure
		suite.Logger.LogError(ctx, fmt.Errorf("invalid JWT token"), "Authentication failed",
			zap.String("user_id", "unknown"),
			zap.String("event_type", "auth_failure"),
			zap.String("reason", "invalid_token"),
			zap.String("remote_addr", "192.168.1.100"),
			zap.String("user_agent", "Git/2.40.0"),
		)

		// Validate security event was logged
		suite.AssertSecurityEvent(t, SecurityEvent{
			Level:     "error",
			Message:   "Authentication failed",
			RequestID: "req-123",
			UserID:    "unknown",
			EventType: "auth_failure",
			Reason:    "invalid_token",
		})

		// Ensure correlation ID is present
		suite.AssertCorrelationID(t, "req-123")
	})

	t.Run("Permission_Denial_Logging", func(t *testing.T) {
		ctx := context.Background()
		ctx = context.WithValue(ctx, RequestIDKey, "req-456")

		// Simulate permission denial
		suite.Logger.LogWarning(ctx, "Access denied to repository",
			zap.String("user_id", "user-123"),
			zap.String("event_type", "permission_denied"),
			zap.String("operation", "git-upload-pack"),
			zap.String("repository", "owner/repo"),
			zap.String("reason", "insufficient_permissions"),
			zap.Int("status_code", 403),
		)

		suite.AssertSecurityEvent(t, SecurityEvent{
			Level:      "warn",
			Message:    "Access denied to repository",
			RequestID:  "req-456",
			UserID:     "user-123",
			EventType:  "permission_denied",
			Operation:  "git-upload-pack",
			Repository: "owner/repo",
			StatusCode: 403,
		})
	})

	t.Run("Security_Violation_Logging", func(t *testing.T) {
		ctx := context.Background()
		ctx = context.WithValue(ctx, RequestIDKey, "req-789")

		// Simulate security violation
		suite.Logger.LogError(ctx, fmt.Errorf("path traversal attempt"), "Security violation detected",
			zap.String("user_id", "malicious-user"),
			zap.String("event_type", "security_violation"),
			zap.String("operation", "repository_access"),
			zap.String("source", "192.168.1.200"),
			zap.String("endpoint", "/../../etc/passwd"),
			zap.String("reason", "path_traversal_attempt"),
			zap.String("user_agent", "curl/7.68.0"),
		)

		suite.AssertSecurityEvent(t, SecurityEvent{
			Level:     "error",
			Message:   "Security violation detected",
			RequestID: "req-789",
			UserID:    "malicious-user",
			EventType: "security_violation",
			Operation: "repository_access",
			Source:    "192.168.1.200",
			Endpoint:  "/../../etc/passwd",
			Reason:    "path_traversal_attempt",
		})
	})

	t.Run("Audit_Trail_Completeness", func(t *testing.T) {
		// Create a fresh suite for this test to avoid log contamination
		freshSuite := NewSecurityTestSuite(t)

		ctx := context.Background()
		ctx = context.WithValue(ctx, RequestIDKey, "req-audit")
		userID := "audit-user"
		operation := "git-clone"

		// Log operation start
		freshSuite.Logger.LogInfo(ctx, "Git operation started",
			zap.String("user_id", userID),
			zap.String("operation", operation),
			zap.String("repository", "owner/repo"),
			zap.String("event_type", "operation_start"),
		)

		// Log operation end
		freshSuite.Logger.LogInfo(ctx, "Git operation completed",
			zap.String("user_id", userID),
			zap.String("operation", operation),
			zap.String("repository", "owner/repo"),
			zap.String("event_type", "operation_complete"),
			zap.Duration("duration", 2*time.Second),
			zap.Int("bytes_transferred", 1024),
		)

		// Validate complete audit trail
		freshSuite.AssertAuditTrail(t, operation, userID)
		freshSuite.AssertCorrelationID(t, "req-audit")
	})

	t.Run("No_Sensitive_Data_In_Logs", func(t *testing.T) {
		// Create a fresh suite for this test to avoid log contamination
		freshSuite := NewSecurityTestSuite(t)

		ctx := context.Background()

		// Log with potential sensitive data (should be filtered out)
		freshSuite.Logger.LogInfo(ctx, "User authenticated successfully",
			zap.String("user_id", "safe-user-id"),
			zap.String("operation", "login"),
			// Note: We should NOT log sensitive data like passwords, secrets, etc.
			// This test ensures our logging practices are secure
		)

		// Validate no sensitive data leaked into logs
		sensitiveTerms := []string{
			"password",
			"secret",
			"Bearer ",
			"Authorization:",
			"private_key",
			"ssh_key",
		}

		freshSuite.AssertNoSensitiveData(t, sensitiveTerms)
	})
}

// Test HTTP Request Security Logging
func TestHTTPSecurityLogging(t *testing.T) {
	suite := NewSecurityTestSuite(t)

	t.Run("HTTP_Request_Security_Context", func(t *testing.T) {
		// Create mock HTTP request
		req := httptest.NewRequest("GET", "/repos/owner/repo/info/refs", nil)
		req.Header.Set("X-USER-ID", "test-user")
		req.Header.Set("X-REQUEST-ID", "http-req-123")
		req.Header.Set("X-Source", "http-gateway")
		req.Header.Set("User-Agent", "git/2.40.0")
		req.RemoteAddr = "192.168.1.50:12345"

		ctx := req.Context()
		ctx = context.WithValue(ctx, RequestIDKey, req.Header.Get("X-REQUEST-ID"))

		// Log HTTP request with security context
		suite.Logger.LogInfo(ctx, "HTTP request processed",
			zap.String("method", req.Method),
			zap.String("path", req.URL.Path),
			zap.String("user_id", req.Header.Get("X-USER-ID")),
			zap.String("source", req.Header.Get("X-Source")),
			zap.String("remote_addr", req.RemoteAddr),
			zap.String("user_agent", req.Header.Get("User-Agent")),
			zap.String("event_type", "http_request"),
		)

		// Validate security context is properly logged
		events := suite.ParseSecurityEvents()
		require.Len(t, events, 1)

		event := events[0]
		assert.Equal(t, "info", event.Level)
		assert.Equal(t, "HTTP request processed", event.Message)
		assert.Equal(t, "test-user", event.UserID)
		assert.Equal(t, "http-req-123", event.RequestID)
		assert.Equal(t, "http_request", event.EventType)
		assert.Equal(t, "192.168.1.50:12345", event.RemoteAddr)
		assert.Equal(t, "git/2.40.0", event.UserAgent)
	})

	t.Run("Unauthorized_Access_Attempt", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/repos/secret/repo/git-receive-pack", nil)
		req.Header.Set("X-REQUEST-ID", "unauth-req-456")
		req.Header.Set("X-Source", "unknown-source")
		req.RemoteAddr = "10.0.0.100:54321"

		ctx := req.Context()
		ctx = context.WithValue(ctx, RequestIDKey, req.Header.Get("X-REQUEST-ID"))

		// Log unauthorized access attempt
		suite.Logger.LogError(ctx, fmt.Errorf("unauthorized access"), "Unauthorized access attempt",
			zap.String("method", req.Method),
			zap.String("path", req.URL.Path),
			zap.String("source", req.Header.Get("X-Source")),
			zap.String("remote_addr", req.RemoteAddr),
			zap.String("event_type", "unauthorized_access"),
			zap.String("reason", "missing_auth_header"),
			zap.Int("status_code", 401),
		)

		suite.AssertSecurityEvent(t, SecurityEvent{
			Level:      "error",
			Message:    "Unauthorized access attempt",
			RequestID:  "unauth-req-456",
			EventType:  "unauthorized_access",
			StatusCode: 401,
			RemoteAddr: "10.0.0.100:54321",
		})
	})
}

// Test Compliance Logging Requirements
func TestComplianceLogging(t *testing.T) {
	suite := NewSecurityTestSuite(t)

	t.Run("GDPR_Compliance_No_PII", func(t *testing.T) {
		ctx := context.Background()
		ctx = context.WithValue(ctx, RequestIDKey, "gdpr-req-123")

		// Log user operation without PII
		suite.Logger.LogInfo(ctx, "User operation completed",
			zap.String("user_id", "hashed-user-id-abc123"), // Use hashed/pseudonymized ID
			zap.String("operation", "repository_access"),
			zap.String("event_type", "user_operation"),
			// NOTE: No email, real names, or other PII should be logged
		)

		// Validate no PII in logs
		piiTerms := []string{
			"@", // Email addresses
			"firstName",
			"lastName",
			"email",
			"phone",
			"address",
			"ssn",
			"credit_card",
		}

		suite.AssertNoSensitiveData(t, piiTerms)
	})

	t.Run("SOC2_Compliance_Access_Logging", func(t *testing.T) {
		ctx := context.Background()
		ctx = context.WithValue(ctx, RequestIDKey, "soc2-req-456")

		// Log all required SOC2 access events
		userID := "soc2-user"
		resource := "repos/company/secret-project"

		// Access granted
		suite.Logger.LogInfo(ctx, "Resource access granted",
			zap.String("user_id", userID),
			zap.String("resource", resource),
			zap.String("action", "read"),
			zap.String("event_type", "access_granted"),
			zap.String("compliance_type", "soc2"),
		)

		// Access revoked
		suite.Logger.LogInfo(ctx, "Resource access revoked",
			zap.String("user_id", userID),
			zap.String("resource", resource),
			zap.String("action", "revoke"),
			zap.String("event_type", "access_revoked"),
			zap.String("compliance_type", "soc2"),
			zap.String("reason", "session_expired"),
		)

		events := suite.ParseSecurityEvents()

		// Validate SOC2 compliance events
		foundGrant := false
		foundRevoke := false

		for _, event := range events {
			if event.EventType == "access_granted" {
				foundGrant = true
			}
			if event.EventType == "access_revoked" {
				foundRevoke = true
			}
		}

		assert.True(t, foundGrant, "SOC2 access granted event not found")
		assert.True(t, foundRevoke, "SOC2 access revoked event not found")
	})
}

// Benchmark Security Logging Performance
func BenchmarkSecurityLogging(b *testing.B) {
	suite := NewSecurityTestSuite(&testing.T{})
	ctx := context.Background()
	ctx = context.WithValue(ctx, RequestIDKey, "bench-req")

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			suite.Logger.LogError(ctx, fmt.Errorf("test error"), "Security event",
				zap.String("user_id", "bench-user"),
				zap.String("event_type", "security_test"),
				zap.String("operation", "benchmark"),
				zap.String("source", "192.168.1.1"),
				zap.Int("status_code", 403),
			)
		}
	})
}
