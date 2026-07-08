package branches

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

// TestBranchListHTTPError verifies that a for-each-ref deadline (repo is fine,
// just slow under load) maps to 429 — a transient signal the BFF does NOT
// auto-retry, so a timeout can't trigger a retry storm on the contended backend
// — while any other failure keeps the 422 corrupted-repo signal, including when
// the deadline is wrapped with %w the way NativeGetBranches returns it.
// See Issue-5071.
func TestBranchListHTTPError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{
			name:       "wrapped deadline → 429",
			err:        fmt.Errorf("git for-each-ref timed out after 5s: %w", context.DeadlineExceeded),
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name:       "bare deadline → 429",
			err:        context.DeadlineExceeded,
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name:       "genuine git failure → 422",
			err:        errors.New("git for-each-ref failed: fatal: bad object"),
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			// Repo dir missing / not a git repo → 404, not the 422 reserved for a
			// present-but-corrupted repo (Issue-5558).
			name:       "missing repo (not a git repository) → 404",
			err:        errors.New("git for-each-ref failed: fatal: not a git repository: '/var/lib/git/repos/x/y.git': exit status 128"),
			wantStatus: http.StatusNotFound,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := branchListHTTPError(c.err)
			if got.Status != c.wantStatus {
				t.Fatalf("status = %d, want %d (msg=%q)", got.Status, c.wantStatus, got.Message)
			}
		})
	}
}
