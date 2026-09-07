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

	"github.com/collabops/git-executor/internal/gitsig"
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
			_, _ = fmt.Fprintf(os.Stderr, "GITEXEC-HOOK: internal panic, allowing push: %v\n", r)
			code = 0
		}
	}()
	return fn()
}

func runPreReceive(stdin io.Reader, stderrW io.Writer) int {
	repoPath := os.Getenv(EnvRepoPath)

	// The bundle is the ONLY evaluation input (design §10 Phase 3). Load it once;
	// a non-OK status is logged here rather than per ref, so an unenforced push is
	// always visible without flooding the log.
	src := ruleguard.LoadShadowBundle(os.Getenv(EnvRuleBundle), os.Getenv(EnvRuleBundleOverflow) == "1")
	logShadowSkip(src, stderrW)

	// Degrade-open: no repo path or no usable bundle → accept every ref. Drain
	// stdin first so git never sees a broken pipe on the ref list.
	//
	// This is fail-open by design (D1) — but never silent: logShadowSkip above
	// names the reason. A push that ran unenforced must be greppable.
	if repoPath == "" || src.Status != ruleguard.BundleOK {
		_, _ = io.Copy(io.Discard, stdin)
		return 0
	}
	bundle := src.Bundle

	denied := false
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		oldHash, newHash, ref, ok := parseRefLine(scanner.Text())
		if !ok {
			continue
		}
		if d := validateRef(repoPath, bundle, oldHash, newHash, ref, stderrW); d != nil {
			// GitLab-style: surface the reason so the git client shows *why*.
			_, _ = fmt.Fprintf(stderrW, "GITEXEC-HOOK-ERR: %s: %s\n", ref, d.Error())
			denied = true
		}
	}
	if err := scanner.Err(); err != nil {
		// Couldn't read the ref list → fail-open (never block on our own error).
		_, _ = fmt.Fprintf(stderrW, "GITEXEC-HOOK: read stdin failed (allowing): %v\n", err)
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

// validateRef validates ONE ref against the bundle. Returns a *Denied only on an
// explicit blocking violation; non-branch/tag refs (refs/notes etc.) return nil.
//
// Evaluation is entirely local — the bundle carries the compiled rule ASTs and the
// actor facts resolved at auth time, so the hook needs no network. That is the
// point of the design: the push choke point must not depend on the control plane
// being reachable (§1a, §10 Phase 3).
func validateRef(
	repoPath string,
	bundle *ruleguard.Bundle,
	oldHash, newHash, ref string,
	stderrW io.Writer,
) *ruleguard.Denied {
	ctx, cancel := context.WithTimeout(context.Background(), validateTimeout)
	defer cancel()

	// 서명 판정을 먼저 만든다. 앵커가 없거나 판독에 실패하면 nil 이고, 그때는 사실을
	// 생산하지 않아 기본값 false 가 쓰인다 — 계산하지 못한 것으로 거부하지 않는다는
	// 원칙은 규칙 쪽에서 fail-policy 가 지킨다.
	verified := signatureFact(ctx, repoPath, bundle, oldHash, newHash, stderrW)

	res, evaluated := ruleguard.LocalEvaluateRefWithSignature(ctx, bundle, repoPath, oldHash, newHash, ref, verified)
	if !evaluated || res.Allowed {
		return nil
	}

	// Surface the ruleset identity so the client sees *why* (GitLab-style).
	printRejectionFeedback(stderrW, ref, res.Violations)
	return &ruleguard.Denied{
		Branch: ruleguard.ClassifyRef(ref).Name,
		Reason: violationsReason(res.Violations),
	}
}

// violationsReason renders the blocking violations into the one-line reason git
// shows next to the rejected ref. Falls back to a generic message when the
// violation list is empty (shouldn't happen — Allowed=false implies at least one).
func violationsReason(violations []ruleguard.RuleViolation) string {
	if len(violations) == 0 {
		return "rejected by ruleset"
	}
	kinds := make([]string, 0, len(violations))
	for _, v := range violations {
		kinds = append(kinds, v.Kind)
	}
	return "rejected by ruleset: " + strings.Join(kinds, ", ")
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
	_, _ = fmt.Fprintf(stderrW, "\n")
	_, _ = fmt.Fprintf(stderrW, "⛔ Push to %q was blocked by repository ruleset policy:\n", branch)
	for _, key := range order {
		_, _ = fmt.Fprintf(stderrW, "\n   ruleset %s\n", label[key])
		for _, kind := range byRuleset[key] {
			_, _ = fmt.Fprintf(stderrW, "     · %s\n", kind)
		}
	}
	_, _ = fmt.Fprintf(stderrW, "\n")
}

// logShadowSkip logs, once per push, why local evaluation was skipped for this
// hook run (design §8). Silent on BundleOK. Absent is expected until the bundle
// transport (tasks 3/4/5) is wired, so it is a warn, not an error.
func logShadowSkip(src ruleguard.BundleSource, stderrW io.Writer) {
	if src.Status == ruleguard.BundleOK {
		return
	}
	// No usable bundle → nothing evaluates this push. Name the reason: an
	// unenforced push that leaves no trace is the failure mode this design exists
	// to remove (§8).
	reason := "unknown"
	switch src.Status {
	case ruleguard.BundleAbsent:
		reason = "no rule bundle in push context (gateway did not forward one)"
	case ruleguard.BundleOverflow:
		reason = "rule bundle exceeded the transport ceiling (gateway signalled overflow)"
	case ruleguard.BundleUnsupportedSchema:
		reason = "unsupported rule bundle schema_version (executor too old for this bundle)"
	case ruleguard.BundleDecodeError:
		reason = fmt.Sprintf("rule bundle decode failed: %v", src.Err)
	}
	_, _ = fmt.Fprintf(stderrW, "GITEXEC-HOOK: push proceeds WITHOUT ruleset enforcement: %s\n", reason)
}

// signatureFact 는 범위의 서명 판정을 만들고 그 결과를 캐시에 적재한다.
//
// 범위 안의 모든 커밋이 통과해야 true 다. 하나라도 미검증이면 그 push 는 서명된
// push 가 아니다.
//
// 판정을 캐시에 넣는 것은 조회 경로를 위한 것이다. 조회 시점에는 앵커가 없어 다시
// 계산할 수 없으므로, push 때 만든 이 판정이 배지의 유일한 근거다.
func signatureFact(
	ctx context.Context,
	repoPath string,
	bundle *ruleguard.Bundle,
	oldHash, newHash string,
	stderrW io.Writer,
) *bool {
	if len(bundle.Signers) == 0 {
		return nil
	}

	verdicts, err := gitsig.EvaluateRangeSignatures(ctx, repoPath, oldHash, newHash, bundle.Signers)
	if err != nil {
		// 삼키면 "서명이 없어서 미검증" 과 "판정을 못 해서 미검증" 이 구분되지 않는다.
		_, _ = fmt.Fprintf(stderrW, "GITEXEC-HOOK: signature verdict failed (unverified): %v\n", err)
		return nil
	}
	if len(verdicts) == 0 {
		return nil
	}

	allVerified := true
	for _, v := range verdicts {
		gitsig.Default().Put(repoPath, v.SHA, v.Verification)
		if !v.Verified() {
			allVerified = false
		}
	}
	return &allVerified
}
