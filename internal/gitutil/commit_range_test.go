package gitutil

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "config", "user.name", "T")
	run(t, dir, "config", "user.email", "t@example.com")
	return dir
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// commitAs 는 지정한 author 이메일/날짜로 빈 커밋을 만들고 SHA 를 준다.
func commitAs(t *testing.T, dir, msg, email, date string) string {
	t.Helper()
	cmd := exec.Command("git", "commit", "-q", "--allow-empty", "-m", msg)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_EMAIL="+email, "GIT_AUTHOR_DATE="+date,
		"GIT_COMMITTER_EMAIL="+email, "GIT_COMMITTER_DATE="+date)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	return run(t, dir, "rev-parse", "HEAD")
}

func shas(commits []CommitMeta) []string {
	out := make([]string, 0, len(commits))
	for _, c := range commits {
		out = append(out, c.SHA)
	}
	return out
}

func TestListCommitRange_ExistingRef_OnlyNewCommits(t *testing.T) {
	dir := newRepo(t)
	old := commitAs(t, dir, "c1", "a@x.com", "2026-01-01T10:00:00+09:00")
	c2 := commitAs(t, dir, "c2", "b@x.com", "2026-01-02T10:00:00+09:00")
	c3 := commitAs(t, dir, "c3", "b@x.com", "2026-01-03T10:00:00+09:00")

	got, err := ListCommitRange(context.Background(), dir+"/.git", old, c3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].SHA != c3 || got[1].SHA != c2 {
		t.Fatalf("want [c3 c2], got %v", shas(got))
	}
}

// default branch 가 처음 생기는 경우 도달 가능한 커밋 전부가 새 기여다.
// 그 이전에 feature 브랜치로 들어온 커밋은 default 가 아니었으므로 아직 한 번도
// 집계되지 않았다 — 여기서 빼면 영영 세어지지 않는다 (설계 D9).
func TestListCommitRange_FirstDefaultBranchPush_ReturnsAll(t *testing.T) {
	dir := newRepo(t)
	commitAs(t, dir, "c1", "a@x.com", "2026-01-01T10:00:00+09:00")
	c2 := commitAs(t, dir, "c2", "a@x.com", "2026-01-02T10:00:00+09:00")

	got, err := ListCommitRange(context.Background(), dir+"/.git", "", c2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %v", shas(got))
	}
}

func TestListCommitRange_ZeroOldHashTreatedAsFirstPush(t *testing.T) {
	dir := newRepo(t)
	c1 := commitAs(t, dir, "c1", "a@x.com", "2026-01-01T10:00:00+09:00")

	got, err := ListCommitRange(context.Background(), dir+"/.git", zeroHash, c1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SHA != c1 {
		t.Fatalf("want [c1], got %v", shas(got))
	}
}

// author 이메일과 **원본 타임존 오프셋**이 보존되어야 한다 (설계 D8).
func TestListCommitRange_PreservesAuthorEmailAndOffset(t *testing.T) {
	dir := newRepo(t)
	old := commitAs(t, dir, "c1", "a@x.com", "2026-01-01T10:00:00+09:00")
	c2 := commitAs(t, dir, "c2", "dev@collabops.ai", "2026-08-20T08:00:00+09:00")

	got, err := ListCommitRange(context.Background(), dir+"/.git", old, c2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1, got %v", shas(got))
	}
	if got[0].AuthorEmail != "dev@collabops.ai" {
		t.Errorf("email = %q", got[0].AuthorEmail)
	}
	// UTC 로 정규화되면 2026-08-19T23:00:00Z 가 되어 하루가 밀린다.
	if got[0].AuthoredAt != "2026-08-20T08:00:00+09:00" {
		t.Errorf("authored_at = %q, want original offset preserved", got[0].AuthoredAt)
	}
}

func TestListCommitRange_EmptyNewRev(t *testing.T) {
	dir := newRepo(t)
	if _, err := ListCommitRange(context.Background(), dir+"/.git", "", ""); err == nil {
		t.Fatal("want error for empty newRev")
	}
}

func TestIsDefaultBranch(t *testing.T) {
	dir := newRepo(t)
	commitAs(t, dir, "c1", "a@x.com", "2026-01-01T10:00:00+09:00")
	gitDir := dir + "/.git"
	ctx := context.Background()

	for _, tc := range []struct {
		ref  string
		want bool
	}{
		{"refs/heads/main", true},
		{"refs/heads/feature", false},
		{"refs/tags/v1", false},
		{"", false},
	} {
		if got := IsDefaultBranch(ctx, gitDir, tc.ref); got != tc.want {
			t.Errorf("IsDefaultBranch(%q) = %v, want %v", tc.ref, got, tc.want)
		}
	}
}

// default branch 가 아닌 ref 의 push 는 기여로 세지 않는다 (설계 D9).
// 세면 squash 머지 후 default branch 커밋과 중복되고 CR 기여와도 겹친다.
//
// 운영 저장소는 bare 라 HEAD 가 checkout 을 따라 움직이지 않는다. 여기서도 bare
// 사본으로 검증한다 — non-bare 에서 checkout 하면 HEAD 가 옮겨가 판정이 달라진다.
func TestListCommitRangeBestEffort_SkipsNonDefaultBranch(t *testing.T) {
	dir := newRepo(t)
	old := commitAs(t, dir, "c1", "a@x.com", "2026-01-01T10:00:00+09:00")
	run(t, dir, "branch", "feature")
	c2 := commitAs(t, dir, "c2", "b@x.com", "2026-01-03T10:00:00+09:00")

	bare := t.TempDir() + "/repo.git"
	run(t, dir, "clone", "-q", "--bare", dir, bare)
	ctx := context.Background()

	if got := ListCommitRangeBestEffort(ctx, bare, old, c2, "refs/heads/feature"); got != nil {
		t.Fatalf("feature 브랜치는 집계 대상이 아니다, got %v", shas(got))
	}
	if got := ListCommitRangeBestEffort(ctx, bare, old, c2, "refs/heads/main"); len(got) != 1 {
		t.Fatalf("default branch 는 집계해야 한다, got %v", shas(got))
	}
}

// bare 저장소의 HEAD 가 default branch 를 가리킨다 — 운영에서 판정의 근거다.
func TestDefaultBranchRef_Bare(t *testing.T) {
	dir := newRepo(t)
	commitAs(t, dir, "c1", "a@x.com", "2026-01-01T10:00:00+09:00")
	bare := t.TempDir() + "/repo.git"
	run(t, dir, "clone", "-q", "--bare", dir, bare)

	ref, err := DefaultBranchRef(context.Background(), bare)
	if err != nil {
		t.Fatal(err)
	}
	if ref != "refs/heads/main" {
		t.Fatalf("DefaultBranchRef = %q, want refs/heads/main", ref)
	}
}
