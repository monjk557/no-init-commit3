package config

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnvWithAlias(t *testing.T) {
	const (
		newKey       = "TEST_PORT"
		legacyKey    = "TEST_SERVER_PORT"
		legacyKey2nd = "TEST_OLD_PORT"
	)

	tests := []struct {
		name     string
		setup    map[string]string
		legacies []string
		want     string
	}{
		{
			name:     "new key only",
			setup:    map[string]string{newKey: "8080"},
			legacies: []string{legacyKey},
			want:     "8080",
		},
		{
			name:     "legacy key only — fallback",
			setup:    map[string]string{legacyKey: "5001"},
			legacies: []string{legacyKey},
			want:     "5001",
		},
		{
			name:     "both set — new key wins",
			setup:    map[string]string{newKey: "9090", legacyKey: "5001"},
			legacies: []string{legacyKey},
			want:     "9090",
		},
		{
			name:     "neither set — empty string (caller fail-fast)",
			setup:    map[string]string{},
			legacies: []string{legacyKey},
			want:     "",
		},
		{
			name:     "new key empty, legacy set — fallback",
			setup:    map[string]string{newKey: "", legacyKey: "5001"},
			legacies: []string{legacyKey},
			want:     "5001",
		},
		{
			name:     "multiple legacies — first non-empty wins",
			setup:    map[string]string{legacyKey2nd: "3333"},
			legacies: []string{legacyKey, legacyKey2nd},
			want:     "3333",
		},
		{
			name:     "no legacies — only new key",
			setup:    map[string]string{newKey: "8080"},
			legacies: nil,
			want:     "8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Cleanup(func() {
				_ = os.Unsetenv(newKey)
				_ = os.Unsetenv(legacyKey)
				_ = os.Unsetenv(legacyKey2nd)
				resetDeprecationWarned()
			})
			_ = os.Unsetenv(newKey)
			_ = os.Unsetenv(legacyKey)
			_ = os.Unsetenv(legacyKey2nd)
			for k, v := range tt.setup {
				if err := os.Setenv(k, v); err != nil {
					t.Fatalf("setenv %s: %v", k, err)
				}
			}

			got := envWithAlias(newKey, tt.legacies...)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestWarnDeprecated_OnlyOnce ensures the deprecation warning fires once per
// legacy key even when envWithAlias is invoked repeatedly (e.g. config reload).
func TestWarnDeprecated_OnlyOnce(t *testing.T) {
	const (
		newKey    = "TEST_DEP_NEW"
		legacyKey = "TEST_DEP_LEGACY"
	)
	t.Cleanup(func() {
		_ = os.Unsetenv(newKey)
		_ = os.Unsetenv(legacyKey)
		resetDeprecationWarned()
	})
	resetDeprecationWarned()
	if err := os.Setenv(legacyKey, "v"); err != nil {
		t.Fatalf("setenv: %v", err)
	}

	_ = envWithAlias(newKey, legacyKey)
	_ = envWithAlias(newKey, legacyKey)

	deprecationWarnedMu.Lock()
	_, seen := deprecationWarned[legacyKey]
	count := len(deprecationWarned)
	deprecationWarnedMu.Unlock()
	assert.True(t, seen, "legacy key should be recorded")
	assert.Equal(t, 1, count, "deprecation map should contain exactly one entry")
}

// TestWarnDeprecated_StructuredJSON verifies the warning line is a single
// JSON object with event=env_var_deprecated plus deprecated_key / use_instead
// fields — consistent with the production zap-style encoder and the
// collabops-api-piper config_with_alias structlog event.
func TestWarnDeprecated_StructuredJSON(t *testing.T) {
	const (
		newKey    = "TEST_FMT_NEW"
		legacyKey = "TEST_FMT_LEGACY"
	)

	r, w, err := os.Pipe()
	require.NoError(t, err)
	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() {
		os.Stderr = origStderr
		_ = os.Unsetenv(newKey)
		_ = os.Unsetenv(legacyKey)
		resetDeprecationWarned()
	})
	resetDeprecationWarned()
	if err := os.Setenv(legacyKey, "v"); err != nil {
		t.Fatalf("setenv: %v", err)
	}

	_ = envWithAlias(newKey, legacyKey)
	require.NoError(t, w.Close())

	out, err := io.ReadAll(r)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(out), &got), "warning line must be valid JSON: %q", out)
	assert.Equal(t, "warn", got["level"])
	assert.Equal(t, "env_var_deprecated", got["event"])
	assert.Equal(t, legacyKey, got["deprecated_key"])
	assert.Equal(t, newKey, got["use_instead"])
	assert.Equal(t, "doc-360-§7", got["doc"])
	assert.NotEmpty(t, got["ts"], "ts must be present")
}

// resetDeprecationWarned clears the one-shot warning state. Test-only helper.
func resetDeprecationWarned() {
	deprecationWarnedMu.Lock()
	defer deprecationWarnedMu.Unlock()
	deprecationWarned = make(map[string]struct{})
}
