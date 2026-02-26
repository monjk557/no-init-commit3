// Package protocol implements Git Smart HTTP protocol error handling
// that maintains protocol compliance while providing useful debugging information.
package protocol

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

// GitProtocolError represents an error that can be communicated in Git Smart HTTP protocol format
type GitProtocolError struct {
	Type        string // Error type for categorization
	Message     string // Human-readable error message
	HTTPStatus  int    // HTTP status code for the response
	ProtocolErr bool   // Whether this should be sent as protocol error vs HTTP error
}

// Error implements the error interface
func (e *GitProtocolError) Error() string {
	return e.Message
}

// Predefined protocol errors
var (
	ErrInvalidService = &GitProtocolError{
		Type:        "invalid_service",
		Message:     "Invalid or unsupported service parameter",
		HTTPStatus:  http.StatusBadRequest,
		ProtocolErr: false,
	}

	ErrMissingUserContext = &GitProtocolError{
		Type:        "missing_user_context",
		Message:     "Missing user authentication context from gateway",
		HTTPStatus:  http.StatusUnauthorized,
		ProtocolErr: false,
	}

	ErrPermissionDenied = &GitProtocolError{
		Type:        "permission_denied",
		Message:     "Access denied to repository",
		HTTPStatus:  http.StatusForbidden,
		ProtocolErr: false,
	}

	ErrRepositoryNotFound = &GitProtocolError{
		Type:        "repository_not_found",
		Message:     "Repository not found or inaccessible",
		HTTPStatus:  http.StatusNotFound,
		ProtocolErr: false,
	}

	ErrInvalidRequest = &GitProtocolError{
		Type:        "invalid_request",
		Message:     "Invalid request format or parameters",
		HTTPStatus:  http.StatusBadRequest,
		ProtocolErr: false,
	}

	ErrInvalidContentType = &GitProtocolError{
		Type:        "invalid_content_type",
		Message:     "Invalid content type for Git Smart HTTP request",
		HTTPStatus:  http.StatusBadRequest,
		ProtocolErr: false,
	}

	ErrProtocolNegotiation = &GitProtocolError{
		Type:        "protocol_negotiation",
		Message:     "Protocol negotiation failed",
		HTTPStatus:  http.StatusOK, // Git protocol errors use 200 with error in body
		ProtocolErr: true,
	}

	ErrInternalServer = &GitProtocolError{
		Type:        "internal_server_error",
		Message:     "Internal server error",
		HTTPStatus:  http.StatusInternalServerError,
		ProtocolErr: false,
	}
)

// NewGitProtocolError creates a new Git protocol error
func NewGitProtocolError(errorType, message string, httpStatus int, protocolErr bool) *GitProtocolError {
	return &GitProtocolError{
		Type:        errorType,
		Message:     message,
		HTTPStatus:  httpStatus,
		ProtocolErr: protocolErr,
	}
}

// ErrorHandler provides protocol-compliant error handling for Git Smart HTTP
type ErrorHandler struct {
	logger Logger // Will be implemented
}

// Logger interface for error logging
type Logger interface {
	LogError(operation, errorType, message string, details map[string]interface{})
	LogProtocol(event string, data map[string]interface{})
}

// NewErrorHandler creates a new error handler
func NewErrorHandler(logger Logger) *ErrorHandler {
	return &ErrorHandler{
		logger: logger,
	}
}

// HandleError handles an error according to Git Smart HTTP protocol requirements
func (eh *ErrorHandler) HandleError(w http.ResponseWriter, r *http.Request, err error, operation string) {
	var gitErr *GitProtocolError

	// Try to cast to GitProtocolError
	if e, ok := err.(*GitProtocolError); ok {
		gitErr = e
	} else {
		// Wrap unknown errors as internal server errors
		gitErr = &GitProtocolError{
			Type:        "unknown_error",
			Message:     err.Error(),
			HTTPStatus:  http.StatusInternalServerError,
			ProtocolErr: false,
		}
	}

	// Log the error with context
	if eh.logger != nil {
		details := map[string]interface{}{
			"error_type":     gitErr.Type,
			"http_status":    gitErr.HTTPStatus,
			"protocol_error": gitErr.ProtocolErr,
			"path":           r.URL.Path,
			"method":         r.Method,
			"user_id":        r.Header.Get("X-USER-ID"),
			"request_id":     r.Header.Get("X-REQUEST-ID"),
		}
		eh.logger.LogError(operation, gitErr.Type, gitErr.Message, details)
	}

	// Send appropriate response
	if gitErr.ProtocolErr {
		eh.writeProtocolError(w, gitErr)
	} else {
		eh.writeHTTPError(w, gitErr)
	}
}

// writeHTTPError writes a standard HTTP error response
func (eh *ErrorHandler) writeHTTPError(w http.ResponseWriter, err *GitProtocolError) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(err.HTTPStatus)
	_, _ = fmt.Fprintf(w, "%s\n", err.Message)
}

// writeProtocolError writes a Git protocol-compliant error response
func (eh *ErrorHandler) writeProtocolError(w http.ResponseWriter, err *GitProtocolError) {
	// Set appropriate content type for Git protocol
	w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(err.HTTPStatus)

	// Write error in pkt-line format
	pktWriter := newPktLineWriter(w)
	errorLine := fmt.Sprintf("ERR %s\n", err.Message)
	_ = pktWriter.writeLine(errorLine)
	_ = pktWriter.flush() // Send flush packet to end
}

// newPktLineWriter creates a new packet line writer
func newPktLineWriter(w io.Writer) *pktLineWriter {
	return &pktLineWriter{w: w}
}

// ValidateContentType validates that the request has an appropriate content type for Git Smart HTTP
func ValidateContentType(r *http.Request, expectedTypes []string) error {
	if r.Method != http.MethodPost {
		return nil // GET requests don't require specific content types
	}

	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		return NewGitProtocolError("missing_content_type", "Missing Content-Type header", http.StatusBadRequest, false)
	}

	// Remove charset and other parameters
	contentType = strings.Split(contentType, ";")[0]
	contentType = strings.TrimSpace(contentType)

	for _, expected := range expectedTypes {
		if contentType == expected {
			return nil
		}
	}

	return NewGitProtocolError("invalid_content_type",
		fmt.Sprintf("Invalid content type: %s, expected one of: %s", contentType, strings.Join(expectedTypes, ", ")),
		http.StatusBadRequest, false)
}

// ValidateGatewayRequest validates that a request has been properly forwarded from the gateway
func ValidateGatewayRequest(r *http.Request) error {
	// Validate required headers from gateway
	userID := r.Header.Get("X-USER-ID")
	if userID == "" {
		return ErrMissingUserContext
	}

	// Additional validation can be added here, such as:
	// - Gateway signature validation
	// - Request ID validation
	// - Rate limiting headers

	return nil
}

// ValidateServiceParameter validates the service parameter for info/refs requests
func ValidateServiceParameter(service string, allowedServices []string) error {
	if service == "" {
		return NewGitProtocolError("missing_service", "Missing service parameter", http.StatusBadRequest, false)
	}

	for _, allowed := range allowedServices {
		if service == allowed {
			return nil
		}
	}

	return NewGitProtocolError("invalid_service",
		fmt.Sprintf("Invalid service: %s, expected one of: %s", service, strings.Join(allowedServices, ", ")),
		http.StatusBadRequest, false)
}
