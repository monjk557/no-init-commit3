package ruleguard

import "testing"

// eval_test.go covers the aggregation half (EvaluateBundleRef) — the bundle-shaped
// port of evaluate_rulesets. Aggregation consumes the bundle shape (produced by
// api-service bundle.py), so it is exercised with Go table tests rather than the
// shared engine/ref fixtures; the drift-sensitive pieces it calls into (the AST
// interpreter and ref applicability) are the ones locked by the golden fixtures.

// astBranch builds the block-main-style AST: action==push AND branch.name==value.
func astBranchEquals(value any) map[string]any {
	return map[string]any{
		"all": []any{
			map[string]any{"op": "==", "left": "action", "right": "push"},
			map[string]any{"op": "==", "left": "branch.name", "right": value},
		},
	}
}

func pushFactsMain() map[string]any {
	return map[string]any{"action": "push", "branch.name": "main"}
}

func TestEvaluateBundle_ActiveBlocks(t *testing.T) {
	b := &Bundle{
		Rulesets: []BundleRuleset{{
			ID: 1, Name: "rs", Level: "repository", Enforcement: "active", TargetType: "branch",
			Rules: []BundleRule{{ID: 10, Kind: "block_main_branch_push", ExpressionAST: astBranchEquals("main")}},
		}},
	}
	res := EvaluateBundleRef(b, "branch", "main", pushFactsMain())
	if res.Allowed {
		t.Fatal("expected blocked")
	}
	if len(res.Violations) != 1 || res.Violations[0].RuleID != 10 || !res.Violations[0].Blocking {
		t.Fatalf("expected one blocking violation, got %+v", res.Violations)
	}
}

func TestEvaluateBundle_EvaluateModeIsShadow(t *testing.T) {
	b := &Bundle{
		Rulesets: []BundleRuleset{{
			ID: 1, Name: "rs", Enforcement: "evaluate", TargetType: "branch",
			Rules: []BundleRule{{ID: 10, Kind: "block_main_branch_push", ExpressionAST: astBranchEquals("main")}},
		}},
	}
	res := EvaluateBundleRef(b, "branch", "main", pushFactsMain())
	if !res.Allowed {
		t.Fatal("evaluate-mode must not block")
	}
	if len(res.ShadowViolations) != 1 || len(res.Violations) != 0 {
		t.Fatalf("expected one shadow violation, got violations=%v shadow=%v", res.Violations, res.ShadowViolations)
	}
	if res.ShadowViolations[0].Blocking {
		t.Fatal("shadow violation must not be blocking")
	}
}

func TestEvaluateBundle_BypassRecordsButAllows(t *testing.T) {
	b := &Bundle{
		Rulesets: []BundleRuleset{{
			ID: 1, Name: "rs", Enforcement: "active", TargetType: "branch", Bypassed: true,
			Rules: []BundleRule{{ID: 10, Kind: "block_main_branch_push", ExpressionAST: astBranchEquals("main")}},
		}},
	}
	res := EvaluateBundleRef(b, "branch", "main", pushFactsMain())
	if !res.Allowed {
		t.Fatal("bypassed ruleset must allow")
	}
	if len(res.BypassedViolations) != 1 || len(res.Violations) != 0 {
		t.Fatalf("expected one bypassed violation recorded, got %+v", res)
	}
	if len(res.BypassedRulesetIDs) != 1 || res.BypassedRulesetIDs[0] != 1 {
		t.Fatalf("expected bypassed ruleset id 1, got %v", res.BypassedRulesetIDs)
	}
}

func TestEvaluateBundle_GrantedRuleSkipped(t *testing.T) {
	b := &Bundle{
		Rulesets: []BundleRuleset{{
			ID: 1, Name: "rs", Enforcement: "active", TargetType: "branch",
			Rules: []BundleRule{{ID: 10, Kind: "restrict_push_to_role", Granted: true, ExpressionAST: astBranchEquals("main")}},
		}},
	}
	res := EvaluateBundleRef(b, "branch", "main", pushFactsMain())
	if !res.Allowed || len(res.Violations) != 0 {
		t.Fatalf("granted rule must not be a violation, got %+v", res)
	}
}

func TestEvaluateBundle_RefApplicabilityFilters(t *testing.T) {
	b := &Bundle{
		Rulesets: []BundleRuleset{{
			ID: 1, Name: "rs", Enforcement: "active", TargetType: "branch", RefInclude: []string{"main"},
			Rules: []BundleRule{{ID: 10, ExpressionAST: astBranchEquals("develop")}},
		}},
	}
	// Push to develop: ruleset's ref_include is "main" only → not applicable.
	res := EvaluateBundleRef(b, "branch", "develop", map[string]any{"action": "push", "branch.name": "develop"})
	if !res.Allowed || len(res.Violations) != 0 {
		t.Fatalf("ref-miss ruleset must not fire, got %+v", res)
	}
}

func TestEvaluateBundle_PushTypeAppliesToEveryRef(t *testing.T) {
	// A push-type ruleset (e.g. max_file_size) is ref-agnostic: it must fire on
	// both a branch push and a tag push (design §6.5).
	ast := map[string]any{"op": ">", "left": "push.max_file_size_mb", "right": 5}
	b := &Bundle{
		Rulesets: []BundleRuleset{{
			ID: 1, Name: "size", Enforcement: "active", TargetType: "push",
			Rules: []BundleRule{{ID: 10, Kind: "max_file_size", ExpressionAST: ast}},
		}},
	}
	facts := map[string]any{"action": "push", "push.max_file_size_mb": 42.0}
	for _, rt := range []struct{ refType, name string }{{"branch", "main"}, {"tag", "v1"}} {
		res := EvaluateBundleRef(b, rt.refType, rt.name, facts)
		if res.Allowed {
			t.Errorf("push-type ruleset must fire on %s ref, got allowed", rt.refType)
		}
	}
}

func TestEvaluateBundle_FailPolicyClosedDeniesOnMissing(t *testing.T) {
	// A fail-closed rule referencing an absent fact must deny (§6.7).
	ast := map[string]any{"op": "==", "left": "commit.committer.email", "right": "x@corp.com"}
	b := &Bundle{
		Rulesets: []BundleRuleset{{
			ID: 1, Name: "rs", Enforcement: "active", TargetType: "branch",
			Rules: []BundleRule{{ID: 10, Kind: "committer_email_pattern", FailPolicy: "closed", ExpressionAST: ast}},
		}},
	}
	// commit.committer.email absent from facts.
	res := EvaluateBundleRef(b, "branch", "main", map[string]any{"action": "push", "branch.name": "main"})
	if res.Allowed {
		t.Fatalf("fail_policy=closed with missing fact must deny, got %+v", res)
	}
}

func TestEvaluateBundle_FailPolicyOpenAllowsOnMissing(t *testing.T) {
	ast := map[string]any{"op": "==", "left": "commit.committer.email", "right": "x@corp.com"}
	b := &Bundle{
		Rulesets: []BundleRuleset{{
			ID: 1, Name: "rs", Enforcement: "active", TargetType: "branch",
			Rules: []BundleRule{{ID: 10, FailPolicy: "open", ExpressionAST: ast}},
		}},
	}
	res := EvaluateBundleRef(b, "branch", "main", map[string]any{"action": "push", "branch.name": "main"})
	if !res.Allowed {
		t.Fatalf("fail_policy=open with missing fact must allow, got %+v", res)
	}
}

func TestEvaluateBundle_SentinelSubstitutedPerRef(t *testing.T) {
	// require_change_request template ships branch.name == "${ref_name}"; the
	// executor swaps the sentinel with the actual ref before evaluating.
	b := &Bundle{
		RefNameSentinel: "${ref_name}",
		Rulesets: []BundleRuleset{{
			ID: 1, Name: "rs", Enforcement: "active", TargetType: "branch",
			Rules: []BundleRule{{ID: 10, Kind: "require_change_request", ExpressionAST: astBranchEquals("${ref_name}")}},
		}},
	}
	// Push to feature/x → branch.name==feature/x after substitution → deny.
	res := EvaluateBundleRef(b, "branch", "feature/x", map[string]any{"action": "push", "branch.name": "feature/x"})
	if res.Allowed {
		t.Fatalf("sentinel should resolve to the pushed ref and deny, got %+v", res)
	}
	// The shared bundle AST must be unmodified (immutability) — right stays sentinel.
	inner := b.Rulesets[0].Rules[0].ExpressionAST["all"].([]any)[1].(map[string]any)
	if inner["right"] != "${ref_name}" {
		t.Fatalf("bundle AST was mutated: right=%v", inner["right"])
	}
}

func TestEvaluateBundle_MostRestrictiveWins(t *testing.T) {
	// One evaluate (shadow) ruleset + one active blocking ruleset → blocked.
	b := &Bundle{
		Rulesets: []BundleRuleset{
			{ID: 1, Name: "shadow", Enforcement: "evaluate", TargetType: "branch",
				Rules: []BundleRule{{ID: 10, ExpressionAST: astBranchEquals("main")}}},
			{ID: 2, Name: "active", Enforcement: "active", TargetType: "branch",
				Rules: []BundleRule{{ID: 20, ExpressionAST: astBranchEquals("main")}}},
		},
	}
	res := EvaluateBundleRef(b, "branch", "main", pushFactsMain())
	if res.Allowed {
		t.Fatal("an active blocking violation must win over a shadow one")
	}
	if len(res.Violations) != 1 || len(res.ShadowViolations) != 1 {
		t.Fatalf("expected 1 blocking + 1 shadow, got %+v", res)
	}
}

func TestBuildEvalFacts_MirrorsPushSubset(t *testing.T) {
	pushFacts := map[string]any{
		"ref": map[string]any{"is_creation": false, "is_update": true, "is_deletion": false},
		"commit": map[string]any{
			"is_force_push":   true,
			"is_merge_commit": false,
			"message":         "WIP",
			"author":          map[string]any{"email": "a@corp.com"},
			"committer":       map[string]any{"email": "c@corp.com"},
		},
		"push": map[string]any{"file_paths": []string{"a.env"}, "max_file_size_mb": 12.0},
	}
	facts := buildEvalFacts(pushFacts, "branch", "main", 2, 1)

	checks := map[string]any{
		"action":                    "push",
		"branch.name":               "main",
		"tag.name":                  "main",
		"commit.is_force_push":      true,
		"commit.message":            "WIP",
		"commit.author.email":       "a@corp.com",
		"commit.committer.email":    "c@corp.com",
		"commit.committer.is_user":  false,
		"commit.signature.verified": false,
		"ref.is_update":             true,
		"actor.repo_role_rank":      2,
		"actor.workspace_role_rank": 1,
		"push.max_file_size_mb":     12.0,
	}
	for k, want := range checks {
		if facts[k] != want {
			t.Errorf("facts[%q] = %v, want %v", k, facts[k], want)
		}
	}
	if _, ok := facts["push.file_paths"]; !ok {
		t.Error("push.file_paths missing")
	}
}
