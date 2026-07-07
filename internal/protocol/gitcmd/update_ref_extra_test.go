package gitcmd

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nonZeroA / nonZeroB are two distinct, well-formed but non-existent object IDs.
// They are only used to satisfy AtomicRefUpdate's create/update/delete branch
// selection in the stdin-write-failure tests (git never resolves them because
// the target directory is not a repository).
const (
	nonZeroA = "1111111111111111111111111111111111111111"
	nonZeroB = "2222222222222222222222222222222222222222"
)

// TestAtomicRefUpdate_MixedTransaction drives create + update + delete inside a
// single atomic transaction so the update and delete happy-path branches are
// exercised alongside create.
func TestAtomicRefUpdate_MixedTransaction(t *testing.T) {
	repo, parent, head := setupUpdateRefRepo(t)

	// Seed two refs we can later update and delete.
	require.NoError(t, AtomicRefUpdate(context.Background(), repo, []RefUpdateCmd{
		{RefName: "refs/heads/to-move", OldOID: zeroHash, NewOID: parent},
		{RefName: "refs/heads/to-remove", OldOID: zeroHash, NewOID: parent},
	}))

	// One transaction: create a new ref, update an existing one (CAS), delete another.
	err := AtomicRefUpdate(context.Background(), repo, []RefUpdateCmd{
		{RefName: "refs/heads/fresh", OldOID: zeroHash, NewOID: head},       // create
		{RefName: "refs/heads/to-move", OldOID: parent, NewOID: head},       // update
		{RefName: "refs/heads/to-remove", OldOID: parent, NewOID: zeroHash}, // delete
	})
	require.NoError(t, err)

	fresh, err := VerifyRef(context.Background(), repo, "refs/heads/fresh")
	require.NoError(t, err)
	assert.Equal(t, head, fresh)

	moved, err := VerifyRef(context.Background(), repo, "refs/heads/to-move")
	require.NoError(t, err)
	assert.Equal(t, head, moved)

	_, err = VerifyRef(context.Background(), repo, "refs/heads/to-remove")
	require.Error(t, err, "deleted ref must not resolve")
}

// TestAtomicRefUpdate_MalformedRefName feeds an invalid ref name so git rejects
// the transaction at prepare/commit time, exercising the cmd.Wait error branch.
func TestAtomicRefUpdate_MalformedRefName(t *testing.T) {
	repo, _, head := setupUpdateRefRepo(t)

	err := AtomicRefUpdate(context.Background(), repo, []RefUpdateCmd{
		// Double-dot and trailing slash make this an invalid refname.
		{RefName: "refs/heads/..bad/", OldOID: zeroHash, NewOID: head},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "update-ref")
}

// TestAtomicRefUpdate_BadNewOID uses a well-formed but non-existent object as
// the new value; git rejects because the object is missing, again driving the
// terminal error path.
func TestAtomicRefUpdate_BadNewOID(t *testing.T) {
	repo, _, _ := setupUpdateRefRepo(t)

	err := AtomicRefUpdate(context.Background(), repo, []RefUpdateCmd{
		{RefName: "refs/heads/missing-obj", OldOID: zeroHash, NewOID: nonZeroA},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "update-ref")
}

// floodCreates / floodDeletes / floodUpdates build large batches so that, when
// git exits early (target is not a repository), the stdin pipe buffer fills and
// the per-command writeField calls fail with a broken pipe — exercising the
// create/update/delete stdin-write error branches deterministically.
const floodN = 100000

func TestAtomicRefUpdate_CreateWriteFailure_NonRepo(t *testing.T) {
	dir := t.TempDir() // plain dir, NOT a git repo

	updates := make([]RefUpdateCmd, floodN)
	for i := range updates {
		updates[i] = RefUpdateCmd{
			RefName: fmt.Sprintf("refs/heads/c%d", i),
			OldOID:  zeroHash, // create
			NewOID:  nonZeroA,
		}
	}

	err := AtomicRefUpdate(context.Background(), dir, updates)
	require.Error(t, err, "create against a non-repo must fail")
}

func TestAtomicRefUpdate_DeleteWriteFailure_NonRepo(t *testing.T) {
	dir := t.TempDir()

	updates := make([]RefUpdateCmd, floodN)
	for i := range updates {
		updates[i] = RefUpdateCmd{
			RefName: fmt.Sprintf("refs/heads/d%d", i),
			OldOID:  nonZeroA,
			NewOID:  zeroHash, // delete
		}
	}

	err := AtomicRefUpdate(context.Background(), dir, updates)
	require.Error(t, err, "delete against a non-repo must fail")
}

func TestAtomicRefUpdate_UpdateWriteFailure_NonRepo(t *testing.T) {
	dir := t.TempDir()

	updates := make([]RefUpdateCmd, floodN)
	for i := range updates {
		updates[i] = RefUpdateCmd{
			RefName: fmt.Sprintf("refs/heads/u%d", i),
			OldOID:  nonZeroA, // non-zero old + non-zero new => update branch
			NewOID:  nonZeroB,
		}
	}

	err := AtomicRefUpdate(context.Background(), dir, updates)
	require.Error(t, err, "update against a non-repo must fail")
}
