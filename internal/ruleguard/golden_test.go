package ruleguard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// golden_test.go drives the two cross-language drift guards (design §7.1):
//   - testdata/engine_cases.json    → the AST interpreter (engine.py parity)
//   - testdata/ref_pattern_cases.json → ref applicability + glob dialect
//
// These fixtures are the SPEC (what the interpreter SHOULD return), not a
// snapshot. They are meant to become the SHARED canonical fixtures with
// api-service so a Python/Go divergence fails CI on both sides — see
// testdata/README.md. Until api-service adopts them, keep them in sync by hand
// whenever engine.py / applicability.py change.

type engineCase struct {
	ID              string         `json:"id"`
	AST             any            `json:"ast"`
	Facts           map[string]any `json:"facts"`
	ExpectedMatched bool           `json:"expected_matched"`
	ExpectedMissing []string       `json:"expected_missing"`
}

func TestGoldenEngineCases(t *testing.T) {
	var doc struct {
		Cases []engineCase `json:"cases"`
	}
	loadFixture(t, "engine_cases.json", &doc)
	if len(doc.Cases) == 0 {
		t.Fatal("no engine cases loaded")
	}

	for _, c := range doc.Cases {
		t.Run(c.ID, func(t *testing.T) {
			got := evalRoot(c.AST, c.Facts)
			if got.Matched != c.ExpectedMatched {
				t.Errorf("matched = %v, want %v", got.Matched, c.ExpectedMatched)
			}
			if !equalStringSets(got.Missing, c.ExpectedMissing) {
				t.Errorf("missing = %v, want %v", got.Missing, c.ExpectedMissing)
			}
		})
	}
}

type refPatternCase struct {
	ID            string   `json:"id"`
	TargetType    string   `json:"target_type"`
	RefInclude    []string `json:"ref_include"`
	RefExclude    []string `json:"ref_exclude"`
	RefType       string   `json:"ref_type"`
	RefName       string   `json:"ref_name"`
	DefaultBranch *string  `json:"default_branch"`
	Expected      bool     `json:"expected"`
}

func TestGoldenRefPatternCases(t *testing.T) {
	var doc struct {
		Cases []refPatternCase `json:"cases"`
	}
	loadFixture(t, "ref_pattern_cases.json", &doc)
	if len(doc.Cases) == 0 {
		t.Fatal("no ref-pattern cases loaded")
	}

	for _, c := range doc.Cases {
		t.Run(c.ID, func(t *testing.T) {
			rs := BundleRuleset{
				TargetType: c.TargetType,
				RefInclude: c.RefInclude,
				RefExclude: c.RefExclude,
			}
			got := RefMatches(rs, c.RefType, c.RefName, c.DefaultBranch)
			if got != c.Expected {
				t.Errorf("RefMatches = %v, want %v", got, c.Expected)
			}
		})
	}
}

func loadFixture(t *testing.T, name string, out any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("unmarshal fixture %s: %v", name, err)
	}
}

func equalStringSets(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}
