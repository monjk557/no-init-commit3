package merge

import (
	"testing"
)

func Test_NewResolutionPlan_ReturnsEmptyPlan(t *testing.T) {
	t.Parallel()

	plan := NewResolutionPlan(MergeStrategyRecursive)

	if plan == nil {
		t.Fatalf("NewResolutionPlan()=nil want=non-nil")
	}
	if plan.Strategy != MergeStrategyRecursive {
		t.Fatalf("Strategy=%v want=%v", plan.Strategy, MergeStrategyRecursive)
	}
	if plan.HasConflicts {
		t.Fatalf("HasConflicts=true want=false")
	}
	if len(plan.Entries) != 0 {
		t.Fatalf("len(Entries)=%d want=0", len(plan.Entries))
	}
	if len(plan.ConflictPaths) != 0 {
		t.Fatalf("len(ConflictPaths)=%d want=0", len(plan.ConflictPaths))
	}
}

func Test_ResolutionPlan_AddEntry_AddsEntry(t *testing.T) {
	t.Parallel()

	plan := NewResolutionPlan(MergeStrategyOurs)
	entry := PlannedEntry{
		Path:   "file.txt",
		Action: EntryActionAdd,
		Source: EntrySourceSource,
	}

	plan.AddEntry(entry)

	if len(plan.Entries) != 1 {
		t.Fatalf("len(Entries)=%d want=1", len(plan.Entries))
	}
	if plan.Entries[0].Path != "file.txt" {
		t.Fatalf("Entries[0].Path=%q want=%q", plan.Entries[0].Path, "file.txt")
	}
}

func Test_ResolutionPlan_AddConflict_SetsHasConflicts(t *testing.T) {
	t.Parallel()

	plan := NewResolutionPlan(MergeStrategyRecursive)

	plan.AddConflict("conflicted.txt")

	if !plan.HasConflicts {
		t.Fatalf("HasConflicts=false want=true after AddConflict")
	}
	if len(plan.ConflictPaths) != 1 {
		t.Fatalf("len(ConflictPaths)=%d want=1", len(plan.ConflictPaths))
	}
	if plan.ConflictPaths[0] != "conflicted.txt" {
		t.Fatalf("ConflictPaths[0]=%q want=%q", plan.ConflictPaths[0], "conflicted.txt")
	}
}

func Test_ResolutionPlan_ConflictCount_ReturnsCorrectCount(t *testing.T) {
	t.Parallel()

	plan := NewResolutionPlan(MergeStrategyRecursive)

	if plan.ConflictCount() != 0 {
		t.Fatalf("ConflictCount()=%d want=0", plan.ConflictCount())
	}

	plan.AddConflict("a.txt")
	plan.AddConflict("b.txt")
	plan.AddConflict("c.txt")

	if plan.ConflictCount() != 3 {
		t.Fatalf("ConflictCount()=%d want=3", plan.ConflictCount())
	}
}

func Test_ResolutionPlan_GetEntry_ReturnsEntry(t *testing.T) {
	t.Parallel()

	plan := NewResolutionPlan(MergeStrategyRecursive)
	plan.AddEntry(PlannedEntry{Path: "a.txt", Action: EntryActionAdd})
	plan.AddEntry(PlannedEntry{Path: "b.txt", Action: EntryActionModify})

	entry, ok := plan.GetEntry("b.txt")

	if !ok {
		t.Fatalf("GetEntry(b.txt)=_, false want=_, true")
	}
	if entry.Action != EntryActionModify {
		t.Fatalf("Action=%v want=%v", entry.Action, EntryActionModify)
	}
}

func Test_ResolutionPlan_GetEntry_NotFound_ReturnsFalse(t *testing.T) {
	t.Parallel()

	plan := NewResolutionPlan(MergeStrategyRecursive)

	_, ok := plan.GetEntry("nonexistent.txt")

	if ok {
		t.Fatalf("GetEntry(nonexistent)=_, true want=_, false")
	}
}

func Test_PlannedEntry_AllFieldsSet(t *testing.T) {
	t.Parallel()

	entry := PlannedEntry{
		Path:       "dir/file.txt",
		Action:     EntryActionModify,
		Source:     EntrySourceSource,
		SourceHash: "abc123",
		TargetHash: "def456",
		BaseHash:   "ghi789",
		Mode:       0100644,
		IsDir:      false,
		Reason:     "changed in source",
	}

	if entry.Path != "dir/file.txt" {
		t.Fatalf("Path=%q want=%q", entry.Path, "dir/file.txt")
	}
	if entry.Action != EntryActionModify {
		t.Fatalf("Action=%v want=%v", entry.Action, EntryActionModify)
	}
	if entry.Source != EntrySourceSource {
		t.Fatalf("Source=%v want=%v", entry.Source, EntrySourceSource)
	}
	if entry.SourceHash != "abc123" {
		t.Fatalf("SourceHash=%q want=%q", entry.SourceHash, "abc123")
	}
	if entry.Reason != "changed in source" {
		t.Fatalf("Reason=%q want=%q", entry.Reason, "changed in source")
	}
}

func Test_EntryAction_Constants(t *testing.T) {
	t.Parallel()

	// Verify constants have distinct values
	actions := []EntryAction{
		EntryActionKeep,
		EntryActionAdd,
		EntryActionDelete,
		EntryActionModify,
	}

	seen := make(map[EntryAction]bool)
	for _, a := range actions {
		if seen[a] {
			t.Fatalf("duplicate EntryAction value: %v", a)
		}
		seen[a] = true
	}
}

func Test_EntrySource_Constants(t *testing.T) {
	t.Parallel()

	// Verify constants have distinct values
	sources := []EntrySource{
		EntrySourceBase,
		EntrySourceSource,
		EntrySourceTarget,
		EntrySourceMerged,
	}

	seen := make(map[EntrySource]bool)
	for _, s := range sources {
		if seen[s] {
			t.Fatalf("duplicate EntrySource value: %v", s)
		}
		seen[s] = true
	}
}

func Test_NewApplyResult_ReturnsSuccessResult(t *testing.T) {
	t.Parallel()

	result := NewApplyResult()

	if result == nil {
		t.Fatalf("NewApplyResult()=nil want=non-nil")
	}
	if !result.Success {
		t.Fatalf("Success=false want=true")
	}
	if result.AppliedHashes == nil {
		t.Fatalf("AppliedHashes=nil want=non-nil map")
	}
	if len(result.AppliedHashes) != 0 {
		t.Fatalf("len(AppliedHashes)=%d want=0", len(result.AppliedHashes))
	}
}

func Test_ApplyResult_StoresTreeHash(t *testing.T) {
	t.Parallel()

	result := NewApplyResult()
	result.TreeHash = "abc123def456"

	if result.TreeHash != "abc123def456" {
		t.Fatalf("TreeHash=%q want=%q", result.TreeHash, "abc123def456")
	}
}

func Test_ApplyResult_StoresAppliedHashes(t *testing.T) {
	t.Parallel()

	result := NewApplyResult()
	result.AppliedHashes["file.txt"] = "hash1"
	result.AppliedHashes["dir/file.txt"] = "hash2"

	if result.AppliedHashes["file.txt"] != "hash1" {
		t.Fatalf("AppliedHashes[file.txt]=%q want=%q", result.AppliedHashes["file.txt"], "hash1")
	}
	if result.AppliedHashes["dir/file.txt"] != "hash2" {
		t.Fatalf("AppliedHashes[dir/file.txt]=%q want=%q", result.AppliedHashes["dir/file.txt"], "hash2")
	}
}
