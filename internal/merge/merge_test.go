package merge

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// TestFindMergeBase_NormalCase tests finding merge base in normal branching scenario
func TestFindMergeBase_NormalCase(t *testing.T) {
	repo, cleanup := setupTestRepository(t)
	defer cleanup()

	handler := setupTestHandler(t)

	// Create commit history:
	// A---B---C (main)
	//      \
	//       D---E (feature)
	// merge base should be B

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Initial commit A
	writeFile(t, wt, "file1.txt", "content1")
	_, err = commitFile(t, wt, "file1.txt", "Commit A")
	require.NoError(t, err)

	// Commit B on main
	writeFile(t, wt, "file2.txt", "content2")
	commitB, err := commitFile(t, wt, "file2.txt", "Commit B")
	require.NoError(t, err)

	// Create feature branch from B
	err = wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	require.NoError(t, err)

	// Commit D on feature
	writeFile(t, wt, "file3.txt", "content3")
	_, err = commitFile(t, wt, "file3.txt", "Commit D")
	require.NoError(t, err)

	// Commit E on feature
	writeFile(t, wt, "file4.txt", "content4")
	commitE, err := commitFile(t, wt, "file4.txt", "Commit E")
	require.NoError(t, err)

	// Switch back to main
	err = wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	})
	require.NoError(t, err)

	// Commit C on main
	writeFile(t, wt, "file5.txt", "content5")
	commitC, err := commitFile(t, wt, "file5.txt", "Commit C")
	require.NoError(t, err)

	// Test findMergeBase
	mergeBase, err := handler.findMergeBase(repo, commitE, commitC)
	require.NoError(t, err)
	require.NotNil(t, mergeBase)
	assert.Equal(t, commitB.String(), mergeBase.Hash.String(), "Merge base should be commit B")
}

// TestFindMergeBase_SameCommit tests merge base when commits are the same
func TestFindMergeBase_SameCommit(t *testing.T) {
	repo, cleanup := setupTestRepository(t)
	defer cleanup()

	handler := setupTestHandler(t)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Single commit
	writeFile(t, wt, "file1.txt", "content1")
	commitA, err := commitFile(t, wt, "file1.txt", "Commit A")
	require.NoError(t, err)

	// Test merge base of same commit
	mergeBase, err := handler.findMergeBase(repo, commitA, commitA)
	require.NoError(t, err)
	require.NotNil(t, mergeBase)
	assert.Equal(t, commitA.String(), mergeBase.Hash.String(), "Merge base of same commit should be itself")
}

// TestFindMergeBase_FastForward tests merge base in fast-forward scenario
func TestFindMergeBase_FastForward(t *testing.T) {
	repo, cleanup := setupTestRepository(t)
	defer cleanup()

	handler := setupTestHandler(t)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Linear history: A---B---C
	// C is ancestor of B, so merge base is B

	writeFile(t, wt, "file1.txt", "content1")
	_, err = commitFile(t, wt, "file1.txt", "Commit A")
	require.NoError(t, err)

	writeFile(t, wt, "file2.txt", "content2")
	commitB, err := commitFile(t, wt, "file2.txt", "Commit B")
	require.NoError(t, err)

	writeFile(t, wt, "file3.txt", "content3")
	commitC, err := commitFile(t, wt, "file3.txt", "Commit C")
	require.NoError(t, err)

	// Test merge base in fast-forward case
	// C and B's merge base is B (B is ancestor of C)
	mergeBase, err := handler.findMergeBase(repo, commitC, commitB)
	require.NoError(t, err)
	require.NotNil(t, mergeBase)
	assert.Equal(t, commitB.String(), mergeBase.Hash.String(), "Merge base should be commit B (the ancestor)")
}

// TestCalculateCommitCounts tests ahead/behind commit counting
func TestCalculateCommitCounts(t *testing.T) {
	repo, cleanup := setupTestRepository(t)
	defer cleanup()

	handler := setupTestHandler(t)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create history: A---B---C (main)
	//                  \
	//                   D---E (feature)
	// merge base is B

	writeFile(t, wt, "file1.txt", "content1")
	_, err = commitFile(t, wt, "file1.txt", "Commit A")
	require.NoError(t, err)

	writeFile(t, wt, "file2.txt", "content2")
	commitB, err := commitFile(t, wt, "file2.txt", "Commit B")
	require.NoError(t, err)

	// Create feature branch
	err = wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	})
	require.NoError(t, err)

	writeFile(t, wt, "file3.txt", "content3")
	_, err = commitFile(t, wt, "file3.txt", "Commit D")
	require.NoError(t, err)

	writeFile(t, wt, "file4.txt", "content4")
	commitE, err := commitFile(t, wt, "file4.txt", "Commit E")
	require.NoError(t, err)

	// Switch back to main
	err = wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	})
	require.NoError(t, err)

	writeFile(t, wt, "file5.txt", "content5")
	commitC, err := commitFile(t, wt, "file5.txt", "Commit C")
	require.NoError(t, err)

	// Get commit objects
	commitEObj, err := repo.CommitObject(commitE)
	require.NoError(t, err)
	commitCObj, err := repo.CommitObject(commitC)
	require.NoError(t, err)
	commitBObj, err := repo.CommitObject(commitB)
	require.NoError(t, err)

	// Test calculateCommitCounts
	// feature (E) vs main (C): ahead=2 (D, E), behind=1 (C)
	stats, err := handler.calculateCommitCounts(repo, commitEObj, commitCObj, commitBObj)
	require.NoError(t, err)
	require.NotNil(t, stats)
	assert.Equal(t, 2, stats.Ahead, "feature should be 2 commits ahead")
	assert.Equal(t, 1, stats.Behind, "feature should be 1 commit behind")
}

// Helper functions are defined in handler_test.go and shared across test files
