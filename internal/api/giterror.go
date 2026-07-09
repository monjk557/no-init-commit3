package api

import (
	"context"
	"errors"
	"strings"
)

// ClassifyGitError inspects a git command error and returns the most specific
// catalog code for it. A recognizable, client-attributable failure (bad/unknown
// ref, missing object/path, a hit deadline) maps to a 4xx/429 code; anything
// unrecognized stays INTERNAL_ERROR (500) — a genuine server fault is reported
// honestly, never silently downgraded to a 4xx (Issue-5577 follow-up).
//
// Use at handler sites that previously returned a blanket INTERNAL_ERROR from a
// git-operation err.Error(): api.NewCoded(api.ClassifyGitError(err), err.Error()).
func ClassifyGitError(err error) ErrorCodeEnum {
	if err == nil {
		return ErrCodeInternalError
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrCodeTimeout
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "not a git repository"),
		strings.Contains(msg, "does not contain a valid git repository"):
		return ErrCodeRepositoryNotFound
	case strings.Contains(msg, "unknown revision"),
		strings.Contains(msg, "bad revision"),
		strings.Contains(msg, "ambiguous argument"),
		strings.Contains(msg, "not a valid object name"),
		strings.Contains(msg, "invalid object name"),
		strings.Contains(msg, "reference not found"),
		strings.Contains(msg, "unknown ref"):
		return ErrCodeRefNotFound
	case strings.Contains(msg, "bad object"),
		strings.Contains(msg, "object not found"),
		strings.Contains(msg, "no such object"):
		return ErrCodeObjectNotFound
	case strings.Contains(msg, "path not found"),
		strings.Contains(msg, "no such path"),
		strings.Contains(msg, "exists on disk, but not in"),
		strings.Contains(msg, "does not exist in"):
		return ErrCodeFileNotFound
	default:
		return ErrCodeInternalError
	}
}
