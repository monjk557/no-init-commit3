package ruleguard

import (
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// engine.go is a 1:1 port of api-service apps/repositories/rules/engine.py
// (evaluate_expression). It is the single largest drift surface in this design
// (a Python and a Go evaluator), so every operator and every edge case below is a
// deliberate mirror of the Python semantics, locked by the shared golden fixtures
// (testdata/engine_cases.json, design §7.1). Change nothing here without changing
// engine.py and the fixtures in lockstep.
//
// Facts are FLAT: keys are dotted paths ("branch.name", "commit.author.email",
// "actor.repo_role_rank"), exactly as api-service build_facts() produces them and
// as engine.py's `left in facts` / `facts[left]` consumes them. The nested map
// BuildPushFacts returns is flattened by buildEvalFacts (eval.go) before it
// reaches this evaluator.

// EngineResult is the outcome of evaluating one rule AST, mirroring engine.py's
// EngineEvalResult: whether the deny condition matched, and which referenced facts
// were absent (used by fail_policy=closed, §6.7).
type EngineResult struct {
	Matched bool
	// Missing is the sorted set of fact paths the AST referenced but that were
	// absent from facts. Deterministic order so callers/tests are stable.
	Missing []string
}

// EvaluateExpression evaluates a rule's compiled AST against flat facts.
//
// Supported (engine.py §6.1):
//   - structural: all, any, not, bare list (= all)
//   - comparison: ==, !=, >, >=, <, <=
//   - set:        in, not_in, contains
//   - string:     starts_with, ends_with, glob, matches
//   - list:       any_matches
//
// Conventions (engine.py §6.2) — all reproduced exactly:
//   - empty AST                 → matched=false
//   - left is not a string      → false
//   - left fact absent          → record in Missing + node false
//   - any_matches on non-list   → false
//   - unknown op                → false
func EvaluateExpression(ast map[string]any, facts map[string]any) EngineResult {
	return evalRoot(ast, facts)
}

// evalRoot is the shared entry for both the map-typed public API and the golden
// test (which also feeds bare-list roots). It reproduces engine.py's
// `if not expression_ast: return matched=false` — a nil/empty map or empty list
// root is a non-match.
func evalRoot(node any, facts map[string]any) EngineResult {
	if isEmptyAST(node) {
		return EngineResult{Matched: false}
	}
	missing := map[string]struct{}{}
	matched := evalNode(node, facts, missing)
	return EngineResult{Matched: matched, Missing: sortedKeys(missing)}
}

// isEmptyAST mirrors Python truthiness for AST roots: nil, an empty map, or an
// empty list are all "empty" (→ non-match); any populated collection or scalar is
// not.
func isEmptyAST(node any) bool {
	switch n := node.(type) {
	case nil:
		return true
	case map[string]any:
		return len(n) == 0
	case []any:
		return len(n) == 0
	default:
		return false
	}
}

// evalNode mirrors engine.py's inner _eval closure. `node` is any because the AST
// is untyped JSON: a dict node (map[string]any), a bare list (all), or a scalar
// (always false at the root — engine.py's final `return False`).
func evalNode(node any, facts map[string]any, missing map[string]struct{}) bool {
	switch n := node.(type) {
	case map[string]any:
		// Structural keys are checked before the leaf, in engine.py's order:
		// all → any → not → leaf comparison.
		if children, ok := n["all"]; ok {
			list, ok := toList(children)
			if !ok {
				return false
			}
			for _, child := range list {
				if !evalNode(child, facts, missing) {
					return false
				}
			}
			return true
		}
		if children, ok := n["any"]; ok {
			list, ok := toList(children)
			if !ok {
				return false
			}
			for _, child := range list {
				if evalNode(child, facts, missing) {
					return true
				}
			}
			return false
		}
		if child, ok := n["not"]; ok {
			return !evalNode(child, facts, missing)
		}
		return evalLeaf(n, facts, missing)
	case []any:
		// A bare list is not a valid root but engine.py treats it as AND.
		for _, child := range n {
			if !evalNode(child, facts, missing) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// evalLeaf evaluates a comparison node {op, left, right}. Mirrors the leaf half of
// engine.py's _eval.
func evalLeaf(n map[string]any, facts map[string]any, missing map[string]struct{}) bool {
	left, ok := n["left"].(string)
	if !ok {
		// left is not a string → false (engine.py: `if not isinstance(left, str)`).
		return false
	}
	lv, present := facts[left]
	if !present {
		missing[left] = struct{}{}
		return false
	}
	op, _ := n["op"].(string)
	right := n["right"]

	switch op {
	case "==":
		return pyEquals(lv, right)
	case "!=":
		return !pyEquals(lv, right)
	case ">":
		return cmpNumbers(lv, right, func(a, b float64) bool { return a > b })
	case ">=":
		return cmpNumbers(lv, right, func(a, b float64) bool { return a >= b })
	case "<":
		return cmpNumbers(lv, right, func(a, b float64) bool { return a < b })
	case "<=":
		return cmpNumbers(lv, right, func(a, b float64) bool { return a <= b })
	case "in":
		// right=None → False (engine.py: `right is not None and lv in right`).
		return right != nil && pyMembership(lv, right)
	case "not_in":
		// right=None → True (nothing to be a member of).
		return right == nil || !pyMembership(lv, right)
	case "contains":
		return pyContains(lv, right)
	case "starts_with":
		ls, lok := lv.(string)
		rs, rok := right.(string)
		return lok && rok && strings.HasPrefix(ls, rs)
	case "ends_with":
		ls, lok := lv.(string)
		rs, rok := right.(string)
		return lok && rok && strings.HasSuffix(ls, rs)
	case "glob":
		// fnmatchcase — '*' crosses '/', case-sensitive. NOT the ref-pattern
		// dialect (applicability.go), which is a different glob.
		ls, lok := lv.(string)
		rs, rok := right.(string)
		return lok && rok && fnmatchCase(ls, rs)
	case "matches":
		// re.search (unanchored). Malformed pattern → false (engine.py
		// _safe_regex_match): a broken user pattern must not fire the deny.
		return safeRegexSearch(lv, right)
	case "any_matches":
		// left is a list-valued fact; true when ANY element matches. Non-list
		// or empty → false.
		list, ok := toList(lv)
		if !ok {
			return false
		}
		for _, item := range list {
			if safeRegexSearch(item, right) {
				return true
			}
		}
		return false
	default:
		// Unknown op → false (engine.py final `return False`).
		return false
	}
}

// pyEquals mirrors Python `==` for the JSON-representable fact types. Numbers
// compare as float64 (JSON gives both fact and right as float64); booleans fold to
// 1/0 so True == 1 (Python treats bool as an int subclass); everything else uses
// structural equality.
func pyEquals(a, b any) bool {
	if af, aok := toFloat(a); aok {
		if bf, bok := toFloat(b); bok {
			return af == bf
		}
	}
	return reflect.DeepEqual(a, b)
}

// cmpNumbers applies an ordered comparison only when both operands are numeric
// (or bool, which Python treats as int). engine.py evaluates `lv > right`
// directly, which would raise TypeError on mismatched types — but the only facts
// compared with </>/<=/>= are numeric (role rank, file size, approval counts), so
// this path is never reached with mismatched types in practice. Returning false
// on a type mismatch keeps the executor safe (no panic) instead of crashing the
// push choke point; documented deviation, unreachable with real facts.
func cmpNumbers(a, b any, cmp func(x, y float64) bool) bool {
	af, aok := toFloat(a)
	bf, bok := toFloat(b)
	if !aok || !bok {
		return false
	}
	return cmp(af, bf)
}

// pyMembership mirrors Python `lv in right`: element equality for a list right,
// substring for a string right. (dict membership is unused by any rule AST.)
func pyMembership(lv, right any) bool {
	if list, ok := toList(right); ok {
		for _, e := range list {
			if pyEquals(lv, e) {
				return true
			}
		}
		return false
	}
	if rs, ok := right.(string); ok {
		if ls, ok := lv.(string); ok {
			return strings.Contains(rs, ls)
		}
	}
	return false
}

// pyContains mirrors engine.py's `contains`: substring when both are strings,
// element membership when lv is a list, else false.
func pyContains(lv, right any) bool {
	if ls, ok := lv.(string); ok {
		if rs, ok := right.(string); ok {
			return strings.Contains(ls, rs)
		}
		return false
	}
	if list, ok := toList(lv); ok {
		for _, e := range list {
			if pyEquals(e, right) {
				return true
			}
		}
		return false
	}
	return false
}

// toFloat reports a numeric value for the JSON/Go types facts can hold. Bool folds
// to 1/0 to match Python's bool-is-int behavior. Strings are NOT numeric.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case bool:
		if n {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

// toList normalizes the two list shapes facts/AST can hold — []any (from JSON) and
// []string (from BuildPushFacts) — to []any. Mirrors Python's list/tuple/set
// acceptance in the set operators.
func toList(v any) ([]any, bool) {
	switch l := v.(type) {
	case []any:
		return l, true
	case []string:
		out := make([]any, len(l))
		for i, s := range l {
			out[i] = s
		}
		return out, true
	default:
		return nil, false
	}
}

// safeRegexSearch mirrors engine.py _safe_regex_match: value and pattern must be
// strings; a malformed pattern is treated as "no match" (never a crash, never a
// spurious deny). Uses re.search semantics (unanchored) — regexp.MatchString is
// already unanchored.
func safeRegexSearch(value, pattern any) bool {
	vs, vok := value.(string)
	ps, pok := pattern.(string)
	if !vok || !pok {
		return false
	}
	re, err := compileRegexCached(ps)
	if err != nil {
		return false
	}
	return re.MatchString(vs)
}

var regexCache sync.Map // pattern string → *regexp.Regexp | error

func compileRegexCached(pattern string) (*regexp.Regexp, error) {
	if v, ok := regexCache.Load(pattern); ok {
		if re, ok := v.(*regexp.Regexp); ok {
			return re, nil
		}
		return nil, v.(error)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		regexCache.Store(pattern, err)
		return nil, err
	}
	regexCache.Store(pattern, re)
	return re, nil
}

func sortedKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
