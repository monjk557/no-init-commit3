package merge

import (
	"context"
	"testing"
	"time"
)

// pure_coverage_extra_test.go — origin/main 코드 기준 순수 로직 커버리지 보강.
// 외부 git/네트워크 의존 없는 헬퍼·어댑터·파서만 대상으로 한다.

func TestMatchPath(t *testing.T) {
	cases := []struct {
		path, pattern string
		want          bool
	}{
		{".env", ".env", true},          // exact
		{"foo.env", "*.env", true},      // leading wildcard (suffix match)
		{"foo.txt", "*.env", false},     // leading wildcard no match
		{"config/secrets.yml", "config/secrets*", true}, // trailing wildcard (prefix match)
		{"other/secrets.yml", "config/secrets*", false},
		{".env.local", ".env.*", true},  // special .env.* branch
		{".envlocal", ".env.*", false},  // missing dot after .env
		{"x", ".env.*", false},          // too short
	}
	for _, c := range cases {
		if got := matchPath(c.path, c.pattern); got != c.want {
			t.Errorf("matchPath(%q,%q)=%v want %v", c.path, c.pattern, got, c.want)
		}
	}
}

func TestIsEnvironmentFile(t *testing.T) {
	yes := []string{".env", ".env.local", ".env.production", "foo.env", "config/secrets.yml", "config/credentials.json"}
	no := []string{"README.md", "main.go", "src/app.ts", "environment.txt"}
	for _, p := range yes {
		if !isEnvironmentFile(p) {
			t.Errorf("isEnvironmentFile(%q)=false want true", p)
		}
	}
	for _, p := range no {
		if isEnvironmentFile(p) {
			t.Errorf("isEnvironmentFile(%q)=true want false", p)
		}
	}
}

func TestSplitParentBaseName(t *testing.T) {
	if got := splitPath("a/b/c"); len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Errorf("splitPath(a/b/c)=%v", got)
	}
	if got := splitPath("/a//b/"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("splitPath(/a//b/)=%v", got)
	}
	if got := splitPath(""); len(got) != 0 {
		t.Errorf("splitPath(empty)=%v", got)
	}
	if got := parentDir("a/b/c"); got != "a/b" {
		t.Errorf("parentDir=%q", got)
	}
	if got := parentDir("a"); got != "" {
		t.Errorf("parentDir(a)=%q want empty", got)
	}
	if got := baseName("a/b/c"); got != "c" {
		t.Errorf("baseName=%q", got)
	}
	if got := baseName("solo"); got != "solo" {
		t.Errorf("baseName(solo)=%q", got)
	}
}

func TestCalculateConflictSeverity(t *testing.T) {
	cd := NewConflictDetector(nil, nil)
	bin := &FileVersion{IsBinary: true}
	txt := &FileVersion{IsBinary: false}

	if got := cd.calculateConflictSeverity(ConflictTypeMode, nil, nil); got != ConflictSeverityLow {
		t.Errorf("mode=%v", got)
	}
	if got := cd.calculateConflictSeverity(ConflictTypeDelete, nil, nil); got != ConflictSeverityHigh {
		t.Errorf("delete=%v", got)
	}
	if got := cd.calculateConflictSeverity(ConflictTypeAdd, bin, txt); got != ConflictSeverityHigh {
		t.Errorf("add binary-mismatch=%v", got)
	}
	if got := cd.calculateConflictSeverity(ConflictTypeAdd, txt, txt); got != ConflictSeverityMedium {
		t.Errorf("add same=%v", got)
	}
	if got := cd.calculateConflictSeverity(ConflictTypeContent, bin, txt); got != ConflictSeverityCritical {
		t.Errorf("content binary=%v", got)
	}
	if got := cd.calculateConflictSeverity(ConflictTypeContent, txt, txt); got != ConflictSeverityMedium {
		t.Errorf("content text=%v", got)
	}
	if got := cd.calculateConflictSeverity(ConflictTypeRename, nil, nil); got != ConflictSeverityMedium {
		t.Errorf("default=%v", got)
	}
}

func TestIsBinaryContent(t *testing.T) {
	cd := NewConflictDetector(nil, nil)
	if !cd.isBinaryContent([]byte{1, 2, 0, 3}) {
		t.Error("expected binary for NUL byte")
	}
	if cd.isBinaryContent([]byte("plain text")) {
		t.Error("expected non-binary for text")
	}
}

func TestSeverityBreakdownAndResolutionTime(t *testing.T) {
	cd := NewConflictDetector(nil, nil)
	conflicts := []FileConflict{
		{Severity: ConflictSeverityLow},
		{Severity: ConflictSeverityLow},
		{Severity: ConflictSeverityHigh},
		{Severity: ConflictSeverityCritical},
	}
	bd := cd.calculateSeverityBreakdown(conflicts)
	if bd[ConflictSeverityLow] != 2 || bd[ConflictSeverityHigh] != 1 || bd[ConflictSeverityCritical] != 1 {
		t.Errorf("breakdown=%v", bd)
	}
	// 2+2+30+60 = 94 minutes
	if got := cd.estimateResolutionTime(conflicts); got != 94*time.Minute {
		t.Errorf("estimateResolutionTime=%v want 94m", got)
	}
}

func TestGenerateRecommendations(t *testing.T) {
	cd := NewConflictDetector(nil, nil)
	if rec := cd.generateRecommendations(nil, MergeStrategyRecursive); len(rec) != 1 {
		t.Errorf("empty recommendations=%v", rec)
	}
	conflicts := []FileConflict{{Severity: ConflictSeverityHigh}}
	if rec := cd.generateRecommendations(conflicts, MergeStrategyRecursive); len(rec) != 2 {
		t.Errorf("recursive recommendations=%v", rec)
	}
	if rec := cd.generateRecommendations(conflicts, MergeStrategyOurs); len(rec) != 2 {
		t.Errorf("ours recommendations=%v", rec)
	}
}

func TestMergeMessageAndType(t *testing.T) {
	req := &MergeRequest{SourceRef: "feature", TargetRef: "main"}

	ff := &MergeResult{IsFastForward: true}
	if msg := generateMergeMessage(ff, req); msg == "" || getMergeType(ff) != "fast-forward" {
		t.Errorf("ff msg=%q type=%q", msg, getMergeType(ff))
	}
	conf := &MergeResult{HasConflicts: true}
	if getMergeType(conf) != "three-way-with-conflicts" {
		t.Errorf("conflict type=%q", getMergeType(conf))
	}
	plain := &MergeResult{}
	if getMergeType(plain) != "three-way" {
		t.Errorf("plain type=%q", getMergeType(plain))
	}
	_ = generateMergeMessage(conf, req)
	_ = generateMergeMessage(plain, req)

	if getMergeTypeForMethod(MergeMethodSquash, plain) != "squash" {
		t.Error("squash type")
	}
	if getMergeTypeForMethod(MergeMethodRebase, plain) != "rebase" {
		t.Error("rebase type")
	}
	if getMergeTypeForMethod(MergeMethodMerge, ff) != "fast-forward" {
		t.Error("merge falls through to getMergeType")
	}
}

func TestCommitToInfo(t *testing.T) {
	h := &MergeOperationsHandler{}
	when := time.Now()
	commit := &NativeCommit{
		Hash:      "abc123",
		Message:   "msg",
		Author:    NativeSignature{Name: "A", Email: "a@x.com", When: when},
		Committer: NativeSignature{Name: "C", Email: "c@x.com", When: when},
	}
	info := h.commitToInfo(commit)
	if info["sha"] != "abc123" || info["message"] != "msg" {
		t.Errorf("commitToInfo=%v", info)
	}
	author, ok := info["author"].(map[string]interface{})
	if !ok || author["email"] != "a@x.com" {
		t.Errorf("author=%v", info["author"])
	}
}

func TestMergeEnv(t *testing.T) {
	ctx := context.Background()
	e := NewMergeEnv(ctx, "/repo", nil, nil)
	if e.RepoPath != "/repo" || e.Now.IsZero() {
		t.Errorf("NewMergeEnv=%+v", e)
	}
	if e.IsCancelled() {
		t.Error("fresh ctx should not be cancelled")
	}
	fixed := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	e2 := NewMergeEnvWithTime(ctx, "/repo", fixed, nil, nil)
	if !e2.Now.Equal(fixed) {
		t.Errorf("NewMergeEnvWithTime Now=%v", e2.Now)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	e3 := NewMergeEnv(cctx, "/repo", nil, nil)
	if !e3.IsCancelled() {
		t.Error("cancelled ctx should report cancelled")
	}
}

func TestParseModeToUint32AndEmptySnapshot(t *testing.T) {
	if got := parseModeToUint32("100644"); got != 0o100644 {
		t.Errorf("100644 -> %o", got)
	}
	if got := parseModeToUint32(" 040000 "); got != 0o040000 {
		t.Errorf("040000 -> %o", got)
	}
	snap, err := TreeToSnapshot(context.Background(), "/nonexistent", "")
	if err != nil || snap == nil || !snap.IsEmpty() {
		t.Errorf("empty treeHash should give empty snapshot, err=%v", err)
	}
}

func TestParseMergeTreeOutput(t *testing.T) {
	if _, err := parseMergeTreeOutput("", false); err == nil {
		t.Error("empty output should error")
	}
	res, err := parseMergeTreeOutput("treehash123\nCONFLICT (content): Merge conflict in a.txt\nAuto-merging b.txt", true)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if res.TreeHash != "treehash123" || !res.HasConflicts {
		t.Errorf("res=%+v", res)
	}
	if len(res.Conflicts) != 2 {
		t.Fatalf("conflicts=%v", res.Conflicts)
	}
	if res.Conflicts[0].Type != "content" || res.Conflicts[0].Path != "a.txt" {
		t.Errorf("first conflict=%+v", res.Conflicts[0])
	}
	// no-conflict path
	res2, err := parseMergeTreeOutput("onlytree", false)
	if err != nil || res2.TreeHash != "onlytree" || res2.HasConflicts {
		t.Errorf("res2=%+v err=%v", res2, err)
	}
}

func TestExtractConflictTypeAndPath(t *testing.T) {
	if got := extractConflictType("CONFLICT (content): Merge conflict in x"); got != "content" {
		t.Errorf("type=%q", got)
	}
	if got := extractConflictType("no parens here"); got != "unknown" {
		t.Errorf("type=%q", got)
	}
	if got := extractConflictPath("CONFLICT (content): Merge conflict in path/to/file"); got != "path/to/file" {
		t.Errorf("path=%q", got)
	}
}
