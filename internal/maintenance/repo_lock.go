package maintenance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// repo_lock.go coordinates maintenance across replicas. All git-executor pods
// share the same repository volume (RWO, one node) and each runs its own
// maintenance scheduler, so without coordination two pods repack the same repo
// at once — colliding on git's *.lock files ("multi-pack-index.lock: File
// exists") and pulling packs out from under each other's MIDX/bitmap writes
// (the closure "object missing" race). See Issue-5071.
//
// A non-blocking advisory flock scoped per repo lets exactly one runner work a
// given repo at a time. flock is tied to the open file description and released
// when the descriptor closes — including on process exit — so a crashed pod
// never leaves a stale lock (no TTL/cleanup needed).

const maintenanceLockFile = "collabops-maintenance.lock"

// repoLock holds an acquired per-repo maintenance lock.
type repoLock struct{ f *os.File }

// tryLockRepo attempts a non-blocking exclusive lock for repoPath.
//
//   - acquired=true, err=nil  → caller owns the lock; must call unlock().
//   - acquired=false, err=nil → another runner holds it; caller should skip.
//   - err!=nil                → lock infrastructure error; caller decides
//     (maintenance proceeds best-effort so a lock/FS problem never stalls it).
func tryLockRepo(repoPath string) (lock *repoLock, acquired bool, err error) {
	path := filepath.Join(repoPath, maintenanceLockFile)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, false, fmt.Errorf("open maintenance lock: %w", err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		// EWOULDBLOCK (== EAGAIN): another open file description holds the lock.
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("flock: %w", err)
	}
	return &repoLock{f: f}, true, nil
}

// unlock releases the lock and closes the descriptor. Closing alone would also
// release the flock; the explicit LOCK_UN makes intent clear.
func (l *repoLock) unlock() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	return l.f.Close()
}
