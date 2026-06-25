package gitcmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupContentRepo 는 실제 파일(blob)을 가진 두 커밋 체인을 만든다.
// 반환: repoPath, head commit SHA, parent commit SHA, file blob SHA.
func setupContentRepo(t *testing.T) (repo, head, parent, blobOID string) {
	t.Helper()
	repo = t.TempDir()
	gitInRepo(t, repo, "init", "-q", "-b", "main")
	gitInRepo(t, repo, "config", "user.name", "tester")
	gitInRepo(t, repo, "config", "user.email", "tester@example.com")

	require.NoError(t, os.WriteFile(filepath.Join(repo, "file.txt"), []byte("hello world\n"), 0644))
	gitInRepo(t, repo, "add", "file.txt")
	gitInRepo(t, repo, "commit", "-q", "-m", "first commit")
	parent = gitInRepo(t, repo, "rev-parse", "HEAD")

	require.NoError(t, os.WriteFile(filepath.Join(repo, "file.txt"), []byte("second version\n"), 0644))
	gitInRepo(t, repo, "add", "file.txt")
	gitInRepo(t, repo, "commit", "-q", "-m", "second commit\n\nbody line one\nbody line two")
	head = gitInRepo(t, repo, "rev-parse", "HEAD")

	blobOID = gitInRepo(t, repo, "rev-parse", "HEAD:file.txt")
	return repo, head, parent, blobOID
}

func TestGetCommitInfo_Parsed(t *testing.T) {
	repo, head, parent, _ := setupContentRepo(t)

	info, err := GetCommitInfo(context.Background(), repo, head)
	require.NoError(t, err)
	require.NotNil(t, info)

	assert.NotEmpty(t, info.TreeOID)
	require.Len(t, info.ParentOIDs, 1)
	assert.Equal(t, parent, info.ParentOIDs[0])
	assert.Equal(t, "tester", info.AuthorName)
	assert.Equal(t, "tester@example.com", info.AuthorEmail)
	assert.Equal(t, "tester", info.CommitterName)
	assert.Equal(t, "tester@example.com", info.CommitterEmail)
	assert.False(t, info.AuthorDate.IsZero(), "author date should parse")
	assert.False(t, info.CommitterDate.IsZero(), "committer date should parse")
	// 메시지 본문이 보존되어야 한다.
	assert.Contains(t, info.Message, "second commit")
	assert.Contains(t, info.Message, "body line one")
}

func TestGetCommitInfo_RootCommitNoParent(t *testing.T) {
	repo, _, parent, _ := setupContentRepo(t)

	// parent 는 루트 커밋이므로 ParentOIDs 가 비어 있어야 한다.
	info, err := GetCommitInfo(context.Background(), repo, parent)
	require.NoError(t, err)
	assert.Empty(t, info.ParentOIDs)
}

func TestGetCommitInfo_BadOID(t *testing.T) {
	repo, _, _, _ := setupContentRepo(t)
	_, err := GetCommitInfo(context.Background(), repo, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cat-file commit")
}

func TestParseCommitObject_MergeCommit(t *testing.T) {
	// 두 부모를 가진 raw commit object 를 직접 파싱한다.
	raw := "tree 1111111111111111111111111111111111111111\n" +
		"parent 2222222222222222222222222222222222222222\n" +
		"parent 3333333333333333333333333333333333333333\n" +
		"author Alice <alice@example.com> 1700000000 +0900\n" +
		"committer Bob <bob@example.com> 1700000100 -0500\n" +
		"\n" +
		"merge branches"

	info, err := parseCommitObject(raw)
	require.NoError(t, err)
	assert.Equal(t, "1111111111111111111111111111111111111111", info.TreeOID)
	require.Len(t, info.ParentOIDs, 2)
	assert.Equal(t, "Alice", info.AuthorName)
	assert.Equal(t, "alice@example.com", info.AuthorEmail)
	assert.Equal(t, "Bob", info.CommitterName)
	assert.Equal(t, "merge branches", info.Message)
	assert.False(t, info.AuthorDate.IsZero())
}

func TestParseCommitObject_NoMessage(t *testing.T) {
	// 메시지 구분 빈 줄이 없는 비정상 입력 → Message 는 비어 있고 에러는 없어야 한다.
	raw := "tree aaaa\nauthor X <x@x> 1700000000 +0000"
	info, err := parseCommitObject(raw)
	require.NoError(t, err)
	assert.Empty(t, info.Message)
	assert.Equal(t, "aaaa", info.TreeOID)
}

func TestParseIdentity_Variants(t *testing.T) {
	t.Run("정상", func(t *testing.T) {
		name, email, ts := parseIdentity("Jane Doe <jane@example.com> 1700000000 +0000")
		assert.Equal(t, "Jane Doe", name)
		assert.Equal(t, "jane@example.com", email)
		assert.False(t, ts.IsZero())
		assert.Equal(t, int64(1700000000), ts.Unix())
	})

	t.Run("음수_타임존", func(t *testing.T) {
		_, _, ts := parseIdentity("Jane <jane@example.com> 1700000000 -0500")
		assert.Equal(t, int64(1700000000), ts.Unix())
	})

	t.Run("이메일_괄호_없음", func(t *testing.T) {
		// '<' 또는 '>' 가 없으면 전체를 name 으로 반환하고 zero time.
		name, email, ts := parseIdentity("just a name no email")
		assert.Equal(t, "just a name no email", name)
		assert.Empty(t, email)
		assert.True(t, ts.IsZero())
	})

	t.Run("타임스탬프_없음", func(t *testing.T) {
		// '>' 뒤에 timestamp/tz 가 부족하면 zero time.
		name, email, ts := parseIdentity("Jane <jane@example.com>")
		assert.Equal(t, "Jane", name)
		assert.Equal(t, "jane@example.com", email)
		assert.True(t, ts.IsZero())
	})
}

func TestCatBlob_Content(t *testing.T) {
	repo, _, _, blobOID := setupContentRepo(t)

	content, err := CatBlob(context.Background(), repo, blobOID)
	require.NoError(t, err)
	assert.Equal(t, "second version\n", string(content))
}

func TestCatBlob_Missing(t *testing.T) {
	repo, _, _, _ := setupContentRepo(t)
	_, err := CatBlob(context.Background(), repo, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cat-file blob")
}

func TestDiffTree_BetweenCommits(t *testing.T) {
	repo, head, parent, _ := setupContentRepo(t)

	out, err := DiffTree(context.Background(), repo, "-r", parent, head)
	require.NoError(t, err)
	// file.txt 가 변경되었으므로 diff-tree 출력에 등장해야 한다.
	assert.Contains(t, string(out), "file.txt")
}

func TestDiffTree_BadArgs(t *testing.T) {
	repo, _, _, _ := setupContentRepo(t)
	_, err := DiffTree(context.Background(), repo, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "diff-tree")
}

func TestObjectExists_TypesAndMissing(t *testing.T) {
	repo, head, _, blobOID := setupContentRepo(t)

	typ, err := ObjectExists(context.Background(), repo, head)
	require.NoError(t, err)
	assert.Equal(t, "commit", typ)

	typ, err = ObjectExists(context.Background(), repo, blobOID)
	require.NoError(t, err)
	assert.Equal(t, "blob", typ)

	_, err = ObjectExists(context.Background(), repo, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
