package merge

import (
	"os"
	"path/filepath"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func Test_TreeToSnapshot_NilTree_ReturnsEmptySnapshot(t *testing.T) {
	t.Parallel()

	snapshot, err := TreeToSnapshot(nil, nil)

	if err != nil {
		t.Fatalf("TreeToSnapshot(nil)=%v want=nil error", err)
	}
	if snapshot == nil {
		t.Fatalf("snapshot=nil want=non-nil")
	}
	if !snapshot.IsEmpty() {
		t.Fatalf("IsEmpty()=false want=true for nil tree")
	}
}

func Test_TreeToSnapshot_SingleFile_ReturnsCorrectSnapshot(t *testing.T) {
	t.Parallel()

	repo, tree := createRepoWithFile(t, "test.txt", "content")

	snapshot, err := TreeToSnapshot(tree, repo)

	if err != nil {
		t.Fatalf("TreeToSnapshot()=%v want=nil error", err)
	}
	if !snapshot.HasEntry("test.txt") {
		t.Fatalf("HasEntry(test.txt)=false want=true")
	}

	entry, _ := snapshot.GetEntry("test.txt")
	if entry.Hash == "" {
		t.Fatalf("Hash=empty want=non-empty")
	}
	if entry.IsDir {
		t.Fatalf("IsDir=true want=false for file")
	}
}

func Test_TreeToSnapshot_NestedDirectory_ReturnsCorrectPaths(t *testing.T) {
	t.Parallel()

	repo, tree := createRepoWithFiles(t, map[string]string{
		"root.txt":       "root content",
		"dir/nested.txt": "nested content",
	})

	snapshot, err := TreeToSnapshot(tree, repo)

	if err != nil {
		t.Fatalf("TreeToSnapshot()=%v want=nil error", err)
	}

	if !snapshot.HasEntry("root.txt") {
		t.Fatalf("HasEntry(root.txt)=false want=true")
	}
	if !snapshot.HasEntry("dir/nested.txt") {
		t.Fatalf("HasEntry(dir/nested.txt)=false want=true")
	}
	if !snapshot.HasEntry("dir") {
		t.Fatalf("HasEntry(dir)=false want=true for directory")
	}

	dirEntry, _ := snapshot.GetEntry("dir")
	if !dirEntry.IsDir {
		t.Fatalf("dir.IsDir=false want=true")
	}
}

func Test_TreeToSnapshot_MultipleFiles_ReturnsAllEntries(t *testing.T) {
	t.Parallel()

	files := map[string]string{
		"a.txt": "a",
		"b.txt": "b",
		"c.txt": "c",
	}
	repo, tree := createRepoWithFiles(t, files)

	snapshot, err := TreeToSnapshot(tree, repo)

	if err != nil {
		t.Fatalf("TreeToSnapshot()=%v want=nil error", err)
	}

	paths := snapshot.Paths()
	if len(paths) != 3 {
		t.Fatalf("len(Paths())=%d want=3", len(paths))
	}

	for name := range files {
		if !snapshot.HasEntry(name) {
			t.Fatalf("HasEntry(%s)=false want=true", name)
		}
	}
}

func Test_CommitToSnapshots_ReturnsThreeSnapshots(t *testing.T) {
	t.Parallel()

	repo, sourceCommit, targetCommit := createRepoWithBranches(t)

	base, source, target, err := CommitToSnapshots(repo, sourceCommit, targetCommit)

	if err != nil {
		t.Fatalf("CommitToSnapshots()=%v want=nil error", err)
	}
	if base == nil {
		t.Fatalf("base=nil want=non-nil")
	}
	if source == nil {
		t.Fatalf("source=nil want=non-nil")
	}
	if target == nil {
		t.Fatalf("target=nil want=non-nil")
	}
}

// --- Helper functions ---

func createRepoWithFile(t *testing.T, filename, content string) (*gogit.Repository, *object.Tree) {
	t.Helper()

	tempDir := t.TempDir()
	repo, err := gogit.PlainInit(tempDir, false)
	if err != nil {
		t.Fatalf("PlainInit failed: %v", err)
	}

	filePath := filepath.Join(tempDir, filename)
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree failed: %v", err)
	}

	if _, err := wt.Add(filename); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	hash, err := wt.Commit("initial", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com"},
	})
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	commit, err := repo.CommitObject(hash)
	if err != nil {
		t.Fatalf("CommitObject failed: %v", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("Tree failed: %v", err)
	}

	return repo, tree
}

func createRepoWithFiles(t *testing.T, files map[string]string) (*gogit.Repository, *object.Tree) {
	t.Helper()

	tempDir := t.TempDir()
	repo, err := gogit.PlainInit(tempDir, false)
	if err != nil {
		t.Fatalf("PlainInit failed: %v", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree failed: %v", err)
	}

	for name, content := range files {
		filePath := filepath.Join(tempDir, name)
		dir := filepath.Dir(filePath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("MkdirAll failed: %v", err)
		}
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile failed: %v", err)
		}
		if _, err := wt.Add(name); err != nil {
			t.Fatalf("Add failed: %v", err)
		}
	}

	hash, err := wt.Commit("initial", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com"},
	})
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	commit, err := repo.CommitObject(hash)
	if err != nil {
		t.Fatalf("CommitObject failed: %v", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("Tree failed: %v", err)
	}

	return repo, tree
}

func createRepoWithBranches(t *testing.T) (*gogit.Repository, *object.Commit, *object.Commit) {
	t.Helper()

	tempDir := t.TempDir()
	repo, err := gogit.PlainInit(tempDir, false)
	if err != nil {
		t.Fatalf("PlainInit failed: %v", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree failed: %v", err)
	}

	// Base commit
	basePath := filepath.Join(tempDir, "base.txt")
	if err := os.WriteFile(basePath, []byte("base"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if _, err := wt.Add("base.txt"); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	baseHash, err := wt.Commit("base", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com"},
	})
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Source commit (on top of base)
	sourcePath := filepath.Join(tempDir, "source.txt")
	if err := os.WriteFile(sourcePath, []byte("source"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if _, err := wt.Add("source.txt"); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	sourceHash, err := wt.Commit("source", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com"},
	})
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	sourceCommit, err := repo.CommitObject(sourceHash)
	if err != nil {
		t.Fatalf("CommitObject failed: %v", err)
	}

	// Target commit (same as base for simplicity)
	targetCommit, err := repo.CommitObject(baseHash)
	if err != nil {
		t.Fatalf("CommitObject failed: %v", err)
	}

	return repo, sourceCommit, targetCommit
}
