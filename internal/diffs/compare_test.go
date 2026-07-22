package diffs

import "testing"

func fileDiff(path string, status FileDiffStatus, lines ...LineChange) FileDiff {
	return FileDiff{Path: path, Status: status, Changes: lines}
}

func line(t LineChangeType, text string) LineChange {
	return LineChange{Type: t, Text: text}
}

func TestDiffContentSignature_IdenticalDiffsMatch(t *testing.T) {
	a := &DiffResponse{Files: []FileDiff{
		fileDiff("src/main.go", "modified", line(LineChangeTypeContext, "package main"), line(LineChangeTypeAdd, "import \"fmt\"")),
	}}
	b := &DiffResponse{Files: []FileDiff{
		fileDiff("src/main.go", "modified", line(LineChangeTypeContext, "package main"), line(LineChangeTypeAdd, "import \"fmt\"")),
	}}
	if diffContentSignature(a) != diffContentSignature(b) {
		t.Fatalf("동일 diff 의 signature 가 달라야 안 됨")
	}
}

func TestDiffContentSignature_DifferentLineContentDiffers(t *testing.T) {
	a := &DiffResponse{Files: []FileDiff{
		fileDiff("src/main.go", "modified", line(LineChangeTypeAdd, "import \"fmt\"")),
	}}
	b := &DiffResponse{Files: []FileDiff{
		fileDiff("src/main.go", "modified", line(LineChangeTypeAdd, "import \"os\"")),
	}}
	if diffContentSignature(a) == diffContentSignature(b) {
		t.Fatalf("라인 내용이 다르면 signature 도 달라야 함")
	}
}

func TestDiffContentSignature_FileOrderIndependent(t *testing.T) {
	a := &DiffResponse{Files: []FileDiff{
		fileDiff("a.go", "modified", line(LineChangeTypeAdd, "x")),
		fileDiff("b.go", "added", line(LineChangeTypeAdd, "y")),
	}}
	b := &DiffResponse{Files: []FileDiff{
		fileDiff("b.go", "added", line(LineChangeTypeAdd, "y")),
		fileDiff("a.go", "modified", line(LineChangeTypeAdd, "x")),
	}}
	if diffContentSignature(a) != diffContentSignature(b) {
		t.Fatalf("파일 순서가 달라도 signature 는 같아야 함")
	}
}

func TestDiffContentSignature_HunkHeaderIgnored(t *testing.T) {
	// 같은 내용이지만 hunk 헤더의 line 번호만 다른 경우 (rebase 후 흔함) → 같은 signature.
	a := &DiffResponse{Files: []FileDiff{
		fileDiff("a.go", "modified", line(LineChangeTypeHunk, "@@ -1,3 +1,4 @@"), line(LineChangeTypeAdd, "x")),
	}}
	b := &DiffResponse{Files: []FileDiff{
		fileDiff("a.go", "modified", line(LineChangeTypeHunk, "@@ -50,3 +51,4 @@"), line(LineChangeTypeAdd, "x")),
	}}
	if diffContentSignature(a) != diffContentSignature(b) {
		t.Fatalf("hunk 헤더 line 번호 차이는 무시해야 함 (내용 동일)")
	}
}

func TestDiffContentSignature_DifferentStatusDiffers(t *testing.T) {
	a := &DiffResponse{Files: []FileDiff{fileDiff("a.go", "modified", line(LineChangeTypeAdd, "x"))}}
	b := &DiffResponse{Files: []FileDiff{fileDiff("a.go", "added", line(LineChangeTypeAdd, "x"))}}
	if diffContentSignature(a) == diffContentSignature(b) {
		t.Fatalf("파일 status 가 다르면 signature 도 달라야 함")
	}
}

func TestDiffContentSignature_NilIsStable(t *testing.T) {
	// nil 은 sentinel 로 빈 문자열을 돌려준다 — 해시된 값이 아니어야 하고,
	// "파일이 없는 diff" 와도 구분돼야 한다. (기존 테스트는 f(nil) != f(nil) 이라는
	// 항등 비교라 아무것도 검증하지 못했다 — staticcheck SA4000)
	if got := diffContentSignature(nil); got != "" {
		t.Fatalf("nil signature 는 빈 문자열이어야 함: got %q", got)
	}
	empty := &DiffResponse{Files: nil}
	if diffContentSignature(empty) == diffContentSignature(nil) {
		t.Fatalf("nil 과 빈 diff 는 구분돼야 함")
	}
	if diffContentSignature(empty) == diffContentSignature(&DiffResponse{Files: []FileDiff{fileDiff("a", "added", line(LineChangeTypeAdd, "x"))}}) {
		t.Fatalf("빈 diff 와 비어있지 않은 diff 는 달라야 함")
	}
}
