package diffs

import "testing"

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
