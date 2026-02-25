package repositories

import "sync"

// repoLockManager provides per-repository RWLocks keyed by repoPath.
// It is used to serialize pack/idx writes (push) vs pack reads (overview/tree/blob).
type repoLockManager struct {
	mu    sync.Mutex
	locks map[string]*sync.RWMutex
}

var globalRepoLocks = &repoLockManager{locks: make(map[string]*sync.RWMutex)}

func (m *repoLockManager) get(path string) *sync.RWMutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.locks[path]; ok {
		return l
	}
	l := &sync.RWMutex{}
	m.locks[path] = l
	return l
}

// GetRepoRWLock returns a per-repository RWMutex for coordinating readers/writers.
func GetRepoRWLock(repoPath string) *sync.RWMutex {
	return globalRepoLocks.get(repoPath)
}
