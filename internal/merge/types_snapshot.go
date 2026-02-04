package merge

// snapshot.go - Layer 0: Pure Types (go-git과 완전히 분리된 자체 DTO)
// 이 파일의 타입들은 context.Context를 모르고, go-git 타입을 직접 사용하지 않음

// TreeSnapshot represents a snapshot of a Git tree
// This is a pure data structure with no dependencies on go-git
type TreeSnapshot struct {
	Entries map[string]FileSnapshot
}

// FileSnapshot represents a snapshot of a file in a Git tree
type FileSnapshot struct {
	Hash  string // plumbing.Hash.String()
	Mode  uint32 // filemode.FileMode
	IsDir bool
	Size  int64
}

// NewTreeSnapshot creates a new empty TreeSnapshot
func NewTreeSnapshot() *TreeSnapshot {
	return &TreeSnapshot{
		Entries: make(map[string]FileSnapshot),
	}
}

// AddEntry adds a file entry to the snapshot
func (s *TreeSnapshot) AddEntry(path string, entry FileSnapshot) {
	s.Entries[path] = entry
}

// GetEntry returns a file entry by path
func (s *TreeSnapshot) GetEntry(path string) (FileSnapshot, bool) {
	entry, ok := s.Entries[path]
	return entry, ok
}

// HasEntry checks if a path exists in the snapshot
func (s *TreeSnapshot) HasEntry(path string) bool {
	_, ok := s.Entries[path]
	return ok
}

// IsEmpty checks if the snapshot has no entries
func (s *TreeSnapshot) IsEmpty() bool {
	return len(s.Entries) == 0
}

// Paths returns all paths in the snapshot
func (s *TreeSnapshot) Paths() []string {
	paths := make([]string, 0, len(s.Entries))
	for path := range s.Entries {
		paths = append(paths, path)
	}
	return paths
}
