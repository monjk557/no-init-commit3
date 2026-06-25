package overview

import (
	"context"
	"path/filepath"
	"testing"
)

// service_extra_coverage_test.go — 실제 bare 레포(main + 3커밋)로 service 트리/리모트/
// 카운트/캐시 함수 커버. 기존 setupTestService/createTestRepositoryWithCommits 재사용.

func TestOverviewService_TreeRemotesCount(t *testing.T) {
	svc, tempDir, cleanup := setupTestService(t)
	defer cleanup()
	repo := createTestRepositoryWithCommits(t, tempDir)
	ctx := context.Background()

	// GetTreeOnly — 루트 트리
	entries, err := svc.GetTreeOnly(ctx, repo, "main", "")
	if err != nil {
		t.Fatalf("GetTreeOnly: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected root entries")
	}

	// GetTreeWithCommit — 루트 + 하위 디렉토리
	rootEntries, _, err := svc.GetTreeWithCommit(ctx, repo, "main", "")
	if err != nil {
		t.Fatalf("GetTreeWithCommit root: %v", err)
	}
	if len(rootEntries) == 0 {
		t.Fatal("expected root entries with commit")
	}
	if _, _, err := svc.GetTreeWithCommit(ctx, repo, "main", "src"); err != nil {
		t.Fatalf("GetTreeWithCommit src: %v", err)
	}

	// GetRemotes — 리모트 없음 → 빈 슬라이스, 에러 없음
	if _, err := svc.GetRemotes(ctx, repo); err != nil {
		t.Fatalf("GetRemotes: %v", err)
	}

	// CountCommitsBetween — HEAD와 그 부모 사이
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	to, err := repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatalf("CommitObject: %v", err)
	}
	from, err := to.Parent(0)
	if err != nil {
		t.Fatalf("Parent: %v", err)
	}
	if _, err := svc.CountCommitsBetween(repo, from, to); err != nil {
		t.Fatalf("CountCommitsBetween: %v", err)
	}
}

func TestOverviewService_CacheAndStats(t *testing.T) {
	svc, tempDir, cleanup := setupTestService(t)
	defer cleanup()
	_ = createTestRepositoryWithCommits(t, tempDir)
	repoPath := filepath.Join(tempDir, "test.git")
	ctx := context.Background()

	if svc.GetCacheMetrics() == nil {
		t.Error("GetCacheMetrics nil")
	}
	if svc.GetPerformanceStats(repoPath) == nil {
		t.Error("GetPerformanceStats nil")
	}
	if err := svc.EnsureRepositoryOptimized(repoPath); err != nil {
		t.Errorf("EnsureRepositoryOptimized: %v", err)
	}
	if err := svc.InvalidateRepositoryCache(ctx, "some-repo-id"); err != nil {
		t.Errorf("InvalidateRepositoryCache: %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
