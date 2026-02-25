package policy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

func TestPolicyEngine_Basic(t *testing.T) {
	// Create a temporary policy file
	tmpDir := t.TempDir()
	policyFile := filepath.Join(tmpDir, "test-policy.yml")

	policyContent := `
version: "1.0"
metadata:
  name: "Test Policy"
  description: "Test policy for unit tests"

policies:
  - name: "test-env-protection"
    description: "Test environment file protection"
    enabled: true
    type: "file_protection"
    scope:
      branches: ["main", "master"]
      repositories: ["*"]
    rules:
      - name: "env-files"
        type: "file_protection"
        patterns:
          - "*.env"
          - "config/env/**/*"
        action: "block_deletion"
        enforcement: "strict"

      - name: "config-files"
        type: "file_protection"
        patterns:
          - "config/*.yml"
          - "docker-compose.yml"
        action: "block_deletion"
        enforcement: "strict"
    enforcement: "strict"
    priority: 100
`

	err := os.WriteFile(policyFile, []byte(policyContent), 0644)
	require.NoError(t, err)

	// Create PolicyEngine
	provider := NewFileBasedProvider(policyFile)
	logger := zaptest.NewLogger(t)
	engine := NewEngine(provider, logger)

	// Initialize engine
	err = engine.Initialize(context.Background())
	require.NoError(t, err)

	// Test file protection evaluation
	testCases := []struct {
		name      string
		filePath  string
		protected bool
		reason    string
	}{
		{
			name:      "env_file_protected",
			filePath:  ".env",
			protected: true,
			reason:    "file matches protected pattern",
		},
		{
			name:      "config_env_file_protected",
			filePath:  "config/env/staging.env",
			protected: true,
			reason:    "file matches protected pattern",
		},
		{
			name:      "config_yml_protected",
			filePath:  "config/database.yml",
			protected: true,
			reason:    "file matches protected pattern",
		},
		{
			name:      "docker_compose_protected",
			filePath:  "docker-compose.yml",
			protected: true,
			reason:    "file matches protected pattern",
		},
		{
			name:      "regular_file_not_protected",
			filePath:  "src/main.go",
			protected: false,
			reason:    "no_matching_protection_rules",
		},
		{
			name:      "readme_not_protected",
			filePath:  "README.md",
			protected: false,
			reason:    "no_matching_protection_rules",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := engine.EvaluateFileProtection(context.Background(), tc.filePath)
			require.NoError(t, err)

			assert.Equal(t, tc.protected, decision.Protected,
				"File %s protection mismatch", tc.filePath)

			if tc.protected {
				assert.Contains(t, decision.Reason, "matches protected pattern",
					"Protected file should have appropriate reason")
			} else {
				assert.Equal(t, tc.reason, decision.Reason,
					"Unprotected file should have no_matching_protection_rules reason")
			}
		})
	}
}

func TestPolicyEngine_MergePolicy(t *testing.T) {
	// Create a temporary policy file
	tmpDir := t.TempDir()
	policyFile := filepath.Join(tmpDir, "merge-policy.yml")

	policyContent := `
version: "1.0"
metadata:
  name: "Merge Policy Test"

policies:
  - name: "merge-protection"
    enabled: true
    type: "file_protection"
    scope:
      branches: ["main"]
      repositories: ["*"]
    rules:
      - name: "critical-files"
        type: "file_protection"
        patterns:
          - "*.env"
          - "config/**/*.yml"
        action: "block_deletion"
        enforcement: "strict"
    enforcement: "strict"
    priority: 100
`

	err := os.WriteFile(policyFile, []byte(policyContent), 0644)
	require.NoError(t, err)

	// Create PolicyEngine
	provider := NewFileBasedProvider(policyFile)
	logger := zaptest.NewLogger(t)
	engine := NewEngine(provider, logger)

	// Initialize engine
	err = engine.Initialize(context.Background())
	require.NoError(t, err)

	// Test merge policy evaluation
	t.Run("blocking_merge_due_to_file_deletion", func(t *testing.T) {
		mergeReq := &MergeRequest{
			SourceRef:  "feature-branch",
			TargetRef:  "main",
			Method:     "squash",
			IsSquash:   true,
			Author:     "testuser",
			Repository: "test-repo",
			Branch:     "main",
			ChangedFiles: []FileChange{
				{
					Path:       ".env",
					ChangeType: "deleted",
				},
				{
					Path:       "config/database.yml",
					ChangeType: "deleted",
				},
				{
					Path:       "src/main.go",
					ChangeType: "modified",
				},
			},
		}

		decision, err := engine.EvaluateMergePolicy(context.Background(), mergeReq)
		require.NoError(t, err)

		assert.False(t, decision.Allowed, "Merge should be blocked due to protected file deletion")
		assert.Len(t, decision.BlockingRules, 2, "Should have 2 blocking rules for deleted protected files")

		// Check that the blocking rules mention the correct files
		filePaths := make([]string, len(decision.BlockingRules))
		for i, rule := range decision.BlockingRules {
			filePaths[i] = rule.FilePath
		}
		assert.Contains(t, filePaths, ".env")
		assert.Contains(t, filePaths, "config/database.yml")
	})

	t.Run("allowing_merge_no_protected_files", func(t *testing.T) {
		mergeReq := &MergeRequest{
			SourceRef:  "feature-branch",
			TargetRef:  "main",
			Method:     "squash",
			IsSquash:   true,
			Author:     "testuser",
			Repository: "test-repo",
			Branch:     "main",
			ChangedFiles: []FileChange{
				{
					Path:       "src/main.go",
					ChangeType: "modified",
				},
				{
					Path:       "README.md",
					ChangeType: "added",
				},
			},
		}

		decision, err := engine.EvaluateMergePolicy(context.Background(), mergeReq)
		require.NoError(t, err)

		assert.True(t, decision.Allowed, "Merge should be allowed when no protected files are affected")
		assert.Empty(t, decision.BlockingRules, "Should have no blocking rules")
	})
}

func TestPolicyEngine_Status(t *testing.T) {
	// Test uninitialized engine
	provider := NewFileBasedProvider("nonexistent.yml")
	logger := zaptest.NewLogger(t)
	engine := NewEngine(provider, logger)

	status := engine.GetPolicyStatus()
	assert.False(t, status.Initialized)
	assert.Equal(t, 0, status.PoliciesLoaded)
	assert.Equal(t, 0, status.RulesLoaded)

	// Test initialized engine
	tmpDir := t.TempDir()
	policyFile := filepath.Join(tmpDir, "status-test.yml")

	policyContent := `
version: "1.0"
metadata:
  name: "Status Test Policy"
  description: "Test policy for status validation"
policies:
  - name: "test-policy"
    enabled: true
    type: "file_protection"
    scope:
      branches: ["main"]
    rules:
      - name: "test-rule"
        type: "file_protection"
        patterns: ["*.env"]
        action: "block_deletion"
    enforcement: "strict"
`

	err := os.WriteFile(policyFile, []byte(policyContent), 0644)
	require.NoError(t, err)

	provider = NewFileBasedProvider(policyFile)
	engine = NewEngine(provider, logger)

	err = engine.Initialize(context.Background())
	require.NoError(t, err)

	status = engine.GetPolicyStatus()
	assert.True(t, status.Initialized)
	assert.Equal(t, 1, status.PoliciesLoaded)
	assert.Equal(t, 1, status.RulesLoaded)
	assert.Contains(t, status.ConfigSource, policyFile)
}

func TestNullPolicyEngine(t *testing.T) {
	engine := &NullPolicyEngine{}

	// Test file protection (should always return not protected)
	decision, err := engine.EvaluateFileProtection(context.Background(), ".env")
	require.NoError(t, err)
	assert.False(t, decision.Protected)
	assert.Equal(t, "policy_engine_disabled", decision.Reason)

	// Test merge policy (should always allow)
	mergeReq := &MergeRequest{
		SourceRef:    "feature",
		TargetRef:    "main",
		ChangedFiles: []FileChange{{Path: ".env", ChangeType: "deleted"}},
	}

	policyDecision, err := engine.EvaluateMergePolicy(context.Background(), mergeReq)
	require.NoError(t, err)
	assert.True(t, policyDecision.Allowed)

	// Test status
	status := engine.GetPolicyStatus()
	assert.False(t, status.Initialized)
	assert.Equal(t, "null", status.Version)
}
