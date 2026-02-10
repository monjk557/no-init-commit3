package tags_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/repositories"
	"github.com/collabops/git-executor/internal/tags"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// 8.1 Create Tag
// =============================================================================

func Test_CreateTag_Lightweight_ReturnsSuccess(t *testing.T) {
	// 회귀: "경량 태그가 정상적으로 생성되어야 한다"
	t.Parallel()

	// Given: 커밋이 있는 repo
	tempDir := t.TempDir()
	_, commitHash := createRepoWithCommit(t, tempDir)

	repoWrapper, err := repositories.OpenRepositorySimple(tempDir)
	require.NoError(t, err)
	defer func() { _ = repoWrapper.Close() }()

	// When: 경량 태그 생성
	req := &tags.CreateTagRequest{
		Name:      "v1.0.0",
		Hash:      commitHash.String(),
		Annotated: false,
	}
	tag, err := tags.CreateTag(repoWrapper, req)

	// Then: 성공
	require.NoError(t, err)
	assert.Equal(t, "v1.0.0", tag.Name)
	assert.Equal(t, commitHash.String(), tag.Hash)
	assert.False(t, tag.IsAnnotated)
}

func Test_CreateTag_Annotated_ReturnsSuccess(t *testing.T) {
	// 회귀: "주석 태그가 메시지/작성자 정보와 함께 정상적으로 생성되어야 한다"
	t.Parallel()

	// Given: 커밋이 있는 repo
	tempDir := t.TempDir()
	_, commitHash := createRepoWithCommit(t, tempDir)

	repoWrapper, err := repositories.OpenRepositorySimple(tempDir)
	require.NoError(t, err)
	defer func() { _ = repoWrapper.Close() }()

	// When: 주석 태그 생성
	req := &tags.CreateTagRequest{
		Name:      "v2.0.0",
		Hash:      commitHash.String(),
		Annotated: true,
		Message:   "Release version 2.0.0",
		Tagger: &tags.Tagger{
			Name:  "Test User",
			Email: "test@example.com",
			Date:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		},
	}
	tag, err := tags.CreateTag(repoWrapper, req)

	// Then: 성공
	require.NoError(t, err)
	assert.Equal(t, "v2.0.0", tag.Name)
	assert.Equal(t, commitHash.String(), tag.Hash)
	assert.True(t, tag.IsAnnotated)
	assert.Equal(t, "Release version 2.0.0", tag.Message)
	assert.NotNil(t, tag.Tagger)
	assert.Equal(t, "Test User", tag.Tagger.Name)
}

func Test_CreateTag_DuplicateName_Returns409(t *testing.T) {
	// 회귀: "중복된 태그 이름으로 생성 시 409를 반환해야 한다"
	t.Parallel()

	// Given: 태그가 이미 존재하는 repo
	tempDir := t.TempDir()
	repo, commitHash := createRepoWithCommit(t, tempDir)

	// 첫 번째 태그 생성
	tagRef := plumbing.NewHashReference(
		plumbing.ReferenceName("refs/tags/v1.0.0"),
		commitHash,
	)
	err := repo.Storer.SetReference(tagRef)
	require.NoError(t, err)

	repoWrapper, err := repositories.OpenRepositorySimple(tempDir)
	require.NoError(t, err)
	defer func() { _ = repoWrapper.Close() }()

	// When: 동일한 이름으로 태그 생성 시도
	req := &tags.CreateTagRequest{
		Name: "v1.0.0",
		Hash: commitHash.String(),
	}
	tag, err := tags.CreateTag(repoWrapper, req)

	// Then: 409 에러 반환
	require.Error(t, err)
	assert.ErrorIs(t, err, tags.ErrTagAlreadyExists)
	assert.Nil(t, tag)
}

func Test_CreateTag_InvalidTarget_Returns400(t *testing.T) {
	// 회귀: "zero hash(형식 오류)에 태그 생성 시 에러를 반환해야 한다"
	t.Parallel()

	// Given: 커밋이 있는 repo
	tempDir := t.TempDir()
	createRepoWithCommit(t, tempDir)

	repoWrapper, err := repositories.OpenRepositorySimple(tempDir)
	require.NoError(t, err)
	defer func() { _ = repoWrapper.Close() }()

	// When: zero hash(형식 오류)에 태그 생성
	zeroHash := "0000000000000000000000000000000000000000"
	req := &tags.CreateTagRequest{
		Name: "v1.0.0",
		Hash: zeroHash,
	}
	tag, err := tags.CreateTag(repoWrapper, req)

	// Then: 에러 반환 (invalid hash)
	require.Error(t, err)
	assert.Nil(t, tag)
	assert.Contains(t, err.Error(), "invalid")
}

func Test_CreateTag_NonExistentCommit_Returns404(t *testing.T) {
	// 회귀: "존재하지 않는 커밋에 태그 생성 시 404를 반환해야 한다"
	t.Parallel()

	// Given: 커밋이 있는 repo
	tempDir := t.TempDir()
	createRepoWithCommit(t, tempDir)

	repoWrapper, err := repositories.OpenRepositorySimple(tempDir)
	require.NoError(t, err)
	defer func() { _ = repoWrapper.Close() }()

	// When: 존재하지 않는 커밋(valid format but non-existent)에 태그 생성
	nonExistentHash := "abcd1234567890abcd1234567890abcd12345678"
	req := &tags.CreateTagRequest{
		Name: "v1.0.0",
		Hash: nonExistentHash,
	}
	tag, err := tags.CreateTag(repoWrapper, req)

	// Then: 에러 반환 (not found)
	require.Error(t, err)
	assert.Nil(t, tag)
	assert.Contains(t, err.Error(), "not found")
}

func Test_CreateTag_FromBranch_ReturnsSuccess(t *testing.T) {
	// 회귀: "브랜치의 HEAD 커밋에서 태그 생성이 정상적으로 동작해야 한다"
	t.Parallel()

	// Given: feature 브랜치가 있는 repo
	tempDir := t.TempDir()
	repo, mainHash := createRepoWithCommit(t, tempDir)

	// feature 브랜치 생성 및 커밋
	wt, err := repo.Worktree()
	require.NoError(t, err)

	branchRef := plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("feature"),
		mainHash,
	)
	err = repo.Storer.SetReference(branchRef)
	require.NoError(t, err)

	err = wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
	})
	require.NoError(t, err)

	featureHash := commitFile(t, wt, tempDir, "feature.txt", "feature content", "feature commit")

	repoWrapper, err := repositories.OpenRepositorySimple(tempDir)
	require.NoError(t, err)
	defer func() { _ = repoWrapper.Close() }()

	// When: feature 브랜치의 커밋에 태그 생성
	req := &tags.CreateTagRequest{
		Name: "feature-v1",
		Hash: featureHash.String(),
	}
	tag, err := tags.CreateTag(repoWrapper, req)

	// Then: 성공
	require.NoError(t, err)
	assert.Equal(t, "feature-v1", tag.Name)
	assert.Equal(t, featureHash.String(), tag.Hash)
}

func Test_CreateTag_FromCommit_ReturnsSuccess(t *testing.T) {
	// 회귀: "특정 커밋 SHA에서 태그 생성이 정상적으로 동작해야 한다"
	t.Parallel()

	// Given: 여러 커밋이 있는 repo
	tempDir := t.TempDir()
	repo, firstHash := createRepoWithCommit(t, tempDir)

	// 두 번째 커밋
	wt, err := repo.Worktree()
	require.NoError(t, err)
	_ = commitFile(t, wt, tempDir, "second.txt", "second", "second commit")

	repoWrapper, err := repositories.OpenRepositorySimple(tempDir)
	require.NoError(t, err)
	defer func() { _ = repoWrapper.Close() }()

	// When: 첫 번째 커밋에 태그 생성 (HEAD가 아닌 과거 커밋)
	req := &tags.CreateTagRequest{
		Name: "v0.1.0",
		Hash: firstHash.String(),
	}
	tag, err := tags.CreateTag(repoWrapper, req)

	// Then: 성공
	require.NoError(t, err)
	assert.Equal(t, "v0.1.0", tag.Name)
	assert.Equal(t, firstHash.String(), tag.Hash)
}

// =============================================================================
// 8.2 Delete Tag
// =============================================================================

func Test_DeleteTag_Existing_ReturnsSuccess(t *testing.T) {
	// 회귀: "존재하는 태그 삭제가 정상적으로 동작해야 한다"
	t.Parallel()

	// Given: 태그가 있는 repo
	tempDir := t.TempDir()
	repo, commitHash := createRepoWithCommit(t, tempDir)

	// 태그 생성
	tagRef := plumbing.NewHashReference(
		plumbing.ReferenceName("refs/tags/v1.0.0"),
		commitHash,
	)
	err := repo.Storer.SetReference(tagRef)
	require.NoError(t, err)

	repoWrapper, err := repositories.OpenRepositorySimple(tempDir)
	require.NoError(t, err)
	defer func() { _ = repoWrapper.Close() }()

	// When: 태그 삭제
	err = tags.DeleteTag(repoWrapper, "v1.0.0")

	// Then: 성공
	require.NoError(t, err)

	// 태그가 삭제되었는지 확인
	_, err = repo.Reference(plumbing.ReferenceName("refs/tags/v1.0.0"), false)
	assert.Error(t, err, "tag should be deleted")
}

func Test_DeleteTag_NonExistent_Returns404(t *testing.T) {
	// 회귀: "존재하지 않는 태그 삭제 시 에러를 반환해야 한다"
	t.Parallel()

	// Given: 태그가 없는 repo
	tempDir := t.TempDir()
	createRepoWithCommit(t, tempDir)

	repoWrapper, err := repositories.OpenRepositorySimple(tempDir)
	require.NoError(t, err)
	defer func() { _ = repoWrapper.Close() }()

	// When: 존재하지 않는 태그 삭제
	err = tags.DeleteTag(repoWrapper, "nonexistent")

	// Then: 에러 반환
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// =============================================================================
// 8.3 List Tags
// =============================================================================

func Test_GetTags_ReturnsAll(t *testing.T) {
	// 회귀: "모든 태그가 반환되어야 한다"
	t.Parallel()

	// Given: 여러 태그가 있는 repo
	tempDir := t.TempDir()
	repo, commitHash := createRepoWithCommit(t, tempDir)

	// 태그 3개 생성
	tagNames := []string{"v1.0.0", "v1.1.0", "v2.0.0"}
	for _, name := range tagNames {
		tagRef := plumbing.NewHashReference(
			plumbing.ReferenceName("refs/tags/"+name),
			commitHash,
		)
		err := repo.Storer.SetReference(tagRef)
		require.NoError(t, err)
	}

	repoWrapper, err := repositories.OpenRepositorySimple(tempDir)
	require.NoError(t, err)
	defer func() { _ = repoWrapper.Close() }()

	// When: 태그 목록 조회
	result, totalCount, err := tags.GetTags(repoWrapper, 100, 0)

	// Then: 모든 태그 반환
	require.NoError(t, err)
	assert.Equal(t, 3, totalCount)
	assert.Len(t, result, 3)
}

func Test_GetTags_EmptyRepo_ReturnsEmptyArray(t *testing.T) {
	// 회귀: "빈 레포에서 빈 배열을 반환해야 한다"
	t.Parallel()

	// Given: 태그가 없는 repo (커밋만 있음)
	tempDir := t.TempDir()
	createRepoWithCommit(t, tempDir)

	repoWrapper, err := repositories.OpenRepositorySimple(tempDir)
	require.NoError(t, err)
	defer func() { _ = repoWrapper.Close() }()

	// When: 태그 목록 조회
	result, totalCount, err := tags.GetTags(repoWrapper, 100, 0)

	// Then: 빈 배열 반환
	require.NoError(t, err)
	assert.Equal(t, 0, totalCount)
	assert.Empty(t, result)
}

func Test_GetTags_Pagination_ReturnsCorrectPage(t *testing.T) {
	// 회귀: "페이지네이션이 정상적으로 동작해야 한다"
	t.Parallel()

	// Given: 10개 태그가 있는 repo
	tempDir := t.TempDir()
	repo, commitHash := createRepoWithCommit(t, tempDir)

	for i := 1; i <= 10; i++ {
		tagRef := plumbing.NewHashReference(
			plumbing.ReferenceName("refs/tags/v"+string(rune('0'+i))),
			commitHash,
		)
		err := repo.Storer.SetReference(tagRef)
		require.NoError(t, err)
	}

	repoWrapper, err := repositories.OpenRepositorySimple(tempDir)
	require.NoError(t, err)
	defer func() { _ = repoWrapper.Close() }()

	// When: limit=3, skip=0으로 조회
	result, totalCount, err := tags.GetTags(repoWrapper, 3, 0)

	// Then: 3개만 반환, totalCount는 10
	require.NoError(t, err)
	assert.Equal(t, 10, totalCount)
	assert.Len(t, result, 3)

	// When: limit=3, skip=3으로 조회 (두 번째 페이지)
	result2, totalCount2, err := tags.GetTags(repoWrapper, 3, 3)

	// Then: 3개 반환
	require.NoError(t, err)
	assert.Equal(t, 10, totalCount2)
	assert.Len(t, result2, 3)
}

func Test_GetTags_AnnotatedAndLightweight_ReturnsBoth(t *testing.T) {
	// 회귀: "annotated와 lightweight 태그가 모두 반환되어야 한다"
	t.Parallel()

	// Given: annotated와 lightweight 태그가 섞인 repo
	tempDir := t.TempDir()
	repo, commitHash := createRepoWithCommit(t, tempDir)

	// Lightweight 태그
	lightRef := plumbing.NewHashReference(
		plumbing.ReferenceName("refs/tags/light-v1"),
		commitHash,
	)
	err := repo.Storer.SetReference(lightRef)
	require.NoError(t, err)

	// Annotated 태그
	tagObj := &object.Tag{
		Name:       "annotated-v1",
		Message:    "Annotated tag message",
		Tagger:     object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
		TargetType: plumbing.CommitObject,
		Target:     commitHash,
	}
	encodedTag := repo.Storer.NewEncodedObject()
	err = tagObj.Encode(encodedTag)
	require.NoError(t, err)

	tagHash, err := repo.Storer.SetEncodedObject(encodedTag)
	require.NoError(t, err)

	annotatedRef := plumbing.NewHashReference(
		plumbing.ReferenceName("refs/tags/annotated-v1"),
		tagHash,
	)
	err = repo.Storer.SetReference(annotatedRef)
	require.NoError(t, err)

	repoWrapper, err := repositories.OpenRepositorySimple(tempDir)
	require.NoError(t, err)
	defer func() { _ = repoWrapper.Close() }()

	// When: 태그 목록 조회
	result, totalCount, err := tags.GetTags(repoWrapper, 100, 0)

	// Then: 2개 반환
	require.NoError(t, err)
	assert.Equal(t, 2, totalCount)

	// annotated와 lightweight 구분 확인
	var lightTag, annotatedTag *tags.Tag
	for i := range result {
		if result[i].Name == "light-v1" {
			lightTag = &result[i]
		}
		if result[i].Name == "annotated-v1" {
			annotatedTag = &result[i]
		}
	}

	require.NotNil(t, lightTag, "lightweight tag should exist")
	assert.False(t, lightTag.IsAnnotated)

	require.NotNil(t, annotatedTag, "annotated tag should exist")
	assert.True(t, annotatedTag.IsAnnotated)
	assert.Equal(t, "Annotated tag message", annotatedTag.Message)
}

func Test_GetTags_SortByName_ReturnsSorted(t *testing.T) {
	// 회귀: "태그가 이름 순으로 정렬되어야 한다"
	t.Parallel()

	// Given: 여러 태그가 있는 repo
	tempDir := t.TempDir()
	repo, commitHash := createRepoWithCommit(t, tempDir)

	tagNames := []string{"z-tag", "a-tag", "m-tag"}
	for _, name := range tagNames {
		tagRef := plumbing.NewHashReference(
			plumbing.ReferenceName("refs/tags/"+name),
			commitHash,
		)
		err := repo.Storer.SetReference(tagRef)
		require.NoError(t, err)
	}

	repoWrapper, err := repositories.OpenRepositorySimple(tempDir)
	require.NoError(t, err)
	defer func() { _ = repoWrapper.Close() }()

	// When: 이름순 정렬로 태그 목록 조회
	result, totalCount, err := tags.GetTagsSorted(repoWrapper, 100, 0, tags.SortByName)

	// Then: 이름순으로 정렬되어 반환
	require.NoError(t, err)
	assert.Equal(t, 3, totalCount)
	require.Len(t, result, 3)

	// 정렬 확인 (a-tag, m-tag, z-tag 순)
	assert.Equal(t, "a-tag", result[0].Name)
	assert.Equal(t, "m-tag", result[1].Name)
	assert.Equal(t, "z-tag", result[2].Name)
}

func Test_GetTags_SortByDate_ReturnsSorted(t *testing.T) {
	// 회귀: "태그가 날짜 순으로 정렬되어야 한다 (최신 먼저)"
	t.Parallel()

	// Given: 다른 날짜의 커밋에 태그가 있는 repo
	tempDir := t.TempDir()
	repo, err := gogit.PlainInit(tempDir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// 첫 번째 커밋 (오래된 것)
	oldTime := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	hash1 := commitFileAt(t, wt, tempDir, "file1.txt", "content1", "commit1", oldTime)

	// 두 번째 커밋 (중간)
	midTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	hash2 := commitFileAt(t, wt, tempDir, "file2.txt", "content2", "commit2", midTime)

	// 세 번째 커밋 (최신)
	newTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	hash3 := commitFileAt(t, wt, tempDir, "file3.txt", "content3", "commit3", newTime)

	// 태그 생성 (순서 섞어서)
	tagRef1 := plumbing.NewHashReference(plumbing.ReferenceName("refs/tags/old-tag"), hash1)
	err = repo.Storer.SetReference(tagRef1)
	require.NoError(t, err)

	tagRef2 := plumbing.NewHashReference(plumbing.ReferenceName("refs/tags/mid-tag"), hash2)
	err = repo.Storer.SetReference(tagRef2)
	require.NoError(t, err)

	tagRef3 := plumbing.NewHashReference(plumbing.ReferenceName("refs/tags/new-tag"), hash3)
	err = repo.Storer.SetReference(tagRef3)
	require.NoError(t, err)

	repoWrapper, err := repositories.OpenRepositorySimple(tempDir)
	require.NoError(t, err)
	defer func() { _ = repoWrapper.Close() }()

	// When: 날짜순 정렬로 태그 목록 조회
	result, totalCount, err := tags.GetTagsSorted(repoWrapper, 100, 0, tags.SortByDate)

	// Then: 날짜 내림차순으로 정렬 (최신 먼저)
	require.NoError(t, err)
	assert.Equal(t, 3, totalCount)
	require.Len(t, result, 3)

	// 정렬 확인 (new-tag, mid-tag, old-tag 순)
	assert.Equal(t, "new-tag", result[0].Name)
	assert.Equal(t, "mid-tag", result[1].Name)
	assert.Equal(t, "old-tag", result[2].Name)
}

// =============================================================================
// Helper Functions
// =============================================================================

func createRepoWithCommit(t *testing.T, tempDir string) (*gogit.Repository, plumbing.Hash) {
	t.Helper()

	repo, err := gogit.PlainInit(tempDir, false)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	hash := commitFile(t, wt, tempDir, "init.txt", "initial content", "initial commit")
	return repo, hash
}

func commitFile(t *testing.T, wt *gogit.Worktree, repoPath, filename, content, message string) plumbing.Hash {
	t.Helper()
	return commitFileAt(t, wt, repoPath, filename, content, message, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
}

func commitFileAt(t *testing.T, wt *gogit.Worktree, repoPath, filename, content, message string, when time.Time) plumbing.Hash {
	t.Helper()

	filePath := filepath.Join(repoPath, filename)
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatalf("write file failed: %v", err)
	}
	if _, err := wt.Add(filename); err != nil {
		t.Fatalf("git add failed: %v", err)
	}

	hash, err := wt.Commit(message, &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@test.com",
			When:  when,
		},
	})
	if err != nil {
		t.Fatalf("commit failed: %v", err)
	}
	return hash
}

// TestResponse is the standard HTTP response type for testing
type TestResponse struct {
	Status int
	Body   string
	Header http.Header
}

//nolint:unused // test helper
func asResp(w *httptest.ResponseRecorder) TestResponse {
	return TestResponse{
		Status: w.Code,
		Body:   w.Body.String(),
		Header: w.Header(),
	}
}

//nolint:unused // test helper
func requireStatus(t *testing.T, resp TestResponse, want int) {
	t.Helper()
	if resp.Status != want {
		t.Fatalf("status=%d want=%d body=%s", resp.Status, want, resp.Body)
	}
}

// marshalJSON is a helper for creating JSON request bodies
//
//nolint:unused // test helper
func marshalJSON(t *testing.T, v interface{}) *bytes.Buffer {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return bytes.NewBuffer(data)
}
