package connection

import (
	"fmt"
	"net"
	"strings"
	"time"
)

// Error interface extends the standard error interface with additional context
type Error interface {
	error
	Code() string
	Category() ErrorCategory
	IsTemporary() bool
	IsRetryable() bool
	Unwrap() error
}

// ErrorCategory represents different categories of errors
type ErrorCategory string

const (
	ErrorCategoryTimeout       ErrorCategory = "timeout"
	ErrorCategoryConfiguration ErrorCategory = "configuration"
	ErrorCategoryNetwork       ErrorCategory = "network"
	ErrorCategoryConcurrency   ErrorCategory = "concurrency"
	ErrorCategoryValidation    ErrorCategory = "validation"
	ErrorCategoryProtocol      ErrorCategory = "protocol"
	ErrorCategoryIO            ErrorCategory = "io"
	ErrorCategoryLifecycle     ErrorCategory = "lifecycle"
)

// BaseError provides common error functionality
type BaseError struct {
	code      string
	category  ErrorCategory
	message   string
	cause     error
	temporary bool
	retryable bool
	context   map[string]interface{}
	timestamp time.Time
}

// Error implements the error interface
func (e *BaseError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %v", e.message, e.cause)
	}
	return e.message
}

// Code returns the error code
func (e *BaseError) Code() string {
	return e.code
}

// Category returns the error category
func (e *BaseError) Category() ErrorCategory {
	return e.category
}

// IsTemporary returns whether the error is temporary
func (e *BaseError) IsTemporary() bool {
	return e.temporary
}

// IsRetryable returns whether the operation can be retried
func (e *BaseError) IsRetryable() bool {
	return e.retryable
}

// Unwrap returns the wrapped error
func (e *BaseError) Unwrap() error {
	return e.cause
}

// GetContext returns the error context
func (e *BaseError) GetContext() map[string]interface{} {
	if e.context == nil {
		return make(map[string]interface{})
	}
	return e.context
}

// SetContext adds context to the error
func (e *BaseError) SetContext(key string, value interface{}) {
	if e.context == nil {
		e.context = make(map[string]interface{})
	}
	e.context[key] = value
}

// GetTimestamp returns when the error occurred
func (e *BaseError) GetTimestamp() time.Time {
	return e.timestamp
}

// ==============================
// Timeout Errors
// ==============================

// TimeoutError represents timeout-related errors
type TimeoutError struct {
	*BaseError
	Phase    ConnectionPhase
	Duration time.Duration
	Type     string
}

// NewTimeoutError creates a new timeout error
func NewTimeoutError(phase ConnectionPhase, duration time.Duration, timeoutType string) *TimeoutError {
	message := fmt.Sprintf("%s timeout after %v in phase %v", timeoutType, duration, phase)

	return &TimeoutError{
		BaseError: &BaseError{
			code:      "TIMEOUT_" + timeoutType,
			category:  ErrorCategoryTimeout,
			message:   message,
			temporary: true,
			retryable: false, // Timeout errors are typically not retryable
			timestamp: time.Now(),
		},
		Phase:    phase,
		Duration: duration,
		Type:     timeoutType,
	}
}

// ==============================
// Configuration Errors
// ==============================

// ConfigurationError represents configuration-related errors
type ConfigurationError struct {
	*BaseError
	Field string
	Value interface{}
}

// NewConfigurationError creates a new configuration error
func NewConfigurationError(field string, value interface{}, message string) *ConfigurationError {
	return &ConfigurationError{
		BaseError: &BaseError{
			code:      "CONFIG_INVALID_" + field,
			category:  ErrorCategoryConfiguration,
			message:   message,
			temporary: false,
			retryable: false,
			timestamp: time.Now(),
			context: map[string]interface{}{
				"field": field,
				"value": value,
			},
		},
		Field: field,
		Value: value,
	}
}

// ==============================
// Network/Connection Errors
// ==============================

// ConnectionError represents connection-related errors
type ConnectionError struct {
	*BaseError
	RemoteAddr string
	Operation  string
}

// NewConnectionError creates a new connection error
func NewConnectionError(operation, remoteAddr string, cause error) *ConnectionError {
	message := fmt.Sprintf("connection %s failed", operation)
	if remoteAddr != "" {
		message += fmt.Sprintf(" for %s", remoteAddr)
	}

	return &ConnectionError{
		BaseError: &BaseError{
			code:      "CONNECTION_" + operation,
			category:  ErrorCategoryNetwork,
			message:   message,
			cause:     cause,
			temporary: true,
			retryable: true,
			timestamp: time.Now(),
			context: map[string]interface{}{
				"remote_addr": remoteAddr,
				"operation":   operation,
			},
		},
		RemoteAddr: remoteAddr,
		Operation:  operation,
	}
}

// ConnectionClosedError represents errors when operating on closed connections
type ConnectionClosedError struct {
	*BaseError
	Operation string
}

// NewConnectionClosedError creates a new connection closed error
func NewConnectionClosedError(operation string) *ConnectionClosedError {
	return &ConnectionClosedError{
		BaseError: &BaseError{
			code:      "CONNECTION_CLOSED",
			category:  ErrorCategoryLifecycle,
			message:   fmt.Sprintf("cannot %s: connection is closed", operation),
			temporary: false,
			retryable: false,
			timestamp: time.Now(),
			context: map[string]interface{}{
				"operation": operation,
			},
		},
		Operation: operation,
	}
}

// ClientDisconnectError represents errors caused by the client disconnecting
// (e.g., Ctrl+C, network drop). These are normal operational events, not server errors.
type ClientDisconnectError struct {
	*BaseError
	Operation  string
	RemoteAddr string
	Phase      string
}

// NewClientDisconnectError creates a new client disconnect error
func NewClientDisconnectError(operation, remoteAddr, phase string, cause error) *ClientDisconnectError {
	return &ClientDisconnectError{
		BaseError: &BaseError{
			code:      "CLIENT_DISCONNECT",
			category:  ErrorCategoryNetwork,
			message:   fmt.Sprintf("client disconnected during %s (%s phase)", operation, phase),
			cause:     cause,
			temporary: true,
			retryable: false,
			timestamp: time.Now(),
			context: map[string]interface{}{
				"operation":   operation,
				"remote_addr": remoteAddr,
				"phase":       phase,
			},
		},
		Operation:  operation,
		RemoteAddr: remoteAddr,
		Phase:      phase,
	}
}

// ConnectionStalledError represents stalled connection errors
type ConnectionStalledError struct {
	*BaseError
	StallDuration    time.Duration
	BytesTransferred int64
}

// NewConnectionStalledError creates a new connection stalled error
func NewConnectionStalledError(stallDuration time.Duration, bytesTransferred int64) *ConnectionStalledError {
	return &ConnectionStalledError{
		BaseError: &BaseError{
			code:      "CONNECTION_STALLED",
			category:  ErrorCategoryNetwork,
			message:   fmt.Sprintf("connection stalled for %v after transferring %d bytes", stallDuration, bytesTransferred),
			temporary: true,
			retryable: true,
			timestamp: time.Now(),
			context: map[string]interface{}{
				"stall_duration":    stallDuration,
				"bytes_transferred": bytesTransferred,
			},
		},
		StallDuration:    stallDuration,
		BytesTransferred: bytesTransferred,
	}
}

// ==============================
// Concurrency Errors
// ==============================

// ConcurrencyError represents concurrency-related errors
type ConcurrencyError struct {
	*BaseError
	Operation   OperationType
	Reason      string
	CurrentLoad int
	MaxLoad     int
}

// NewConcurrencyError creates a new concurrency error
func NewConcurrencyError(operation OperationType, reason string, currentLoad, maxLoad int) *ConcurrencyError {
	return &ConcurrencyError{
		BaseError: &BaseError{
			code:      "CONCURRENCY_" + reason,
			category:  ErrorCategoryConcurrency,
			message:   fmt.Sprintf("%s operation denied: %s (load: %d/%d)", operation, reason, currentLoad, maxLoad),
			temporary: true,
			retryable: true,
			timestamp: time.Now(),
			context: map[string]interface{}{
				"operation":    operation.String(),
				"reason":       reason,
				"current_load": currentLoad,
				"max_load":     maxLoad,
			},
		},
		Operation:   operation,
		Reason:      reason,
		CurrentLoad: currentLoad,
		MaxLoad:     maxLoad,
	}
}

// QueueFullError represents errors when operation queue is full
type QueueFullError struct {
	*BaseError
	Operation OperationType
	QueueSize int
}

// NewQueueFullError creates a new queue full error
func NewQueueFullError(operation OperationType, queueSize int) *QueueFullError {
	return &QueueFullError{
		BaseError: &BaseError{
			code:      "QUEUE_FULL",
			category:  ErrorCategoryConcurrency,
			message:   fmt.Sprintf("%s operation queue is full (size: %d)", operation, queueSize),
			temporary: true,
			retryable: true,
			timestamp: time.Now(),
			context: map[string]interface{}{
				"operation":  operation.String(),
				"queue_size": queueSize,
			},
		},
		Operation: operation,
		QueueSize: queueSize,
	}
}

// ==============================
// Validation Errors
// ==============================

// ValidationError represents validation errors
type ValidationError struct {
	*BaseError
	Field string
	Value interface{}
	Rule  string
}

// NewValidationError creates a new validation error
func NewValidationError(field string, value interface{}, rule, message string) *ValidationError {
	return &ValidationError{
		BaseError: &BaseError{
			code:      "VALIDATION_" + rule,
			category:  ErrorCategoryValidation,
			message:   message,
			temporary: false,
			retryable: false,
			timestamp: time.Now(),
			context: map[string]interface{}{
				"field": field,
				"value": value,
				"rule":  rule,
			},
		},
		Field: field,
		Value: value,
		Rule:  rule,
	}
}

// ==============================
// I/O Errors
// ==============================

// IOError represents I/O related errors
type IOError struct {
	*BaseError
	Operation  string
	BytesCount int64
}

// NewIOError creates a new I/O error
func NewIOError(operation string, bytesCount int64, cause error) *IOError {
	return &IOError{
		BaseError: &BaseError{
			code:      "IO_" + operation,
			category:  ErrorCategoryIO,
			message:   fmt.Sprintf("I/O %s failed after %d bytes", operation, bytesCount),
			cause:     cause,
			temporary: true,
			retryable: true,
			timestamp: time.Now(),
			context: map[string]interface{}{
				"operation":   operation,
				"bytes_count": bytesCount,
			},
		},
		Operation:  operation,
		BytesCount: bytesCount,
	}
}

// ==============================
// Protocol Errors
// ==============================

// ProtocolError represents Git protocol-related errors
type ProtocolError struct {
	*BaseError
	Phase   string
	Command string
}

// NewProtocolError creates a new protocol error
func NewProtocolError(phase, command, message string, cause error) *ProtocolError {
	return &ProtocolError{
		BaseError: &BaseError{
			code:      "PROTOCOL_" + phase,
			category:  ErrorCategoryProtocol,
			message:   message,
			cause:     cause,
			temporary: false,
			retryable: false,
			timestamp: time.Now(),
			context: map[string]interface{}{
				"phase":   phase,
				"command": command,
			},
		},
		Phase:   phase,
		Command: command,
	}
}

// ==============================
// Helper Functions
// ==============================

// IsTimeoutError checks if an error is a timeout error
func IsTimeoutError(err error) bool {
	_, ok := err.(*TimeoutError)
	return ok
}

// IsConfigurationError checks if an error is a configuration error
func IsConfigurationError(err error) bool {
	_, ok := err.(*ConfigurationError)
	return ok
}

// IsConnectionError checks if an error is a connection error
func IsConnectionError(err error) bool {
	switch err.(type) {
	case *ConnectionError, *ConnectionClosedError, *ConnectionStalledError, *ClientDisconnectError:
		return true
	default:
		return false
	}
}

// IsConcurrencyError checks if an error is a concurrency error
func IsConcurrencyError(err error) bool {
	switch err.(type) {
	case *ConcurrencyError, *QueueFullError:
		return true
	default:
		return false
	}
}

// IsRetryableError checks if an error is retryable
func IsRetryableError(err error) bool {
	if connErr, ok := err.(Error); ok {
		return connErr.IsRetryable()
	}
	return false
}

// IsTemporaryError checks if an error is temporary
func IsTemporaryError(err error) bool {
	if connErr, ok := err.(Error); ok {
		return connErr.IsTemporary()
	}
	return false
}

// GetErrorCategory returns the category of an error
func GetErrorCategory(err error) ErrorCategory {
	if connErr, ok := err.(Error); ok {
		return connErr.Category()
	}
	return ErrorCategory("unknown")
}

// WrapError wraps a standard error with additional context
func WrapError(err error, category ErrorCategory, message string) Error {
	return &BaseError{
		code:      "WRAPPED",
		category:  category,
		message:   message,
		cause:     err,
		temporary: false,
		retryable: false,
		timestamp: time.Now(),
	}
}

// clientDisconnectPatterns contains error message substrings that indicate a client disconnect.
var clientDisconnectPatterns = []string{
	"use of closed network connection",
	"broken pipe",
	"connection reset by peer",
}

// ClassifyNetworkError classifies a network error into a typed error for appropriate handling.
// It inspects the error message to distinguish client disconnects from timeouts and protocol errors.
func ClassifyNetworkError(err error, operation, remoteAddr, phase string) Error {
	if err == nil {
		return nil
	}

	errMsg := err.Error()

	// Check for client disconnect patterns
	for _, pattern := range clientDisconnectPatterns {
		if strings.Contains(errMsg, pattern) {
			return NewClientDisconnectError(operation, remoteAddr, phase, err)
		}
	}

	// Check for timeout (net.Error interface)
	var netErr net.Error
	if ok := errorAs(err, &netErr); ok && netErr.Timeout() {
		return NewTimeoutError(PhaseOperation, 0, "NETWORK")
	}

	// Default to protocol error
	return NewProtocolError(phase, operation, fmt.Sprintf("network error during %s: %v", operation, err), err)
}

// errorAs is a thin wrapper for type-asserting into a *net.Error target.
func errorAs(err error, target *net.Error) bool {
	if ne, ok := err.(net.Error); ok {
		*target = ne
		return true
	}
	return false
}

// IsClientDisconnect checks if an error is a client disconnect error
func IsClientDisconnect(err error) bool {
	_, ok := err.(*ClientDisconnectError)
	return ok
}
