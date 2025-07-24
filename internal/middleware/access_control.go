// Package middleware implements HTTP middleware for the Git executor
package middleware

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/collabops/git-executor/internal/logging"
	"go.uber.org/zap"
)

// SourceType represents the type of calling component
type SourceType string

const (
	// SourceHTTPGateway represents the HTTP Git gateway
	SourceHTTPGateway SourceType = "http-gateway"
	// SourceSSHGateway represents the SSH Git gateway
	SourceSSHGateway SourceType = "ssh-gateway"
	// SourceOrchestrator represents the orchestrator service
	SourceOrchestrator SourceType = "orchestrator"
)

// EndpointType represents the type of endpoint being accessed
type EndpointType string

const (
	// EndpointGitProtocol represents Git Smart HTTP protocol endpoints
	EndpointGitProtocol EndpointType = "git-protocol"
	// EndpointSSHProtocol represents SSH Git protocol endpoints
	EndpointSSHProtocol EndpointType = "ssh-protocol"
	// EndpointRESTAPI represents REST API endpoints
	EndpointRESTAPI EndpointType = "rest-api"
	// EndpointCommon represents common endpoints (health, metrics)
	EndpointCommon EndpointType = "common"
)

// AccessControlRule defines access permissions for source-endpoint combinations
type AccessControlRule struct {
	Source   SourceType
	Endpoint EndpointType
	Allowed  bool
}

// AccessControlConfig configures the access control middleware
type AccessControlConfig struct {
	// Rules defines the access control matrix
	Rules []AccessControlRule
	// DenyByDefault determines if unknown combinations should be denied
	DenyByDefault bool
	// LogSecurityEvents enables logging of security violations
	LogSecurityEvents bool
}

// AccessControlMiddleware enforces endpoint access control based on X-Source header
type AccessControlMiddleware struct {
	config *AccessControlConfig
	logger *logging.Logger

	// Compiled regex patterns for endpoint classification
	gitProtocolPattern *regexp.Regexp
	sshProtocolPattern *regexp.Regexp
	restAPIPattern     *regexp.Regexp
	commonPattern      *regexp.Regexp
}

// NewAccessControlMiddleware creates a new access control middleware
func NewAccessControlMiddleware(config *AccessControlConfig, logger *logging.Logger) (*AccessControlMiddleware, error) {
	if config == nil {
		config = DefaultAccessControlConfig()
	}

	if logger == nil {
		return nil, fmt.Errorf("logger is required")
	}

	// Compile endpoint classification patterns
	gitProtocolPattern, err := regexp.Compile(`^/[^/]+/[^/]+/(info/refs|git-upload-pack|git-receive-pack)`)
	if err != nil {
		return nil, fmt.Errorf("failed to compile git protocol pattern: %w", err)
	}

	sshProtocolPattern, err := regexp.Compile(`^/ssh/(upload-pack|receive-pack)`)
	if err != nil {
		return nil, fmt.Errorf("failed to compile ssh protocol pattern: %w", err)
	}

	restAPIPattern, err := regexp.Compile(`^/repos/[^/]+/[^/]+/(tree|blob|commits|diff|file-diff|patch)`)
	if err != nil {
		return nil, fmt.Errorf("failed to compile rest api pattern: %w", err)
	}

	commonPattern, err := regexp.Compile(`^/(health|metrics|)$`)
	if err != nil {
		return nil, fmt.Errorf("failed to compile common pattern: %w", err)
	}

	return &AccessControlMiddleware{
		config:             config,
		logger:             logger,
		gitProtocolPattern: gitProtocolPattern,
		sshProtocolPattern: sshProtocolPattern,
		restAPIPattern:     restAPIPattern,
		commonPattern:      commonPattern,
	}, nil
}

// DefaultAccessControlConfig returns the default access control configuration
func DefaultAccessControlConfig() *AccessControlConfig {
	return &AccessControlConfig{
		Rules: []AccessControlRule{
			// HTTP Gateway access rules
			{SourceHTTPGateway, EndpointGitProtocol, true},
			{SourceHTTPGateway, EndpointSSHProtocol, false},
			{SourceHTTPGateway, EndpointRESTAPI, false},
			{SourceHTTPGateway, EndpointCommon, true},

			// SSH Gateway access rules
			{SourceSSHGateway, EndpointGitProtocol, false},
			{SourceSSHGateway, EndpointSSHProtocol, true},
			{SourceSSHGateway, EndpointRESTAPI, false},
			{SourceSSHGateway, EndpointCommon, true},

			// Orchestrator access rules
			{SourceOrchestrator, EndpointGitProtocol, false},
			{SourceOrchestrator, EndpointSSHProtocol, false},
			{SourceOrchestrator, EndpointRESTAPI, true},
			{SourceOrchestrator, EndpointCommon, true},
		},
		DenyByDefault:     true,
		LogSecurityEvents: true,
	}
}

// Middleware returns the HTTP middleware function
func (acm *AccessControlMiddleware) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract X-Source header
		sourceHeader := r.Header.Get("X-Source")
		if sourceHeader == "" {
			acm.logSecurityViolation(r, "", "missing", "Missing X-Source header")
			acm.writeErrorResponse(w, http.StatusForbidden, "Access denied: X-Source header required")
			return
		}

		// Validate source type
		source := SourceType(sourceHeader)
		if !acm.isValidSource(source) {
			acm.logSecurityViolation(r, string(source), "invalid", "Invalid X-Source header value")
			acm.writeErrorResponse(w, http.StatusForbidden, "Access denied: Invalid X-Source header")
			return
		}

		// Classify endpoint type
		endpointType := acm.classifyEndpoint(r.URL.Path)

		// Check access permission
		allowed := acm.isAccessAllowed(source, endpointType)
		if !allowed {
			acm.logSecurityViolation(r, string(source), string(endpointType), "Unauthorized endpoint access attempt")
			acm.writeErrorResponse(w, http.StatusForbidden,
				fmt.Sprintf("Access denied: %s cannot access %s endpoints", source, endpointType))
			return
		}

		// Log successful access
		acm.logAccessAttempt(r, string(source), string(endpointType), "allowed")

		// Continue to next handler
		next.ServeHTTP(w, r)
	})
}

// isValidSource checks if the source type is valid
func (acm *AccessControlMiddleware) isValidSource(source SourceType) bool {
	switch source {
	case SourceHTTPGateway, SourceSSHGateway, SourceOrchestrator:
		return true
	default:
		return false
	}
}

// classifyEndpoint determines the endpoint type based on the URL path
func (acm *AccessControlMiddleware) classifyEndpoint(path string) EndpointType {
	// Check in order of specificity
	if acm.gitProtocolPattern.MatchString(path) {
		return EndpointGitProtocol
	}
	if acm.sshProtocolPattern.MatchString(path) {
		return EndpointSSHProtocol
	}
	if acm.restAPIPattern.MatchString(path) {
		return EndpointRESTAPI
	}
	if acm.commonPattern.MatchString(path) {
		return EndpointCommon
	}

	// Default to unknown (will be denied by default)
	return "unknown"
}

// isAccessAllowed checks if the source is allowed to access the endpoint type
func (acm *AccessControlMiddleware) isAccessAllowed(source SourceType, endpointType EndpointType) bool {
	// Check explicit rules first
	for _, rule := range acm.config.Rules {
		if rule.Source == source && rule.Endpoint == endpointType {
			return rule.Allowed
		}
	}

	// Apply default policy
	return !acm.config.DenyByDefault
}

// logSecurityViolation logs security violations
func (acm *AccessControlMiddleware) logSecurityViolation(r *http.Request, source, endpointType, reason string) {
	if !acm.config.LogSecurityEvents {
		return
	}

	ctx := r.Context()
	securityErr := fmt.Errorf("access control violation: %s", reason)

	acm.logger.LogError(ctx, securityErr, "Access control violation",
		zap.String("source", source),
		zap.String("endpoint_type", endpointType),
		zap.String("path", r.URL.Path),
		zap.String("method", r.Method),
		zap.String("remote_addr", r.RemoteAddr),
		zap.String("user_agent", r.UserAgent()),
		zap.String("reason", reason),
		zap.String("request_id", r.Header.Get("X-Request-ID")),
	)
}

// logAccessAttempt logs successful access attempts
func (acm *AccessControlMiddleware) logAccessAttempt(r *http.Request, source, endpointType, result string) {
	ctx := r.Context()

	acm.logger.LogDebug(ctx, "Access control check",
		zap.String("source", source),
		zap.String("endpoint_type", endpointType),
		zap.String("path", r.URL.Path),
		zap.String("method", r.Method),
		zap.String("result", result),
		zap.String("request_id", r.Header.Get("X-Request-ID")),
	)
}

// writeErrorResponse writes a standardized error response
func (acm *AccessControlMiddleware) writeErrorResponse(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	// Write JSON error response
	errorResponse := fmt.Sprintf(`{"error": "%s", "code": %d}`,
		strings.ReplaceAll(message, `"`, `\"`), statusCode)
	_, _ = w.Write([]byte(errorResponse))
}
