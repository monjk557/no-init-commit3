package maintenance

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTryLockRepo_ExclusiveThenReleasable verifies the cross-runner invariant:
// while one holder owns the lock a second acquisition is denied (acquired=false,
// no error), and after release it can be re-acquired. flock is tied to the open
// file description, so two independent opens contend even within one process —
// which mirrors two pods contending on the shared volume.
func TestTryLockRepo_ExclusiveThenReleasable(t *testing.T) {
	dir := t.TempDir()

	l1, ok1, err := tryLockRepo(dir)
	if err != nil || !ok1 {
		t.Fatalf("first lock: acquired=%v err=%v", ok1, err)
	}

	_, ok2, err := tryLockRepo(dir)
	if err != nil {
		t.Fatalf("second lock returned error: %v", err)
	}
	if ok2 {
		t.Fatal("second lock should be denied while first is held")
	}

	if err := l1.unlock(); err != nil {
		t.Fatalf("unlock: %v", err)
	}

	l3, ok3, err := tryLockRepo(dir)
	if err != nil || !ok3 {
		t.Fatalf("re-lock after release: acquired=%v err=%v", ok3, err)
	}
	_ = l3.unlock()

	// The lock file lives inside the repo dir and persists (only the advisory
	// lock is released, not the file).
	if _, statErr := os.Stat(filepath.Join(dir, maintenanceLockFile)); statErr != nil {
		t.Errorf("lock file should persist: %v", statErr)
	}
}

func TestUnlock_NilSafe(t *testing.T) {
	var l *repoLock
	if err := l.unlock(); err != nil {
		t.Fatalf("nil unlock should be a no-op, got %v", err)
	}
}
