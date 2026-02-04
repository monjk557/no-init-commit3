package merge

// tree_plan.go - Layer 1: Pure Logic (ctx 모름, go-git 모름)
// 이 파일의 함수들은 순수 함수임:
// - context.Context를 사용하지 않음
// - go-git 타입을 직접 사용하지 않음
// - 부작용이 없음
// - 같은 입력 → 같은 출력 (deterministic)

// PlanTreeMerge creates a ResolutionPlan by analyzing three tree snapshots
// This is a PURE function - no side effects, no context, no go-git types
func PlanTreeMerge(
	base, source, target *TreeSnapshot,
	conflicts map[string]bool, // path -> hasConflict
	strategy MergeStrategy,
) *ResolutionPlan {
	plan := NewResolutionPlan(strategy)

	// Collect all unique paths from all three trees
	allPaths := collectAllPaths(base, source, target)

	// Plan resolution for each path
	for _, path := range allPaths {
		baseEntry, baseExists := getEntry(base, path)
		sourceEntry, sourceExists := getEntry(source, path)
		targetEntry, targetExists := getEntry(target, path)

		hasConflict := conflicts != nil && conflicts[path]

		entry := planEntryResolution(
			path,
			baseEntry, baseExists,
			sourceEntry, sourceExists,
			targetEntry, targetExists,
			hasConflict,
			strategy,
		)

		plan.AddEntry(entry)

		if hasConflict {
			plan.AddConflict(path)
		}
	}

	return plan
}

// collectAllPaths collects all unique paths from all three trees
// Pure function - no side effects
func collectAllPaths(base, source, target *TreeSnapshot) []string {
	pathSet := make(map[string]struct{})

	if base != nil {
		for path := range base.Entries {
			pathSet[path] = struct{}{}
		}
	}
	if source != nil {
		for path := range source.Entries {
			pathSet[path] = struct{}{}
		}
	}
	if target != nil {
		for path := range target.Entries {
			pathSet[path] = struct{}{}
		}
	}

	paths := make([]string, 0, len(pathSet))
	for path := range pathSet {
		paths = append(paths, path)
	}

	// Sort for determinism
	sortStrings(paths)

	return paths
}

// sortStrings sorts a slice of strings in place (simple bubble sort for determinism)
// Pure function - modifies input in place but returns nothing
func sortStrings(s []string) {
	for i := 0; i < len(s); i++ {
		for j := i + 1; j < len(s); j++ {
			if s[i] > s[j] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

// getEntry safely gets an entry from a snapshot
// Pure function - no side effects
func getEntry(snapshot *TreeSnapshot, path string) (FileSnapshot, bool) {
	if snapshot == nil {
		return FileSnapshot{}, false
	}
	return snapshot.GetEntry(path)
}

// planEntryResolution plans the resolution for a single entry
// This is the core merge logic - PURE function
func planEntryResolution(
	path string,
	baseEntry FileSnapshot, baseExists bool,
	sourceEntry FileSnapshot, sourceExists bool,
	targetEntry FileSnapshot, targetExists bool,
	hasConflict bool,
	strategy MergeStrategy,
) PlannedEntry {
	entry := PlannedEntry{
		Path: path,
	}

	// Set hashes and modes
	if baseExists {
		entry.BaseHash = baseEntry.Hash
	}
	if sourceExists {
		entry.SourceHash = sourceEntry.Hash
		entry.Mode = sourceEntry.Mode
		entry.IsDir = sourceEntry.IsDir
	}
	if targetExists {
		entry.TargetHash = targetEntry.Hash
		if !sourceExists {
			entry.Mode = targetEntry.Mode
			entry.IsDir = targetEntry.IsDir
		}
	}

	// Case 1: File doesn't exist in any tree
	if !baseExists && !sourceExists && !targetExists {
		entry.Action = EntryActionDelete
		entry.Reason = "file does not exist in any tree"
		return entry
	}

	// Case 2: New file in source only
	if !baseExists && !targetExists && sourceExists {
		entry.Action = EntryActionAdd
		entry.Source = EntrySourceSource
		entry.Reason = "new file in source"
		return entry
	}

	// Case 3: New file in target only
	if !baseExists && !sourceExists && targetExists {
		entry.Action = EntryActionAdd
		entry.Source = EntrySourceTarget
		entry.Reason = "new file in target"
		return entry
	}

	// Case 4: New file in both (add/add conflict)
	if !baseExists && sourceExists && targetExists {
		if sourceEntry.Hash == targetEntry.Hash {
			// Same content - no conflict
			entry.Action = EntryActionAdd
			entry.Source = EntrySourceSource
			entry.Reason = "new file in both with identical content"
			return entry
		}
		// Different content - conflict
		entry.Action = EntryActionModify
		entry.Source = resolveConflictSource(strategy)
		entry.Reason = "add/add conflict"
		return entry
	}

	// Case 5: File exists in all three
	if baseExists && sourceExists && targetExists {
		return planThreeWayResolution(entry, baseEntry, sourceEntry, targetEntry, hasConflict, strategy)
	}

	// Case 6: File deleted in source
	if baseExists && !sourceExists && targetExists {
		if baseEntry.Hash == targetEntry.Hash {
			// Target unchanged - respect source deletion based on strategy
			if strategy == MergeStrategyTheirs {
				entry.Action = EntryActionDelete
				entry.Source = EntrySourceSource
				entry.Reason = "deleted in source, unchanged in target (theirs strategy)"
			} else {
				// Preserve unchanged target for recursive/ORT/ours
				entry.Action = EntryActionKeep
				entry.Source = EntrySourceTarget
				entry.Reason = "deleted in source, unchanged in target (preserving)"
			}
			return entry
		}
		// Target modified - conflict
		entry.Action = EntryActionModify
		entry.Source = resolveDeleteModifyConflict(strategy, false)
		entry.Reason = "modified in target, deleted in source"
		return entry
	}

	// Case 7: File deleted in target
	if baseExists && sourceExists && !targetExists {
		if baseEntry.Hash == sourceEntry.Hash {
			// Source unchanged - respect target deletion
			entry.Action = EntryActionDelete
			entry.Reason = "deleted in target, unchanged in source"
			return entry
		}
		// Source modified - conflict
		entry.Action = EntryActionModify
		entry.Source = resolveDeleteModifyConflict(strategy, true)
		entry.Reason = "modified in source, deleted in target"
		return entry
	}

	// Default: keep target
	entry.Action = EntryActionKeep
	entry.Source = EntrySourceTarget
	entry.Reason = "default to target"
	return entry
}

// planThreeWayResolution plans resolution for the common case where file exists in all three trees
// Pure function
func planThreeWayResolution(
	entry PlannedEntry,
	baseEntry, sourceEntry, targetEntry FileSnapshot,
	hasConflict bool,
	strategy MergeStrategy,
) PlannedEntry {
	// Both unchanged
	if baseEntry.Hash == sourceEntry.Hash && baseEntry.Hash == targetEntry.Hash {
		entry.Action = EntryActionKeep
		entry.Source = EntrySourceTarget
		entry.Reason = "unchanged in both branches"
		return entry
	}

	// Only source changed
	if baseEntry.Hash != sourceEntry.Hash && baseEntry.Hash == targetEntry.Hash {
		entry.Action = EntryActionModify
		entry.Source = EntrySourceSource
		entry.Reason = "only changed in source"
		return entry
	}

	// Only target changed
	if baseEntry.Hash == sourceEntry.Hash && baseEntry.Hash != targetEntry.Hash {
		entry.Action = EntryActionModify
		entry.Source = EntrySourceTarget
		entry.Reason = "only changed in target"
		return entry
	}

	// Both changed to same content
	if sourceEntry.Hash == targetEntry.Hash {
		entry.Action = EntryActionModify
		entry.Source = EntrySourceSource
		entry.Reason = "changed identically in both branches"
		return entry
	}

	// Both changed differently - conflict!
	entry.Action = EntryActionModify
	entry.Source = resolveConflictSource(strategy)
	entry.Reason = "changed differently in both branches"
	return entry
}

// resolveConflictSource determines which source to use based on strategy
// Pure function
func resolveConflictSource(strategy MergeStrategy) EntrySource {
	switch strategy {
	case MergeStrategyOurs:
		return EntrySourceTarget
	case MergeStrategyTheirs:
		return EntrySourceSource
	case MergeStrategyRecursive, MergeStrategyResolve, MergeStrategyORT:
		// Default to source for recursive/resolve/ORT
		return EntrySourceSource
	default:
		return EntrySourceSource
	}
}

// resolveDeleteModifyConflict determines which source to use for delete/modify conflicts
// sourceModified: true if source was modified (target deleted), false if target was modified (source deleted)
// Pure function
func resolveDeleteModifyConflict(strategy MergeStrategy, sourceModified bool) EntrySource {
	switch strategy {
	case MergeStrategyTheirs:
		if sourceModified {
			return EntrySourceSource
		}
		return EntrySourceTarget // This would mean deletion, but we return target as placeholder
	case MergeStrategyOurs:
		if sourceModified {
			return EntrySourceTarget // Deletion
		}
		return EntrySourceTarget
	default:
		// For recursive/ORT, prefer keeping modified content
		if sourceModified {
			return EntrySourceSource
		}
		return EntrySourceTarget
	}
}
