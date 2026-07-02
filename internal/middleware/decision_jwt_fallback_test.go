package middleware

import (
	"testing"
	"time"
)

// TestFallbackShouldLog_RateLimitsPerReason verifies the dual-accept fallback is
// logged at most once per reason per interval, that reasons are independent, and
// that the suppressed count is carried into the next emitted line. See Issue-5071.
func TestFallbackShouldLog_RateLimitsPerReason(t *testing.T) {
	v := NewDecisionVerifier(testConfig(false, testSecret), nil)

	// First occurrence for a reason emits, with nothing suppressed yet.
	if emit, sup := v.fallbackShouldLog("missing_token"); !emit || sup != 0 {
		t.Fatalf("first call: emit=%v suppressed=%d, want true/0", emit, sup)
	}

	// Subsequent occurrences within the interval are suppressed.
	for i := 0; i < 5; i++ {
		if emit, _ := v.fallbackShouldLog("missing_token"); emit {
			t.Fatalf("call %d within interval should be suppressed", i)
		}
	}

	// A different reason is tracked independently and emits.
	if emit, _ := v.fallbackShouldLog("invalid_token"); !emit {
		t.Fatal("distinct reason should emit independently")
	}

	// Simulate the interval elapsing: the next emit reports the 5 suppressed.
	v.fbMu.Lock()
	v.fbLastLogged["missing_token"] = time.Now().Add(-2 * fallbackLogInterval)
	v.fbMu.Unlock()

	emit, sup := v.fallbackShouldLog("missing_token")
	if !emit {
		t.Fatal("should emit again after interval elapsed")
	}
	if sup != 5 {
		t.Fatalf("suppressed_since_last = %d, want 5", sup)
	}

	// Counter resets after an emit.
	if emit, _ := v.fallbackShouldLog("missing_token"); emit {
		t.Fatal("immediately after emit, within interval, must suppress")
	}
}
