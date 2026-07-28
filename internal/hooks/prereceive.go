// Package hooks implements the git pre-receive hook that enforces ruleset rules
// on SSH pushes — the SSH counterpart of the HTTP "Point-B" validation in
// internal/protocol/receive_pack.go. It is the only post-index / pre-ref-update
// injection point in native git-receive-pack (Issue-6237, see
// docs/design/ssh-push-rule-enforcement.md).
//
// The git-executor binary doubles as the hook (multi-call binary): when invoked
// as ".../pre-receive" via a symlink in the shared core.hooksPath dir,
// IsPreReceiveInvocation reports true and main runs RunPreReceive instead of the
// server. git feeds "<old> <new> <ref>" lines on stdin; exit 0 accepts, non-zero
// rejects. Context (repo path/id, workspace slug, decision token, user id,
// orchestrator URL) arrives via GITEXEC_HOOK_* env vars set by the SSH
// receive-pack spawn.
//
// The pushed objects live in git's quarantine directory during pre-receive;
// BuildPushFacts' git commands inherit this process's env (GIT_OBJECT_DIRECTORY
// et al. that git sets), so they see the new objects. We never clobber that env.
package hooks

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/ruleguard"
)

// hookName is the git hook this binary services. git invokes
// <core.hooksPath>/pre-receive; we symlink that name to the executable.
const hookName = "pre-receive"

// GITEXEC_HOOK_* env vars carry the push context into the hook process. Set by
// the SSH receive-pack spawn in internal/server; read here.
const (
	EnvRepoPath      = "GITEXEC_HOOK_REPO_PATH"
	EnvRepoID        = "GITEXEC_HOOK_REPO_ID"
	EnvWorkspaceSlug = "GITEXEC_HOOK_WS_SLUG"
	EnvDecisionToken = "GITEXEC_HOOK_DECISION_TOKEN"
	EnvUserID        = "GITEXEC_HOOK_USER_ID"
	EnvOrchestrator  = "GITEXEC_HOOK_ORCH_URL"
)

// validateTimeout bounds one ref's validation call. Mirrors GitHub's fixed hook
// budget so a slow/hung orchestrator never wedges a push (fail-open on timeout).
const validateTimeout = 5 * time.Second

// IsPreReceiveInvocation reports whether this process was started by git as the
// pre-receive hook (argv[0] basename == "pre-receive"). Uses the multi-call
// binary trick: the hook is a symlink to this executable.
func IsPreReceiveInvocation() bool {
	if len(os.Args) == 0 {
		return false
	}
	return filepath.Base(os.Args[0]) == hookName
}

// EnsureHooksDir idempotently creates a directory containing a "pre-receive"
// symlink to the current executable and returns the directory. The SSH
// receive-pack spawn points git at it via `-c core.hooksPath=<dir>`, so one
// shared dir serves every repo — no per-repo hook install or backfill (design
// decision D2).
func EnsureHooksDir(dir string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir hooks dir %q: %w", dir, err)
	}
	link := filepath.Join(dir, hookName)
	// Reuse an already-correct symlink; otherwise (re)create it so a redeploy at a
	// new binary path is picked up.
	if existing, lerr := os.Readlink(link); lerr == nil && existing == self {
		return dir, nil
	}
	_ = os.Remove(link)
	if err := os.Symlink(self, link); err != nil {
		return "", fmt.Errorf("symlink pre-receive → %q: %w", self, err)
	}
	return dir, nil
}

// RunPreReceive is the hook entrypoint. It reads "<old> <new> <ref>" lines from
// stdin, validates each ref INDEPENDENTLY against the ruleset, and returns the
// process exit code: 0 = all refs allowed, 1 = at least one ref denied.
//
// Fail-open (design decision D1): missing config, transport error, or timeout
// ALLOW the push. Only an explicit rule violation (*ruleguard.Denied) rejects.
// Rulesets are still being wired, so a validation-service problem must never
// block pushes.
//
// Per-ref independence (Gitea CVE-2026-27775): no verdict is cached or
// short-circuited across refs — each "<old> <new> <ref>" line stands alone.
func RunPreReceive() int {
	return runWithRecover(func() int { return runPreReceive(os.Stdin, os.Stderr) })
}

// runWithRecover is the last-resort fail-open: a bug/panic inside the hook must
// NEVER block a push. Without it, an unhandled panic exits non-zero and git
// rejects the push (fail-closed) — defeating the D1 guarantee. Recover → exit 0.
func runWithRecover(fn func() int) (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "GITEXEC-HOOK: internal panic, allowing push: %v\n", r)
			code = 0
		}
	}()
	return fn()
}

func runPreReceive(stdin io.Reader, stderrW io.Writer) int {
	repoPath := os.Getenv(EnvRepoPath)
	baseURL := os.Getenv(EnvOrchestrator)
	repoID := os.Getenv(EnvRepoID)
	decisionToken := os.Getenv(EnvDecisionToken)

	// Degrade-open: without the pieces needed to validate, accept every ref.
	// Drain stdin first so git never sees a broken pipe on the ref list.
	if repoPath == "" || baseURL == "" || repoID == "" || decisionToken == "" {
		_, _ = io.Copy(io.Discard, stdin)
		return 0
	}

	client := ruleguard.NewClient(baseURL, validateTimeout)
	base := ruleguard.PushTarget{
		RepositoryID:  repoID,
		WorkspaceSlug: os.Getenv(EnvWorkspaceSlug),
		UserID:        os.Getenv(EnvUserID),
		DecisionToken: decisionToken,
	}

	denied := false
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		oldHash, newHash, ref, ok := parseRefLine(scanner.Text())
		if !ok {
			continue
		}
		if d := validateRef(repoPath, client, base, oldHash, newHash, ref, stderrW); d != nil {
			// GitLab-style: surface the reason so the git client shows *why*.
			fmt.Fprintf(stderrW, "GITEXEC-HOOK-ERR: %s: %s\n", ref, d.Error())
			denied = true
		}
	}
	if err := scanner.Err(); err != nil {
		// Couldn't read the ref list → fail-open (never block on our own error).
		fmt.Fprintf(stderrW, "GITEXEC-HOOK: read stdin failed (allowing): %v\n", err)
		return 0
	}
	if denied {
		return 1
	}
	return 0
}

// parseRefLine parses a git pre-receive stdin line: "<old-sha> <new-sha> <ref>".
func parseRefLine(line string) (oldHash, newHash, ref string, ok bool) {
	fields := strings.Fields(line)
	if len(fields) != 3 {
		return "", "", "", false
	}
	return fields[0], fields[1], fields[2], true
}

// validateRef validates ONE ref for the SSH pre-receive hook by delegating to
// ruleguard.ValidatePushRef — the single rule-factor check shared with the HTTP
// receive-pack path (receive_pack.go validatePushFacts). Returns a *Denied only
// on an explicit rule violation; non-branch/tag refs and transport/timeout
// errors return nil (allow — fail-open, logged to the hook's stderr).
func validateRef(
	repoPath string,
	client *ruleguard.Client,
	base ruleguard.PushTarget,
	oldHash, newHash, ref string,
	stderrW io.Writer,
) *ruleguard.Denied {
	ctx, cancel := context.WithTimeout(context.Background(), validateTimeout)
	defer cancel()

	denied, err := ruleguard.ValidatePushRef(ctx, client, base, repoPath, oldHash, newHash, ref)
	if err != nil {
		// Transport / API / timeout error → fail-open.
		fmt.Fprintf(stderrW, "GITEXEC-HOOK: validation error for %s (allowing): %v\n", ref, err)
		return nil
	}
	return denied
}
