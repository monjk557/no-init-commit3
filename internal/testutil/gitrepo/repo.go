// Package gitrepo provides test helpers for creating and manipulating git repositories.
// 목적: 회귀 테스트용 git repo fixture를 deterministic하게 생성
package gitrepo

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// TestRepo wraps a git repository for testing
type TestRepo struct {
	T        *testing.T
	Dir      string
	Repo     *gogit.Repository
	Worktree *gogit.Worktree

	// Fixed time for deterministic commits
	baseTime time.Time
	timeStep int
}

// Signature returns a deterministic author signature
var DefaultSignature = &object.Signature{
	Name:  "Test Author",
	Email: "test@example.com",
}

// NewRepo creates a new test git repository
// 회귀: "테스트용 레포는 항상 동일한 초기 상태를 가진다"
func NewRepo(t *testing.T) *TestRepo {
	t.Helper()

	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	require.NoError(t, err, "git init failed")

	// Set HEAD to main branch
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main"))
	err = repo.Storer.SetReference(headRef)
	require.NoError(t, err, "set HEAD failed")

	// Configure git user (required for commits)
	cfg, err := repo.Config()
	require.NoError(t, err)
	cfg.User.Name = DefaultSignature.Name
	cfg.User.Email = DefaultSignature.Email
	err = repo.SetConfig(cfg)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err, "worktree failed")

	return &TestRepo{
		T:        t,
		Dir:      dir,
		Repo:     repo,
		Worktree: wt,
		baseTime: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		timeStep: 0,
	}
}

// NewBareRepo creates a bare git repository for testing
func NewBareRepo(t *testing.T) *TestRepo {
	t.Helper()

	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, true)
	require.NoError(t, err, "git init --bare failed")

	return &TestRepo{
		T:        t,
		Dir:      dir,
		Repo:     repo,
		Worktree: nil, // bare repos have no worktree
		baseTime: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		timeStep: 0,
	}
}

// NextTime returns the next deterministic timestamp
// 회귀: "커밋 시간이 동일하면 정렬/순서가 비결정적이 될 수 있음"
func (r *TestRepo) NextTime() time.Time {
	r.timeStep++
	return r.baseTime.Add(time.Duration(r.timeStep) * time.Hour)
}

// Commit creates a commit with a single file
// 회귀: "테스트 커밋은 항상 동일한 해시를 생성해야 함"
func (r *TestRepo) Commit(filename, content, message string) plumbing.Hash {
	r.T.Helper()
	return r.CommitWithTime(filename, content, message, r.NextTime())
}

// CommitWithTime creates a commit with explicit timestamp
func (r *TestRepo) CommitWithTime(filename, content, message string, when time.Time) plumbing.Hash {
	r.T.Helper()

	filePath := filepath.Join(r.Dir, filename)
	dirPath := filepath.Dir(filePath)
	err := os.MkdirAll(dirPath, 0755)
	require.NoError(r.T, err)
	err = os.WriteFile(filePath, []byte(content), 0644)
	require.NoError(r.T, err)

	_, err = r.Worktree.Add(filename)
	require.NoError(r.T, err)

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

// CommitMultiple creates a commit with multiple files
func (r *TestRepo) CommitMultiple(files map[string]string, message string) plumbing.Hash {
	r.T.Helper()

	when := r.NextTime()

	for filename, content := range files {
		filePath := filepath.Join(r.Dir, filename)
		dirPath := filepath.Dir(filePath)
		err := os.MkdirAll(dirPath, 0755)
		require.NoError(r.T, err)
		err = os.WriteFile(filePath, []byte(content), 0644)
		require.NoError(r.T, err)

		_, err = r.Worktree.Add(filename)
		require.NoError(r.T, err)
	}

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

// BranchCreate creates a new branch
func (r *TestRepo) BranchCreate(name string) {
	r.T.Helper()
	err := r.Worktree.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(name),
		Create: true,
	})
	require.NoError(r.T, err, "branch create %s failed", name)
}

// BranchCheckout switches to an existing branch
func (r *TestRepo) BranchCheckout(name string) {
	r.T.Helper()
	err := r.Worktree.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(name),
	})
	require.NoError(r.T, err, "checkout %s failed", name)
}

// BranchDelete deletes a branch
func (r *TestRepo) BranchDelete(name string) {
	r.T.Helper()
	err := r.Repo.Storer.RemoveReference(plumbing.NewBranchReferenceName(name))
	require.NoError(r.T, err, "delete branch %s failed", name)
}

// BranchRename renames a branch
func (r *TestRepo) BranchRename(oldName, newName string) {
	r.T.Helper()

	ref, err := r.Repo.Reference(plumbing.NewBranchReferenceName(oldName), true)
	require.NoError(r.T, err)

	newRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(newName), ref.Hash())
	err = r.Repo.Storer.SetReference(newRef)
	require.NoError(r.T, err)

	err = r.Repo.Storer.RemoveReference(plumbing.NewBranchReferenceName(oldName))
	require.NoError(r.T, err)
}

// TagLightweight creates a lightweight tag
func (r *TestRepo) TagLightweight(name string, target plumbing.Hash) {
	r.T.Helper()
	ref := plumbing.NewHashReference(plumbing.NewTagReferenceName(name), target)
	err := r.Repo.Storer.SetReference(ref)
	require.NoError(r.T, err, "create lightweight tag %s failed", name)
}

// TagAnnotated creates an annotated tag
func (r *TestRepo) TagAnnotated(name, message string, target plumbing.Hash) plumbing.Hash {
	r.T.Helper()
	when := r.NextTime()
	tagRef, err := r.Repo.CreateTag(name, target, &gogit.CreateTagOptions{
		Message: message,
		Tagger: &object.Signature{
			Name:  DefaultSignature.Name,
			Email: DefaultSignature.Email,
			When:  when,
		},
	})
	require.NoError(r.T, err, "create annotated tag %s failed", name)
	return tagRef.Hash()
}

// TagDelete deletes a tag
func (r *TestRepo) TagDelete(name string) {
	r.T.Helper()
	err := r.Repo.DeleteTag(name)
	require.NoError(r.T, err, "delete tag %s failed", name)
}

// HeadHash returns the current HEAD commit hash
func (r *TestRepo) HeadHash() plumbing.Hash {
	r.T.Helper()
	head, err := r.Repo.Head()
	require.NoError(r.T, err)
	return head.Hash()
}

// GetCommit returns a commit object by hash
func (r *TestRepo) GetCommit(hash plumbing.Hash) *object.Commit {
	r.T.Helper()
	commit, err := r.Repo.CommitObject(hash)
	require.NoError(r.T, err)
	return commit
}

// HasBranch checks if a branch exists
func (r *TestRepo) HasBranch(name string) bool {
	_, err := r.Repo.Reference(plumbing.NewBranchReferenceName(name), true)
	return err == nil
}

// HasTag checks if a tag exists
func (r *TestRepo) HasTag(name string) bool {
	_, err := r.Repo.Tag(name)
	return err == nil
}

// CreateRemote creates a remote reference (for testing remote-style refs)
func (r *TestRepo) CreateRemote(name, url string) {
	r.T.Helper()
	_, err := r.Repo.CreateRemote(&config.RemoteConfig{
		Name: name,
		URLs: []string{url},
	})
	require.NoError(r.T, err)
}

// SetFile creates or updates a file (without committing)
func (r *TestRepo) SetFile(filename, content string) {
	r.T.Helper()
	filePath := filepath.Join(r.Dir, filename)
	dirPath := filepath.Dir(filePath)
	err := os.MkdirAll(dirPath, 0755)
	require.NoError(r.T, err)
	err = os.WriteFile(filePath, []byte(content), 0644)
	require.NoError(r.T, err)
}

// DeleteFile deletes a file (without committing)
func (r *TestRepo) DeleteFile(filename string) {
	r.T.Helper()
	filePath := filepath.Join(r.Dir, filename)
	err := os.Remove(filePath)
	require.NoError(r.T, err)
}

// StageFile stages a file for commit
func (r *TestRepo) StageFile(filename string) {
	r.T.Helper()
	_, err := r.Worktree.Add(filename)
	require.NoError(r.T, err)
}

// StageRemove stages a file removal
func (r *TestRepo) StageRemove(filename string) {
	r.T.Helper()
	_, err := r.Worktree.Remove(filename)
	require.NoError(r.T, err)
}

// FileExists checks if a file exists in the repository directory
func (r *TestRepo) FileExists(path string) bool {
	fullPath := filepath.Join(r.Dir, path)
	_, err := os.Stat(fullPath)
	return err == nil
}

// DeleteBranch is an alias for BranchDelete for test compatibility
func (r *TestRepo) DeleteBranch(name string) {
	r.BranchDelete(name)
}

// GetHead returns the current HEAD hash (alias for HeadHash for compatibility)
func (r *TestRepo) GetHead() plumbing.Hash {
	return r.HeadHash()
}
