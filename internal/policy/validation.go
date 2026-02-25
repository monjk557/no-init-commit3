package policy

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// PolicyValidator validates policy configurations
type PolicyValidator struct {
	schemaVersion     string
	validActions      map[string]bool
	validTypes        map[string]bool
	validEnforcements map[string]bool
}

// NewPolicyValidator creates a new policy validator
func NewPolicyValidator() *PolicyValidator {
	return &PolicyValidator{
		schemaVersion: "1.0",
		validActions: map[string]bool{
			"block_deletion":     true,
			"require_approval":   true,
			"warn":               true,
			"allow":              true,
			"block_modification": true,
			"require_review":     true,
		},
		validTypes: map[string]bool{
			"file_protection":      true,
			"merge_request_policy": true,
			"approval_requirement": true,
			"branch_protection":    true,
			"security_scan":        true,
		},
		validEnforcements: map[string]bool{
			"strict":   true,
			"warn":     true,
			"disabled": true,
			"audit":    true,
		},
	}
}

// ValidateConfiguration validates the entire policy configuration
func (v *PolicyValidator) ValidateConfiguration(config *PolicyConfiguration) error {
	if config == nil {
		return fmt.Errorf("configuration is nil")
	}

	// Validate version
	if err := v.validateVersion(config.Version); err != nil {
		return fmt.Errorf("version validation failed: %w", err)
	}

	// Validate metadata
	if err := v.validateMetadata(&config.Metadata); err != nil {
		return fmt.Errorf("metadata validation failed: %w", err)
	}

	// Validate policies
	if err := v.validatePolicies(config.Policies); err != nil {
		return fmt.Errorf("policies validation failed: %w", err)
	}

	// Validate templates
	if err := v.validateTemplates(config.Templates); err != nil {
		return fmt.Errorf("templates validation failed: %w", err)
	}

	// Validate overrides
	if err := v.validateOverrides(config.Overrides, config.Policies); err != nil {
		return fmt.Errorf("overrides validation failed: %w", err)
	}

	// Cross-validation
	if err := v.validateCrossReferences(config); err != nil {
		return fmt.Errorf("cross-reference validation failed: %w", err)
	}

	return nil
}

// validateVersion validates the configuration version
func (v *PolicyValidator) validateVersion(version string) error {
	if version == "" {
		return fmt.Errorf("version is required")
	}

	// Validate semantic version format
	semverPattern := `^(\d+)\.(\d+)(?:\.(\d+))?(?:-([a-zA-Z0-9.-]+))?(?:\+([a-zA-Z0-9.-]+))?$`
	if matched, _ := regexp.MatchString(semverPattern, version); !matched {
		return fmt.Errorf("invalid version format: %s (expected semantic version)", version)
	}

	return nil
}

// validateMetadata validates the policy metadata
func (v *PolicyValidator) validateMetadata(metadata *PolicyMetadata) error {
	if metadata.Name == "" {
		return fmt.Errorf("metadata.name is required")
	}

	if len(metadata.Name) > 100 {
		return fmt.Errorf("metadata.name too long (max 100 characters): %s", metadata.Name)
	}

	if metadata.Description != "" && len(metadata.Description) > 500 {
		return fmt.Errorf("metadata.description too long (max 500 characters)")
	}

	// Validate author format (simple email validation)
	if metadata.Author != "" {
		emailPattern := `^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`
		if matched, _ := regexp.MatchString(emailPattern, metadata.Author); !matched {
			// Allow non-email formats like "Team Name" or "username"
			if strings.Contains(metadata.Author, "@") {
				return fmt.Errorf("invalid author email format: %s", metadata.Author)
			}
		}
	}

	return nil
}

// validatePolicies validates all policies
func (v *PolicyValidator) validatePolicies(policies []PolicyDefinition) error {
	if len(policies) == 0 {
		return fmt.Errorf("at least one policy is required")
	}

	if len(policies) > 100 {
		return fmt.Errorf("too many policies (max 100): %d", len(policies))
	}

	policyNames := make(map[string]bool)

	for i, policy := range policies {
		if err := v.validatePolicy(&policy, i); err != nil {
			return fmt.Errorf("policy %d (%s): %w", i, policy.Name, err)
		}

		// Check for duplicate names
		if policyNames[policy.Name] {
			return fmt.Errorf("duplicate policy name: %s", policy.Name)
		}
		policyNames[policy.Name] = true
	}

	return nil
}

// validatePolicy validates a single policy
func (v *PolicyValidator) validatePolicy(policy *PolicyDefinition, index int) error {
	// Validate name
	if policy.Name == "" {
		return fmt.Errorf("name is required")
	}

	if !v.isValidPolicyName(policy.Name) {
		return fmt.Errorf("invalid policy name: %s (must be lowercase, alphanumeric, hyphens only)", policy.Name)
	}

	// Validate type
	if policy.Type == "" {
		return fmt.Errorf("type is required")
	}

	if !v.validTypes[policy.Type] {
		return fmt.Errorf("invalid type: %s", policy.Type)
	}

	// Validate enforcement
	if policy.Enforcement != "" && !v.validEnforcements[policy.Enforcement] {
		return fmt.Errorf("invalid enforcement level: %s", policy.Enforcement)
	}

	// Validate priority
	if policy.Priority < 0 || policy.Priority > 1000 {
		return fmt.Errorf("priority must be between 0 and 1000: %d", policy.Priority)
	}

	// Validate scope
	if err := v.validateScope(&policy.Scope); err != nil {
		return fmt.Errorf("scope validation failed: %w", err)
	}

	// Validate rules
	if err := v.validateRules(policy.Rules, policy.Type); err != nil {
		return fmt.Errorf("rules validation failed: %w", err)
	}

	return nil
}

// validateScope validates policy scope
func (v *PolicyValidator) validateScope(scope *PolicyScope) error {
	// Validate branch patterns
	for _, branch := range scope.Branches {
		if strings.TrimSpace(branch) == "" {
			return fmt.Errorf("empty branch pattern not allowed")
		}
		if len(branch) > 100 {
			return fmt.Errorf("branch pattern too long (max 100 characters): %s", branch)
		}
	}

	// Validate repository patterns
	for _, repo := range scope.Repositories {
		if strings.TrimSpace(repo) == "" {
			return fmt.Errorf("empty repository pattern not allowed")
		}
		if len(repo) > 200 {
			return fmt.Errorf("repository pattern too long (max 200 characters): %s", repo)
		}
	}

	// Validate users
	for _, user := range scope.Users {
		if strings.TrimSpace(user) == "" {
			return fmt.Errorf("empty user pattern not allowed")
		}
		if len(user) > 50 {
			return fmt.Errorf("user pattern too long (max 50 characters): %s", user)
		}
	}

	// Validate teams
	for _, team := range scope.Teams {
		if strings.TrimSpace(team) == "" {
			return fmt.Errorf("empty team pattern not allowed")
		}
		if len(team) > 50 {
			return fmt.Errorf("team pattern too long (max 50 characters): %s", team)
		}
	}

	return nil
}

// validateRules validates policy rules
func (v *PolicyValidator) validateRules(rules []PolicyRule, policyType string) error {
	if len(rules) == 0 {
		return fmt.Errorf("at least one rule is required")
	}

	if len(rules) > 50 {
		return fmt.Errorf("too many rules (max 50): %d", len(rules))
	}

	ruleNames := make(map[string]bool)

	for i, rule := range rules {
		if err := v.validateRule(&rule, policyType, i); err != nil {
			return fmt.Errorf("rule %d (%s): %w", i, rule.Name, err)
		}

		// Check for duplicate names within the policy
		if ruleNames[rule.Name] {
			return fmt.Errorf("duplicate rule name: %s", rule.Name)
		}
		ruleNames[rule.Name] = true
	}

	return nil
}

// validateRule validates a single rule
func (v *PolicyValidator) validateRule(rule *PolicyRule, policyType string, index int) error {
	// Validate name
	if rule.Name == "" {
		return fmt.Errorf("name is required")
	}

	if !v.isValidRuleName(rule.Name) {
		return fmt.Errorf("invalid rule name: %s (must be lowercase, alphanumeric, hyphens/underscores only)", rule.Name)
	}

	// Validate type
	if rule.Type == "" {
		return fmt.Errorf("type is required")
	}

	// Validate action
	if rule.Action == "" {
		return fmt.Errorf("action is required")
	}

	if !v.validActions[rule.Action] {
		return fmt.Errorf("invalid action: %s", rule.Action)
	}

	// Validate enforcement
	if rule.Enforcement != "" && !v.validEnforcements[rule.Enforcement] {
		return fmt.Errorf("invalid enforcement level: %s", rule.Enforcement)
	}

	// Type-specific validation
	switch rule.Type {
	case "file_protection":
		if err := v.validateFileProtectionRule(rule); err != nil {
			return fmt.Errorf("file protection rule validation failed: %w", err)
		}
	case "approval_requirement":
		if err := v.validateApprovalRule(rule); err != nil {
			return fmt.Errorf("approval requirement rule validation failed: %w", err)
		}
	default:
		if !v.validTypes[rule.Type] {
			return fmt.Errorf("invalid rule type: %s", rule.Type)
		}
	}

	return nil
}

// validateFileProtectionRule validates file protection specific rules
func (v *PolicyValidator) validateFileProtectionRule(rule *PolicyRule) error {
	// File protection rules must have patterns
	if len(rule.Patterns) == 0 {
		return fmt.Errorf("file protection rules require at least one pattern")
	}

	if len(rule.Patterns) > 20 {
		return fmt.Errorf("too many patterns (max 20): %d", len(rule.Patterns))
	}

	// Validate each pattern
	for i, pattern := range rule.Patterns {
		if strings.TrimSpace(pattern) == "" {
			return fmt.Errorf("pattern %d: empty pattern not allowed", i)
		}

		if len(pattern) > 200 {
			return fmt.Errorf("pattern %d too long (max 200 characters): %s", i, pattern)
		}

		// Basic pattern validation (ensure it's not obviously malformed)
		if err := v.validateFilePattern(pattern); err != nil {
			return fmt.Errorf("pattern %d (%s): %w", i, pattern, err)
		}
	}

	// Validate action is appropriate for file protection
	validFileActions := map[string]bool{
		"block_deletion":     true,
		"require_approval":   true,
		"warn":               true,
		"block_modification": true,
	}

	if !validFileActions[rule.Action] {
		return fmt.Errorf("action %s not valid for file protection (valid: block_deletion, require_approval, warn, block_modification)", rule.Action)
	}

	return nil
}

// validateApprovalRule validates approval requirement rules
func (v *PolicyValidator) validateApprovalRule(rule *PolicyRule) error {
	// Check if metadata contains approver information
	if rule.Metadata == nil {
		return fmt.Errorf("approval rules require metadata with approver information")
	}

	// Validate action is appropriate for approval requirements
	validApprovalActions := map[string]bool{
		"require_approval": true,
		"require_review":   true,
	}

	if !validApprovalActions[rule.Action] {
		return fmt.Errorf("action %s not valid for approval requirements (valid: require_approval, require_review)", rule.Action)
	}

	return nil
}

// validateTemplates validates policy templates
func (v *PolicyValidator) validateTemplates(templates []PolicyTemplate) error {
	templateNames := make(map[string]bool)

	for i, template := range templates {
		if err := v.validateTemplate(&template, i); err != nil {
			return fmt.Errorf("template %d (%s): %w", i, template.Name, err)
		}

		// Check for duplicate names
		if templateNames[template.Name] {
			return fmt.Errorf("duplicate template name: %s", template.Name)
		}
		templateNames[template.Name] = true
	}

	return nil
}

// validateTemplate validates a single template
func (v *PolicyValidator) validateTemplate(template *PolicyTemplate, index int) error {
	// Validate name
	if template.Name == "" {
		return fmt.Errorf("name is required")
	}

	if !v.isValidTemplateName(template.Name) {
		return fmt.Errorf("invalid template name: %s (must be lowercase, alphanumeric, hyphens only)", template.Name)
	}

	// Validate type
	if template.Type == "" {
		return fmt.Errorf("type is required")
	}

	if !v.validTypes[template.Type] {
		return fmt.Errorf("invalid type: %s", template.Type)
	}

	// Validate rules
	if err := v.validateRules(template.Rules, template.Type); err != nil {
		return fmt.Errorf("template rules validation failed: %w", err)
	}

	// Validate variables (if any)
	if err := v.validateTemplateVariables(template.Variables); err != nil {
		return fmt.Errorf("template variables validation failed: %w", err)
	}

	return nil
}

// validateTemplateVariables validates template variables
func (v *PolicyValidator) validateTemplateVariables(variables map[string]interface{}) error {
	for key, value := range variables {
		if !v.isValidVariableName(key) {
			return fmt.Errorf("invalid variable name: %s (must be alphanumeric, underscores only)", key)
		}

		// Validate variable types (strings, numbers, booleans)
		switch value.(type) {
		case string, int, int64, float64, bool:
			// Valid types
		default:
			return fmt.Errorf("invalid variable type for %s: %T (must be string, number, or boolean)", key, value)
		}
	}

	return nil
}

// validateOverrides validates policy overrides
func (v *PolicyValidator) validateOverrides(overrides []PolicyOverride, policies []PolicyDefinition) error {
	policyNames := make(map[string]bool)
	for _, policy := range policies {
		policyNames[policy.Name] = true
	}

	for i, override := range overrides {
		if err := v.validateOverride(&override, policyNames, i); err != nil {
			return fmt.Errorf("override %d: %w", i, err)
		}
	}

	return nil
}

// validateOverride validates a single override
func (v *PolicyValidator) validateOverride(override *PolicyOverride, policyNames map[string]bool, index int) error {
	// Validate policy name
	if override.PolicyName == "" {
		return fmt.Errorf("policy_name is required")
	}

	if !policyNames[override.PolicyName] {
		return fmt.Errorf("references unknown policy: %s", override.PolicyName)
	}

	// Validate scope
	if err := v.validateScope(&override.Scope); err != nil {
		return fmt.Errorf("scope validation failed: %w", err)
	}

	// Validate enforcement override
	if override.Enforcement != nil && !v.validEnforcements[*override.Enforcement] {
		return fmt.Errorf("invalid enforcement override: %s", *override.Enforcement)
	}

	return nil
}

// validateCrossReferences validates cross-references between different parts of the configuration
func (v *PolicyValidator) validateCrossReferences(config *PolicyConfiguration) error {
	// Build maps for quick lookup
	policyNames := make(map[string]*PolicyDefinition)
	templateNames := make(map[string]*PolicyTemplate)

	for i := range config.Policies {
		policyNames[config.Policies[i].Name] = &config.Policies[i]
	}

	for i := range config.Templates {
		templateNames[config.Templates[i].Name] = &config.Templates[i]
	}

	// Validate that overrides reference existing policies (already done above)
	// Validate template references in policies (if we add template inheritance later)

	return nil
}

// validateFilePattern validates file patterns for basic correctness
func (v *PolicyValidator) validateFilePattern(pattern string) error {
	// Check for obviously malformed patterns
	if strings.Contains(pattern, "//") {
		return fmt.Errorf("pattern contains double slashes: %s", pattern)
	}

	if strings.HasPrefix(pattern, "/") && !strings.HasPrefix(pattern, "/*") {
		return fmt.Errorf("absolute paths not allowed (patterns should be relative): %s", pattern)
	}

	// Check for dangerous patterns
	if pattern == "*" || pattern == "**" || pattern == "**/*" {
		return fmt.Errorf("overly broad pattern not allowed: %s (would match all files)", pattern)
	}

	return nil
}

// Helper validation functions

func (v *PolicyValidator) isValidPolicyName(name string) bool {
	// Allow lowercase alphanumeric and hyphens
	pattern := `^[a-z0-9-]+$`
	matched, _ := regexp.MatchString(pattern, name)
	return matched && len(name) <= 50
}

func (v *PolicyValidator) isValidRuleName(name string) bool {
	// Allow lowercase alphanumeric, hyphens, and underscores
	pattern := `^[a-z0-9-_]+$`
	matched, _ := regexp.MatchString(pattern, name)
	return matched && len(name) <= 50
}

func (v *PolicyValidator) isValidTemplateName(name string) bool {
	// Allow lowercase alphanumeric and hyphens
	pattern := `^[a-z0-9-]+$`
	matched, _ := regexp.MatchString(pattern, name)
	return matched && len(name) <= 50
}

func (v *PolicyValidator) isValidVariableName(name string) bool {
	// Allow alphanumeric and underscores
	pattern := `^[a-zA-Z][a-zA-Z0-9_]*$`
	matched, _ := regexp.MatchString(pattern, name)
	return matched && len(name) <= 30
}

// GetValidationReport returns a detailed validation report
func (v *PolicyValidator) GetValidationReport(config *PolicyConfiguration) *ValidationReport {
	report := &ValidationReport{
		Valid:     true,
		Errors:    make([]ValidationError, 0),
		Warnings:  make([]ValidationWarning, 0),
		Timestamp: time.Now(),
		Summary: ValidationSummary{
			TotalPolicies:  len(config.Policies),
			TotalRules:     0,
			TotalTemplates: len(config.Templates),
			TotalOverrides: len(config.Overrides),
		},
	}

	// Count total rules
	for _, policy := range config.Policies {
		report.Summary.TotalRules += len(policy.Rules)
	}

	// Validate and collect errors
	if err := v.ValidateConfiguration(config); err != nil {
		report.Valid = false
		report.Errors = append(report.Errors, ValidationError{
			Type:    "configuration",
			Message: err.Error(),
			Path:    "root",
		})
	}

	// Add performance warnings for large configurations
	if len(config.Policies) > 50 {
		report.Warnings = append(report.Warnings, ValidationWarning{
			Type:    "performance",
			Message: fmt.Sprintf("Large number of policies (%d) may impact performance", len(config.Policies)),
			Path:    "policies",
		})
	}

	if report.Summary.TotalRules > 500 {
		report.Warnings = append(report.Warnings, ValidationWarning{
			Type:    "performance",
			Message: fmt.Sprintf("Large number of rules (%d) may impact performance", report.Summary.TotalRules),
			Path:    "rules",
		})
	}

	return report
}

// Note: ValidationReport, ValidationError, ValidationWarning, and ValidationSummary are defined in interfaces.go
