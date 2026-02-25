package policy

import (
	"fmt"
	"regexp"
	"strings"
	"text/template"

	"go.uber.org/zap"
)

// TemplateManager handles policy template operations
type TemplateManager struct {
	templates map[string]*PolicyTemplate
	logger    *zap.Logger
}

// NewTemplateManager creates a new template manager
func NewTemplateManager(logger *zap.Logger) *TemplateManager {
	if logger == nil {
		logger = zap.NewNop()
	}

	return &TemplateManager{
		templates: make(map[string]*PolicyTemplate),
		logger:    logger,
	}
}

// LoadTemplates loads templates from configuration
func (tm *TemplateManager) LoadTemplates(templates []PolicyTemplate) error {
	tm.templates = make(map[string]*PolicyTemplate)

	for i := range templates {
		template := &templates[i]

		// Validate template
		if err := tm.validateTemplate(template); err != nil {
			return fmt.Errorf("template %s validation failed: %w", template.Name, err)
		}

		tm.templates[template.Name] = template
		tm.logger.Debug("Loaded policy template",
			zap.String("name", template.Name),
			zap.String("type", template.Type),
			zap.Int("variables", len(template.Variables)))
	}

	tm.logger.Info("Policy templates loaded",
		zap.Int("count", len(tm.templates)))

	return nil
}

// InstantiateTemplate creates a policy from a template with given variables
func (tm *TemplateManager) InstantiateTemplate(templateName string, instanceName string, variables map[string]interface{}) (*PolicyDefinition, error) {
	template, exists := tm.templates[templateName]
	if !exists {
		return nil, fmt.Errorf("template not found: %s", templateName)
	}

	// Merge template defaults with provided variables
	mergedVars := tm.mergeVariables(template.Variables, variables)

	// Create policy from template
	policy := &PolicyDefinition{
		Name:        instanceName,
		Description: template.Description,
		Type:        template.Type,
		Enabled:     true,          // Default to enabled
		Scope:       PolicyScope{}, // Default empty scope
		Priority:    100,           // Default priority
		Enforcement: "strict",      // Default enforcement
		Rules:       make([]PolicyRule, len(template.Rules)),
	}

	// Process template variables in description
	if processedDesc, err := tm.processTemplateString(policy.Description, mergedVars); err == nil {
		policy.Description = processedDesc
	}

	// Instantiate rules from template
	for i, templateRule := range template.Rules {
		rule, err := tm.instantiateRule(&templateRule, mergedVars)
		if err != nil {
			return nil, fmt.Errorf("failed to instantiate rule %s: %w", templateRule.Name, err)
		}
		policy.Rules[i] = *rule
	}

	// Apply any scope variable substitution
	if err := tm.processScope(&policy.Scope, mergedVars); err != nil {
		return nil, fmt.Errorf("failed to process scope: %w", err)
	}

	tm.logger.Info("Policy instantiated from template",
		zap.String("template", templateName),
		zap.String("instance", instanceName),
		zap.Int("rules", len(policy.Rules)))

	return policy, nil
}

// instantiateRule creates a rule from a template rule with variables
func (tm *TemplateManager) instantiateRule(templateRule *PolicyRule, variables map[string]interface{}) (*PolicyRule, error) {
	rule := &PolicyRule{
		Name:        templateRule.Name,
		Type:        templateRule.Type,
		Action:      templateRule.Action,
		Enforcement: templateRule.Enforcement,
		Scope:       templateRule.Scope,
		Patterns:    make([]string, len(templateRule.Patterns)),
		Metadata:    make(map[string]interface{}),
	}

	// Process patterns with variable substitution
	for i, pattern := range templateRule.Patterns {
		processed, err := tm.processTemplateString(pattern, variables)
		if err != nil {
			return nil, fmt.Errorf("failed to process pattern %s: %w", pattern, err)
		}
		rule.Patterns[i] = processed
	}

	// Copy and process metadata
	for key, value := range templateRule.Metadata {
		if strValue, ok := value.(string); ok {
			if processed, err := tm.processTemplateString(strValue, variables); err == nil {
				rule.Metadata[key] = processed
			} else {
				rule.Metadata[key] = value
			}
		} else {
			rule.Metadata[key] = value
		}
	}

	return rule, nil
}

// processTemplateString processes template variables in a string
func (tm *TemplateManager) processTemplateString(templateStr string, variables map[string]interface{}) (string, error) {
	// Handle both {{ variable }} and ${variable} syntax
	tmpl, err := template.New("policy").Parse(templateStr)
	if err != nil {
		// If template parsing fails, try simple variable substitution
		return tm.simpleVariableSubstitution(templateStr, variables), nil
	}

	var result strings.Builder
	if err := tmpl.Execute(&result, variables); err != nil {
		// Fallback to simple substitution
		return tm.simpleVariableSubstitution(templateStr, variables), nil
	}

	return result.String(), nil
}

// simpleVariableSubstitution performs simple ${var} and {{var}} substitution
func (tm *TemplateManager) simpleVariableSubstitution(text string, variables map[string]interface{}) string {
	// Replace ${variable} patterns
	dollarPattern := regexp.MustCompile(`\$\{([^}]+)\}`)
	text = dollarPattern.ReplaceAllStringFunc(text, func(match string) string {
		varName := strings.TrimSpace(match[2 : len(match)-1]) // Remove ${ and }
		if value, exists := variables[varName]; exists {
			return fmt.Sprintf("%v", value)
		}
		return match // Keep original if variable not found
	})

	// Replace {{variable}} patterns
	bracePattern := regexp.MustCompile(`\{\{([^}]+)\}\}`)
	text = bracePattern.ReplaceAllStringFunc(text, func(match string) string {
		varName := strings.TrimSpace(match[2 : len(match)-2]) // Remove {{ and }}
		if value, exists := variables[varName]; exists {
			return fmt.Sprintf("%v", value)
		}
		return match // Keep original if variable not found
	})

	return text
}

// processScope applies variable substitution to scope fields
func (tm *TemplateManager) processScope(scope *PolicyScope, variables map[string]interface{}) error {
	// Process branches
	for i, branch := range scope.Branches {
		if processed, err := tm.processTemplateString(branch, variables); err == nil {
			scope.Branches[i] = processed
		}
	}

	// Process repositories
	for i, repo := range scope.Repositories {
		if processed, err := tm.processTemplateString(repo, variables); err == nil {
			scope.Repositories[i] = processed
		}
	}

	// Process users
	for i, user := range scope.Users {
		if processed, err := tm.processTemplateString(user, variables); err == nil {
			scope.Users[i] = processed
		}
	}

	// Process teams
	for i, team := range scope.Teams {
		if processed, err := tm.processTemplateString(team, variables); err == nil {
			scope.Teams[i] = processed
		}
	}

	return nil
}

// mergeVariables merges template defaults with instance variables
func (tm *TemplateManager) mergeVariables(defaults, instance map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})

	// Start with defaults
	for key, value := range defaults {
		result[key] = value
	}

	// Override with instance values
	for key, value := range instance {
		result[key] = value
	}

	return result
}

// validateTemplate validates a template definition
func (tm *TemplateManager) validateTemplate(template *PolicyTemplate) error {
	if template.Name == "" {
		return fmt.Errorf("template name is required")
	}

	if template.Type == "" {
		return fmt.Errorf("template type is required")
	}

	if len(template.Rules) == 0 {
		return fmt.Errorf("template must have at least one rule")
	}

	// Validate that template variables are used in the rules
	usedVars := tm.findUsedVariables(template)
	for varName := range template.Variables {
		if !usedVars[varName] {
			tm.logger.Warn("Template variable not used",
				zap.String("template", template.Name),
				zap.String("variable", varName))
		}
	}

	return nil
}

// findUsedVariables finds which template variables are actually used
func (tm *TemplateManager) findUsedVariables(template *PolicyTemplate) map[string]bool {
	used := make(map[string]bool)

	// Check description
	tm.markVariablesInString(template.Description, used)

	// Check rules
	for _, rule := range template.Rules {
		// Check rule name for variables
		tm.markVariablesInString(rule.Name, used)

		for _, pattern := range rule.Patterns {
			tm.markVariablesInString(pattern, used)
		}

		for _, value := range rule.Metadata {
			if strValue, ok := value.(string); ok {
				tm.markVariablesInString(strValue, used)
			}
		}

		// Check rule scope
		for _, branch := range rule.Scope.Branches {
			tm.markVariablesInString(branch, used)
		}
		for _, repo := range rule.Scope.Repositories {
			tm.markVariablesInString(repo, used)
		}
		for _, user := range rule.Scope.Users {
			tm.markVariablesInString(user, used)
		}
		for _, team := range rule.Scope.Teams {
			tm.markVariablesInString(team, used)
		}
	}

	return used
}

// markVariablesInString marks variables found in a string
func (tm *TemplateManager) markVariablesInString(text string, used map[string]bool) {
	// Find ${variable} patterns
	dollarPattern := regexp.MustCompile(`\$\{([^}]+)\}`)
	matches := dollarPattern.FindAllStringSubmatch(text, -1)
	for _, match := range matches {
		if len(match) > 1 {
			used[strings.TrimSpace(match[1])] = true
		}
	}

	// Find {{variable}} patterns
	bracePattern := regexp.MustCompile(`\{\{([^}]+)\}\}`)
	matches = bracePattern.FindAllStringSubmatch(text, -1)
	for _, match := range matches {
		if len(match) > 1 {
			used[strings.TrimSpace(match[1])] = true
		}
	}
}

// GetTemplate returns a template by name
func (tm *TemplateManager) GetTemplate(name string) (*PolicyTemplate, bool) {
	template, exists := tm.templates[name]
	return template, exists
}

// ListTemplates returns all available templates
func (tm *TemplateManager) ListTemplates() []string {
	names := make([]string, 0, len(tm.templates))
	for name := range tm.templates {
		names = append(names, name)
	}
	return names
}

// CreateCompositePolicy creates a policy by combining multiple templates
func (tm *TemplateManager) CreateCompositePolicy(name string, templateSpecs []TemplateSpec) (*PolicyDefinition, error) {
	if len(templateSpecs) == 0 {
		return nil, fmt.Errorf("at least one template specification is required")
	}

	// Use the first template as the base
	baseSpec := templateSpecs[0]
	policy, err := tm.InstantiateTemplate(baseSpec.TemplateName, name, baseSpec.Variables)
	if err != nil {
		return nil, fmt.Errorf("failed to instantiate base template %s: %w", baseSpec.TemplateName, err)
	}

	// Merge additional templates
	for i, spec := range templateSpecs[1:] {
		additionalPolicy, err := tm.InstantiateTemplate(spec.TemplateName, fmt.Sprintf("%s-part-%d", name, i+2), spec.Variables)
		if err != nil {
			return nil, fmt.Errorf("failed to instantiate template %s: %w", spec.TemplateName, err)
		}

		// Merge rules (simple concatenation for now)
		policy.Rules = append(policy.Rules, additionalPolicy.Rules...)

		// Merge scopes (union of all scopes)
		policy.Scope = tm.mergeScopes(policy.Scope, additionalPolicy.Scope)
	}

	tm.logger.Info("Composite policy created",
		zap.String("name", name),
		zap.Int("templates", len(templateSpecs)),
		zap.Int("total_rules", len(policy.Rules)))

	return policy, nil
}

// Note: TemplateSpec is defined in interfaces.go

// mergeScopes merges two policy scopes
func (tm *TemplateManager) mergeScopes(scope1, scope2 PolicyScope) PolicyScope {
	result := PolicyScope{
		Branches:     append(scope1.Branches, scope2.Branches...),
		Repositories: append(scope1.Repositories, scope2.Repositories...),
		Users:        append(scope1.Users, scope2.Users...),
		Teams:        append(scope1.Teams, scope2.Teams...),
	}

	// Remove duplicates
	result.Branches = tm.removeDuplicates(result.Branches)
	result.Repositories = tm.removeDuplicates(result.Repositories)
	result.Users = tm.removeDuplicates(result.Users)
	result.Teams = tm.removeDuplicates(result.Teams)

	return result
}

// removeDuplicates removes duplicate strings from a slice
func (tm *TemplateManager) removeDuplicates(slice []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(slice))

	for _, item := range slice {
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}

	return result
}
