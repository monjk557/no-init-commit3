package merge

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/collabops/git-executor/internal/config"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// MigrationAdapter provides gradual migration from go-git to native Git
type MigrationAdapter struct {
	// Legacy go-git handler
	legacyHandler *MergeOperationsHandler

	// Native Git executor
	nativeExecutor *NativeGitExecutor

	// Configuration and feature flags
	config         *config.Config
	migrationFlags *MigrationFlags
	logger         *zap.Logger

	// A/B testing and validation
	validator *MigrationValidator
	metrics   *MigrationMetrics
}

// MigrationFlags controls which operations use native Git
type MigrationFlags struct {
	// Global enable flag
	EnableNativeGit bool `mapstructure:"enable_native_git"`

	// Per-operation flags
	EnableNativeThreeWay    bool `mapstructure:"enable_native_three_way"`
	EnableNativeSquash      bool `mapstructure:"enable_native_squash"`
	EnableNativeRebase      bool `mapstructure:"enable_native_rebase"`
	EnableNativeFastForward bool `mapstructure:"enable_native_fast_forward"`

	// Repository-based rollout
	EnabledRepositories  []string `mapstructure:"enabled_repositories"`
	DisabledRepositories []string `mapstructure:"disabled_repositories"`

	// Percentage-based rollout
	RolloutPercentage int `mapstructure:"rollout_percentage"`

	// Safety controls
	ValidateResults    bool `mapstructure:"validate_results"`
	FallbackOnError    bool `mapstructure:"fallback_on_error"`
	ParallelValidation bool `mapstructure:"parallel_validation"`
}

// MigrationValidator validates native Git results against go-git results
type MigrationValidator struct {
	logger *zap.Logger
}

// MigrationMetrics tracks migration performance and success rates
type MigrationMetrics struct {
	// Operation counts
	NativeOperations int64
	LegacyOperations int64
	Validations      int64

	// Success rates
	NativeSuccessRate  float64
	LegacySuccessRate  float64
	ValidationPassRate float64

	// Performance comparison
	NativeAverageTime time.Duration
	LegacyAverageTime time.Duration

	// Error tracking
	NativeErrors     map[string]int
	LegacyErrors     map[string]int
	ValidationErrors map[string]int

	logger *zap.Logger
}

// MigrationValidationResult represents the result of comparing native vs legacy operations
type MigrationValidationResult struct {
	Passed           bool
	Differences      []string
	NativeResult     interface{}
	LegacyResult     interface{}
	NativeDuration   time.Duration
	LegacyDuration   time.Duration
	ValidationErrors []error
}

// NewMigrationAdapter creates a new migration adapter
func NewMigrationAdapter(legacyHandler *MergeOperationsHandler, cfg *config.Config, logger *zap.Logger) (*MigrationAdapter, error) {
	// Initialize native executor
	nativeConfig := &NativeGitExecutorConfig{
		EnableAtomicOperations: true,
		EnableHooks:            true,
		DefaultMergeStrategy:   "ort",
		WorkingDirectory:       cfg.Git.RepoRoot,
	}

	nativeExecutor, err := NewNativeGitExecutor(nativeConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create native executor: %w", err)
	}

	// Load migration flags from config
	migrationFlags := loadMigrationFlags(cfg)

	// Initialize validator and metrics
	validator := &MigrationValidator{logger: logger}
	metrics := &MigrationMetrics{
		NativeErrors:     make(map[string]int),
		LegacyErrors:     make(map[string]int),
		ValidationErrors: make(map[string]int),
		logger:           logger,
	}

	adapter := &MigrationAdapter{
		legacyHandler:  legacyHandler,
		nativeExecutor: nativeExecutor,
		config:         cfg,
		migrationFlags: migrationFlags,
		logger:         logger,
		validator:      validator,
		metrics:        metrics,
	}

	logger.Info("Migration Adapter initialized",
		zap.Bool("native_git_enabled", migrationFlags.EnableNativeGit),
		zap.Int("rollout_percentage", migrationFlags.RolloutPercentage),
		zap.Bool("validation_enabled", migrationFlags.ValidateResults),
		zap.Bool("fallback_enabled", migrationFlags.FallbackOnError))

	return adapter, nil
}

// PerformMerge routes merge operations between native and legacy implementations
func (ma *MigrationAdapter) PerformMerge(ctx context.Context, repo *gogit.Repository, sourceHash, targetHash plumbing.Hash, req *MergeRequest, analysis *MergeAnalysisResult) (*MergeResult, error) {
	startTime := time.Now()

	// Determine execution strategy
	useNative := ma.shouldUseNative(req, repo)

	ma.logger.Info("Routing merge operation",
		zap.String("method", string(req.Method)),
		zap.Bool("use_native", useNative),
		zap.String("source", sourceHash.String()),
		zap.String("target", targetHash.String()))

	if !useNative {
		// Use legacy go-git implementation
		result, err := ma.legacyHandler.performMerge(ctx, repo, sourceHash, targetHash, req, analysis)
		ma.metrics.RecordLegacyOperation(time.Since(startTime), err == nil)
		return result, err
	}

	// Use native Git implementation
	nativeResult, err := ma.performNativeMerge(ctx, repo, sourceHash, targetHash, req, analysis)

	// Handle validation if enabled
	if ma.migrationFlags.ValidateResults {
		if ma.migrationFlags.ParallelValidation {
			// Run validation in parallel
			go ma.validateInBackground(ctx, repo, sourceHash, targetHash, req, analysis, nativeResult, err)
		} else {
			// Run validation synchronously
			ma.validateSync(ctx, repo, sourceHash, targetHash, req, analysis, nativeResult, err)
		}
	}

	// Handle fallback on error
	if err != nil && ma.migrationFlags.FallbackOnError {
		ma.logger.Warn("Native Git operation failed, falling back to legacy",
			zap.Error(err),
			zap.String("method", string(req.Method)))

		legacyResult, legacyErr := ma.legacyHandler.performMerge(ctx, repo, sourceHash, targetHash, req, analysis)
		if legacyErr == nil {
			ma.metrics.RecordFallback(time.Since(startTime))
			return legacyResult, nil
		}

		// Both failed
		ma.logger.Error("Both native and legacy operations failed",
			zap.Error(err),
			zap.NamedError("legacy_error", legacyErr))
		return nil, fmt.Errorf("native operation failed: %w (legacy fallback also failed: %v)", err, legacyErr)
	}

	ma.metrics.RecordNativeOperation(time.Since(startTime), err == nil)
	return nativeResult, err
}

// performNativeMerge executes merge using native Git
func (ma *MigrationAdapter) performNativeMerge(ctx context.Context, repo *gogit.Repository, sourceHash, targetHash plumbing.Hash, req *MergeRequest, analysis *MergeAnalysisResult) (*MergeResult, error) {
	// Convert go-git repository to repository path
	workTree, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("failed to get worktree: %w", err)
	}
	repoPath := workTree.Filesystem.Root()

	switch req.Method {
	case MergeMethodMerge:
		return ma.performNativeThreeWayMerge(ctx, repoPath, sourceHash, targetHash, req)

	case MergeMethodSquash:
		return ma.performNativeSquashMerge(ctx, repoPath, sourceHash, targetHash, req)

	case MergeMethodRebase:
		return ma.performNativeRebaseMerge(ctx, repoPath, sourceHash, targetHash, req)

	default:
		return nil, fmt.Errorf("unsupported merge method: %s", req.Method)
	}
}

// performNativeThreeWayMerge executes three-way merge using native Git
func (ma *MigrationAdapter) performNativeThreeWayMerge(ctx context.Context, repoPath string, sourceHash, targetHash plumbing.Hash, req *MergeRequest) (*MergeResult, error) {
	nativeReq := &NativeThreeWayMergeRequest{
		RepositoryPath: repoPath,
		SourceRef:      sourceHash.String(),
		TargetRef:      targetHash.String(),
		Strategy:       string(req.Strategy),
		Message:        req.Message,
		ForceConflict:  req.ForceConflict,
		NoFF:           true, // Always create merge commit
	}

	// Add author/committer if available
	if req.Author != nil {
		nativeReq.Author = &GitSignature{
			Name:  req.Author.Name,
			Email: req.Author.Email,
			When:  req.Author.Date,
		}
	}

	nativeResult, err := ma.nativeExecutor.ThreeWayMerge(ctx, nativeReq)
	if err != nil {
		return nil, err
	}

	// Convert native result back to legacy format
	return ma.convertNativeToLegacyResult(nativeResult), nil
}

// performNativeSquashMerge executes squash merge using native Git
func (ma *MigrationAdapter) performNativeSquashMerge(ctx context.Context, repoPath string, sourceHash, targetHash plumbing.Hash, req *MergeRequest) (*MergeResult, error) {
	nativeReq := &NativeSquashMergeRequest{
		RepositoryPath: repoPath,
		SourceRef:      sourceHash.String(),
		TargetRef:      targetHash.String(),
		Message:        req.Message,
		NoCommit:       false,
	}

	if req.Author != nil {
		nativeReq.Author = &GitSignature{
			Name:  req.Author.Name,
			Email: req.Author.Email,
			When:  req.Author.Date,
		}
	}

	nativeResult, err := ma.nativeExecutor.SquashMerge(ctx, nativeReq)
	if err != nil {
		return nil, err
	}

	return ma.convertNativeToLegacyResult(nativeResult), nil
}

// performNativeRebaseMerge executes rebase using native Git
func (ma *MigrationAdapter) performNativeRebaseMerge(ctx context.Context, repoPath string, sourceHash, targetHash plumbing.Hash, req *MergeRequest) (*MergeResult, error) {
	nativeReq := &NativeRebaseRequest{
		RepositoryPath: repoPath,
		SourceRef:      sourceHash.String(),
		TargetRef:      targetHash.String(),
		Strategy:       string(req.Strategy),
		Interactive:    false,
		PreserveMerges: false,
	}

	nativeResult, err := ma.nativeExecutor.RebaseMerge(ctx, nativeReq)
	if err != nil {
		return nil, err
	}

	return ma.convertNativeToLegacyResult(nativeResult), nil
}

// convertNativeToLegacyResult converts native Git result to legacy format
func (ma *MigrationAdapter) convertNativeToLegacyResult(nativeResult *NativeMergeResult) *MergeResult {
	result := &MergeResult{
		IsFastForward: nativeResult.IsFastForward,
		HasConflicts:  nativeResult.HasConflicts,
	}

	// Convert commit hash to commit object if available
	if nativeResult.MergeCommitHash != "" {
		// In a full implementation, we would need to load the commit object
		// For now, we'll create a placeholder
		result.MergeCommit = &object.Commit{
			Hash: plumbing.NewHash(nativeResult.MergeCommitHash),
			// Other fields would be populated from Git
		}
	}

	return result
}

// shouldUseNative determines whether to use native Git for a given operation
func (ma *MigrationAdapter) shouldUseNative(req *MergeRequest, repo *gogit.Repository) bool {
	// Check global flag
	if !ma.migrationFlags.EnableNativeGit {
		return false
	}

	// Check operation-specific flags
	switch req.Method {
	case MergeMethodMerge:
		if !ma.migrationFlags.EnableNativeThreeWay {
			return false
		}
	case MergeMethodSquash:
		if !ma.migrationFlags.EnableNativeSquash {
			return false
		}
	case MergeMethodRebase:
		if !ma.migrationFlags.EnableNativeRebase {
			return false
		}
	}

	// Check repository-specific settings
	repoName := ma.getRepositoryName(repo)
	if ma.isRepositoryDisabled(repoName) {
		return false
	}

	if !ma.isRepositoryEnabled(repoName) && ma.migrationFlags.RolloutPercentage < 100 {
		// Check percentage-based rollout
		hash := ma.calculateHash(repoName + req.SourceRef + req.TargetRef)
		return (hash % 100) < ma.migrationFlags.RolloutPercentage
	}

	return true
}

// Validation methods
func (ma *MigrationAdapter) validateSync(ctx context.Context, repo *gogit.Repository, sourceHash, targetHash plumbing.Hash, req *MergeRequest, analysis *MergeAnalysisResult, nativeResult *MergeResult, nativeErr error) {
	validationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	result := ma.validator.ValidateResults(validationCtx, repo, sourceHash, targetHash, req, analysis, nativeResult, nativeErr, ma.legacyHandler)
	ma.metrics.RecordValidation(result)

	if !result.Passed {
		ma.logger.Warn("Native Git validation failed",
			zap.Strings("differences", result.Differences),
			zap.Errors("validation_errors", result.ValidationErrors))
	}
}

func (ma *MigrationAdapter) validateInBackground(ctx context.Context, repo *gogit.Repository, sourceHash, targetHash plumbing.Hash, req *MergeRequest, analysis *MergeAnalysisResult, nativeResult *MergeResult, nativeErr error) {
	go func() {
		validationCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		result := ma.validator.ValidateResults(validationCtx, repo, sourceHash, targetHash, req, analysis, nativeResult, nativeErr, ma.legacyHandler)
		ma.metrics.RecordValidation(result)

		if !result.Passed {
			ma.logger.Warn("Background validation failed",
				zap.Strings("differences", result.Differences),
				zap.Errors("validation_errors", result.ValidationErrors))
		}
	}()
}

// ValidateResults compares native and legacy operation results
func (mv *MigrationValidator) ValidateResults(ctx context.Context, repo *gogit.Repository, sourceHash, targetHash plumbing.Hash, req *MergeRequest, analysis *MergeAnalysisResult, nativeResult *MergeResult, nativeErr error, legacyHandler *MergeOperationsHandler) *MigrationValidationResult {
	result := &MigrationValidationResult{
		Passed:       true,
		NativeResult: nativeResult,
	}

	// Execute legacy operation for comparison
	legacyStartTime := time.Now()
	legacyResult, legacyErr := legacyHandler.performMerge(ctx, repo, sourceHash, targetHash, req, analysis)
	result.LegacyDuration = time.Since(legacyStartTime)
	result.LegacyResult = legacyResult

	// Compare error states
	if (nativeErr == nil) != (legacyErr == nil) {
		result.Passed = false
		result.Differences = append(result.Differences, fmt.Sprintf("Error state mismatch: native=%v, legacy=%v", nativeErr == nil, legacyErr == nil))
	}

	// If both succeeded, compare results
	if nativeErr == nil && legacyErr == nil {
		if !mv.compareResults(nativeResult, legacyResult) {
			result.Passed = false
			result.Differences = append(result.Differences, "Result content mismatch")
		}
	}

	return result
}

func (mv *MigrationValidator) compareResults(native *MergeResult, legacy *MergeResult) bool {
	if native == nil || legacy == nil {
		return native == legacy
	}

	// Compare key fields
	if native.IsFastForward != legacy.IsFastForward {
		return false
	}

	if native.HasConflicts != legacy.HasConflicts {
		return false
	}

	// More detailed comparison would be implemented here
	return true
}

// Helper methods
func (ma *MigrationAdapter) getRepositoryName(repo *gogit.Repository) string {
	// Extract repository name from repository
	// This would need proper implementation based on repository structure
	return "default"
}

func (ma *MigrationAdapter) isRepositoryDisabled(repoName string) bool {
	for _, disabled := range ma.migrationFlags.DisabledRepositories {
		if disabled == repoName {
			return true
		}
	}
	return false
}

func (ma *MigrationAdapter) isRepositoryEnabled(repoName string) bool {
	if len(ma.migrationFlags.EnabledRepositories) == 0 {
		return false // No explicit enablement
	}

	for _, enabled := range ma.migrationFlags.EnabledRepositories {
		if enabled == repoName {
			return true
		}
	}
	return false
}

func (ma *MigrationAdapter) calculateHash(input string) int {
	// Simple hash function for rollout percentage
	hash := 0
	for _, b := range input {
		hash = hash*31 + int(b)
	}
	if hash < 0 {
		hash = -hash
	}
	return hash
}

func loadMigrationFlags(cfg *config.Config) *MigrationFlags {
	// Load migration flags from configuration
	// This would be implemented based on your config structure
	return &MigrationFlags{
		EnableNativeGit:      false, // Default to disabled
		EnableNativeThreeWay: false,
		EnableNativeSquash:   false,
		EnableNativeRebase:   false,
		RolloutPercentage:    0,
		ValidateResults:      true,
		FallbackOnError:      true,
		ParallelValidation:   true,
	}
}

// Metrics recording methods
func (mm *MigrationMetrics) RecordNativeOperation(duration time.Duration, success bool) {
	mm.NativeOperations++
	if success {
		mm.NativeAverageTime = (mm.NativeAverageTime*time.Duration(mm.NativeOperations-1) + duration) / time.Duration(mm.NativeOperations)
	}
}

func (mm *MigrationMetrics) RecordLegacyOperation(duration time.Duration, success bool) {
	mm.LegacyOperations++
	if success {
		mm.LegacyAverageTime = (mm.LegacyAverageTime*time.Duration(mm.LegacyOperations-1) + duration) / time.Duration(mm.LegacyOperations)
	}
}

func (mm *MigrationMetrics) RecordValidation(result *MigrationValidationResult) {
	mm.Validations++
	if result.Passed {
		mm.ValidationPassRate = float64(mm.Validations-1)/float64(mm.Validations)*mm.ValidationPassRate + 1.0/float64(mm.Validations)
	} else {
		mm.ValidationPassRate = float64(mm.Validations-1) / float64(mm.Validations) * mm.ValidationPassRate
	}
}

func (mm *MigrationMetrics) RecordFallback(duration time.Duration) {
	mm.logger.Info("Fallback operation recorded", zap.Duration("duration", duration))
}
