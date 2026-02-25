package policy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// FileBasedProvider loads policies from YAML files
type FileBasedProvider struct {
	configPath    string
	lastModified  time.Time
	watchInterval time.Duration
}

// NewFileBasedProvider creates a new file-based policy provider
func NewFileBasedProvider(configPath string) *FileBasedProvider {
	return &FileBasedProvider{
		configPath:    configPath,
		watchInterval: 30 * time.Second, // Check for changes every 30 seconds
	}
}

// LoadPolicies loads policies from YAML files
func (f *FileBasedProvider) LoadPolicies(ctx context.Context) (*PolicyConfiguration, error) {
	// Check if config path is a file or directory
	info, err := os.Stat(f.configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to access config path %s: %w", f.configPath, err)
	}

	var config *PolicyConfiguration
	if info.IsDir() {
		config, err = f.loadFromDirectory(ctx, f.configPath)
	} else {
		config, err = f.loadFromFile(ctx, f.configPath)
	}

	if err != nil {
		return nil, err
	}

	// Update last modified time
	f.lastModified = info.ModTime()

	// Validate configuration
	if err := f.validateConfiguration(config); err != nil {
		return nil, fmt.Errorf("policy configuration validation failed: %w", err)
	}

	return config, nil
}

// loadFromFile loads policies from a single YAML file
func (f *FileBasedProvider) loadFromFile(ctx context.Context, filePath string) (*PolicyConfiguration, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read policy file %s: %w", filePath, err)
	}

	var config PolicyConfiguration
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse YAML from %s: %w", filePath, err)
	}

	return &config, nil
}

// loadFromDirectory loads and merges policies from all YAML files in a directory
func (f *FileBasedProvider) loadFromDirectory(ctx context.Context, dirPath string) (*PolicyConfiguration, error) {
	files, err := f.findYAMLFiles(dirPath)
	if err != nil {
		return nil, err
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("no YAML policy files found in directory %s", dirPath)
	}

	// Start with empty configuration
	merged := &PolicyConfiguration{
		Version:   "1.0",
		Policies:  make([]PolicyDefinition, 0),
		Templates: make([]PolicyTemplate, 0),
		Overrides: make([]PolicyOverride, 0),
	}

	// Load and merge all files
	for _, file := range files {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		config, err := f.loadFromFile(ctx, file)
		if err != nil {
			return nil, fmt.Errorf("failed to load policy file %s: %w", file, err)
		}

		if err := f.mergeConfiguration(merged, config); err != nil {
			return nil, fmt.Errorf("failed to merge configuration from %s: %w", file, err)
		}
	}

	return merged, nil
}

// findYAMLFiles recursively finds all YAML files in a directory
func (f *FileBasedProvider) findYAMLFiles(dirPath string) ([]string, error) {
	var files []string

	err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".yml" || ext == ".yaml" {
			files = append(files, path)
		}

		return nil
	})

	return files, err
}

// mergeConfiguration merges one configuration into another
func (f *FileBasedProvider) mergeConfiguration(target, source *PolicyConfiguration) error {
	// Update metadata if source has newer version
	if source.Metadata.Modified.After(target.Metadata.Modified) {
		target.Metadata = source.Metadata
	}

	// Merge policies (check for duplicates)
	policyNames := make(map[string]bool)
	for _, policy := range target.Policies {
		policyNames[policy.Name] = true
	}

	for _, policy := range source.Policies {
		if policyNames[policy.Name] {
			return fmt.Errorf("duplicate policy name: %s", policy.Name)
		}
		target.Policies = append(target.Policies, policy)
		policyNames[policy.Name] = true
	}

	// Merge templates (check for duplicates)
	templateNames := make(map[string]bool)
	for _, template := range target.Templates {
		templateNames[template.Name] = true
	}

	for _, template := range source.Templates {
		if templateNames[template.Name] {
			return fmt.Errorf("duplicate template name: %s", template.Name)
		}
		target.Templates = append(target.Templates, template)
		templateNames[template.Name] = true
	}

	// Merge overrides
	target.Overrides = append(target.Overrides, source.Overrides...)

	return nil
}

// validateConfiguration validates the loaded configuration
func (f *FileBasedProvider) validateConfiguration(config *PolicyConfiguration) error {
	if config.Version == "" {
		return fmt.Errorf("version is required")
	}

	// Validate policies
	policyNames := make(map[string]bool)
	for i, policy := range config.Policies {
		if policy.Name == "" {
			return fmt.Errorf("policy %d: name is required", i)
		}

		if policyNames[policy.Name] {
			return fmt.Errorf("duplicate policy name: %s", policy.Name)
		}
		policyNames[policy.Name] = true

		if err := f.validatePolicy(&policy); err != nil {
			return fmt.Errorf("policy %s: %w", policy.Name, err)
		}
	}

	// Validate templates
	templateNames := make(map[string]bool)
	for i, template := range config.Templates {
		if template.Name == "" {
			return fmt.Errorf("template %d: name is required", i)
		}

		if templateNames[template.Name] {
			return fmt.Errorf("duplicate template name: %s", template.Name)
		}
		templateNames[template.Name] = true
	}

	// Validate overrides reference existing policies
	for i, override := range config.Overrides {
		if override.PolicyName == "" {
			return fmt.Errorf("override %d: policy_name is required", i)
		}

		if !policyNames[override.PolicyName] {
			return fmt.Errorf("override %d: references unknown policy: %s", i, override.PolicyName)
		}
	}

	return nil
}

// validatePolicy validates a single policy definition
func (f *FileBasedProvider) validatePolicy(policy *PolicyDefinition) error {
	if policy.Type == "" {
		return fmt.Errorf("type is required")
	}

	validEnforcements := map[string]bool{
		"strict":   true,
		"warn":     true,
		"disabled": true,
	}

	if policy.Enforcement != "" && !validEnforcements[policy.Enforcement] {
		return fmt.Errorf("invalid enforcement level: %s", policy.Enforcement)
	}

	// Validate rules
	if len(policy.Rules) == 0 {
		return fmt.Errorf("at least one rule is required")
	}

	ruleNames := make(map[string]bool)
	for i, rule := range policy.Rules {
		if rule.Name == "" {
			return fmt.Errorf("rule %d: name is required", i)
		}

		if ruleNames[rule.Name] {
			return fmt.Errorf("duplicate rule name: %s", rule.Name)
		}
		ruleNames[rule.Name] = true

		if err := f.validateRule(&rule); err != nil {
			return fmt.Errorf("rule %s: %w", rule.Name, err)
		}
	}

	return nil
}

// validateRule validates a single rule
func (f *FileBasedProvider) validateRule(rule *PolicyRule) error {
	if rule.Type == "" {
		return fmt.Errorf("type is required")
	}

	validActions := map[string]bool{
		"block_deletion":   true,
		"require_approval": true,
		"warn":             true,
		"allow":            true,
	}

	if rule.Action != "" && !validActions[rule.Action] {
		return fmt.Errorf("invalid action: %s", rule.Action)
	}

	if rule.Type == "file_protection" && len(rule.Patterns) == 0 {
		return fmt.Errorf("file_protection rules require at least one pattern")
	}

	return nil
}

// Watch watches for changes in policy files
func (f *FileBasedProvider) Watch(ctx context.Context) (<-chan PolicyChangeEvent, error) {
	ch := make(chan PolicyChangeEvent, 1)

	go func() {
		defer close(ch)
		ticker := time.NewTicker(f.watchInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if changed, err := f.hasChanged(); err != nil {
					ch <- PolicyChangeEvent{
						Type:      "error",
						Timestamp: time.Now(),
						Source:    f.configPath,
						Error:     err,
					}
				} else if changed {
					ch <- PolicyChangeEvent{
						Type:      "reload",
						Timestamp: time.Now(),
						Source:    f.configPath,
					}
				}
			}
		}
	}()

	return ch, nil
}

// hasChanged checks if the configuration file has been modified
func (f *FileBasedProvider) hasChanged() (bool, error) {
	info, err := os.Stat(f.configPath)
	if err != nil {
		return false, err
	}

	return info.ModTime().After(f.lastModified), nil
}

// GetSource returns the configuration source description
func (f *FileBasedProvider) GetSource() string {
	return fmt.Sprintf("file://%s", f.configPath)
}
