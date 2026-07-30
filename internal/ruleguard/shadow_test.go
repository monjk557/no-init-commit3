package ruleguard

import (
	"context"
	"testing"
)

func TestLoadShadowBundle_StatusClassification(t *testing.T) {
	t.Run("overflow_wins", func(t *testing.T) {
		if got := LoadShadowBundle("anything", true); got.Status != BundleOverflow {
			t.Fatalf("status = %v, want overflow", got.Status)
		}
	})
	t.Run("absent", func(t *testing.T) {
		if got := LoadShadowBundle("", false); got.Status != BundleAbsent {
			t.Fatalf("status = %v, want absent", got.Status)
		}
	})
	t.Run("decode_error", func(t *testing.T) {
		got := LoadShadowBundle("!!not base64!!", false)
		if got.Status != BundleDecodeError || got.Err == nil {
			t.Fatalf("status = %v err = %v, want decode-error with err", got.Status, got.Err)
		}
	})
	t.Run("unsupported_schema", func(t *testing.T) {
		enc := gzipBase64(t, `{"schema_version": 999, "repository_id": 1}`)
		got := LoadShadowBundle(enc, false)
		if got.Status != BundleUnsupportedSchema || got.Bundle == nil {
			t.Fatalf("status = %v, want unsupported-schema with bundle", got.Status)
		}
	})
	t.Run("ok", func(t *testing.T) {
		got := LoadShadowBundle(gzipBase64(t, sampleBundleJSON), false)
		if got.Status != BundleOK || got.Bundle == nil {
			t.Fatalf("status = %v, want ok with bundle", got.Status)
		}
	})
}

func TestCompareOutcome(t *testing.T) {
	blocking := BundleEvalResult{Allowed: false, Violations: []RuleViolation{{RulesetID: 1, RuleID: 2}}}
	allow := BundleEvalResult{Allowed: true}

	cases := []struct {
		name          string
		res           BundleEvalResult
		ran           bool
		remoteAllowed bool
		wantEvaluated bool
		wantMismatch  bool
	}{
		{"not_target_ref", allow, false, true, false, false},
		{"agree_allow", allow, true, true, true, false},
		{"agree_deny", blocking, true, false, true, false},
		{"mismatch_local_denies_remote_allows", blocking, true, true, true, true},
		{"mismatch_local_allows_remote_denies", allow, true, false, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := compareOutcome(c.res, c.ran, c.remoteAllowed)
			if out.Evaluated != c.wantEvaluated {
				t.Errorf("Evaluated = %v, want %v", out.Evaluated, c.wantEvaluated)
			}
			if out.Mismatch != c.wantMismatch {
				t.Errorf("Mismatch = %v, want %v", out.Mismatch, c.wantMismatch)
			}
		})
	}
}

func TestShadowCompareRef_NonTargetRefSkips(t *testing.T) {
	// refs/notes/* is not a ruleset target → local eval never runs, no git needed.
	out := ShadowCompareRef(context.Background(), &Bundle{}, "/nonexistent", zeroOID, "abc", "refs/notes/commits", nil)
	if out.Evaluated {
		t.Fatal("non-target ref must not evaluate")
	}
}

// gzipBase64 mirrors the transport encoder for tests (also defined in bundle_test.go
// as encodeBundle; kept separate here to avoid coupling test files).
func gzipBase64(t *testing.T, raw string) string {
	t.Helper()
	return encodeBundle(t, raw)
}
