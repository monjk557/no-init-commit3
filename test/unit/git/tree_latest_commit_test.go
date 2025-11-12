package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/git"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTreeLatestCommit_RecursiveDirectories는 GetTreeWithCommits가 중첩된 하위 디렉토리를 가진
// 디렉토리의 최신 커밋을 올바르게 찾는지 테스트합니다.
func TestTreeLatestCommit_RecursiveDirectories(t *testing.T) {
	ctx := context.Background()

	// 1. 테스트용 임시 디렉토리 생성
	tempDir, err := os.MkdirTemp("", "git-test-tree-commit-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// 2. 임시 디렉토리에 git 레포지토리 초기화
	repo, err := gogit.PlainInit(tempDir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// 3. 재귀적인 디렉토리 구조 생성
	// src/
	//   main.go
	//   utils/
	//     helper.go
	//     helpers/
	//       string.go
	//       math.go
	//   api/
	//     handler.go
	directoryStructure := map[string]string{
		"src/main.go":                 "package main",
		"src/utils/helper.go":         "package utils",
		"src/utils/helpers/string.go": "package helpers",
		"src/utils/helpers/math.go":   "package helpers",
		"src/api/handler.go":          "package api",
	}

	// 4. 서로 다른 시간에 파일을 커밋하여 서로 다른 커밋 타임스탬프 생성
	// 깊은 파일부터 먼저 커밋하고, 루트 파일은 마지막에 커밋하여 최신 커밋이 되도록 함
	commitOrder := []string{
		"src/utils/helpers/math.go",
		"src/utils/helpers/string.go",
		"src/utils/helper.go",
		"src/api/handler.go",
		"src/main.go",
	}

	commitHashes := make(map[string]string)
	commitTimes := make(map[string]time.Time)
	baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	for i, filePath := range commitOrder {
		// 디렉토리가 필요하면 생성
		dir := filepath.Join(tempDir, filepath.Dir(filePath))
		err = os.MkdirAll(dir, 0755)
		require.NoError(t, err)

		// 파일 생성
		fullPath := filepath.Join(tempDir, filePath)
		err = os.WriteFile(fullPath, []byte(directoryStructure[filePath]), 0644)
		require.NoError(t, err)

		// git에 추가
		_, err = wt.Add(filePath)
		require.NoError(t, err)

		// 서로 다른 타임스탬프로 커밋 (1시간씩 증가)
		commitTime := baseTime.Add(time.Duration(i) * time.Hour)
		commitHash, err := wt.Commit(fmt.Sprintf("Add %s", filePath), &gogit.CommitOptions{
			Author: &object.Signature{
				Name:  "Test Author",
				Email: "test@example.com",
				When:  commitTime,
			},
			Committer: &object.Signature{
				Name:  "Test Committer",
				Email: "test@example.com",
				When:  commitTime,
			},
		})
		require.NoError(t, err)

		commitHashes[filePath] = commitHash.String()
		commitTimes[filePath] = commitTime
	}

	// 5. 기본 브랜치 이름 가져오기 ("master" 또는 "main"일 수 있음)
	headRef, err := repo.Head()
	require.NoError(t, err)
	branchName := headRef.Name().Short()

	// 6. GoGitRepository를 사용하여 레포지토리 열기
	gitRepo, err := git.OpenRepository(tempDir)
	require.NoError(t, err)
	defer gitRepo.Close()

	// 7. 각 디렉토리 레벨에서 GetTreeWithCommits 테스트
	t.Run("root_directory", func(t *testing.T) {
		entries, err := gitRepo.GetTreeWithCommits(ctx, branchName, "")
		require.NoError(t, err)

		// src 디렉토리 찾기
		var srcEntry *git.TreeEntry
		for i := range entries {
			if entries[i].Path == "src" && entries[i].Type == git.EntryTypeTree {
				srcEntry = &entries[i]
				break
			}
		}
		require.NotNil(t, srcEntry, "src 디렉토리가 존재해야 함")
		require.NotNil(t, srcEntry.LatestCommit, "src 디렉토리는 최신 커밋을 가져야 함")

		// src의 최신 커밋은 src/ 내 모든 파일 중 가장 최신 커밋이어야 함
		// src/main.go가 마지막에 커밋되었으므로 이것이 최신 커밋이어야 함
		expectedLatestCommit := commitHashes["src/main.go"]
		assert.Equal(t, expectedLatestCommit, srcEntry.LatestCommit.Sha,
			"src 디렉토리의 최신 커밋은 src/main.go 커밋과 일치해야 함")
	})

	t.Run("src_directory", func(t *testing.T) {
		entries, err := gitRepo.GetTreeWithCommits(ctx, branchName, "src")
		require.NoError(t, err)

		// utils와 api 디렉토리 찾기
		var utilsEntry, apiEntry *git.TreeEntry
		for i := range entries {
			if entries[i].Path == "utils" && entries[i].Type == git.EntryTypeTree {
				utilsEntry = &entries[i]
			}
			if entries[i].Path == "api" && entries[i].Type == git.EntryTypeTree {
				apiEntry = &entries[i]
			}
		}

		// utils 디렉토리 테스트
		require.NotNil(t, utilsEntry, "utils 디렉토리가 존재해야 함")
		require.NotNil(t, utilsEntry.LatestCommit, "utils 디렉토리는 최신 커밋을 가져야 함")

		// utils의 최신 커밋은 utils/helper.go와 utils/helpers/* 중 가장 최신 커밋이어야 함
		// utils/helper.go가 utils/helpers/* 파일들보다 나중에 커밋되었음
		expectedUtilsCommit := commitHashes["src/utils/helper.go"]
		assert.Equal(t, expectedUtilsCommit, utilsEntry.LatestCommit.Sha,
			"utils 디렉토리의 최신 커밋은 utils/helper.go 커밋과 일치해야 함")

		// api 디렉토리 테스트
		require.NotNil(t, apiEntry, "api 디렉토리가 존재해야 함")
		require.NotNil(t, apiEntry.LatestCommit, "api 디렉토리는 최신 커밋을 가져야 함")

		// api의 최신 커밋은 api/handler.go여야 함
		expectedApiCommit := commitHashes["src/api/handler.go"]
		assert.Equal(t, expectedApiCommit, apiEntry.LatestCommit.Sha,
			"api 디렉토리의 최신 커밋은 api/handler.go 커밋과 일치해야 함")
	})

	t.Run("src_utils_helpers_directory", func(t *testing.T) {
		entries, err := gitRepo.GetTreeWithCommits(ctx, branchName, "src/utils/helpers")
		require.NoError(t, err)

		// helpers 디렉토리 내 엔트리 찾기
		var mathEntry, stringEntry *git.TreeEntry
		for i := range entries {
			if entries[i].Path == "math.go" {
				mathEntry = &entries[i]
			}
			if entries[i].Path == "string.go" {
				stringEntry = &entries[i]
			}
		}

		// 두 파일 모두 존재해야 함
		require.NotNil(t, mathEntry, "math.go가 존재해야 함")
		require.NotNil(t, stringEntry, "string.go가 존재해야 함")

		// 파일 커밋 확인
		require.NotNil(t, mathEntry.LatestCommit, "math.go는 최신 커밋을 가져야 함")
		require.NotNil(t, stringEntry.LatestCommit, "string.go는 최신 커밋을 가져야 함")

		// string.go가 math.go보다 나중에 커밋되었음
		assert.Equal(t, commitHashes["src/utils/helpers/string.go"], stringEntry.LatestCommit.Sha)
		assert.Equal(t, commitHashes["src/utils/helpers/math.go"], mathEntry.LatestCommit.Sha)

		// 부모 디렉토리에서 helpers 디렉토리 테스트
		parentEntries, err := gitRepo.GetTreeWithCommits(ctx, branchName, "src/utils")
		require.NoError(t, err)

		var helpersEntry *git.TreeEntry
		for i := range parentEntries {
			if parentEntries[i].Path == "helpers" && parentEntries[i].Type == git.EntryTypeTree {
				helpersEntry = &parentEntries[i]
				break
			}
		}

		require.NotNil(t, helpersEntry, "helpers 디렉토리가 존재해야 함")
		require.NotNil(t, helpersEntry.LatestCommit, "helpers 디렉토리는 최신 커밋을 가져야 함")

		// helpers의 최신 커밋은 그 내부 파일들 중 가장 최신 커밋(string.go)이어야 함
		expectedHelpersCommit := commitHashes["src/utils/helpers/string.go"]
		assert.Equal(t, expectedHelpersCommit, helpersEntry.LatestCommit.Sha,
			"helpers 디렉토리의 최신 커밋은 helpers/string.go 커밋과 일치해야 함")
	})

	t.Run("verify_commit_timestamps", func(t *testing.T) {
		// 커밋이 올바른 순서인지 확인
		// 나중 커밋은 더 늦은 타임스탬프를 가져야 함
		entries, err := gitRepo.GetTreeWithCommits(ctx, branchName, "src")
		require.NoError(t, err)

		var utilsEntry, mainEntry *git.TreeEntry
		for i := range entries {
			if entries[i].Path == "utils" && entries[i].Type == git.EntryTypeTree {
				utilsEntry = &entries[i]
			}
			if entries[i].Path == "main.go" && entries[i].Type == git.EntryTypeBlob {
				mainEntry = &entries[i]
			}
		}

		// main.go가 가장 최신이어야 함
		require.NotNil(t, mainEntry)
		require.NotNil(t, mainEntry.LatestCommit)

		// main.go 커밋 시간은 utils와 api보다 나중이어야 함
		mainCommitTime := commitTimes["src/main.go"]
		utilsCommitTime := commitTimes["src/utils/helper.go"]
		apiCommitTime := commitTimes["src/api/handler.go"]

		assert.True(t, mainCommitTime.After(utilsCommitTime),
			"main.go는 utils/helper.go보다 나중에 커밋되어야 함")
		assert.True(t, mainCommitTime.After(apiCommitTime),
			"main.go는 api/handler.go보다 나중에 커밋되어야 함")

		// 디렉토리의 최신 커밋이 파일 커밋 시간과 일치하는지 확인
		if utilsEntry != nil && utilsEntry.LatestCommit != nil {
			// utils는 helper.go의 커밋 시간을 가져야 함 (helpers/*가 아님)
			utilsLatestTime := commitTimes["src/utils/helper.go"]
			helpersStringTime := commitTimes["src/utils/helpers/string.go"]
			assert.True(t, utilsLatestTime.After(helpersStringTime),
				"utils/helper.go는 utils/helpers/string.go보다 나중에 커밋되어야 함")
		}
	})
}
