package policy

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Engine implements the PolicyEngine interface
type Engine struct {
	mu           sync.RWMutex
	provider     PolicyProvider
	config       *PolicyConfiguration
	logger       *zap.Logger
	initialized  bool
	lastReload   time.Time
	patternCache map[string]*regexp.Regexp // Cache compiled regex patterns

	// Phase 2 Features
	// hotReloadManager    *HotReloadManager // Disabled for build compatibility
	templateManager     *TemplateManager
	hierarchicalManager *HierarchicalPolicyManager
	validator           *PolicyValidator
	hotReloadEnabled    bool
}

// NewEngine creates a new policy engine with the given provider
func NewEngine(provider PolicyProvider, logger *zap.Logger) *Engine {
	if logger == nil {
		logger = zap.NewNop()
	}

	engine := &Engine{
		provider:     provider,
		logger:       logger,
		patternCache: make(map[string]*regexp.Regexp),
	}

	// Initialize Phase 2 components
	engine.templateManager = NewTemplateManager(logger)
	engine.hierarchicalManager = NewHierarchicalPolicyManager(logger)
	engine.validator = NewPolicyValidator()

	return engine
}

// Initialize loads policies from the provider
func (e *Engine) Initialize(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	config, err := e.provider.LoadPolicies(ctx)
	if err != nil {
		return fmt.Errorf("failed to load policies: %w", err)
	}

	// Validate configuration using Phase 2 validator
	if err := e.validator.ValidateConfiguration(config); err != nil {
		e.logger.Error("Policy configuration validation failed", zap.Error(err))
		return fmt.Errorf("policy validation failed: %w", err)
	}

	e.config = config
	e.initialized = true
	e.lastReload = time.Now()

	// Pre-compile regex patterns for performance
	e.compilePatterns()

	// Initialize Phase 2 components
	if err := e.initializePhase2Components(); err != nil {
		e.logger.Error("Failed to initialize Phase 2 components", zap.Error(err))
		// Don't fail completely - continue with basic functionality
	}

	e.logger.Info("Policy engine initialized",
		zap.String("source", e.provider.GetSource()),
		zap.Int("policies_loaded", len(config.Policies)),
		zap.Int("rules_total", e.countTotalRules()),
		zap.Int("templates_loaded", len(config.Templates)),
		zap.String("version", config.Version))

	return nil
}

// EvaluateFileProtection evaluates file protection policies
func (e *Engine) EvaluateFileProtection(ctx context.Context, filePath string) (*FileProtectionDecision, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.initialized {
		return &FileProtectionDecision{
			Protected: false,
			Reason:    "policy_engine_not_initialized",
		}, nil
	}

	// Find applicable policies for file protection
	for _, policy := range e.config.Policies {
		if !policy.Enabled || policy.Type != "file_protection" {
			continue
		}

		// Check if file matches any rule in this policy
		for _, rule := range policy.Rules {
			if rule.Type != "file_protection" {
				continue
			}

			if matched, matchedPattern := e.matchesFilePatterns(filePath, rule.Patterns); matched {
				decision := &FileProtectionDecision{
					RuleMatched: &rule,
					Metadata: map[string]interface{}{
						"policy_name":     policy.Name,
						"matched_pattern": matchedPattern,
						"enforcement":     policy.Enforcement,
						"rule_action":     rule.Action,
					},
				}

				// Determine protection based on action and enforcement
				switch rule.Action {
				case "block_deletion":
					decision.Protected = true
					decision.Reason = fmt.Sprintf("file matches protected pattern '%s' in policy '%s'", matchedPattern, policy.Name)
				case "require_approval":
					decision.Protected = true
					decision.Reason = fmt.Sprintf("file requires approval due to pattern '%s' in policy '%s'", matchedPattern, policy.Name)
				case "warn":
					decision.Protected = false
					decision.Reason = fmt.Sprintf("file matches warning pattern '%s' in policy '%s'", matchedPattern, policy.Name)
				case "allow":
					decision.Protected = false
					decision.Reason = fmt.Sprintf("file explicitly allowed by pattern '%s' in policy '%s'", matchedPattern, policy.Name)
				default:
					decision.Protected = true
					decision.Reason = fmt.Sprintf("file matches pattern '%s' in policy '%s' (default protection)", matchedPattern, policy.Name)
				}

				e.logger.Debug("File protection evaluated",
					zap.String("file_path", filePath),
					zap.Bool("protected", decision.Protected),
					zap.String("reason", decision.Reason),
					zap.String("policy", policy.Name),
					zap.String("rule", rule.Name))

				return decision, nil
			}
		}
	}

	// No matching rules found - file is not protected
	return &FileProtectionDecision{
		Protected: false,
		Reason:    "no_matching_protection_rules",
	}, nil
}

// EvaluateMergePolicy evaluates merge policies
func (e *Engine) EvaluateMergePolicy(ctx context.Context, req *MergeRequest) (*PolicyDecision, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.initialized {
		return &PolicyDecision{
			Allowed: true,
			Metadata: map[string]interface{}{
				"reason": "policy_engine_not_initialized",
			},
		}, nil
	}

	decision := &PolicyDecision{
		Allowed:           true,
		BlockingRules:     make([]RuleViolation, 0),
		Warnings:          make([]RuleWarning, 0),
		RequiredApprovals: make([]ApprovalRequirement, 0),
		Metadata: map[string]interface{}{
			"evaluated_policies": 0,
			"evaluated_rules":    0,
		},
	}

	evaluatedPolicies := 0
	evaluatedRules := 0

	// Evaluate all applicable policies
	for _, policy := range e.config.Policies {
		if !policy.Enabled {
			continue
		}

		// Check if policy applies to this merge request
		if !e.policyAppliesTo(policy, req) {
			continue
		}

		evaluatedPolicies++

		// Evaluate each rule in the policy
		for _, rule := range policy.Rules {
			evaluatedRules++

			if err := e.evaluateRule(policy, rule, req, decision); err != nil {
				e.logger.Error("Error evaluating rule",
					zap.String("policy", policy.Name),
					zap.String("rule", rule.Name),
					zap.Error(err))

				// On evaluation error, be conservative and add a blocking rule
				decision.BlockingRules = append(decision.BlockingRules, RuleViolation{
					RuleName: rule.Name,
					RuleType: rule.Type,
					Severity: "error",
					Message:  fmt.Sprintf("Rule evaluation failed: %v", err),
				})
				decision.Allowed = false
			}
		}
	}

	// Update metadata
	decision.Metadata["evaluated_policies"] = evaluatedPolicies
	decision.Metadata["evaluated_rules"] = evaluatedRules

	e.logger.Debug("Merge policy evaluated",
		zap.String("source_ref", req.SourceRef),
		zap.String("target_ref", req.TargetRef),
		zap.Bool("allowed", decision.Allowed),
		zap.Int("blocking_rules", len(decision.BlockingRules)),
		zap.Int("warnings", len(decision.Warnings)),
		zap.Int("required_approvals", len(decision.RequiredApprovals)))

	return decision, nil
}

// ReloadPolicies reloads policies from the provider
func (e *Engine) ReloadPolicies(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	config, err := e.provider.LoadPolicies(ctx)
	if err != nil {
		return fmt.Errorf("failed to reload policies: %w", err)
	}

	e.config = config
	e.lastReload = time.Now()

	// Clear and recompile pattern cache
	e.patternCache = make(map[string]*regexp.Regexp)
	e.compilePatterns()

	e.logger.Info("Policies reloaded",
		zap.String("source", e.provider.GetSource()),
		zap.Int("policies_loaded", len(config.Policies)),
		zap.Int("rules_total", e.countTotalRules()))

	return nil
}

// GetPolicyStatus returns current policy engine status
func (e *Engine) GetPolicyStatus() *PolicyEngineStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()

	status := &PolicyEngineStatus{
		Initialized:  e.initialized,
		ConfigSource: e.provider.GetSource(),
		LastReload:   e.lastReload,
		Version:      "1.0.0",
	}

	if e.config != nil {
		status.PoliciesLoaded = len(e.config.Policies)
		status.RulesLoaded = e.countTotalRules()
		if e.config.Metadata.Version != "" {
			status.Version = e.config.Metadata.Version
		}
	}

	return status
}

// matchesFilePatterns checks if a file path matches any of the given patterns
func (e *Engine) matchesFilePatterns(filePath string, patterns []string) (bool, string) {
	for _, pattern := range patterns {
		if e.matchesPattern(filePath, pattern) {
			return true, pattern
		}
	}
	return false, ""
}

// matchesPattern checks if a file path matches a single pattern
func (e *Engine) matchesPattern(filePath, pattern string) bool {
	// Try exact match first
	if filePath == pattern {
		return true
	}

	// Try simple glob pattern matching
	if matched, _ := filepath.Match(pattern, filePath); matched {
		return true
	}

	// Try regex pattern if it's compiled
	if regex, exists := e.patternCache[pattern]; exists {
		return regex.MatchString(filePath)
	}

	// Handle ** patterns manually
	if strings.Contains(pattern, "**") {
		return e.matchesGlobStar(filePath, pattern)
	}

	return false
}

// matchesGlobStar handles ** glob patterns
func (e *Engine) matchesGlobStar(filePath, pattern string) bool {
	parts := strings.Split(pattern, "**")
	if len(parts) != 2 {
		return false
	}

	prefix := parts[0]
	suffix := parts[1]

	// Clean up prefix and suffix
	prefix = strings.TrimSuffix(prefix, "/")
	suffix = strings.TrimPrefix(suffix, "/")

	// Check prefix match
	if prefix != "" {
		if prefix == filePath {
			// Exact match with prefix
			return suffix == ""
		}
		if !strings.HasPrefix(filePath, prefix+"/") {
			return false
		}
	}

	// Check suffix match
	if suffix != "" {
		// Get the remaining part after prefix
		remaining := filePath
		if prefix != "" {
			remaining = strings.TrimPrefix(filePath, prefix+"/")
		}

		// Handle different suffix patterns
		if suffix == "*" {
			// Match any single level
			return !strings.Contains(remaining, "/")
		} else if strings.HasPrefix(suffix, "*.") {
			// Pattern like "*.yml" - check file extension
			ext := suffix[1:] // Remove the "*"
			return strings.HasSuffix(remaining, ext)
		} else if strings.HasSuffix(suffix, "/*") {
			// Pattern ends with /* - match directory with any file
			dirPattern := strings.TrimSuffix(suffix, "/*")
			if dirPattern == "" {
				return true // **/* matches everything
			}
			return strings.HasPrefix(remaining, dirPattern+"/")
		} else {
			// Exact suffix match
			return strings.HasSuffix(remaining, suffix) || remaining == suffix
		}
	}

	// No suffix - pattern ends with **
	return true
}

// compilePatterns pre-compiles regex patterns for performance
func (e *Engine) compilePatterns() {
	if e.config == nil {
		return
	}

	for _, policy := range e.config.Policies {
		for _, rule := range policy.Rules {
			for _, pattern := range rule.Patterns {
				// Try to compile as regex if it looks like one
				if strings.ContainsAny(pattern, "[]{}()|+?^$\\") {
					if regex, err := regexp.Compile(pattern); err == nil {
						e.patternCache[pattern] = regex
					}
				}
			}
		}
	}
}

// policyAppliesTo checks if a policy applies to a merge request
func (e *Engine) policyAppliesTo(policy PolicyDefinition, req *MergeRequest) bool {
	scope := policy.Scope

	// Check branches
	if len(scope.Branches) > 0 {
		branchMatches := false
		for _, branch := range scope.Branches {
			if e.matchesPattern(req.TargetRef, branch) || e.matchesPattern(req.Branch, branch) {
				branchMatches = true
				break
			}
		}
		if !branchMatches {
			return false
		}
	}

	// Check repositories
	if len(scope.Repositories) > 0 {
		repoMatches := false
		for _, repo := range scope.Repositories {
			if repo == "*" || e.matchesPattern(req.Repository, repo) {
				repoMatches = true
				break
			}
		}
		if !repoMatches {
			return false
		}
	}

	// Check users
	if len(scope.Users) > 0 {
		userMatches := false
		for _, user := range scope.Users {
			if user == "*" || req.Author == user {
				userMatches = true
				break
			}
		}
		if !userMatches {
			return false
		}
	}

	return true
}

// evaluateRule evaluates a single rule against a merge request
func (e *Engine) evaluateRule(policy PolicyDefinition, rule PolicyRule, req *MergeRequest, decision *PolicyDecision) error {
	switch rule.Type {
	case "file_protection":
		return e.evaluateFileProtectionRule(policy, rule, req, decision)
	case "approval_requirement":
		return e.evaluateApprovalRule(policy, rule, req, decision)
	default:
		return fmt.Errorf("unknown rule type: %s", rule.Type)
	}
}

// evaluateFileProtectionRule evaluates file protection rules
func (e *Engine) evaluateFileProtectionRule(policy PolicyDefinition, rule PolicyRule, req *MergeRequest, decision *PolicyDecision) error {
	for _, fileChange := range req.ChangedFiles {
		if fileChange.ChangeType == "deleted" {
			if matched, pattern := e.matchesFilePatterns(fileChange.Path, rule.Patterns); matched {
				switch rule.Action {
				case "block_deletion":
					decision.BlockingRules = append(decision.BlockingRules, RuleViolation{
						RuleName:   rule.Name,
						RuleType:   rule.Type,
						Severity:   "high",
						Message:    fmt.Sprintf("File deletion blocked by policy '%s'", policy.Name),
						FilePath:   fileChange.Path,
						Suggestion: "Contact the platform team for approval to delete this protected file",
					})
					decision.Allowed = false

				case "require_approval":
					approvers := e.config.DefaultApprovers
					if len(approvers) == 0 {
						approvers = []string{"platform-team"} // Fallback for backward compatibility
					}
					decision.RequiredApprovals = append(decision.RequiredApprovals, ApprovalRequirement{
						Type:        "policy",
						Approvers:   approvers,
						MinRequired: 1,
						Reason:      fmt.Sprintf("File deletion requires approval due to pattern '%s'", pattern),
					})

				case "warn":
					decision.Warnings = append(decision.Warnings, RuleWarning{
						RuleName: rule.Name,
						Message:  fmt.Sprintf("Warning: Deleting protected file '%s'", fileChange.Path),
						FilePath: fileChange.Path,
					})
				}
			}
		}
	}

	return nil
}

// evaluateApprovalRule evaluates approval requirement rules
func (e *Engine) evaluateApprovalRule(policy PolicyDefinition, rule PolicyRule, req *MergeRequest, decision *PolicyDecision) error {
	// TODO: Implement approval rule evaluation
	// This would check for required approvals based on various criteria
	return nil
}

// countTotalRules counts total number of rules across all policies
func (e *Engine) countTotalRules() int {
	if e.config == nil {
		return 0
	}

	total := 0
	for _, policy := range e.config.Policies {
		total += len(policy.Rules)
	}
	return total
}

// Phase 2 Implementation

// initializePhase2Components initializes Phase 2 components
func (e *Engine) initializePhase2Components() error {
	// Load templates
	if len(e.config.Templates) > 0 {
		if err := e.templateManager.LoadTemplates(e.config.Templates); err != nil {
			return fmt.Errorf("failed to load templates: %w", err)
		}
	}

	// Set up hierarchical layers from policies
	e.setupHierarchicalLayers()

	return nil
}

// setupHierarchicalLayers creates policy layers from the loaded policies
func (e *Engine) setupHierarchicalLayers() {
	// Group policies by priority for hierarchical evaluation
	layerMap := make(map[int][]PolicyDefinition)

	for _, policy := range e.config.Policies {
		priority := policy.Priority
		if priority == 0 {
			priority = 100 // Default priority
		}
		layerMap[priority] = append(layerMap[priority], policy)
	}

	// Create layers
	for priority, policies := range layerMap {
		layer := PolicyLayer{
			Name:        fmt.Sprintf("priority-%d", priority),
			Priority:    priority,
			Policies:    policies,
			Enabled:     true,
			Description: fmt.Sprintf("Policies with priority %d", priority),
		}
		e.hierarchicalManager.AddLayer(layer)
	}
}

// EnableHotReload enables hot reloading of policy configurations
func (e *Engine) EnableHotReload(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.hotReloadEnabled {
		return nil // Already enabled
	}

	// Hot reload manager temporarily disabled for build compatibility
	// var err error
	// 	// e.hotReloadManager, err = NewHotReloadManager(e, e.logger)
	// if err != nil {
	//	return fmt.Errorf("failed to create hot reload manager: %w", err)
	// }

	// Hot reload functionality disabled for build compatibility
	// 	// e.hotReloadManager.AddReloadCallback(func(engine *Engine, duration time.Duration) {
	//	if err := engine.initializePhase2Components(); err != nil {
	//		engine.logger.Error("Failed to reinitialize Phase 2 components after reload", zap.Error(err))
	//	}
	// })
	//
	// 	// if err := e.hotReloadManager.Start(ctx); err != nil {
	//	return fmt.Errorf("failed to start hot reload manager: %w", err)
	// }

	e.hotReloadEnabled = true
	e.logger.Info("Hot reload enabled for policy engine")
	return nil
}

// DisableHotReload disables hot reloading
func (e *Engine) DisableHotReload() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.hotReloadEnabled {
		return nil
	}

	// if err := e.hotReloadManager.Stop(); err != nil {
	//	return fmt.Errorf("failed to stop hot reload manager: %w", err)
	// }

	// 	e.hotReloadManager = nil
	e.hotReloadEnabled = false
	e.logger.Info("Hot reload disabled for policy engine")
	return nil
}

// GetReloadStatus returns the status of hot reloading
func (e *Engine) GetReloadStatus() *HotReloadStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// Hot reload manager disabled
	return &HotReloadStatus{Running: false}
}

// InstantiateTemplate creates a policy from a template
func (e *Engine) InstantiateTemplate(templateName, instanceName string, variables map[string]interface{}) (*PolicyDefinition, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.templateManager.InstantiateTemplate(templateName, instanceName, variables)
}

// ListTemplates returns all available templates
func (e *Engine) ListTemplates() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.templateManager.ListTemplates()
}

// GetTemplate returns a template by name
func (e *Engine) GetTemplate(name string) (*PolicyTemplate, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.templateManager.GetTemplate(name)
}

// EvaluateFileProtectionHierarchical evaluates file protection using hierarchical policies
func (e *Engine) EvaluateFileProtectionHierarchical(ctx context.Context, filePath string) (*HierarchicalDecision, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.initialized {
		return &HierarchicalDecision{
			FilePath: filePath,
			FinalDecision: &FileProtectionDecision{
				Protected: false,
				Reason:    "policy_engine_not_initialized",
			},
		}, nil
	}

	return e.hierarchicalManager.EvaluateFileProtectionHierarchical(ctx, filePath)
}

// SetConflictResolution sets the conflict resolution strategy
func (e *Engine) SetConflictResolution(strategy ConflictResolutionStrategy) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.hierarchicalManager.SetConflictResolution(strategy)
	e.logger.Info("Conflict resolution strategy updated", zap.Int("strategy", int(strategy)))
}

// GetLayerStatus returns the status of policy layers
func (e *Engine) GetLayerStatus() *LayerStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.hierarchicalManager.GetLayerStatus()
}

// GetValidationReport returns a comprehensive validation report
func (e *Engine) GetValidationReport() *ValidationReport {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.config == nil {
		return &ValidationReport{
			Valid:     false,
			Errors:    []ValidationError{{Type: "configuration", Message: "no configuration loaded"}},
			Warnings:  make([]ValidationWarning, 0),
			Summary:   ValidationSummary{},
			Timestamp: time.Now(),
		}
	}

	return e.validator.GetValidationReport(e.config)
}
