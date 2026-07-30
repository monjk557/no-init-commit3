package ruleguard

import (
	"regexp"
	"strings"
	"sync"
)

// applicability.go is a 1:1 port of api-service
// apps/rulesets/evaluation/applicability.py (ref_matches, _ref_pattern_matches,
// _translate_ref_pattern). It decides, per pushed ref, which rulesets in the
// bundle apply (design §6.5). The bundle ships branch / tag / push rulesets all
// together because a single push can carry both branches and tags (git push
// --tags), so this ref-by-ref decision is the executor's job — it cannot be
// pre-folded at auth time.
//
// The ref-pattern glob here is the GitHub-ruleset dialect (FNM_PATHNAME): '*'/'?'
// do NOT cross '/', '**' matches any depth, always case-sensitive. This is a
// DIFFERENT glob from fnmatch.go (the `glob` operator), and different again from
// Go's path.Match — do not unify them. Both dialects are covered by golden
// fixtures (design §6.5, §7).

const (
	// refTypeBranch/Tag/Push mirror api-service TargetRefType keys.
	refTypeBranch = "branch"
	refTypeTag    = "tag"
	refTypePush   = "push"

	tokenAll           = "~ALL"
	tokenDefaultBranch = "~DEFAULT_BRANCH"

	refHeadsPrefix = "refs/heads/"
	refTagsPrefix  = "refs/tags/"
)

// RefTarget is a pushed ref decomposed into the type/name the ruleset applicability
// and fact building need.
type RefTarget struct {
	Type string // "branch" | "tag" | "" (not a ruleset target, e.g. refs/notes/*)
	Name string // short name with the refs/heads|tags/ prefix stripped
}

// ClassifyRef derives (ref_type, short_name) from a full ref (design §6.5):
// refs/heads/* → branch, refs/tags/* → tag, anything else → not a ruleset target.
func ClassifyRef(ref string) RefTarget {
	if name, ok := strings.CutPrefix(ref, refHeadsPrefix); ok {
		return RefTarget{Type: refTypeBranch, Name: name}
	}
	if name, ok := strings.CutPrefix(ref, refTagsPrefix); ok {
		return RefTarget{Type: refTypeTag, Name: name}
	}
	return RefTarget{}
}

// RefMatches reports whether a ruleset applies to (refType, refName), mirroring
// applicability.py ref_matches:
//
//   - target_type != ref_type            → not applicable
//   - push-type ruleset                  → applies to every ref (ref-agnostic)
//   - branch/tag: ref_include (empty = all) must match AND ref_exclude must not
//     (exclude wins over include)
//
// defaultBranch resolves the ~DEFAULT_BRANCH token and is only meaningful for
// branch refs; when absent (nil/empty) that token fails safe (never matches).
func RefMatches(rs BundleRuleset, refType, refName string, defaultBranch *string) bool {
	if rs.TargetType != refType {
		return false
	}
	if refType == refTypePush {
		return true
	}
	var db string
	if refType == refTypeBranch && defaultBranch != nil {
		db = *defaultBranch
	}
	included := len(rs.RefInclude) == 0
	for _, p := range rs.RefInclude {
		if refPatternMatches(p, refName, db) {
			included = true
			break
		}
	}
	if !included {
		return false
	}
	for _, p := range rs.RefExclude {
		if refPatternMatches(p, refName, db) {
			return false // exclude wins
		}
	}
	return true
}

// refPatternMatches mirrors applicability.py _ref_pattern_matches, including the
// two legacy/special tokens and the "*" root alias.
func refPatternMatches(pattern, refName, defaultBranch string) bool {
	if pattern == tokenAll {
		return true
	}
	if pattern == "*" {
		// Legacy fnmatch alias: a bare root "*" means "all refs" (old semantics
		// where "*" crossed "/"). Only the standalone "*" is promoted; composite
		// patterns like "release/*" stay in the segment-glob dialect below.
		return true
	}
	if pattern == tokenDefaultBranch {
		// Branch-only + requires a known default branch (fail-safe when absent).
		return defaultBranch != "" && refName == defaultBranch
	}
	return translateRefPattern(pattern).MatchString(refName)
}

var refPatternCache sync.Map // pattern string → *regexp.Regexp

// translateRefPattern compiles a GitHub-ruleset glob to an anchored regexp,
// mirroring applicability.py _translate_ref_pattern:
//   - "**/" → 0+ directories (so "releases/**/*" also matches "releases/x")
//   - "**"  → any string
//   - "*"   → within a segment only ([^/]*)
//   - "?"   → one non-'/' char
//   - "[seq]" / "[!seq]" → character class (unterminated → literal '[')
//   - case-sensitive; everything else is a literal.
func translateRefPattern(pattern string) *regexp.Regexp {
	if v, ok := refPatternCache.Load(pattern); ok {
		return v.(*regexp.Regexp)
	}
	var out strings.Builder
	out.WriteString(`\A`)
	i, n := 0, len(pattern)
	for i < n {
		switch {
		case strings.HasPrefix(pattern[i:], "**/"):
			out.WriteString("(?:.*/)?")
			i += 3
		case strings.HasPrefix(pattern[i:], "**"):
			out.WriteString(".*")
			i += 2
		case pattern[i] == '*':
			out.WriteString("[^/]*")
			i++
		case pattern[i] == '?':
			out.WriteString("[^/]")
			i++
		case pattern[i] == '[':
			consumed, class := refCharClass(pattern, i)
			out.WriteString(class)
			i += consumed
		default:
			out.WriteString(regexp.QuoteMeta(string(pattern[i])))
			i++
		}
	}
	out.WriteString(`\z`)
	// Pattern content is validated at write time in api-service; a translation
	// bug here would be a build/test failure, so MustCompile is appropriate.
	re := regexp.MustCompile(out.String())
	refPatternCache.Store(pattern, re)
	return re
}

// refCharClass parses a "[...]" character class starting at index i, mirroring the
// bracket handling in applicability.py _translate_ref_pattern. Returns how many
// input chars were consumed and the emitted regexp fragment. An unterminated class
// emits a literal '[' and consumes one char.
func refCharClass(pattern string, i int) (consumed int, out string) {
	n := len(pattern)
	j := i + 1
	if j < n && (pattern[j] == '!' || pattern[j] == '^') {
		j++
	}
	if j < n && pattern[j] == ']' {
		j++ // a leading ']' is a literal member
	}
	for j < n && pattern[j] != ']' {
		j++
	}
	if j >= n {
		return 1, regexp.QuoteMeta("[")
	}
	inner := strings.ReplaceAll(pattern[i+1:j], `\`, `\\`)
	if strings.HasPrefix(inner, "!") {
		inner = "^" + inner[1:]
	}
	return (j + 1) - i, "[" + inner + "]"
}
