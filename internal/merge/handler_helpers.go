package merge

// This file previously contained unused helper functions that were marked with //nolint:unused
// All unused helper functions have been removed:
// - writeError
// - writeSuccess
// - writeErrorWithStatus
// - writeValidationError
// - writeNotFoundError
// - writeConflictError
// - writeInternalError
//
// These functions were never used throughout the codebase and added unnecessary complexity.
// If error response helpers are needed in the future, they should be implemented
// as part of a centralized API response system rather than individual helper functions.

// If you need error handling utilities, consider using:
// - The ToAPIError function from types_errors.go for error conversion
// - Direct gin.Context.JSON calls for responses
// - Centralized error middleware for consistent error handling
