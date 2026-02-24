package merge

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"go.uber.org/zap"
)

// EnvironmentFileValidator validates environment-specific changes
type EnvironmentFileValidator struct {
	envSystem *EnvironmentProtectionSystem
	logger    *zap.Logger
}

func (efv *EnvironmentFileValidator) Name() string {
	return "environment_file_validator"
}

func (efv *EnvironmentFileValidator) Priority() int {
	return 1 // Highest priority
}

func (efv *EnvironmentFileValidator) Validate(ctx context.Context, change *ValidationFileChange) *ValidationResult {
	if !IsEnvironmentFile(change.Path) {
		return nil // Not an environment file, skip validation
	}

	classification := efv.envSystem.ClassifyEnvironmentFile(change.Path)

	result := efv.envSystem.ValidateEnvironmentChange(
		classification,
		string(change.ChangeType),
		change.SourceContent,
		change.TargetContent,
	)

	// Add environment-specific metadata
	if result.Metadata == nil {
		result.Metadata = make(map[string]interface{})
	}
	result.Metadata["environment_type"] = classification.Type
	result.Metadata["protection_level"] = classification.ProtectionLevel
	result.Metadata["is_sensitive"] = classification.IsSensitive

	efv.logger.Debug("Environment file validated",
		zap.String("path", change.Path),
		zap.String("type", string(classification.Type)),
		zap.Bool("valid", result.Valid))

	return result
}

// SyntaxValidator validates file syntax and format
type SyntaxValidator struct {
	logger *zap.Logger
}

func (sv *SyntaxValidator) Name() string {
	return "syntax_validator"
}

func (sv *SyntaxValidator) Priority() int {
	return 2
}

func (sv *SyntaxValidator) Validate(ctx context.Context, change *ValidationFileChange) *ValidationResult {
	result := &ValidationResult{
		Valid:    true,
		Warnings: []string{},
		Errors:   []string{},
		Metadata: make(map[string]interface{}),
	}

	// Only validate files with content (not deletions)
	if change.ChangeType == ChangeTypeDeleted {
		return result
	}

	// Validate source content if it exists
	if len(change.SourceContent) > 0 {
		if err := sv.validateFileSyntax(change.Path, change.SourceContent); err != nil {
			result.Valid = false
			result.Errors = append(result.Errors,
				fmt.Sprintf("Source content syntax error: %v", err))
		}
	}

	// Check for common syntax issues
	if warnings := sv.checkCommonIssues(change.Path, change.SourceContent); len(warnings) > 0 {
		result.Warnings = append(result.Warnings, warnings...)
	}

	sv.logger.Debug("Syntax validated",
		zap.String("path", change.Path),
		zap.Bool("valid", result.Valid))

	return result
}

func (sv *SyntaxValidator) validateFileSyntax(path string, content []byte) error {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))

	switch ext {
	case "env":
		return sv.validateEnvFile(content)
	case "yml", "yaml":
		return sv.validateYAMLFile(content)
	case "json":
		return sv.validateJSONFile(content)
	case "xml":
		return sv.validateXMLFile(content)
	case "ini":
		return sv.validateINIFile(content)
	default:
		return sv.validateTextFile(content)
	}
}

func (sv *SyntaxValidator) validateEnvFile(content []byte) error {
	lines := strings.Split(string(content), "\n")

	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if !strings.Contains(line, "=") {
			return fmt.Errorf("line %d: missing '=' in environment variable definition", i+1)
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("line %d: invalid variable definition", i+1)
		}

		varName := strings.TrimSpace(parts[0])
		if varName == "" {
			return fmt.Errorf("line %d: empty variable name", i+1)
		}

		// Check variable name format (basic validation)
		if !isValidEnvVarName(varName) {
			return fmt.Errorf("line %d: invalid variable name '%s'", i+1, varName)
		}
	}

	return nil
}

func (sv *SyntaxValidator) validateYAMLFile(content []byte) error {
	str := string(content)
	lines := strings.Split(str, "\n")

	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}

		// Check for tabs (YAML doesn't allow tabs for indentation)
		if strings.Contains(line, "\t") {
			return fmt.Errorf("line %d: YAML files cannot use tabs for indentation", i+1)
		}

		// Basic structure validation
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "-") || strings.Contains(trimmed, ":") {
			// Valid YAML structure indicators
			continue
		}
	}

	return nil
}

func (sv *SyntaxValidator) validateJSONFile(content []byte) error {
	str := strings.TrimSpace(string(content))

	if len(str) == 0 {
		return fmt.Errorf("empty JSON file")
	}

	if (!strings.HasPrefix(str, "{") || !strings.HasSuffix(str, "}")) &&
		(!strings.HasPrefix(str, "[") || !strings.HasSuffix(str, "]")) {
		return fmt.Errorf("JSON must start and end with {} or []")
	}

	// Check for balanced brackets (simplified validation)
	if err := sv.checkBalancedBrackets(str); err != nil {
		return fmt.Errorf("JSON structure error: %v", err)
	}

	return nil
}

func (sv *SyntaxValidator) validateXMLFile(content []byte) error {
	str := strings.TrimSpace(string(content))

	if !strings.HasPrefix(str, "<") || !strings.HasSuffix(str, ">") {
		return fmt.Errorf("invalid XML structure")
	}

	return nil
}

func (sv *SyntaxValidator) validateINIFile(content []byte) error {
	lines := strings.Split(string(content), "\n")

	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		// Section headers
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			continue
		}

		// Key-value pairs
		if strings.Contains(line, "=") {
			continue
		}

		return fmt.Errorf("line %d: invalid INI syntax", i+1)
	}

	return nil
}

func (sv *SyntaxValidator) validateTextFile(content []byte) error {
	// Basic text file validation
	if len(content) == 0 {
		return nil // Empty files are valid
	}

	// Check for null bytes
	if strings.Contains(string(content), "\x00") {
		return fmt.Errorf("file contains null bytes")
	}

	return nil
}

func (sv *SyntaxValidator) checkCommonIssues(path string, content []byte) []string {
	warnings := []string{}
	str := string(content)

	// Check for trailing whitespace
	lines := strings.Split(str, "\n")
	for i, line := range lines {
		if len(line) > 0 && (line[len(line)-1] == ' ' || line[len(line)-1] == '\t') {
			warnings = append(warnings, fmt.Sprintf("line %d: trailing whitespace", i+1))
			break // Only report once
		}
	}

	// Check for very long lines
	for i, line := range lines {
		if len(line) > 200 {
			warnings = append(warnings, fmt.Sprintf("line %d: very long line (%d characters)", i+1, len(line)))
		}
	}

	// Check for mixed line endings
	if strings.Contains(str, "\r\n") && strings.Contains(str, "\n") {
		warnings = append(warnings, "mixed line endings detected")
	}

	return warnings
}

func (sv *SyntaxValidator) checkBalancedBrackets(str string) error {
	stack := []rune{}
	pairs := map[rune]rune{
		')': '(',
		']': '[',
		'}': '{',
	}

	for _, char := range str {
		switch char {
		case '(', '[', '{':
			stack = append(stack, char)
		case ')', ']', '}':
			if len(stack) == 0 {
				return fmt.Errorf("unmatched closing bracket: %c", char)
			}
			if stack[len(stack)-1] != pairs[char] {
				return fmt.Errorf("mismatched brackets")
			}
			stack = stack[:len(stack)-1]
		}
	}

	if len(stack) > 0 {
		return fmt.Errorf("unclosed brackets")
	}

	return nil
}

func isValidEnvVarName(name string) bool {
	if len(name) == 0 {
		return false
	}

	// Must start with letter or underscore
	first := name[0]
	if !((first >= 'A' && first <= 'Z') || (first >= 'a' && first <= 'z') || first == '_') {
		return false
	}

	// Rest must be letters, digits, or underscores
	for _, char := range name[1:] {
		if !((char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') ||
			(char >= '0' && char <= '9') || char == '_') {
			return false
		}
	}

	return true
}

// SecurityValidator checks for security-related issues
type SecurityValidator struct {
	logger *zap.Logger
}

func (sv *SecurityValidator) Name() string {
	return "security_validator"
}

func (sv *SecurityValidator) Priority() int {
	return 3
}

func (sv *SecurityValidator) Validate(ctx context.Context, change *ValidationFileChange) *ValidationResult {
	result := &ValidationResult{
		Valid:    true,
		Warnings: []string{},
		Errors:   []string{},
		Metadata: make(map[string]interface{}),
	}

	if change.ChangeType == ChangeTypeDeleted {
		return result
	}

	// Check for security issues in source content
	if len(change.SourceContent) > 0 {
		if issues := sv.detectSecurityIssues(change.Path, change.SourceContent); len(issues) > 0 {
			result.Warnings = append(result.Warnings, issues...)
		}

		if secrets := sv.detectHardcodedSecrets(change.SourceContent); len(secrets) > 0 {
			result.Errors = append(result.Errors, secrets...)
			result.Valid = false
		}
	}

	sv.logger.Debug("Security validated",
		zap.String("path", change.Path),
		zap.Bool("valid", result.Valid),
		zap.Int("warnings", len(result.Warnings)))

	return result
}

func (sv *SecurityValidator) detectSecurityIssues(path string, content []byte) []string {
	warnings := []string{}
	str := strings.ToLower(string(content))

	// Check for potential security issues
	securityPatterns := map[string]string{
		"debug=true":          "Debug mode enabled in configuration",
		"ssl_verify=false":    "SSL verification disabled",
		"verify_ssl=false":    "SSL verification disabled",
		"disable_ssl=true":    "SSL disabled",
		"insecure=true":       "Insecure mode enabled",
		"allow_insecure=true": "Insecure connections allowed",
		"cors_origin=*":       "CORS allows all origins (potential security risk)",
	}

	for pattern, message := range securityPatterns {
		if strings.Contains(str, pattern) {
			warnings = append(warnings, message)
		}
	}

	return warnings
}

func (sv *SecurityValidator) detectHardcodedSecrets(content []byte) []string {
	errors := []string{}
	str := string(content)

	// Patterns for common hardcoded secrets
	secretPatterns := map[string]string{
		`password\s*=\s*["']?[^"'\s]{8,}`:     "Potential hardcoded password detected",
		`secret\s*=\s*["']?[^"'\s]{16,}`:      "Potential hardcoded secret detected",
		`api_key\s*=\s*["']?[^"'\s]{20,}`:     "Potential hardcoded API key detected",
		`private_key\s*=\s*["']?[^"'\s]{32,}`: "Potential hardcoded private key detected",
	}

	for pattern, message := range secretPatterns {
		if matched, _ := regexp.MatchString(pattern, str); matched {
			errors = append(errors, message)
		}
	}

	return errors
}

// PolicyValidator enforces organizational policies
type PolicyValidator struct {
	logger *zap.Logger
}

func (pv *PolicyValidator) Name() string {
	return "policy_validator"
}

func (pv *PolicyValidator) Priority() int {
	return 4
}

func (pv *PolicyValidator) Validate(ctx context.Context, change *ValidationFileChange) *ValidationResult {
	result := &ValidationResult{
		Valid:    true,
		Warnings: []string{},
		Errors:   []string{},
		Metadata: make(map[string]interface{}),
	}

	// Check file naming conventions
	if violations := pv.checkNamingConventions(change.Path); len(violations) > 0 {
		result.Warnings = append(result.Warnings, violations...)
	}

	// Check for prohibited file types
	if pv.isProhibitedFile(change.Path) {
		result.Valid = false
		result.Errors = append(result.Errors,
			fmt.Sprintf("File type prohibited by policy: %s", change.Path))
	}

	// Check file size limits
	if len(change.SourceContent) > 1024*1024 { // 1MB limit
		result.Valid = false
		result.Errors = append(result.Errors,
			fmt.Sprintf("File exceeds size limit: %s (%d bytes)",
				change.Path, len(change.SourceContent)))
	}

	pv.logger.Debug("Policy validated",
		zap.String("path", change.Path),
		zap.Bool("valid", result.Valid))

	return result
}

func (pv *PolicyValidator) checkNamingConventions(path string) []string {
	warnings := []string{}

	// Check for spaces in file names
	if strings.Contains(filepath.Base(path), " ") {
		warnings = append(warnings, "File name contains spaces - consider using underscores or hyphens")
	}

	// Check for uppercase in environment files
	if IsEnvironmentFile(path) && strings.Contains(path, strings.ToUpper(path)) {
		warnings = append(warnings, "Environment file contains uppercase characters - consider lowercase naming")
	}

	return warnings
}

func (pv *PolicyValidator) isProhibitedFile(path string) bool {
	prohibitedExtensions := []string{
		".exe", ".bat", ".cmd", ".sh", ".ps1",
		".dll", ".so", ".dylib",
	}

	ext := strings.ToLower(filepath.Ext(path))
	for _, prohibited := range prohibitedExtensions {
		if ext == prohibited {
			return true
		}
	}

	return false
}
