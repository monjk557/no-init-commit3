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

	// 3. 초기 커밋 생성 (빈 커밋)
	cmd := exec.Command("git", "commit", "--allow-empty", "-m", "Initial commit")
	cmd.Dir = repoPath
	err = cmd.Run()
	if err != nil {
		t.Fatalf("Failed to create initial commit: %v", err)
	}

	// 4. Hello World 파일 생성
	newFilePath := filepath.Join(repoPath, "newfile.txt")
	err = os.WriteFile(newFilePath, []byte("Hello World"), 0644)
	if err != nil {
		t.Fatalf("Failed to create newfile.txt: %v", err)
	}

	// 5. 새 파일 추가 및 커밋
	cmd = exec.Command("git", "add", "newfile.txt")
	cmd.Dir = repoPath
	err = cmd.Run()
	if err != nil {
		t.Fatalf("Failed to git add newfile.txt: %v", err)
	}

	cmd = exec.Command("git", "commit", "-m", "Add newfile.txt")
	cmd.Dir = repoPath
	err = cmd.Run()
	if err != nil {
		t.Fatalf("Failed to git commit newfile.txt: %v", err)
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

	t.Run("ExistingFileBlob", func(t *testing.T) {
		// 3. 실존 파일 Blob 조회 테스트
		blob, err := repo.GetBlob(context.Background(), "newfile.txt", "main")
		assert.NoError(t, err)
		assert.NotEmpty(t, blob)

		// 4. Blob 내용 확인
		expectedContent := "Hello World"
		assert.Equal(t, expectedContent, string(blob))
	})

	t.Run("NonExistentFileBlob", func(t *testing.T) {
		// 5. 존재하지 않는 파일 Blob 조회 테스트
		blob, err := repo.GetBlob(context.Background(), "nonexistent.txt", "main")
		assert.Error(t, err)
		assert.Empty(t, blob)
	})

	t.Run("FileCommitsHistory", func(t *testing.T) {
		// 6. 레포 파일 관여 commits history 조회 테스트
		commits, err := repo.GetCommitsForFile(context.Background(), "newfile.txt", "main", 10, 0)
		assert.NoError(t, err)
		assert.NotEmpty(t, commits)
	})

	t.Run("RepositoryClose", func(t *testing.T) {
		// 7. 레포 삭제
		err := repo.Close()
		assert.NoError(t, err)
	})
}
