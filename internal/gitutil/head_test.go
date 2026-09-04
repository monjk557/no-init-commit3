package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newBareRepo 는 HEAD 가 defaultBranch 를 가리키는 빈 bare 저장소를 만든다
// (브랜치는 아직 없다 — unborn HEAD).
func newBareRepo(t *testing.T, defaultBranch string) string {
	t.Helper()
	parent := t.TempDir()
	run(t, parent, "init", "-q", "--bare", "-b", defaultBranch, "repo.git")
	return filepath.Join(parent, "repo.git")
}

// seedBranch 는 저장소에 커밋 하나를 만들고 그 커밋으로 브랜치를 만든다.
func seedBranch(t *testing.T, repoPath, branch string) {
	t.Helper()
	tree := strings.TrimSpace(runStdin(t, repoPath, "", "mktree"))
	commit := strings.TrimSpace(runStdin(t, repoPath, "seed\n", "commit-tree", tree))
	run(t, repoPath, "update-ref", BranchRefPrefix+branch, commit)
}

func runStdin(t *testing.T, repoPath, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, string(out))
	}
	return string(out)
}

func TestAdoptHeadIfUnborn_FirstPushOfNonDefaultBranch(t *testing.T) {
	// git init -b main → HEAD=refs/heads/main (unborn). 첫 push 가 develop 만
	// 올린 상황: HEAD 를 develop 으로 승격해야 clone 이 체크아웃할 게 생긴다.
	repo := newBareRepo(t, "main")
	seedBranch(t, repo, "develop")

	adopted, err := AdoptHeadIfUnborn(context.Background(), repo, []string{BranchRefPrefix + "develop"})
	if err != nil {
		t.Fatalf("AdoptHeadIfUnborn: %v", err)
	}
	if adopted != BranchRefPrefix+"develop" {
		t.Fatalf("adopted = %q, want refs/heads/develop", adopted)
	}
	if head := headOf(t, repo); head != BranchRefPrefix+"develop" {
		t.Fatalf("HEAD = %q, want refs/heads/develop", head)
	}
}

func TestAdoptHeadIfUnborn_NoopWhenHeadBorn(t *testing.T) {
	// HEAD 가 실재하는 브랜치를 가리키면 이후 어떤 push 도 default branch 를
	// 바꾸지 못한다 — 여기서 no-op 이 아니면 feature push 마다 기본이 흔들린다.
	repo := newBareRepo(t, "main")
	seedBranch(t, repo, "main")
	seedBranch(t, repo, "feature")

	adopted, err := AdoptHeadIfUnborn(context.Background(), repo, []string{BranchRefPrefix + "feature"})
	if err != nil {
		t.Fatalf("AdoptHeadIfUnborn: %v", err)
	}
	if adopted != "" {
		t.Fatalf("adopted = %q, want no-op", adopted)
	}
	if head := headOf(t, repo); head != BranchRefPrefix+"main" {
		t.Fatalf("HEAD = %q, want refs/heads/main", head)
	}
}

func TestAdoptHeadIfUnborn_PrefersMainThenMaster(t *testing.T) {
	cases := []struct {
		name       string
		branches   []string
		wantBranch string
	}{
		{"main wins", []string{"zebra", "master", "main"}, "main"},
		{"master when no main", []string{"zebra", "master", "alpha"}, "master"},
		{"lexicographic fallback", []string{"zebra", "alpha"}, "alpha"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newBareRepo(t, "trunk")
			refs := make([]string, 0, len(tc.branches))
			for _, b := range tc.branches {
				seedBranch(t, repo, b)
				refs = append(refs, BranchRefPrefix+b)
			}
			adopted, err := AdoptHeadIfUnborn(context.Background(), repo, refs)
			if err != nil {
				t.Fatalf("AdoptHeadIfUnborn: %v", err)
			}
			if adopted != BranchRefPrefix+tc.wantBranch {
				t.Fatalf("adopted = %q, want %s", adopted, BranchRefPrefix+tc.wantBranch)
			}
		})
	}
}

func TestAdoptHeadIfUnborn_IgnoresNonBranchAndMissingCandidates(t *testing.T) {
	repo := newBareRepo(t, "main")
	seedBranch(t, repo, "develop")
	run(t, repo, "tag", "v1", BranchRefPrefix+"develop")

	// tag ref / 존재하지 않는 브랜치만 후보면 승격할 게 없다.
	adopted, err := AdoptHeadIfUnborn(context.Background(), repo,
		[]string{"refs/tags/v1", BranchRefPrefix + "ghost"})
	if err != nil {
		t.Fatalf("AdoptHeadIfUnborn: %v", err)
	}
	if adopted != "" {
		t.Fatalf("adopted = %q, want no-op", adopted)
	}
	if head := headOf(t, repo); head != BranchRefPrefix+"main" {
		t.Fatalf("HEAD = %q, want unchanged", head)
	}
}

func TestAdoptHeadIfUnborn_LeavesDetachedHeadAlone(t *testing.T) {
	repo := newBareRepo(t, "main")
	seedBranch(t, repo, "develop")
	commit := run(t, repo, "rev-parse", BranchRefPrefix+"develop")
	run(t, repo, "update-ref", "--no-deref", "HEAD", commit)

	adopted, err := AdoptHeadIfUnborn(context.Background(), repo, []string{BranchRefPrefix + "develop"})
	if err != nil {
		t.Fatalf("AdoptHeadIfUnborn: %v", err)
	}
	if adopted != "" {
		t.Fatalf("adopted = %q, want no-op on detached HEAD", adopted)
	}
}

func TestAdoptHeadIfUnborn_EmptyCandidates(t *testing.T) {
	repo := newBareRepo(t, "main")
	adopted, err := AdoptHeadIfUnborn(context.Background(), repo, nil)
	if err != nil || adopted != "" {
		t.Fatalf("adopted = %q, err = %v; want no-op", adopted, err)
	}
}

func TestHeadRefNameAndRefExists(t *testing.T) {
	repo := newBareRepo(t, "main")

	head, err := HeadRefName(context.Background(), repo)
	if err != nil {
		t.Fatalf("HeadRefName: %v", err)
	}
	if head != BranchRefPrefix+"main" {
		t.Fatalf("HeadRefName = %q", head)
	}
	// unborn — HEAD 가 가리키지만 실재하지 않는다.
	if RefExists(context.Background(), repo, head) {
		t.Fatal("RefExists = true for unborn HEAD target")
	}

	seedBranch(t, repo, "main")
	if !RefExists(context.Background(), repo, head) {
		t.Fatal("RefExists = false after branch created")
	}
}

func TestSetHeadRef(t *testing.T) {
	repo := newBareRepo(t, "main")
	seedBranch(t, repo, "develop")

	if err := SetHeadRef(context.Background(), repo, BranchRefPrefix+"develop"); err != nil {
		t.Fatalf("SetHeadRef: %v", err)
	}
	if head := headOf(t, repo); head != BranchRefPrefix+"develop" {
		t.Fatalf("HEAD = %q", head)
	}
}

func headOf(t *testing.T, repoPath string) string {
	t.Helper()
	head, err := HeadRefName(context.Background(), repoPath)
	if err != nil {
		t.Fatalf("HeadRefName: %v", err)
	}
	return head
}

// TestAdoptHeadIfUnborn_CloneChecksOutAfterAdoption 은 승격의 실제 효용을
// 확인한다: 승격 전에는 clone 이 아무것도 체크아웃하지 못하고(빈 워킹트리 +
// "remote HEAD refers to nonexistent ref" 경고), 승격 후에는 정상 체크아웃된다.
func TestAdoptHeadIfUnborn_CloneChecksOutAfterAdoption(t *testing.T) {
	repo := newBareRepo(t, "main")
	seedBranch(t, repo, "develop")

	before := t.TempDir()
	run(t, before, "clone", "-q", repo, "checkout-before")
	if _, err := os.Stat(filepath.Join(before, "checkout-before", ".git")); err != nil {
		t.Fatalf("clone 자체는 성공해야 한다: %v", err)
	}
	if headRefOfClone(t, filepath.Join(before, "checkout-before")) != "" {
		t.Fatal("승격 전 clone 은 체크아웃된 브랜치가 없어야 한다 (fixture 전제 확인)")
	}

	if _, err := AdoptHeadIfUnborn(context.Background(), repo, []string{BranchRefPrefix + "develop"}); err != nil {
		t.Fatalf("AdoptHeadIfUnborn: %v", err)
	}

	after := t.TempDir()
	run(t, after, "clone", "-q", repo, "checkout-after")
	if got := headRefOfClone(t, filepath.Join(after, "checkout-after")); got != "develop" {
		t.Fatalf("clone 후 체크아웃 브랜치 = %q, want develop", got)
	}
}

// headRefOfClone 은 clone 된 워킹트리가 실제로 체크아웃한 브랜치명을 준다
// (체크아웃된 브랜치가 없으면 "").
func headRefOfClone(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "symbolic-ref", "--short", "-q", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(out))
	// unborn HEAD 도 symbolic-ref 는 성공한다 — 브랜치 실재 여부로 판별.
	verify := exec.Command("git", "rev-parse", "--verify", "--quiet", name)
	verify.Dir = dir
	if verify.Run() != nil {
		return ""
	}
	return name
}
