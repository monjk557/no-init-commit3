package core_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/collabops/git-executor/internal/git"
	"github.com/stretchr/testify/assert"
)

func setupTestRepository(t *testing.T) string {
	// 1. 임시 디렉토리 생성
	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "testrepo")

	// 2. Git 저장소 초기화
	err := exec.Command("git", "init", repoPath).Run()
	if err != nil {
		t.Fatalf("Failed to init git repo: %v", err)
	}

	// 3. 테스트 파일 생성
	readmePath := filepath.Join(repoPath, "README.md")
	err = os.WriteFile(readmePath, []byte("Hello World"), 0644)
	if err != nil {
		t.Fatalf("Failed to create README: %v", err)
	}

	// 4. Git add & commit
	err = exec.Command("git", "add", ".").Run()
	if err != nil {
		t.Fatalf("Failed to git add: %v", err)
	}

	err = exec.Command("git", "commit", "-m", "Initial commit").Run()
	if err != nil {
		t.Fatalf("Failed to git commit: %v", err)
	}

	return repoPath
}

func cleanupTestRepository(t *testing.T, testRepoPath string) {
	// t.TempDir() 사용 시 자동 정리됨
	// 수동 정리가 필요한 경우에만 실행
	if testRepoPath != "" {
		os.RemoveAll(testRepoPath)
	}
}

func TestGitBlobBasicFunctionality(t *testing.T) {
	// 1. 테스트용 저장소 설정
	testRepoPath := setupTestRepository(t)
	defer cleanupTestRepository(t, testRepoPath)

	// 2. 저장소 열기
	repo, err := git.OpenRepositorySimple(testRepoPath)
	assert.NoError(t, err)
	defer repo.Close()

	// 3. Blob 조회 테스트
	blob, err := repo.GetBlob(context.Background(), "README.md", "main")
	assert.NoError(t, err)
	assert.NotEmpty(t, blob)

	// 4. Blob 내용 확인
	expectedContent := "Hello World"
	assert.Equal(t, expectedContent, string(blob))
}
