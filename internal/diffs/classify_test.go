package diffs

import (
	"reflect"
	"testing"
)

// classifyOneAnchor는 순수 로직이라 FileDiff 맵만 주입해서 검증 가능하다.
// E2E (git 저장소 기반) 테스트는 별도 integration 레벨.

func fd(path, oldPath string, status FileDiffStatus, binary bool, hunks ...Hunk) *FileDiff {
	return &FileDiff{Path: path, OldPath: oldPath, Status: status, IsBinary: binary, Hunks: hunks}
}

func hunk(startOld, countOld, startNew, countNew int) Hunk {
	return Hunk{StartOld: startOld, CountOld: countOld, StartNew: startNew, CountNew: countNew}
}

func TestClassifyOneAnchor_ActiveWhenFileUnchanged(t *testing.T) {
	m := map[string]*FileDiff{}
	r := classifyOneAnchor(m, AnchorRequest{ID: "a", Path: "src/foo.py", StartLine: 10, EndLine: 20})
	if r.Status != AnchorStatusActive {
		t.Fatalf("expected active, got %s", r.Status)
	}
}

func TestClassifyOneAnchor_OrphanedWhenFileDeleted(t *testing.T) {
	m := map[string]*FileDiff{
		"src/foo.py": fd("src/foo.py", "", FileDiffStatusDeleted, false),
	}
	r := classifyOneAnchor(m, AnchorRequest{ID: "a", Path: "src/foo.py", StartLine: 10, EndLine: 20})
	if r.Status != AnchorStatusOrphaned {
		t.Fatalf("expected orphaned, got %s", r.Status)
	}
}

func TestClassifyOneAnchor_OrphanedWhenBinary(t *testing.T) {
	m := map[string]*FileDiff{
		"src/foo.png": fd("src/foo.png", "", FileDiffStatusModified, true),
	}
	r := classifyOneAnchor(m, AnchorRequest{ID: "a", Path: "src/foo.png", StartLine: 1, EndLine: 1})
	if r.Status != AnchorStatusOrphaned {
		t.Fatalf("expected orphaned, got %s", r.Status)
	}
}

func TestClassifyOneAnchor_StaleWhenHunkOverlapsRange(t *testing.T) {
	// anchor [10,20], hunk old [12,15] → overlap → stale
	m := map[string]*FileDiff{
		"src/foo.py": fd("src/foo.py", "", FileDiffStatusModified, false, hunk(12, 4, 12, 4)),
	}
	r := classifyOneAnchor(m, AnchorRequest{ID: "a", Path: "src/foo.py", StartLine: 10, EndLine: 20})
	if r.Status != AnchorStatusStale {
		t.Fatalf("expected stale, got %s", r.Status)
	}
}

func TestClassifyOneAnchor_ActiveWhenHunkAboveRange(t *testing.T) {
	// anchor [50,55], hunk old [10,14] (엄밀히 10-13) → 겹치지 않음 → active
	m := map[string]*FileDiff{
		"src/foo.py": fd("src/foo.py", "", FileDiffStatusModified, false, hunk(10, 4, 10, 8)),
	}
	r := classifyOneAnchor(m, AnchorRequest{ID: "a", Path: "src/foo.py", StartLine: 50, EndLine: 55})
	if r.Status != AnchorStatusActive {
		t.Fatalf("expected active, got %s", r.Status)
	}
}

func TestClassifyOneAnchor_ActiveWhenHunkBelowRange(t *testing.T) {
	m := map[string]*FileDiff{
		"src/foo.py": fd("src/foo.py", "", FileDiffStatusModified, false, hunk(100, 3, 100, 3)),
	}
	r := classifyOneAnchor(m, AnchorRequest{ID: "a", Path: "src/foo.py", StartLine: 10, EndLine: 20})
	if r.Status != AnchorStatusActive {
		t.Fatalf("expected active, got %s", r.Status)
	}
}

func TestClassifyOneAnchor_PureInsertionAtAnchorNotStale(t *testing.T) {
	// CountOld=0 → old side에 해당 라인 없음. anchor [50,55] 위/아래/안에 insertion이어도 stale 아님.
	m := map[string]*FileDiff{
		"src/foo.py": fd("src/foo.py", "", FileDiffStatusModified, false, hunk(50, 0, 50, 5)),
	}
	r := classifyOneAnchor(m, AnchorRequest{ID: "a", Path: "src/foo.py", StartLine: 50, EndLine: 55})
	if r.Status != AnchorStatusActive {
		t.Fatalf("expected active for pure insertion, got %s", r.Status)
	}
}

func TestClassifyOneAnchor_RenameWithoutOverlap_ActiveWithNewPath(t *testing.T) {
	m := map[string]*FileDiff{
		"apps/old.py": fd("apps/new.py", "apps/old.py", FileDiffStatusRenamed, false),
	}
	r := classifyOneAnchor(m, AnchorRequest{ID: "a", Path: "apps/old.py", StartLine: 10, EndLine: 20})
	if r.Status != AnchorStatusActive {
		t.Fatalf("expected active on rename-only, got %s", r.Status)
	}
	if r.NewPath != "apps/new.py" {
		t.Fatalf("expected new_path=apps/new.py, got %q", r.NewPath)
	}
}

func TestClassifyOneAnchor_RenameWithOverlap_StaleWithNewPath(t *testing.T) {
	m := map[string]*FileDiff{
		"apps/old.py": fd("apps/new.py", "apps/old.py", FileDiffStatusRenamed, false, hunk(12, 4, 12, 4)),
	}
	r := classifyOneAnchor(m, AnchorRequest{ID: "a", Path: "apps/old.py", StartLine: 10, EndLine: 20})
	if r.Status != AnchorStatusStale {
		t.Fatalf("expected stale on rename+overlap, got %s", r.Status)
	}
	if r.NewPath != "apps/new.py" {
		t.Fatalf("expected new_path=apps/new.py, got %q", r.NewPath)
	}
}

// ----------------------------------------------------------------------------
// Issue-2547: per-anchor CommitSHA grouping & content-aware classification.
// ----------------------------------------------------------------------------

func TestEffectiveBaseSHA_AnchorOverridesGlobal(t *testing.T) {
	a := AnchorRequest{ID: "a", Path: "x", CommitSHA: "anchor_sha"}
	if got := effectiveBaseSHA(a, "global_base"); got != "anchor_sha" {
		t.Fatalf("expected anchor_sha, got %q", got)
	}
}

func TestEffectiveBaseSHA_FallsBackToGlobalWhenAnchorEmpty(t *testing.T) {
	a := AnchorRequest{ID: "a", Path: "x"}
	if got := effectiveBaseSHA(a, "global_base"); got != "global_base" {
		t.Fatalf("expected global_base fallback, got %q", got)
	}
}

func TestBuildOldPathIndex_SkipsAddedFiles(t *testing.T) {
	// added 파일은 anchor 가 base 기준으로 매칭될 수 없으므로 색인 제외.
	diff := &DiffResponse{
		Files: []FileDiff{
			{Path: "src/new.py", Status: FileDiffStatusAdded},
			{Path: "src/mod.py", Status: FileDiffStatusModified},
		},
	}
	idx := buildOldPathIndex(diff)
	if _, ok := idx["src/new.py"]; ok {
		t.Fatalf("added 파일이 색인에 포함되면 안됨")
	}
	if _, ok := idx["src/mod.py"]; !ok {
		t.Fatalf("modified 파일이 색인에 있어야 함")
	}
}

func TestBuildOldPathIndex_RenameKeyedByOldPath(t *testing.T) {
	diff := &DiffResponse{
		Files: []FileDiff{
			{Path: "apps/new.py", OldPath: "apps/old.py", Status: FileDiffStatusRenamed},
		},
	}
	idx := buildOldPathIndex(diff)
	if _, ok := idx["apps/old.py"]; !ok {
		t.Fatalf("rename 파일이 OldPath 로 색인되어야 함")
	}
	if _, ok := idx["apps/new.py"]; ok {
		t.Fatalf("rename 파일이 새 Path 로 색인되면 안됨 (OldPath 가 있는 경우)")
	}
}

func TestBuildOldPathIndex_DeletedAndModifiedKeyedByPath(t *testing.T) {
	diff := &DiffResponse{
		Files: []FileDiff{
			{Path: "del.py", Status: FileDiffStatusDeleted},
			{Path: "mod.py", Status: FileDiffStatusModified},
		},
	}
	idx := buildOldPathIndex(diff)
	if _, ok := idx["del.py"]; !ok {
		t.Fatalf("deleted 파일이 색인에 있어야 함")
	}
	if _, ok := idx["mod.py"]; !ok {
		t.Fatalf("modified 파일이 색인에 있어야 함")
	}
}

func TestBuildOldPathIndex_OrderPreservedAcrossMultipleStatuses(t *testing.T) {
	// 다양한 status 가 섞여도 모두 정상 색인되는지 — 단순 sanity.
	diff := &DiffResponse{
		Files: []FileDiff{
			{Path: "added.py", Status: FileDiffStatusAdded},
			{Path: "modified.py", Status: FileDiffStatusModified, Hunks: []Hunk{hunk(5, 2, 5, 3)}},
			{Path: "renamed_new.py", OldPath: "renamed_old.py", Status: FileDiffStatusRenamed},
			{Path: "deleted.py", Status: FileDiffStatusDeleted},
		},
	}
	idx := buildOldPathIndex(diff)
	wantKeys := []string{"modified.py", "renamed_old.py", "deleted.py"}
	gotKeys := make([]string, 0, len(idx))
	for k := range idx {
		gotKeys = append(gotKeys, k)
	}
	// 순서 보장 안 함 (map iteration), set 동등성만 확인.
	want := map[string]bool{}
	for _, k := range wantKeys {
		want[k] = true
	}
	got := map[string]bool{}
	for _, k := range gotKeys {
		got[k] = true
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("색인 key set 불일치: want=%v got=%v", wantKeys, gotKeys)
	}
}
