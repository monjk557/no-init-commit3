package merge

import (
	"testing"
)

func Test_PlanTreeMerge_EmptyTrees_ReturnsEmptyPlan(t *testing.T) {
	t.Parallel()

	base := NewTreeSnapshot()
	source := NewTreeSnapshot()
	target := NewTreeSnapshot()

	plan := PlanTreeMerge(base, source, target, nil, MergeStrategyRecursive)

	if plan == nil {
		t.Fatalf("PlanTreeMerge()=nil want=non-nil")
	}
	if len(plan.Entries) != 0 {
		t.Fatalf("len(Entries)=%d want=0", len(plan.Entries))
	}
}

func Test_PlanTreeMerge_NewFileInSource_ReturnsAddAction(t *testing.T) {
	t.Parallel()

	base := NewTreeSnapshot()
	source := NewTreeSnapshot()
	source.AddEntry("new.txt", FileSnapshot{Hash: "abc123", Mode: 0100644})
	target := NewTreeSnapshot()

	plan := PlanTreeMerge(base, source, target, nil, MergeStrategyRecursive)

	entry, ok := plan.GetEntry("new.txt")
	if !ok {
		t.Fatalf("GetEntry(new.txt)=_, false want=_, true")
	}
	if entry.Action != EntryActionAdd {
		t.Fatalf("Action=%v want=%v", entry.Action, EntryActionAdd)
	}
	if entry.Source != EntrySourceSource {
		t.Fatalf("Source=%v want=%v", entry.Source, EntrySourceSource)
	}
}

func Test_PlanTreeMerge_NewFileInTarget_ReturnsAddAction(t *testing.T) {
	t.Parallel()

	base := NewTreeSnapshot()
	source := NewTreeSnapshot()
	target := NewTreeSnapshot()
	target.AddEntry("new.txt", FileSnapshot{Hash: "def456", Mode: 0100644})

	plan := PlanTreeMerge(base, source, target, nil, MergeStrategyRecursive)

	entry, ok := plan.GetEntry("new.txt")
	if !ok {
		t.Fatalf("GetEntry(new.txt)=_, false want=_, true")
	}
	if entry.Action != EntryActionAdd {
		t.Fatalf("Action=%v want=%v", entry.Action, EntryActionAdd)
	}
	if entry.Source != EntrySourceTarget {
		t.Fatalf("Source=%v want=%v", entry.Source, EntrySourceTarget)
	}
}

func Test_PlanTreeMerge_FileUnchangedInBoth_ReturnsKeepAction(t *testing.T) {
	t.Parallel()

	base := NewTreeSnapshot()
	base.AddEntry("file.txt", FileSnapshot{Hash: "same123", Mode: 0100644})
	source := NewTreeSnapshot()
	source.AddEntry("file.txt", FileSnapshot{Hash: "same123", Mode: 0100644})
	target := NewTreeSnapshot()
	target.AddEntry("file.txt", FileSnapshot{Hash: "same123", Mode: 0100644})

	plan := PlanTreeMerge(base, source, target, nil, MergeStrategyRecursive)

	entry, ok := plan.GetEntry("file.txt")
	if !ok {
		t.Fatalf("GetEntry(file.txt)=_, false want=_, true")
	}
	if entry.Action != EntryActionKeep {
		t.Fatalf("Action=%v want=%v", entry.Action, EntryActionKeep)
	}
}

func Test_PlanTreeMerge_OnlySourceChanged_ReturnsModifyWithSource(t *testing.T) {
	t.Parallel()

	base := NewTreeSnapshot()
	base.AddEntry("file.txt", FileSnapshot{Hash: "base123", Mode: 0100644})
	source := NewTreeSnapshot()
	source.AddEntry("file.txt", FileSnapshot{Hash: "changed", Mode: 0100644})
	target := NewTreeSnapshot()
	target.AddEntry("file.txt", FileSnapshot{Hash: "base123", Mode: 0100644})

	plan := PlanTreeMerge(base, source, target, nil, MergeStrategyRecursive)

	entry, ok := plan.GetEntry("file.txt")
	if !ok {
		t.Fatalf("GetEntry(file.txt)=_, false want=_, true")
	}
	if entry.Action != EntryActionModify {
		t.Fatalf("Action=%v want=%v", entry.Action, EntryActionModify)
	}
	if entry.Source != EntrySourceSource {
		t.Fatalf("Source=%v want=%v", entry.Source, EntrySourceSource)
	}
}

func Test_PlanTreeMerge_OnlyTargetChanged_ReturnsModifyWithTarget(t *testing.T) {
	t.Parallel()

	base := NewTreeSnapshot()
	base.AddEntry("file.txt", FileSnapshot{Hash: "base123", Mode: 0100644})
	source := NewTreeSnapshot()
	source.AddEntry("file.txt", FileSnapshot{Hash: "base123", Mode: 0100644})
	target := NewTreeSnapshot()
	target.AddEntry("file.txt", FileSnapshot{Hash: "changed", Mode: 0100644})

	plan := PlanTreeMerge(base, source, target, nil, MergeStrategyRecursive)

	entry, ok := plan.GetEntry("file.txt")
	if !ok {
		t.Fatalf("GetEntry(file.txt)=_, false want=_, true")
	}
	if entry.Action != EntryActionModify {
		t.Fatalf("Action=%v want=%v", entry.Action, EntryActionModify)
	}
	if entry.Source != EntrySourceTarget {
		t.Fatalf("Source=%v want=%v", entry.Source, EntrySourceTarget)
	}
}

func Test_PlanTreeMerge_BothChangedIdentically_ReturnsModifyWithSource(t *testing.T) {
	t.Parallel()

	base := NewTreeSnapshot()
	base.AddEntry("file.txt", FileSnapshot{Hash: "base123", Mode: 0100644})
	source := NewTreeSnapshot()
	source.AddEntry("file.txt", FileSnapshot{Hash: "same_new", Mode: 0100644})
	target := NewTreeSnapshot()
	target.AddEntry("file.txt", FileSnapshot{Hash: "same_new", Mode: 0100644})

	plan := PlanTreeMerge(base, source, target, nil, MergeStrategyRecursive)

	entry, ok := plan.GetEntry("file.txt")
	if !ok {
		t.Fatalf("GetEntry(file.txt)=_, false want=_, true")
	}
	if entry.Action != EntryActionModify {
		t.Fatalf("Action=%v want=%v", entry.Action, EntryActionModify)
	}
}

func Test_PlanTreeMerge_BothChangedDifferently_Conflict(t *testing.T) {
	t.Parallel()

	base := NewTreeSnapshot()
	base.AddEntry("file.txt", FileSnapshot{Hash: "base123", Mode: 0100644})
	source := NewTreeSnapshot()
	source.AddEntry("file.txt", FileSnapshot{Hash: "source_change", Mode: 0100644})
	target := NewTreeSnapshot()
	target.AddEntry("file.txt", FileSnapshot{Hash: "target_change", Mode: 0100644})

	conflicts := map[string]bool{"file.txt": true}

	plan := PlanTreeMerge(base, source, target, conflicts, MergeStrategyRecursive)

	if !plan.HasConflicts {
		t.Fatalf("HasConflicts=false want=true")
	}
	if plan.ConflictCount() != 1 {
		t.Fatalf("ConflictCount()=%d want=1", plan.ConflictCount())
	}
}

func Test_PlanTreeMerge_DeletedInSource_TargetUnchanged_OursStrategy_Preserves(t *testing.T) {
	t.Parallel()

	base := NewTreeSnapshot()
	base.AddEntry("file.txt", FileSnapshot{Hash: "base123", Mode: 0100644})
	source := NewTreeSnapshot()
	// file.txt deleted in source
	target := NewTreeSnapshot()
	target.AddEntry("file.txt", FileSnapshot{Hash: "base123", Mode: 0100644})

	plan := PlanTreeMerge(base, source, target, nil, MergeStrategyOurs)

	entry, ok := plan.GetEntry("file.txt")
	if !ok {
		t.Fatalf("GetEntry(file.txt)=_, false want=_, true")
	}
	// Ours strategy preserves unchanged target
	if entry.Action != EntryActionKeep {
		t.Fatalf("Action=%v want=%v (ours should preserve)", entry.Action, EntryActionKeep)
	}
}

func Test_PlanTreeMerge_DeletedInSource_TheirsStrategy_Deletes(t *testing.T) {
	t.Parallel()

	base := NewTreeSnapshot()
	base.AddEntry("file.txt", FileSnapshot{Hash: "base123", Mode: 0100644})
	source := NewTreeSnapshot()
	// file.txt deleted in source
	target := NewTreeSnapshot()
	target.AddEntry("file.txt", FileSnapshot{Hash: "base123", Mode: 0100644})

	plan := PlanTreeMerge(base, source, target, nil, MergeStrategyTheirs)

	entry, ok := plan.GetEntry("file.txt")
	if !ok {
		t.Fatalf("GetEntry(file.txt)=_, false want=_, true")
	}
	if entry.Action != EntryActionDelete {
		t.Fatalf("Action=%v want=%v (theirs should delete)", entry.Action, EntryActionDelete)
	}
}

func Test_PlanTreeMerge_DeletedInTarget_SourceUnchanged_Deletes(t *testing.T) {
	t.Parallel()

	base := NewTreeSnapshot()
	base.AddEntry("file.txt", FileSnapshot{Hash: "base123", Mode: 0100644})
	source := NewTreeSnapshot()
	source.AddEntry("file.txt", FileSnapshot{Hash: "base123", Mode: 0100644})
	target := NewTreeSnapshot()
	// file.txt deleted in target

	plan := PlanTreeMerge(base, source, target, nil, MergeStrategyRecursive)

	entry, ok := plan.GetEntry("file.txt")
	if !ok {
		t.Fatalf("GetEntry(file.txt)=_, false want=_, true")
	}
	if entry.Action != EntryActionDelete {
		t.Fatalf("Action=%v want=%v", entry.Action, EntryActionDelete)
	}
}

func Test_PlanTreeMerge_AddAddConflict_SameContent_NoConflict(t *testing.T) {
	t.Parallel()

	base := NewTreeSnapshot()
	source := NewTreeSnapshot()
	source.AddEntry("new.txt", FileSnapshot{Hash: "same123", Mode: 0100644})
	target := NewTreeSnapshot()
	target.AddEntry("new.txt", FileSnapshot{Hash: "same123", Mode: 0100644})

	plan := PlanTreeMerge(base, source, target, nil, MergeStrategyRecursive)

	entry, ok := plan.GetEntry("new.txt")
	if !ok {
		t.Fatalf("GetEntry(new.txt)=_, false want=_, true")
	}
	// Same content = no conflict
	if entry.Action != EntryActionAdd {
		t.Fatalf("Action=%v want=%v", entry.Action, EntryActionAdd)
	}
}

func Test_PlanTreeMerge_IsDeterministic(t *testing.T) {
	t.Parallel()

	base := NewTreeSnapshot()
	base.AddEntry("a.txt", FileSnapshot{Hash: "a_base"})
	base.AddEntry("b.txt", FileSnapshot{Hash: "b_base"})
	base.AddEntry("c.txt", FileSnapshot{Hash: "c_base"})

	source := NewTreeSnapshot()
	source.AddEntry("a.txt", FileSnapshot{Hash: "a_source"})
	source.AddEntry("b.txt", FileSnapshot{Hash: "b_base"})
	source.AddEntry("d.txt", FileSnapshot{Hash: "d_new"})

	target := NewTreeSnapshot()
	target.AddEntry("a.txt", FileSnapshot{Hash: "a_base"})
	target.AddEntry("b.txt", FileSnapshot{Hash: "b_target"})
	target.AddEntry("c.txt", FileSnapshot{Hash: "c_base"})

	plan1 := PlanTreeMerge(base, source, target, nil, MergeStrategyRecursive)
	plan2 := PlanTreeMerge(base, source, target, nil, MergeStrategyRecursive)

	if len(plan1.Entries) != len(plan2.Entries) {
		t.Fatalf("len(plan1.Entries)=%d len(plan2.Entries)=%d want=equal", len(plan1.Entries), len(plan2.Entries))
	}

	for i := range plan1.Entries {
		if plan1.Entries[i].Path != plan2.Entries[i].Path {
			t.Fatalf("Entry[%d].Path=%q vs %q want=equal", i, plan1.Entries[i].Path, plan2.Entries[i].Path)
		}
		if plan1.Entries[i].Action != plan2.Entries[i].Action {
			t.Fatalf("Entry[%d].Action=%v vs %v want=equal", i, plan1.Entries[i].Action, plan2.Entries[i].Action)
		}
	}
}

func Test_resolveConflictSource_Ours_ReturnsTarget(t *testing.T) {
	t.Parallel()

	got := resolveConflictSource(MergeStrategyOurs)

	if got != EntrySourceTarget {
		t.Fatalf("resolveConflictSource(Ours)=%v want=%v", got, EntrySourceTarget)
	}
}

func Test_resolveConflictSource_Theirs_ReturnsSource(t *testing.T) {
	t.Parallel()

	got := resolveConflictSource(MergeStrategyTheirs)

	if got != EntrySourceSource {
		t.Fatalf("resolveConflictSource(Theirs)=%v want=%v", got, EntrySourceSource)
	}
}

func Test_resolveConflictSource_Recursive_ReturnsSource(t *testing.T) {
	t.Parallel()

	got := resolveConflictSource(MergeStrategyRecursive)

	if got != EntrySourceSource {
		t.Fatalf("resolveConflictSource(Recursive)=%v want=%v", got, EntrySourceSource)
	}
}

func Test_collectAllPaths_ReturnsUniqueSortedPaths(t *testing.T) {
	t.Parallel()

	base := NewTreeSnapshot()
	base.AddEntry("c.txt", FileSnapshot{})
	base.AddEntry("a.txt", FileSnapshot{})

	source := NewTreeSnapshot()
	source.AddEntry("b.txt", FileSnapshot{})
	source.AddEntry("a.txt", FileSnapshot{})

	target := NewTreeSnapshot()
	target.AddEntry("d.txt", FileSnapshot{})

	paths := collectAllPaths(base, source, target)

	expected := []string{"a.txt", "b.txt", "c.txt", "d.txt"}
	if len(paths) != len(expected) {
		t.Fatalf("len(paths)=%d want=%d", len(paths), len(expected))
	}
	for i, want := range expected {
		if paths[i] != want {
			t.Fatalf("paths[%d]=%q want=%q", i, paths[i], want)
		}
	}
}

func Test_collectAllPaths_NilSnapshots_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	paths := collectAllPaths(nil, nil, nil)

	if len(paths) != 0 {
		t.Fatalf("len(paths)=%d want=0", len(paths))
	}
}
