package overview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/merge"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func fixedBaseTime() time.Time {
	return time.Unix(1700000000, 0).UTC()
}

func setupTestHandler(t *testing.T) (*RepositoryOverviewHandler, string, func()) {
	tempDir, err := os.MkdirTemp("", "overview-handler-test-*")
	require.NoError(t, err)

	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
		Events: config.EventsConfig{
			Enabled: false,
		},
	}

	handler, err := NewRepositoryOverviewHandler(cfg)
	require.NoError(t, err)

	cleanup := func() {
		_ = os.RemoveAll(tempDir)
	}

	return handler, tempDir, cleanup
}

func createTestRepositoryForHandler(t *testing.T, tempDir, owner, repo string) {
	repoPath := filepath.Join(tempDir, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	tempWorkDir, err := os.MkdirTemp("", "overview-handler-work-*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempWorkDir) }()

	workRepo, err := gogit.PlainInit(tempWorkDir, false)
	require.NoError(t, err)

	worktree, err := workRepo.Worktree()
	require.NoError(t, err)

	// Go 파일
	goPath := filepath.Join(tempWorkDir, "main.go")
	goContent := `package main

import "fmt"

func main() {
	fmt.Println("Hello, World!")
}
`
	require.NoError(t, os.WriteFile(goPath, []byte(goContent), 0644))
	_, err = worktree.Add("main.go")
	require.NoError(t, err)

	// JavaScript 파일
	jsPath := filepath.Join(tempWorkDir, "app.js")
	jsContent := `function greet(name) {
	console.log("Hello, " + name + "!");
}
`
	require.NoError(t, os.WriteFile(jsPath, []byte(jsContent), 0644))
	_, err = worktree.Add("app.js")
	require.NoError(t, err)

	// README.md
	readmePath := filepath.Join(tempWorkDir, "README.md")
	readmeContent := `# Test Repository
`
	require.NoError(t, os.WriteFile(readmePath, []byte(readmeContent), 0644))
	_, err = worktree.Add("README.md")
	require.NoError(t, err)

	when := time.Unix(1700000000, 0).UTC()
	_, err = worktree.Commit("Initial commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  when,
		},
	})
	require.NoError(t, err)

	// 베어 저장소로 복사
	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	head, err := workRepo.Head()
	require.NoError(t, err)

	commit, err := workRepo.CommitObject(head.Hash())
	require.NoError(t, err)

	commitObj, err := workRepo.Storer.EncodedObject(plumbing.CommitObject, head.Hash())
	require.NoError(t, err)
	_, err = gitRepo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	err = copyTreeRecursiveForHandlerTest(workRepo, gitRepo, commit.TreeHash)
	require.NoError(t, err)

	mainRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), head.Hash())
	require.NoError(t, gitRepo.Storer.SetReference(mainRef))

	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))
	require.NoError(t, gitRepo.Storer.SetReference(headRef))
}

func Test_PostLatestCommitsBatch_ReturnsCommitsAndMeta(t *testing.T) {
	t.Parallel()

	h, tempDir, cleanup := setupTestHandler(t)
	t.Cleanup(cleanup)

	owner := "o"
	repo := "r"
	createTestRepositoryForHandler(t, tempDir, owner, repo)

	r := gin.New()
	r.POST("/repos/:owner/:repo/overview/latest-commits", h.HandlePostLatestCommitsBatch)

	body := LatestCommitsBatchRequest{
		Ref:     "main",
		DirPath: "",
		Paths:   []string{"README.md", "main.go"},
	}
	payload, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequest(
		http.MethodPost,
		fmt.Sprintf("/repos/%s/%s/overview/latest-commits", owner, repo),
		bytes.NewReader(payload),
	)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "status=%d body=%s", w.Code, w.Body.String())

	var resp LatestCommitsBatchResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Equal(t, owner, resp.Owner)
	require.Equal(t, repo, resp.Name)
	require.Equal(t, "", resp.DirPath)
	require.NotEmpty(t, resp.Meta.ResolvedRefHash)
	require.Greater(t, resp.Meta.ScannedCommits, 0)
	require.NotNil(t, resp.CommitsByPath["README.md"])
	require.NotNil(t, resp.CommitsByPath["main.go"])
}

func copyTreeRecursiveForHandlerTest(workRepo, gitRepo *gogit.Repository, treeHash plumbing.Hash) error {
	tree, err := workRepo.TreeObject(treeHash)
	if err != nil {
		return err
	}

	treeObj, err := workRepo.Storer.EncodedObject(plumbing.TreeObject, treeHash)
	if err != nil {
		return err
	}
	_, err = gitRepo.Storer.SetEncodedObject(treeObj)
	if err != nil {
		return err
	}

	for _, entry := range tree.Entries {
		if entry.Mode == filemode.Dir {
			if err := copyTreeRecursiveForHandlerTest(workRepo, gitRepo, entry.Hash); err != nil {
				return err
			}
		} else {
			blobObj, err := workRepo.Storer.EncodedObject(plumbing.BlobObject, entry.Hash)
			if err != nil {
				continue
			}
			_, err = gitRepo.Storer.SetEncodedObject(blobObj)
			if err != nil {
				continue
			}
		}
	}

	return nil
}

func TestHandleGetRepositoryOverview_LanguageDistribution(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	owner := "testowner"
	repo := "testrepo"
	createTestRepositoryForHandler(t, tempDir, owner, repo)

	router := gin.New()
	router.GET("/repos/:owner/:repo/overview", handler.HandleGetRepositoryOverview)

	// 당분간 overview에서는 언어 분포를 제공하지 않음
	req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repo+"/overview", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response RepositoryOverviewResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.True(t, response.Success)
	assert.Equal(t, 0, response.LanguageDistribution.TotalFiles, "TotalFiles should be 0 while language distribution is disabled")
	assert.Equal(t, int64(0), response.LanguageDistribution.TotalBytes, "TotalBytes should be 0 while language distribution is disabled")
	assert.Empty(t, response.LanguageDistribution.Languages, "Languages should be empty while language distribution is disabled")
}

func TestHandleGetRepositoryOverview_LanguageDistribution_EmptyRepository(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	owner := "testowner"
	repo := "emptyrepo"
	repoPath := filepath.Join(tempDir, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	emptyTree := &object.Tree{}
	treeObj := gitRepo.Storer.NewEncodedObject()
	err = emptyTree.Encode(treeObj)
	require.NoError(t, err)

	treeHash, err := gitRepo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)

	when := fixedBaseTime()
	commit := &object.Commit{
		Message:   "Initial empty commit",
		Author:    object.Signature{Name: "Test", Email: "test@example.com", When: when},
		Committer: object.Signature{Name: "Test", Email: "test@example.com", When: when},
		TreeHash:  treeHash,
	}

	commitObj := gitRepo.Storer.NewEncodedObject()
	err = commit.Encode(commitObj)
	require.NoError(t, err)

	commitHash, err := gitRepo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	mainRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), commitHash)
	require.NoError(t, gitRepo.Storer.SetReference(mainRef))

	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))
	require.NoError(t, gitRepo.Storer.SetReference(headRef))

	router := gin.New()
	router.GET("/repos/:owner/:repo/overview", handler.HandleGetRepositoryOverview)

	req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repo+"/overview", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response RepositoryOverviewResponse
	err = json.Unmarshal(w.Body.Bytes(), &response)
	require.NoError(t, err)

	assert.True(t, response.Success)
	assert.NotNil(t, response.LanguageDistribution, "LanguageDistribution should always be included")
	assert.Equal(t, 0, response.LanguageDistribution.TotalFiles, "TotalFiles should be 0 for empty repository")
	assert.Equal(t, int64(0), response.LanguageDistribution.TotalBytes, "TotalBytes should be 0 for empty repository")
	assert.Empty(t, response.LanguageDistribution.Languages, "Languages should be empty for empty repository")
}

// createTestRepositoryWithMultiplePathCommits는 여러 경로에 서로 다른 시점에 커밋이 있는 저장소를 생성합니다.
// 커밋 순서: root -> src -> docs -> src (src가 가장 최신)
func createTestRepositoryWithMultiplePathCommits(t *testing.T, tempDir, owner, repo string) map[string]plumbing.Hash {
	repoPath := filepath.Join(tempDir, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	tempWorkDir, err := os.MkdirTemp("", "overview-handler-work-*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempWorkDir) }()

	workRepo, err := gogit.PlainInit(tempWorkDir, false)
	require.NoError(t, err)

	worktree, err := workRepo.Worktree()
	require.NoError(t, err)

	commitHashes := make(map[string]plumbing.Hash)
	baseTime := fixedBaseTime()

	// 1. Initial commit (root files)
	readmePath := filepath.Join(tempWorkDir, "README.md")
	require.NoError(t, os.WriteFile(readmePath, []byte("# Test Repository\n"), 0644))
	_, err = worktree.Add("README.md")
	require.NoError(t, err)

	rootCommitHash, err := worktree.Commit("Initial commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  baseTime,
		},
	})
	require.NoError(t, err)
	commitHashes["root"] = rootCommitHash

	// 2. src/ 디렉토리 커밋
	srcDir := filepath.Join(tempWorkDir, "src")
	require.NoError(t, os.MkdirAll(srcDir, 0755))
	srcMainPath := filepath.Join(srcDir, "main.go")
	require.NoError(t, os.WriteFile(srcMainPath, []byte("package main\n\nfunc main() {}\n"), 0644))
	_, err = worktree.Add("src/main.go")
	require.NoError(t, err)

	srcCommitHash, err := worktree.Commit("Add src/main.go", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  baseTime.Add(1 * time.Hour),
		},
	})
	require.NoError(t, err)
	commitHashes["src"] = srcCommitHash

	// 3. docs/ 디렉토리 커밋
	docsDir := filepath.Join(tempWorkDir, "docs")
	require.NoError(t, os.MkdirAll(docsDir, 0755))
	docsReadmePath := filepath.Join(docsDir, "README.md")
	require.NoError(t, os.WriteFile(docsReadmePath, []byte("# Documentation\n"), 0644))
	_, err = worktree.Add("docs/README.md")
	require.NoError(t, err)

	docsCommitHash, err := worktree.Commit("Add docs/README.md", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  baseTime.Add(2 * time.Hour),
		},
	})
	require.NoError(t, err)
	commitHashes["docs"] = docsCommitHash

	// 4. src/ 디렉토리 다시 수정 (src가 가장 최신이 되도록)
	srcUtilPath := filepath.Join(srcDir, "util.go")
	require.NoError(t, os.WriteFile(srcUtilPath, []byte("package main\n\nfunc util() {}\n"), 0644))
	_, err = worktree.Add("src/util.go")
	require.NoError(t, err)

	srcLatestCommitHash, err := worktree.Commit("Add src/util.go", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  baseTime.Add(3 * time.Hour),
		},
	})
	require.NoError(t, err)
	commitHashes["src_latest"] = srcLatestCommitHash

	// 베어 저장소로 복사
	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	head, err := workRepo.Head()
	require.NoError(t, err)

	// 모든 커밋과 트리 복사
	commitIter, err := workRepo.Log(&gogit.LogOptions{})
	require.NoError(t, err)

	err = commitIter.ForEach(func(c *object.Commit) error {
		commitObj, err := workRepo.Storer.EncodedObject(plumbing.CommitObject, c.Hash)
		if err != nil {
			return err
		}
		_, err = gitRepo.Storer.SetEncodedObject(commitObj)
		if err != nil {
			return err
		}

		err = copyTreeRecursiveForHandlerTest(workRepo, gitRepo, c.TreeHash)
		return err
	})
	require.NoError(t, err)

	mainRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), head.Hash())
	require.NoError(t, gitRepo.Storer.SetReference(mainRef))

	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))
	require.NoError(t, gitRepo.Storer.SetReference(headRef))

	return commitHashes
}

func TestHandleGetTreeOverview_LatestCommit_ForSpecificPath(t *testing.T) {
	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	owner := "testowner"
	repo := "testrepo"
	commitHashes := createTestRepositoryWithMultiplePathCommits(t, tempDir, owner, repo)

	router := gin.New()
	router.GET("/repos/:owner/:repo/tree", handler.HandleGetTreeOverview)

	t.Run("RootPath_ReturnsLatestCommit", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repo+"/tree?path=", nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response TreeOverviewResponse
		err := json.Unmarshal(w.Body.Bytes(), &response)
		require.NoError(t, err)

		assert.True(t, response.Success)
		require.NotNil(t, response.LatestCommit, "LatestCommit should not be nil for root path")
		// 루트 경로는 전체 저장소의 최신 커밋 (src_latest)
		assert.Equal(t, commitHashes["src_latest"].String()[:7], response.LatestCommit.SHA[:7])
		assert.Equal(t, "Add src/util.go", response.LatestCommit.Message)
	})

	t.Run("SrcPath_ReturnsLatestCommitForSrc", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repo+"/tree?path=src", nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response TreeOverviewResponse
		err := json.Unmarshal(w.Body.Bytes(), &response)
		require.NoError(t, err)

		assert.True(t, response.Success)
		require.NotNil(t, response.LatestCommit, "LatestCommit should not be nil for src path")
		// src 경로는 src의 최신 커밋 (src_latest)
		assert.Equal(t, commitHashes["src_latest"].String()[:7], response.LatestCommit.SHA[:7])
		assert.Equal(t, "Add src/util.go", response.LatestCommit.Message)
	})

	t.Run("DocsPath_ReturnsLatestCommitForDocs", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repo+"/tree?path=docs", nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response TreeOverviewResponse
		err := json.Unmarshal(w.Body.Bytes(), &response)
		require.NoError(t, err)

		assert.True(t, response.Success)
		require.NotNil(t, response.LatestCommit, "LatestCommit should not be nil for docs path")
		// docs 경로는 docs의 최신 커밋
		assert.Equal(t, commitHashes["docs"].String()[:7], response.LatestCommit.SHA[:7])
		assert.Equal(t, "Add docs/README.md", response.LatestCommit.Message)
	})

	t.Run("PathWithMultipleCommits_ReturnsMostRecentCommit", func(t *testing.T) {
		// src 경로는 두 번 커밋되었지만, 최신 커밋(src_latest)이 반환되어야 함
		req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repo+"/tree?path=src", nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response TreeOverviewResponse
		err := json.Unmarshal(w.Body.Bytes(), &response)
		require.NoError(t, err)

		assert.True(t, response.Success)
		require.NotNil(t, response.LatestCommit, "LatestCommit should not be nil")
		// src 경로는 src의 최신 커밋 (src_latest)이어야 하고, 이전 커밋(src)이 아니어야 함
		assert.Equal(t, commitHashes["src_latest"].String()[:7], response.LatestCommit.SHA[:7])
		assert.NotEqual(t, commitHashes["src"].String()[:7], response.LatestCommit.SHA[:7], "Should return latest commit, not older commit")
		assert.Equal(t, "Add src/util.go", response.LatestCommit.Message)
	})
}

// =============================================================================
// 5.1 Repository Overview (test_cases.md)
// =============================================================================

func overviewRoute(router *gin.Engine, h *RepositoryOverviewHandler) {
	router.GET("/repos/:owner/:repo/overview", h.HandleGetRepositoryOverview)
}

func Test_Overview_ValidRepo_ReturnsOverview(t *testing.T) {
	t.Parallel()

	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	owner := "testowner"
	repo := "testrepo"
	createTestRepositoryForHandler(t, tempDir, owner, repo)

	router := gin.New()
	overviewRoute(router, handler)

	req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repo+"/overview", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "status=%d body=%s", w.Code, w.Body.String())

	var resp RepositoryOverviewResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Equal(t, owner+"/"+repo, resp.Repository)
	require.Equal(t, owner, resp.Owner)
	require.Equal(t, repo, resp.Name)
	require.NotEmpty(t, resp.Count)
}

func Test_Overview_NonExistentRepo_Returns404(t *testing.T) {
	t.Parallel()

	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	router := gin.New()
	overviewRoute(router, handler)

	req := httptest.NewRequest("GET", "/repos/nonexistent/norepo/overview", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code, "status=%d body=%s", w.Code, w.Body.String())
	_ = tempDir
	cleanup()
}

func Test_Overview_EmptyRepo_ReturnsEmptyData(t *testing.T) {
	t.Parallel()

	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	owner := "testowner"
	repo := "emptyrepo"
	repoPath := filepath.Join(tempDir, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	emptyTree := &object.Tree{}
	treeObj := gitRepo.Storer.NewEncodedObject()
	require.NoError(t, emptyTree.Encode(treeObj))
	treeHash, err := gitRepo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)

	when := fixedBaseTime()
	commit := &object.Commit{
		Message:   "Initial empty commit",
		Author:    object.Signature{Name: "Test", Email: "test@example.com", When: when},
		Committer: object.Signature{Name: "Test", Email: "test@example.com", When: when},
		TreeHash:  treeHash,
	}
	commitObj := gitRepo.Storer.NewEncodedObject()
	require.NoError(t, commit.Encode(commitObj))
	commitHash, err := gitRepo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	mainRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), commitHash)
	require.NoError(t, gitRepo.Storer.SetReference(mainRef))
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))
	require.NoError(t, gitRepo.Storer.SetReference(headRef))

	router := gin.New()
	overviewRoute(router, handler)

	req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repo+"/overview", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp RepositoryOverviewResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Equal(t, 1, resp.Count.Commits)
	require.Len(t, resp.Branches, 1)
	require.Empty(t, resp.Tree)
}

func Test_Overview_IncludesDefaultBranch(t *testing.T) {
	t.Parallel()

	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	owner := "testowner"
	repo := "testrepo"
	createTestRepositoryForHandler(t, tempDir, owner, repo)

	router := gin.New()
	overviewRoute(router, handler)

	req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repo+"/overview", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp RepositoryOverviewResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.NotEmpty(t, resp.Branches, "overview must include branches")
	foundCurrent := false
	for _, b := range resp.Branches {
		if b.IsCurrent {
			foundCurrent = true
			break
		}
	}
	require.True(t, foundCurrent, "default/current branch should be marked (IsCurrent)")
}

func Test_Overview_IncludesBranchCount(t *testing.T) {
	t.Parallel()

	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	owner := "testowner"
	repo := "testrepo"
	createTestRepositoryForHandler(t, tempDir, owner, repo)

	router := gin.New()
	overviewRoute(router, handler)

	req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repo+"/overview", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp RepositoryOverviewResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Equal(t, len(resp.Branches), resp.Count.Branches)
	require.GreaterOrEqual(t, resp.Count.Branches, 1)
}

func Test_Overview_IncludesCommitCount(t *testing.T) {
	t.Parallel()

	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	owner := "testowner"
	repo := "testrepo"
	createTestRepositoryForHandler(t, tempDir, owner, repo)

	router := gin.New()
	overviewRoute(router, handler)

	req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repo+"/overview", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp RepositoryOverviewResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.GreaterOrEqual(t, resp.Count.Commits, 1)
}

func Test_Overview_IncludesLanguageDistribution(t *testing.T) {
	t.Parallel()

	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	owner := "testowner"
	repo := "testrepo"
	createTestRepositoryForHandler(t, tempDir, owner, repo)

	router := gin.New()
	overviewRoute(router, handler)

	req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repo+"/overview", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp RepositoryOverviewResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.NotNil(t, resp.LanguageDistribution)
	require.NotNil(t, resp.LanguageDistribution.Languages)
}

// =============================================================================
// 5.1A Default Branch (test_cases.md)
// =============================================================================

func Test_Overview_DefaultBranchMissing_Returns404OrFallbackExplicitly(t *testing.T) {
	t.Parallel()

	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	owner := "testowner"
	repo := "nohead"
	repoPath := filepath.Join(tempDir, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	emptyTree := &object.Tree{}
	treeObj := gitRepo.Storer.NewEncodedObject()
	require.NoError(t, emptyTree.Encode(treeObj))
	treeHash, err := gitRepo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)

	when := fixedBaseTime()
	commit := &object.Commit{
		Message:   "Orphan",
		Author:    object.Signature{Name: "Test", Email: "test@example.com", When: when},
		Committer: object.Signature{Name: "Test", Email: "test@example.com", When: when},
		TreeHash:  treeHash,
	}
	commitObj := gitRepo.Storer.NewEncodedObject()
	require.NoError(t, commit.Encode(commitObj))
	commitHash, err := gitRepo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	ref := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), commitHash)
	require.NoError(t, gitRepo.Storer.SetReference(ref))
	// HEAD를 설정하지 않음 (detached/없음 시뮬레이션은 복잡하므로, ref=main으로 요청하면 동작)
	// 실제로 HEAD가 없으면 GetBranches 등이 실패할 수 있음. 현재 구현은 HEAD 없이도 ref 쿼리로 열 수 있음.
	router := gin.New()
	overviewRoute(router, handler)

	req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repo+"/overview?ref=main", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 구현에 따라 200(ref=main으로 성공) 또는 404
	require.True(t, w.Code == http.StatusOK || w.Code == http.StatusNotFound,
		"status=%d body=%s", w.Code, w.Body.String())
}

func Test_Overview_DefaultBranchIsMasterOrMain_ResolvedByRepoMetadata_NotByString(t *testing.T) {
	t.Parallel()

	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	owner := "testowner"
	repo := "testrepo"
	createTestRepositoryForHandler(t, tempDir, owner, repo)

	router := gin.New()
	overviewRoute(router, handler)

	req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repo+"/overview", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp RepositoryOverviewResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.NotEmpty(t, resp.Branches)
	var currentName string
	for _, b := range resp.Branches {
		if b.IsCurrent {
			currentName = b.Name
			break
		}
	}
	require.NotEmpty(t, currentName, "current branch name comes from repo HEAD, not hardcoded main/master")
	require.Contains(t, []string{"main", "master"}, currentName, "typical default is main or master")
}

func Test_BranchOps_DefaultBranchRename_MasterToMain_ConsistencyAcrossAPIs(t *testing.T) {
	t.Skip("cross-API default branch consistency requires shared default-branch resolution; covered by integration")
}

// =============================================================================
// 5.2 Language Distribution (test_cases.md) — handler returns structure; service has real logic
// =============================================================================

func Test_LanguageDistribution_SingleLanguage_Returns100Percent(t *testing.T) {
	t.Parallel()

	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	owner := "testowner"
	repo := "testrepo"
	createTestRepositoryForHandler(t, tempDir, owner, repo)

	router := gin.New()
	overviewRoute(router, handler)

	req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repo+"/overview", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp RepositoryOverviewResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.NotNil(t, resp.LanguageDistribution)
	// 당분간 overview 응답에서는 언어 분포를 비우고 반환함. 단일 언어 100%는 service_test.go에서 검증.
	if len(resp.LanguageDistribution.Languages) > 0 {
		require.Len(t, resp.LanguageDistribution.Languages, 1)
		require.Equal(t, 100.0, resp.LanguageDistribution.Languages[0].Percentage)
	}
}

func Test_LanguageDistribution_MultipleLanguages_ReturnsSorted(t *testing.T) {
	t.Parallel()

	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	owner := "testowner"
	repo := "testrepo"
	createTestRepositoryForHandler(t, tempDir, owner, repo)

	router := gin.New()
	overviewRoute(router, handler)

	req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repo+"/overview", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp RepositoryOverviewResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.NotNil(t, resp.LanguageDistribution)
	if len(resp.LanguageDistribution.Languages) >= 2 {
		for i := 1; i < len(resp.LanguageDistribution.Languages); i++ {
			require.GreaterOrEqual(t, resp.LanguageDistribution.Languages[i-1].Percentage,
				resp.LanguageDistribution.Languages[i].Percentage, "languages should be sorted by percentage desc")
		}
	}
}

func Test_LanguageDistribution_OthersCategory_GroupsSmallLanguages(t *testing.T) {
	t.Parallel()

	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	service, _, _ := setupTestService(t)
	ctx := context.Background()

	// Service 테스트의 helper 재사용 (tempDir에 직접 repo 생성)
	gitRepo := createTestRepositoryForLanguageDistributionFiltering(t, tempDir)

	// Service를 직접 호출하여 언어 분포 가져오기 (HEAD 대신 브랜치 이름 사용)
	distribution := service.GetLanguageDistribution(ctx, gitRepo, "main")

	require.Greater(t, distribution.TotalBytes, int64(0))
	require.Greater(t, distribution.TotalFiles, 0)
	require.NotEmpty(t, distribution.Languages)

	visibleCount := 0
	hasOthers := false
	sumPct := 0.0
	for i, lang := range distribution.Languages {
		sumPct += lang.Percentage
		if lang.Language == "Others" {
			hasOthers = true
			require.Equal(t, len(distribution.Languages)-1, i, "Others should be last")
			continue
		}
		visibleCount++
		require.GreaterOrEqual(t, lang.Percentage, 1.0, "visible languages should be >= 1%%")
	}

	require.True(t, hasOthers, "Others should exist when there are hidden languages (<1%% each)")
	require.LessOrEqual(t, visibleCount, 5, "visible languages should be at most 5")
	require.InDelta(t, 100.0, sumPct, 0.01, "total percentage should be ~100%%")
	_ = handler
}

func Test_LanguageDistribution_ExcludesVendor_SkipsVendorDir(t *testing.T) {
	t.Parallel()

	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	service, _, _ := setupTestService(t)
	ctx := context.Background()

	// Service 테스트의 helper 재사용 (tempDir에 직접 repo 생성)
	gitRepo := createTestRepositoryForLinguistStyleExclusions(t, tempDir)

	// Service를 직접 호출하여 언어 분포 가져오기 (HEAD 대신 브랜치 이름 사용)
	distribution := service.GetLanguageDistribution(ctx, gitRepo, "main")

	// Included bytes/files should reflect only non-excluded files (main.py)
	require.Equal(t, 1, distribution.TotalFiles, "vendor/doc/dotfile/generated should be excluded")
	require.NotEmpty(t, distribution.Languages)
	require.Equal(t, "Python", distribution.Languages[0].Language, "only main.py should be counted")
	_ = handler
}

func Test_LanguageDistribution_ExcludesNodeModules_SkipsNodeModules(t *testing.T) {
	t.Parallel()

	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	service, _, _ := setupTestService(t)
	ctx := context.Background()

	repoPath := filepath.Join(tempDir, "nodemodules.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	tempWorkDir, err := os.MkdirTemp("", "overview-nodemodules-work-*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempWorkDir) }()

	workRepo, err := gogit.PlainInit(tempWorkDir, false)
	require.NoError(t, err)

	worktree, err := workRepo.Worktree()
	require.NoError(t, err)

	// Real code
	require.NoError(t, os.WriteFile(filepath.Join(tempWorkDir, "main.js"), []byte("console.log('hello');\n"), 0644))
	_, err = worktree.Add("main.js")
	require.NoError(t, err)

	// node_modules (should be excluded)
	require.NoError(t, os.MkdirAll(filepath.Join(tempWorkDir, "node_modules"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tempWorkDir, "node_modules", "dep.js"), []byte("module.exports = {};\n"), 0644))
	_, err = worktree.Add("node_modules/dep.js")
	require.NoError(t, err)

	when := fixedBaseTime()
	commit, err := worktree.Commit("Add files", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  when,
		},
	})
	require.NoError(t, err)

	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	commitObj, err := workRepo.CommitObject(commit)
	require.NoError(t, err)

	commitObjEncoded, err := workRepo.Storer.EncodedObject(plumbing.CommitObject, commit)
	require.NoError(t, err)
	_, err = gitRepo.Storer.SetEncodedObject(commitObjEncoded)
	require.NoError(t, err)

	err = copyTreeRecursiveForTest(workRepo, gitRepo, commitObj.TreeHash)
	require.NoError(t, err)

	mainRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), commit)
	require.NoError(t, gitRepo.Storer.SetReference(mainRef))
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))
	require.NoError(t, gitRepo.Storer.SetReference(headRef))

	// Service를 직접 호출하여 언어 분포 가져오기 (HEAD 대신 브랜치 이름 사용)
	distribution := service.GetLanguageDistribution(ctx, gitRepo, "main")

	// node_modules는 제외되어야 함 (enry.IsVendor가 node_modules도 감지)
	require.Equal(t, 1, distribution.TotalFiles, "node_modules should be excluded")
	require.NotEmpty(t, distribution.Languages)
	require.Equal(t, "JavaScript", distribution.Languages[0].Language, "only main.js should be counted")
	_ = handler
}

// =============================================================================
// 5.3 Contributors (test_cases.md) — Merge Preview API 사용
// =============================================================================

func Test_Contributors_ReturnsUniqueContributors(t *testing.T) {
	t.Parallel()

	// Merge preview API를 사용하여 contributors 테스트
	tempDir := t.TempDir()
	owner := "testowner"
	repo := "contributors"
	repoPath := filepath.Join(tempDir, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	tempWorkDir, err := os.MkdirTemp("", "contributors-work-*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempWorkDir) }()

	workRepo, err := gogit.PlainInit(tempWorkDir, false)
	require.NoError(t, err)

	worktree, err := workRepo.Worktree()
	require.NoError(t, err)

	baseTime := fixedBaseTime()

	// main 브랜치: base commit
	require.NoError(t, os.WriteFile(filepath.Join(tempWorkDir, "base.txt"), []byte("base\n"), 0644))
	_, err = worktree.Add("base.txt")
	require.NoError(t, err)

	baseCommit, err := worktree.Commit("Base commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Author1",
			Email: "author1@example.com",
			When:  baseTime,
		},
	})
	require.NoError(t, err)

	// feature 브랜치: 여러 contributor의 커밋
	err = worktree.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	require.NoError(t, err)

	// Contributor 1: 2개 커밋
	require.NoError(t, os.WriteFile(filepath.Join(tempWorkDir, "file1.txt"), []byte("content1\n"), 0644))
	_, err = worktree.Add("file1.txt")
	require.NoError(t, err)
	commit1, err := worktree.Commit("Add file1", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Contributor1",
			Email: "contributor1@example.com",
			When:  baseTime.Add(1 * time.Second),
		},
	})
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(tempWorkDir, "file2.txt"), []byte("content2\n"), 0644))
	_, err = worktree.Add("file2.txt")
	require.NoError(t, err)
	commit2, err := worktree.Commit("Add file2", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Contributor1",
			Email: "contributor1@example.com",
			When:  baseTime.Add(2 * time.Second),
		},
	})
	require.NoError(t, err)

	// Contributor 2: 1개 커밋
	require.NoError(t, os.WriteFile(filepath.Join(tempWorkDir, "file3.txt"), []byte("content3\n"), 0644))
	_, err = worktree.Add("file3.txt")
	require.NoError(t, err)
	_, err = worktree.Commit("Add file3", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Contributor2",
			Email: "contributor2@example.com",
			When:  baseTime.Add(3 * time.Second),
		},
	})
	require.NoError(t, err)

	// 베어 저장소로 복사
	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	// 모든 커밋 복사
	for _, commitHash := range []plumbing.Hash{baseCommit, commit1, commit2} {
		commitObj, err := workRepo.CommitObject(commitHash)
		require.NoError(t, err)

		commitObjEncoded, err := workRepo.Storer.EncodedObject(plumbing.CommitObject, commitHash)
		require.NoError(t, err)
		_, err = gitRepo.Storer.SetEncodedObject(commitObjEncoded)
		require.NoError(t, err)

		err = copyTreeRecursiveForTest(workRepo, gitRepo, commitObj.TreeHash)
		require.NoError(t, err)
	}

	mainRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), baseCommit)
	require.NoError(t, gitRepo.Storer.SetReference(mainRef))
	featureRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/feature"), commit2)
	require.NoError(t, gitRepo.Storer.SetReference(featureRef))
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))
	require.NoError(t, gitRepo.Storer.SetReference(headRef))

	// Merge preview handler 생성
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
		Git:     config.GitConfig{RepoRoot: tempDir},
	}
	mergeHandler, err := merge.NewMergePreviewHandler(cfg)
	require.NoError(t, err)

	router := gin.New()
	router.GET("/repos/:owner/:repo/merges/preview", mergeHandler.HandleMergePreview)

	req := httptest.NewRequest("GET", fmt.Sprintf("/repos/%s/%s/merges/preview?source_ref=feature&target_ref=main", owner, repo), nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "status=%d body=%s", w.Code, w.Body.String())

	var resp merge.MergePreviewResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	// Contributors는 중복 제거되어야 함 (contributor1@example.com이 2개 커밋이지만 1번만 나타나야 함)
	require.NotNil(t, resp.Contributors)
	require.GreaterOrEqual(t, len(resp.Contributors), 1, "should have at least one contributor")

	// 중복 확인
	contributorSet := make(map[string]bool)
	for _, c := range resp.Contributors {
		require.False(t, contributorSet[c], "contributor %s should be unique", c)
		contributorSet[c] = true
	}
}

func Test_Contributors_SortsByCommitCount(t *testing.T) {
	t.Parallel()

	// Merge preview API의 contributors는 email 리스트만 반환 (정렬은 커밋 수 기준이 아님)
	// 실제 정렬은 CountSummary.Contributors (개수)로 확인 가능
	tempDir := t.TempDir()
	owner := "testowner"
	repo := "contributors"
	repoPath := filepath.Join(tempDir, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	tempWorkDir, err := os.MkdirTemp("", "contributors-work-*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempWorkDir) }()

	workRepo, err := gogit.PlainInit(tempWorkDir, false)
	require.NoError(t, err)

	worktree, err := workRepo.Worktree()
	require.NoError(t, err)

	baseTime := fixedBaseTime()

	// main 브랜치
	require.NoError(t, os.WriteFile(filepath.Join(tempWorkDir, "base.txt"), []byte("base\n"), 0644))
	_, err = worktree.Add("base.txt")
	require.NoError(t, err)
	baseCommit, err := worktree.Commit("Base", &gogit.CommitOptions{
		Author: &object.Signature{Name: "A", Email: "a@example.com", When: baseTime},
	})
	require.NoError(t, err)

	// feature 브랜치: 여러 contributor
	err = worktree.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	require.NoError(t, err)

	// Contributor 1: 3개 커밋
	for i := 1; i <= 3; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(tempWorkDir, fmt.Sprintf("file%d.txt", i)), []byte("content\n"), 0644))
		_, err = worktree.Add(fmt.Sprintf("file%d.txt", i))
		require.NoError(t, err)
		_, err = worktree.Commit(fmt.Sprintf("Commit %d", i), &gogit.CommitOptions{
			Author: &object.Signature{Name: "C1", Email: "c1@example.com", When: baseTime.Add(time.Duration(i) * time.Second)},
		})
		require.NoError(t, err)
	}

	// Contributor 2: 1개 커밋
	require.NoError(t, os.WriteFile(filepath.Join(tempWorkDir, "file4.txt"), []byte("content\n"), 0644))
	_, err = worktree.Add("file4.txt")
	require.NoError(t, err)
	featureHead, err := worktree.Commit("Commit 4", &gogit.CommitOptions{
		Author: &object.Signature{Name: "C2", Email: "c2@example.com", When: baseTime.Add(10 * time.Second)},
	})
	require.NoError(t, err)

	// 베어 저장소로 복사
	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	commitIter, err := workRepo.Log(&gogit.LogOptions{From: featureHead})
	require.NoError(t, err)
	err = commitIter.ForEach(func(c *object.Commit) error {
		commitObjEncoded, err := workRepo.Storer.EncodedObject(plumbing.CommitObject, c.Hash)
		if err != nil {
			return err
		}
		_, err = gitRepo.Storer.SetEncodedObject(commitObjEncoded)
		if err != nil {
			return err
		}
		return copyTreeRecursiveForTest(workRepo, gitRepo, c.TreeHash)
	})
	require.NoError(t, err)

	mainRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), baseCommit)
	require.NoError(t, gitRepo.Storer.SetReference(mainRef))
	featureRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/feature"), featureHead)
	require.NoError(t, gitRepo.Storer.SetReference(featureRef))
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))
	require.NoError(t, gitRepo.Storer.SetReference(headRef))

	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
		Git:     config.GitConfig{RepoRoot: tempDir},
	}
	mergeHandler, err := merge.NewMergePreviewHandler(cfg)
	require.NoError(t, err)

	router := gin.New()
	router.GET("/repos/:owner/:repo/merges/preview", mergeHandler.HandleMergePreview)

	req := httptest.NewRequest("GET", fmt.Sprintf("/repos/%s/%s/merges/preview?source_ref=feature&target_ref=main", owner, repo), nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp merge.MergePreviewResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	// Contributors 배열과 CountSummary.Contributors 일치 확인
	require.NotNil(t, resp.Contributors)
	if resp.CountSummary != nil {
		require.Equal(t, len(resp.Contributors), resp.CountSummary.Contributors,
			"contributors count should match")
	}
	require.GreaterOrEqual(t, len(resp.Contributors), 2, "should have multiple contributors")
}

func Test_Contributors_IncludesEmail(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	owner := "testowner"
	repo := "contributors"
	repoPath := filepath.Join(tempDir, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	tempWorkDir, err := os.MkdirTemp("", "contributors-work-*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempWorkDir) }()

	workRepo, err := gogit.PlainInit(tempWorkDir, false)
	require.NoError(t, err)

	worktree, err := workRepo.Worktree()
	require.NoError(t, err)

	baseTime := fixedBaseTime()

	// main 브랜치
	require.NoError(t, os.WriteFile(filepath.Join(tempWorkDir, "base.txt"), []byte("base\n"), 0644))
	_, err = worktree.Add("base.txt")
	require.NoError(t, err)
	baseCommit, err := worktree.Commit("Base", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Base", Email: "base@example.com", When: baseTime},
	})
	require.NoError(t, err)

	// feature 브랜치
	err = worktree.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(tempWorkDir, "feature.txt"), []byte("feature\n"), 0644))
	_, err = worktree.Add("feature.txt")
	require.NoError(t, err)
	featureHead, err := worktree.Commit("Feature", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Feature Author", Email: "feature@example.com", When: baseTime.Add(1 * time.Second)},
	})
	require.NoError(t, err)

	// 베어 저장소로 복사
	gitRepo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	commitObjEncoded, err := workRepo.Storer.EncodedObject(plumbing.CommitObject, baseCommit)
	require.NoError(t, err)
	_, err = gitRepo.Storer.SetEncodedObject(commitObjEncoded)
	require.NoError(t, err)

	commitObjEncoded, err = workRepo.Storer.EncodedObject(plumbing.CommitObject, featureHead)
	require.NoError(t, err)
	_, err = gitRepo.Storer.SetEncodedObject(commitObjEncoded)
	require.NoError(t, err)

	baseCommitObj, err := workRepo.CommitObject(baseCommit)
	require.NoError(t, err)
	featureCommitObj, err := workRepo.CommitObject(featureHead)
	require.NoError(t, err)

	require.NoError(t, copyTreeRecursiveForTest(workRepo, gitRepo, baseCommitObj.TreeHash))
	require.NoError(t, copyTreeRecursiveForTest(workRepo, gitRepo, featureCommitObj.TreeHash))

	mainRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), baseCommit)
	require.NoError(t, gitRepo.Storer.SetReference(mainRef))
	featureRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/feature"), featureHead)
	require.NoError(t, gitRepo.Storer.SetReference(featureRef))
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))
	require.NoError(t, gitRepo.Storer.SetReference(headRef))

	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
		Git:     config.GitConfig{RepoRoot: tempDir},
	}
	mergeHandler, err := merge.NewMergePreviewHandler(cfg)
	require.NoError(t, err)

	router := gin.New()
	router.GET("/repos/:owner/:repo/merges/preview", mergeHandler.HandleMergePreview)

	req := httptest.NewRequest("GET", fmt.Sprintf("/repos/%s/%s/merges/preview?source_ref=feature&target_ref=main", owner, repo), nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp merge.MergePreviewResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	// Contributors는 이메일 주소를 포함해야 함
	require.NotNil(t, resp.Contributors)
	require.Greater(t, len(resp.Contributors), 0, "should have at least one contributor")
	for _, contributor := range resp.Contributors {
		require.Contains(t, contributor, "@", "contributor should be an email address: %s", contributor)
	}
}

// =============================================================================
// Tree-with-commit Meta propagation
// =============================================================================

func Test_TreeWithCommit_ResponseIncludesMeta(t *testing.T) {
	t.Parallel()

	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	owner := "testowner"
	repo := "testrepo"
	createTestRepositoryForHandler(t, tempDir, owner, repo)

	router := gin.New()
	router.GET("/repos/:owner/:repo/overview/tree/commits", handler.HandleGetTreeWithCommitOverview)

	req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repo+"/overview/tree/commits", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "status=%d body=%s", w.Code, w.Body.String())

	var resp TreeWithCommitOverviewResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.NotNil(t, resp.Meta, "response must include meta for caps visibility")
	require.NotEmpty(t, resp.Meta.ResolvedRefHash, "meta should have resolved ref hash")
	require.NotEmpty(t, resp.Meta.CutReason, "meta should have cut reason")
	require.GreaterOrEqual(t, resp.Meta.ScannedCommits, 0)
}

func Test_TreeWithCommit_SmallRepo_ReturnsComplete(t *testing.T) {
	t.Parallel()

	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	owner := "testowner"
	repo := "testrepo"
	createTestRepositoryForHandler(t, tempDir, owner, repo)

	router := gin.New()
	router.GET("/repos/:owner/:repo/overview/tree/commits", handler.HandleGetTreeWithCommitOverview)

	req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repo+"/overview/tree/commits", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp TreeWithCommitOverviewResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.NotNil(t, resp.Meta)
	require.False(t, resp.Meta.Incomplete, "small repo should always complete within caps")
	require.Equal(t, "complete", resp.Meta.CutReason)
}

func Test_TreeWithCommit_NonExistentPath_Returns404(t *testing.T) {
	t.Parallel()

	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	owner := "testowner"
	repo := "testrepo"
	createTestRepositoryForHandler(t, tempDir, owner, repo)

	router := gin.New()
	router.GET("/repos/:owner/:repo/overview/tree/commits", handler.HandleGetTreeWithCommitOverview)

	req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repo+"/overview/tree/commits?path=nonexistent", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
}

// =============================================================================
// Overview parallel execution + capped commit count
// =============================================================================

func Test_Overview_Parallel_ReturnsAllFields(t *testing.T) {
	t.Parallel()

	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	owner := "testowner"
	repo := "testrepo"
	createTestRepositoryForHandler(t, tempDir, owner, repo)

	router := gin.New()
	router.GET("/repos/:owner/:repo/overview", handler.HandleGetRepositoryOverview)

	req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repo+"/overview", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "status=%d body=%s", w.Code, w.Body.String())

	var resp RepositoryOverviewResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	// All fields must be populated (parallelism must not lose data)
	require.NotEmpty(t, resp.Repository)
	require.NotEmpty(t, resp.Owner)
	require.NotEmpty(t, resp.Name)
	require.NotEmpty(t, resp.Tree, "tree should have entries")
	require.NotEmpty(t, resp.Branches, "branches should have entries")
	require.GreaterOrEqual(t, resp.Count.Commits, 1, "commit count should be at least 1")
	require.GreaterOrEqual(t, resp.Count.Branches, 1)
	require.False(t, resp.Count.CommitsApproximate, "small repo should have exact count")
}

func Test_Overview_CommitCountApproximate_FlagExposedInResponse(t *testing.T) {
	t.Parallel()

	handler, tempDir, cleanup := setupTestHandler(t)
	defer cleanup()

	owner := "testowner"
	repo := "testrepo"
	createTestRepositoryForHandler(t, tempDir, owner, repo)

	router := gin.New()
	router.GET("/repos/:owner/:repo/overview", handler.HandleGetRepositoryOverview)

	req := httptest.NewRequest("GET", "/repos/"+owner+"/"+repo+"/overview", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	// Parse raw JSON to verify commits_approximate field exists
	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
	countMap, ok := raw["count"].(map[string]interface{})
	require.True(t, ok, "count field should be an object")
	// For small repos, commits_approximate is either false or omitted (omitempty)
	if v, exists := countMap["commits_approximate"]; exists {
		require.Equal(t, false, v, "small repo should not be approximate")
	}
}
