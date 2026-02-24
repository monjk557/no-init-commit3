package merge

import (
	"context"
	"fmt"
	"sort"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
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

	// DEBUG: List what's actually in the protected tree
	createdTreeObj, debugErr := repo.TreeObject(protectedTree)
	if debugErr == nil {
		var actualFiles []string
		for _, entry := range createdTreeObj.Entries {
			actualFiles = append(actualFiles, entry.Name)
		}
		t.logger.Info("DEBUG: Protected tree contents",
			zap.String("protected_tree", protectedTree.String()),
			zap.Strings("actual_files_in_tree", actualFiles),
			zap.Strings("intended_protected_files", protectedLostFiles))
	}

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

// createProtectedTree creates a new tree that preserves protected files with proper directory hierarchy
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

	// Create a map to track all tree entries and subdirectories
	treeBuilder := NewTreeBuilder(repo)

	// Add all entries from fallback tree first
	err = treeBuilder.AddTreeEntries(fallbackTreeObj.Entries)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to add fallback entries: %w", err)
	}

	// Add/replace protected files from target tree with proper directory structure
	for _, protectedPath := range protectedFiles {
		protectedFile, err := targetTree.File(protectedPath)
		if err != nil {
			t.logger.Warn("Could not retrieve protected file from target",
				zap.String("file", protectedPath),
				zap.Error(err))
			continue
		}

		// Add the protected file with proper directory hierarchy
		err = treeBuilder.AddFileWithPath(protectedPath, protectedFile.Hash, protectedFile.Mode)
		if err != nil {
			t.logger.Error("Failed to add protected file to tree builder",
				zap.String("file", protectedPath),
				zap.Error(err))
			continue
		}

		t.logger.Debug("Preserved protected file in merge tree with directory structure",
			zap.String("file", protectedPath),
			zap.String("hash", protectedFile.Hash.String()))
	}

	// Build the final tree with proper Git directory structure
	treeHash, err := treeBuilder.Build()
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to build protected tree: %w", err)
	}

	return treeHash, nil
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
		"docker-compose.yml",
		"config/application.yml",
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

// TreeBuilder helps construct Git trees with proper directory hierarchy
type TreeBuilder struct {
	repo        *gogit.Repository
	rootEntries map[string]TreeBuilderEntry
}

// TreeBuilderEntry represents a file or directory in the tree being built
type TreeBuilderEntry struct {
	Name        string
	Mode        filemode.FileMode
	Hash        plumbing.Hash
	IsDirectory bool
	Children    map[string]TreeBuilderEntry // Only used for directories
}

// NewTreeBuilder creates a new tree builder
func NewTreeBuilder(repo *gogit.Repository) *TreeBuilder {
	return &TreeBuilder{
		repo:        repo,
		rootEntries: make(map[string]TreeBuilderEntry),
	}
}

// AddTreeEntries adds existing tree entries to the builder
func (tb *TreeBuilder) AddTreeEntries(entries []object.TreeEntry) error {
	for _, entry := range entries {
		tb.rootEntries[entry.Name] = TreeBuilderEntry{
			Name:        entry.Name,
			Mode:        entry.Mode,
			Hash:        entry.Hash,
			IsDirectory: entry.Mode == filemode.Dir,
			Children:    make(map[string]TreeBuilderEntry),
		}
	}
	return nil
}

// AddFileWithPath adds a file with proper directory structure
func (tb *TreeBuilder) AddFileWithPath(filePath string, fileHash plumbing.Hash, fileMode filemode.FileMode) error {
	pathParts := strings.Split(filePath, "/")

	// If it's a root-level file, add it directly
	if len(pathParts) == 1 {
		tb.rootEntries[pathParts[0]] = TreeBuilderEntry{
			Name:        pathParts[0],
			Mode:        fileMode,
			Hash:        fileHash,
			IsDirectory: false,
		}
		return nil
	}

	// For nested files, we need to create/update directory structure
	return tb.addFileRecursively(tb.rootEntries, pathParts, fileHash, fileMode, 0)
}

// addFileRecursively recursively creates directory structure for nested files
func (tb *TreeBuilder) addFileRecursively(currentLevel map[string]TreeBuilderEntry, pathParts []string, fileHash plumbing.Hash, fileMode filemode.FileMode, depth int) error {
	if depth >= len(pathParts) {
		return nil
	}

	currentPart := pathParts[depth]

	// If this is the last part, it's the file
	if depth == len(pathParts)-1 {
		currentLevel[currentPart] = TreeBuilderEntry{
			Name:        currentPart,
			Mode:        fileMode,
			Hash:        fileHash,
			IsDirectory: false,
		}
		return nil
	}

	// This is a directory part
	var dirEntry TreeBuilderEntry
	if existing, exists := currentLevel[currentPart]; exists && existing.IsDirectory {
		// Directory already exists, use it
		dirEntry = existing
	} else {
		// Create new directory entry
		dirEntry = TreeBuilderEntry{
			Name:        currentPart,
			Mode:        filemode.Dir,
			Hash:        plumbing.ZeroHash, // Will be calculated when we build the tree
			IsDirectory: true,
			Children:    make(map[string]TreeBuilderEntry),
		}
		currentLevel[currentPart] = dirEntry
	}

	// Recurse into the directory
	return tb.addFileRecursively(dirEntry.Children, pathParts, fileHash, fileMode, depth+1)
}

// Build creates the final Git tree with proper directory structure
func (tb *TreeBuilder) Build() (plumbing.Hash, error) {
	return tb.buildTreeRecursively(tb.rootEntries)
}

// buildTreeRecursively recursively builds Git tree objects
func (tb *TreeBuilder) buildTreeRecursively(entries map[string]TreeBuilderEntry) (plumbing.Hash, error) {
	var treeEntries []object.TreeEntry

	// Process all entries at this level
	for _, entry := range entries {
		if entry.IsDirectory {
			// For directories, recursively build the subtree first
			subtreeHash, err := tb.buildTreeRecursively(entry.Children)
			if err != nil {
				return plumbing.ZeroHash, fmt.Errorf("failed to build subtree for %s: %w", entry.Name, err)
			}

			// Add directory entry pointing to the subtree
			treeEntries = append(treeEntries, object.TreeEntry{
				Name: entry.Name,
				Mode: filemode.Dir,
				Hash: subtreeHash,
			})
		} else {
			// For files, add them directly
			treeEntries = append(treeEntries, object.TreeEntry{
				Name: entry.Name,
				Mode: entry.Mode,
				Hash: entry.Hash,
			})
		}
	}

	// Sort entries (required by Git)
	sort.Slice(treeEntries, func(i, j int) bool {
		return treeEntries[i].Name < treeEntries[j].Name
	})

	// Create and store the tree object
	tree := &object.Tree{Entries: treeEntries}
	encodedTree := tb.repo.Storer.NewEncodedObject()
	if err := tree.Encode(encodedTree); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to encode tree: %w", err)
	}

	treeHash, err := tb.repo.Storer.SetEncodedObject(encodedTree)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to store tree: %w", err)
	}

	return treeHash, nil
}
