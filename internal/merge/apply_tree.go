package merge

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// createMergedTreeWithBase creates a new tree by performing 3-way merge with an explicit base commit
// This is used for rebase operations where we know the base commit (the parent of the commit being rebased)
func (h *MergeOperationsHandler) createMergedTreeWithBase(
	ctx context.Context,
	repo *gogit.Repository,
	sourceCommit, targetCommit, baseCommit *object.Commit,
	analysis *MergeAnalysisResult,
	strategy MergeStrategy,
	protectionCtx *MergeProtectionContext,
) (plumbing.Hash, error) {
	if baseCommit == nil {
		return plumbing.ZeroHash, fmt.Errorf("base commit cannot be nil")
	}

	// Get trees for base, source, and target
	baseTree, err := baseCommit.Tree()
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to get base tree: %w", err)
	}

	sourceTree, err := sourceCommit.Tree()
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to get source tree: %w", err)
	}

	targetTree, err := targetCommit.Tree()
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to get target tree: %w", err)
	}

	h.logger.Debug("Starting 3-way merge tree creation",
		zap.String("base", baseCommit.Hash.String()),
		zap.String("source", sourceCommit.Hash.String()),
		zap.String("target", targetCommit.Hash.String()))

	// Build conflict map for quick lookup
	conflictMap := make(map[string]*FileConflict)
	if analysis != nil {
		for _, conflict := range analysis.FileConflicts {
			conflictMap[conflict.FilePath] = &conflict
		}
	}

	// Merge trees recursively starting from root
	treeEntries, err := h.mergeTreeEntries(ctx, repo, baseTree, sourceTree, targetTree, "", conflictMap, strategy, protectionCtx)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to merge tree entries: %w", err)
	}

	// Sort and store the merged tree
	sortTreeEntries(treeEntries)
	treeHash, err := h.createAndStoreTree(repo, treeEntries)
	if err != nil {
		return plumbing.ZeroHash, err
	}

	h.logger.Info("Successfully created merged tree",
		zap.String("tree_hash", treeHash.String()),
		zap.Int("entries", len(treeEntries)))

	return treeHash, nil
}

// createMergedTree creates a new tree by performing 3-way merge
func (h *MergeOperationsHandler) createMergedTree(
	ctx context.Context,
	repo *gogit.Repository,
	sourceCommit, targetCommit *object.Commit,
	analysis *MergeAnalysisResult,
	strategy MergeStrategy,
	protectionCtx *MergeProtectionContext,
) (plumbing.Hash, error) {
	// Find merge base (common ancestor)
	mergeBases, err := sourceCommit.MergeBase(targetCommit)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to find merge base: %w", err)
	}
	if len(mergeBases) == 0 {
		return plumbing.ZeroHash, fmt.Errorf("no common ancestor found")
	}

	baseCommit := mergeBases[0]
	return h.createMergedTreeWithBase(ctx, repo, sourceCommit, targetCommit, baseCommit, analysis, strategy, protectionCtx)
}

// resolveFileForMerge determines which version of a file to use in the merged tree
func (h *MergeOperationsHandler) resolveFileForMerge(
	entry *mergeFileEntry,
	conflict *FileConflict,
	strategy MergeStrategy,
	protectionCtx *MergeProtectionContext, // NEW: Protection context for feature flag control
) (plumbing.Hash, filemode.FileMode, error) {
	baseExists := !entry.baseHash.IsZero()
	sourceExists := !entry.sourceHash.IsZero()
	targetExists := !entry.targetHash.IsZero()

	// Case 1: File doesn't exist in any tree (shouldn't happen)
	if !baseExists && !sourceExists && !targetExists {
		return plumbing.ZeroHash, 0, nil
	}

	// Case 2: File only exists in source (new file in source)
	if !baseExists && !targetExists && sourceExists {
		h.logger.Debug("New file in source", zap.String("path", entry.path))
		return entry.sourceHash, entry.sourceMode, nil
	}

	// Case 3: File only exists in target (new file in target)
	if !baseExists && !sourceExists && targetExists {
		h.logger.Debug("New file in target", zap.String("path", entry.path))
		return entry.targetHash, entry.targetMode, nil
	}

	// Case 4: File deleted in source but exists in target
	// Note: This case applies when the file was explicitly deleted in source.
	// If the file is unchanged between base and target, we should preserve it
	// (the directory might not exist in source, which is different from explicit deletion).
	// For recursive/ORT strategies, prefer preserving unchanged files from base/target.
	if baseExists && !sourceExists && targetExists {
		if entry.baseHash == entry.targetHash {
			// Target unchanged from base. For recursive/ORT strategies, preserve the file
			// because the directory might not exist in source (not an explicit deletion).
			// For "theirs" strategy, respect the deletion.
			if strategy == MergeStrategyTheirs {
				h.logger.Debug("File deleted in source, unchanged in target - respecting deletion (theirs strategy)", zap.String("path", entry.path))
				return plumbing.ZeroHash, 0, nil
			}

			// **CRITICAL FIX**: Check feature flag before preserving files
			if protectionCtx != nil && !protectionCtx.ShouldPreserveFile(entry.path) {
				h.logger.Info("Feature flag control - allowing environment file deletion",
					zap.String("path", entry.path),
					zap.String("protection_mode", protectionCtx.GetProtectionStatus()),
					zap.Bool("destructive_mode", protectionCtx.DestructiveMode))
				return plumbing.ZeroHash, 0, nil // DELETE THE FILE
			}

			// For recursive/ORT/ours strategies, preserve the file (when protection enabled)
			h.logger.Debug("File missing in source but unchanged in target - preserving target (protection enabled or non-environment file)",
				zap.String("path", entry.path),
				zap.String("protection_status", func() string {
					if protectionCtx != nil {
						return protectionCtx.GetProtectionStatus()
					}
					return "no_protection_context"
				}()))
			return entry.targetHash, entry.targetMode, nil
		}
		// Conflict: modified in target, deleted in source
		h.logger.Warn("Conflict: modified in target, deleted in source", zap.String("path", entry.path))
		if strategy == MergeStrategyTheirs {
			return entry.sourceHash, entry.sourceMode, nil // Keep deletion
		}
		return entry.targetHash, entry.targetMode, nil // Keep target (ours)
	}

	// Case 5: File deleted in target but exists in source
	if baseExists && sourceExists && !targetExists {
		if entry.baseHash == entry.sourceHash {
			// Source unchanged, respect target deletion
			h.logger.Debug("File deleted in target, unchanged in source", zap.String("path", entry.path))
			return plumbing.ZeroHash, 0, nil
		}
		// Conflict: modified in source, deleted in target
		h.logger.Warn("Conflict: modified in source, deleted in target", zap.String("path", entry.path))
		if strategy == MergeStrategyTheirs {
			return entry.sourceHash, entry.sourceMode, nil // Keep source
		}
		return plumbing.ZeroHash, 0, nil // Keep deletion (ours)
	}

	// Case 6: File exists in all three (most common case)
	if baseExists && sourceExists && targetExists {
		// Both unchanged from base
		if entry.baseHash == entry.sourceHash && entry.baseHash == entry.targetHash {
			h.logger.Debug("File unchanged in both branches", zap.String("path", entry.path))
			return entry.targetHash, entry.targetMode, nil
		}

		// Only source changed
		if entry.baseHash != entry.sourceHash && entry.baseHash == entry.targetHash {
			h.logger.Debug("File only changed in source", zap.String("path", entry.path))
			return entry.sourceHash, entry.sourceMode, nil
		}

		// Only target changed
		if entry.baseHash == entry.sourceHash && entry.baseHash != entry.targetHash {
			h.logger.Debug("File only changed in target", zap.String("path", entry.path))
			return entry.targetHash, entry.targetMode, nil
		}

		// Both changed to same content (identical changes)
		if entry.sourceHash == entry.targetHash {
			h.logger.Debug("File changed identically in both branches", zap.String("path", entry.path))
			return entry.sourceHash, entry.sourceMode, nil
		}

		// Both changed differently - conflict!
		h.logger.Warn("Conflict: file changed differently in both branches", zap.String("path", entry.path))

		// Check if conflict is auto-resolvable
		if conflict != nil && conflict.AutoResolvable {
			h.logger.Debug("Auto-resolvable conflict, using source", zap.String("path", entry.path))
			return entry.sourceHash, entry.sourceMode, nil
		}

		// Use strategy to resolve
		switch strategy {
		case MergeStrategyTheirs:
			return entry.sourceHash, entry.sourceMode, nil
		case MergeStrategyOurs:
			return entry.targetHash, entry.targetMode, nil
		case MergeStrategyRecursive, MergeStrategyResolve, MergeStrategyORT:
			// SAFETY FIX: Environment-aware conflict resolution
			if h.isEnvironmentFile(entry.path) {
				return h.resolveEnvironmentConflict(entry)
			}
			// For non-environment files, use intelligent resolution
			return h.intelligentConflictResolution(entry)
		default:
			// Default to source for other strategies
			return entry.sourceHash, entry.sourceMode, nil
		}
	}

	// Case 7: File exists in base and source but not target
	if baseExists && sourceExists && !targetExists {
		h.logger.Debug("File exists in base and source, deleted in target", zap.String("path", entry.path))
		return plumbing.ZeroHash, 0, nil // Respect deletion
	}

	// Case 8: File exists in base and target but not source
	if baseExists && !sourceExists && targetExists {
		// If target unchanged from base, keep target (don't respect source deletion)
		// This is because if the entire directory doesn't exist in source,
		// we should preserve files that exist in both base and target
		if entry.baseHash == entry.targetHash {
			h.logger.Debug("File exists in base and target but not source, keeping target", zap.String("path", entry.path))
			return entry.targetHash, entry.targetMode, nil // Keep target
		}
		// Conflict: modified in target, deleted in source
		h.logger.Warn("Conflict: modified in target, deleted in source", zap.String("path", entry.path))
		if strategy == MergeStrategyTheirs {
			return plumbing.ZeroHash, 0, nil // Keep deletion
		}
		return entry.targetHash, entry.targetMode, nil // Keep target (ours)
	}

	// Default: keep target version
	if targetExists {
		return entry.targetHash, entry.targetMode, nil
	}
	if sourceExists {
		return entry.sourceHash, entry.sourceMode, nil
	}

	return plumbing.ZeroHash, 0, nil
}

// collectTreeEntries collects all entries from three trees into a map
func (h *MergeOperationsHandler) collectTreeEntries(baseTree, sourceTree, targetTree *object.Tree) map[string]*mergeTreeEntry {
	entryMap := make(map[string]*mergeTreeEntry)

	// Helper to add entry to map
	addEntry := func(name string, entry *object.TreeEntry, setter func(*mergeTreeEntry)) {
		if mergeEntry, exists := entryMap[name]; exists {
			setter(mergeEntry)
		} else {
			mergeEntry = &mergeTreeEntry{name: name}
			setter(mergeEntry)
			entryMap[name] = mergeEntry
		}
	}

	// Collect from base tree
	for i := range baseTree.Entries {
		entry := &baseTree.Entries[i]
		addEntry(entry.Name, entry, func(me *mergeTreeEntry) { me.baseEntry = entry })
	}

	// Collect from source tree
	for i := range sourceTree.Entries {
		entry := &sourceTree.Entries[i]
		addEntry(entry.Name, entry, func(me *mergeTreeEntry) { me.sourceEntry = entry })
	}

	// Collect from target tree
	for i := range targetTree.Entries {
		entry := &targetTree.Entries[i]
		addEntry(entry.Name, entry, func(me *mergeTreeEntry) { me.targetEntry = entry })
	}

	return entryMap
}

// sortTreeEntries sorts tree entries by name (required by Git)
func sortTreeEntries(entries []object.TreeEntry) {
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[i].Name > entries[j].Name {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}
}

// createAndStoreTree creates and stores a tree object in the repository
func (h *MergeOperationsHandler) createAndStoreTree(repo *gogit.Repository, entries []object.TreeEntry) (plumbing.Hash, error) {
	newTree := &object.Tree{Entries: entries}
	treeObj := repo.Storer.NewEncodedObject()
	if err := newTree.Encode(treeObj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to encode tree: %w", err)
	}
	treeHash, err := repo.Storer.SetEncodedObject(treeObj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to store tree: %w", err)
	}
	return treeHash, nil
}

// getOrCreateEmptyTree gets an existing tree or returns nil if entry is nil
func (h *MergeOperationsHandler) getOrCreateEmptyTree(repo *gogit.Repository, entry *object.TreeEntry) (*object.Tree, error) {
	if entry == nil {
		return nil, nil
	}
	if entry.Mode != filemode.Dir {
		return nil, fmt.Errorf("entry is not a directory")
	}
	return repo.TreeObject(entry.Hash)
}

// createEmptyTree creates and stores an empty tree in the repository
func (h *MergeOperationsHandler) createEmptyTree(repo *gogit.Repository) (*object.Tree, error) {
	emptyTree := &object.Tree{Entries: []object.TreeEntry{}}
	treeObj := repo.Storer.NewEncodedObject()
	if err := emptyTree.Encode(treeObj); err != nil {
		return nil, fmt.Errorf("failed to encode empty tree: %w", err)
	}
	emptyTreeHash, err := repo.Storer.SetEncodedObject(treeObj)
	if err != nil {
		return nil, fmt.Errorf("failed to store empty tree: %w", err)
	}
	return repo.TreeObject(emptyTreeHash)
}

// mergeTreeEntries recursively merges tree entries from base, source, and target trees
func (h *MergeOperationsHandler) mergeTreeEntries(
	ctx context.Context,
	repo *gogit.Repository,
	baseTree, sourceTree, targetTree *object.Tree,
	prefix string,
	conflictMap map[string]*FileConflict,
	strategy MergeStrategy,
	protectionCtx *MergeProtectionContext,
) ([]object.TreeEntry, error) {
	// Collect all entries from all three trees
	entryMap := h.collectTreeEntries(baseTree, sourceTree, targetTree)

	// Resolve each entry
	var resolvedEntries []object.TreeEntry

	for _, mergeEntry := range entryMap {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		resolvedEntry, err := h.resolveTreeEntry(ctx, repo, mergeEntry, prefix, conflictMap, strategy, protectionCtx)
		if err != nil {
			h.logger.Warn("Failed to resolve tree entry, skipping",
				zap.String("name", mergeEntry.name),
				zap.String("prefix", prefix),
				zap.Error(err))
			continue
		}

		if resolvedEntry != nil {
			resolvedEntries = append(resolvedEntries, *resolvedEntry)
		}
	}

	// Sort tree entries by name (required by Git)
	sortTreeEntries(resolvedEntries)

	return resolvedEntries, nil
}

// resolveTreeEntry resolves a single tree entry (file or directory) for merge
func (h *MergeOperationsHandler) resolveTreeEntry(
	ctx context.Context,
	repo *gogit.Repository,
	mergeEntry *mergeTreeEntry,
	prefix string,
	conflictMap map[string]*FileConflict,
	strategy MergeStrategy,
	protectionCtx *MergeProtectionContext,
) (*object.TreeEntry, error) {
	baseExists := mergeEntry.baseEntry != nil
	sourceExists := mergeEntry.sourceEntry != nil
	targetExists := mergeEntry.targetEntry != nil

	baseIsDir := baseExists && mergeEntry.baseEntry.Mode == filemode.Dir
	sourceIsDir := sourceExists && mergeEntry.sourceEntry.Mode == filemode.Dir
	targetIsDir := targetExists && mergeEntry.targetEntry.Mode == filemode.Dir

	// Determine full path for conflict lookup
	fullPath := mergeEntry.name
	if prefix != "" {
		fullPath = prefix + "/" + mergeEntry.name
	}

	// Handle type conflicts (directory vs file)
	if h.hasTypeConflict(baseIsDir, sourceIsDir, targetIsDir, sourceExists, targetExists) {
		return h.resolveTypeConflict(ctx, repo, mergeEntry, prefix, conflictMap, strategy, fullPath, baseIsDir, sourceIsDir, targetIsDir, sourceExists, targetExists, protectionCtx)
	}

	// Handle directory entries (recursive merge)
	if baseIsDir || sourceIsDir || targetIsDir {
		return h.mergeDirectoryEntry(ctx, repo, mergeEntry, prefix, conflictMap, strategy, protectionCtx)
	}

	// Handle file entries (use existing file merge logic)
	fileEntry := &mergeFileEntry{
		path: fullPath,
	}
	if baseExists {
		fileEntry.baseHash = mergeEntry.baseEntry.Hash
		fileEntry.baseMode = mergeEntry.baseEntry.Mode
	}
	if sourceExists {
		fileEntry.sourceHash = mergeEntry.sourceEntry.Hash
		fileEntry.sourceMode = mergeEntry.sourceEntry.Mode
	}
	if targetExists {
		fileEntry.targetHash = mergeEntry.targetEntry.Hash
		fileEntry.targetMode = mergeEntry.targetEntry.Mode
	}

	conflict := conflictMap[fullPath]
	resolvedHash, resolvedMode, err := h.resolveFileForMerge(fileEntry, conflict, strategy, protectionCtx)
	if err != nil {
		return nil, err
	}

	if resolvedHash.IsZero() {
		return nil, nil // File was deleted
	}

	return &object.TreeEntry{
		Name: mergeEntry.name,
		Mode: resolvedMode,
		Hash: resolvedHash,
	}, nil
}

// mergeDirectoryEntry recursively merges a directory entry
func (h *MergeOperationsHandler) mergeDirectoryEntry(
	ctx context.Context,
	repo *gogit.Repository,
	mergeEntry *mergeTreeEntry,
	prefix string,
	conflictMap map[string]*FileConflict,
	strategy MergeStrategy,
	protectionCtx *MergeProtectionContext,
) (*object.TreeEntry, error) {
	// Get sub-trees for base, source, and target (or create empty ones)
	baseSubTree, err := h.getOrCreateEmptyTree(repo, mergeEntry.baseEntry)
	if err != nil && mergeEntry.baseEntry != nil {
		return nil, fmt.Errorf("failed to get base subtree: %w", err)
	}

	// Handle case where source is a file instead of directory (type conflict)
	var sourceSubTree *object.Tree
	if mergeEntry.sourceEntry != nil {
		if mergeEntry.sourceEntry.Mode == filemode.Dir {
			sourceSubTree, err = h.getOrCreateEmptyTree(repo, mergeEntry.sourceEntry)
			if err != nil {
				return nil, fmt.Errorf("failed to get source subtree: %w", err)
			}
		} else {
			// Source is a file, create a tree with that file
			// This handles type conflict where directory is preferred over file
			// The file should be included in the merged directory
			// Use the source entry's name (e.g., "blob" for "tree/blob")
			fileEntries := []object.TreeEntry{
				{
					Name: mergeEntry.sourceEntry.Name, // Use the actual file name from source entry
					Mode: mergeEntry.sourceEntry.Mode,
					Hash: mergeEntry.sourceEntry.Hash,
				},
			}
			sortTreeEntries(fileEntries)
			treeHash, err := h.createAndStoreTree(repo, fileEntries)
			if err != nil {
				return nil, fmt.Errorf("failed to create tree with source file: %w", err)
			}
			sourceSubTree, err = repo.TreeObject(treeHash)
			if err != nil {
				return nil, fmt.Errorf("failed to get tree with source file: %w", err)
			}
		}
	}

	targetSubTree, err := h.getOrCreateEmptyTree(repo, mergeEntry.targetEntry)
	if err != nil && mergeEntry.targetEntry != nil {
		return nil, fmt.Errorf("failed to get target subtree: %w", err)
	}

	// If no sub-trees exist, return nil (directory was deleted)
	if baseSubTree == nil && sourceSubTree == nil && targetSubTree == nil {
		return nil, nil
	}

	// Ensure all trees are non-nil (create empty ones if needed)
	if baseSubTree == nil {
		baseSubTree, err = h.createEmptyTree(repo)
		if err != nil {
			return nil, err
		}
	}
	if sourceSubTree == nil {
		sourceSubTree, err = h.createEmptyTree(repo)
		if err != nil {
			return nil, err
		}
	}
	if targetSubTree == nil {
		targetSubTree, err = h.createEmptyTree(repo)
		if err != nil {
			return nil, err
		}
	}

	// Recursively merge sub-trees
	newPrefix := mergeEntry.name
	if prefix != "" {
		newPrefix = prefix + "/" + mergeEntry.name
	}

	mergedEntries, err := h.mergeTreeEntries(ctx, repo, baseSubTree, sourceSubTree, targetSubTree, newPrefix, conflictMap, strategy, protectionCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to merge subtree entries: %w", err)
	}

	// Check if directory should be kept even if merged entries is empty
	baseHadContent := mergeEntry.baseEntry != nil && mergeEntry.baseEntry.Mode == filemode.Dir
	targetHadContent := mergeEntry.targetEntry != nil && mergeEntry.targetEntry.Mode == filemode.Dir
	sourceHadContent := mergeEntry.sourceEntry != nil && mergeEntry.sourceEntry.Mode == filemode.Dir

	// If merged entries is empty, check if we should keep the directory
	if len(mergedEntries) == 0 {
		// If directory existed in base or target (not just source), keep it even if empty
		// This handles the case where source doesn't have the directory but base/target do
		if baseHadContent || targetHadContent {
			// Directory existed in base or target, keep it (even if empty after merge)
			// This is valid in Git - empty directories can exist
			h.logger.Debug("Keeping empty directory that existed in base or target",
				zap.String("name", mergeEntry.name),
				zap.Bool("base", baseHadContent),
				zap.Bool("target", targetHadContent))
		} else if sourceHadContent {
			// Directory only existed in source, keep it
			h.logger.Debug("Keeping empty directory that existed in source",
				zap.String("name", mergeEntry.name))
		} else {
			// Directory was never created, return nil
			h.logger.Debug("Directory was never created, returning nil",
				zap.String("name", mergeEntry.name))
			return nil, nil
		}
	} else {
		// Directory has content, log for debugging
		h.logger.Debug("Directory has merged entries",
			zap.String("name", mergeEntry.name),
			zap.Int("entry_count", len(mergedEntries)))
	}

	// Sort and store the merged tree
	sortTreeEntries(mergedEntries)
	treeHash, err := h.createAndStoreTree(repo, mergedEntries)
	if err != nil {
		return nil, fmt.Errorf("failed to create merged subtree: %w", err)
	}

	return &object.TreeEntry{
		Name: mergeEntry.name,
		Mode: filemode.Dir,
		Hash: treeHash,
	}, nil
}

// hasTypeConflict checks if there's a type conflict (directory vs file)
func (h *MergeOperationsHandler) hasTypeConflict(baseIsDir, sourceIsDir, targetIsDir, sourceExists, targetExists bool) bool {
	return (baseIsDir && sourceExists && !sourceIsDir) ||
		(baseIsDir && targetExists && !targetIsDir) ||
		(sourceIsDir && targetExists && !targetIsDir) ||
		(targetIsDir && sourceExists && !sourceIsDir)
}

// resolveTypeConflict resolves a type conflict (directory vs file) using the merge strategy
func (h *MergeOperationsHandler) resolveTypeConflict(
	ctx context.Context,
	repo *gogit.Repository,
	mergeEntry *mergeTreeEntry,
	prefix string,
	conflictMap map[string]*FileConflict,
	strategy MergeStrategy,
	fullPath string,
	baseIsDir, sourceIsDir, targetIsDir, sourceExists, targetExists bool,
	protectionCtx *MergeProtectionContext,
) (*object.TreeEntry, error) {
	h.logger.Warn("Type conflict: directory vs file",
		zap.String("path", fullPath),
		zap.Bool("base_is_dir", baseIsDir),
		zap.Bool("source_is_dir", sourceIsDir),
		zap.Bool("target_is_dir", targetIsDir))

	switch strategy {
	case MergeStrategyTheirs:
		if sourceExists {
			if sourceIsDir {
				return h.mergeDirectoryEntry(ctx, repo, mergeEntry, prefix, conflictMap, strategy, protectionCtx)
			}
			return mergeEntry.sourceEntry, nil
		}
	case MergeStrategyOurs:
		if targetExists {
			if targetIsDir {
				return h.mergeDirectoryEntry(ctx, repo, mergeEntry, prefix, conflictMap, strategy, protectionCtx)
			}
			return mergeEntry.targetEntry, nil
		}
	default:
		// Prefer directory over file
		if sourceIsDir {
			return h.mergeDirectoryEntry(ctx, repo, mergeEntry, prefix, conflictMap, strategy, protectionCtx)
		}
		if targetIsDir {
			return h.mergeDirectoryEntry(ctx, repo, mergeEntry, prefix, conflictMap, strategy, protectionCtx)
		}
		// Both are files, use source
		if sourceExists {
			return mergeEntry.sourceEntry, nil
		}
		if targetExists {
			return mergeEntry.targetEntry, nil
		}
	}
	return nil, nil
}

// SAFETY FIX: Environment file detection
func (h *MergeOperationsHandler) isEnvironmentFile(path string) bool {
	environmentPatterns := []string{
		"config/env/",
		".env",
		"environments/",
		"config/staging/",
		"config/production/",
		"*.env",
	}

	for _, pattern := range environmentPatterns {
		if matchPath(path, pattern) {
			return true
		}
	}
	return false
}

// SAFETY FIX: Environment-aware conflict resolution
func (h *MergeOperationsHandler) resolveEnvironmentConflict(entry *mergeFileEntry) (plumbing.Hash, filemode.FileMode, error) {
	h.logger.Warn("Environment file conflict detected",
		zap.String("path", entry.path),
		zap.String("resolution", "preserving_target"))

	// CRITICAL SAFETY: Always preserve target environment unless explicitly overridden
	if !entry.targetHash.IsZero() {
		h.logger.Info("Preserving target environment file", zap.String("path", entry.path))
		return entry.targetHash, entry.targetMode, nil
	}

	// If target doesn't exist but source does, require explicit confirmation
	if !entry.sourceHash.IsZero() {
		h.logger.Warn("Environment file creation requires explicit confirmation",
			zap.String("path", entry.path))
		return plumbing.ZeroHash, 0, &EnvironmentConflictError{
			Path:    entry.path,
			Message: fmt.Sprintf("Environment file %s requires explicit approval for creation/modification", entry.path),
		}
	}

	// No environment file in either branch
	return plumbing.ZeroHash, 0, nil
}

// SAFETY FIX: Intelligent conflict resolution for non-environment files
func (h *MergeOperationsHandler) intelligentConflictResolution(entry *mergeFileEntry) (plumbing.Hash, filemode.FileMode, error) {
	// For non-environment files, use contextual resolution

	baseExists := !entry.baseHash.IsZero()
	sourceExists := !entry.sourceHash.IsZero()
	targetExists := !entry.targetHash.IsZero()

	// If only one side changed, choose the changed version
	if baseExists && sourceExists && targetExists {
		if entry.baseHash == entry.targetHash && entry.baseHash != entry.sourceHash {
			// Only source changed - choose source
			h.logger.Debug("Choosing source version (only source changed)", zap.String("path", entry.path))
			return entry.sourceHash, entry.sourceMode, nil
		}
		if entry.baseHash == entry.sourceHash && entry.baseHash != entry.targetHash {
			// Only target changed - choose target
			h.logger.Debug("Choosing target version (only target changed)", zap.String("path", entry.path))
			return entry.targetHash, entry.targetMode, nil
		}
	}

	// Both sides changed or complex scenario - require manual resolution
	h.logger.Warn("Complex conflict requires manual resolution", zap.String("path", entry.path))
	return plumbing.ZeroHash, 0, &ComplexConflictError{
		Path:    entry.path,
		Message: fmt.Sprintf("File %s has complex conflicts requiring manual resolution", entry.path),
	}
}

// Helper function for path matching
func matchPath(path, pattern string) bool {
	// Simple pattern matching - can be enhanced with proper glob matching
	if pattern[len(pattern)-1] == '/' {
		// Directory pattern
		return len(path) > len(pattern) && path[:len(pattern)] == pattern
	}
	if pattern[0] == '*' {
		// Suffix pattern
		suffix := pattern[1:]
		return len(path) >= len(suffix) && path[len(path)-len(suffix):] == suffix
	}
	// Exact or prefix match
	return path == pattern || (len(path) > len(pattern) && path[:len(pattern)] == pattern)
}
