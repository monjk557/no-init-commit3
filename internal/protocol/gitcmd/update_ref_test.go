package gitcmd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const zeroHash = "0000000000000000000000000000000000000000"

// setupUpdateRefRepo 는 main + 두 커밋(parent, head)을 가진 repo 를 만든다.
func setupUpdateRefRepo(t *testing.T) (repo, parent, head string) {
	t.Helper()
	repo = t.TempDir()
	gitInRepo(t, repo, "init", "-q", "-b", "main")
	gitInRepo(t, repo, "config", "user.name", "tester")
	gitInRepo(t, repo, "config", "user.email", "tester@example.com")
	gitInRepo(t, repo, "commit", "--allow-empty", "-q", "-m", "first")
	parent = gitInRepo(t, repo, "rev-parse", "HEAD")
	gitInRepo(t, repo, "commit", "--allow-empty", "-q", "-m", "second")
	head = gitInRepo(t, repo, "rev-parse", "HEAD")
	return repo, parent, head
}

func TestAtomicRefUpdate_NoOp(t *testing.T) {
	repo, _, _ := setupUpdateRefRepo(t)
	// 빈 업데이트 목록은 아무 일 없이 nil 반환.
	err := AtomicRefUpdate(context.Background(), repo, nil)
	require.NoError(t, err)
}

func TestAtomicRefUpdate_Create(t *testing.T) {
	repo, _, head := setupUpdateRefRepo(t)

	// OldOID 가 zero/빈값이면 create.
	err := AtomicRefUpdate(context.Background(), repo, []RefUpdateCmd{
		{RefName: "refs/heads/feature", OldOID: zeroHash, NewOID: head},
	})
	require.NoError(t, err)

	oid, err := VerifyRef(context.Background(), repo, "refs/heads/feature")
	require.NoError(t, err)
	assert.Equal(t, head, oid)
}

func TestAtomicRefUpdate_CreateWithEmptyOldOID(t *testing.T) {
	repo, _, head := setupUpdateRefRepo(t)

	// OldOID="" 도 create 경로로 처리되어야 한다.
	err := AtomicRefUpdate(context.Background(), repo, []RefUpdateCmd{
		{RefName: "refs/heads/empty-old", OldOID: "", NewOID: head},
	})
	require.NoError(t, err)

	oid, err := VerifyRef(context.Background(), repo, "refs/heads/empty-old")
	require.NoError(t, err)
	assert.Equal(t, head, oid)
}

func TestAtomicRefUpdate_Update(t *testing.T) {
	repo, parent, head := setupUpdateRefRepo(t)

	// 먼저 parent 를 가리키는 ref 생성.
	require.NoError(t, AtomicRefUpdate(context.Background(), repo, []RefUpdateCmd{
		{RefName: "refs/heads/movable", OldOID: zeroHash, NewOID: parent},
	}))

	// parent → head 로 CAS 업데이트.
	err := AtomicRefUpdate(context.Background(), repo, []RefUpdateCmd{
		{RefName: "refs/heads/movable", OldOID: parent, NewOID: head},
	})
	require.NoError(t, err)

	oid, err := VerifyRef(context.Background(), repo, "refs/heads/movable")
	require.NoError(t, err)
	assert.Equal(t, head, oid)
}

func TestAtomicRefUpdate_Delete(t *testing.T) {
	repo, _, head := setupUpdateRefRepo(t)

	require.NoError(t, AtomicRefUpdate(context.Background(), repo, []RefUpdateCmd{
		{RefName: "refs/heads/to-delete", OldOID: zeroHash, NewOID: head},
	}))

	// NewOID=zero → delete.
	err := AtomicRefUpdate(context.Background(), repo, []RefUpdateCmd{
		{RefName: "refs/heads/to-delete", OldOID: head, NewOID: zeroHash},
	})
	require.NoError(t, err)

	_, err = VerifyRef(context.Background(), repo, "refs/heads/to-delete")
	require.Error(t, err, "삭제된 ref 는 더 이상 존재하지 않아야 한다")
}

func TestAtomicRefUpdate_MultipleAtomic(t *testing.T) {
	repo, parent, head := setupUpdateRefRepo(t)

	// 한 트랜잭션 안에서 create + create.
	err := AtomicRefUpdate(context.Background(), repo, []RefUpdateCmd{
		{RefName: "refs/heads/a", OldOID: zeroHash, NewOID: parent},
		{RefName: "refs/heads/b", OldOID: zeroHash, NewOID: head},
	})
	require.NoError(t, err)

	a, err := VerifyRef(context.Background(), repo, "refs/heads/a")
	require.NoError(t, err)
	assert.Equal(t, parent, a)
	b, err := VerifyRef(context.Background(), repo, "refs/heads/b")
	require.NoError(t, err)
	assert.Equal(t, head, b)
}

func TestAtomicRefUpdate_CASFailure(t *testing.T) {
	repo, parent, head := setupUpdateRefRepo(t)

	require.NoError(t, AtomicRefUpdate(context.Background(), repo, []RefUpdateCmd{
		{RefName: "refs/heads/cas", OldOID: zeroHash, NewOID: head},
	}))

	// 잘못된 OldOID(parent) 로 업데이트 시도 → CAS 실패로 트랜잭션 거부.
	err := AtomicRefUpdate(context.Background(), repo, []RefUpdateCmd{
		{RefName: "refs/heads/cas", OldOID: parent, NewOID: parent},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "update-ref")

	// ref 는 여전히 head 를 가리켜야 한다 (트랜잭션 롤백).
	oid, err := VerifyRef(context.Background(), repo, "refs/heads/cas")
	require.NoError(t, err)
	assert.Equal(t, head, oid)
}

func TestVerifyRef_Missing(t *testing.T) {
	repo, _, _ := setupUpdateRefRepo(t)
	_, err := VerifyRef(context.Background(), repo, "refs/heads/does-not-exist")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rev-parse --verify")
}

func TestVerifyRef_HEAD(t *testing.T) {
	repo, _, head := setupUpdateRefRepo(t)
	oid, err := VerifyRef(context.Background(), repo, "HEAD")
	require.NoError(t, err)
	assert.Equal(t, head, oid)
}
