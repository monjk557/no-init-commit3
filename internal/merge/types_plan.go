package merge

// plan_types.go - Layer 0: Pure Types (결정만, 결과 없음)
// 이 파일의 타입들은 context.Context를 모르고, go-git 타입을 직접 사용하지 않음
// ResolutionPlan은 "무엇을 해야 하는지"만 담고, "결과"는 담지 않음

// EntryAction represents the action to take for a file entry
type EntryAction int

const (
	EntryActionKeep   EntryAction = iota // Keep existing entry
	EntryActionAdd                       // Add new entry
	EntryActionDelete                    // Delete entry
	EntryActionModify                    // Modify entry (use source/target version)
)

// EntrySource represents which version to use for the entry
type EntrySource int

const (
	EntrySourceBase   EntrySource = iota // Use base version
	EntrySourceSource                    // Use source version (theirs)
	EntrySourceTarget                    // Use target version (ours)
	EntrySourceMerged                    // Use merged content
)

// PlannedEntry represents a planned action for a single file entry
// This is a pure data structure - no ResultHash!
type PlannedEntry struct {
	Path       string      // Full path of the file
	Action     EntryAction // What action to take
	Source     EntrySource // Which version to use
	SourceHash string      // Hash from source tree (if applicable)
	TargetHash string      // Hash from target tree (if applicable)
	BaseHash   string      // Hash from base tree (if applicable)
	Mode       uint32      // File mode to use
	IsDir      bool        // Is this a directory?
	Reason     string      // Why this decision was made (for debugging)
}

// ResolutionPlan represents the plan for merging two trees
// This is a pure data structure - contains decisions only, no results!
type ResolutionPlan struct {
	Entries      []PlannedEntry
	Strategy     MergeStrategy
	HasConflicts bool
	ConflictPaths []string
}

// NewResolutionPlan creates a new empty ResolutionPlan
func NewResolutionPlan(strategy MergeStrategy) *ResolutionPlan {
	return &ResolutionPlan{
		Entries:       make([]PlannedEntry, 0),
		Strategy:      strategy,
		HasConflicts:  false,
		ConflictPaths: make([]string, 0),
	}
}

// AddEntry adds a planned entry to the plan
func (p *ResolutionPlan) AddEntry(entry PlannedEntry) {
	p.Entries = append(p.Entries, entry)
}

// AddConflict marks a path as having a conflict
func (p *ResolutionPlan) AddConflict(path string) {
	p.HasConflicts = true
	p.ConflictPaths = append(p.ConflictPaths, path)
}

// ConflictCount returns the number of conflicts
func (p *ResolutionPlan) ConflictCount() int {
	return len(p.ConflictPaths)
}

// GetEntry returns the planned entry for a path
func (p *ResolutionPlan) GetEntry(path string) (PlannedEntry, bool) {
	for _, entry := range p.Entries {
		if entry.Path == path {
			return entry, true
		}
	}
	return PlannedEntry{}, false
}

// ApplyResult represents the result of applying a ResolutionPlan
// This is separate from ResolutionPlan to maintain plan purity
type ApplyResult struct {
	TreeHash      string            // Resulting tree hash
	AppliedHashes map[string]string // Path -> resulting hash
	Success       bool
	Error         error
}

// NewApplyResult creates a new ApplyResult
func NewApplyResult() *ApplyResult {
	return &ApplyResult{
		AppliedHashes: make(map[string]string),
		Success:       true,
	}
}
