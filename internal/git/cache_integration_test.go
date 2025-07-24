package git

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestOpenRepositoryWithCache_Integration(t *testing.T) {
	// Save and restore original global cache
	originalCache := GetGlobalCache()
	defer SetGlobalCache(originalCache)

	// Create test cache with small size for testing eviction
	testCache := NewRepositoryCache(&CacheConfig{
		MaxSize: 2,
		MaxAge:  time.Minute,
	})
	SetGlobalCache(testCache)

	// Create test repositories
	repo1Path := createTestRepo(t)
	defer func() { _ = os.RemoveAll(repo1Path) }()
	addFileToRepo(t, repo1Path, "README.md", "# Repo 1")
	commitRepo(t, repo1Path, "Initial commit")

	repo2Path := createTestRepo(t)
	defer func() { _ = os.RemoveAll(repo2Path) }()
	addFileToRepo(t, repo2Path, "README.md", "# Repo 2")
	commitRepo(t, repo2Path, "Initial commit")

	repo3Path := createTestRepo(t)
	defer func() { _ = os.RemoveAll(repo3Path) }()
	addFileToRepo(t, repo3Path, "README.md", "# Repo 3")
	commitRepo(t, repo3Path, "Initial commit")

	// Test cache miss - first open
	repo1, err := OpenRepositoryWithCache(repo1Path)
	if err != nil {
		t.Fatalf("Failed to open repository: %v", err)
	}
	defer func() { _ = repo1.Close() }()

	if testCache.Size() != 1 {
		t.Errorf("Expected cache size 1, got %d", testCache.Size())
	}

	// Test cache hit - open same repository again
	repo1Again, err := OpenRepositoryWithCache(repo1Path)
	if err != nil {
		t.Fatalf("Failed to open cached repository: %v", err)
	}
	defer func() { _ = repo1Again.Close() }()

	// Should be the same instance from cache
	if repo1 != repo1Again {
		t.Error("Expected same repository instance from cache")
	}

	if testCache.Size() != 1 {
		t.Errorf("Expected cache size still 1, got %d", testCache.Size())
	}

	// Test adding second repository
	repo2, err := OpenRepositoryWithCache(repo2Path)
	if err != nil {
		t.Fatalf("Failed to open second repository: %v", err)
	}
	defer func() { _ = repo2.Close() }()

	if testCache.Size() != 2 {
		t.Errorf("Expected cache size 2, got %d", testCache.Size())
	}

	// Test cache eviction - add third repository (should evict oldest)
	repo3, err := OpenRepositoryWithCache(repo3Path)
	if err != nil {
		t.Fatalf("Failed to open third repository: %v", err)
	}
	defer func() { _ = repo3.Close() }()

	if testCache.Size() != 2 {
		t.Errorf("Expected cache size still 2 after eviction, got %d", testCache.Size())
	}

	// First repository should be evicted, so opening it again should be a cache miss
	repo1NewInstance, err := OpenRepositoryWithCache(repo1Path)
	if err != nil {
		t.Fatalf("Failed to re-open first repository: %v", err)
	}
	defer func() { _ = repo1NewInstance.Close() }()

	// Should be a different instance since it was evicted and reopened
	if repo1 == repo1NewInstance {
		t.Error("Expected different repository instance after eviction")
	}
}

func TestOpenRepositoryWithCache_FunctionalityPreserved(t *testing.T) {
	// Save and restore original global cache
	originalCache := GetGlobalCache()
	defer SetGlobalCache(originalCache)

	// Set up test cache
	testCache := NewRepositoryCache(DefaultCacheConfig())
	SetGlobalCache(testCache)

	// Create test repository
	repoPath := createTestRepo(t)
	defer func() { _ = os.RemoveAll(repoPath) }()
	addFileToRepo(t, repoPath, "main.go", "package main\n\nfunc main() {}")
	addFileToRepo(t, repoPath, "docs/README.md", "# Documentation")
	commitRepo(t, repoPath, "Initial commit")

	// Open repository with cache
	repo, err := OpenRepositoryWithCache(repoPath)
	if err != nil {
		t.Fatalf("Failed to open repository with cache: %v", err)
	}
	defer func() { _ = repo.Close() }()

	ctx := context.Background()

	// Test that all Repository interface methods work correctly with cached repository
	// Test GetTree
	entries, err := repo.GetTree(ctx, "HEAD")
	if err != nil {
		t.Fatalf("Failed to get tree from cached repository: %v", err)
	}
	if len(entries) == 0 {
		t.Error("Expected tree entries from cached repository")
	}

	// Test GetBlob
	content, err := repo.GetBlob(ctx, "main.go", "HEAD")
	if err != nil {
		t.Fatalf("Failed to get blob from cached repository: %v", err)
	}
	if string(content) != "package main\n\nfunc main() {}" {
		t.Errorf("Unexpected blob content from cached repository: %s", string(content))
	}

	// Test GetCommits
	commits, err := repo.GetCommits(ctx, "HEAD", 10, 0)
	if err != nil {
		t.Fatalf("Failed to get commits from cached repository: %v", err)
	}
	if len(commits) == 0 {
		t.Error("Expected commits from cached repository")
	}

	// Test Path method
	if repo.Path() != repoPath {
		t.Errorf("Expected path %s, got %s", repoPath, repo.Path())
	}
}

func TestOpenRepositoryWithCache_ErrorHandling(t *testing.T) {
	// Save and restore original global cache
	originalCache := GetGlobalCache()
	defer SetGlobalCache(originalCache)

	// Set up test cache
	testCache := NewRepositoryCache(DefaultCacheConfig())
	SetGlobalCache(testCache)

	// Test opening non-existent repository
	_, err := OpenRepositoryWithCache("/path/that/does/not/exist")
	if err == nil {
		t.Fatal("Expected error for non-existent repository")
	}

	_, ok := err.(*RepositoryNotFoundError)
	if !ok {
		t.Errorf("Expected RepositoryNotFoundError, got %T", err)
	}

	// Cache should be empty after failed open
	if testCache.Size() != 0 {
		t.Errorf("Expected cache size 0 after failed open, got %d", testCache.Size())
	}

	// Test opening invalid repository
	tempDir, err := os.MkdirTemp("", "invalid-repo-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	_, err = OpenRepositoryWithCache(tempDir)
	if err == nil {
		t.Fatal("Expected error for invalid repository")
	}

	_, ok = err.(*InvalidRepositoryError)
	if !ok {
		t.Errorf("Expected InvalidRepositoryError, got %T", err)
	}

	// Cache should still be empty after failed open
	if testCache.Size() != 0 {
		t.Errorf("Expected cache size 0 after failed open, got %d", testCache.Size())
	}
}

func TestOpenRepository_vs_OpenRepositoryWithCache(t *testing.T) {
	// Save and restore original global cache
	originalCache := GetGlobalCache()
	defer SetGlobalCache(originalCache)

	// Set up test cache
	testCache := NewRepositoryCache(DefaultCacheConfig())
	SetGlobalCache(testCache)

	// Create test repository
	repoPath := createTestRepo(t)
	defer func() { _ = os.RemoveAll(repoPath) }()
	addFileToRepo(t, repoPath, "README.md", "# Test")
	commitRepo(t, repoPath, "Initial commit")

	// Open repository without cache
	repo1, err := OpenRepository(repoPath)
	if err != nil {
		t.Fatalf("Failed to open repository without cache: %v", err)
	}
	defer func() { _ = repo1.Close() }()

	// Cache should be empty
	if testCache.Size() != 0 {
		t.Errorf("Expected cache size 0 after non-cached open, got %d", testCache.Size())
	}

	// Open repository with cache
	repo2, err := OpenRepositoryWithCache(repoPath)
	if err != nil {
		t.Fatalf("Failed to open repository with cache: %v", err)
	}
	defer func() { _ = repo2.Close() }()

	// Cache should have one item
	if testCache.Size() != 1 {
		t.Errorf("Expected cache size 1 after cached open, got %d", testCache.Size())
	}

	// Both repositories should be different instances
	if repo1 == repo2 {
		t.Error("Expected different instances from cached vs non-cached opens")
	}

	// But they should point to the same path
	if repo1.Path() != repo2.Path() {
		t.Error("Expected same path for both repository instances")
	}

	// Open with cache again should return same instance as repo2
	repo3, err := OpenRepositoryWithCache(repoPath)
	if err != nil {
		t.Fatalf("Failed to open repository with cache again: %v", err)
	}
	defer func() { _ = repo3.Close() }()

	if repo2 != repo3 {
		t.Error("Expected same instance from subsequent cached opens")
	}

	// Cache size should still be 1
	if testCache.Size() != 1 {
		t.Errorf("Expected cache size 1 after multiple cached opens, got %d", testCache.Size())
	}
}

func TestCacheExpiration_Integration(t *testing.T) {
	// Save and restore original global cache
	originalCache := GetGlobalCache()
	defer SetGlobalCache(originalCache)

	// Set up test cache with short expiration
	testCache := NewRepositoryCache(&CacheConfig{
		MaxAge: 50 * time.Millisecond,
	})
	SetGlobalCache(testCache)

	// Create test repository
	repoPath := createTestRepo(t)
	defer func() { _ = os.RemoveAll(repoPath) }()
	addFileToRepo(t, repoPath, "README.md", "# Test")
	commitRepo(t, repoPath, "Initial commit")

	// Open repository with cache
	repo1, err := OpenRepositoryWithCache(repoPath)
	if err != nil {
		t.Fatalf("Failed to open repository: %v", err)
	}
	defer func() { _ = repo1.Close() }()

	if testCache.Size() != 1 {
		t.Errorf("Expected cache size 1, got %d", testCache.Size())
	}

	// Wait for expiration
	time.Sleep(60 * time.Millisecond)

	// Open again - should be cache miss due to expiration
	repo2, err := OpenRepositoryWithCache(repoPath)
	if err != nil {
		t.Fatalf("Failed to open repository after expiration: %v", err)
	}
	defer func() { _ = repo2.Close() }()

	// Should be different instances due to expiration
	if repo1 == repo2 {
		t.Error("Expected different instances after cache expiration")
	}

	// Cache should have one item (the new one)
	if testCache.Size() != 1 {
		t.Errorf("Expected cache size 1 after expiration and reopen, got %d", testCache.Size())
	}
}

func BenchmarkOpenRepository_WithoutCache(b *testing.B) {
	repoPath := createTestRepo(&testing.T{})
	defer func() { _ = os.RemoveAll(repoPath) }()
	addFileToRepo(&testing.T{}, repoPath, "README.md", "# Benchmark Test")
	commitRepo(&testing.T{}, repoPath, "Initial commit")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		repo, err := OpenRepository(repoPath)
		if err != nil {
			b.Fatalf("Failed to open repository: %v", err)
		}
		_ = repo.Close()
	}
}

func BenchmarkOpenRepository_WithCache(b *testing.B) {
	// Save and restore original global cache
	originalCache := GetGlobalCache()
	defer SetGlobalCache(originalCache)

	// Set up test cache
	testCache := NewRepositoryCache(DefaultCacheConfig())
	SetGlobalCache(testCache)

	repoPath := createTestRepo(&testing.T{})
	defer func() { _ = os.RemoveAll(repoPath) }()
	addFileToRepo(&testing.T{}, repoPath, "README.md", "# Benchmark Test")
	commitRepo(&testing.T{}, repoPath, "Initial commit")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		repo, err := OpenRepositoryWithCache(repoPath)
		if err != nil {
			b.Fatalf("Failed to open repository: %v", err)
		}
		_ = repo.Close()
	}
}
