package ruleguard

import (
	"context"
	"fmt"
)

// eval.go is the aggregation half of local evaluation: a 1:1 port of api-service
// apps/rulesets/evaluation/evaluator.py (evaluate_rulesets). It ties together the
// AST interpreter (engine.go), ref applicability (applicability.go), and the
// server-pre-folded bypass/grant bits from the bundle. The executor knows nothing
// about rule kinds here either — kind is carried only into the human-facing reason.
//
// defaultRefNameSentinel matches api-service bundle.py REF_NAME_SENTINEL. The
// bundle normally carries its own sentinel in ref_name_sentinel; this is the
// fallback if that field is absent.
const defaultRefNameSentinel = "${ref_name}"

// RuleViolation mirrors evaluator.py RuleViolation. Kind/Reason/RulesetName/Level
// are for the denial message and audit; the decision is Blocking.
type RuleViolation struct {
	RulesetID    int
	RuleID       int
	Kind         string
	Reason       string
	Enforcement  string // "active" | "evaluate"
	Blocking     bool   // true = active & not bypassed (actually rejects)
	RulesetName  string
	RulesetLevel string
}

// BundleEvalResult mirrors evaluator.py RulesetEvaluationResult. Allowed is false
// iff there is at least one blocking violation (most-restrictive-wins). Shadow and
// bypassed violations are recorded for audit but do not affect Allowed.
type BundleEvalResult struct {
	Allowed            bool
	Violations         []RuleViolation // blocking
	ShadowViolations   []RuleViolation // evaluate-mode would-block
	BypassedRulesetIDs []int
	BypassedViolations []RuleViolation // bypassed but recorded (§6.3)
}

// EvaluateBundleRef evaluates every applicable ruleset in the bundle against one
// pushed ref's facts. `refType` / `refName` come from ClassifyRef; `facts` are the
// flat facts from buildEvalFacts. Mirrors evaluate_rulesets' loop (§6.3).
//
// bypass is NOT a skip: a bypassed ruleset's rules are still evaluated so the
// bypassed violation is recorded (audit — an unrecorded skip is an insights blind
// spot, exactly as the Python evaluator notes).
func EvaluateBundleRef(b *Bundle, refType, refName string, facts map[string]any) BundleEvalResult {
	res := BundleEvalResult{Allowed: true}
	if b == nil {
		return res
	}
	sentinel := b.RefNameSentinel
	if sentinel == "" {
		sentinel = defaultRefNameSentinel
	}

	for _, rs := range b.Rulesets {
		// A ruleset applies if it matches this ref's branch/tag conditions, OR if
		// it is a push-type ruleset (which applies to every ref, ref-agnostic —
		// design §6.5). RefMatches is a faithful port of applicability.py
		// ref_matches: it returns true for a push-type ruleset only when queried
		// with ref_type="push", so the executor makes that second query here. The
		// design's "push-type applies to every ref" is this aggregation concern,
		// not something baked into RefMatches.
		if !RefMatches(rs, refType, refName, b.DefaultBranch) &&
			!RefMatches(rs, refTypePush, refName, b.DefaultBranch) {
			continue
		}
		canBypass := rs.Bypassed
		isActive := rs.Enforcement == "active"
		if canBypass {
			res.BypassedRulesetIDs = append(res.BypassedRulesetIDs, rs.ID)
		}
		for _, rule := range rs.Rules {
			// Identity grant (RuleActor) is a normal allow the server already
			// resolved — not a violation (mirrors evaluator.py _actor_granted).
			if rule.Granted {
				continue
			}
			if !ruleDenies(rule, sentinel, refName, facts) {
				continue
			}
			v := RuleViolation{
				RulesetID:    rs.ID,
				RuleID:       rule.ID,
				Kind:         rule.Kind,
				Reason:       fmt.Sprintf("'%s' rule in ruleset '%s' denies this action", rule.Kind, rs.Name),
				Enforcement:  enforcementLabel(isActive, canBypass),
				Blocking:     isActive && !canBypass,
				RulesetName:  rs.Name,
				RulesetLevel: rs.Level,
			}
			switch {
			case canBypass:
				res.BypassedViolations = append(res.BypassedViolations, v)
			case isActive:
				res.Violations = append(res.Violations, v)
			default:
				res.ShadowViolations = append(res.ShadowViolations, v)
			}
		}
	}

	res.Allowed = len(res.Violations) == 0
	return res
}

// ruleDenies decides whether a single rule fires against the facts. The AST match
// is the primary decision (mirrors evaluator.py _rule_denies). fail_policy=closed
// adds the §6.7 rule: a rule that needs a fact that is ABSENT is treated as a deny.
// In the bundle model the executor builds every push fact, so this path is nearly
// dead — but the contract is implemented identically.
func ruleDenies(rule BundleRule, sentinel, refName string, facts map[string]any) bool {
	ast := substituteRefName(rule.ExpressionAST, sentinel, refName)
	r := EvaluateExpression(ast, facts)
	if r.Matched {
		return true
	}
	if rule.FailPolicy == "closed" && len(r.Missing) > 0 {
		return true
	}
	return false
}

func enforcementLabel(isActive, canBypass bool) string {
	// A bypassed ruleset's recorded violation keeps its active/evaluate character
	// (bypass affects Blocking, not enforcement); mirrors evaluator.py which sets
	// enforcement from is_active regardless of bypass.
	if isActive {
		return "active"
	}
	return "evaluate"
}

// substituteRefName returns a deep copy of the AST with every string equal to the
// sentinel replaced by refName. The bundle is generated before the pushed ref is
// known, so ref-name-bearing templates (e.g. require_change_request's
// branch.name == <ref>) carry a sentinel the executor swaps per ref (bundle.py).
// The copy keeps the shared bundle AST unmodified across refs (immutability).
func substituteRefName(node any, sentinel, refName string) map[string]any {
	sub, _ := subst(node, sentinel, refName).(map[string]any)
	return sub
}

func subst(node any, sentinel, refName string) any {
	switch n := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(n))
		for k, v := range n {
			out[k] = subst(v, sentinel, refName)
		}
		return out
	case []any:
		out := make([]any, len(n))
		for i, v := range n {
			out[i] = subst(v, sentinel, refName)
		}
		return out
	case string:
		if n == sentinel {
			return refName
		}
		return n
	default:
		return node
	}
}

// buildEvalFacts flattens the nested BuildPushFacts output plus the bundle's actor
// ranks into the flat, dotted-key facts the interpreter consumes — a Go mirror of
// api-service build_facts() for the push-scope fact set (design §6.6). MERGE-scope
// facts (review.* / run.* / change_request.*) are intentionally NOT built: no
// push-scope rule references them, and computing role-tier counts would require the
// role vocabulary the executor must not carry (§4.2). Missing such a fact simply
// reads as absent, which push ASTs never reference.
//
// Fact polarity mirrors build_facts: booleans default false when absent
// (fail-open for is_force_push / is_merge_commit / ref.is_*); message / email /
// file facts are OMITTED when absent so the engine sees "missing" (the polarity the
// deny templates rely on).
func buildEvalFacts(pushFacts map[string]any, refType, refName string, actorRepoRank, actorWorkspaceRank int) map[string]any {
	facts := map[string]any{
		"action": "push",
		// build_facts sets both branch.name and tag.name to the ref name for every
		// push (option_facts_from_context uses target_value; build_facts stamps
		// tag.name); ref applicability ensures only the matching ruleset fires.
		"branch.name": refName,
		"tag.name":    refName,
		// Not produced by BuildPushFacts → fail-closed defaults (false), same as
		// build_facts when the payload omits them.
		"commit.signature.verified": false,
		"commit.committer.is_user":  false,
		// Actor ranks come straight from the bundle (§4.2).
		"actor.repo_role_rank":      actorRepoRank,
		"actor.workspace_role_rank": actorWorkspaceRank,
	}
	_ = refType // ref type only drives applicability; both name facts are always set.

	ref, _ := pushFacts["ref"].(map[string]any)
	facts["ref.is_creation"] = boolAt(ref, "is_creation")
	facts["ref.is_update"] = boolAt(ref, "is_update")
	facts["ref.is_deletion"] = boolAt(ref, "is_deletion")

	commit, _ := pushFacts["commit"].(map[string]any)
	facts["commit.is_force_push"] = boolAt(commit, "is_force_push")
	facts["commit.is_merge_commit"] = boolAt(commit, "is_merge_commit")
	if commit != nil {
		if msg, ok := commit["message"]; ok {
			facts["commit.message"] = msg
		}
		if author, ok := commit["author"].(map[string]any); ok {
			if email, ok := author["email"]; ok {
				facts["commit.author.email"] = email
			}
		}
		if committer, ok := commit["committer"].(map[string]any); ok {
			if email, ok := committer["email"]; ok {
				facts["commit.committer.email"] = email
			}
			if isUser, ok := committer["is_user"]; ok {
				facts["commit.committer.is_user"] = boolValue(isUser)
			}
		}
	}

	if push, ok := pushFacts["push"].(map[string]any); ok {
		if v, ok := push["max_file_size_mb"]; ok {
			facts["push.max_file_size_mb"] = v
		}
		if v, ok := push["file_paths"]; ok {
			facts["push.file_paths"] = v
		}
	}

	return facts
}

// LocalEvaluateRef is the full local pipeline for one ref: build push facts, flatten
// them with the bundle actor, and evaluate. Used by the Phase-1 shadow comparison
// (shadow.go) and, in Phase 2, would become the authoritative path.
func LocalEvaluateRef(ctx context.Context, b *Bundle, repoPath, oldHash, newHash, ref string) (BundleEvalResult, bool) {
	rt := ClassifyRef(ref)
	if rt.Type == "" {
		return BundleEvalResult{Allowed: true}, false // not a ruleset target (refs/notes etc.)
	}
	pushFacts := BuildPushFacts(ctx, repoPath, oldHash, newHash, rt.Name)
	facts := buildEvalFacts(pushFacts, rt.Type, rt.Name, b.Actor.RepoRoleRank, b.Actor.WorkspaceRoleRank)
	return EvaluateBundleRef(b, rt.Type, rt.Name, facts), true
}

func boolAt(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	return boolValue(m[key])
}

func boolValue(v any) bool {
	b, _ := v.(bool)
	return b
}
