# ruleguard golden fixtures

These JSON files are the **cross-language contract** for local rule-bundle
evaluation (design `rule-bundle.md` §7). They are the *spec* — what the evaluator
SHOULD return — not a snapshot of current behavior.

| File | Locks | Python counterpart |
|------|-------|--------------------|
| `engine_cases.json` | AST interpreter (`EvaluateExpression` / `evalRoot`) — every operator, missing-fact recording, empty AST, structural nodes, and the `glob` op's fnmatch dialect | `apps/repositories/rules/engine.py` `evaluate_expression` |
| `ref_pattern_cases.json` | Ref applicability (`RefMatches`) + the GitHub-ruleset glob dialect (`~ALL` / `~DEFAULT_BRANCH` / `*` alias / segment glob / `**`) | `apps/rulesets/evaluation/applicability.py` `ref_matches` / `_translate_ref_pattern` |

## Two different globs — do not conflate

- `engine_cases.json` `glob` op → **fnmatch** dialect: `*` crosses `/`.
- `ref_pattern_cases.json` → **GitHub-ruleset** dialect: `*` does NOT cross `/`,
  `**` matches any depth.

Both differ from Go's `path.Match`, so both are hand-ported and both are pinned
here.

## SHOULD be shared with api-service

The drift risk this design introduces is having two evaluators (Python + Go). The
intended defense (§7.1) is for **api-service to drive its `engine.py` /
`applicability.py` tests from these SAME JSON files**, so a divergence between the
Python and Go implementations fails CI on both sides.

As of this commit, api-service task #2 ("golden fixture 생성기 + 정본 케이스 커밋")
has not landed an engine-level `{ast, facts, expected_matched, expected_missing}`
fixture — its existing `tests/unit/rulesets/fixtures/ruleset_cases.json` is a
model-level truth table for `evaluate_rulesets()` (it needs the Django ORM to
build rulesets and is not consumable by the executor, which only sees compiled
ASTs). These files are therefore authored here in the engine-level format and
must be adopted as the canonical, shared fixtures. Until then, keep them in sync
by hand whenever `engine.py` or `applicability.py` change.

## Not covered here

Aggregation (`EvaluateBundleRef` / `evaluate_rulesets`) is exercised by Go table
tests (`eval_test.go`) rather than a shared fixture: it consumes the **bundle**
shape produced by api-service `bundle.py`, whereas the Python aggregator works on
Django models — there is no common input to share. The drift-sensitive pieces it
calls (the AST interpreter and ref applicability) are the ones pinned by these
fixtures.
