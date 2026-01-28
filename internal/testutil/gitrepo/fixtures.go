package gitrepo

import (
	"os"
	"path/filepath"

	"github.com/stretchr/testify/require"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// =============================================================================
// Orphan Branch (공통 조상 없음)
// 회귀: "공통 조상 없는 레포에서 에러가 409/500으로 뭉개지지 않는다"
// =============================================================================

// MakeOrphanBranch creates a branch with no common ancestor with main
// Returns: main branch tip, orphan branch name, orphan tip hash
func (r *TestRepo) MakeOrphanBranch(orphanName string) (mainTip, orphanTip plumbing.Hash) {
	r.T.Helper()

	// Create main with some commits
	r.Commit("file1.txt", "main content", "Main commit 1")
	mainTip = r.Commit("file2.txt", "main content 2", "Main commit 2")

	// Create orphan branch manually
	// 1. Create a new tree with different content
	when := r.NextTime()
	orphanFilePath := filepath.Join(r.Dir, "orphan.txt")
	err := os.WriteFile(orphanFilePath, []byte("orphan content"), 0644)
	require.NoError(r.T, err)

	// 2. Stage and create commit without parent
	_, err = r.Worktree.Add("orphan.txt")
	require.NoError(r.T, err)

	// Create commit with no parents (orphan)
	blob, err := r.Repo.BlobObject(plumbing.NewHash(""))
	_ = blob // Not used, just checking

	// Manual orphan commit creation
	orphanTip, err = r.Worktree.Commit("Orphan commit", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  DefaultSignature.Name,
			Email: DefaultSignature.Email,
			When:  when,
		},
		Parents: []plumbing.Hash{}, // No parents = orphan
	})
	require.NoError(r.T, err)

	// Create the branch reference
	ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName(orphanName), orphanTip)
	err = r.Repo.Storer.SetReference(ref)
	require.NoError(r.T, err)

	// Switch back to main
	r.BranchCheckout("main")

	return mainTip, orphanTip
}

// =============================================================================
// Criss-Cross History (Multiple Merge Bases)
// 회귀: "criss-cross에서 결과가 순회 순서에 따라 바뀌지 않는다"
// =============================================================================

// CrissCrossResult contains the hashes from criss-cross setup
type CrissCrossResult struct {
	Base1     plumbing.Hash // First common ancestor
	Base2     plumbing.Hash // Second common ancestor (from cross-merge)
	BranchA   plumbing.Hash // Tip of branch A
	BranchB   plumbing.Hash // Tip of branch B
	MergeBase []plumbing.Hash
}

// MakeCrissCrossHistory creates a criss-cross merge scenario
// This creates multiple merge bases which is the hardest case for merge
//
// History:
//
//	      A1---A2---A3 (branch-a)
//	     /   \ /
//	    O     X
//	     \   / \
//	      B1---B2---B3 (branch-b)
func (r *TestRepo) MakeCrissCrossHistory() CrissCrossResult {
	r.T.Helper()

	// Base commit
	base := r.Commit("base.txt", "base", "Base commit")

	// Branch A commits
	r.BranchCreate("branch-a")
	a1 := r.Commit("a1.txt", "a1", "A1 commit")

	// Branch B commits
	r.BranchCheckout("main")
	r.BranchCreate("branch-b")
	b1 := r.Commit("b1.txt", "b1", "B1 commit")

	// Cross-merge: A merges B
	r.BranchCheckout("branch-a")
	a2 := r.CommitMerge(b1, "Merge B into A")

	// Cross-merge: B merges A
	r.BranchCheckout("branch-b")
	b2 := r.CommitMerge(a1, "Merge A into B")

	// Continue both branches
	r.BranchCheckout("branch-a")
	a3 := r.Commit("a3.txt", "a3", "A3 commit")

	r.BranchCheckout("branch-b")
	b3 := r.Commit("b3.txt", "b3", "B3 commit")

	r.BranchCheckout("main")

	return CrissCrossResult{
		Base1:     base,
		Base2:     a2,
		BranchA:   a3,
		BranchB:   b3,
		MergeBase: []plumbing.Hash{a2, b2}, // Multiple merge bases
	}
}

// CommitMerge creates a merge commit with the current HEAD and given parent
func (r *TestRepo) CommitMerge(otherParent plumbing.Hash, message string) plumbing.Hash {
	r.T.Helper()

	head := r.HeadHash()
	when := r.NextTime()

	hash, err := r.Worktree.Commit(message, &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  DefaultSignature.Name,
			Email: DefaultSignature.Email,
			When:  when,
		},
		Parents: []plumbing.Hash{head, otherParent},
	})
	require.NoError(r.T, err)

	return hash
}

// =============================================================================
// Conflict Scenarios
// 회귀: "충돌 감지가 정확해야 merge preview가 올바른 결과를 보여줌"
// =============================================================================

// ConflictSetup contains branches ready to demonstrate conflicts
type ConflictSetup struct {
	BaseBranch     string
	ConflictBranch string
	BaseCommit     plumbing.Hash
	SourceCommit   plumbing.Hash
	TargetCommit   plumbing.Hash
}

// MakeSingleFileConflict creates a single file conflict scenario
// Both branches modify the same line in the same file
func (r *TestRepo) MakeSingleFileConflict() ConflictSetup {
	r.T.Helper()

	// Base commit
	baseCommit := r.Commit("conflict.txt", "line 1\nline 2\nline 3\n", "Base commit")

	// Feature branch: modify line 2
	r.BranchCreate("feature-conflict")
	sourceCommit := r.Commit("conflict.txt", "line 1\nmodified by feature\nline 3\n", "Feature modify")

	// Main branch: also modify line 2 differently
	r.BranchCheckout("main")
	targetCommit := r.Commit("conflict.txt", "line 1\nmodified by main\nline 3\n", "Main modify")

	return ConflictSetup{
		BaseBranch:     "main",
		ConflictBranch: "feature-conflict",
		BaseCommit:     baseCommit,
		SourceCommit:   sourceCommit,
		TargetCommit:   targetCommit,
	}
}

// MakeDeleteVsModifyConflict creates delete vs modify conflict
func (r *TestRepo) MakeDeleteVsModifyConflict() ConflictSetup {
	r.T.Helper()

	// Base commit
	baseCommit := r.Commit("deleteme.txt", "original content", "Base commit")

	// Feature branch: modify file
	r.BranchCreate("feature-delete-conflict")
	sourceCommit := r.Commit("deleteme.txt", "modified content", "Feature modify")

	// Main branch: delete file
	r.BranchCheckout("main")
	r.DeleteFile("deleteme.txt")
	r.StageRemove("deleteme.txt")
	targetCommit := r.CommitStaged("Main delete file")

	return ConflictSetup{
		BaseBranch:     "main",
		ConflictBranch: "feature-delete-conflict",
		BaseCommit:     baseCommit,
		SourceCommit:   sourceCommit,
		TargetCommit:   targetCommit,
	}
}

// MakeAddAddConflict creates add-add conflict (both add same file)
func (r *TestRepo) MakeAddAddConflict() ConflictSetup {
	r.T.Helper()

	// Base commit (without the conflict file)
	baseCommit := r.Commit("base.txt", "base", "Base commit")

	// Feature branch: add new file
	r.BranchCreate("feature-add-conflict")
	sourceCommit := r.Commit("newfile.txt", "content from feature", "Feature add")

	// Main branch: also add same file with different content
	r.BranchCheckout("main")
	targetCommit := r.Commit("newfile.txt", "content from main", "Main add")

	return ConflictSetup{
		BaseBranch:     "main",
		ConflictBranch: "feature-add-conflict",
		BaseCommit:     baseCommit,
		SourceCommit:   sourceCommit,
		TargetCommit:   targetCommit,
	}
}

// MakeRenameConflict creates rename conflict
func (r *TestRepo) MakeRenameConflict() ConflictSetup {
	r.T.Helper()

	// Base commit
	baseCommit := r.Commit("original.txt", "content", "Base commit")

	// Feature branch: rename to one name
	r.BranchCreate("feature-rename-conflict")
	r.RenameFile("original.txt", "renamed-by-feature.txt")
	r.StageRemove("original.txt")
	r.StageFile("renamed-by-feature.txt")
	sourceCommit := r.CommitStaged("Feature rename")

	// Main branch: rename to different name
	r.BranchCheckout("main")
	r.RenameFile("original.txt", "renamed-by-main.txt")
	r.StageRemove("original.txt")
	r.StageFile("renamed-by-main.txt")
	targetCommit := r.CommitStaged("Main rename")

	return ConflictSetup{
		BaseBranch:     "main",
		ConflictBranch: "feature-rename-conflict",
		BaseCommit:     baseCommit,
		SourceCommit:   sourceCommit,
		TargetCommit:   targetCommit,
	}
}

// MakeBinaryConflict creates binary file conflict
func (r *TestRepo) MakeBinaryConflict() ConflictSetup {
	r.T.Helper()

	// Base commit with binary file
	baseCommit := r.CommitBinary("image.png", []byte{0x89, 0x50, 0x4E, 0x47, 0x00, 0x01}, "Base binary")

	// Feature branch: modify binary
	r.BranchCreate("feature-binary-conflict")
	sourceCommit := r.CommitBinary("image.png", []byte{0x89, 0x50, 0x4E, 0x47, 0x00, 0x02}, "Feature binary")

	// Main branch: also modify binary
	r.BranchCheckout("main")
	targetCommit := r.CommitBinary("image.png", []byte{0x89, 0x50, 0x4E, 0x47, 0x00, 0x03}, "Main binary")

	return ConflictSetup{
		BaseBranch:     "main",
		ConflictBranch: "feature-binary-conflict",
		BaseCommit:     baseCommit,
		SourceCommit:   sourceCommit,
		TargetCommit:   targetCommit,
	}
}

// MakeMultipleConflicts creates multiple file conflicts
func (r *TestRepo) MakeMultipleConflicts(count int) ConflictSetup {
	r.T.Helper()

	// Base commit with multiple files
	files := make(map[string]string)
	for i := 0; i < count; i++ {
		files[filepath.Join("dir", "file"+string(rune('a'+i))+".txt")] = "original content"
	}
	baseCommit := r.CommitMultiple(files, "Base commit")

	// Feature branch: modify all files
	r.BranchCreate("feature-multi-conflict")
	for i := 0; i < count; i++ {
		files[filepath.Join("dir", "file"+string(rune('a'+i))+".txt")] = "feature content"
	}
	sourceCommit := r.CommitMultiple(files, "Feature modify all")

	// Main branch: also modify all files
	r.BranchCheckout("main")
	for i := 0; i < count; i++ {
		files[filepath.Join("dir", "file"+string(rune('a'+i))+".txt")] = "main content"
	}
	targetCommit := r.CommitMultiple(files, "Main modify all")

	return ConflictSetup{
		BaseBranch:     "main",
		ConflictBranch: "feature-multi-conflict",
		BaseCommit:     baseCommit,
		SourceCommit:   sourceCommit,
		TargetCommit:   targetCommit,
	}
}

// =============================================================================
// Fast-Forward Scenarios
// =============================================================================

// FastForwardSetup contains branches for fast-forward merge
type FastForwardSetup struct {
	BaseBranch   string
	FeatureBranch string
	BaseCommit   plumbing.Hash
	FeatureTip   plumbing.Hash
}

// MakeFastForwardScenario creates a fast-forward mergeable scenario
func (r *TestRepo) MakeFastForwardScenario() FastForwardSetup {
	r.T.Helper()

	// Main has some commits
	baseCommit := r.Commit("base.txt", "base", "Base commit")

	// Feature extends main linearly
	r.BranchCreate("feature-ff")
	r.Commit("feature1.txt", "f1", "Feature 1")
	featureTip := r.Commit("feature2.txt", "f2", "Feature 2")

	r.BranchCheckout("main")

	return FastForwardSetup{
		BaseBranch:    "main",
		FeatureBranch: "feature-ff",
		BaseCommit:    baseCommit,
		FeatureTip:    featureTip,
	}
}

// MakeNonFastForwardScenario creates a non-fast-forward scenario
func (r *TestRepo) MakeNonFastForwardScenario() FastForwardSetup {
	r.T.Helper()

	// Main has commits
	baseCommit := r.Commit("base.txt", "base", "Base commit")

	// Feature branch
	r.BranchCreate("feature-non-ff")
	featureTip := r.Commit("feature.txt", "feature", "Feature commit")

	// Main advances
	r.BranchCheckout("main")
	r.Commit("main.txt", "main", "Main advance")

	return FastForwardSetup{
		BaseBranch:    "main",
		FeatureBranch: "feature-non-ff",
		BaseCommit:    baseCommit,
		FeatureTip:    featureTip,
	}
}

// =============================================================================
// Helper Methods
// =============================================================================

// CommitStaged commits staged changes
func (r *TestRepo) CommitStaged(message string) plumbing.Hash {
	r.T.Helper()

	when := r.NextTime()
	hash, err := r.Worktree.Commit(message, &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  DefaultSignature.Name,
			Email: DefaultSignature.Email,
			When:  when,
		},
	})
	require.NoError(r.T, err)
	return hash
}

// CommitBinary creates a commit with binary content
func (r *TestRepo) CommitBinary(filename string, content []byte, message string) plumbing.Hash {
	r.T.Helper()

	filePath := filepath.Join(r.Dir, filename)
	dirPath := filepath.Dir(filePath)
	err := os.MkdirAll(dirPath, 0755)
	require.NoError(r.T, err)
	err = os.WriteFile(filePath, content, 0644)
	require.NoError(r.T, err)

	_, err = r.Worktree.Add(filename)
	require.NoError(r.T, err)

	when := r.NextTime()
	hash, err := r.Worktree.Commit(message, &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  DefaultSignature.Name,
			Email: DefaultSignature.Email,
			When:  when,
		},
	})
	require.NoError(r.T, err)
	return hash
}

// RenameFile renames a file (doesn't commit)
func (r *TestRepo) RenameFile(oldName, newName string) {
	r.T.Helper()
	oldPath := filepath.Join(r.Dir, oldName)
	newPath := filepath.Join(r.Dir, newName)
	err := os.Rename(oldPath, newPath)
	require.NoError(r.T, err)
}
