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
