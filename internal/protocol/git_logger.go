// Package protocol implements enhanced logging for Git Smart HTTP operations
// with specific support for gateway context and debugging.
package protocol

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"go.uber.org/zap"
)

// Context key types to avoid collisions
type ContextKey string

const (
	UserIDKey     ContextKey = "user_id"
	RepositoryKey ContextKey = "repository"
	OperationKey  ContextKey = "operation"
)

// GitOperationLogger provides comprehensive logging for Git operations with gateway context
type GitOperationLogger struct {
	logger *logging.Logger
}

// NewGitOperationLogger creates a new Git operation logger
func NewGitOperationLogger(logger *logging.Logger) *GitOperationLogger {
	return &GitOperationLogger{
		logger: logger,
	}
}

// LogError logs operation errors with full context
func (gol *GitOperationLogger) LogError(operation, errorType, message string, details map[string]interface{}) {
	if gol.logger == nil {
		return
	}

	gol.logger.Error("Git operation error",
		zap.String("operation", operation),
		zap.String("error_type", errorType),
		zap.String("message", message),
		zap.Any("details", details),
		zap.String("component", "git-executor"),
	)
}

// LogProtocol logs protocol-specific events
func (gol *GitOperationLogger) LogProtocol(event string, data map[string]interface{}) {
	if gol.logger == nil {
		return
	}

	gol.logger.Debug("Git protocol event",
		zap.String("event", event),
		zap.Any("data", data),
		zap.String("component", "git-executor"),
	)
}

// RequestContext holds request-specific information for logging
type RequestContext struct {
	UserID        string
	RequestID     string
	Repository    string
	Operation     string
	Service       string
	StartTime     time.Time
	GatewayAddr   string
	UserAgent     string
	ContentLength int64
}

// ExtractRequestContext extracts context information from HTTP request
func ExtractRequestContext(r *http.Request, operation string) *RequestContext {
	owner := r.Header.Get("X-OWNER") // Try header first
	repo := r.Header.Get("X-REPO")   // Try header first

	// Fallback to URL path parsing if headers not available
	if owner == "" || repo == "" {
		// This would need to be implemented based on your URL structure
		owner = "unknown"
		repo = "unknown"
	}

	return &RequestContext{
		UserID:        r.Header.Get("X-USER-ID"),
		RequestID:     r.Header.Get("X-REQUEST-ID"),
		Repository:    fmt.Sprintf("%s/%s", owner, repo),
		Operation:     operation,
		Service:       r.URL.Query().Get("service"),
		StartTime:     time.Now(),
		GatewayAddr:   r.Header.Get("X-FORWARDED-FOR"),
		UserAgent:     r.Header.Get("User-Agent"),
		ContentLength: r.ContentLength,
	}
}

// ToContext adds request context to Go context
func (rc *RequestContext) ToContext(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, logging.RequestIDKey, rc.RequestID)
	ctx = context.WithValue(ctx, UserIDKey, rc.UserID)
	ctx = context.WithValue(ctx, RepositoryKey, rc.Repository)
	ctx = context.WithValue(ctx, OperationKey, rc.Operation)
	return ctx
}

// LogRequestStart logs the beginning of a Git operation
func (gol *GitOperationLogger) LogRequestStart(ctx context.Context, reqCtx *RequestContext) {
	if gol.logger == nil {
		return
	}

	gol.logger.LogInfo(ctx, "Git operation started",
		zap.String("operation", reqCtx.Operation),
		zap.String("service", reqCtx.Service),
		zap.String("repository", reqCtx.Repository),
		zap.String("user_id", reqCtx.UserID),
		zap.String("request_id", reqCtx.RequestID),
		zap.String("gateway_addr", reqCtx.GatewayAddr),
		zap.String("user_agent", reqCtx.UserAgent),
		zap.Int64("content_length", reqCtx.ContentLength),
		zap.String("component", "git-executor"),
	)
}

// LogRequestEnd logs the completion of a Git operation
func (gol *GitOperationLogger) LogRequestEnd(ctx context.Context, reqCtx *RequestContext, statusCode int, bytesWritten int64, err error) {
	if gol.logger == nil {
		return
	}

	duration := time.Since(reqCtx.StartTime)

	fields := []zap.Field{
		zap.String("operation", reqCtx.Operation),
		zap.String("repository", reqCtx.Repository),
		zap.String("user_id", reqCtx.UserID),
		zap.String("request_id", reqCtx.RequestID),
		zap.Int("status_code", statusCode),
		zap.Int64("bytes_written", bytesWritten),
		zap.Duration("duration", duration),
		zap.String("component", "git-executor"),
	}

	if err != nil {
		gol.logger.LogError(ctx, err, "Git operation completed with error", fields...)
	} else {
		gol.logger.LogInfo(ctx, "Git operation completed successfully", fields...)
	}
}

// LogAuthentication logs authentication and authorization events
func (gol *GitOperationLogger) LogAuthentication(ctx context.Context, reqCtx *RequestContext, authResult string, details map[string]interface{}) {
	if gol.logger == nil {
		return
	}

	gol.logger.LogInfo(ctx, "Authentication event",
		zap.String("operation", reqCtx.Operation),
		zap.String("repository", reqCtx.Repository),
		zap.String("user_id", reqCtx.UserID),
		zap.String("request_id", reqCtx.RequestID),
		zap.String("auth_result", authResult),
		zap.Any("details", details),
		zap.String("component", "git-executor"),
	)
}

// LogRepositoryAccess logs repository access attempts and results
func (gol *GitOperationLogger) LogRepositoryAccess(ctx context.Context, reqCtx *RequestContext, repoPath string, accessResult string, details map[string]interface{}) {
	if gol.logger == nil {
		return
	}

	gol.logger.LogInfo(ctx, "Repository access event",
		zap.String("operation", reqCtx.Operation),
		zap.String("repository", reqCtx.Repository),
		zap.String("repo_path", repoPath),
		zap.String("user_id", reqCtx.UserID),
		zap.String("request_id", reqCtx.RequestID),
		zap.String("access_result", accessResult),
		zap.Any("details", details),
		zap.String("component", "git-executor"),
	)
}

// LogProtocolNegotiation logs protocol capability negotiation
func (gol *GitOperationLogger) LogProtocolNegotiation(ctx context.Context, reqCtx *RequestContext, phase string, details map[string]interface{}) {
	if gol.logger == nil {
		return
	}

	gol.logger.LogDebug(ctx, "Protocol negotiation",
		zap.String("operation", reqCtx.Operation),
		zap.String("repository", reqCtx.Repository),
		zap.String("user_id", reqCtx.UserID),
		zap.String("request_id", reqCtx.RequestID),
		zap.String("phase", phase),
		zap.Any("details", details),
		zap.String("component", "git-executor"),
	)
}

// LogGatewayInteraction logs specific gateway communication events
func (gol *GitOperationLogger) LogGatewayInteraction(ctx context.Context, reqCtx *RequestContext, interaction string, details map[string]interface{}) {
	if gol.logger == nil {
		return
	}

	gol.logger.LogDebug(ctx, "Gateway interaction",
		zap.String("operation", reqCtx.Operation),
		zap.String("repository", reqCtx.Repository),
		zap.String("user_id", reqCtx.UserID),
		zap.String("request_id", reqCtx.RequestID),
		zap.String("gateway_addr", reqCtx.GatewayAddr),
		zap.String("interaction", interaction),
		zap.Any("details", details),
		zap.String("component", "git-executor"),
	)
}

// LogSecurityEvent logs security-related events
func (gol *GitOperationLogger) LogSecurityEvent(ctx context.Context, reqCtx *RequestContext, eventType string, severity string, details map[string]interface{}) {
	if gol.logger == nil {
		return
	}

	message := "Security event"
	fields := []zap.Field{
		zap.String("operation", reqCtx.Operation),
		zap.String("repository", reqCtx.Repository),
		zap.String("user_id", reqCtx.UserID),
		zap.String("request_id", reqCtx.RequestID),
		zap.String("gateway_addr", reqCtx.GatewayAddr),
		zap.String("event_type", eventType),
		zap.String("severity", severity),
		zap.Any("details", details),
		zap.String("component", "git-executor"),
	}

	switch severity {
	case "critical", "high":
		gol.logger.LogError(ctx, fmt.Errorf("security event: %s", eventType), message, fields...)
	case "medium":
		gol.logger.LogWarning(ctx, message, fields...)
	default:
		gol.logger.LogInfo(ctx, message, fields...)
	}
}

// CreateDefaultGitLogger creates a default Git operation logger
func CreateDefaultGitLogger() (*GitOperationLogger, error) {
	// Use the existing logging configuration
	cfg := config.LoggingConfig{
		Level:  "debug",
		Format: "json",
		Output: "", // Use stdout
	}

	logger, err := logging.NewLogger(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	return NewGitOperationLogger(logger), nil
}
