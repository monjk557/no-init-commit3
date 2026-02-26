package merge

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

// NativeGitExecutor provides native Git merge operations
type NativeGitExecutor struct {
	orchestrator *NativeGitOrchestrator
	logger       *zap.Logger
	config       *NativeGitExecutorConfig
}

// NativeGitExecutorConfig configures the native Git executor
type NativeGitExecutorConfig struct {
	EnableAtomicOperations bool
	EnableHooks            bool
	DefaultMergeStrategy   string
	WorkingDirectory       string
}

// GitMergeInterface defines the interface for Git merge operations
type GitMergeInterface interface {
	// Core merge operations
	ThreeWayMerge(ctx context.Context, req *NativeThreeWayMergeRequest) (*NativeMergeResult, error)
	FastForwardMerge(ctx context.Context, req *NativeFastForwardRequest) (*NativeMergeResult, error)
	RebaseMerge(ctx context.Context, req *NativeRebaseRequest) (*NativeMergeResult, error)
	SquashMerge(ctx context.Context, req *NativeSquashMergeRequest) (*NativeMergeResult, error)

	// Merge analysis and preview
	PreviewMerge(ctx context.Context, req *NativeMergePreviewRequest) (*NativeMergePreview, error)
	AnalyzeConflicts(ctx context.Context, req *NativeConflictAnalysisRequest) (*NativeConflictAnalysis, error)

	// Repository state operations
	GetRepositoryStatus(ctx context.Context, repoPath string) (*NativeRepositoryStatus, error)
	ValidateRepository(ctx context.Context, repoPath string) (*NativeRepositoryValidation, error)
}

// Native merge operation requests
type NativeThreeWayMergeRequest struct {
	RepositoryPath string
	SourceRef      string
	TargetRef      string
	Strategy       string
	Message        string
	Author         *GitSignature
	Committer      *GitSignature
	ForceConflict  bool
	NoFF           bool // Force merge commit even for fast-forward
}

type NativeFastForwardRequest struct {
	RepositoryPath string
	SourceRef      string
	TargetRef      string
	Force          bool
}

type NativeRebaseRequest struct {
	RepositoryPath string
	SourceRef      string
	TargetRef      string
	Interactive    bool
	PreserveMerges bool
	Strategy       string
}

type NativeSquashMergeRequest struct {
	RepositoryPath string
	SourceRef      string
	TargetRef      string
	Message        string
	Author         *GitSignature
	NoCommit       bool
}

type NativeMergePreviewRequest struct {
	RepositoryPath string
	SourceRef      string
	TargetRef      string
	Strategy       string
}

type NativeConflictAnalysisRequest struct {
	RepositoryPath string
	SourceRef      string
	TargetRef      string
	Strategy       string
	MaxFiles       int
}

// Native merge operation results
type NativeMergeResult struct {
	Success         bool
	IsFastForward   bool
	HasConflicts    bool
	MergeCommitHash string
	ConflictFiles   []string
	Statistics      *MergeStatistics
	Duration        time.Duration
	Strategy        string
}

type NativeMergePreview struct {
	CanMerge            bool
	IsFastForward       bool
	ConflictCount       int
	ConflictFiles       []string
	ChangedFiles        []string
	AddedFiles          []string
	DeletedFiles        []string
	EstimatedTime       time.Duration
	RecommendedStrategy string
}

type NativeConflictAnalysis struct {
	HasConflicts   bool
	ConflictCount  int
	Conflicts      []*NativeFileConflict
	AutoResolvable int
	ManualRequired int
	Severity       ConflictSeverity
}

type NativeRepositoryStatus struct {
	IsClean         bool
	StagedFiles     []string
	ModifiedFiles   []string
	UntrackedFiles  []string
	ConflictedFiles []string
	Branch          string
}

type NativeRepositoryValidation struct {
	IsValid         bool
	Issues          []string
	Warnings        []string
	Recommendations []string
}

// Supporting types
type GitSignature struct {
	Name  string
	Email string
	When  time.Time
}

type MergeStatistics struct {
	FilesChanged int
	Insertions   int
	Deletions    int
	Duration     time.Duration
	Strategy     string
}

type NativeFileConflict struct {
	Path         string
	ConflictType ConflictType     // Reuse existing type from schema.go
	Severity     ConflictSeverity // Reuse existing type from schema.go
	SourceLines  []string
	TargetLines  []string
	BaseLines    []string
}

// NewNativeGitExecutor creates a new native Git executor
func NewNativeGitExecutor(config *NativeGitExecutorConfig, logger *zap.Logger) (*NativeGitExecutor, error) {
	if config == nil {
		config = &NativeGitExecutorConfig{
			EnableAtomicOperations: true,
			EnableHooks:            true,
			DefaultMergeStrategy:   "ort",
			WorkingDirectory:       ".",
		}
	}

	// Create orchestrator configuration
	orchestratorConfig := &NativeGitConfig{
		GitExecutable:    "git",
		WorkingDirectory: config.WorkingDirectory,
		DefaultTimeout:   60 * time.Second,
		MaxConcurrency:   10,
		EnableMetrics:    true,
		EnableTracing:    true,
	}

	// Initialize orchestrator
	orchestrator := NewNativeGitOrchestrator(orchestratorConfig, logger)

	executor := &NativeGitExecutor{
		orchestrator: orchestrator,
		logger:       logger,
		config:       config,
	}

	logger.Info("Native Git Executor initialized",
		zap.Bool("atomic_operations", config.EnableAtomicOperations),
		zap.Bool("hooks_enabled", config.EnableHooks),
		zap.String("default_strategy", config.DefaultMergeStrategy),
		zap.String("working_directory", config.WorkingDirectory))

	return executor, nil
}

// ThreeWayMerge performs a three-way merge using native Git
func (e *NativeGitExecutor) ThreeWayMerge(ctx context.Context, req *NativeThreeWayMergeRequest) (*NativeMergeResult, error) {
	e.logger.Info("Starting three-way merge",
		zap.String("repository", req.RepositoryPath),
		zap.String("source", req.SourceRef),
		zap.String("target", req.TargetRef),
		zap.String("strategy", req.Strategy),
		zap.Bool("force_conflict", req.ForceConflict))

	startTime := time.Now()

	// Validate repository
	if err := e.validateRepository(ctx, req.RepositoryPath); err != nil {
		return nil, fmt.Errorf("repository validation failed: %w", err)
	}

	// Check if merge is needed (fast-forward detection)
	isFastForward, err := e.checkFastForward(ctx, req.RepositoryPath, req.SourceRef, req.TargetRef)
	if err != nil {
		return nil, fmt.Errorf("fast-forward check failed: %w", err)
	}

	// Prepare merge arguments
	args := []string{"merge"}

	// Add strategy if specified
	if req.Strategy != "" {
		args = append(args, "-s", req.Strategy)
	}

	// Force merge commit even for fast-forward if requested
	if req.NoFF || !isFastForward {
		args = append(args, "--no-ff")
	}

	// Add message if specified
	if req.Message != "" {
		args = append(args, "-m", req.Message)
	}

	// Handle conflicts
	if req.ForceConflict {
		// Allow automatic conflict resolution
		args = append(args, "--strategy-option=ours")
	}

	// Add source reference
	args = append(args, req.SourceRef)

	// Execute merge command
	result, err := e.orchestrator.ExecuteGitCommand(ctx, req.RepositoryPath, args...)
	if err != nil {
		// Check if error is due to conflicts
		if strings.Contains(err.Error(), "conflict") || strings.Contains(string(result.ErrorOutput), "conflict") {
			return &NativeMergeResult{
				Success:       false,
				HasConflicts:  true,
				ConflictFiles: e.parseConflictFiles(string(result.Output)),
				Duration:      time.Since(startTime),
				Strategy:      req.Strategy,
			}, nil
		}
		return nil, fmt.Errorf("merge command failed: %w", err)
	}

	// Get merge commit hash
	commitHash, err := e.getCommitHash(ctx, req.RepositoryPath, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("failed to get merge commit hash: %w", err)
	}

	// Parse merge statistics
	stats := e.parseMergeStatistics(string(result.Output))
	stats.Duration = time.Since(startTime)
	stats.Strategy = req.Strategy

	return &NativeMergeResult{
		Success:         true,
		IsFastForward:   isFastForward && !req.NoFF,
		HasConflicts:    false,
		MergeCommitHash: commitHash,
		Statistics:      stats,
		Duration:        time.Since(startTime),
		Strategy:        req.Strategy,
	}, nil
}

// FastForwardMerge performs a fast-forward merge
func (e *NativeGitExecutor) FastForwardMerge(ctx context.Context, req *NativeFastForwardRequest) (*NativeMergeResult, error) {
	e.logger.Info("Starting fast-forward merge",
		zap.String("repository", req.RepositoryPath),
		zap.String("source", req.SourceRef),
		zap.String("target", req.TargetRef),
		zap.Bool("force", req.Force))

	startTime := time.Now()

	// Check if fast-forward is possible
	canFF, err := e.checkFastForward(ctx, req.RepositoryPath, req.SourceRef, req.TargetRef)
	if err != nil {
		return nil, fmt.Errorf("fast-forward check failed: %w", err)
	}

	if !canFF && !req.Force {
		return nil, fmt.Errorf("fast-forward not possible and force not enabled")
	}

	// Prepare merge arguments for fast-forward only
	args := []string{"merge", "--ff-only", req.SourceRef}

	// Execute merge command
	_, err = e.orchestrator.ExecuteGitCommand(ctx, req.RepositoryPath, args...)
	if err != nil {
		if req.Force {
			// Fallback to force update
			return e.forceFastForward(ctx, req)
		}
		return nil, fmt.Errorf("fast-forward merge failed: %w", err)
	}

	// Get updated commit hash
	commitHash, err := e.getCommitHash(ctx, req.RepositoryPath, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("failed to get commit hash: %w", err)
	}

	return &NativeMergeResult{
		Success:         true,
		IsFastForward:   true,
		HasConflicts:    false,
		MergeCommitHash: commitHash,
		Duration:        time.Since(startTime),
		Statistics: &MergeStatistics{
			Duration: time.Since(startTime),
			Strategy: "fast-forward",
		},
	}, nil
}

// SquashMerge performs a squash merge
func (e *NativeGitExecutor) SquashMerge(ctx context.Context, req *NativeSquashMergeRequest) (*NativeMergeResult, error) {
	e.logger.Info("Starting squash merge",
		zap.String("repository", req.RepositoryPath),
		zap.String("source", req.SourceRef),
		zap.String("target", req.TargetRef),
		zap.Bool("no_commit", req.NoCommit))

	startTime := time.Now()

	// Prepare squash merge arguments
	args := []string{"merge", "--squash", req.SourceRef}

	// Execute squash merge command
	result, err := e.orchestrator.ExecuteGitCommand(ctx, req.RepositoryPath, args...)
	if err != nil {
		return nil, fmt.Errorf("squash merge failed: %w", err)
	}

	var commitHash string
	if !req.NoCommit {
		// Create commit after squash
		commitArgs := []string{"commit"}
		if req.Message != "" {
			commitArgs = append(commitArgs, "-m", req.Message)
		}
		if req.Author != nil {
			commitArgs = append(commitArgs, "--author", fmt.Sprintf("%s <%s>", req.Author.Name, req.Author.Email))
		}

		commitResult, err := e.orchestrator.ExecuteGitCommand(ctx, req.RepositoryPath, commitArgs...)
		if err != nil {
			return nil, fmt.Errorf("commit after squash failed: %w", err)
		}

		// Get commit hash
		commitHash, err = e.getCommitHash(ctx, req.RepositoryPath, "HEAD")
		if err != nil {
			return nil, fmt.Errorf("failed to get commit hash: %w", err)
		}

		e.logger.Debug("Squash commit created", zap.String("output", string(commitResult.Output)))
	}

	// Parse merge statistics
	stats := e.parseMergeStatistics(string(result.Output))
	stats.Duration = time.Since(startTime)
	stats.Strategy = "squash"

	return &NativeMergeResult{
		Success:         true,
		IsFastForward:   false, // Squash is never fast-forward
		HasConflicts:    false, // Would have failed if conflicts
		MergeCommitHash: commitHash,
		Statistics:      stats,
		Duration:        time.Since(startTime),
		Strategy:        "squash",
	}, nil
}

// RebaseMerge performs a rebase operation
func (e *NativeGitExecutor) RebaseMerge(ctx context.Context, req *NativeRebaseRequest) (*NativeMergeResult, error) {
	e.logger.Info("Starting rebase",
		zap.String("repository", req.RepositoryPath),
		zap.String("source", req.SourceRef),
		zap.String("target", req.TargetRef),
		zap.Bool("interactive", req.Interactive),
		zap.Bool("preserve_merges", req.PreserveMerges))

	startTime := time.Now()

	// Prepare rebase arguments
	args := []string{"rebase"}

	if req.Interactive {
		args = append(args, "-i")
	}

	if req.PreserveMerges {
		args = append(args, "-p")
	}

	if req.Strategy != "" {
		args = append(args, "-s", req.Strategy)
	}

	args = append(args, req.TargetRef, req.SourceRef)

	// Execute rebase command
	result, err := e.orchestrator.ExecuteGitCommand(ctx, req.RepositoryPath, args...)
	if err != nil {
		// Check if error is due to conflicts
		if strings.Contains(err.Error(), "conflict") {
			return &NativeMergeResult{
				Success:       false,
				HasConflicts:  true,
				ConflictFiles: e.parseConflictFiles(string(result.Output)),
				Duration:      time.Since(startTime),
				Strategy:      "rebase",
			}, nil
		}
		return nil, fmt.Errorf("rebase failed: %w", err)
	}

	// Get final commit hash
	commitHash, err := e.getCommitHash(ctx, req.RepositoryPath, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("failed to get commit hash: %w", err)
	}

	return &NativeMergeResult{
		Success:         true,
		IsFastForward:   false, // Rebase creates new commits
		HasConflicts:    false,
		MergeCommitHash: commitHash,
		Duration:        time.Since(startTime),
		Strategy:        "rebase",
		Statistics: &MergeStatistics{
			Duration: time.Since(startTime),
			Strategy: "rebase",
		},
	}, nil
}

// PreviewMerge analyzes a merge without performing it
func (e *NativeGitExecutor) PreviewMerge(ctx context.Context, req *NativeMergePreviewRequest) (*NativeMergePreview, error) {
	e.logger.Debug("Starting merge preview",
		zap.String("repository", req.RepositoryPath),
		zap.String("source", req.SourceRef),
		zap.String("target", req.TargetRef))

	// Check fast-forward possibility
	isFastForward, err := e.checkFastForward(ctx, req.RepositoryPath, req.SourceRef, req.TargetRef)
	if err != nil {
		return nil, fmt.Errorf("fast-forward check failed: %w", err)
	}

	if isFastForward {
		return &NativeMergePreview{
			CanMerge:            true,
			IsFastForward:       true,
			ConflictCount:       0,
			EstimatedTime:       1 * time.Second, // Fast-forward is quick
			RecommendedStrategy: "fast-forward",
		}, nil
	}

	// Use git merge-tree for dry-run analysis
	args := []string{"merge-tree", req.TargetRef, req.SourceRef}
	result, err := e.orchestrator.ExecuteGitCommand(ctx, req.RepositoryPath, args...)
	if err != nil {
		return nil, fmt.Errorf("merge-tree analysis failed: %w", err)
	}

	// Parse merge-tree output
	preview := e.parseMergeTreeOutput(string(result.Output))
	preview.IsFastForward = isFastForward

	// Estimate completion time based on complexity
	preview.EstimatedTime = e.estimateMergeTime(len(preview.ConflictFiles), len(preview.ChangedFiles))

	return preview, nil
}

// Helper methods

func (e *NativeGitExecutor) validateRepository(ctx context.Context, repoPath string) error {
	// Check if directory exists
	if !filepath.IsAbs(repoPath) {
		repoPath = filepath.Join(e.config.WorkingDirectory, repoPath)
	}

	// Check if it's a git repository
	result, err := e.orchestrator.ExecuteGitCommand(ctx, repoPath, "rev-parse", "--git-dir")
	if err != nil {
		return fmt.Errorf("not a git repository: %w", err)
	}

	e.logger.Debug("Repository validated", zap.String("git_dir", string(result.Output)))
	return nil
}

func (e *NativeGitExecutor) checkFastForward(ctx context.Context, repoPath, sourceRef, targetRef string) (bool, error) {
	// Check if target is ancestor of source (fast-forward possible)
	result, err := e.orchestrator.ExecuteGitCommand(ctx, repoPath, "merge-base", "--is-ancestor", targetRef, sourceRef)
	return err == nil && result.ExitCode == 0, nil
}

func (e *NativeGitExecutor) getCommitHash(ctx context.Context, repoPath, ref string) (string, error) {
	result, err := e.orchestrator.ExecuteGitCommand(ctx, repoPath, "rev-parse", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(result.Output)), nil
}

func (e *NativeGitExecutor) forceFastForward(ctx context.Context, req *NativeFastForwardRequest) (*NativeMergeResult, error) {
	// Force update the branch
	args := []string{"reset", "--hard", req.SourceRef}
	result, err := e.orchestrator.ExecuteGitCommand(ctx, req.RepositoryPath, args...)
	if err != nil {
		return nil, fmt.Errorf("force fast-forward failed: %w", err)
	}

	commitHash, err := e.getCommitHash(ctx, req.RepositoryPath, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("failed to get commit hash: %w", err)
	}

	e.logger.Warn("Forced fast-forward performed", zap.String("output", string(result.Output)))

	return &NativeMergeResult{
		Success:         true,
		IsFastForward:   true,
		HasConflicts:    false,
		MergeCommitHash: commitHash,
		Statistics:      &MergeStatistics{Strategy: "force-fast-forward"},
	}, nil
}

func (e *NativeGitExecutor) parseConflictFiles(output string) []string {
	var conflicts []string
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "CONFLICT") {
			// Extract file path from conflict message
			parts := strings.Split(line, " in ")
			if len(parts) > 1 {
				conflicts = append(conflicts, strings.TrimSpace(parts[1]))
			}
		}
	}
	return conflicts
}

func (e *NativeGitExecutor) parseMergeStatistics(output string) *MergeStatistics {
	stats := &MergeStatistics{}

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "file changed") || strings.Contains(line, "files changed") {
			// Parse statistics from output like "5 files changed, 10 insertions(+), 2 deletions(-)"
			// This is a simplified parser - would need more robust parsing in production
			parts := strings.Fields(line)
			for i, part := range parts {
				switch part {
				case "files", "file":
					if i > 0 {
						// Try to parse number before "files"
						fmt.Sscanf(parts[i-1], "%d", &stats.FilesChanged)
					}
				case "insertions(+),":
					if i > 0 {
						fmt.Sscanf(parts[i-1], "%d", &stats.Insertions)
					}
				case "deletions(-)":
					if i > 0 {
						fmt.Sscanf(parts[i-1], "%d", &stats.Deletions)
					}
				}
			}
		}
	}

	return stats
}

func (e *NativeGitExecutor) parseMergeTreeOutput(output string) *NativeMergePreview {
	preview := &NativeMergePreview{
		CanMerge: true, // merge-tree succeeding means merge is possible
	}

	if strings.TrimSpace(output) == "" {
		// Empty output means clean merge
		preview.ConflictCount = 0
		preview.RecommendedStrategy = "ort"
		return preview
	}

	// Parse conflicts and changes
	lines := strings.Split(output, "\n")
	conflictFiles := make(map[string]bool)
	changedFiles := make(map[string]bool)

	for _, line := range lines {
		if strings.HasPrefix(line, "<<<<<<< ") || strings.HasPrefix(line, ">>>>>>> ") || strings.HasPrefix(line, "======= ") {
			// This indicates conflicts in the output
			preview.ConflictCount++
		}
		// Additional parsing logic would be needed for more detailed analysis
	}

	// Convert maps to slices
	for file := range conflictFiles {
		preview.ConflictFiles = append(preview.ConflictFiles, file)
	}
	for file := range changedFiles {
		preview.ChangedFiles = append(preview.ChangedFiles, file)
	}

	// Recommend strategy based on complexity
	if preview.ConflictCount > 10 {
		preview.RecommendedStrategy = "recursive"
	} else {
		preview.RecommendedStrategy = "ort"
	}

	return preview
}

func (e *NativeGitExecutor) estimateMergeTime(conflictCount, changedFileCount int) time.Duration {
	baseTime := 5 * time.Second
	conflictTime := time.Duration(conflictCount) * 2 * time.Second
	fileTime := time.Duration(changedFileCount) * 100 * time.Millisecond

	return baseTime + conflictTime + fileTime
}

// Additional interface implementations would go here...
func (e *NativeGitExecutor) AnalyzeConflicts(ctx context.Context, req *NativeConflictAnalysisRequest) (*NativeConflictAnalysis, error) {
	// Implementation placeholder
	return &NativeConflictAnalysis{}, fmt.Errorf("not implemented")
}

func (e *NativeGitExecutor) GetRepositoryStatus(ctx context.Context, repoPath string) (*NativeRepositoryStatus, error) {
	// Implementation placeholder
	return &NativeRepositoryStatus{}, fmt.Errorf("not implemented")
}

func (e *NativeGitExecutor) ValidateRepository(ctx context.Context, repoPath string) (*NativeRepositoryValidation, error) {
	// Implementation placeholder
	return &NativeRepositoryValidation{}, fmt.Errorf("not implemented")
}
