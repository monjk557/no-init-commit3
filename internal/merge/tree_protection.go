package merge

import (
	"context"
	"fmt"
	"sort"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"go.uber.org/zap"
)

// EnvironmentFileDetector provides advanced environment file detection
type EnvironmentFileDetector struct {
	pathMatchers []PathMatcher
	patterns     []string
}

// TreeProtectionLayer provides centralized protection for all merge tree operations
// This is the core component that prevents environment file deletion across ALL merge strategies
type TreeProtectionLayer struct {
	logger              *zap.Logger
	environmentDetector *EnvironmentFileDetector
	protectionRules     []ProtectionRule
	fallbackPolicy      *AdaptiveFallbackPolicy
	observability       *TreeProtectionObservability
}

// ProtectionRule defines a rule for protecting specific files during merges
type ProtectionRule struct {
	Name        string
	PathMatcher PathMatcher
	Strategy    ProtectionStrategy
	Priority    int
	Enabled     bool
	Description string
}

// ProtectionStrategy defines how to protect files when conflicts occur
type ProtectionStrategy int

const (
	ProtectionStrategyPreserveTarget   ProtectionStrategy = iota // Keep target version
	ProtectionStrategyPreserveSource                             // Keep source version
	ProtectionStrategyMergeIntelligent                           // Intelligent merge
	ProtectionStrategyPreventDeletion                            // Never allow deletion
	ProtectionStrategyRequireManual                              // Force manual resolution
)

// PathMatcher interface for matching file paths
type PathMatcher interface {
	Matches(path string) bool
	Description() string
}

// FallbackContext contains all context needed for intelligent fallback decisions
type FallbackContext struct {
	SourceCommit     *object.Commit
	TargetCommit     *object.Commit
	BaseCommit       *object.Commit
	Strategy         MergeStrategy
	IsSquash         bool
	ConflictCount    int
	AutoResolvable   int
	EnvironmentFiles []string
}

// NewEnvironmentFileDetector creates a new environment file detector
func NewEnvironmentFileDetector() *EnvironmentFileDetector {
	return &EnvironmentFileDetector{
		pathMatchers: []PathMatcher{
			&EnvironmentPathMatcher{},
		},
		patterns: []string{
			"config/env/",
			".env",
			"environment",
			"config/database.yml",
			"config/secrets",
			"docker-compose.yml",
			"config/application.yml",
		},
	}
}

// IsEnvironmentFile checks if a given path represents an environment file
func (d *EnvironmentFileDetector) IsEnvironmentFile(path string) bool {
	for _, matcher := range d.pathMatchers {
		if matcher.Matches(path) {
			return true
		}
	}
	return false
}

// NewTreeProtectionLayer creates a centralized tree protection system
func NewTreeProtectionLayer(logger *zap.Logger) *TreeProtectionLayer {
	return &TreeProtectionLayer{
		logger:              logger,
		environmentDetector: NewEnvironmentFileDetector(),
		protectionRules:     createDefaultProtectionRules(),
		fallbackPolicy:      NewAdaptiveFallbackPolicy(logger),
		observability:       NewTreeProtectionObservability(logger),
	}
}

// ProtectFallback applies protection rules during merge fallback scenarios
// This is the immediate fix for the environment deletion issue
func (t *TreeProtectionLayer) ProtectFallback(
	ctx context.Context,
	repo *gogit.Repository,
	fallbackTree plumbing.Hash,
	context FallbackContext,
) (plumbing.Hash, error) {
	t.logger.Info("Applying tree protection during fallback",
		zap.String("fallback_strategy", string(context.Strategy)),
		zap.Bool("is_squash", context.IsSquash),
		zap.Int("conflict_count", context.ConflictCount))

	// Analyze what would be lost with the fallback
	lostFiles, err := t.analyzeLostFiles(ctx, repo, fallbackTree, context)
	if err != nil {
		return fallbackTree, fmt.Errorf("failed to analyze lost files: %w", err)
	}

	// Check if any protected files would be lost
	protectedLostFiles := t.identifyProtectedFiles(lostFiles)
	if len(protectedLostFiles) == 0 {
		// No protected files at risk, use fallback as-is
		t.observability.RecordFallback("safe", context)
		return fallbackTree, nil
	}

	t.logger.Warn("Protected files at risk during fallback",
		zap.Strings("protected_files", protectedLostFiles))

	// Apply protection by creating a new tree that preserves protected files
	protectedTree, err := t.createProtectedTree(ctx, repo, fallbackTree, protectedLostFiles, context)
	if err != nil {
		t.logger.Error("Failed to create protected tree, using fallback",
			zap.Error(err),
			zap.Strings("lost_protected_files", protectedLostFiles))
		t.observability.RecordFallback("failed_protection", context)
		return fallbackTree, nil
	}

	t.observability.RecordFallback("protected", context)
	t.logger.Info("Successfully created protected tree",
		zap.String("protected_tree", protectedTree.String()),
		zap.Strings("preserved_files", protectedLostFiles))

	return protectedTree, nil
}

// analyzeLostFiles identifies files that would be lost in the fallback
func (t *TreeProtectionLayer) analyzeLostFiles(
	ctx context.Context,
	repo *gogit.Repository,
	fallbackTree plumbing.Hash,
	context FallbackContext,
) ([]string, error) {
	// Get target tree (what we want to preserve from)
	targetTree, err := repo.TreeObject(context.TargetCommit.TreeHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get target tree: %w", err)
	}

	// Get fallback tree (what we're falling back to)
	fallbackTreeObj, err := repo.TreeObject(fallbackTree)
	if err != nil {
		return nil, fmt.Errorf("failed to get fallback tree: %w", err)
	}

	var lostFiles []string

	// Walk target tree and check if files exist in fallback
	err = targetTree.Files().ForEach(func(file *object.File) error {
		_, err := fallbackTreeObj.File(file.Name)
		if err != nil {
			// File exists in target but not in fallback = lost file
			lostFiles = append(lostFiles, file.Name)
		}
		return nil
	})

	return lostFiles, err
}

// identifyProtectedFiles filters lost files to only those that need protection
func (t *TreeProtectionLayer) identifyProtectedFiles(lostFiles []string) []string {
	var protected []string

	for _, file := range lostFiles {
		if t.isProtectedFile(file) {
			protected = append(protected, file)
		}
	}

	return protected
}

// isProtectedFile checks if a file matches any protection rules
func (t *TreeProtectionLayer) isProtectedFile(path string) bool {
	for _, rule := range t.protectionRules {
		if rule.Enabled && rule.PathMatcher.Matches(path) {
			return true
		}
	}
	return false
}

// createProtectedTree creates a new tree that preserves protected files
func (t *TreeProtectionLayer) createProtectedTree(
	ctx context.Context,
	repo *gogit.Repository,
	fallbackTree plumbing.Hash,
	protectedFiles []string,
	context FallbackContext,
) (plumbing.Hash, error) {
	// Get the fallback tree object
	fallbackTreeObj, err := repo.TreeObject(fallbackTree)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to get fallback tree: %w", err)
	}

	// Get target tree to extract protected files from
	targetTree, err := repo.TreeObject(context.TargetCommit.TreeHash)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to get target tree: %w", err)
	}

	// Create a new tree builder
	var entries []object.TreeEntry

	// Add all entries from fallback tree
	for _, entry := range fallbackTreeObj.Entries {
		entries = append(entries, entry)
	}

	// Add/replace protected files from target tree
	for _, protectedPath := range protectedFiles {
		protectedFile, err := targetTree.File(protectedPath)
		if err != nil {
			t.logger.Warn("Could not retrieve protected file from target",
				zap.String("file", protectedPath),
				zap.Error(err))
			continue
		}

		// Remove existing entry if it exists
		entries = removeEntry(entries, protectedPath)

		// Add protected file entry
		entry := object.TreeEntry{
			Name: protectedPath,
			Mode: protectedFile.Mode,
			Hash: protectedFile.Hash,
		}
		entries = append(entries, entry)

		t.logger.Debug("Preserved protected file in merge tree",
			zap.String("file", protectedPath),
			zap.String("hash", protectedFile.Hash.String()))
	}

	// Sort tree entries (required by Git)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	// Create new tree object
	tree := &object.Tree{Entries: entries}
	encodedTree := repo.Storer.NewEncodedObject()
	if err := tree.Encode(encodedTree); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to encode protected tree: %w", err)
	}

	treeHash, err := repo.Storer.SetEncodedObject(encodedTree)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to store protected tree: %w", err)
	}

	return treeHash, nil
}

// removeEntry removes an entry with the given name from the entries slice
func removeEntry(entries []object.TreeEntry, name string) []object.TreeEntry {
	var result []object.TreeEntry
	for _, entry := range entries {
		if entry.Name != name {
			result = append(result, entry)
		}
	}
	return result
}

// createDefaultProtectionRules defines the default rules for file protection
func createDefaultProtectionRules() []ProtectionRule {
	return []ProtectionRule{
		{
			Name:        "environment_files",
			PathMatcher: &EnvironmentPathMatcher{},
			Strategy:    ProtectionStrategyPreventDeletion,
			Priority:    100,
			Enabled:     true,
			Description: "Prevent deletion of environment configuration files",
		},
		{
			Name:        "config_files",
			PathMatcher: &ConfigPathMatcher{},
			Strategy:    ProtectionStrategyPreserveTarget,
			Priority:    90,
			Enabled:     true,
			Description: "Preserve configuration files from target",
		},
		{
			Name:        "security_files",
			PathMatcher: &SecurityPathMatcher{},
			Strategy:    ProtectionStrategyRequireManual,
			Priority:    95,
			Enabled:     true,
			Description: "Security files require manual resolution",
		},
	}
}

// EnvironmentPathMatcher matches environment files
type EnvironmentPathMatcher struct{}

func (m *EnvironmentPathMatcher) Matches(path string) bool {
	envPatterns := []string{
		"config/env/",
		".env",
		"environment",
		"config/database.yml",
		"config/secrets",
	}

	pathLower := strings.ToLower(path)
	for _, pattern := range envPatterns {
		if strings.Contains(pathLower, pattern) {
			return true
		}
	}
	return false
}

func (m *EnvironmentPathMatcher) Description() string {
	return "Environment configuration files and directories"
}

// ConfigPathMatcher matches general configuration files
type ConfigPathMatcher struct{}

func (m *ConfigPathMatcher) Matches(path string) bool {
	configPatterns := []string{
		"config/",
		".config",
		"configuration",
		"settings",
	}

	pathLower := strings.ToLower(path)
	for _, pattern := range configPatterns {
		if strings.Contains(pathLower, pattern) {
			return true
		}
	}
	return false
}

func (m *ConfigPathMatcher) Description() string {
	return "General configuration files and directories"
}

// SecurityPathMatcher matches security-related files
type SecurityPathMatcher struct{}

func (m *SecurityPathMatcher) Matches(path string) bool {
	securityPatterns := []string{
		"security/",
		"certs/",
		"keys/",
		"secrets/",
		"auth/",
		".key",
		".pem",
		".crt",
	}

	pathLower := strings.ToLower(path)
	for _, pattern := range securityPatterns {
		if strings.Contains(pathLower, pattern) {
			return true
		}
	}
	return false
}

func (m *SecurityPathMatcher) Description() string {
	return "Security certificates, keys, and authentication files"
}
