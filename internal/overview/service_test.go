package overview

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestService(t *testing.T) (*RepositoryOverviewService, string, func()) {
	tempDir, err := os.MkdirTemp("", "overview-test-*")
	require.NoError(t, err)

	logger, err := logging.NewLogger(config.LoggingConfig{
		Level:       "info",
		Format:      "json",
		Output:      "stdout",
		Development: false,
	})
	require.NoError(t, err)

	pathConfig, err := repositories.NewPathConfig(tempDir)
	require.NoError(t, err)

	repoManager, err := repositories.NewRepositoryManager(pathConfig, logger)
	require.NoError(t, err)

	service, err := NewRepositoryOverviewService(logger, pathConfig, repoManager)
	require.NoError(t, err)

	cleanup := func() {
		os.RemoveAll(tempDir)
	}

	return service, tempDir, cleanup
}

func createTestRepositoryWithCommits(t *testing.T, tempDir string) *git.Repository {
	repoPath := filepath.Join(tempDir, "test.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	// 작업 디렉토리에서 일반 저장소 생성
	tempWorkDir, err := os.MkdirTemp("", "overview-test-work-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempWorkDir)

	workRepo, err := git.PlainInit(tempWorkDir, false)
	require.NoError(t, err)

	worktree, err := workRepo.Worktree()
	require.NoError(t, err)

	// 첫 번째 커밋: README.md 생성
	readmePath := filepath.Join(tempWorkDir, "README.md")
	require.NoError(t, os.WriteFile(readmePath, []byte("# Test Repository\n"), 0644))
	_, err = worktree.Add("README.md")
	require.NoError(t, err)

	commit1, err := worktree.Commit("Initial commit: Add README", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	// 두 번째 커밋: src/main.go 생성
	srcDir := filepath.Join(tempWorkDir, "src")
	require.NoError(t, os.MkdirAll(srcDir, 0755))
	mainPath := filepath.Join(srcDir, "main.go")
	require.NoError(t, os.WriteFile(mainPath, []byte("package main\n\nfunc main() {}\n"), 0644))
	_, err = worktree.Add("src/main.go")
	require.NoError(t, err)

	commit2, err := worktree.Commit("Add main.go", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now().Add(time.Minute),
		},
	})
	require.NoError(t, err)

	// 세 번째 커밋: README.md 수정
	require.NoError(t, os.WriteFile(readmePath, []byte("# Test Repository\n\nUpdated content\n"), 0644))
	_, err = worktree.Add("README.md")
	require.NoError(t, err)

	commit3, err := worktree.Commit("Update README", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now().Add(2 * time.Minute),
		},
	})
	require.NoError(t, err)

	// 베어 저장소 생성 및 객체 복사
	gitRepo, err := git.PlainInit(repoPath, true)
	require.NoError(t, err)

	// 모든 커밋 객체와 트리/블롭 객체 복사
	commits := []plumbing.Hash{commit1, commit2, commit3}
	for _, commitHash := range commits {
		commit, err := workRepo.CommitObject(commitHash)
		require.NoError(t, err)

		// 커밋 객체 복사
		commitObj, err := workRepo.Storer.EncodedObject(plumbing.CommitObject, commitHash)
		require.NoError(t, err)
		_, err = gitRepo.Storer.SetEncodedObject(commitObj)
		require.NoError(t, err)

		// 트리 객체 복사
		err = copyTreeRecursiveForTest(workRepo, gitRepo, commit.TreeHash)
		require.NoError(t, err)
	}

	// HEAD를 최신 커밋으로 설정
	mainRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), commit3)
	require.NoError(t, gitRepo.Storer.SetReference(mainRef))

	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))
	require.NoError(t, gitRepo.Storer.SetReference(headRef))

	return gitRepo
}

func copyTreeRecursiveForTest(workRepo, gitRepo *git.Repository, treeHash plumbing.Hash) error {
	tree, err := workRepo.TreeObject(treeHash)
	if err != nil {
		return err
	}

	// 트리 객체 복사
	treeObj, err := workRepo.Storer.EncodedObject(plumbing.TreeObject, treeHash)
	if err != nil {
		return err
	}
	_, err = gitRepo.Storer.SetEncodedObject(treeObj)
	if err != nil {
		return err
	}

	// 모든 엔트리 처리
	for _, entry := range tree.Entries {
		if entry.Mode == filemode.Dir {
			// 서브트리 재귀적으로 복사
			if err := copyTreeRecursiveForTest(workRepo, gitRepo, entry.Hash); err != nil {
				return err
			}
		} else {
			// 블롭 객체 복사
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

func TestGetLatestCommitForPath_BlobFile(t *testing.T) {
	service, tempDir, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	gitRepo := createTestRepositoryWithCommits(t, tempDir)

	// README.md 파일의 최신 커밋 조회
	commit, err := service.GetLatestCommitForPath(ctx, gitRepo, "HEAD", "README.md", false)
	require.NoError(t, err)
	require.NotNil(t, commit)

	assert.Equal(t, "Update README", commit.Message)
	assert.NotEmpty(t, commit.SHA)
	assert.NotEmpty(t, commit.Author.Name)
	assert.NotEmpty(t, commit.Author.Email)
}

func TestGetLatestCommitForPath_TreeDirectory(t *testing.T) {
	service, tempDir, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	gitRepo := createTestRepositoryWithCommits(t, tempDir)

	// src 디렉토리의 최신 커밋 조회 (하위 파일 중 가장 최근)
	commit, err := service.GetLatestCommitForPath(ctx, gitRepo, "HEAD", "src", true)
	require.NoError(t, err)
	require.NotNil(t, commit)

	assert.Equal(t, "Add main.go", commit.Message)
	assert.NotEmpty(t, commit.SHA)
}

func TestGetLatestCommitForPath_NonExistentPath(t *testing.T) {
	service, tempDir, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	gitRepo := createTestRepositoryWithCommits(t, tempDir)

	// 존재하지 않는 파일 경로
	commit, err := service.GetLatestCommitForPath(ctx, gitRepo, "HEAD", "nonexistent.txt", false)
	require.NoError(t, err)
	assert.Nil(t, commit)
}

func TestGetLatestCommitForPath_RootPath(t *testing.T) {
	service, tempDir, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	gitRepo := createTestRepositoryWithCommits(t, tempDir)

	// 루트 경로의 최신 커밋 조회
	commit, err := service.GetLatestCommitForPath(ctx, gitRepo, "HEAD", "", true)
	require.NoError(t, err)
	require.NotNil(t, commit)

	assert.NotEmpty(t, commit.SHA)
}

func TestGetLatestCommitsForPaths_MultiplePaths(t *testing.T) {
	service, tempDir, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	gitRepo := createTestRepositoryWithCommits(t, tempDir)

	pathInfos := []CommitPathInfo{
		{Path: "README.md", IsTree: false},
		{Path: "src", IsTree: true},
		{Path: "nonexistent.txt", IsTree: false},
	}

	result := service.GetLatestCommitsForPaths(ctx, gitRepo, "HEAD", pathInfos)

	// README.md의 커밋 확인
	readmeCommit, exists := result["README.md"]
	require.True(t, exists)
	require.NotNil(t, readmeCommit)
	assert.Equal(t, "Update README", readmeCommit.Message)

	// src 디렉토리의 커밋 확인
	srcCommit, exists := result["src"]
	require.True(t, exists)
	require.NotNil(t, srcCommit)
	assert.Equal(t, "Add main.go", srcCommit.Message)
}

func TestConvertCommit(t *testing.T) {
	service, tempDir, cleanup := setupTestService(t)
	defer cleanup()

	gitRepo := createTestRepositoryWithCommits(t, tempDir)

	// 커밋 객체 가져오기
	head, err := gitRepo.Head()
	require.NoError(t, err)

	commitObj, err := gitRepo.CommitObject(head.Hash())
	require.NoError(t, err)

	// convertCommit 메서드 테스트
	gitCommit := service.convertCommit(commitObj)

	assert.Equal(t, commitObj.Hash.String(), gitCommit.SHA)
	assert.Equal(t, commitObj.Message, gitCommit.Message)
	assert.Equal(t, commitObj.Author.Name, gitCommit.Author.Name)
	assert.Equal(t, commitObj.Author.Email, gitCommit.Author.Email)
	assert.Equal(t, commitObj.Committer.Name, gitCommit.Committer.Name)
	assert.Equal(t, commitObj.Committer.Email, gitCommit.Committer.Email)
	assert.Equal(t, commitObj.TreeHash.String(), gitCommit.TreeHash)
	assert.Equal(t, len(commitObj.ParentHashes), len(gitCommit.ParentHashes))
}
