package policy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

func TestTemplateManager(t *testing.T) {
	logger := zaptest.NewLogger(t)
	tm := NewTemplateManager(logger)

	// Define a test template
	templates := []PolicyTemplate{
		{
			Name:        "file-protection-template",
			Description: "Template for protecting {{file_type}} files",
			Type:        "file_protection",
			Rules: []PolicyRule{
				{
					Name:        "protect-files",
					Type:        "file_protection",
					Patterns:    []string{"${path_prefix}/**/*{{file_extension}}"},
					Action:      "block_deletion",
					Enforcement: "strict",
				},
			},
			Variables: map[string]interface{}{
				"file_type":      "environment",
				"path_prefix":    "config",
				"file_extension": ".env",
			},
		},
	}

	// Load templates
	err := tm.LoadTemplates(templates)
	require.NoError(t, err)

	// Test template instantiation
	variables := map[string]interface{}{
		"file_type":      "configuration",
		"path_prefix":    "configs",
		"file_extension": ".yaml",
	}

	policy, err := tm.InstantiateTemplate("file-protection-template", "config-protection", variables)
	require.NoError(t, err)

	assert.Equal(t, "config-protection", policy.Name)
	assert.Equal(t, "file_protection", policy.Type)
	assert.Len(t, policy.Rules, 1)

	rule := policy.Rules[0]
	assert.Equal(t, "protect-files", rule.Name)
	assert.Equal(t, []string{"configs/**/*.yaml"}, rule.Patterns)
	assert.Equal(t, "block_deletion", rule.Action)
}

func TestHierarchicalPolicyManager(t *testing.T) {
	logger := zaptest.NewLogger(t)
	hpm := NewHierarchicalPolicyManager(logger)

	// Create test layers
	highPriorityLayer := PolicyLayer{
		Name:     "critical",
		Priority: 1000,
		Enabled:  true,
		Policies: []PolicyDefinition{
			{
				Name:    "critical-files",
				Enabled: true,
				Type:    "file_protection",
				Rules: []PolicyRule{
					{
						Name:     "block-env-deletion",
						Type:     "file_protection",
						Patterns: []string{"*.env"},
						Action:   "block_deletion",
					},
				},
			},
		},
	}

	lowPriorityLayer := PolicyLayer{
		Name:     "standard",
		Priority: 100,
		Enabled:  true,
		Policies: []PolicyDefinition{
			{
				Name:    "standard-files",
				Enabled: true,
				Type:    "file_protection",
				Rules: []PolicyRule{
					{
						Name:     "warn-config-deletion",
						Type:     "file_protection",
						Patterns: []string{"*.env"},
						Action:   "warn",
					},
				},
			},
		},
	}

	hpm.AddLayer(highPriorityLayer)
	hpm.AddLayer(lowPriorityLayer)

	// Test hierarchical evaluation
	decision, err := hpm.EvaluateFileProtectionHierarchical(context.Background(), "test.env")
	require.NoError(t, err)

	assert.Equal(t, "test.env", decision.FilePath)
	assert.Len(t, decision.LayerDecisions, 2)

	// With MostRestrictiveWins strategy, should block deletion
	assert.True(t, decision.FinalDecision.Protected)
	assert.Contains(t, decision.FinalDecision.Reason, "critical-files")

	// Test with HighestPriorityWins strategy
	hpm.SetConflictResolution(HighestPriorityWins)
	decision, err = hpm.EvaluateFileProtectionHierarchical(context.Background(), "test.env")
	require.NoError(t, err)

	assert.True(t, decision.FinalDecision.Protected)
	assert.Contains(t, decision.FinalDecision.Metadata["layer_name"], "critical")
}

func TestAdvancedPolicyEngine(t *testing.T) {
	// Create a temporary policy file with templates
	tmpDir := t.TempDir()
	policyFile := filepath.Join(tmpDir, "advanced-policy.yml")

	policyContent := `
version: "1.0"
metadata:
  name: "Advanced Policy Test"
  description: "Testing Phase 2 features"

templates:
  - name: "env-protection"
    description: "Protect {{env_type}} environment files"
    type: "file_protection"
    rules:
      - name: "env-files"
        type: "file_protection"
        patterns:
          - "${base_path}/**/*.env"
          - "{{custom_pattern}}"
        action: "block_deletion"
        enforcement: "strict"
    variables:
      env_type: "production"
      base_path: "config"
      custom_pattern: "*.environment"

policies:
  - name: "critical-env-protection"
    description: "Critical environment file protection"
    enabled: true
    type: "file_protection"
    priority: 1000
    scope:
      branches: ["main", "master"]
      repositories: ["*"]
    rules:
      - name: "env-files"
        type: "file_protection"
        patterns:
          - "*.env"
          - "config/**/*.env"
        action: "block_deletion"
        enforcement: "strict"

  - name: "standard-config-protection"
    description: "Standard config file protection"
    enabled: true
    type: "file_protection"
    priority: 100
    scope:
      branches: ["main"]
    rules:
      - name: "config-files"
        type: "file_protection"
        patterns:
          - "config/*.yml"
          - "config/*.yaml"
        action: "require_approval"
        enforcement: "strict"
    enforcement: "strict"
`

	err := os.WriteFile(policyFile, []byte(policyContent), 0644)
	require.NoError(t, err)

	// Create engine with Phase 2 features
	provider := NewFileBasedProvider(policyFile)
	logger := zaptest.NewLogger(t)
	engine := NewEngine(provider, logger)

	// Initialize engine
	err = engine.Initialize(context.Background())
	require.NoError(t, err)

	// Test template functionality
	templates := engine.ListTemplates()
	assert.Contains(t, templates, "env-protection")

	template, exists := engine.GetTemplate("env-protection")
	assert.True(t, exists)
	assert.Equal(t, "env-protection", template.Name)

	// Test template instantiation
	variables := map[string]interface{}{
		"env_type":       "staging",
		"base_path":      "environments",
		"custom_pattern": "*.config",
	}

	policy, err := engine.InstantiateTemplate("env-protection", "staging-protection", variables)
	require.NoError(t, err)
	assert.Equal(t, "staging-protection", policy.Name)
	assert.Contains(t, policy.Description, "staging")

	// Test hierarchical evaluation
	decision, err := engine.EvaluateFileProtectionHierarchical(context.Background(), "config/production.env")
	require.NoError(t, err)

	assert.Equal(t, "config/production.env", decision.FilePath)
	assert.True(t, decision.FinalDecision.Protected)
	assert.Len(t, decision.LayerDecisions, 2) // Two priority levels

	// Test layer status
	layerStatus := engine.GetLayerStatus()
	assert.Equal(t, 2, layerStatus.TotalLayers)
	assert.Equal(t, 2, layerStatus.EnabledLayers)

	// Test validation report
	report := engine.GetValidationReport()
	assert.True(t, report.Valid)
	assert.Empty(t, report.Errors)
	assert.Equal(t, 2, report.Summary.TotalPolicies)
	assert.Equal(t, 1, report.Summary.TotalTemplates)
}

func TestHotReloadManager(t *testing.T) {
	// Create a temporary policy file
	tmpDir := t.TempDir()
	policyFile := filepath.Join(tmpDir, "hot-reload-test.yml")

	initialContent := `
version: "1.0"
metadata:
  name: "Hot Reload Test"

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
    priority: 100
`

	err := os.WriteFile(policyFile, []byte(initialContent), 0644)
	require.NoError(t, err)

	// Create engine
	provider := NewFileBasedProvider(policyFile)
	logger := zaptest.NewLogger(t)
	engine := NewEngine(provider, logger)

	err = engine.Initialize(context.Background())
	require.NoError(t, err)

	// Test that hot reload is initially disabled
	status := engine.GetReloadStatus()
	assert.False(t, status.Running)

	// Enable hot reload
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = engine.EnableHotReload(ctx)
	require.NoError(t, err)

	// Check that hot reload is now running
	status = engine.GetReloadStatus()
	assert.True(t, status.Running)
	assert.Equal(t, 1, status.WatchedFilesCount) // Should watch the policy file

	// Test disabling hot reload
	err = engine.DisableHotReload()
	require.NoError(t, err)

	status = engine.GetReloadStatus()
	assert.False(t, status.Running)
}

func TestPolicyValidator(t *testing.T) {
	validator := NewPolicyValidator()

	// Test valid configuration
	validConfig := &PolicyConfiguration{
		Version: "1.0.0",
		Metadata: PolicyMetadata{
			Name:        "Test Config",
			Description: "A test configuration",
			Author:      "test@example.com",
		},
		Policies: []PolicyDefinition{
			{
				Name:        "test-policy",
				Description: "Test policy",
				Enabled:     true,
				Type:        "file_protection",
				Priority:    100,
				Scope: PolicyScope{
					Branches: []string{"main"},
				},
				Rules: []PolicyRule{
					{
						Name:     "test-rule",
						Type:     "file_protection",
						Patterns: []string{"*.env"},
						Action:   "block_deletion",
					},
				},
				Enforcement: "strict",
			},
		},
	}

	err := validator.ValidateConfiguration(validConfig)
	assert.NoError(t, err)

	// Test validation report
	report := validator.GetValidationReport(validConfig)
	assert.True(t, report.Valid)
	assert.Empty(t, report.Errors)
	assert.Equal(t, 1, report.Summary.TotalPolicies)
	assert.Equal(t, 1, report.Summary.TotalRules)

	// Test invalid configuration
	invalidConfig := &PolicyConfiguration{
		Version: "", // Missing version
		Policies: []PolicyDefinition{
			{
				Name:    "", // Missing name
				Enabled: true,
				Type:    "invalid_type", // Invalid type
				Rules:   []PolicyRule{}, // No rules
			},
		},
	}

	err = validator.ValidateConfiguration(invalidConfig)
	assert.Error(t, err)

	// Test validation report for invalid config
	report = validator.GetValidationReport(invalidConfig)
	assert.False(t, report.Valid)
	assert.NotEmpty(t, report.Errors)
}

func TestConflictResolutionStrategies(t *testing.T) {
	logger := zaptest.NewLogger(t)
	hpm := NewHierarchicalPolicyManager(logger)

	// Create conflicting layers
	blockLayer := PolicyLayer{
		Name:     "security",
		Priority: 1000,
		Enabled:  true,
		Policies: []PolicyDefinition{
			{
				Name:    "security-policy",
				Enabled: true,
				Type:    "file_protection",
				Rules: []PolicyRule{
					{
						Name:     "block-sensitive",
						Type:     "file_protection",
						Patterns: []string{"*.env"},
						Action:   "block_deletion",
					},
				},
			},
		},
	}

	warnLayer := PolicyLayer{
		Name:     "development",
		Priority: 500,
		Enabled:  true,
		Policies: []PolicyDefinition{
			{
				Name:    "dev-policy",
				Enabled: true,
				Type:    "file_protection",
				Rules: []PolicyRule{
					{
						Name:     "warn-env",
						Type:     "file_protection",
						Patterns: []string{"*.env"},
						Action:   "warn",
					},
				},
			},
		},
	}

	hpm.AddLayer(blockLayer)
	hpm.AddLayer(warnLayer)

	// Test MostRestrictiveWins (default)
	hpm.SetConflictResolution(MostRestrictiveWins)
	decision, err := hpm.EvaluateFileProtectionHierarchical(context.Background(), "test.env")
	require.NoError(t, err)
	assert.True(t, decision.FinalDecision.Protected)
	assert.Contains(t, decision.FinalDecision.Metadata["rule_action"], "block_deletion")

	// Test HighestPriorityWins
	hpm.SetConflictResolution(HighestPriorityWins)
	decision, err = hpm.EvaluateFileProtectionHierarchical(context.Background(), "test.env")
	require.NoError(t, err)
	assert.True(t, decision.FinalDecision.Protected)
	assert.Contains(t, decision.FinalDecision.Metadata["layer_name"], "security")

	// Test CompositeDecision
	hpm.SetConflictResolution(CompositeDecision)
	decision, err = hpm.EvaluateFileProtectionHierarchical(context.Background(), "test.env")
	require.NoError(t, err)
	assert.True(t, decision.FinalDecision.Protected)
	assert.Contains(t, decision.FinalDecision.Reason, "composite decision")
}
