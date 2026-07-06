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
	if diffContentSignature(nil) != diffContentSignature(nil) {
		t.Fatalf("nil signature 는 안정적이어야 함")
	}
	empty := &DiffResponse{Files: nil}
	if diffContentSignature(empty) == diffContentSignature(&DiffResponse{Files: []FileDiff{fileDiff("a", "added", line(LineChangeTypeAdd, "x"))}}) {
		t.Fatalf("빈 diff 와 비어있지 않은 diff 는 달라야 함")
	}
}
