package branches

import (
	"os"
	"path/filepath"
	"sort"
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

func makePackedRefsFromLooseR6(t *testing.T, repoRoot string) {
	t.Helper()
	refsDir := filepath.Join(repoRoot, "refs", "heads")
	entries, err := os.ReadDir(refsDir)
	require.NoError(t, err)

	var lines []string
	lines = append(lines, "# pack-refs with: peeled fully-peeled")
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		refPath := filepath.Join(refsDir, e.Name())
		data, err := os.ReadFile(refPath)
		require.NoError(t, err)
		hash := string(data)
		if len(hash) > 0 && hash[len(hash)-1] == '\n' {
			hash = hash[:len(hash)-1]
		}
		lines = append(lines, hash+" refs/heads/"+e.Name())
	}
	packedPath := filepath.Join(repoRoot, "packed-refs")
	f, err := os.OpenFile(packedPath, os.O_WRONLY|os.O_TRUNC|os.O_CREATE, 0644)
	require.NoError(t, err)
	for _, line := range lines {
		_, err = f.WriteString(line + "\n")
		require.NoError(t, err)
	}
	require.NoError(t, f.Close())
	for _, e := range entries {
		if !e.IsDir() {
			require.NoError(t, os.Remove(filepath.Join(refsDir, e.Name())))
		}
	}
}

func createBareRepoWithRefsR6(t *testing.T, repoPath string) (mainHash plumbing.Hash) {
	t.Helper()
	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)
	content := []byte("x\n")
	blobObj := repo.Storer.NewEncodedObject()
	blobObj.SetType(plumbing.BlobObject)
	blobObj.SetSize(int64(len(content)))
	w, _ := blobObj.Writer()
	_, _ = w.Write(content)
	_ = w.Close()
	blobHash, _ := repo.Storer.SetEncodedObject(blobObj)
	treeObj := &object.Tree{Entries: []object.TreeEntry{{Name: "f", Mode: 0100644, Hash: blobHash}}}
	treeEnc := repo.Storer.NewEncodedObject()
	require.NoError(t, treeObj.Encode(treeEnc))
	treeHash, _ := repo.Storer.SetEncodedObject(treeEnc)
	when := object.Signature{Name: "a", Email: "a@b.com"}
	commit := &object.Commit{
		Message: "init", Author: when, Committer: when,
		TreeHash: treeHash,
	}
	commitEnc := repo.Storer.NewEncodedObject()
	require.NoError(t, commit.Encode(commitEnc))
	commitHash, _ := repo.Storer.SetEncodedObject(commitEnc)
	mainHash = commitHash
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), commitHash)))
	require.NoError(t, repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))))
	for _, name := range []string{"branch-0", "branch-1"} {
		ref := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/"+name), commitHash)
		require.NoError(t, repo.Storer.SetReference(ref))
	}
	return mainHash
}

// Test_ListBranches_PackedRefsOnOff_SameResult
// R6_storage: packed-refs 유무와 관계없이 브랜치 목록 결과가 동일해야 함
func Test_ListBranches_PackedRefsOnOff_SameResult(t *testing.T) {
	t.Parallel()

	looseRoot := t.TempDir()
	createBareRepoWithRefsR6(t, looseRoot)

	packedRoot := t.TempDir()
	copyDirR6(t, looseRoot, packedRoot)
	makePackedRefsFromLooseR6(t, packedRoot)

	repoLoose, err := gogit.PlainOpen(looseRoot)
	require.NoError(t, err)
	repoPacked, err := gogit.PlainOpen(packedRoot)
	require.NoError(t, err)

	listLoose, err := GetBranches(repoLoose)
	require.NoError(t, err)
	listPacked, err := GetBranches(repoPacked)
	require.NoError(t, err)

	sort.Slice(listLoose, func(i, j int) bool { return listLoose[i].Name < listLoose[j].Name })
	sort.Slice(listPacked, func(i, j int) bool { return listPacked[i].Name < listPacked[j].Name })

	require.Len(t, listLoose, 3, "loose: main, branch-0, branch-1")
	require.Len(t, listPacked, 3, "packed: same three branches")
	for i := range listLoose {
		require.Equal(t, listLoose[i].Name, listPacked[i].Name, "branch name must match")
		require.Equal(t, listLoose[i].Hash, listPacked[i].Hash, "branch OID must match")
	}
}

// Test_ResolveRef_LooseVsPacked_SameOID
// R6_storage: loose ref와 packed ref로 해석해도 동일한 OID가 나와야 함
func Test_ResolveRef_LooseVsPacked_SameOID(t *testing.T) {
	t.Parallel()

	looseRoot := t.TempDir()
	expectedHash := createBareRepoWithRefsR6(t, looseRoot)

	packedRoot := t.TempDir()
	copyDirR6(t, looseRoot, packedRoot)
	makePackedRefsFromLooseR6(t, packedRoot)

	repoLoose, err := gogit.PlainOpen(looseRoot)
	require.NoError(t, err)
	repoPacked, err := gogit.PlainOpen(packedRoot)
	require.NoError(t, err)

	refLoose, err := repoLoose.Reference(plumbing.ReferenceName("refs/heads/main"), false)
	require.NoError(t, err)
	refPacked, err := repoPacked.Reference(plumbing.ReferenceName("refs/heads/main"), false)
	require.NoError(t, err)

	require.Equal(t, refLoose.Hash(), refPacked.Hash(), "resolved OID must match")
	require.Equal(t, expectedHash, refLoose.Hash(), "OID must be the created commit")
}
