package merge

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func Test_NotFoundError_Error_ReturnsFormattedMessage(t *testing.T) {
	t.Parallel()

	err := NotFoundError{Kind: "ref", Name: "feature/test"}

	got := err.Error()
	want := "ref not found: feature/test"

	if got != want {
		t.Fatalf("Error()=%q want=%q", got, want)
	}
}

func Test_NotFoundError_Unwrap_ReturnsErrNotFound(t *testing.T) {
	t.Parallel()

	err := NotFoundError{Kind: "commit", Name: "abc123"}

	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("errors.Is(err, ErrNotFound)=false want=true")
	}
}

func Test_ConflictError_Error_ReturnsCustomMessage(t *testing.T) {
	t.Parallel()

	err := ConflictError{
		ConflictCount:  5,
		AutoResolvable: 2,
		Message:        "custom conflict message",
	}

	got := err.Error()
	want := "custom conflict message"

	if got != want {
		t.Fatalf("Error()=%q want=%q", got, want)
	}
}

func Test_ConflictError_Error_ReturnsDefaultMessage(t *testing.T) {
	t.Parallel()

	err := ConflictError{ConflictCount: 3}

	got := err.Error()
	want := "merge has 3 conflicts"

	if got != want {
		t.Fatalf("Error()=%q want=%q", got, want)
	}
}

func Test_ConflictError_Unwrap_ReturnsErrConflict(t *testing.T) {
	t.Parallel()

	err := ConflictError{ConflictCount: 1}

	if !errors.Is(err, ErrConflict) {
		t.Fatalf("errors.Is(err, ErrConflict)=false want=true")
	}
}

func Test_ValidationError_Error_WithField(t *testing.T) {
	t.Parallel()

	err := ValidationError{Field: "source_ref", Message: "cannot be empty"}

	got := err.Error()
	want := "validation failed for source_ref: cannot be empty"

	if got != want {
		t.Fatalf("Error()=%q want=%q", got, want)
	}
}

func Test_ValidationError_Error_WithoutField(t *testing.T) {
	t.Parallel()

	err := ValidationError{Message: "invalid request"}

	got := err.Error()
	want := "invalid request"

	if got != want {
		t.Fatalf("Error()=%q want=%q", got, want)
	}
}

func Test_ValidationError_Unwrap_ReturnsErrInvalidInput(t *testing.T) {
	t.Parallel()

	err := ValidationError{Message: "test"}

	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("errors.Is(err, ErrInvalidInput)=false want=true")
	}
}

func Test_LockedError_Error_ReturnsCustomMessage(t *testing.T) {
	t.Parallel()

	err := LockedError{Resource: "repo", Message: "locked by another process"}

	got := err.Error()
	want := "locked by another process"

	if got != want {
		t.Fatalf("Error()=%q want=%q", got, want)
	}
}

func Test_LockedError_Error_ReturnsDefaultMessage(t *testing.T) {
	t.Parallel()

	err := LockedError{Resource: "branch/main"}

	got := err.Error()
	want := "resource is locked: branch/main"

	if got != want {
		t.Fatalf("Error()=%q want=%q", got, want)
	}
}

func Test_InternalError_Error_WithWrappedError(t *testing.T) {
	t.Parallel()

	inner := errors.New("disk full")
	err := InternalError{Op: "save", Message: "failed to write", Err: inner}

	got := err.Error()
	want := "save: failed to write: disk full"

	if got != want {
		t.Fatalf("Error()=%q want=%q", got, want)
	}
}

func Test_InternalError_Unwrap_ReturnsWrappedError(t *testing.T) {
	t.Parallel()

	inner := errors.New("original error")
	err := InternalError{Op: "test", Message: "msg", Err: inner}

	if !errors.Is(err, inner) {
		t.Fatalf("errors.Is(err, inner)=false want=true")
	}
}

func Test_ToAPIError_NotFoundError_Returns404(t *testing.T) {
	t.Parallel()

	err := NotFoundError{Kind: "ref", Name: "feature/test"}

	apiErr := ToAPIError(err)

	if apiErr.Status != http.StatusNotFound {
		t.Fatalf("Status=%d want=%d", apiErr.Status, http.StatusNotFound)
	}
	if apiErr.Code != "NOT_FOUND" {
		t.Fatalf("Code=%q want=%q", apiErr.Code, "NOT_FOUND")
	}
	if apiErr.Details["kind"] != "ref" {
		t.Fatalf("Details[kind]=%v want=%v", apiErr.Details["kind"], "ref")
	}
}

func Test_ToAPIError_ConflictError_Returns409(t *testing.T) {
	t.Parallel()

	err := ConflictError{ConflictCount: 5, AutoResolvable: 2}

	apiErr := ToAPIError(err)

	if apiErr.Status != http.StatusConflict {
		t.Fatalf("Status=%d want=%d", apiErr.Status, http.StatusConflict)
	}
	if apiErr.Code != "CONFLICT" {
		t.Fatalf("Code=%q want=%q", apiErr.Code, "CONFLICT")
	}
}

func Test_ToAPIError_ValidationError_Returns400(t *testing.T) {
	t.Parallel()

	err := ValidationError{Field: "source_ref", Message: "required"}

	apiErr := ToAPIError(err)

	if apiErr.Status != http.StatusBadRequest {
		t.Fatalf("Status=%d want=%d", apiErr.Status, http.StatusBadRequest)
	}
	if apiErr.Code != "INVALID_INPUT" {
		t.Fatalf("Code=%q want=%q", apiErr.Code, "INVALID_INPUT")
	}
}

func Test_ToAPIError_LockedError_Returns423(t *testing.T) {
	t.Parallel()

	err := LockedError{Resource: "repo"}

	apiErr := ToAPIError(err)

	if apiErr.Status != http.StatusLocked {
		t.Fatalf("Status=%d want=%d", apiErr.Status, http.StatusLocked)
	}
	if apiErr.Code != "LOCKED" {
		t.Fatalf("Code=%q want=%q", apiErr.Code, "LOCKED")
	}
}

func Test_ToAPIError_WrappedError_ExtractsTypedError(t *testing.T) {
	t.Parallel()

	inner := NotFoundError{Kind: "commit", Name: "abc123"}
	wrapped := fmt.Errorf("rebase failed: %w", inner)

	apiErr := ToAPIError(wrapped)

	if apiErr.Status != http.StatusNotFound {
		t.Fatalf("Status=%d want=%d (wrapped error not extracted)", apiErr.Status, http.StatusNotFound)
	}
	if apiErr.Details["kind"] != "commit" {
		t.Fatalf("Details[kind]=%v want=%v", apiErr.Details["kind"], "commit")
	}
}

func Test_ToAPIError_NilError_ReturnsInternalError(t *testing.T) {
	t.Parallel()

	apiErr := ToAPIError(nil)

	if apiErr.Status != http.StatusInternalServerError {
		t.Fatalf("Status=%d want=%d", apiErr.Status, http.StatusInternalServerError)
	}
}

func Test_ToAPIError_UnknownError_ReturnsInternalError(t *testing.T) {
	t.Parallel()

	err := errors.New("unknown error")

	apiErr := ToAPIError(err)

	if apiErr.Status != http.StatusInternalServerError {
		t.Fatalf("Status=%d want=%d", apiErr.Status, http.StatusInternalServerError)
	}
	if apiErr.Code != "INTERNAL_ERROR" {
		t.Fatalf("Code=%q want=%q", apiErr.Code, "INTERNAL_ERROR")
	}
}

func Test_WrapMergeError_PreservesErrorChain(t *testing.T) {
	t.Parallel()

	inner := NotFoundError{Kind: "ref", Name: "test"}
	wrapped := WrapMergeError("performMerge", inner)

	if !errors.Is(wrapped, ErrNotFound) {
		t.Fatalf("errors.Is(wrapped, ErrNotFound)=false want=true")
	}

	var notFound NotFoundError
	if !errors.As(wrapped, &notFound) {
		t.Fatalf("errors.As(wrapped, &notFound)=false want=true")
	}
	if notFound.Kind != "ref" {
		t.Fatalf("notFound.Kind=%q want=%q", notFound.Kind, "ref")
	}
}

func Test_WrapMergeError_NilError_ReturnsNil(t *testing.T) {
	t.Parallel()

	wrapped := WrapMergeError("test", nil)

	if wrapped != nil {
		t.Fatalf("WrapMergeError(nil)=%v want=nil", wrapped)
	}
}

func Test_NewNotFoundError_CreatesCorrectError(t *testing.T) {
	t.Parallel()

	err := NewNotFoundError("branch", "feature/test")

	var notFound NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("errors.As failed")
	}
	if notFound.Kind != "branch" {
		t.Fatalf("Kind=%q want=%q", notFound.Kind, "branch")
	}
	if notFound.Name != "feature/test" {
		t.Fatalf("Name=%q want=%q", notFound.Name, "feature/test")
	}
}

func Test_NewConflictError_CreatesCorrectError(t *testing.T) {
	t.Parallel()

	err := NewConflictError(5, 2)

	var conflict ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("errors.As failed")
	}
	if conflict.ConflictCount != 5 {
		t.Fatalf("ConflictCount=%d want=%d", conflict.ConflictCount, 5)
	}
	if conflict.AutoResolvable != 2 {
		t.Fatalf("AutoResolvable=%d want=%d", conflict.AutoResolvable, 2)
	}
}
