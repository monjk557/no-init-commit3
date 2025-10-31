package unit

import (
	"strings"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDiffLineCount_SingleLineAddition은 1줄짜리 파일을 추가했을 때 정확히 1줄만 추가되는지 테스트합니다
func TestDiffLineCount_SingleLineAddition(t *testing.T) {
	// 테스트 저장소 설정
	repoPath := t.TempDir() + "/test-repo"
	repo, err := gogit.PlainInit(repoPath, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// 초기 커밋 생성
	initialFile, err := wt.Filesystem.Create("initial.txt")
	require.NoError(t, err)
	_, err = initialFile.Write([]byte("initial"))
	require.NoError(t, err)
	require.NoError(t, initialFile.Close())

	_, err = wt.Add("initial.txt")
	require.NoError(t, err)

	initialCommit, err := wt.Commit("Initial commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	// 1줄짜리 파일 추가
	singleLineFile, err := wt.Filesystem.Create("single-line.txt")
	require.NoError(t, err)
	_, err = singleLineFile.Write([]byte("Hello"))
	require.NoError(t, err)
	require.NoError(t, singleLineFile.Close())

	_, err = wt.Add("single-line.txt")
	require.NoError(t, err)

	newCommit, err := wt.Commit("Add single line file", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	// 두 커밋 가져오기
	initialCommitObj, err := repo.CommitObject(initialCommit)
	require.NoError(t, err)

	newCommitObj, err := repo.CommitObject(newCommit)
	require.NoError(t, err)

	// 트리 가져오기
	initialTree, err := initialCommitObj.Tree()
	require.NoError(t, err)

	newTree, err := newCommitObj.Tree()
	require.NoError(t, err)

	// diff 계산
	changes, err := object.DiffTree(initialTree, newTree)
	require.NoError(t, err)

	// single-line.txt 변경사항 찾기
	var singleLineChange *object.Change
	for _, change := range changes {
		if change.To.Name == "single-line.txt" {
			singleLineChange = change
			break
		}
	}

	require.NotNil(t, singleLineChange, "single-line.txt should be in the diff")

	// patch 가져오기 및 countPatchLines 테스트
	patch, err := singleLineChange.Patch()
	require.NoError(t, err)

	// 테스트 헬퍼를 사용하여 countPatchLines 로직 테스트
	additions, deletions := testCountPatchLines(patch)

	assert.Equal(t, 1, additions, "정확히 1줄만 추가되어야 합니다")
	assert.Equal(t, 0, deletions, "삭제된 줄이 없어야 합니다")
}

// testCountPatchLines는 countPatchLines 로직을 반영한 테스트 헬퍼 함수입니다
func testCountPatchLines(patch *object.Patch) (additions, deletions int) {
	for _, filePatch := range patch.FilePatches() {
		for _, chunk := range filePatch.Chunks() {
			switch chunk.Type() {
			case 1: // Add - 추가된 라인
				content := chunk.Content()
				lines := strings.Split(content, "\n")
				// 마지막이 빈 문자열이면(개행으로 끝난 경우) 제거
				if len(lines) > 0 && lines[len(lines)-1] == "" {
					lines = lines[:len(lines)-1]
				}
				additions += len(lines)
			case 2: // Delete - 삭제된 라인
				content := chunk.Content()
				lines := strings.Split(content, "\n")
				if len(lines) > 0 && lines[len(lines)-1] == "" {
					lines = lines[:len(lines)-1]
				}
				deletions += len(lines)
			}
		}
	}
	return additions, deletions
}

// TestDiffLineCount_SingleLineAddedToExistingFile는 기존 파일에 1줄을 추가했을 때를 테스트합니다
func TestDiffLineCount_SingleLineAddedToExistingFile(t *testing.T) {
	// 테스트 저장소 설정
	repoPath := t.TempDir() + "/test-repo"
	repo, err := gogit.PlainInit(repoPath, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// 초기 파일 생성 (끝에 개행 포함)
	existingFile, err := wt.Filesystem.Create("existing.txt")
	require.NoError(t, err)
	_, err = existingFile.Write([]byte("First line\n"))
	require.NoError(t, err)
	require.NoError(t, existingFile.Close())

	_, err = wt.Add("existing.txt")
	require.NoError(t, err)

	initialCommit, err := wt.Commit("Initial commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	// 파일 수정하여 한 줄 추가 (Second line 추가)
	existingFile2, err := wt.Filesystem.Create("existing.txt")
	require.NoError(t, err)
	_, err = existingFile2.Write([]byte("First line\nSecond line\n"))
	require.NoError(t, err)
	require.NoError(t, existingFile2.Close())

	_, err = wt.Add("existing.txt")
	require.NoError(t, err)

	newCommit, err := wt.Commit("Add second line", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	// 두 커밋 가져오기
	initialCommitObj, err := repo.CommitObject(initialCommit)
	require.NoError(t, err)

	newCommitObj, err := repo.CommitObject(newCommit)
	require.NoError(t, err)

	// 트리 가져오기
	initialTree, err := initialCommitObj.Tree()
	require.NoError(t, err)

	newTree, err := newCommitObj.Tree()
	require.NoError(t, err)

	// diff 계산
	changes, err := object.DiffTree(initialTree, newTree)
	require.NoError(t, err)

	// existing.txt 변경사항 찾기
	var existingFileChange *object.Change
	for _, change := range changes {
		if change.To.Name == "existing.txt" {
			existingFileChange = change
			break
		}
	}

	require.NotNil(t, existingFileChange, "existing.txt should be in the diff")

	// patch 가져오기 및 countPatchLines 테스트
	patch, err := existingFileChange.Patch()
	require.NoError(t, err)

	additions, deletions := testCountPatchLines(patch)

	// 라인 수 검증
	assert.Equal(t, 1, additions, "정확히 1줄만 추가되어야 합니다")
	assert.Equal(t, 0, deletions, "삭제된 줄이 없어야 합니다")
}
