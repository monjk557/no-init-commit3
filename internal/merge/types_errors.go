package merge

import (
	"errors"
	"fmt"
	"net/http"
)

// Sentinel errors (카테고리)
var (
	ErrConflict      = errors.New("merge conflict")
	ErrNotFound      = errors.New("not found")
	ErrLocked        = errors.New("resource locked")
	ErrInvalidInput  = errors.New("invalid input")
	ErrInternalError = errors.New("internal error")
)

// NotFoundError is a typed error for not found resources
type NotFoundError struct {
	Kind string // "ref", "commit", "repo", "object"
	Name string
}

func (e NotFoundError) Error() string {
	return fmt.Sprintf("%s not found: %s", e.Kind, e.Name)
}

func (e NotFoundError) Unwrap() error {
	return ErrNotFound
}

// ConflictError is a typed error for merge conflicts
type ConflictError struct {
	ConflictCount  int
	AutoResolvable int
	Message        string
}

func (e ConflictError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("merge has %d conflicts", e.ConflictCount)
}

func (e ConflictError) Unwrap() error {
	return ErrConflict
}

// ValidationError is a typed error for validation failures
type ValidationError struct {
	Field   string
	Message string
}

func (e ValidationError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("validation failed for %s: %s", e.Field, e.Message)
	}
	return e.Message
}

func (e ValidationError) Unwrap() error {
	return ErrInvalidInput
}

// LockedError is a typed error for locked resources
type LockedError struct {
	Resource string
	Message  string
}

func (e LockedError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("resource is locked: %s", e.Resource)
}

func (e LockedError) Unwrap() error {
	return ErrLocked
}

// ConcurrentUpdateError is a typed error for concurrent update conflicts
type ConcurrentUpdateError struct {
	Resource string
	Message  string
}

func (e ConcurrentUpdateError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("concurrent update on resource: %s", e.Resource)
}

func (e ConcurrentUpdateError) Unwrap() error {
	return ErrConflict
}

// InternalError is a typed error for internal errors
type InternalError struct {
	Op      string // operation that failed
	Message string
	Err     error
}

func (e InternalError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Op, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Op, e.Message)
}

func (e InternalError) Unwrap() error {
	if e.Err != nil {
		return e.Err
	}
	return ErrInternalError
}

// APIError is a framework-agnostic error response structure
type APIError struct {
	Code    string         `json:"code"`
	Status  int            `json:"-"` // HTTP status code (not serialized)
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// Error implements the error interface
func (e APIError) Error() string {
	return e.Message
}

// ToAPIError converts any error to an APIError
// Uses errors.As for proper type introspection with wrapped errors
func ToAPIError(err error) APIError {
	if err == nil {
		return APIError{
			Code:    "UNKNOWN",
			Status:  http.StatusInternalServerError,
			Message: "An unknown error occurred",
		}
	}

	// Check for typed errors using errors.As
	var notFound NotFoundError
	if errors.As(err, &notFound) {
		return APIError{
			Code:    "NOT_FOUND",
			Status:  http.StatusNotFound,
			Message: "The requested resource was not found",
			Details: map[string]any{
				"kind": notFound.Kind,
				"name": notFound.Name,
			},
		}
	}

	var conflict ConflictError
	if errors.As(err, &conflict) {
		return APIError{
			Code:    "CONFLICT",
			Status:  http.StatusConflict,
			Message: conflict.Error(),
			Details: map[string]any{
				"conflict_count":  conflict.ConflictCount,
				"auto_resolvable": conflict.AutoResolvable,
			},
		}
	}

	var validation ValidationError
	if errors.As(err, &validation) {
		details := map[string]any{}
		if validation.Field != "" {
			details["field"] = validation.Field
		}
		return APIError{
			Code:    "INVALID_INPUT",
			Status:  http.StatusBadRequest,
			Message: validation.Message,
			Details: details,
		}
	}

	var locked LockedError
	if errors.As(err, &locked) {
		return APIError{
			Code:    "LOCKED",
			Status:  http.StatusLocked, // 423
			Message: locked.Error(),
			Details: map[string]any{
				"resource": locked.Resource,
			},
		}
	}

	var internal InternalError
	if errors.As(err, &internal) {
		return APIError{
			Code:    "INTERNAL_ERROR",
			Status:  http.StatusInternalServerError,
			Message: "An internal error occurred",
			Details: map[string]any{
				"operation": internal.Op,
			},
		}
	}

	// Check for sentinel errors using errors.Is
	if errors.Is(err, ErrNotFound) {
		return APIError{
			Code:    "NOT_FOUND",
			Status:  http.StatusNotFound,
			Message: err.Error(),
		}
	}

	if errors.Is(err, ErrConflict) {
		return APIError{
			Code:    "CONFLICT",
			Status:  http.StatusConflict,
			Message: err.Error(),
		}
	}

	if errors.Is(err, ErrInvalidInput) {
		return APIError{
			Code:    "INVALID_INPUT",
			Status:  http.StatusBadRequest,
			Message: err.Error(),
		}
	}

	if errors.Is(err, ErrLocked) {
		return APIError{
			Code:    "LOCKED",
			Status:  http.StatusLocked,
			Message: err.Error(),
		}
	}

	// Default: internal error
	return APIError{
		Code:    "INTERNAL_ERROR",
		Status:  http.StatusInternalServerError,
		Message: err.Error(),
	}
}

// WrapMergeError wraps an error with operation context
// Always uses %w for proper error chain preservation
func WrapMergeError(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", op, err)
}

// NewNotFoundError creates a new NotFoundError
func NewNotFoundError(kind, name string) error {
	return NotFoundError{Kind: kind, Name: name}
}

// NewConflictError creates a new ConflictError.
// All conflicts require manual human resolution.
func NewConflictError(conflictCount int) error {
	return ConflictError{
		ConflictCount:  conflictCount,
		AutoResolvable: 0,
		Message:        fmt.Sprintf("merge has %d conflicts. All conflicts require manual resolution", conflictCount),
	}
}

// NewValidationError creates a new ValidationError
func NewValidationError(field, message string) error {
	return ValidationError{Field: field, Message: message}
}

// NewLockedError creates a new LockedError
func NewLockedError(resource string) error {
	return LockedError{Resource: resource}
}

// NewInternalError creates a new InternalError
func NewInternalError(op, message string, err error) error {
	return InternalError{Op: op, Message: message, Err: err}
}

// SAFETY FIX: Environment file conflict error
type EnvironmentConflictError struct {
	Path    string
	Message string
}

func (e EnvironmentConflictError) Error() string {
	return e.Message
}

func (e EnvironmentConflictError) Unwrap() error {
	return ErrConflict
}

func (e EnvironmentConflictError) HTTPStatusCode() int {
	return http.StatusConflict
}

// SAFETY FIX: Complex conflict error requiring manual resolution
type ComplexConflictError struct {
	Path    string
	Message string
}

func (e ComplexConflictError) Error() string {
	return e.Message
}

func (e ComplexConflictError) Unwrap() error {
	return ErrConflict
}

func (e ComplexConflictError) HTTPStatusCode() int {
	return http.StatusConflict
}
