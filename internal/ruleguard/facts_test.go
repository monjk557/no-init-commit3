package ruleguard

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitInRepo runs a git command in repoPath with a deterministic identity and
// returns trimmed stdout. Fatal on error.
func gitInRepo(t *testing.T, repoPath string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=tester", "GIT_AUTHOR_EMAIL=tester@example.com",
		"GIT_COMMITTER_NAME=tester", "GIT_COMMITTER_EMAIL=tester@example.com",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, repoPath, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repoPath, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// initRepo creates a non-bare repo with an initial commit on main and returns the
// repo path and the initial commit SHA.
func initRepo(t *testing.T) (repo, first string) {
	t.Helper()
	repo = t.TempDir()
	gitInRepo(t, repo, "init", "-q", "-b", "main")
	writeFile(t, repo, "README.md", "hello\n")
	gitInRepo(t, repo, "add", "README.md")
	gitInRepo(t, repo, "commit", "-q", "-m", "init")
	return repo, gitInRepo(t, repo, "rev-parse", "HEAD")
}

func refMap(facts map[string]any) map[string]any {
	if r, ok := facts["ref"].(map[string]any); ok {
		return r
	}
	return map[string]any{}
}

func commitMap(facts map[string]any) map[string]any {
	if c, ok := facts["commit"].(map[string]any); ok {
		return c
	}
	return map[string]any{}
}

func pushMap(facts map[string]any) map[string]any {
	if p, ok := facts["push"].(map[string]any); ok {
		return p
	}
	return map[string]any{}
}

func TestBuildPushFacts_Creation(t *testing.T) {
	repo, _ := initRepo(t)
	// Build a feature commit, capture its SHA, then remove the ref so the object
	// is dangling — exactly the Point-B state when a branch CREATION is validated
	// (objects indexed, no ref pointing at them yet).
	gitInRepo(t, repo, "checkout", "-q", "-b", "feature")
	writeFile(t, repo, "feature.txt", "feat\n")
	gitInRepo(t, repo, "add", "feature.txt")
	gitInRepo(t, repo, "commit", "-q", "-m", "feature work")
	newTip := gitInRepo(t, repo, "rev-parse", "HEAD")
	gitInRepo(t, repo, "checkout", "-q", "main")
	gitInRepo(t, repo, "branch", "-q", "-D", "feature")

	facts := BuildPushFacts(context.Background(), repo, zeroOID, newTip, "feature")

	ref := refMap(facts)
	if ref["is_creation"] != true || ref["is_update"] != false || ref["is_deletion"] != false {
		t.Fatalf("creation ref facts wrong: %v", ref)
	}
	if commitMap(facts)["is_force_push"] != false {
		t.Errorf("creation must never be a force push: %v", commitMap(facts))
	}
	// The new commit introduces feature.txt; README predates it (already on main).
	paths, _ := pushMap(facts)["file_paths"].([]string)
	if !containsStr(paths, "feature.txt") {
		t.Errorf("expected feature.txt in file_paths, got %v", paths)
	}
	if containsStr(paths, "README.md") {
		t.Errorf("README.md is on main and must not appear in a creation's new files: %v", paths)
	}
}

func TestBuildPushFacts_FastForwardUpdate(t *testing.T) {
	repo, first := initRepo(t)
	writeFile(t, repo, "a.txt", "content\n")
	gitInRepo(t, repo, "add", "a.txt")
	gitInRepo(t, repo, "commit", "-q", "-m", "add a")
	second := gitInRepo(t, repo, "rev-parse", "HEAD")

	facts := BuildPushFacts(context.Background(), repo, first, second, "main")

	ref := refMap(facts)
	if ref["is_update"] != true || ref["is_creation"] != false || ref["is_deletion"] != false {
		t.Fatalf("update ref facts wrong: %v", ref)
	}
	c := commitMap(facts)
	if c["is_force_push"] != false {
		t.Errorf("fast-forward must not be a force push: %v", c)
	}
	if c["is_merge_commit"] != false {
		t.Errorf("linear update must not be a merge commit: %v", c)
	}
	// Only a.txt was introduced between first..second (README predates the range).
	paths, _ := pushMap(facts)["file_paths"].([]string)
	if !containsStr(paths, "a.txt") {
		t.Errorf("expected a.txt in file_paths, got %v", paths)
	}
	if containsStr(paths, "README.md") {
		t.Errorf("README.md predates the range and must not appear: %v", paths)
	}
}

func TestBuildPushFacts_ForcePush(t *testing.T) {
	repo, first := initRepo(t)
	// Build a real commit, capture it, then rewind main and diverge — the old tip
	// is no longer an ancestor of the new tip.
	writeFile(t, repo, "b.txt", "b\n")
	gitInRepo(t, repo, "add", "b.txt")
	gitInRepo(t, repo, "commit", "-q", "-m", "b")
	oldTip := gitInRepo(t, repo, "rev-parse", "HEAD")

	gitInRepo(t, repo, "reset", "-q", "--hard", first)
	writeFile(t, repo, "c.txt", "c\n")
	gitInRepo(t, repo, "add", "c.txt")
	gitInRepo(t, repo, "commit", "-q", "-m", "c")
	newTip := gitInRepo(t, repo, "rev-parse", "HEAD")

	facts := BuildPushFacts(context.Background(), repo, oldTip, newTip, "main")
	if commitMap(facts)["is_force_push"] != true {
		t.Errorf("divergent update must be a force push: %v", commitMap(facts))
	}
}

func TestBuildPushFacts_MergeCommit(t *testing.T) {
	repo, _ := initRepo(t)
	// feature branch off the initial commit
	gitInRepo(t, repo, "checkout", "-q", "-b", "feature")
	writeFile(t, repo, "f.txt", "f\n")
	gitInRepo(t, repo, "add", "f.txt")
	gitInRepo(t, repo, "commit", "-q", "-m", "feature work")

	// advance main so the merge is a true two-parent merge (no fast-forward)
	gitInRepo(t, repo, "checkout", "-q", "main")
	writeFile(t, repo, "m.txt", "m\n")
	gitInRepo(t, repo, "add", "m.txt")
	gitInRepo(t, repo, "commit", "-q", "-m", "main work")
	mainBefore := gitInRepo(t, repo, "rev-parse", "HEAD")

	gitInRepo(t, repo, "merge", "-q", "--no-ff", "-m", "merge feature", "feature")
	merged := gitInRepo(t, repo, "rev-parse", "HEAD")

	facts := BuildPushFacts(context.Background(), repo, mainBefore, merged, "main")
	if commitMap(facts)["is_merge_commit"] != true {
		t.Errorf("range containing a merge commit must report is_merge_commit=true: %v", commitMap(facts))
	}
}

func TestBuildPushFacts_MaxFileSize(t *testing.T) {
	repo, first := initRepo(t)
	big := strings.Repeat("x", 3*1024*1024) // 3 MiB
	writeFile(t, repo, "big.bin", big)
	gitInRepo(t, repo, "add", "big.bin")
	gitInRepo(t, repo, "commit", "-q", "-m", "big")
	second := gitInRepo(t, repo, "rev-parse", "HEAD")

	facts := BuildPushFacts(context.Background(), repo, first, second, "main")
	mb, ok := pushMap(facts)["max_file_size_mb"].(float64)
	if !ok {
		t.Fatalf("max_file_size_mb missing/not float: %v", pushMap(facts))
	}
	if mb < 2.9 || mb > 3.1 {
		t.Errorf("expected ~3 MB, got %v", mb)
	}
}

func TestBuildPushFacts_CommitMetadata(t *testing.T) {
	repo, first := initRepo(t)
	writeFile(t, repo, "x.txt", "x\n")
	gitInRepo(t, repo, "add", "x.txt")
	gitInRepo(t, repo, "commit", "-q", "-m", "fix: message pattern check")
	tip := gitInRepo(t, repo, "rev-parse", "HEAD")

	facts := BuildPushFacts(context.Background(), repo, first, tip, "main")
	c := commitMap(facts)

	msg, _ := c["message"].(string)
	if !strings.Contains(msg, "fix: message pattern check") {
		t.Errorf("commit.message wrong: %q", msg)
	}
	author, _ := c["author"].(map[string]any)
	if author["email"] != "tester@example.com" {
		t.Errorf("commit.author.email wrong: %v", c["author"])
	}
	committer, _ := c["committer"].(map[string]any)
	if committer["email"] != "tester@example.com" {
		t.Errorf("commit.committer.email wrong: %v", c["committer"])
	}
}

// TestBuildPushFacts_AnnotatedTag guards the tag-push enforcement path: a tag
// ref update carries the annotated tag *object* SHA (distinct from the commit),
// yet BuildPushFacts must still surface the target commit's message/author facts
// so message_pattern / author_email rules evaluate for tags. This works because
// every git plumbing call here (cat-file commit, rev-list, merge-base) auto-peels
// a tag object to its commit — this test locks that reliance against regression.
func TestBuildPushFacts_AnnotatedTag(t *testing.T) {
	repo, _ := initRepo(t)
	writeFile(t, repo, "y.txt", "y\n")
	gitInRepo(t, repo, "add", "y.txt")
	gitInRepo(t, repo, "commit", "-q", "-m", "feat: taggable commit")
	commit := gitInRepo(t, repo, "rev-parse", "HEAD")
	gitInRepo(t, repo, "tag", "-a", "v1.0.0", "-m", "release v1.0.0")
	tagObject := gitInRepo(t, repo, "rev-parse", "v1.0.0")
	if tagObject == commit {
		t.Fatalf("annotated tag object must differ from commit (got %s)", tagObject)
	}

	// Tag creation: old = zero, new = the tag object SHA (what a push sends).
	facts := BuildPushFacts(context.Background(), repo, zeroOID, tagObject, "v1.0.0")

	if !refMap(facts)["is_creation"].(bool) {
		t.Errorf("tag creation: ref.is_creation should be true, got %v", refMap(facts))
	}
	c := commitMap(facts)
	if msg, _ := c["message"].(string); !strings.Contains(msg, "feat: taggable commit") {
		t.Errorf("annotated tag: commit.message not peeled through, got %q", msg)
	}
	if author, _ := c["author"].(map[string]any); author["email"] != "tester@example.com" {
		t.Errorf("annotated tag: commit.author.email wrong: %v", c["author"])
	}
}

func TestBuildPushFacts_Deletion(t *testing.T) {
	repo, first := initRepo(t)

	facts := BuildPushFacts(context.Background(), repo, first, zeroOID, "main")

	ref := refMap(facts)
	if ref["is_deletion"] != true || ref["is_update"] != false || ref["is_creation"] != false {
		t.Fatalf("deletion ref facts wrong: %v", ref)
	}
	// A deletion carries no pushed objects — no commit/push facts.
	if _, ok := facts["commit"]; ok {
		t.Errorf("deletion must not carry commit facts: %v", facts)
	}
	if _, ok := facts["push"]; ok {
		t.Errorf("deletion must not carry push facts: %v", facts)
	}
}

func TestIsAncestor(t *testing.T) {
	repo, first := initRepo(t)
	gitInRepo(t, repo, "commit", "-q", "--allow-empty", "-m", "second")
	second := gitInRepo(t, repo, "rev-parse", "HEAD")

	ctx := context.Background()
	if !isAncestor(ctx, repo, first, second) {
		t.Errorf("first must be ancestor of second")
	}
	if isAncestor(ctx, repo, second, first) {
		t.Errorf("second must NOT be ancestor of first")
	}
	if !isAncestor(ctx, repo, first, first) {
		t.Errorf("a commit is its own ancestor (no-op update)")
	}
}

func containsStr(ss []string, target string) bool {
	for _, s := range ss {
		if s == target {
			return true
		}
	}
	return false
}
