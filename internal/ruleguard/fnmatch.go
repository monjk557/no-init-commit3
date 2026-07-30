package ruleguard

import (
	"regexp"
	"strings"
	"sync"
)

// fnmatch.go implements the `glob` operator's pattern dialect: Python's
// fnmatch.fnmatchcase (used by engine.py's `glob` op). This is the STANDARD
// fnmatch dialect — '*' matches everything INCLUDING '/', '?' matches any single
// char, '[seq]'/'[!seq]' are character classes, and matching is CASE-SENSITIVE
// (fnmatchcase, not fnmatch).
//
// This is intentionally DIFFERENT from the ref-pattern dialect in applicability.go
// (where '*' does NOT cross '/'). The two globs come from two different Python
// modules (fnmatch vs the GitHub-ruleset translator) and must not be conflated —
// Go's path.Match matches NEITHER (its '*' stops at '/'), so both are hand-ported.
//
// The port mirrors CPython Lib/fnmatch.py translate() closely enough to be exact
// for the patterns rules use ('release/*', 'v[0-9]*', '*.env', 'feature/?'); the
// glob cases in testdata/engine_cases.json lock the behavior against engine.py.

var fnmatchCache sync.Map // glob pattern string → *regexp.Regexp | error

// fnmatchCase reports whether name matches the fnmatch pattern, case-sensitively.
// A pattern that fails to translate/compile is treated as "no match" (defensive —
// the deny must not fire on a broken pattern), consistent with engine.py tolerance.
func fnmatchCase(name, pattern string) bool {
	re, err := fnmatchCompile(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(name)
}

func fnmatchCompile(pattern string) (*regexp.Regexp, error) {
	if v, ok := fnmatchCache.Load(pattern); ok {
		if re, ok := v.(*regexp.Regexp); ok {
			return re, nil
		}
		return nil, v.(error)
	}
	// (?s) = DOTALL so '.' (from '?' and '*') crosses newlines, matching CPython's
	// translate which wraps the body in (?s:...). \A..\z anchors a full match,
	// mirroring fnmatch's re.match(translate(pat)) where translate ends in \Z.
	re, err := regexp.Compile(`(?s)\A` + fnmatchTranslate(pattern) + `\z`)
	if err != nil {
		fnmatchCache.Store(pattern, err)
		return nil, err
	}
	fnmatchCache.Store(pattern, re)
	return re, nil
}

// fnmatchTranslate converts an fnmatch pattern to a Go regexp body, mirroring
// CPython Lib/fnmatch.py translate() for '*', '?', and '[...]' character classes.
func fnmatchTranslate(pat string) string {
	var out strings.Builder
	i, n := 0, len(pat)
	for i < n {
		c := pat[i]
		i++
		switch c {
		case '*':
			out.WriteString(".*")
		case '?':
			out.WriteString(".")
		case '[':
			j := i
			if j < n && pat[j] == '!' {
				j++
			}
			if j < n && pat[j] == ']' {
				j++
			}
			for j < n && pat[j] != ']' {
				j++
			}
			if j >= n {
				// Unterminated class → literal '['.
				out.WriteString(`\[`)
			} else {
				stuff := pat[i:j]
				i = j + 1
				out.WriteString(translateClass(stuff))
			}
		default:
			out.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return out.String()
}

// translateClass renders the inside of a '[...]' fnmatch class as a Go regexp
// class. Mirrors CPython: '!' → '^' negation; a leading '^'/'[' is escaped; set
// operators (& ~ |) are escaped so RE2 treats them literally; empty class never
// matches; a lone '!' means "any char".
func translateClass(stuff string) string {
	if stuff == "" {
		return "(?!)" // empty class matches nothing (CPython 3.x)
	}
	if stuff == "!" {
		return "." // '[!]' → any char
	}
	// Escape RE2 set-operation chars so they stay literal inside the class.
	stuff = classSetOps.ReplaceAllString(stuff, `\$1`)
	if stuff[0] == '!' {
		stuff = "^" + stuff[1:]
	} else if stuff[0] == '^' || stuff[0] == '[' {
		stuff = `\` + stuff
	}
	return "[" + stuff + "]"
}

var classSetOps = regexp.MustCompile(`([&~|])`)
