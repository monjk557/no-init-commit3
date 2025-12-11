package merge

import (
	"time"

	"github.com/collabops/git-executor/internal/diffs"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// MergeMethod represents different merge methods (how to combine branches)
type MergeMethod string

const (
	MergeMethodMerge  MergeMethod = "merge"  // Default: create a merge commit
	MergeMethodSquash MergeMethod = "squash" // Squash all commits into a single commit
	MergeMethodRebase MergeMethod = "rebase" // Rebase source branch onto target (not yet implemented)
)

// MergeStrategy represents different merge strategies (Git's internal merge algorithm)
type MergeStrategy string

const (
	MergeStrategyRecursive MergeStrategy = "recursive" // Default three-way merge
	MergeStrategyResolve   MergeStrategy = "resolve"   // Three-way merge with aggressive resolution
	MergeStrategyORT       MergeStrategy = "ort"       // Optimized Recursive Three-way merge (improved recursive)
	MergeStrategyOurs      MergeStrategy = "ours"      // Keep our version on conflicts
	MergeStrategyTheirs    MergeStrategy = "theirs"    // Keep their version on conflicts
	MergeStrategySubtree   MergeStrategy = "subtree"   // Subtree merge strategy
)

// MergeRequest represents the JSON payload for merge operations
type MergeRequest struct {
	SourceRef     string                 `json:"source_ref" binding:"required"` // Source branch/commit to merge FROM
	TargetRef     string                 `json:"target_ref" binding:"required"` // Target branch/commit to merge INTO
	Method        MergeMethod            `json:"method,omitempty"`              // Merge method (merge, squash, rebase) - default: merge
	Strategy      MergeStrategy          `json:"strategy,omitempty"`            // Merge strategy (default: recursive)
	Message       string                 `json:"message,omitempty"`             // Custom merge commit message
	Author        *AuthorInfo            `json:"author,omitempty"`              // Merge commit author
	Committer     *AuthorInfo            `json:"committer,omitempty"`           // Merge commit committer
	ForceConflict bool                   `json:"force_conflict,omitempty"`      // Force merge even with conflicts
	DryRun        bool                   `json:"dry_run,omitempty"`             // Analyze merge without performing it
	Metadata      map[string]interface{} `json:"metadata,omitempty"`            // Additional metadata
}

// MergeResponse represents the response for merge operations
type MergeResponse struct {
	Success         bool                   `json:"success"`
	MergeCommit     *object.Commit         `json:"merge_commit"`     // Created merge commit info
	IsFastForward   bool                   `json:"is_fast_forward"`  // Whether merge was fast-forward
	HasConflicts    bool                   `json:"has_conflicts"`    // Whether conflicts were detected
	ConflictCount   int                    `json:"conflict_count"`   // Number of conflicts
	Strategy        MergeStrategy          `json:"strategy"`         // Strategy used
	Method          MergeMethod            `json:"method"`           // Merge method used (merge, squash, rebase)
	Message         string                 `json:"message"`          // Operation result message
	Details         map[string]interface{} `json:"details"`          // Additional details
	ConflictSummary *MergeAnalysisResult   `json:"conflict_summary"` // Detailed conflict analysis
}

// MergeResult represents the internal result of a merge operation
type MergeResult struct {
	MergeCommit      *object.Commit
	IsFastForward    bool
	HasConflicts     bool
	ConflictAnalysis *MergeAnalysisResult
}

// mergeFileEntry represents a file's state in base, source, and target
type mergeFileEntry struct {
	path       string
	baseHash   plumbing.Hash
	baseMode   filemode.FileMode
	sourceHash plumbing.Hash
	sourceMode filemode.FileMode
	targetHash plumbing.Hash
	targetMode filemode.FileMode
}

// mergeTreeEntry represents a tree entry's state in base, source, and target
type mergeTreeEntry struct {
	name        string
	baseEntry   *object.TreeEntry
	sourceEntry *object.TreeEntry
	targetEntry *object.TreeEntry
}

// ConflictType은 Merge 충돌의 유형을 나타냅니다.
type ConflictType string

const (
	ConflictTypeContent   ConflictType = "content"   // Content conflicts within file
	ConflictTypeRename    ConflictType = "rename"    // File renamed in both branches
	ConflictTypeDelete    ConflictType = "delete"    // File deleted in one branch, modified in other
	ConflictTypeAdd       ConflictType = "add"       // File added in both branches with different content
	ConflictTypeMode      ConflictType = "mode"      // File mode conflicts (permissions)
	ConflictTypeSubmodule ConflictType = "submodule" // Submodule conflicts
	ConflictTypeSymlink   ConflictType = "symlink"   // Symlink conflicts
)

// ConflictSeverity는 충돌의 심각도 수준을 나타냅니다.
type ConflictSeverity string

const (
	ConflictSeverityLow      ConflictSeverity = "low"      // Easy to resolve automatically
	ConflictSeverityMedium   ConflictSeverity = "medium"   // Requires manual review
	ConflictSeverityHigh     ConflictSeverity = "high"     // Complex conflicts requiring careful resolution
	ConflictSeverityCritical ConflictSeverity = "critical" // Conflicts that may cause data loss if resolved incorrectly
)

// ConflictRegion은 파일 내의 특정 충돌을 나타냅니다.
type ConflictRegion struct {
	StartLine    int    `json:"start_line"`    // Starting line number of conflict
	EndLine      int    `json:"end_line"`      // Ending line number of conflict
	OurContent   string `json:"our_content"`   // Content from our branch
	TheirContent string `json:"their_content"` // Content from their branch
	BaseContent  string `json:"base_content"`  // Content from common ancestor
	MarkerStyle  string `json:"marker_style"`  // Conflict marker style (<<<, ===, >>>)
}

// FileConflict는 단일 파일 내의 충돌을 나타냅니다.
type FileConflict struct {
	FilePath       string                 `json:"file_path"`            // Path to the conflicted file
	ConflictType   ConflictType           `json:"conflict_type"`        // Type of conflict
	Severity       ConflictSeverity       `json:"severity"`             // Conflict severity level
	OurVersion     *FileVersion           `json:"our_version"`          // Our branch version
	TheirVersion   *FileVersion           `json:"their_version"`        // Their branch version
	BaseVersion    *FileVersion           `json:"base_version"`         // Common ancestor version
	Regions        []ConflictRegion       `json:"regions"`              // Specific conflict regions
	AutoResolvable bool                   `json:"auto_resolvable"`      // Can be automatically resolved
	Resolution     *ConflictResolution    `json:"resolution,omitempty"` // Suggested resolution
	Metadata       map[string]interface{} `json:"metadata,omitempty"`   // Additional metadata
}

// FileVersion은 특정 커밋에서의 파일 버전을 나타냅니다.
type FileVersion struct {
	Hash        plumbing.Hash `json:"hash"`              // Object hash
	Mode        uint32        `json:"mode"`              // File mode (permissions)
	Size        int64         `json:"size"`              // File size in bytes
	Content     string        `json:"content,omitempty"` // File content (for small files)
	ContentHash string        `json:"content_hash"`      // Hash of file content
	IsBinary    bool          `json:"is_binary"`         // Whether file is binary
	Exists      bool          `json:"exists"`            // Whether file exists in this version
}

// ConflictResolution represents a suggested resolution for a conflict
type ConflictResolution struct {
	Strategy     string                 `json:"strategy"`           // Resolution strategy
	Content      string                 `json:"content"`            // Resolved content
	Confidence   float64                `json:"confidence"`         // Confidence in resolution (0.0-1.0)
	AutoApplied  bool                   `json:"auto_applied"`       // Whether resolution was automatically applied
	Reasoning    string                 `json:"reasoning"`          // Explanation of resolution
	Alternatives []string               `json:"alternatives"`       // Alternative resolutions
	Metadata     map[string]interface{} `json:"metadata,omitempty"` // Additional metadata
}

// MergeAnalysisRequest represents a request for merge analysis
type MergeAnalysisRequest struct {
	Owner          string            `json:"owner"`           // Repository owner (optional if Repository is provided)
	Repo           string            `json:"repo"`            // Repository name (optional if Repository is provided)
	Repository     *gogit.Repository `json:"-"`               // Already opened repository (우선 사용, Owner/Repo보다 우선)
	SourceRef      string            `json:"source_ref"`      // Source branch/commit to merge FROM
	TargetRef      string            `json:"target_ref"`      // Target branch/commit to merge INTO
	Strategy       MergeStrategy     `json:"strategy"`        // Preferred merge strategy
	AutoResolve    bool              `json:"auto_resolve"`    // Whether to attempt automatic resolution
	IncludeContent bool              `json:"include_content"` // Whether to include file content in response
	MaxFileSize    int64             `json:"max_file_size"`   // Maximum file size to analyze for content conflicts
}

// MergeAnalysisResult represents the result of merge conflict analysis
type MergeAnalysisResult struct {
	CanMerge          bool                     `json:"can_merge"`          // Whether merge is possible
	HasConflicts      bool                     `json:"has_conflicts"`      // Whether conflicts exist
	IsFastForward     bool                     `json:"is_fast_forward"`    // Whether merge is fast-forward
	ConflictCount     int                      `json:"conflict_count"`     // Total number of conflicts
	FileConflicts     []FileConflict           `json:"file_conflicts"`     // Detailed file conflicts
	MergeBase         *CommitInfo              `json:"merge_base"`         // Common ancestor commit
	SourceCommit      *CommitInfo              `json:"source_commit"`      // Source commit info
	TargetCommit      *CommitInfo              `json:"target_commit"`      // Target commit info
	Strategy          MergeStrategy            `json:"strategy"`           // Merge strategy used
	AutoResolvable    int                      `json:"auto_resolvable"`    // Number of auto-resolvable conflicts
	SeverityBreakdown map[ConflictSeverity]int `json:"severity_breakdown"` // Conflicts by severity
	EstimatedTime     time.Duration            `json:"estimated_time"`     // Estimated resolution time
	Recommendations   []string                 `json:"recommendations"`    // Resolution recommendations
	Metadata          map[string]interface{}   `json:"metadata,omitempty"` // Additional metadata
}

// AuthorInfo represents commit author information
type AuthorInfo struct {
	Name  string    `json:"name"`
	Email string    `json:"email"`
	Date  time.Time `json:"date"`
}

// CommitInfo represents commit information for API responses
type CommitInfo struct {
	SHA       string     `json:"sha"`        // Commit hash
	Message   string     `json:"message"`    // Commit message
	Author    AuthorInfo `json:"author"`     // Author information
	Committer AuthorInfo `json:"committer"`  // Committer information
	CreatedAt string     `json:"created_at"` // Creation timestamp (ISO 8601)
	TreeSHA   string     `json:"tree_sha"`   // Tree hash
}

// convertCommitToInfo converts object.Commit to CommitInfo
func convertCommitToInfo(c *object.Commit) *CommitInfo {
	if c == nil {
		return nil
	}
	return &CommitInfo{
		SHA:     c.Hash.String(),
		Message: c.Message,
		Author: AuthorInfo{
			Name:  c.Author.Name,
			Email: c.Author.Email,
			Date:  c.Author.When,
		},
		Committer: AuthorInfo{
			Name:  c.Committer.Name,
			Email: c.Committer.Email,
			Date:  c.Committer.When,
		},
		CreatedAt: c.Committer.When.Format(time.RFC3339),
		TreeSHA:   c.TreeHash.String(),
	}
}

// CommitStats는 브랜치 비교를 위한 커밋 수 차이를 나타냅니다
type CommitStats struct {
	Ahead  int `json:"ahead"`  // base에 없는 head의 커밋들
	Behind int `json:"behind"` // head에 없는 base의 커밋들
}

// CommitsByDate는 날짜별로 그룹화된 커밋 목록을 나타냅니다
type CommitsByDate struct {
	Date    string                 `json:"date"`    // YYYY-MM-DD 형식의 날짜
	Commits []diffs.CommitMetadata `json:"commits"` // 해당 날짜의 커밋 목록
}

// MergePreviewRequest represents the request for merge preview operations
type MergePreviewRequest struct {
	SourceRef       string        `form:"source_ref" binding:"required"` // Source branch/commit to merge FROM
	TargetRef       string        `form:"target_ref" binding:"required"` // Target branch/commit to merge INTO
	Strategy        MergeStrategy `form:"strategy,omitempty"`            // Merge strategy (default: recursive)
	IncludeChanges  bool          `form:"include_changes,omitempty"`     // Include detailed file changes
	IncludeContent  bool          `form:"include_content,omitempty"`     // Include file content previews
	MaxFileSize     int64         `form:"max_file_size,omitempty"`       // Max file size for content preview (default: 1MB)
	MaxFiles        int           `form:"max_files,omitempty"`           // Max files to analyze (default: 1000)
	CompareStrategy string        `form:"compare_strategy,omitempty"`    // tree, diff, or full (default: tree)
}

// MergePreviewResponse represents the response for merge preview operations
type MergePreviewResponse struct {
	CanMerge           bool                 `json:"can_merge"`                     // Whether merge can proceed
	IsFastForward      bool                 `json:"is_fast_forward"`               // Whether merge would be fast-forward
	HasConflicts       bool                 `json:"has_conflicts"`                 // Whether conflicts exist
	ConflictCount      int                  `json:"conflict_count"`                // Number of conflicts
	Strategy           MergeStrategy        `json:"strategy"`                      // Strategy to be used
	RecommendedActions []string             `json:"recommended_actions,omitempty"` // Recommended actions
	MergeBase          *object.Commit       `json:"merge_base,omitempty"`          // Common ancestor commit
	ConflictSummary    *MergeAnalysisResult `json:"conflict_summary,omitempty"`    // Detailed conflict analysis
	ChangesSummary     *ChangesSummary      `json:"changes_summary,omitempty"`     // Summary of changes
	FilesChanged       []*FileChange        `json:"files_changed,omitempty"`       // Detailed file changes
	PerformanceMetrics *PreviewMetrics      `json:"performance_metrics,omitempty"` // Analysis performance
	Warnings           []string             `json:"warnings,omitempty"`            // Any warnings or notes
	// GitHub PR style statistics
	Commits       *CommitStats    `json:"commits,omitempty"`         // Commit statistics (ahead/behind)
	Contributors  []string        `json:"contributors,omitempty"`    // List of contributors
	CommitsByDate []CommitsByDate `json:"commits_by_date,omitempty"` // Commits grouped by date

	// Simple count summary for UI display
	CountSummary *CountSummary `json:"count_summary,omitempty"` // Simple count summary
}

// CountSummary provides simple count summary for UI display
type CountSummary struct {
	Commits      int `json:"commits"`      // Total commits count
	Files        int `json:"files"`        // Total files changed count
	Contributors int `json:"contributors"` // Total contributors count
}

// ChangesSummary provides high-level statistics about merge changes
type ChangesSummary struct {
	TotalFiles    int `json:"total_files"`    // Total files affected
	AddedFiles    int `json:"added_files"`    // Files added
	ModifiedFiles int `json:"modified_files"` // Files modified
	DeletedFiles  int `json:"deleted_files"`  // Files deleted
	RenamedFiles  int `json:"renamed_files"`  // Files renamed
	LinesAdded    int `json:"lines_added"`    // Total lines added
	LinesDeleted  int `json:"lines_deleted"`  // Total lines deleted
	BinaryFiles   int `json:"binary_files"`   // Binary files affected
}

// FileChange represents a change to a single file in the merge
// Reuses FileDiff from diff.go but adds merge-specific fields
type FileChange struct {
	diffs.FileDiff                            // Embed diffs.FileDiff to reuse all diff functionality
	Content            *FileContent           `json:"content,omitempty"`             // File content preview
	Conflicts          []string               `json:"conflicts,omitempty"`           // Conflict descriptions
	ContentAnalysis    map[string]interface{} `json:"content_analysis,omitempty"`    // Detailed content analysis
	PotentialConflicts []string               `json:"potential_conflicts,omitempty"` // Potential merge conflicts
}

// FileContent represents file content preview
type FileContent struct {
	SourceContent string `json:"source_content,omitempty"` // Source file content
	TargetContent string `json:"target_content,omitempty"` // Target file content
	MergedContent string `json:"merged_content,omitempty"` // Predicted merged content
	Encoding      string `json:"encoding,omitempty"`       // Content encoding
	IsTruncated   bool   `json:"is_truncated,omitempty"`   // Whether content was truncated
}

// PreviewMetrics contains performance metrics for the preview operation
type PreviewMetrics struct {
	AnalysisTime         time.Duration `json:"analysis_time"`          // Total analysis time
	ConflictAnalysisTime time.Duration `json:"conflict_analysis_time"` // Conflict analysis time
	TreeComparisonTime   time.Duration `json:"tree_comparison_time"`   // Tree comparison time
	FilesAnalyzed        int           `json:"files_analyzed"`         // Number of files analyzed
	ObjectsTraversed     int           `json:"objects_traversed"`      // Git objects traversed
	MemoryUsed           int64         `json:"memory_used,omitempty"`  // Memory usage estimate
}
