package overview

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
		_ = os.RemoveAll(tempDir)
	}

	return service, tempDir, cleanup
}

func createTestRepositoryWithCommits(t *testing.T, tempDir string) *git.Repository {
	repoPath := filepath.Join(tempDir, "test.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	// 작업 디렉토리에서 일반 저장소 생성
	tempWorkDir, err := os.MkdirTemp("", "overview-test-work-*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempWorkDir) }()

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

func createTestRepositoryWithMultipleLanguages(t *testing.T, tempDir string) *git.Repository {
	repoPath := filepath.Join(tempDir, "multilang.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	// 작업 디렉토리에서 일반 저장소 생성
	tempWorkDir, err := os.MkdirTemp("", "overview-test-work-*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempWorkDir) }()

	workRepo, err := git.PlainInit(tempWorkDir, false)
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

greet("World");
`
	require.NoError(t, os.WriteFile(jsPath, []byte(jsContent), 0644))
	_, err = worktree.Add("app.js")
	require.NoError(t, err)

	// Python 파일
	pyPath := filepath.Join(tempWorkDir, "script.py")
	pyContent := `def greet(name):
	print(f"Hello, {name}!")

if __name__ == "__main__":
	greet("World")
`
	require.NoError(t, os.WriteFile(pyPath, []byte(pyContent), 0644))
	_, err = worktree.Add("script.py")
	require.NoError(t, err)

	// TypeScript 파일
	tsPath := filepath.Join(tempWorkDir, "utils.ts")
	tsContent := `interface User {
	name: string;
	age: number;
}

function getUser(): User {
	return { name: "John", age: 30 };
}
`
	require.NoError(t, os.WriteFile(tsPath, []byte(tsContent), 0644))
	_, err = worktree.Add("utils.ts")
	require.NoError(t, err)

	// README.md (Markdown)
	readmePath := filepath.Join(tempWorkDir, "README.md")
	readmeContent := `# Multi-language Repository

This repository contains code in multiple languages.
`
	require.NoError(t, os.WriteFile(readmePath, []byte(readmeContent), 0644))
	_, err = worktree.Add("README.md")
	require.NoError(t, err)

	commit, err := worktree.Commit("Add multiple language files", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	// 베어 저장소 생성 및 객체 복사
	gitRepo, err := git.PlainInit(repoPath, true)
	require.NoError(t, err)

	commitObj, err := workRepo.CommitObject(commit)
	require.NoError(t, err)

	// 커밋 객체 복사
	commitObjEncoded, err := workRepo.Storer.EncodedObject(plumbing.CommitObject, commit)
	require.NoError(t, err)
	_, err = gitRepo.Storer.SetEncodedObject(commitObjEncoded)
	require.NoError(t, err)

	// 트리 객체 복사
	err = copyTreeRecursiveForTest(workRepo, gitRepo, commitObj.TreeHash)
	require.NoError(t, err)

	// HEAD를 최신 커밋으로 설정
	mainRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), commit)
	require.NoError(t, gitRepo.Storer.SetReference(mainRef))

	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))
	require.NoError(t, gitRepo.Storer.SetReference(headRef))

	return gitRepo
}

func TestGetLanguageDistribution_MultipleLanguages(t *testing.T) {
	service, tempDir, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	gitRepo := createTestRepositoryWithMultipleLanguages(t, tempDir)

	// 언어 분포 가져오기
	distribution := service.GetLanguageDistribution(ctx, gitRepo, "HEAD")

	// 기본 검증
	assert.Greater(t, distribution.TotalFiles, 0, "TotalFiles should be greater than 0")
	assert.Greater(t, distribution.TotalBytes, int64(0), "TotalBytes should be greater than 0")
	assert.NotEmpty(t, distribution.Languages, "Languages should not be empty")

	// 언어별 검증
	languageMap := make(map[string]LanguageStats)
	for _, lang := range distribution.Languages {
		languageMap[lang.Language] = lang
		assert.Greater(t, lang.Bytes, int64(0), "Language bytes should be greater than 0")
		assert.Greater(t, lang.Files, 0, "Language files should be greater than 0")
		assert.GreaterOrEqual(t, lang.Percentage, 0.0, "Percentage should be >= 0")
		assert.LessOrEqual(t, lang.Percentage, 100.0, "Percentage should be <= 100")
	}

	// 출력 규칙 검증: top 5 + 1% 이상 + Others 합산
	visibleCount := 0
	for _, lang := range distribution.Languages {
		if lang.Language == "Others" {
			continue
		}
		visibleCount++
		assert.GreaterOrEqual(t, lang.Percentage, 1.0, "Visible languages should be >= 1%%")
	}
	assert.LessOrEqual(t, visibleCount, 5, "Visible languages should be at most 5")

	// Others는 존재한다면 마지막에 있어야 함
	for i := 0; i < len(distribution.Languages)-1; i++ {
		assert.NotEqual(t, "Others", distribution.Languages[i].Language, "Others should be the last entry when present")
	}

	// Go 언어 확인
	goLang, exists := languageMap["Go"]
	assert.True(t, exists, "Go language should be detected")
	if exists {
		assert.Greater(t, goLang.Files, 0, "Go files count should be greater than 0")
	}

	// JavaScript 언어 확인
	jsLang, exists := languageMap["JavaScript"]
	assert.True(t, exists, "JavaScript language should be detected")
	if exists {
		assert.Greater(t, jsLang.Files, 0, "JavaScript files count should be greater than 0")
	}

	// Python 언어 확인
	pyLang, exists := languageMap["Python"]
	assert.True(t, exists, "Python language should be detected")
	if exists {
		assert.Greater(t, pyLang.Files, 0, "Python files count should be greater than 0")
	}

	// 정렬 검증: Others를 제외한 항목은 바이트 수 내림차순이어야 함
	lastBytes := int64(1<<63 - 1)
	for _, lang := range distribution.Languages {
		if lang.Language == "Others" {
			continue
		}
		assert.GreaterOrEqual(t, lastBytes, lang.Bytes, "Visible languages should be sorted by bytes in descending order")
		lastBytes = lang.Bytes
	}

	// Percentage 합계 검증 (100%에 가까워야 함, 부동소수점 오차 고려)
	totalPercentage := 0.0
	for _, lang := range distribution.Languages {
		totalPercentage += lang.Percentage
	}
	assert.InDelta(t, 100.0, totalPercentage, 0.01, "Total percentage should be approximately 100%%")
}

func createTestRepositoryForLanguageDistributionFiltering(t *testing.T, tempDir string) *git.Repository {
	repoPath := filepath.Join(tempDir, "filtering.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	tempWorkDir, err := os.MkdirTemp("", "overview-filtering-work-*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempWorkDir) }()

	workRepo, err := git.PlainInit(tempWorkDir, false)
	require.NoError(t, err)

	worktree, err := workRepo.Worktree()
	require.NoError(t, err)

	// 3개 언어는 1% 이상이 되도록 크게 만들고, 나머지는 매우 작게 만들어 Others로 합산되게 함
	goPath := filepath.Join(tempWorkDir, "main.go")
	goContent := "package main\n\nfunc main() {}\n" + strings.Repeat("a", 200000)
	require.NoError(t, os.WriteFile(goPath, []byte(goContent), 0644))
	_, err = worktree.Add("main.go")
	require.NoError(t, err)

	jsPath := filepath.Join(tempWorkDir, "app.js")
	jsContent := "function main() { return 1; }\n" + strings.Repeat("b", 15000)
	require.NoError(t, os.WriteFile(jsPath, []byte(jsContent), 0644))
	_, err = worktree.Add("app.js")
	require.NoError(t, err)

	pyPath := filepath.Join(tempWorkDir, "script.py")
	pyContent := "def main():\n    return 1\n" + strings.Repeat("c", 5000)
	require.NoError(t, os.WriteFile(pyPath, []byte(pyContent), 0644))
	_, err = worktree.Add("script.py")
	require.NoError(t, err)

	// Tiny languages (<1% each)
	require.NoError(t, os.WriteFile(filepath.Join(tempWorkDir, "tiny.rb"), []byte("puts 'x'\n"+strings.Repeat("d", 50)), 0644))
	_, err = worktree.Add("tiny.rb")
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(tempWorkDir, "tiny.rs"), []byte("fn main() {}\n"+strings.Repeat("e", 50)), 0644))
	_, err = worktree.Add("tiny.rs")
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(tempWorkDir, "tiny.java"), []byte("class A { public static void main(String[] args){} }\n"+strings.Repeat("f", 50)), 0644))
	_, err = worktree.Add("tiny.java")
	require.NoError(t, err)

	commit, err := worktree.Commit("Add language files for filtering test", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	// 베어 저장소 생성 및 객체 복사
	gitRepo, err := git.PlainInit(repoPath, true)
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

	return gitRepo
}

func TestGetLanguageDistribution_FilteringAndOthers(t *testing.T) {
	service, tempDir, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	gitRepo := createTestRepositoryForLanguageDistributionFiltering(t, tempDir)

	distribution := service.GetLanguageDistribution(ctx, gitRepo, "HEAD")

	assert.Greater(t, distribution.TotalBytes, int64(0))
	assert.Greater(t, distribution.TotalFiles, 0)
	assert.NotEmpty(t, distribution.Languages)

	visibleCount := 0
	hasOthers := false
	sumPct := 0.0
	for i, lang := range distribution.Languages {
		sumPct += lang.Percentage
		if lang.Language == "Others" {
			hasOthers = true
			assert.Equal(t, len(distribution.Languages)-1, i, "Others should be last")
			continue
		}
		visibleCount++
		assert.GreaterOrEqual(t, lang.Percentage, 1.0)
	}

	assert.True(t, hasOthers, "Others should exist when there are hidden languages")
	assert.LessOrEqual(t, visibleCount, 5)
	assert.InDelta(t, 100.0, sumPct, 0.01)
}

func createTestRepositoryForLanguageDebugLargeFile(t *testing.T, tempDir string) *git.Repository {
	repoPath := filepath.Join(tempDir, "debug-large.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	tempWorkDir, err := os.MkdirTemp("", "overview-debug-large-work-*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempWorkDir) }()

	workRepo, err := git.PlainInit(tempWorkDir, false)
	require.NoError(t, err)

	worktree, err := workRepo.Worktree()
	require.NoError(t, err)

	// 1MB 초과 파일은 집계에서 제외되어야 함 (skip too_large_over_1mb)
	pyPath := filepath.Join(tempWorkDir, "big.py")
	pyContent := "print('x')\n" + strings.Repeat("a", 1024*1024+10)
	require.NoError(t, os.WriteFile(pyPath, []byte(pyContent), 0644))
	_, err = worktree.Add("big.py")
	require.NoError(t, err)

	goPath := filepath.Join(tempWorkDir, "main.go")
	goContent := "package main\n\nfunc main() {}\n"
	require.NoError(t, os.WriteFile(goPath, []byte(goContent), 0644))
	_, err = worktree.Add("main.go")
	require.NoError(t, err)

	commit, err := worktree.Commit("Add big file for debug test", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	gitRepo, err := git.PlainInit(repoPath, true)
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

	return gitRepo
}

func TestGetLanguageDistribution_LargeFileSkipped(t *testing.T) {
	service, tempDir, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	gitRepo := createTestRepositoryForLanguageDebugLargeFile(t, tempDir)

	distribution := service.GetLanguageDistribution(ctx, gitRepo, "HEAD")

	// big.py는 1MB 초과라 제외되어야 함
	assert.Equal(t, 1, distribution.TotalFiles)
	assert.NotEmpty(t, distribution.Languages)
	assert.Equal(t, "Go", distribution.Languages[0].Language)
}

func createTestRepositoryForLinguistStyleExclusions(t *testing.T, tempDir string) *git.Repository {
	repoPath := filepath.Join(tempDir, "linguist-exclusions.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	tempWorkDir, err := os.MkdirTemp("", "overview-linguist-exclusions-work-*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempWorkDir) }()

	workRepo, err := git.PlainInit(tempWorkDir, false)
	require.NoError(t, err)

	worktree, err := workRepo.Worktree()
	require.NoError(t, err)

	// Included (real code)
	require.NoError(t, os.WriteFile(filepath.Join(tempWorkDir, "main.py"), []byte("print('ok')\n"), 0644))
	_, err = worktree.Add("main.py")
	require.NoError(t, err)

	// Vendored
	require.NoError(t, os.MkdirAll(filepath.Join(tempWorkDir, "vendor"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tempWorkDir, "vendor", "vendored.py"), []byte("print('vendored')\n"), 0644))
	_, err = worktree.Add("vendor/vendored.py")
	require.NoError(t, err)

	// Documentation
	require.NoError(t, os.WriteFile(filepath.Join(tempWorkDir, "README.md"), []byte("# Docs\n"), 0644))
	_, err = worktree.Add("README.md")
	require.NoError(t, err)

	// Dotfile
	require.NoError(t, os.WriteFile(filepath.Join(tempWorkDir, ".env"), []byte("KEY=value\n"), 0644))
	_, err = worktree.Add(".env")
	require.NoError(t, err)

	// Generated (linguist-style marker)
	require.NoError(t, os.WriteFile(filepath.Join(tempWorkDir, "generated.go"), []byte("// Code generated by tool DO NOT EDIT.\npackage main\n"), 0644))
	_, err = worktree.Add("generated.go")
	require.NoError(t, err)

	commit, err := worktree.Commit("Add files for linguist-style exclusions test", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	gitRepo, err := git.PlainInit(repoPath, true)
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

	return gitRepo
}

func TestGetLanguageDistribution_LinguistStyleExclusions(t *testing.T) {
	service, tempDir, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	gitRepo := createTestRepositoryForLinguistStyleExclusions(t, tempDir)

	distribution := service.GetLanguageDistribution(ctx, gitRepo, "HEAD")

	// Included bytes/files should reflect only non-excluded files (main.py)
	assert.Equal(t, 1, distribution.TotalFiles)
	assert.NotEmpty(t, distribution.Languages)
	assert.Equal(t, "Python", distribution.Languages[0].Language)
}

func TestGetLanguageDistribution_EmptyRepository(t *testing.T) {
	service, tempDir, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()

	// 빈 저장소 생성
	repoPath := filepath.Join(tempDir, "empty.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0755))

	gitRepo, err := git.PlainInit(repoPath, true)
	require.NoError(t, err)

	// 빈 커밋 생성
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

	// 언어 분포 가져오기
	distribution := service.GetLanguageDistribution(ctx, gitRepo, "HEAD")

	// 빈 저장소는 빈 결과 반환
	assert.Equal(t, 0, distribution.TotalFiles, "TotalFiles should be 0 for empty repository")
	assert.Equal(t, int64(0), distribution.TotalBytes, "TotalBytes should be 0 for empty repository")
	assert.Empty(t, distribution.Languages, "Languages should be empty for empty repository")
}

func TestGetLanguageDistribution_InvalidRef(t *testing.T) {
	service, tempDir, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	gitRepo := createTestRepositoryWithMultipleLanguages(t, tempDir)

	// 존재하지 않는 ref로 언어 분포 가져오기
	distribution := service.GetLanguageDistribution(ctx, gitRepo, "nonexistent-ref")

	// 에러 시 빈 결과 반환
	assert.Equal(t, 0, distribution.TotalFiles, "TotalFiles should be 0 for invalid ref")
	assert.Equal(t, int64(0), distribution.TotalBytes, "TotalBytes should be 0 for invalid ref")
	assert.Empty(t, distribution.Languages, "Languages should be empty for invalid ref")
}
