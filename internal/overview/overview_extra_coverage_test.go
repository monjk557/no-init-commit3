package overview

import (
	"context"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
)

// overview_extra_coverage_test.go — overview 캐시/페이지네이션 순수 로직 커버.

func testLogger(t *testing.T) *logging.Logger {
	t.Helper()
	l, err := logging.NewLogger(config.LoggingConfig{Level: "error", Format: "json"})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	return l
}

func TestOverviewCache_LanguagesAndTreeEntries(t *testing.T) {
	c := NewOverviewCache(10, 30*time.Second)

	if _, ok := c.GetLanguages("/repo", "h"); ok {
		t.Error("expected languages miss")
	}
	c.SetLanguages("/repo", "h", LanguageDistribution{})
	if _, ok := c.GetLanguages("/repo", "h"); !ok {
		t.Error("expected languages hit after set")
	}

	if _, ok := c.GetTreeEntries("/repo", "h", "dir"); ok {
		t.Error("expected tree entries miss")
	}
	c.SetTreeEntries("/repo", "h", "dir", []GitTreeEntryWithCommit{{Type: "blob", Name: "a"}})
	if entries, ok := c.GetTreeEntries("/repo", "h", "dir"); !ok || len(entries) != 1 {
		t.Errorf("expected tree entries hit, got %v ok=%v", entries, ok)
	}
}

func TestDeterminePageSize_AllBranches(t *testing.T) {
	p := NewSmartPagination(testLogger(t))
	ctx := context.Background()

	if got := p.DeterminePageSize(ctx, "repo", "", 200); got != 100 {
		t.Errorf("over max -> %d want 100", got)
	}
	if got := p.DeterminePageSize(ctx, "repo", "", 5); got != 10 {
		t.Errorf("under min -> %d want 10", got)
	}
	if got := p.DeterminePageSize(ctx, "repo", "", 30); got != 30 {
		t.Errorf("valid -> %d want 30", got)
	}
	// requested=0 → 추정(50, small) → SmallRepoPageSize 100, 성능 정상
	if got := p.DeterminePageSize(ctx, "repo", "", 0); got != 100 {
		t.Errorf("estimate path -> %d want 100", got)
	}
	// 성능 저하 분기: avg > MaxResponseTime → 페이지 크기 축소
	p.monitor.RecordResponseTime("slow", 500*time.Millisecond)
	if got := p.monitor.GetAverageResponseTime("slow"); got != 500*time.Millisecond {
		t.Errorf("recorded avg = %v", got)
	}
	if got := p.DeterminePageSize(ctx, "slow", "", 0); got != 66 {
		t.Errorf("degraded path -> %d want 66", got)
	}
}

func TestApplyPagination(t *testing.T) {
	p := NewSmartPagination(testLogger(t))
	files := make([]GitTreeEntryWithCommit, 25)
	for i := range files {
		files[i] = GitTreeEntryWithCommit{Type: "blob", Name: "f"}
	}
	resp, pg := p.ApplyPagination(files, 1, 10, "http://x/tree")
	if resp == nil || pg == nil {
		t.Fatal("nil response/pagination")
	}
	// page<1 정규화 분기
	resp2, pg2 := p.ApplyPagination(files, 0, 10, "http://x/tree")
	if resp2 == nil || pg2 == nil {
		t.Fatal("nil on page<1")
	}
}
