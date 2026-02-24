package merge

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"go.uber.org/zap"
)

// EnvironmentType categorizes different types of environment files
type EnvironmentType string

const (
	EnvTypeProduction  EnvironmentType = "production"
	EnvTypeStaging     EnvironmentType = "staging"
	EnvTypeDevelopment EnvironmentType = "development"
	EnvTypeTest        EnvironmentType = "test"
	EnvTypeGeneral     EnvironmentType = "general"
	EnvTypeSecret      EnvironmentType = "secret"
	EnvTypeConfig      EnvironmentType = "config"
)

// ProtectionLevel defines how strictly an environment file should be protected
type ProtectionLevel string

const (
	ProtectionImmutable ProtectionLevel = "immutable" // Never auto-merge, requires manual approval
	ProtectionValidated ProtectionLevel = "validated" // Requires validation + approval
	ProtectionMonitored ProtectionLevel = "monitored" // Auto-merge with monitoring
	ProtectionFlexible  ProtectionLevel = "flexible"  // Standard merge handling
)

// EnvironmentClassification contains detailed information about an environment file
type EnvironmentClassification struct {
	Path            string            `json:"path"`
	Type            EnvironmentType   `json:"type"`
	ProtectionLevel ProtectionLevel   `json:"protection_level"`
	IsSensitive     bool              `json:"is_sensitive"`
	RequiresBackup  bool              `json:"requires_backup"`
	Metadata        map[string]string `json:"metadata"`
}

// EnvironmentProtectionSystem manages environment file safety
type EnvironmentProtectionSystem struct {
	logger            *zap.Logger
	protectionRules   map[EnvironmentType]ProtectionLevel
	sensitivePatterns []*regexp.Regexp
	backupRequired    []string
}

// NewEnvironmentProtectionSystem creates a new environment protection system
func NewEnvironmentProtectionSystem(logger *zap.Logger) *EnvironmentProtectionSystem {
	eps := &EnvironmentProtectionSystem{
		logger: logger,
		protectionRules: map[EnvironmentType]ProtectionLevel{
			EnvTypeProduction:  ProtectionImmutable, // Production = highest protection
			EnvTypeStaging:     ProtectionValidated, // Staging = validation required
			EnvTypeDevelopment: ProtectionMonitored, // Development = monitored
			EnvTypeTest:        ProtectionFlexible,  // Test = flexible
			EnvTypeGeneral:     ProtectionMonitored, // General = monitored
			EnvTypeSecret:      ProtectionImmutable, // Secrets = immutable
			EnvTypeConfig:      ProtectionValidated, // Config = validated
		},
		backupRequired: []string{
			"production", "staging", "secret", "database", "api",
		},
	}

	// Compile sensitive patterns
	sensitivePatterns := []string{
		`(?i)(password|secret|key|token|credential)`,
		`(?i)(api_key|auth_token|private_key)`,
		`(?i)(database.*url|db.*pass|connection.*string)`,
		`(?i)(ssl.*cert|tls.*key|encryption)`,
	}

	for _, pattern := range sensitivePatterns {
		if compiled, err := regexp.Compile(pattern); err == nil {
			eps.sensitivePatterns = append(eps.sensitivePatterns, compiled)
		}
	}

	return eps
}

// ClassifyEnvironmentFile analyzes and classifies an environment file
func (eps *EnvironmentProtectionSystem) ClassifyEnvironmentFile(path string) *EnvironmentClassification {
	classification := &EnvironmentClassification{
		Path:           path,
		Type:           eps.determineEnvironmentType(path),
		IsSensitive:    eps.isSensitiveFile(path),
		RequiresBackup: eps.requiresBackup(path),
		Metadata:       make(map[string]string),
	}

	// Set protection level based on type
	classification.ProtectionLevel = eps.protectionRules[classification.Type]

	// Enhance protection for sensitive files
	if classification.IsSensitive {
		if classification.ProtectionLevel == ProtectionFlexible {
			classification.ProtectionLevel = ProtectionMonitored
		} else if classification.ProtectionLevel == ProtectionMonitored {
			classification.ProtectionLevel = ProtectionValidated
		}
	}

	// Add metadata
	classification.Metadata["directory"] = filepath.Dir(path)
	classification.Metadata["filename"] = filepath.Base(path)
	classification.Metadata["extension"] = filepath.Ext(path)

	eps.logger.Debug("Environment file classified",
		zap.String("path", path),
		zap.String("type", string(classification.Type)),
		zap.String("protection_level", string(classification.ProtectionLevel)),
		zap.Bool("sensitive", classification.IsSensitive))

	return classification
}

// determineEnvironmentType analyzes the file path to determine environment type
func (eps *EnvironmentProtectionSystem) determineEnvironmentType(path string) EnvironmentType {
	lowerPath := strings.ToLower(path)

	// Production patterns (highest priority)
	productionPatterns := []string{
		"production", "prod", "live", "release", "main",
	}
	for _, pattern := range productionPatterns {
		if strings.Contains(lowerPath, pattern) {
			return EnvTypeProduction
		}
	}

	// Secret patterns (high priority)
	secretPatterns := []string{
		"secret", "credential", "auth", "key", "token", "cert",
	}
	for _, pattern := range secretPatterns {
		if strings.Contains(lowerPath, pattern) {
			return EnvTypeSecret
		}
	}

	// Staging patterns
	stagingPatterns := []string{
		"staging", "stage", "stg", "pre-prod", "preprod",
	}
	for _, pattern := range stagingPatterns {
		if strings.Contains(lowerPath, pattern) {
			return EnvTypeStaging
		}
	}

	// Test patterns
	testPatterns := []string{
		"test", "testing", "spec", "qa", "integration",
	}
	for _, pattern := range testPatterns {
		if strings.Contains(lowerPath, pattern) {
			return EnvTypeTest
		}
	}

	// Development patterns
	devPatterns := []string{
		"dev", "development", "local", "debug",
	}
	for _, pattern := range devPatterns {
		if strings.Contains(lowerPath, pattern) {
			return EnvTypeDevelopment
		}
	}

	// Config patterns
	configPatterns := []string{
		"config", "configuration", "settings", "options",
	}
	for _, pattern := range configPatterns {
		if strings.Contains(lowerPath, pattern) {
			return EnvTypeConfig
		}
	}

	// Default to general
	return EnvTypeGeneral
}

// isSensitiveFile checks if a file contains sensitive information patterns
func (eps *EnvironmentProtectionSystem) isSensitiveFile(path string) bool {
	lowerPath := strings.ToLower(path)

	// Check against compiled patterns
	for _, pattern := range eps.sensitivePatterns {
		if pattern.MatchString(lowerPath) {
			return true
		}
	}

	// Additional sensitive file indicators
	sensitiveIndicators := []string{
		"password", "secret", "private", "credential",
		"token", "key", "auth", "ssl", "tls", "cert",
		"database", "db", "connection", "url",
	}

	for _, indicator := range sensitiveIndicators {
		if strings.Contains(lowerPath, indicator) {
			return true
		}
	}

	return false
}

// requiresBackup checks if a file should be backed up before changes
func (eps *EnvironmentProtectionSystem) requiresBackup(path string) bool {
	lowerPath := strings.ToLower(path)

	for _, pattern := range eps.backupRequired {
		if strings.Contains(lowerPath, pattern) {
			return true
		}
	}

	return false
}

// ValidateEnvironmentChange validates a proposed environment file change
func (eps *EnvironmentProtectionSystem) ValidateEnvironmentChange(
	classification *EnvironmentClassification,
	changeType string,
	sourceContent, targetContent []byte,
) *ValidationResult {
	result := &ValidationResult{
		Valid:    true,
		Warnings: []string{},
		Errors:   []string{},
		Metadata: make(map[string]interface{}),
	}

	// Check protection level requirements
	switch classification.ProtectionLevel {
	case ProtectionImmutable:
		result.Valid = false
		result.Errors = append(result.Errors,
			fmt.Sprintf("File %s is immutable and cannot be modified automatically", classification.Path))

	case ProtectionValidated:
		if err := eps.validateSyntax(classification.Path, sourceContent); err != nil {
			result.Valid = false
			result.Errors = append(result.Errors, fmt.Sprintf("Syntax validation failed: %v", err))
		}

		if classification.IsSensitive {
			if warnings := eps.validateSensitiveContent(sourceContent, targetContent); len(warnings) > 0 {
				result.Warnings = append(result.Warnings, warnings...)
			}
		}

	case ProtectionMonitored:
		if warnings := eps.generateMonitoringWarnings(classification, changeType); len(warnings) > 0 {
			result.Warnings = append(result.Warnings, warnings...)
		}
	}

	// Add backup requirement
	if classification.RequiresBackup {
		result.Metadata["backup_required"] = true
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("Backup recommended before modifying %s", classification.Path))
	}

	eps.logger.Info("Environment change validated",
		zap.String("path", classification.Path),
		zap.Bool("valid", result.Valid),
		zap.Int("warnings", len(result.Warnings)),
		zap.Int("errors", len(result.Errors)))

	return result
}

// ValidationResult contains the result of environment validation
type ValidationResult struct {
	Valid    bool                   `json:"valid"`
	Warnings []string               `json:"warnings"`
	Errors   []string               `json:"errors"`
	Metadata map[string]interface{} `json:"metadata"`
}

// validateSyntax performs basic syntax validation on environment files
func (eps *EnvironmentProtectionSystem) validateSyntax(path string, content []byte) error {
	ext := strings.ToLower(filepath.Ext(path))

	switch ext {
	case ".env":
		return eps.validateDotEnvSyntax(content)
	case ".yml", ".yaml":
		return eps.validateYAMLSyntax(content)
	case ".json":
		return eps.validateJSONSyntax(content)
	default:
		// For other formats, perform basic checks
		return eps.validateBasicSyntax(content)
	}
}

// validateDotEnvSyntax validates .env file syntax
func (eps *EnvironmentProtectionSystem) validateDotEnvSyntax(content []byte) error {
	lines := strings.Split(string(content), "\n")
	envVarPattern := regexp.MustCompile(`^[A-Z_][A-Z0-9_]*=.*$`)

	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue // Skip empty lines and comments
		}

		if !envVarPattern.MatchString(line) {
			return fmt.Errorf("invalid .env syntax at line %d: %s", i+1, line)
		}
	}

	return nil
}

// validateYAMLSyntax validates YAML syntax (basic check)
func (eps *EnvironmentProtectionSystem) validateYAMLSyntax(content []byte) error {
	// Basic YAML validation - check for common issues
	str := string(content)

	// Check for consistent indentation
	lines := strings.Split(str, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}

		// Check for tabs (YAML doesn't allow tabs)
		if strings.Contains(line, "\t") {
			return fmt.Errorf("YAML syntax error at line %d: tabs are not allowed", i+1)
		}
	}

	return nil
}

// validateJSONSyntax validates JSON syntax (basic check)
func (eps *EnvironmentProtectionSystem) validateJSONSyntax(content []byte) error {
	// For now, just check that it's valid JSON structure
	str := strings.TrimSpace(string(content))

	if !strings.HasPrefix(str, "{") || !strings.HasSuffix(str, "}") {
		if !strings.HasPrefix(str, "[") || !strings.HasSuffix(str, "]") {
			return fmt.Errorf("invalid JSON: must start and end with {} or []")
		}
	}

	return nil
}

// validateBasicSyntax performs basic syntax validation
func (eps *EnvironmentProtectionSystem) validateBasicSyntax(content []byte) error {
	// Check for null bytes or other problematic characters
	if strings.Contains(string(content), "\x00") {
		return fmt.Errorf("file contains null bytes")
	}

	return nil
}

// validateSensitiveContent checks for potential security issues in sensitive files
func (eps *EnvironmentProtectionSystem) validateSensitiveContent(sourceContent, targetContent []byte) []string {
	warnings := []string{}

	// Check for hardcoded credentials in source
	if eps.containsHardcodedCredentials(sourceContent) {
		warnings = append(warnings, "Potential hardcoded credentials detected in source content")
	}

	// Check for credential removal
	if eps.containsHardcodedCredentials(targetContent) && !eps.containsHardcodedCredentials(sourceContent) {
		warnings = append(warnings, "Credentials appear to be removed - verify this is intentional")
	}

	// Check for plaintext secrets
	if eps.containsPlaintextSecrets(sourceContent) {
		warnings = append(warnings, "Potential plaintext secrets detected - consider using encrypted values")
	}

	return warnings
}

// containsHardcodedCredentials checks for patterns that look like hardcoded credentials
func (eps *EnvironmentProtectionSystem) containsHardcodedCredentials(content []byte) bool {
	str := strings.ToLower(string(content))

	// Common hardcoded credential patterns
	patterns := []string{
		"password=",
		"pwd=",
		"secret=",
		"token=",
		"key=",
		"auth=",
	}

	for _, pattern := range patterns {
		if strings.Contains(str, pattern) {
			return true
		}
	}

	return false
}

// containsPlaintextSecrets checks for patterns that indicate plaintext secrets
func (eps *EnvironmentProtectionSystem) containsPlaintextSecrets(content []byte) bool {
	str := string(content)

	// Look for patterns that suggest plaintext secrets
	// This is a simplified check - in production, you'd want more sophisticated detection
	secretPatterns := []*regexp.Regexp{
		regexp.MustCompile(`[A-Za-z0-9]{32,}`),    // Long alphanumeric strings (API keys)
		regexp.MustCompile(`sk-[A-Za-z0-9]{40,}`), // OpenAI-style API keys
		regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`), // GitHub personal access tokens
	}

	for _, pattern := range secretPatterns {
		if pattern.FindString(str) != "" {
			return true
		}
	}

	return false
}

// generateMonitoringWarnings generates warnings for monitored files
func (eps *EnvironmentProtectionSystem) generateMonitoringWarnings(
	classification *EnvironmentClassification,
	changeType string,
) []string {
	warnings := []string{}

	if classification.Type == EnvTypeDevelopment && changeType == "delete" {
		warnings = append(warnings, "Deleting development environment file - ensure this is intentional")
	}

	if classification.IsSensitive && changeType == "modify" {
		warnings = append(warnings, "Modifying sensitive configuration file - changes will be logged")
	}

	return warnings
}

// IsEnvironmentFile checks if a file path represents an environment file
func IsEnvironmentFile(path string) bool {
	environmentPatterns := []string{
		"config/env/",
		".env",
		"environments/",
		"config/staging/",
		"config/production/",
		"config/development/",
		"*.env",
	}

	lowerPath := strings.ToLower(path)

	for _, pattern := range environmentPatterns {
		if pattern[len(pattern)-1] == '/' {
			// Directory pattern
			if strings.Contains(lowerPath, strings.ToLower(pattern)) {
				return true
			}
		} else if pattern[0] == '*' {
			// Suffix pattern
			suffix := strings.ToLower(pattern[1:])
			if strings.HasSuffix(lowerPath, suffix) {
				return true
			}
		} else {
			// Exact or contains pattern
			if strings.Contains(lowerPath, strings.ToLower(pattern)) {
				return true
			}
		}
	}

	return false
}
