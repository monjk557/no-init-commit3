package merge

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// =============================================================================
// 15.2 R6_storage: "repo 저장 포맷이 달라도 결과가 동일하고 성능만 달라진다"
// =============================================================================

func copyDirR6(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
	require.NoError(t, err)
}

func createRepoWithBranchingR6(t *testing.T, repoPath string) (*gogit.Repository, plumbing.Hash, plumbing.Hash) {
	t.Helper()
	repo, err := gogit.PlainInit(repoPath, false)
	require.NoError(t, err)
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))
	require.NoError(t, repo.Storer.SetReference(headRef))

	blob := []byte("x")
	blobObj := repo.Storer.NewEncodedObject()
	blobObj.SetType(plumbing.BlobObject)
	blobObj.SetSize(int64(len(blob)))
	w, _ := blobObj.Writer()
	_, _ = w.Write(blob)
	_ = w.Close()
	blobHash, _ := repo.Storer.SetEncodedObject(blobObj)
	treeEntry := object.TreeEntry{Name: "f", Mode: 0100644, Hash: blobHash}
	treeObj := &object.Tree{Entries: []object.TreeEntry{treeEntry}}
	treeEnc := repo.Storer.NewEncodedObject()
	require.NoError(t, treeObj.Encode(treeEnc))
	treeHash, _ := repo.Storer.SetEncodedObject(treeEnc)
	when := object.Signature{Name: "a", Email: "a@b.com"}

	commitA := &object.Commit{Message: "A", Author: when, Committer: when, TreeHash: treeHash}
	encA := repo.Storer.NewEncodedObject()
	require.NoError(t, commitA.Encode(encA))
	hashA, _ := repo.Storer.SetEncodedObject(encA)
	commitB := &object.Commit{Message: "B", Author: when, Committer: when, TreeHash: treeHash, ParentHashes: []plumbing.Hash{hashA}}
	encB := repo.Storer.NewEncodedObject()
	require.NoError(t, commitB.Encode(encB))
	hashB, _ := repo.Storer.SetEncodedObject(encB)
	commitC := &object.Commit{Message: "C", Author: when, Committer: when, TreeHash: treeHash, ParentHashes: []plumbing.Hash{hashB}}
	encC := repo.Storer.NewEncodedObject()
	require.NoError(t, commitC.Encode(encC))
	hashC, _ := repo.Storer.SetEncodedObject(encC)
	commitD := &object.Commit{Message: "D", Author: when, Committer: when, TreeHash: treeHash, ParentHashes: []plumbing.Hash{hashB}}
	encD := repo.Storer.NewEncodedObject()
	require.NoError(t, commitD.Encode(encD))
	hashD, _ := repo.Storer.SetEncodedObject(encD)

	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), hashC)))
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/feature"), hashD)))
	return repo, hashC, hashD
}

func runGitCommitGraphWriteR6(t *testing.T, repoPath string) error {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		return err
	}
	gitDir := filepath.Join(repoPath, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		return err
	}
	cmd := exec.Command("git", "-C", repoPath, "commit-graph", "write", "--reachable")
	return cmd.Run()
}

// Test_MergeBase_WithCommitGraphEnabled_SameResult
// R6_storage: commit-graph 유무와 관계없이 merge base 결과가 동일해야 함
func Test_MergeBase_WithCommitGraphEnabled_SameResult(t *testing.T) {
	t.Parallel()

	dirA := t.TempDir()
	repo, mainHash, featureHash := createRepoWithBranchingR6(t, dirA)
	commitMain, err := repo.CommitObject(mainHash)
	require.NoError(t, err)
	commitFeature, err := repo.CommitObject(featureHash)
	require.NoError(t, err)
	mergeBases, err := commitFeature.MergeBase(commitMain)
	require.NoError(t, err)
	require.Len(t, mergeBases, 1)
	expectedBase := mergeBases[0].Hash

	dirB := t.TempDir()
	copyDirR6(t, dirA, dirB)

	if err := runGitCommitGraphWriteR6(t, dirA); err != nil {
		t.Skipf("git commit-graph write not available: %v", err)
	}

	repoA, err := gogit.PlainOpen(dirA)
	require.NoError(t, err)
	repoB, err := gogit.PlainOpen(dirB)
	require.NoError(t, err)

	commitMainA, _ := repoA.CommitObject(mainHash)
	commitFeatureA, _ := repoA.CommitObject(featureHash)
	commitMainB, _ := repoB.CommitObject(mainHash)
	commitFeatureB, _ := repoB.CommitObject(featureHash)

	basesA, err := commitFeatureA.MergeBase(commitMainA)
	require.NoError(t, err)
	require.Len(t, basesA, 1)
	basesB, err := commitFeatureB.MergeBase(commitMainB)
	require.NoError(t, err)
	require.Len(t, basesB, 1)

	require.Equal(t, expectedBase, basesA[0].Hash, "merge base with commit-graph")
	require.Equal(t, expectedBase, basesB[0].Hash, "merge base without commit-graph")
	require.Equal(t, basesA[0].Hash, basesB[0].Hash, "merge base must be same regardless of commit-graph")
}
