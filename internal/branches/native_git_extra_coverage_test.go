package branches

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

// native_git_extra_coverage_test.go — 실제 git 레포로 native-git ahead/behind,
// merge-base, resolve 및 service wrapper를 커버. (origin/main 코드 기준)

// buildAheadBehindRepo는 master/feature가 1커밋씩 분기한 실제 레포를 만들고
// git-dir 경로를 반환한다.
func buildAheadBehindRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "master")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "T")
	run("commit", "--allow-empty", "-q", "-m", "base") // commit A (merge-base)
	run("branch", "feature")
	run("commit", "--allow-empty", "-q", "-m", "master2") // master ahead by 1
	run("checkout", "-q", "feature")
	run("commit", "--allow-empty", "-q", "-m", "feat2") // feature ahead by 1
	run("checkout", "-q", "master")
	return filepath.Join(dir, ".git")
}

func TestNativeMergeBaseAndAheadBehind(t *testing.T) {
	gitDir := buildAheadBehindRepo(t)
	ctx := context.Background()

	mb, err := NativeMergeBase(ctx, gitDir, "master", "feature")
	if err != nil || len(mb) != 40 {
		t.Fatalf("NativeMergeBase=%q err=%v", mb, err)
	}

	ab, err := NativeCalculateAheadBehind(ctx, gitDir, "feature", "master")
	if err != nil {
		t.Fatalf("NativeCalculateAheadBehind err=%v", err)
	}
	// base...branch: left=base(master)쪽 only=behind, right=feature쪽 only=ahead
	if ab.Ahead != 1 || ab.Behind != 1 {
		t.Errorf("ahead/behind = %d/%d want 1/1", ab.Ahead, ab.Behind)
	}

	// service wrapper는 native를 그대로 호출
	ab2, err := CalculateAheadBehind(ctx, gitDir, "feature", "master")
	if err != nil || ab2.Ahead != 1 || ab2.Behind != 1 {
		t.Errorf("CalculateAheadBehind=%+v err=%v", ab2, err)
	}

	// resolve
	sha, err := NativeResolveRef(ctx, gitDir, "feature")
	if err != nil || len(sha) != 40 {
		t.Errorf("NativeResolveRef=%q err=%v", sha, err)
	}
}

func TestNativeGit_ErrorPaths(t *testing.T) {
	gitDir := buildAheadBehindRepo(t)
	ctx := context.Background()

	if _, err := NativeCalculateAheadBehind(ctx, gitDir, "no-such-branch", "master"); err == nil {
		t.Error("expected error for nonexistent branch")
	}
	if _, err := NativeResolveRef(ctx, gitDir, "no-such-ref"); err == nil {
		t.Error("expected error resolving nonexistent ref")
	}
	if _, err := NativeMergeBase(ctx, gitDir, "no-such-1", "no-such-2"); err == nil {
		t.Error("expected error for merge-base of bad refs")
	}
}
