package protocol

import (
	"strconv"
	"strings"
	"testing"
)

// envToMap turns the GIT_CONFIG_* slice into a git-config key->value map,
// resolving GIT_CONFIG_KEY_n / GIT_CONFIG_VALUE_n pairs by GIT_CONFIG_COUNT.
func envToMap(t *testing.T, env []string) map[string]string {
	t.Helper()
	raw := map[string]string{}
	for _, e := range env {
		k, v, ok := strings.Cut(e, "=")
		if !ok {
			t.Fatalf("malformed env entry: %q", e)
		}
		raw[k] = v
	}
	out := map[string]string{}
	for i := 0; ; i++ {
		key, ok := raw["GIT_CONFIG_KEY_"+strconv.Itoa(i)]
		if !ok {
			break
		}
		out[key] = raw["GIT_CONFIG_VALUE_"+strconv.Itoa(i)]
	}
	return out
}

func TestPackLimitEnv_Unset_AppliesDefaults(t *testing.T) {
	// No t.Setenv calls → keys are unset → defaults apply.
	env := packLimitEnv()
	m := envToMap(t, env)

	if got := m["pack.windowMemory"]; got != defaultPackWindowMemory {
		t.Errorf("pack.windowMemory = %q, want %q", got, defaultPackWindowMemory)
	}
	if got := m["pack.threads"]; got != defaultPackThreads {
		t.Errorf("pack.threads = %q, want %q", got, defaultPackThreads)
	}
	if got := m["pack.deltaCacheSize"]; got != defaultPackDeltaCacheSize {
		t.Errorf("pack.deltaCacheSize = %q, want %q", got, defaultPackDeltaCacheSize)
	}
	if got := m["pack.allowPackReuse"]; got != defaultPackAllowReuse {
		t.Errorf("pack.allowPackReuse = %q, want %q", got, defaultPackAllowReuse)
	}
	if got := m["pack.useBitmaps"]; got != defaultPackUseBitmaps {
		t.Errorf("pack.useBitmaps = %q, want %q", got, defaultPackUseBitmaps)
	}

	var count string
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok && k == "GIT_CONFIG_COUNT" {
			count = v
		}
	}
	if count != "5" {
		t.Errorf("GIT_CONFIG_COUNT = %q, want 5", count)
	}
}

func TestPackLimitEnv_AllDisabled_YieldsNil(t *testing.T) {
	t.Setenv(EnvPackWindowMemory, "off")
	t.Setenv(EnvPackThreads, "0")
	t.Setenv(EnvPackDeltaCacheSize, "")
	t.Setenv(EnvPackAllowReuse, "off")
	t.Setenv(EnvPackUseBitmaps, "0")

	if env := packLimitEnv(); env != nil {
		t.Fatalf("all knobs disabled should yield nil env, got %v", env)
	}
}

func TestPackLimitEnv_Overrides(t *testing.T) {
	t.Setenv(EnvPackWindowMemory, "512m")
	t.Setenv(EnvPackThreads, "4")
	t.Setenv(EnvPackDeltaCacheSize, "256m")

	m := envToMap(t, packLimitEnv())
	if m["pack.windowMemory"] != "512m" {
		t.Errorf("windowMemory override not applied: %v", m)
	}
	if m["pack.threads"] != "4" {
		t.Errorf("threads override not applied: %v", m)
	}
	if m["pack.deltaCacheSize"] != "256m" {
		t.Errorf("deltaCacheSize override not applied: %v", m)
	}
}

func TestPackLimitEnv_PartialDisable(t *testing.T) {
	// Disabling one knob drops only that pair and adjusts GIT_CONFIG_COUNT.
	t.Setenv(EnvPackThreads, "off")

	env := packLimitEnv()
	m := envToMap(t, env)
	if _, ok := m["pack.threads"]; ok {
		t.Errorf("pack.threads should be omitted when set to off, got %v", m)
	}
	if _, ok := m["pack.windowMemory"]; !ok {
		t.Errorf("pack.windowMemory should remain, got %v", m)
	}
	if len(m) != 4 {
		t.Errorf("expected 4 knobs after disabling one, got %d: %v", len(m), m)
	}
}

func TestPackLimitValue_DisableTokens(t *testing.T) {
	for _, tok := range []string{"", "0", "off", "OFF", "Off"} {
		t.Setenv(EnvPackWindowMemory, tok)
		if got := packLimitValue(EnvPackWindowMemory, "128m"); got != "" {
			t.Errorf("token %q should disable (return \"\"), got %q", tok, got)
		}
	}
}
