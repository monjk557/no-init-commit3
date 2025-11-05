package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// TestMergeMethodsCommitHistory는 merge, squash, rebase 세 가지 merge method의
// commit history가 예상대로 생성되는지 검증하는 테스트입니다.
func TestMergeMethodsCommitHistory(t *testing.T) {
	// 테스트 환경 설정
	suite := SetupIntegrationTest(t)

	// 테스트용 repo 3개 생성 (각 merge method별로)
	repos := []struct {
		name   string
		method string
	}{
		{"mergetest", "merge"},
		{"squashtest", "squash"},
		{"rebasetest", "rebase"},
	}

	for _, repoInfo := range repos {
		t.Run(fmt.Sprintf("%s_Method", repoInfo.method), func(t *testing.T) {
			testMergeMethodCommitHistory(t, suite, repoInfo.name, repoInfo.method)
		})
	}
}

// testMergeMethodCommitHistory는 특정 merge method의 commit history를 테스트합니다
func testMergeMethodCommitHistory(t *testing.T, suite *IntegrationTestSuite, repoName, mergeMethod string) {
	owner := "testowner"
	baseBranch := "main"
	featureBranch := "feature/test"

	// 1. 테스트용 repository 생성
	repoPath := filepath.Join(suite.tempDir, owner, fmt.Sprintf("%s.git", repoName))
	err := os.MkdirAll(filepath.Dir(repoPath), 0755)
	require.NoError(t, err)

	// Bare repository 초기화
	_, err = gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	// 2. main 브랜치에 초기 커밋 생성 (첫 커밋부터 시작)
	mainCommit1, err := createFirstCommitInBranch(t, repoPath, baseBranch, "main1.txt", "Main branch first commit", "Main branch first commit content")
	require.NoError(t, err)

	mainCommit2, err := createCommitInRepo(t, repoPath, baseBranch, "main2.txt", "Main branch second commit", "Main branch second commit content")
	require.NoError(t, err)

	// 3. feature 브랜치를 main-commit-1에서 생성하고 커밋 추가
	// feature 브랜치는 main-commit-1에서 분기
	featureCommit1, err := createCommitInBranchFromParent(t, repoPath, featureBranch, baseBranch, mainCommit1, "feature1.txt", "Feature branch first commit", "Feature branch first commit content")
	require.NoError(t, err)

	featureCommit2, err := createCommitInRepo(t, repoPath, featureBranch, "feature2.txt", "Feature branch second commit", "Feature branch second commit content")
	require.NoError(t, err)

	// 4. Merge operation 실행
	mergeRequest := map[string]interface{}{
		"source_ref":     featureBranch,
		"target_ref":     baseBranch,
		"method":         mergeMethod,
		"strategy":       "ort",
		"message":        fmt.Sprintf("%s merge from %s to %s", mergeMethod, featureBranch, baseBranch),
		"force_conflict": true, // 충돌이 있어도 강제로 merge 수행
	}

	resp, err := suite.makeRequest("POST", fmt.Sprintf("/repos/%s/%s/merges", owner, repoName), mergeRequest)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	// Merge 성공 확인
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var mergeResponse map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&mergeResponse)
	require.NoError(t, err)
	assert.Equal(t, true, mergeResponse["success"])

	// 5. 최종 commit history 검증
	repo, err := gogit.PlainOpen(repoPath)
	require.NoError(t, err)

	// main 브랜치의 최신 커밋 가져오기
	ref, err := repo.Reference(plumbing.ReferenceName(fmt.Sprintf("refs/heads/%s", baseBranch)), false)
	require.NoError(t, err)

	mainHead, err := repo.CommitObject(ref.Hash())
	require.NoError(t, err)

	// Commit history 수집
	commitHistory := collectCommitHistory(t, repo, mainHead)

	// 6. Merge method별 예상 결과 검증
	switch mergeMethod {
	case "merge":
		validateMergeCommitHistory(t, commitHistory, mainCommit1, mainCommit2, featureCommit1, featureCommit2)
	case "squash":
		validateSquashCommitHistory(t, commitHistory, mainCommit1, mainCommit2, featureCommit1, featureCommit2)
	case "rebase":
		validateRebaseCommitHistory(t, repo, commitHistory, mainCommit1, mainCommit2, featureCommit1, featureCommit2)
	}
}

// createFirstCommitInBranch는 브랜치의 첫 번째 커밋을 생성합니다
func createFirstCommitInBranch(t *testing.T, repoPath, branchName, fileName, commitMessage, fileContent string) (plumbing.Hash, error) {
	repo, err := gogit.PlainOpen(repoPath)
	require.NoError(t, err)

	// 파일을 위한 blob 생성
	blob := repo.Storer.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	blob.SetSize(int64(len(fileContent)))
	writer, err := blob.Writer()
	require.NoError(t, err)
	_, err = writer.Write([]byte(fileContent))
	require.NoError(t, err)
	_ = writer.Close()

	blobHash, err := repo.Storer.SetEncodedObject(blob)
	require.NoError(t, err)

	// Tree 생성
	tree := &object.Tree{
		Entries: []object.TreeEntry{
			{
				Name: fileName,
				Mode: filemode.Regular,
				Hash: blobHash,
			},
		},
	}

	treeObj := repo.Storer.NewEncodedObject()
	err = tree.Encode(treeObj)
	require.NoError(t, err)

	treeHash, err := repo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)

	// 첫 커밋 생성 (parent 없음)
	commit := &object.Commit{
		Message:      commitMessage,
		TreeHash:     treeHash,
		ParentHashes: []plumbing.Hash{},
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
	}

	commitObj := repo.Storer.NewEncodedObject()
	err = commit.Encode(commitObj)
	require.NoError(t, err)

	commitHash, err := repo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	// 브랜치 참조 생성
	branchRefName := plumbing.ReferenceName(fmt.Sprintf("refs/heads/%s", branchName))
	newRef := plumbing.NewHashReference(branchRefName, commitHash)
	err = repo.Storer.SetReference(newRef)
	require.NoError(t, err)

	// main 브랜치인 경우 HEAD도 업데이트
	if branchName == "main" {
		headRef := plumbing.NewSymbolicReference(plumbing.HEAD, branchRefName)
		err = repo.Storer.SetReference(headRef)
		require.NoError(t, err)
	}

	return commitHash, nil
}

// createCommitInBranchFromParent는 특정 parent 커밋에서 브랜치를 생성하고 커밋을 추가합니다
func createCommitInBranchFromParent(t *testing.T, repoPath, branchName, parentBranchName string, parentHash plumbing.Hash, fileName, commitMessage, fileContent string) (plumbing.Hash, error) {
	repo, err := gogit.PlainOpen(repoPath)
	require.NoError(t, err)

	// Parent 커밋 가져오기
	parentCommit, err := repo.CommitObject(parentHash)
	require.NoError(t, err)

	// Parent의 tree 가져오기
	parentTree, err := parentCommit.Tree()
	require.NoError(t, err)

	// 새 파일을 위한 blob 생성
	blob := repo.Storer.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	blob.SetSize(int64(len(fileContent)))
	writer, err := blob.Writer()
	require.NoError(t, err)
	_, err = writer.Write([]byte(fileContent))
	require.NoError(t, err)
	_ = writer.Close()

	blobHash, err := repo.Storer.SetEncodedObject(blob)
	require.NoError(t, err)

	// Tree에 새 파일 추가
	entries := parentTree.Entries
	entries = append(entries, object.TreeEntry{
		Name: fileName,
		Mode: filemode.Regular,
		Hash: blobHash,
	})

	// Tree 정렬 (Git 요구사항)
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[i].Name > entries[j].Name {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}

	// 새 tree 생성
	newTree := &object.Tree{
		Entries: entries,
	}

	treeObj := repo.Storer.NewEncodedObject()
	err = newTree.Encode(treeObj)
	require.NoError(t, err)

	treeHash, err := repo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)

	// 새 커밋 생성
	commit := &object.Commit{
		Message:      commitMessage,
		TreeHash:     treeHash,
		ParentHashes: []plumbing.Hash{parentCommit.Hash},
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
	}

	commitObj := repo.Storer.NewEncodedObject()
	err = commit.Encode(commitObj)
	require.NoError(t, err)

	commitHash, err := repo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	// 브랜치 참조 생성
	branchRefName := plumbing.ReferenceName(fmt.Sprintf("refs/heads/%s", branchName))
	newRef := plumbing.NewHashReference(branchRefName, commitHash)
	err = repo.Storer.SetReference(newRef)
	require.NoError(t, err)

	return commitHash, nil
}

// createCommitInRepo는 지정된 브랜치에 파일을 추가하고 커밋을 생성합니다
func createCommitInRepo(t *testing.T, repoPath, branchName, fileName, commitMessage, fileContent string) (plumbing.Hash, error) {
	repo, err := gogit.PlainOpen(repoPath)
	require.NoError(t, err)

	// 브랜치 참조 가져오기
	branchRefName := plumbing.ReferenceName(fmt.Sprintf("refs/heads/%s", branchName))
	branchRef, err := repo.Reference(branchRefName, false)
	require.NoError(t, err, "브랜치가 존재해야 함: %s", branchName)

	// 기존 커밋 가져오기
	parentCommit, err := repo.CommitObject(branchRef.Hash())
	require.NoError(t, err)

	// 기존 tree 가져오기
	parentTree, err := parentCommit.Tree()
	require.NoError(t, err)

	// 새 파일을 위한 blob 생성
	blob := repo.Storer.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	blob.SetSize(int64(len(fileContent)))
	writer, err := blob.Writer()
	require.NoError(t, err)
	_, err = writer.Write([]byte(fileContent))
	require.NoError(t, err)
	_ = writer.Close()

	blobHash, err := repo.Storer.SetEncodedObject(blob)
	require.NoError(t, err)

	// Tree에 새 파일 추가
	entries := parentTree.Entries
	entries = append(entries, object.TreeEntry{
		Name: fileName,
		Mode: filemode.Regular,
		Hash: blobHash,
	})

	// Tree 정렬 (Git 요구사항)
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[i].Name > entries[j].Name {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}

	// 새 tree 생성
	newTree := &object.Tree{
		Entries: entries,
	}

	treeObj := repo.Storer.NewEncodedObject()
	err = newTree.Encode(treeObj)
	require.NoError(t, err)

	treeHash, err := repo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)

	// 새 커밋 생성
	commit := &object.Commit{
		Message:      commitMessage,
		TreeHash:     treeHash,
		ParentHashes: []plumbing.Hash{parentCommit.Hash},
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
	}

	commitObj := repo.Storer.NewEncodedObject()
	err = commit.Encode(commitObj)
	require.NoError(t, err)

	commitHash, err := repo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	commit.Hash = commitHash

	// 브랜치 참조 업데이트
	newRef := plumbing.NewHashReference(branchRefName, commitHash)
	err = repo.Storer.SetReference(newRef)
	require.NoError(t, err)

	return commitHash, nil
}

// CommitInfo는 커밋 정보를 저장하는 구조체입니다
type CommitInfo struct {
	Hash    string
	Message string
	Parents []string
}

// collectCommitHistory는 주어진 커밋부터 시작하여 모든 부모 커밋의 히스토리를 수집합니다
func collectCommitHistory(t *testing.T, repo *gogit.Repository, startCommit *object.Commit) []CommitInfo {
	var history []CommitInfo
	visited := make(map[plumbing.Hash]bool)
	queue := []*object.Commit{startCommit}

	for len(queue) > 0 {
		commit := queue[0]
		queue = queue[1:]

		// 이미 방문한 커밋은 건너뛰기
		if visited[commit.Hash] {
			continue
		}
		visited[commit.Hash] = true

		// 커밋 정보 저장
		parents := make([]string, len(commit.ParentHashes))
		for i, parentHash := range commit.ParentHashes {
			parents[i] = parentHash.String()
		}

		history = append(history, CommitInfo{
			Hash:    commit.Hash.String(),
			Message: commit.Message,
			Parents: parents,
		})

		// 부모 커밋들을 큐에 추가
		for _, parentHash := range commit.ParentHashes {
			parentCommit, err := repo.CommitObject(parentHash)
			if err == nil {
				queue = append(queue, parentCommit)
			}
		}
	}

	return history
}

// validateMergeCommitHistory는 merge method의 commit history를 검증합니다
// Merge commit의 경우:
// - main 브랜치의 커밋들이 그대로 유지되어야 함
// - feature 브랜치의 커밋들이 그대로 유지되어야 함
// - 최종적으로 merge commit이 생성되어야 함 (2개의 parent를 가져야 함)
func validateMergeCommitHistory(t *testing.T, history []CommitInfo, mainCommit1, mainCommit2, featureCommit1, featureCommit2 plumbing.Hash) {
	// 모든 커밋이 히스토리에 포함되어 있는지 확인
	commitHashes := make(map[string]bool)
	for _, info := range history {
		commitHashes[info.Hash] = true
	}

	// main 브랜치 커밋들이 포함되어 있는지 확인
	assert.True(t, commitHashes[mainCommit1.String()], "main-commit-1이 히스토리에 포함되어야 함")
	assert.True(t, commitHashes[mainCommit2.String()], "main-commit-2가 히스토리에 포함되어야 함")

	// feature 브랜치 커밋들이 포함되어 있는지 확인
	assert.True(t, commitHashes[featureCommit1.String()], "feature-commit-1이 히스토리에 포함되어야 함")
	assert.True(t, commitHashes[featureCommit2.String()], "feature-commit-2가 히스토리에 포함되어야 함")

	// Merge commit 찾기 (2개의 parent를 가진 커밋)
	var mergeCommit *CommitInfo
	for i := range history {
		if len(history[i].Parents) == 2 {
			mergeCommit = &history[i]
			break
		}
	}

	require.NotNil(t, mergeCommit, "Merge commit이 생성되어야 함 (2개의 parent를 가진 커밋)")
	// Merge commit 메시지는 "merge" 또는 커스텀 메시지를 포함할 수 있음
	lowerMsg := strings.ToLower(mergeCommit.Message)
	assert.True(t, strings.Contains(lowerMsg, "merge") ||
		strings.Contains(lowerMsg, "merge from") ||
		strings.Contains(lowerMsg, "merge branch"),
		"Merge commit 메시지에 merge 관련 키워드가 포함되어야 함: %s", mergeCommit.Message)

	// Merge commit의 parent가 main과 feature의 최신 커밋인지 확인
	hasMainParent := false
	hasFeatureParent := false
	for _, parent := range mergeCommit.Parents {
		if parent == mainCommit2.String() {
			hasMainParent = true
		}
		if parent == featureCommit2.String() {
			hasFeatureParent = true
		}
	}
	assert.True(t, hasMainParent, "Merge commit의 parent 중 하나가 main-commit-2여야 함")
	assert.True(t, hasFeatureParent, "Merge commit의 parent 중 하나가 feature-commit-2여야 함")

}

// validateSquashCommitHistory는 squash method의 commit history를 검증합니다
// Squash merge의 경우:
// - main 브랜치의 커밋들이 그대로 유지되어야 함
// - feature 브랜치의 커밋들은 히스토리에 포함되지 않아야 함 (squash되어 하나로 합쳐짐)
// - 새로운 squash 커밋이 생성되어야 함 (1개의 parent만 가져야 함 - main 브랜치)
func validateSquashCommitHistory(t *testing.T, history []CommitInfo, mainCommit1, mainCommit2, featureCommit1, featureCommit2 plumbing.Hash) {
	// 모든 커밋이 히스토리에 포함되어 있는지 확인
	commitHashes := make(map[string]bool)
	for _, info := range history {
		commitHashes[info.Hash] = true
	}

	// main 브랜치 커밋들이 포함되어 있는지 확인
	assert.True(t, commitHashes[mainCommit1.String()], "main-commit-1이 히스토리에 포함되어야 함")
	assert.True(t, commitHashes[mainCommit2.String()], "main-commit-2가 히스토리에 포함되어야 함")

	// feature 브랜치 커밋들은 squash되어 히스토리에 포함되지 않아야 함
	assert.False(t, commitHashes[featureCommit1.String()], "feature-commit-1은 squash되어 히스토리에 포함되지 않아야 함")
	assert.False(t, commitHashes[featureCommit2.String()], "feature-commit-2는 squash되어 히스토리에 포함되지 않아야 함")

	// Squash commit 찾기 (1개의 parent를 가진 커밋이고, main-commit-2를 parent로 가짐)
	// main-commit-1과 main-commit-2가 아닌 커밋 중에서 찾기
	var squashCommit *CommitInfo
	for i := range history {
		// 1개의 parent를 가지고, main-commit-1이나 main-commit-2가 아닌 커밋
		if len(history[i].Parents) == 1 &&
			history[i].Hash != mainCommit1.String() &&
			history[i].Hash != mainCommit2.String() {
			// Parent가 main-commit-2인지 확인
			if history[i].Parents[0] == mainCommit2.String() {
				squashCommit = &history[i]
				break
			}
		}
	}

	require.NotNil(t, squashCommit, "Squash commit이 생성되어야 함 (main-commit-2를 parent로 하는 새로운 커밋)")
	assert.Equal(t, 1, len(squashCommit.Parents), "Squash commit은 1개의 parent만 가져야 함")
	assert.Equal(t, mainCommit2.String(), squashCommit.Parents[0], "Squash commit의 parent가 main-commit-2여야 함")

}

// containsSquashMessage는 메시지에 squash 관련 키워드가 포함되어 있는지 확인합니다
func containsSquashMessage(message string) bool {
	lowerMsg := strings.ToLower(message)
	return strings.Contains(lowerMsg, "squash")
}

// validateRebaseCommitHistory는 rebase method의 commit history를 검증합니다
// Rebase의 경우:
// - main 브랜치의 커밋들이 그대로 유지되어야 함
// - feature 브랜치의 커밋들이 main 위에 재적용되어 선형 히스토리가 되어야 함
// - 각 rebase된 커밋은 1개의 parent만 가져야 함 (선형 구조)
// - feature 커밋들의 메시지는 유지되지만 새로운 hash를 가져야 함 (rebase로 인해)
func validateRebaseCommitHistory(t *testing.T, repo *gogit.Repository, history []CommitInfo, mainCommit1, mainCommit2, featureCommit1, featureCommit2 plumbing.Hash) {
	// 모든 커밋이 히스토리에 포함되어 있는지 확인
	commitHashes := make(map[string]bool)
	for _, info := range history {
		commitHashes[info.Hash] = true
	}

	// main 브랜치 커밋들이 포함되어 있는지 확인
	assert.True(t, commitHashes[mainCommit1.String()], "main-commit-1이 히스토리에 포함되어야 함")
	assert.True(t, commitHashes[mainCommit2.String()], "main-commit-2가 히스토리에 포함되어야 함")

	// Feature 브랜치의 원본 커밋들은 rebase로 인해 새로운 hash를 가지므로
	// 원본 hash는 히스토리에 없을 수 있음
	// 대신 feature 커밋 메시지를 가진 커밋들이 히스토리에 있는지 확인
	featureCommit1Found := false
	featureCommit2Found := false

	for _, info := range history {
		if info.Message == "Feature branch first commit" {
			featureCommit1Found = true
		}
		if info.Message == "Feature branch second commit" {
			featureCommit2Found = true
		}
	}

	assert.True(t, featureCommit1Found, "Feature branch first commit 메시지를 가진 커밋이 히스토리에 있어야 함")
	assert.True(t, featureCommit2Found, "Feature branch second commit 메시지를 가진 커밋이 히스토리에 있어야 함")

	// Rebase된 커밋들은 모두 1개의 parent만 가져야 함 (선형 구조)
	// main 브랜치의 첫 커밋은 parent가 없을 수 있음
	for _, info := range history {
		if info.Message != "main-commit-1" && info.Message != "Initial commit" {
			// Rebase된 커밋들은 모두 1개의 parent를 가져야 함
			assert.LessOrEqual(t, len(info.Parents), 1,
				"Rebase된 커밋은 1개 이하의 parent를 가져야 함 (선형 구조): %s", info.Message)
		}
	}

	// Rebase 후 선형 히스토리 구조 확인
	// main-commit-2 -> feature-commit-1 (rebase) -> feature-commit-2 (rebase) 순서가 되어야 함

	// Rebase된 feature 커밋들이 main 위에 선형으로 쌓여있는지 확인
	assert.True(t, featureCommit1Found && featureCommit2Found,
		"Feature 브랜치의 두 커밋이 모두 rebase되어 히스토리에 있어야 함")

	// Rebase 후 선형 구조 확인 (각 커밋이 1개 이하의 parent를 가져야 함)
	linearStructure := true
	for _, info := range history {
		if info.Message != "Main branch first commit" && len(info.Parents) > 1 {
			linearStructure = false
			break
		}
	}
	assert.True(t, linearStructure, "Rebase 후 히스토리는 선형 구조여야 함 (각 커밋이 1개 이하의 parent)")

}
