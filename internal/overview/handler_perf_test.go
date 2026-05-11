package overview

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// setupLargeRepository creates a repository with at least 100MB of content.
// Returns the commit hash of the created HEAD commit so callers can use a fixed ref (not HEAD) in perf comparisons.
func setupLargeRepository(t *testing.T, repoPath string, targetSizeMB int) plumbing.Hash {
	// Initialize bare repository
	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	// Create multiple large files to reach target size
	// Each file will be approximately 10MB
	fileSize := 10 * 1024 * 1024 // 10MB per file
	numFiles := (targetSizeMB / 10) + 1

	t.Logf("Creating repository with %d files of ~10MB each (target: %dMB)", numFiles, targetSizeMB)

	// Create a large blob content (10MB of data)
	largeContent := make([]byte, fileSize)
	// Use pseudo-random data so it does not compress too well on disk (we want a truly large repo on filesystem).
	// Fixed seed to keep tests deterministic.
	rng := rand.New(rand.NewSource(1))
	_, err = rng.Read(largeContent)
	require.NoError(t, err)

	// Create tree entries for all files
	treeEntries := make([]object.TreeEntry, 0, numFiles)

	for i := 0; i < numFiles; i++ {
		// Ensure each file blob is unique; otherwise Git will store a single blob object reused by all paths.
		binary.LittleEndian.PutUint64(largeContent[:8], uint64(i))

		// Create blob
		blob := repo.Storer.NewEncodedObject()
		blob.SetType(plumbing.BlobObject)
		blob.SetSize(int64(len(largeContent)))
		writer, err := blob.Writer()
		require.NoError(t, err)
		_, err = writer.Write(largeContent)
		require.NoError(t, err)
		require.NoError(t, writer.Close())

		blobHash, err := repo.Storer.SetEncodedObject(blob)
		require.NoError(t, err)

		// Add to tree
		fileName := fmt.Sprintf("large_file_%d.bin", i)
		treeEntries = append(treeEntries, object.TreeEntry{
			Name: fileName,
			Mode: filemode.Regular,
			Hash: blobHash,
		})
	}

	// go-git requires tree entries to be sorted for encoding
	sort.Slice(treeEntries, func(i, j int) bool { return treeEntries[i].Name < treeEntries[j].Name })

	// Create tree with all files
	tree := &object.Tree{
		Entries: treeEntries,
	}

	treeObj := repo.Storer.NewEncodedObject()
	err = tree.Encode(treeObj)
	require.NoError(t, err)

	treeHash, err := repo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)

	// Create initial commit
	commit := &object.Commit{
		Message: fmt.Sprintf("Initial commit with %d large files", numFiles),
		Author: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
		Committer: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
		TreeHash: treeHash,
	}

	commitObj := repo.Storer.NewEncodedObject()
	err = commit.Encode(commitObj)
	require.NoError(t, err)

	commitHash, err := repo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	// Create and update master branch reference
	masterRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/master"), commitHash)
	err = repo.Storer.SetReference(masterRef)
	require.NoError(t, err)

	// Update HEAD to be a symbolic reference to master
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/master"))
	err = repo.Storer.SetReference(headRef)
	require.NoError(t, err)

	// Verify repository size
	repoSize := getRepositorySize(t, repoPath)
	t.Logf("Repository created with size: %.2f MB", float64(repoSize)/(1024*1024))
	require.GreaterOrEqual(t, repoSize, int64(targetSizeMB*1024*1024), "Repository should be at least %dMB", targetSizeMB)

	return commitHash
}

// getRepositorySize calculates the total size of a repository directory
func getRepositorySize(t *testing.T, repoPath string) int64 {
	var size int64
	err := filepath.Walk(repoPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	require.NoError(t, err)
	return size
}

// setupTestHandlerForPerf creates a test handler for performance testing
func setupTestHandlerForPerf(t *testing.T) (*RepositoryOverviewHandler, string, func()) {
	gin.SetMode(gin.TestMode)

	// Create temporary directory for test repositories
	tempDir, err := os.MkdirTemp("", "overview-perf-test-*")
	require.NoError(t, err)

	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "warn", // Reduce logging for performance tests
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
		Events: config.EventsConfig{
			Enabled: false, // Disable events for testing
		},
	}

	handler, err := NewRepositoryOverviewHandler(cfg)
	require.NoError(t, err)

	cleanup := func() {
		_ = os.RemoveAll(tempDir)
	}

	return handler, tempDir, cleanup
}

// TestOverviewAPI_Performance_LargeRepository tests overview API performance with 100MB+ repository
func TestOverviewAPI_Performance_LargeRepository(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping performance test in short mode")
	}

	handler, tempDir, cleanup := setupTestHandlerForPerf(t)
	defer cleanup()

	// Create large repository (100MB)
	owner := "testowner"
	repo := "large-repo"
	repoPath := filepath.Join(tempDir, owner, fmt.Sprintf("%s.git", repo))
	err := os.MkdirAll(filepath.Dir(repoPath), 0755)
	require.NoError(t, err)

	t.Log("Creating 100MB repository (this may take a while)...")
	startSetup := time.Now()
	commitHash := setupLargeRepository(t, repoPath, 100)
	setupDuration := time.Since(startSetup)
	t.Logf("Repository setup completed in: %v", setupDuration)

	// Setup router
	router := gin.New()
	router.GET("/repos/:owner/:repo/overview", adapt(handler.HandleGetRepositoryOverview))

	// Use a fixed ref (commit SHA) for perf comparisons; HEAD is allowed in prod but moves over time.
	fixedRef := commitHash.String()

	// Test overview API without optional features (fastest)
	t.Run("Overview_WithoutOptionalFeatures", func(t *testing.T) {
		req := httptest.NewRequest("GET", fmt.Sprintf("/repos/%s/%s/overview?ref=%s", owner, repo, fixedRef), nil)
		w := httptest.NewRecorder()

		start := time.Now()
		router.ServeHTTP(w, req)
		duration := time.Since(start)

		assert.Equal(t, http.StatusOK, w.Code)
		t.Logf("Overview API (without optional features) took: %v", duration)

		var response RepositoryOverviewResponse
		err := json.Unmarshal(w.Body.Bytes(), &response)
		require.NoError(t, err)
		assert.True(t, response.Success)
		assert.Greater(t, response.Count.Commits, 0)

		// Performance assertion: should complete in reasonable time
		// For 100MB repo, expect under 5 seconds (adjust based on your requirements)
		assert.Less(t, duration, 10*time.Second, "Overview API should complete in reasonable time")
	})

	// Test overview API with commits (slower)
	t.Run("Overview_WithCommits", func(t *testing.T) {
		req := httptest.NewRequest("GET", fmt.Sprintf("/repos/%s/%s/overview?ref=%s&include_commits=true", owner, repo, fixedRef), nil)
		w := httptest.NewRecorder()

		start := time.Now()
		router.ServeHTTP(w, req)
		duration := time.Since(start)

		assert.Equal(t, http.StatusOK, w.Code)
		t.Logf("Overview API (with commits) took: %v", duration)

		var response RepositoryOverviewResponse
		err := json.Unmarshal(w.Body.Bytes(), &response)
		require.NoError(t, err)
		assert.True(t, response.Success)
	})

	// Test overview API with languages (slowest)
	t.Run("Overview_WithLanguages", func(t *testing.T) {
		req := httptest.NewRequest("GET", fmt.Sprintf("/repos/%s/%s/overview?ref=%s&include_languages=true", owner, repo, fixedRef), nil)
		w := httptest.NewRecorder()

		start := time.Now()
		router.ServeHTTP(w, req)
		duration := time.Since(start)

		assert.Equal(t, http.StatusOK, w.Code)
		t.Logf("Overview API (with languages) took: %v", duration)

		var response RepositoryOverviewResponse
		err := json.Unmarshal(w.Body.Bytes(), &response)
		require.NoError(t, err)
		assert.True(t, response.Success)
	})

	// Test overview API with all features
	t.Run("Overview_WithAllFeatures", func(t *testing.T) {
		req := httptest.NewRequest("GET", fmt.Sprintf("/repos/%s/%s/overview?ref=%s&include_commits=true&include_languages=true", owner, repo, fixedRef), nil)
		w := httptest.NewRecorder()

		start := time.Now()
		router.ServeHTTP(w, req)
		duration := time.Since(start)

		assert.Equal(t, http.StatusOK, w.Code)
		t.Logf("Overview API (with all features) took: %v", duration)

		var response RepositoryOverviewResponse
		err := json.Unmarshal(w.Body.Bytes(), &response)
		require.NoError(t, err)
		assert.True(t, response.Success)
	})
}

func Test_TreeWithCommitOverview_LargeRepository_Returns200(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping performance test in short mode")
	}

	handler, tempDir, cleanup := setupTestHandlerForPerf(t)
	defer cleanup()

	owner := "testowner"
	repo := "large-repo"
	repoPath := filepath.Join(tempDir, owner, fmt.Sprintf("%s.git", repo))
	err := os.MkdirAll(filepath.Dir(repoPath), 0755)
	require.NoError(t, err)

	commitHash := setupLargeRepository(t, repoPath, 100)
	fixedRef := commitHash.String()

	router := gin.New()
	router.GET("/repos/:owner/:repo/overview/tree/commits", adaptQuery(handler.HandleGetTreeWithCommitOverview))

	req := httptest.NewRequest("GET", fmt.Sprintf("/repos/%s/%s/overview/tree/commits?ref=%s&path=", owner, repo, fixedRef), nil)
	w := httptest.NewRecorder()

	start := time.Now()
	router.ServeHTTP(w, req)
	duration := time.Since(start)

	assert.Equal(t, http.StatusOK, w.Code, "status=%d body=%s", w.Code, w.Body.String())
	t.Logf("TreeWithCommitOverview API took: %v", duration)

	var response TreeWithCommitOverviewResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.True(t, response.Success)
	require.Equal(t, owner+"/"+repo, response.Repository)
}

func createCHeavyRepoForLatestCommitsPerf(t *testing.T, repoPath string, pendingN int, commits int, noiseFiles int) (plumbing.Hash, []string) {
	t.Helper()
	require.GreaterOrEqual(t, pendingN, 1)
	require.GreaterOrEqual(t, commits, 2)
	require.GreaterOrEqual(t, noiseFiles, 1)

	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	// Root pending files.
	rootPaths := make([]string, 0, pendingN)
	rootEntries := make([]object.TreeEntry, 0, pendingN+1)
	for i := 0; i < pendingN; i++ {
		name := fmt.Sprintf("p%05d.txt", i)
		rootPaths = append(rootPaths, name)

		content := make([]byte, 64)
		binary.LittleEndian.PutUint64(content[:8], uint64(i))

		blob := repo.Storer.NewEncodedObject()
		blob.SetType(plumbing.BlobObject)
		blob.SetSize(int64(len(content)))
		w, err := blob.Writer()
		require.NoError(t, err)
		_, err = w.Write(content)
		require.NoError(t, err)
		require.NoError(t, w.Close())

		blobHash, err := repo.Storer.SetEncodedObject(blob)
		require.NoError(t, err)
		rootEntries = append(rootEntries, object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: blobHash})
	}

	// Noise subtree (to create diffs without touching pending paths).
	noiseEntries := make([]object.TreeEntry, 0, noiseFiles)
	for i := 0; i < noiseFiles; i++ {
		name := fmt.Sprintf("n%05d.bin", i)
		content := make([]byte, 256)
		binary.LittleEndian.PutUint64(content[:8], uint64(i))

		blob := repo.Storer.NewEncodedObject()
		blob.SetType(plumbing.BlobObject)
		blob.SetSize(int64(len(content)))
		w, err := blob.Writer()
		require.NoError(t, err)
		_, err = w.Write(content)
		require.NoError(t, err)
		require.NoError(t, w.Close())

		blobHash, err := repo.Storer.SetEncodedObject(blob)
		require.NoError(t, err)
		noiseEntries = append(noiseEntries, object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: blobHash})
	}
	sort.Slice(noiseEntries, func(i, j int) bool { return noiseEntries[i].Name < noiseEntries[j].Name })

	noiseTreeEnc := repo.Storer.NewEncodedObject()
	require.NoError(t, (&object.Tree{Entries: noiseEntries}).Encode(noiseTreeEnc))
	noiseTreeHash, err := repo.Storer.SetEncodedObject(noiseTreeEnc)
	require.NoError(t, err)

	rootEntries = append(rootEntries, object.TreeEntry{Name: "noise", Mode: filemode.Dir, Hash: noiseTreeHash})
	sort.Slice(rootEntries, func(i, j int) bool { return rootEntries[i].Name < rootEntries[j].Name })

	rootTreeEnc := repo.Storer.NewEncodedObject()
	require.NoError(t, (&object.Tree{Entries: rootEntries}).Encode(rootTreeEnc))
	rootTreeHash, err := repo.Storer.SetEncodedObject(rootTreeEnc)
	require.NoError(t, err)

	when := time.Unix(1700000000, 0).UTC()

	// Commit 0: create everything.
	var parent plumbing.Hash
	{
		commit := &object.Commit{
			Message:   "init",
			Author:    object.Signature{Name: "perf", Email: "perf@example.com", When: when},
			Committer: object.Signature{Name: "perf", Email: "perf@example.com", When: when},
			TreeHash:  rootTreeHash,
		}
		commitEnc := repo.Storer.NewEncodedObject()
		require.NoError(t, commit.Encode(commitEnc))
		commitHash, err := repo.Storer.SetEncodedObject(commitEnc)
		require.NoError(t, err)
		parent = commitHash
	}

	// Commits 1..C-1: mutate noise tree only (touch 1 noise blob per commit).
	for ci := 1; ci < commits; ci++ {
		idx := (ci - 1) % len(noiseEntries)
		content := make([]byte, 256)
		binary.LittleEndian.PutUint64(content[:8], uint64(ci))
		binary.LittleEndian.PutUint64(content[8:16], uint64(idx))

		blob := repo.Storer.NewEncodedObject()
		blob.SetType(plumbing.BlobObject)
		blob.SetSize(int64(len(content)))
		w, err := blob.Writer()
		require.NoError(t, err)
		_, err = w.Write(content)
		require.NoError(t, err)
		require.NoError(t, w.Close())

		blobHash, err := repo.Storer.SetEncodedObject(blob)
		require.NoError(t, err)
		noiseEntries[idx].Hash = blobHash

		noiseTreeEnc := repo.Storer.NewEncodedObject()
		require.NoError(t, (&object.Tree{Entries: noiseEntries}).Encode(noiseTreeEnc))
		noiseTreeHash, err := repo.Storer.SetEncodedObject(noiseTreeEnc)
		require.NoError(t, err)

		for i := range rootEntries {
			if rootEntries[i].Name == "noise" {
				rootEntries[i].Hash = noiseTreeHash
				break
			}
		}
		rootTreeEnc := repo.Storer.NewEncodedObject()
		require.NoError(t, (&object.Tree{Entries: rootEntries}).Encode(rootTreeEnc))
		rootTreeHash, err = repo.Storer.SetEncodedObject(rootTreeEnc)
		require.NoError(t, err)

		commit := &object.Commit{
			Message:      fmt.Sprintf("commit %d", ci),
			Author:       object.Signature{Name: "perf", Email: "perf@example.com", When: when.Add(time.Duration(ci) * time.Second)},
			Committer:    object.Signature{Name: "perf", Email: "perf@example.com", When: when.Add(time.Duration(ci) * time.Second)},
			TreeHash:     rootTreeHash,
			ParentHashes: []plumbing.Hash{parent},
		}
		commitEnc := repo.Storer.NewEncodedObject()
		require.NoError(t, commit.Encode(commitEnc))
		commitHash, err := repo.Storer.SetEncodedObject(commitEnc)
		require.NoError(t, err)
		parent = commitHash
	}

	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/master"), parent)))
	require.NoError(t, repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/master"))))

	return parent, rootPaths
}

func durationPercentile(values []time.Duration, p float64) time.Duration {
	// nearest-rank percentile
	sorted := make([]time.Duration, len(values))
	copy(sorted, values)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(float64(len(sorted))*p+0.999999) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func Test_LatestCommitsBatch_Performance_TimeBudget50ms_P99Under1500ms(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping performance test in short mode")
	}
	// Perf test: heavy I/O + CPU; do not run in parallel.

	handler, tempDir, cleanup := setupTestHandlerForPerf(t)
	defer cleanup()

	owner := "testowner"
	repo := "latest-commits-c-heavy"
	repoPath := filepath.Join(tempDir, owner, fmt.Sprintf("%s.git", repo))
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	headHash, rootPaths := createCHeavyRepoForLatestCommitsPerf(t, repoPath, 200, 2000, 10)

	t.Setenv("OVERVIEW_LATEST_COMMITS_TIME_BUDGET_MS", "50")
	t.Setenv("OVERVIEW_LATEST_COMMITS_MAX_COMMITS", "1000000")

	router := gin.New()
	router.POST("/repos/:owner/:repo/overview/latest-commits", adapt(handler.HandlePostLatestCommitsBatch))

	reqBody := LatestCommitsBatchRequest{
		Ref:     headHash.String(),
		DirPath: "",
		Paths:   rootPaths,
	}
	payload, err := json.Marshal(reqBody)
	require.NoError(t, err)

	const runs = 30
	latencies := make([]time.Duration, 0, runs)
	for i := 0; i < runs; i++ {
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/repos/%s/%s/overview/latest-commits", owner, repo), bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		start := time.Now()
		router.ServeHTTP(w, req)
		lat := time.Since(start)
		latencies = append(latencies, lat)

		require.Equal(t, http.StatusOK, w.Code, "status=%d body=%s", w.Code, w.Body.String())
		var resp LatestCommitsBatchResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.True(t, resp.Success)
		require.True(t, resp.Meta.Incomplete)
	}

	p50 := durationPercentile(latencies, 0.50)
	p95 := durationPercentile(latencies, 0.95)
	p99 := durationPercentile(latencies, 0.99)
	t.Logf("latest-commits timeBudget=50ms warm runs=%d p50=%s p95=%s p99=%s", runs, p50, p95, p99)

	require.LessOrEqual(t, p95, 800*time.Millisecond)
	require.LessOrEqual(t, p99, 1500*time.Millisecond)
}

// TestOverviewAPI_Performance_CommitCounting tests commit counting performance
func TestOverviewAPI_Performance_CommitCounting(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping performance test in short mode")
	}

	handler, tempDir, cleanup := setupTestHandlerForPerf(t)
	defer cleanup()

	// Create repository with many commits
	owner := "testowner"
	repo := "many-commits-repo"
	repoPath := filepath.Join(tempDir, owner, fmt.Sprintf("%s.git", repo))
	err := os.MkdirAll(filepath.Dir(repoPath), 0755)
	require.NoError(t, err)

	// Initialize repository
	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	// Create many commits (100 commits)
	numCommits := 100
	t.Logf("Creating repository with %d commits...", numCommits)

	// Create initial commit
	blob := gitRepo.Storer.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	blob.SetSize(100)
	writer, err := blob.Writer()
	require.NoError(t, err)
	_, err = writer.Write(bytes.Repeat([]byte("A"), 100))
	require.NoError(t, err)
	_ = writer.Close()

	blobHash, err := gitRepo.Storer.SetEncodedObject(blob)
	require.NoError(t, err)

	var lastCommitHash plumbing.Hash
	for i := 0; i < numCommits; i++ {
		// Create tree
		tree := &object.Tree{
			Entries: []object.TreeEntry{
				{
					Name: fmt.Sprintf("file_%d.txt", i),
					Mode: filemode.Regular,
					Hash: blobHash,
				},
			},
		}

		treeObj := gitRepo.Storer.NewEncodedObject()
		err = tree.Encode(treeObj)
		require.NoError(t, err)

		treeHash, err := gitRepo.Storer.SetEncodedObject(treeObj)
		require.NoError(t, err)

		// Create commit
		var parentHashes []plumbing.Hash
		if !lastCommitHash.IsZero() {
			parentHashes = []plumbing.Hash{lastCommitHash}
		}

		commit := &object.Commit{
			Message:      fmt.Sprintf("Commit %d", i),
			Author:       object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
			Committer:    object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
			TreeHash:     treeHash,
			ParentHashes: parentHashes,
		}

		commitObj := gitRepo.Storer.NewEncodedObject()
		err = commit.Encode(commitObj)
		require.NoError(t, err)

		commitHash, err := gitRepo.Storer.SetEncodedObject(commitObj)
		require.NoError(t, err)

		lastCommitHash = commitHash
	}

	// Update master branch
	masterRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/master"), lastCommitHash)
	err = gitRepo.Storer.SetReference(masterRef)
	require.NoError(t, err)

	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/master"))
	err = gitRepo.Storer.SetReference(headRef)
	require.NoError(t, err)

	// Setup router
	router := gin.New()
	router.GET("/repos/:owner/:repo/overview", adapt(handler.HandleGetRepositoryOverview))

	// Test commit counting performance
	req := httptest.NewRequest("GET", fmt.Sprintf("/repos/%s/%s/overview", owner, repo), nil)
	w := httptest.NewRecorder()

	start := time.Now()
	router.ServeHTTP(w, req)
	duration := time.Since(start)

	assert.Equal(t, http.StatusOK, w.Code)
	t.Logf("Overview API with %d commits took: %v", numCommits, duration)

	var response RepositoryOverviewResponse
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)
	assert.True(t, response.Success)
	assert.Equal(t, numCommits, response.Count.Commits, "Should count all commits correctly")

	// Performance assertion: counting 100 commits should be fast
	assert.Less(t, duration, 2*time.Second, "Commit counting should be fast even with many commits")
}

// BenchmarkOverviewAPI benchmarks the overview API performance
func BenchmarkOverviewAPI(b *testing.B) {
	handler, tempDir, cleanup := setupTestHandlerForPerf(&testing.T{})
	defer cleanup()

	// Create repository
	owner := "testowner"
	repo := "bench-repo"
	repoPath := filepath.Join(tempDir, owner, fmt.Sprintf("%s.git", repo))
	err := os.MkdirAll(filepath.Dir(repoPath), 0755)
	require.NoError(&testing.T{}, err)

	// Create a smaller repository for benchmarking (10MB)
	setupLargeRepository(&testing.T{}, repoPath, 10)

	// Setup router
	router := gin.New()
	router.GET("/repos/:owner/:repo/overview", adapt(handler.HandleGetRepositoryOverview))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", fmt.Sprintf("/repos/%s/%s/overview", owner, repo), nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}
}
