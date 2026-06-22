package gitcmd

import (
	"context"
	"strings"
	"testing"
)

// setupBatchRepo creates a repo with two commits and returns its path plus both
// commit SHAs (parent, head).
func setupBatchRepo(t *testing.T) (repo, parent, head string) {
	t.Helper()
	repo = t.TempDir()
	gitInRepo(t, repo, "init", "-q")
	gitInRepo(t, repo, "config", "user.name", "tester")
	gitInRepo(t, repo, "config", "user.email", "tester@example.com")
	gitInRepo(t, repo, "commit", "--allow-empty", "-q", "-m", "first")
	parent = gitInRepo(t, repo, "rev-parse", "HEAD")
	gitInRepo(t, repo, "commit", "--allow-empty", "-q", "-m", "second")
	head = gitInRepo(t, repo, "rev-parse", "HEAD")
	return repo, parent, head
}

func TestFilterExistingObjects_MixedKnownAndUnknown(t *testing.T) {
	repo, parent, head := setupBatchRepo(t)
	// A syntactically valid SHA-1 that does not exist in the repo.
	missing := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

	got, err := FilterExistingObjects(context.Background(), repo, []string{head, missing, parent})
	if err != nil {
		t.Fatalf("FilterExistingObjects error: %v", err)
	}
	if !got[head] {
		t.Errorf("expected head %s to be present", head)
	}
	if !got[parent] {
		t.Errorf("expected parent %s to be present", parent)
	}
	if got[missing] {
		t.Errorf("missing oid %s must not be reported as present", missing)
	}
	if len(got) != 2 {
		t.Errorf("expected exactly 2 existing objects, got %d (%v)", len(got), got)
	}
}

func TestFilterExistingObjects_Empty(t *testing.T) {
	repo, _, _ := setupBatchRepo(t)
	got, err := FilterExistingObjects(context.Background(), repo, nil)
	if err != nil {
		t.Fatalf("FilterExistingObjects(nil) error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty result for no oids, got %v", got)
	}
}

// TestFilterExistingObjects_MatchesObjectExists guards the batch path against
// the per-oid ObjectExists it replaces: both must agree on membership.
func TestFilterExistingObjects_MatchesObjectExists(t *testing.T) {
	repo, parent, head := setupBatchRepo(t)
	missing := "0000000000000000000000000000000000000000"
	oids := []string{head, parent, missing}

	batch, err := FilterExistingObjects(context.Background(), repo, oids)
	if err != nil {
		t.Fatalf("FilterExistingObjects error: %v", err)
	}
	for _, oid := range oids {
		_, oeErr := ObjectExists(context.Background(), repo, oid)
		wantPresent := oeErr == nil
		if batch[oid] != wantPresent {
			t.Errorf("oid %s: batch=%v ObjectExists-present=%v (mismatch)",
				oid, batch[oid], wantPresent)
		}
	}
}

// TestFilterExistingObjects_BadRepo ensures a hard git failure propagates as an
// error rather than being silently swallowed.
func TestFilterExistingObjects_BadRepo(t *testing.T) {
	notARepo := t.TempDir()
	_, err := FilterExistingObjects(context.Background(), notARepo,
		[]string{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"})
	if err == nil {
		t.Fatal("expected error for non-repository path, got nil")
	}
	if !strings.Contains(err.Error(), "cat-file") {
		t.Errorf("error should mention cat-file, got: %v", err)
	}
}
