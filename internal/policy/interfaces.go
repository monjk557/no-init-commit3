package policy

import (
	"context"
	"time"
)

// PolicyEngine defines the interface for merge and file protection policies
type PolicyEngine interface {
	// EvaluateFileProtection checks if a file should be protected from deletion
	EvaluateFileProtection(ctx context.Context, filePath string) (*FileProtectionDecision, error)

	// EvaluateMergePolicy evaluates overall merge policy for a merge request
	EvaluateMergePolicy(ctx context.Context, req *MergeRequest) (*PolicyDecision, error)

	// ReloadPolicies reloads policy configuration from source
	ReloadPolicies(ctx context.Context) error

	// GetPolicyStatus returns current policy engine status
	GetPolicyStatus() *PolicyEngineStatus
}

// FileProtectionDecision represents the result of file protection evaluation
type FileProtectionDecision struct {
	Protected   bool                   `json:"protected"`
	Reason      string                 `json:"reason"`
	RuleMatched *PolicyRule            `json:"rule_matched,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// PolicyDecision represents the result of merge policy evaluation
type PolicyDecision struct {
	Allowed           bool                   `json:"allowed"`
	BlockingRules     []RuleViolation        `json:"blocking_rules,omitempty"`
	Warnings          []RuleWarning          `json:"warnings,omitempty"`
	RequiredApprovals []ApprovalRequirement  `json:"required_approvals,omitempty"`
	Metadata          map[string]interface{} `json:"metadata,omitempty"`
}

// RuleViolation represents a policy rule that was violated
type RuleViolation struct {
	RuleName   string `json:"rule_name"`
	RuleType   string `json:"rule_type"`
	Severity   string `json:"severity"`
	Message    string `json:"message"`
	FilePath   string `json:"file_path,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`
}

// RuleWarning represents a policy rule warning
type RuleWarning struct {
	RuleName string `json:"rule_name"`
	Message  string `json:"message"`
	FilePath string `json:"file_path,omitempty"`
}

// ApprovalRequirement represents required approvals for the merge
type ApprovalRequirement struct {
	Type        string   `json:"type"`      // "team", "user", "role"
	Approvers   []string `json:"approvers"` // team names, usernames, or role names
	MinRequired int      `json:"min_required"`
	Reason      string   `json:"reason"`
}

// PolicyRule represents a single policy rule
type PolicyRule struct {
	Name        string                 `json:"name"`
	Type        string                 `json:"type"`
	Patterns    []string               `json:"patterns"`
	Action      string                 `json:"action"`
	Enforcement string                 `json:"enforcement"`
	Scope       PolicyScope            `json:"scope"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// PolicyScope defines the scope where a policy applies
type PolicyScope struct {
	Branches     []string `json:"branches"`
	Repositories []string `json:"repositories"`
	Users        []string `json:"users"`
	Teams        []string `json:"teams"`
	FileTypes    []string `json:"file_types"`
}

// MergeRequest represents a merge request for policy evaluation
type MergeRequest struct {
	SourceRef    string       `json:"source_ref"`
	TargetRef    string       `json:"target_ref"`
	Method       string       `json:"method"` // "squash", "merge", "rebase"
	IsSquash     bool         `json:"is_squash"`
	IsRebase     bool         `json:"is_rebase"`
	ChangedFiles []FileChange `json:"changed_files"`
	Author       string       `json:"author"`
	Repository   string       `json:"repository"`
	Branch       string       `json:"branch"`
}

// FileChange represents a file change in a merge request
type FileChange struct {
	Path       string `json:"path"`
	ChangeType string `json:"change_type"`        // "added", "modified", "deleted"
	OldPath    string `json:"old_path,omitempty"` // for renames
}

// PolicyEngineStatus represents the current status of the policy engine
type PolicyEngineStatus struct {
	Initialized    bool      `json:"initialized"`
	ConfigSource   string    `json:"config_source"`
	LastReload     time.Time `json:"last_reload"`
	PoliciesLoaded int       `json:"policies_loaded"`
	RulesLoaded    int       `json:"rules_loaded"`
	Version        string    `json:"version"`
	Errors         []string  `json:"errors,omitempty"`
}

// PolicyProvider defines interface for loading policies from different sources
type PolicyProvider interface {
	// LoadPolicies loads policies from the source
	LoadPolicies(ctx context.Context) (*PolicyConfiguration, error)

	// Watch watches for policy changes and returns a channel for notifications
	Watch(ctx context.Context) (<-chan PolicyChangeEvent, error)

	// GetSource returns a human-readable description of the policy source
	GetSource() string
}

// PolicyConfiguration represents the complete policy configuration
type PolicyConfiguration struct {
	Version          string             `json:"version" yaml:"version"`
	Metadata         PolicyMetadata     `json:"metadata" yaml:"metadata"`
	Policies         []PolicyDefinition `json:"policies" yaml:"policies"`
	Templates        []PolicyTemplate   `json:"templates,omitempty" yaml:"templates,omitempty"`
	Overrides        []PolicyOverride   `json:"overrides,omitempty" yaml:"overrides,omitempty"`
	DefaultApprovers []string           `json:"default_approvers,omitempty" yaml:"default_approvers,omitempty"`
}

// PolicyMetadata contains metadata about the policy configuration
type PolicyMetadata struct {
	Name        string    `json:"name" yaml:"name"`
	Description string    `json:"description" yaml:"description"`
	Version     string    `json:"version" yaml:"version"`
	Created     time.Time `json:"created" yaml:"created"`
	Modified    time.Time `json:"modified" yaml:"modified"`
	Author      string    `json:"author" yaml:"author"`
}

// PolicyDefinition represents a complete policy with rules
type PolicyDefinition struct {
	Name        string                 `json:"name" yaml:"name"`
	Description string                 `json:"description,omitempty" yaml:"description,omitempty"`
	Enabled     bool                   `json:"enabled" yaml:"enabled"`
	Type        string                 `json:"type" yaml:"type"`
	Scope       PolicyScope            `json:"scope" yaml:"scope"`
	Rules       []PolicyRule           `json:"rules" yaml:"rules"`
	Enforcement string                 `json:"enforcement" yaml:"enforcement"` // "strict", "warn", "disabled"
	Priority    int                    `json:"priority" yaml:"priority"`
	Metadata    map[string]interface{} `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// PolicyTemplate represents a reusable policy template
type PolicyTemplate struct {
	Name        string                 `json:"name" yaml:"name"`
	Description string                 `json:"description,omitempty" yaml:"description,omitempty"`
	Type        string                 `json:"type" yaml:"type"`
	Rules       []PolicyRule           `json:"rules" yaml:"rules"`
	Variables   map[string]interface{} `json:"variables,omitempty" yaml:"variables,omitempty"`
}

// PolicyOverride allows overriding specific policy settings
type PolicyOverride struct {
	PolicyName  string                 `json:"policy_name" yaml:"policy_name"`
	Scope       PolicyScope            `json:"scope" yaml:"scope"`
	Overrides   map[string]interface{} `json:"overrides" yaml:"overrides"`
	Enabled     *bool                  `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Enforcement *string                `json:"enforcement,omitempty" yaml:"enforcement,omitempty"`
}

// PolicyChangeEvent represents a change in policy configuration
type PolicyChangeEvent struct {
	Type      string    `json:"type"` // "reload", "add", "update", "delete"
	PolicyID  string    `json:"policy_id,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Source    string    `json:"source"`
	Error     error     `json:"error,omitempty"`
}

// NullPolicyEngine is a no-op implementation for backward compatibility
type NullPolicyEngine struct{}

// EvaluateFileProtection always returns not protected (original behavior)
func (n *NullPolicyEngine) EvaluateFileProtection(ctx context.Context, filePath string) (*FileProtectionDecision, error) {
	return &FileProtectionDecision{
		Protected: false,
		Reason:    "policy_engine_disabled",
	}, nil
}

// EvaluateMergePolicy always allows merge (original behavior)
func (n *NullPolicyEngine) EvaluateMergePolicy(ctx context.Context, req *MergeRequest) (*PolicyDecision, error) {
	return &PolicyDecision{
		Allowed: true,
	}, nil
}

// ReloadPolicies is a no-op
func (n *NullPolicyEngine) ReloadPolicies(ctx context.Context) error {
	return nil
}

// GetPolicyStatus returns disabled status
func (n *NullPolicyEngine) GetPolicyStatus() *PolicyEngineStatus {
	return &PolicyEngineStatus{
		Initialized: false,
		Version:     "null",
	}
}

// Phase 2 Interfaces

// AdvancedPolicyEngine extends PolicyEngine with Phase 2 capabilities
type AdvancedPolicyEngine interface {
	PolicyEngine

	// Hot Reload Management
	EnableHotReload(ctx context.Context) error
	DisableHotReload() error
	GetReloadStatus() *HotReloadStatus

	// Template Management
	InstantiateTemplate(templateName, instanceName string, variables map[string]interface{}) (*PolicyDefinition, error)
	ListTemplates() []string
	GetTemplate(name string) (*PolicyTemplate, bool)

	// Hierarchical Policy Management
	EvaluateFileProtectionHierarchical(ctx context.Context, filePath string) (*HierarchicalDecision, error)
	SetConflictResolution(strategy ConflictResolutionStrategy)
	GetLayerStatus() *LayerStatus
}

// HotReloadStatus represents the status of hot reloading
type HotReloadStatus struct {
	Running           bool          `json:"running"`
	WatchedFilesCount int           `json:"watched_files_count"`
	LastReloadTime    time.Time     `json:"last_reload_time"`
	DebounceInterval  time.Duration `json:"debounce_interval"`
}

// ConflictResolutionStrategy defines how policy conflicts are resolved
type ConflictResolutionStrategy int

const (
	HighestPriorityWins ConflictResolutionStrategy = iota
	MostRestrictiveWins
	CompositeDecision
	ExplicitOverride
)

// PolicyMergeStrategy defines how multiple applicable policies are merged
type PolicyMergeStrategy int

const (
	FirstMatch PolicyMergeStrategy = iota
	AllMatches
	LayeredEvaluation
)

// HierarchicalDecision represents a decision made through hierarchical policy evaluation
type HierarchicalDecision struct {
	FilePath         string                     `json:"file_path"`
	LayerDecisions   []LayerDecision            `json:"layer_decisions"`
	FinalDecision    *FileProtectionDecision    `json:"final_decision"`
	ConflictDetails  []ConflictDetail           `json:"conflict_details"`
	ResolutionReason string                     `json:"resolution_reason"`
	Strategy         ConflictResolutionStrategy `json:"strategy"`
}

// LayerDecision represents decisions from a single policy layer
type LayerDecision struct {
	LayerName     string                    `json:"layer_name"`
	LayerPriority int                       `json:"layer_priority"`
	Decisions     []*FileProtectionDecision `json:"decisions"`
}

// ConflictDetail represents details about policy conflicts
type ConflictDetail struct {
	Type        string   `json:"type"`
	PolicyNames []string `json:"policy_names"`
	Description string   `json:"description"`
}

// PolicyLayer represents a layer in the policy hierarchy
type PolicyLayer struct {
	Name        string             `yaml:"name" json:"name"`
	Priority    int                `yaml:"priority" json:"priority"`
	Policies    []PolicyDefinition `yaml:"policies" json:"policies"`
	Enabled     bool               `yaml:"enabled" json:"enabled"`
	Description string             `yaml:"description" json:"description"`
}

// LayerStatus represents the status of policy layers
type LayerStatus struct {
	TotalLayers   int                        `json:"total_layers"`
	EnabledLayers int                        `json:"enabled_layers"`
	LayerInfo     []LayerInfo                `json:"layer_info"`
	Strategy      ConflictResolutionStrategy `json:"strategy"`
}

// LayerInfo provides information about a single policy layer
type LayerInfo struct {
	Name        string `json:"name"`
	Priority    int    `json:"priority"`
	Enabled     bool   `json:"enabled"`
	PolicyCount int    `json:"policy_count"`
	RuleCount   int    `json:"rule_count"`
	Description string `json:"description"`
}

// TemplateSpec specifies a template and its variables for composition
type TemplateSpec struct {
	TemplateName string                 `yaml:"template" json:"template"`
	Variables    map[string]interface{} `yaml:"variables" json:"variables"`
}

// ReloadCallback is called when policies are successfully reloaded
type ReloadCallback func(engine *Engine, reloadDuration time.Duration)

// ReloadErrorCallback is called when policy reload fails
type ReloadErrorCallback func(err error, filePath string)

// ValidationReport represents a comprehensive validation report
type ValidationReport struct {
	Valid     bool                `json:"valid"`
	Errors    []ValidationError   `json:"errors"`
	Warnings  []ValidationWarning `json:"warnings"`
	Summary   ValidationSummary   `json:"summary"`
	Timestamp time.Time           `json:"timestamp"`
}

// ValidationError represents a validation error
type ValidationError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Path    string `json:"path"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
}

// ValidationWarning represents a validation warning
type ValidationWarning struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Path    string `json:"path"`
}

// ValidationSummary provides a summary of the validated configuration
type ValidationSummary struct {
	TotalPolicies  int `json:"total_policies"`
	TotalRules     int `json:"total_rules"`
	TotalTemplates int `json:"total_templates"`
	TotalOverrides int `json:"total_overrides"`
}
