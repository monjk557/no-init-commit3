package gitsig

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoWithOneCommit 은 서명 없는 커밋 하나짜리 저장소를 만든다.
func repoWithOneCommit(t *testing.T) (repoPath, sha string) {
	t.Helper()
	repoPath = t.TempDir()

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoPath
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}

	run("init", "-q", "-b", "main")
	run("config", "user.email", "dev@example.com")
	run("config", "user.name", "dev")
	run("config", "commit.gpgsign", "false")
	run("commit", "-q", "--allow-empty", "-m", "첫 커밋")

	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return repoPath, string(out[:40])
}

// 미서명은 위반이 아니라 부재다. unverified 로 표시하면 서명하지 않은 모든 커밋이
// 문제가 있는 것처럼 보인다.
func TestAnnotate_UnsignedCommitIsAbsenceNotFailure(t *testing.T) {
	repoPath, sha := repoWithOneCommit(t)

	sigs := NewStore(nil).Annotate(context.Background(), repoPath, []string{sha})

	sig, ok := sigs[sha]
	if !ok {
		t.Fatalf("판정이 없다: %v", sigs)
	}
	if sig.State != StateUnsigned {
		t.Fatalf("state = %q, want %q", sig.State, StateUnsigned)
	}
}

// push 가 적재한 판정이 조회의 재계산보다 우선한다. 그러지 않으면 앵커를 가진 판정이
// 앵커 없는 판정으로 덮여 verified 배지가 사라진다.
func TestAnnotate_CachedVerdictWins(t *testing.T) {
	repoPath, sha := repoWithOneCommit(t)
	store := NewStore(nil)
	store.Put(repoPath, sha, Verification{State: StateVerified, Reason: ReasonVerified})

	sig := store.Annotate(context.Background(), repoPath, []string{sha})[sha]

	if sig.State != StateVerified {
		t.Fatalf("state = %q, want %q", sig.State, StateVerified)
	}
}

// 다른 저장소의 같은 SHA 가 서로의 판정을 보면 안 된다. fork 는 커밋을 공유하지만
// 신뢰 앵커는 저장소마다 다르다.
func TestStore_KeysAreScopedByRepository(t *testing.T) {
	store := NewStore(nil)
	store.Put("/repo/a", "abc", Verification{State: StateVerified})

	if _, ok := store.cache.Get("/repo/b:abc"); ok {
		t.Fatal("다른 저장소의 판정이 보인다")
	}
}

// 검증 도구가 없어도 서명이 있는 커밋은 미서명이 아니다.
//
// %G? 는 gpg 나 ssh-keygen 이 없으면 서명이 달린 커밋에도 N 을 돌려준다. 그 값으로
// 미서명을 판단하면, 도구가 빠진 환경에서 사용자가 분명히 서명한 커밋이 "서명 없음"
// 으로 표시된다. 실제로 executor 이미지에 gpg 가 없어 그렇게 나왔다.
func TestAnnotate_SignedCommitIsNotUnsignedWhenVerifierIsMissing(t *testing.T) {
	repoPath, _ := repoWithOneCommit(t)
	sha := commitWithSignatureHeader(t, repoPath)

	// PATH 를 비워 gpg / ssh-keygen 을 찾을 수 없게 만든다.
	t.Setenv("PATH", gitDirOnly(t))

	sig := NewStore(nil).Annotate(context.Background(), repoPath, []string{sha})[sha]

	if sig.State == StateUnsigned {
		t.Fatal("서명이 달린 커밋을 미서명으로 판정했다")
	}
	if sig.State != StateKeyUnavailable {
		t.Fatalf("state = %q, want %q", sig.State, StateKeyUnavailable)
	}
}

// commitWithSignatureHeader 는 gpgsig 헤더를 가진 커밋을 만든다.
//
// 실제 서명 대신 헤더만 넣는다. 이 판독이 보는 것은 헤더의 존재이지 서명의 유효성이
// 아니며, 유효성은 앵커가 있는 push 경로가 판정한다.
func commitWithSignatureHeader(t *testing.T, repoPath string) string {
	t.Helper()

	raw := gitOut(t, repoPath, "cat-file", "-p", "HEAD")
	tree := ""
	for _, line := range splitLines(raw) {
		if len(line) > 5 && line[:5] == "tree " {
			tree = line[5:]
			break
		}
	}
	if tree == "" {
		t.Fatal("tree 를 찾지 못했다")
	}

	object := "tree " + tree + "\n" +
		"author dev <dev@example.com> 1700000000 +0900\n" +
		"committer dev <dev@example.com> 1700000000 +0900\n" +
		"gpgsig -----BEGIN PGP SIGNATURE-----\n" +
		" \n" +
		" ZmFrZQ==\n" +
		" -----END PGP SIGNATURE-----\n" +
		"\n서명 헤더가 있는 커밋\n"

	cmd := exec.Command("git", "hash-object", "-t", "commit", "-w", "--stdin")
	cmd.Dir = repoPath
	cmd.Stdin = strings.NewReader(object)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("hash-object: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func gitOut(t *testing.T, repoPath string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

func splitLines(s string) []string { return strings.Split(s, "\n") }

// gitDirOnly 는 git 만 있고 서명 검증 도구는 없는 PATH 를 만든다.
func gitDirOnly(t *testing.T) string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git 을 찾을 수 없다")
	}
	dir := t.TempDir()
	if err := os.Symlink(gitPath, filepath.Join(dir, "git")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	return dir
}
