package overview

import (
	"encoding/json"
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
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func setupTestHandler(t *testing.T) (*RepositoryOverviewHandler, string, func()) {
	gin.SetMode(gin.TestMode)

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
		os.RemoveAll(tempDir)
	}

	return handler, tempDir, cleanup
}

func createTestRepositoryForHandler(t *testing.T, tempDir, owner, repo string) {
	repoPath := filepath.Join(tempDir, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	tempWorkDir, err := os.MkdirTemp("", "overview-handler-work-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempWorkDir)

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

	_, err = worktree.Commit("Initial commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
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

	commit := &object.Commit{
		Message:   "Initial empty commit",
		Author:    object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
		Committer: object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
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
	defer os.RemoveAll(tempWorkDir)

	workRepo, err := gogit.PlainInit(tempWorkDir, false)
	require.NoError(t, err)

	worktree, err := workRepo.Worktree()
	require.NoError(t, err)

	commitHashes := make(map[string]plumbing.Hash)
	baseTime := time.Now()

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
