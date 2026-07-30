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

	// EnvRuleBundle / EnvRuleBundleOverflow are the Phase-1 shadow bundle receipt
	// point (design §5.1, §10). Until the SSH handshake / HTTP header transport
	// (tasks 4/5) lands, the gateway spawn can stamp the gzip+base64 bundle (and an
	// overflow flag when it exceeded the 32KB ceiling) into these env vars. When the
	// real transport arrives only this receipt changes — the comparison is unaffected.
	EnvRuleBundle         = "GITEXEC_HOOK_RULE_BUNDLE"
	EnvRuleBundleOverflow = "GITEXEC_HOOK_RULE_BUNDLE_OVERFLOW"
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

	// Phase-1 shadow (design §10): load the local-eval bundle once. Any non-OK
	// status is logged ONCE here (not per ref) so the fallback-to-remote is never
	// silent, without flooding the log. shadowBundle is non-nil only when usable.
	shadowSrc := ruleguard.LoadShadowBundle(os.Getenv(EnvRuleBundle), os.Getenv(EnvRuleBundleOverflow) == "1")
	logShadowSkip(shadowSrc, stderrW)
	var shadowBundle *ruleguard.Bundle
	if shadowSrc.Status == ruleguard.BundleOK {
		shadowBundle = shadowSrc.Bundle
	}

	denied := false
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		oldHash, newHash, ref, ok := parseRefLine(scanner.Text())
		if !ok {
			continue
		}
		if d := validateRef(repoPath, client, base, shadowBundle, oldHash, newHash, ref, stderrW); d != nil {
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
//
// shadowBundle drives the Phase-1 shadow comparison (design §10): when non-nil,
// this ref is ALSO evaluated locally and any divergence from the remote (still
// authoritative) verdict is logged at error level. The remote result is what
// controls the push — local evaluation never changes it in Phase 1.
func validateRef(
	repoPath string,
	client *ruleguard.Client,
	base ruleguard.PushTarget,
	shadowBundle *ruleguard.Bundle,
	oldHash, newHash, ref string,
	stderrW io.Writer,
) *ruleguard.Denied {
	ctx, cancel := context.WithTimeout(context.Background(), validateTimeout)
	defer cancel()

	denied, err := ruleguard.ValidatePushRef(ctx, client, base, repoPath, oldHash, newHash, ref)
	if err != nil {
		// Transport / API / timeout error → fail-open (remote path is authoritative
		// in Phase 1; a shadow comparison against a failed remote is meaningless).
		fmt.Fprintf(stderrW, "GITEXEC-HOOK: validation error for %s (allowing): %v\n", ref, err)
		return nil
	}

	if shadowBundle != nil {
		out := shadowCompareRef(ctx, shadowBundle, repoPath, oldHash, newHash, ref, denied, stderrW)
		// Phase 1: verdict stays remote. When remote rejected AND local agrees with
		// blocking violations, enrich the client-facing reason from those local
		// violations (ruleset name/level + rule kinds). On a mismatch (local
		// allowed) there are no violations, so only the remote reason shows.
		if denied != nil {
			printRejectionFeedback(stderrW, ref, out.Violations)
		}
	}
	return denied
}

// shadowCompareRef runs local evaluation for one ref and logs any disagreement
// with the remote verdict (Phase 1, log-only). A match is dropped silently
// (shadow); a mismatch is loud (error) with the ref, both verdicts, and the local
// ruleset/rule identity so the divergence can be diagnosed. The outcome is
// returned so the caller can render user-facing feedback from the local
// violations when the (authoritative) remote path rejected the push.
func shadowCompareRef(
	ctx context.Context,
	bundle *ruleguard.Bundle,
	repoPath, oldHash, newHash, ref string,
	remoteDenied *ruleguard.Denied,
	stderrW io.Writer,
) ruleguard.ShadowOutcome {
	out := ruleguard.ShadowCompareRef(ctx, bundle, repoPath, oldHash, newHash, ref, remoteDenied)
	if out.Evaluated && out.Mismatch {
		fmt.Fprintf(stderrW,
			"GITEXEC-SHADOW-ERR: local/remote MISMATCH for %s: remote_allowed=%v local_allowed=%v%s\n",
			ref, out.RemoteAllowed, out.LocalAllowed, formatViolations(out.Violations))
	}
	return out
}

// printRejectionFeedback renders user-facing ruleset feedback to the hook's
// stderr — git surfaces every stderr line to the client as `remote: ...`. It is
// derived from the LOCAL evaluation's blocking violations, so it only prints when
// the remote (authoritative) path also rejected AND local agreed with blocking
// violations (Phase 1: local never changes the verdict, only enriches the reason).
//
// Kind-agnostic by design (§6/§7.2 — executor must not know rule-kind semantics):
// it names the ruleset (name + level) and lists the violated rule kinds verbatim,
// without translating them. Human per-kind copy, if wanted later, comes from a
// server-supplied bundle field, not from executor.
func printRejectionFeedback(stderrW io.Writer, ref string, violations []ruleguard.RuleViolation) {
	if len(violations) == 0 {
		return
	}
	branch := ref
	if b, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		branch = b
	} else if t, ok := strings.CutPrefix(ref, "refs/tags/"); ok {
		branch = t
	}
	// group rule kinds by ruleset, preserving first-seen order.
	order := make([]string, 0)
	byRuleset := make(map[string][]string)
	label := make(map[string]string)
	for _, v := range violations {
		key := fmt.Sprintf("%d", v.RulesetID)
		if _, seen := byRuleset[key]; !seen {
			order = append(order, key)
			lvl := v.RulesetLevel
			if lvl == "" {
				lvl = "repository"
			}
			label[key] = fmt.Sprintf("%q (%s)", v.RulesetName, lvl)
		}
		byRuleset[key] = append(byRuleset[key], v.Kind)
	}
	fmt.Fprintf(stderrW, "\n")
	fmt.Fprintf(stderrW, "⛔ Push to %q was blocked by repository ruleset policy:\n", branch)
	for _, key := range order {
		fmt.Fprintf(stderrW, "\n   ruleset %s\n", label[key])
		for _, kind := range byRuleset[key] {
			fmt.Fprintf(stderrW, "     · %s\n", kind)
		}
	}
	fmt.Fprintf(stderrW, "\n")
}

// formatViolations renders the local blocking violations for the mismatch log —
// which ruleset/rule the local evaluator would have blocked on.
func formatViolations(vs []ruleguard.RuleViolation) string {
	if len(vs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(vs))
	for _, v := range vs {
		parts = append(parts, fmt.Sprintf("ruleset=%d(%q) rule=%d kind=%q", v.RulesetID, v.RulesetName, v.RuleID, v.Kind))
	}
	return " local_violations=[" + strings.Join(parts, "; ") + "]"
}

// logShadowSkip logs, once per push, why local evaluation was skipped for this
// hook run (design §8). Silent on BundleOK. Absent is expected until the bundle
// transport (tasks 3/4/5) is wired, so it is a warn, not an error.
func logShadowSkip(src ruleguard.BundleSource, stderrW io.Writer) {
	switch src.Status {
	case ruleguard.BundleOK:
		return
	case ruleguard.BundleAbsent:
		fmt.Fprintf(stderrW, "GITEXEC-SHADOW-WARN: no rule bundle delivered — local eval skipped, remote authoritative (bundle transport pending, tasks 3/4/5)\n")
	case ruleguard.BundleOverflow:
		fmt.Fprintf(stderrW, "GITEXEC-SHADOW-WARN: rule bundle over size ceiling (overflow) — local eval skipped, remote authoritative\n")
	case ruleguard.BundleUnsupportedSchema:
		fmt.Fprintf(stderrW, "GITEXEC-SHADOW-WARN: unsupported rule bundle schema_version — local eval skipped, remote authoritative\n")
	case ruleguard.BundleDecodeError:
		fmt.Fprintf(stderrW, "GITEXEC-SHADOW-WARN: rule bundle decode failed (%v) — local eval skipped, remote authoritative\n", src.Err)
	}
}
