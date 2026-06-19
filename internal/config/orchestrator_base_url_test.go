package config

import (
	"os"
	"testing"
)

// TestParseOrchestratorConfig_BaseURLAlias verifies the doc-526 env rename:
// parseOrchestratorConfig prefers the new BASE_URL_ORCHESTRATOR name, falls
// back to the legacy ORCHESTRATOR_BASE_URL while helm-charts/values catch up,
// and lets the new name win when both are set. Remove the legacy half together
// with the alias in Phase 3.
func TestParseOrchestratorConfig_BaseURLAlias(t *testing.T) {
	clear := func() {
		_ = os.Unsetenv("BASE_URL_ORCHESTRATOR")
		_ = os.Unsetenv("ORCHESTRATOR_BASE_URL")
	}

	tests := []struct {
		name   string
		newKey string
		oldKey string
		want   string
	}{
		{name: "new name honored", newKey: "http://new-orchestrator:2345", want: "http://new-orchestrator:2345"},
		{name: "legacy fallback", oldKey: "http://legacy-orchestrator:2345", want: "http://legacy-orchestrator:2345"},
		{
			name:   "new wins over legacy",
			newKey: "http://new-orchestrator:2345",
			oldKey: "http://legacy-orchestrator:2345",
			want:   "http://new-orchestrator:2345",
		},
		{name: "neither set", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clear()
			resetDeprecationWarned()
			t.Cleanup(func() {
				clear()
				resetDeprecationWarned()
			})
			if tt.newKey != "" {
				t.Setenv("BASE_URL_ORCHESTRATOR", tt.newKey)
			}
			if tt.oldKey != "" {
				t.Setenv("ORCHESTRATOR_BASE_URL", tt.oldKey)
			}

			got := parseOrchestratorConfig()
			if got.BaseURL != tt.want {
				t.Errorf("BaseURL = %q, want %q", got.BaseURL, tt.want)
			}
		})
	}
}
