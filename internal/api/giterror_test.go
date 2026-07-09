package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

// TestClassifyGitError verifies recognizable git failures map to a specific 4xx
// / 429 code while an unrecognized failure stays INTERNAL_ERROR (500) — a real
// server fault is never silently downgraded (Issue-5577).
func TestClassifyGitError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantCode   ErrorCodeEnum
		wantStatus int
	}{
		{"nil", nil, ErrCodeInternalError, http.StatusInternalServerError},
		{"deadline", fmt.Errorf("git log: %w", context.DeadlineExceeded), ErrCodeTimeout, http.StatusTooManyRequests},
		{"unknown revision", errors.New("fatal: ambiguous argument 'x': unknown revision or path"), ErrCodeRefNotFound, http.StatusNotFound},
		{"bad revision", errors.New("fatal: bad revision 'deadbeef'"), ErrCodeRefNotFound, http.StatusNotFound},
		{"bad object", errors.New("fatal: bad object HEAD~99"), ErrCodeObjectNotFound, http.StatusNotFound},
		{"not a git repo", errors.New("fatal: not a git repository: '/x.git'"), ErrCodeRepositoryNotFound, http.StatusNotFound},
		{"path not found", errors.New("path 'a/b' does not exist in 'HEAD'"), ErrCodeFileNotFound, http.StatusNotFound},
		{"unrecognized stays 500", errors.New("merge-tree: unexpected internal failure"), ErrCodeInternalError, http.StatusInternalServerError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code := ClassifyGitError(c.err)
			if code != c.wantCode {
				t.Fatalf("code = %s, want %s", code, c.wantCode)
			}
			if got := StatusForCode(code); got != c.wantStatus {
				t.Fatalf("status = %d, want %d", got, c.wantStatus)
			}
		})
	}
}
