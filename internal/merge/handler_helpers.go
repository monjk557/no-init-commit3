package merge

import (
	"github.com/gin-gonic/gin"
)

// writeError writes an error response using the centralized APIError system
// This is the ONLY place where gin.H conversion happens for errors
//
//nolint:unused // kept for consistent error response helpers
func writeError(c *gin.Context, err error) {
	apiErr := ToAPIError(err)

	response := gin.H{
		"success": false,
		"error":   apiErr.Message,
	}

	// Add code if not empty
	if apiErr.Code != "" {
		response["code"] = apiErr.Code
	}

	// Add details if present
	if len(apiErr.Details) > 0 {
		response["details"] = apiErr.Details
	}

	c.JSON(apiErr.Status, response)
}

// writeSuccess writes a success response
//
//nolint:unused
func writeSuccess(c *gin.Context, status int, data interface{}) {
	c.JSON(status, data)
}

// writeErrorWithStatus writes an error response with a specific status code
//
//nolint:unused
func writeErrorWithStatus(c *gin.Context, status int, err error) {
	apiErr := ToAPIError(err)
	apiErr.Status = status

	response := gin.H{
		"success": false,
		"error":   apiErr.Message,
	}

	if apiErr.Code != "" {
		response["code"] = apiErr.Code
	}

	if len(apiErr.Details) > 0 {
		response["details"] = apiErr.Details
	}

	c.JSON(status, response)
}

// writeValidationError writes a validation error response
//
//nolint:unused
func writeValidationError(c *gin.Context, message string) {
	writeError(c, NewValidationError("", message))
}

// writeNotFoundError writes a not found error response
//
//nolint:unused
func writeNotFoundError(c *gin.Context, kind, name string) {
	writeError(c, NewNotFoundError(kind, name))
}

// writeConflictError writes a conflict error response
//
//nolint:unused
func writeConflictError(c *gin.Context, conflictCount, autoResolvable int) {
	writeError(c, NewConflictError(conflictCount, autoResolvable))
}

// writeInternalError writes an internal error response
//
//nolint:unused
func writeInternalError(c *gin.Context, op string, err error) {
	writeError(c, NewInternalError(op, "operation failed", err))
}

// Note: convertCommitToInfo is defined in schema.go
