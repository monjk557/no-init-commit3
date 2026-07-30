package hooks

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/collabops/git-executor/internal/ruleguard"
)

// gitInRepo runs a git command in dir with a deterministic identity.
func gitInRepo(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=tester", "GIT_AUTHOR_EMAIL=tester@example.com",
		"GIT_COMMITTER_NAME=tester", "GIT_COMMITTER_EMAIL=tester@example.com",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRepoWithCommit builds a repo with one commit on main and returns the repo
// path, the zero hash (creation "old"), and the commit SHA.
func newRepoWithCommit(t *testing.T) (repo, commit string) {
	t.Helper()
	repo = t.TempDir()
	gitInRepo(t, repo, "init", "-q", "-b", "main")
	gitInRepo(t, repo, "commit", "--allow-empty", "-q", "-m", "init")
	return repo, gitInRepo(t, repo, "rev-parse", "HEAD")
}

const zeroHash = "0000000000000000000000000000000000000000"

func TestRunWithRecover_PanicFailsOpen(t *testing.T) {
	// A panic inside the hook must be recovered and yield exit 0 (allow), never a
	// non-zero exit that git would treat as a push rejection.
	if code := runWithRecover(func() int { panic("boom") }); code != 0 {
		t.Fatalf("panic exit = %d, want 0 (fail-open)", code)
	}
	// Normal path passes the inner code through unchanged.
	if code := runWithRecover(func() int { return 1 }); code != 1 {
		t.Fatalf("passthrough exit = %d, want 1", code)
	}
}

func TestParseRefLine(t *testing.T) {
	old, nw, ref, ok := parseRefLine("aaa bbb refs/heads/main")
	if !ok || old != "aaa" || nw != "bbb" || ref != "refs/heads/main" {
		t.Fatalf("parse = %q %q %q %v", old, nw, ref, ok)
	}
	if _, _, _, ok := parseRefLine("only two"); ok {
		t.Fatal("expected not-ok for a 2-field line")
	}
	if _, _, _, ok := parseRefLine(""); ok {
		t.Fatal("expected not-ok for empty line")
	}
}

func TestIsPreReceiveInvocation(t *testing.T) {
	saved := os.Args
	t.Cleanup(func() { os.Args = saved })

	os.Args = []string{"/hooks/pre-receive"}
	if !IsPreReceiveInvocation() {
		t.Error("expected true when argv[0] basename is pre-receive")
	}
	os.Args = []string{"/usr/local/bin/git-executor"}
	if IsPreReceiveInvocation() {
		t.Error("expected false for the normal binary name")
	}
}

func TestEnsureHooksDir_SymlinksToExecutable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hooks")
	got, err := EnsureHooksDir(dir)
	if err != nil {
		t.Fatalf("EnsureHooksDir: %v", err)
	}
	if got != dir {
		t.Fatalf("dir = %q, want %q", got, dir)
	}
	self, _ := os.Executable()
	link, err := os.Readlink(filepath.Join(dir, "pre-receive"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if link != self {
		t.Fatalf("symlink → %q, want %q", link, self)
	}
	// Idempotent: a second call keeps the correct link.
	if _, err := EnsureHooksDir(dir); err != nil {
		t.Fatalf("second EnsureHooksDir: %v", err)
	}
}

// validateEnv sets the GITEXEC_HOOK_* vars a configured hook needs.
func validateEnv(t *testing.T, repoPath, orchURL string) {
	t.Helper()
	t.Setenv(EnvRepoPath, repoPath)
	t.Setenv(EnvOrchestrator, orchURL)
	t.Setenv(EnvRepoID, "7")
	t.Setenv(EnvWorkspaceSlug, "acme")
	t.Setenv(EnvDecisionToken, "tok")
	t.Setenv(EnvUserID, "u-1")
}

func TestRunPreReceive_DegradeOpenWhenUnconfigured(t *testing.T) {
	// No env set → fail-open (exit 0), and stdin is drained.
	for _, k := range []string{EnvRepoPath, EnvOrchestrator, EnvRepoID, EnvDecisionToken} {
		t.Setenv(k, "")
	}
	var errBuf bytes.Buffer
	code := runPreReceive(strings.NewReader("aaa bbb refs/heads/main\n"), &errBuf)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (degrade-open)", code)
	}
}

func TestRunPreReceive_AllowWhenOrchestratorAllows(t *testing.T) {
	repo, commit := newRepoWithCommit(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK) // allowed
	}))
	defer srv.Close()
	validateEnv(t, repo, srv.URL)

	var errBuf bytes.Buffer
	code := runPreReceive(strings.NewReader(zeroHash+" "+commit+" refs/heads/main\n"), &errBuf)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, errBuf.String())
	}
}

func TestRunPreReceive_DenyWhenOrchestratorRejects(t *testing.T) {
	repo, commit := newRepoWithCommit(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"allowed":false,"reason":"linear history required"}`))
	}))
	defer srv.Close()
	validateEnv(t, repo, srv.URL)

	var errBuf bytes.Buffer
	code := runPreReceive(strings.NewReader(zeroHash+" "+commit+" refs/heads/main\n"), &errBuf)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (denied)", code)
	}
	if !strings.Contains(errBuf.String(), "GITEXEC-HOOK-ERR") {
		t.Fatalf("expected denial reason on stderr, got: %s", errBuf.String())
	}
}

func TestRunPreReceive_FailOpenOnTransportError(t *testing.T) {
	repo, commit := newRepoWithCommit(t)
	// Point at a closed server → transport error → fail-open.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	validateEnv(t, repo, url)

	var errBuf bytes.Buffer
	code := runPreReceive(strings.NewReader(zeroHash+" "+commit+" refs/heads/main\n"), &errBuf)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (fail-open on transport error)", code)
	}
}

// TestRunPreReceive_PerRefIndependence is the Gitea CVE-2026-27775 regression
// guard: a batch push of two refs where only one violates must reject only the
// violating ref, and every ref must be validated on its own (no cross-ref cache).
func TestRunPreReceive_PerRefIndependence(t *testing.T) {
	repo, commit := newRepoWithCommit(t)
	// second branch → a distinct ref in the same push.
	gitInRepo(t, repo, "branch", "feature")

	var mu sync.Mutex
	seen := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		mu.Lock()
		// target_value distinguishes the two refs.
		if strings.Contains(string(body), `"target_value":"main"`) {
			seen["main"]++
			mu.Unlock()
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"allowed":false,"reason":"blocked"}`))
			return
		}
		seen["feature"]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	validateEnv(t, repo, srv.URL)

	stdin := zeroHash + " " + commit + " refs/heads/main\n" +
		zeroHash + " " + commit + " refs/heads/feature\n"
	var errBuf bytes.Buffer
	code := runPreReceive(strings.NewReader(stdin), &errBuf)

	if code != 1 {
		t.Fatalf("exit = %d, want 1 (one ref denied)", code)
	}
	mu.Lock()
	defer mu.Unlock()
	// Both refs must have been validated independently — the allowed ref is NOT
	// skipped because an earlier ref was denied, and vice-versa.
	if seen["main"] != 1 || seen["feature"] != 1 {
		t.Fatalf("each ref must be validated once: seen=%v", seen)
	}
}

// TestPrintRejectionFeedback — A안(Phase 1): 원격 deny 시 로컬 violation 을
// ruleset 별로 그룹핑해 client-facing 피드백으로 출력하는지. kind 문자열만 노출
// (kind 의미 미해석 — §6/§7.2), branch prefix 제거, 빈 violation 은 무출력.
func TestPrintRejectionFeedback(t *testing.T) {
	var buf bytes.Buffer
	vs := []ruleguard.RuleViolation{
		{RulesetID: 12, RulesetName: "prod-protection", RulesetLevel: "workspace", Kind: "require_change_request"},
		{RulesetID: 12, RulesetName: "prod-protection", RulesetLevel: "workspace", Kind: "require_approvals_min_count"},
		{RulesetID: 7, RulesetName: "repo-local", RulesetLevel: "repository", Kind: "block_main_branch_push"},
	}
	printRejectionFeedback(&buf, "refs/heads/main", vs)
	out := buf.String()

	for _, want := range []string{
		"blocked by repository ruleset policy",
		`"main"`,                        // branch prefix 제거
		`"prod-protection" (workspace)`, // ruleset 라벨(name+level)
		`"repo-local" (repository)`,
		"· require_change_request", // kind 문자열 그대로
		"· require_approvals_min_count",
		"· block_main_branch_push",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("feedback missing %q\n---\n%s", want, out)
		}
	}
	// 같은 ruleset(12) 은 한 번만 라벨링(그룹핑).
	if strings.Count(out, `"prod-protection"`) != 1 {
		t.Errorf("ruleset 12 label should appear once (grouped), got:\n%s", out)
	}
}

func TestPrintRejectionFeedback_EmptyNoOutput(t *testing.T) {
	var buf bytes.Buffer
	printRejectionFeedback(&buf, "refs/heads/main", nil)
	if buf.Len() != 0 {
		t.Errorf("expected no output for empty violations, got %q", buf.String())
	}
}
