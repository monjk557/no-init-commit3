package policy

import (
	"context"
	"fmt"
	"regexp"
	"sort"

	"go.uber.org/zap"
)

// HierarchicalPolicyManager manages layered policy evaluation
type HierarchicalPolicyManager struct {
	logger             *zap.Logger
	conflictResolution ConflictResolutionStrategy
	layers             []PolicyLayer
	mergeStrategy      PolicyMergeStrategy
}

// Note: PolicyLayer is defined in interfaces.go

// Note: ConflictResolutionStrategy and PolicyMergeStrategy are defined in interfaces.go

// NewHierarchicalPolicyManager creates a new hierarchical policy manager
func NewHierarchicalPolicyManager(logger *zap.Logger) *HierarchicalPolicyManager {
	if logger == nil {
		logger = zap.NewNop()
	}

	return &HierarchicalPolicyManager{
		logger:             logger,
		conflictResolution: MostRestrictiveWins,
		mergeStrategy:      LayeredEvaluation,
		layers:             make([]PolicyLayer, 0),
	}
}

// SetConflictResolution sets the conflict resolution strategy
func (hpm *HierarchicalPolicyManager) SetConflictResolution(strategy ConflictResolutionStrategy) {
	hpm.conflictResolution = strategy
}

// SetMergeStrategy sets the policy merge strategy
func (hpm *HierarchicalPolicyManager) SetMergeStrategy(strategy PolicyMergeStrategy) {
	hpm.mergeStrategy = strategy
}

// AddLayer adds a policy layer to the hierarchy
func (hpm *HierarchicalPolicyManager) AddLayer(layer PolicyLayer) {
	hpm.layers = append(hpm.layers, layer)
	hpm.sortLayers()

	hpm.logger.Info("Policy layer added",
		zap.String("name", layer.Name),
		zap.Int("priority", layer.Priority),
		zap.Int("policies", len(layer.Policies)),
		zap.Bool("enabled", layer.Enabled))
}

// sortLayers sorts layers by priority (highest first)
func (hpm *HierarchicalPolicyManager) sortLayers() {
	sort.Slice(hpm.layers, func(i, j int) bool {
		return hpm.layers[i].Priority > hpm.layers[j].Priority
	})
}

// EvaluateFileProtectionHierarchical evaluates file protection across all layers
func (hpm *HierarchicalPolicyManager) EvaluateFileProtectionHierarchical(ctx context.Context, filePath string) (*HierarchicalDecision, error) {
	decision := &HierarchicalDecision{
		FilePath:        filePath,
		LayerDecisions:  make([]LayerDecision, 0),
		ConflictDetails: make([]ConflictDetail, 0),
		Strategy:        hpm.conflictResolution,
	}

	// Evaluate each layer
	for _, layer := range hpm.layers {
		if !layer.Enabled {
			continue
		}

		layerDecision := hpm.evaluateLayerForFileProtection(ctx, layer, filePath)
		decision.LayerDecisions = append(decision.LayerDecisions, layerDecision)
	}

	// Resolve conflicts and determine final decision
	if err := hpm.resolveFileProtectionConflicts(decision); err != nil {
		return nil, fmt.Errorf("failed to resolve policy conflicts: %w", err)
	}

	hpm.logger.Debug("Hierarchical file protection evaluated",
		zap.String("file", filePath),
		zap.Bool("final_protected", decision.FinalDecision.Protected),
		zap.Int("layers_evaluated", len(decision.LayerDecisions)),
		zap.Int("conflicts", len(decision.ConflictDetails)))

	return decision, nil
}

// evaluateLayerForFileProtection evaluates a single layer for file protection
func (hpm *HierarchicalPolicyManager) evaluateLayerForFileProtection(ctx context.Context, layer PolicyLayer, filePath string) LayerDecision {
	layerDecision := LayerDecision{
		LayerName:     layer.Name,
		LayerPriority: layer.Priority,
		Decisions:     make([]*FileProtectionDecision, 0),
	}

	// Evaluate each policy in the layer
	for _, policy := range layer.Policies {
		if !policy.Enabled || policy.Type != "file_protection" {
			continue
		}

		for _, rule := range policy.Rules {
			if rule.Type != "file_protection" {
				continue
			}

			// Check if file matches this rule
			engine := &Engine{patternCache: make(map[string]*regexp.Regexp)}
			if matched, matchedPattern := engine.matchesFilePatterns(filePath, rule.Patterns); matched {
				decision := &FileProtectionDecision{
					RuleMatched: &rule,
					Metadata: map[string]interface{}{
						"policy_name":     policy.Name,
						"matched_pattern": matchedPattern,
						"enforcement":     policy.Enforcement,
						"rule_action":     rule.Action,
						"layer_name":      layer.Name,
						"layer_priority":  layer.Priority,
					},
				}

				// Determine protection based on action
				switch rule.Action {
				case "block_deletion", "require_approval":
					decision.Protected = true
					decision.Reason = fmt.Sprintf("file matches protected pattern '%s' in policy '%s' (layer: %s)", matchedPattern, policy.Name, layer.Name)
				case "warn":
					decision.Protected = false
					decision.Reason = fmt.Sprintf("file matches warning pattern '%s' in policy '%s' (layer: %s)", matchedPattern, policy.Name, layer.Name)
				case "allow":
					decision.Protected = false
					decision.Reason = fmt.Sprintf("file explicitly allowed by pattern '%s' in policy '%s' (layer: %s)", matchedPattern, policy.Name, layer.Name)
				default:
					decision.Protected = true
					decision.Reason = fmt.Sprintf("file matches pattern '%s' in policy '%s' (layer: %s, default protection)", matchedPattern, policy.Name, layer.Name)
				}

				layerDecision.Decisions = append(layerDecision.Decisions, decision)
			}
		}
	}

	return layerDecision
}

// resolveFileProtectionConflicts resolves conflicts between layer decisions
func (hpm *HierarchicalPolicyManager) resolveFileProtectionConflicts(decision *HierarchicalDecision) error {
	if len(decision.LayerDecisions) == 0 {
		decision.FinalDecision = &FileProtectionDecision{
			Protected: false,
			Reason:    "no_matching_protection_rules_in_hierarchy",
		}
		return nil
	}

	switch hpm.conflictResolution {
	case HighestPriorityWins:
		return hpm.resolveByHighestPriority(decision)
	case MostRestrictiveWins:
		return hpm.resolveByMostRestrictive(decision)
	case CompositeDecision:
		return hpm.resolveByComposite(decision)
	case ExplicitOverride:
		return hpm.resolveByExplicitOverride(decision)
	default:
		return hpm.resolveByMostRestrictive(decision) // Default strategy
	}
}

// resolveByHighestPriority uses the highest priority matching decision
func (hpm *HierarchicalPolicyManager) resolveByHighestPriority(decision *HierarchicalDecision) error {
	// Layers are already sorted by priority, so take the first match
	for _, layerDecision := range decision.LayerDecisions {
		if len(layerDecision.Decisions) > 0 {
			// Take the first decision in the highest priority layer
			decision.FinalDecision = layerDecision.Decisions[0]
			decision.ResolutionReason = fmt.Sprintf("highest priority layer (%s) decision", layerDecision.LayerName)
			return nil
		}
	}

	decision.FinalDecision = &FileProtectionDecision{
		Protected: false,
		Reason:    "no_matching_rules_in_hierarchy",
	}
	return nil
}

// resolveByMostRestrictive uses the most restrictive decision across all layers
func (hpm *HierarchicalPolicyManager) resolveByMostRestrictive(decision *HierarchicalDecision) error {
	var mostRestrictive *FileProtectionDecision
	var restrictionLevel int // 0=allow, 1=warn, 2=require_approval, 3=block

	for _, layerDecision := range decision.LayerDecisions {
		for _, fileDecision := range layerDecision.Decisions {
			level := hpm.getRestrictionLevel(fileDecision)
			if level > restrictionLevel {
				restrictionLevel = level
				mostRestrictive = fileDecision
			}

			// Record conflicts if multiple policies apply
			if mostRestrictive != nil && mostRestrictive != fileDecision && level > 0 {
				decision.ConflictDetails = append(decision.ConflictDetails, ConflictDetail{
					Type: "restriction_level",
					PolicyNames: []string{
						mostRestrictive.Metadata["policy_name"].(string),
						fileDecision.Metadata["policy_name"].(string),
					},
					Description: fmt.Sprintf("Conflicting protection levels: %s vs %s",
						mostRestrictive.Metadata["rule_action"],
						fileDecision.Metadata["rule_action"]),
				})
			}
		}
	}

	if mostRestrictive != nil {
		decision.FinalDecision = mostRestrictive
		decision.ResolutionReason = "most restrictive policy applied"
	} else {
		decision.FinalDecision = &FileProtectionDecision{
			Protected: false,
			Reason:    "no_matching_rules_in_hierarchy",
		}
	}

	return nil
}

// getRestrictionLevel returns a numeric restriction level for comparison
func (hpm *HierarchicalPolicyManager) getRestrictionLevel(decision *FileProtectionDecision) int {
	action, ok := decision.Metadata["rule_action"].(string)
	if !ok {
		return 0
	}

	switch action {
	case "allow":
		return 0
	case "warn":
		return 1
	case "require_approval":
		return 2
	case "block_deletion", "block_modification":
		return 3
	default:
		return 1
	}
}

// resolveByComposite creates a composite decision from all applicable policies
func (hpm *HierarchicalPolicyManager) resolveByComposite(decision *HierarchicalDecision) error {
	var hasBlockingRules bool
	var hasApprovalRequired bool
	var hasWarnings bool
	var reasons []string

	for _, layerDecision := range decision.LayerDecisions {
		for _, fileDecision := range layerDecision.Decisions {
			action, ok := fileDecision.Metadata["rule_action"].(string)
			if !ok {
				continue
			}

			switch action {
			case "block_deletion", "block_modification":
				hasBlockingRules = true
			case "require_approval":
				hasApprovalRequired = true
			case "warn":
				hasWarnings = true
			}

			reasons = append(reasons, fileDecision.Reason)
		}
	}

	// Create composite decision
	protected := hasBlockingRules || hasApprovalRequired
	reason := "composite decision: "

	if hasBlockingRules {
		reason += "blocking rules present"
	} else if hasApprovalRequired {
		reason += "approval required"
	} else if hasWarnings {
		reason += "warnings only"
		protected = false
	} else {
		reason += "no restrictions"
		protected = false
	}

	decision.FinalDecision = &FileProtectionDecision{
		Protected: protected,
		Reason:    reason,
		Metadata: map[string]interface{}{
			"composite_decision": true,
			"has_blocking":       hasBlockingRules,
			"has_approval":       hasApprovalRequired,
			"has_warnings":       hasWarnings,
			"component_reasons":  reasons,
		},
	}

	decision.ResolutionReason = "composite decision from all applicable policies"
	return nil
}

// resolveByExplicitOverride only applies explicit override policies
func (hpm *HierarchicalPolicyManager) resolveByExplicitOverride(decision *HierarchicalDecision) error {
	// Look for explicit overrides (highest priority policies with "override" in metadata)
	for _, layerDecision := range decision.LayerDecisions {
		for _, fileDecision := range layerDecision.Decisions {
			if metadata, ok := fileDecision.Metadata["explicit_override"]; ok && metadata == true {
				decision.FinalDecision = fileDecision
				decision.ResolutionReason = fmt.Sprintf("explicit override from layer %s", layerDecision.LayerName)
				return nil
			}
		}
	}

	// If no explicit overrides, fall back to highest priority
	return hpm.resolveByHighestPriority(decision)
}

// GetLayerStatus returns status information about policy layers
func (hpm *HierarchicalPolicyManager) GetLayerStatus() *LayerStatus {
	status := &LayerStatus{
		TotalLayers:   len(hpm.layers),
		EnabledLayers: 0,
		LayerInfo:     make([]LayerInfo, 0, len(hpm.layers)),
		Strategy:      hpm.conflictResolution,
	}

	for _, layer := range hpm.layers {
		info := LayerInfo{
			Name:        layer.Name,
			Priority:    layer.Priority,
			Enabled:     layer.Enabled,
			PolicyCount: len(layer.Policies),
			Description: layer.Description,
		}

		// Count total rules in this layer
		for _, policy := range layer.Policies {
			info.RuleCount += len(policy.Rules)
		}

		if layer.Enabled {
			status.EnabledLayers++
		}

		status.LayerInfo = append(status.LayerInfo, info)
	}

	return status
}

// Note: All data structures (HierarchicalDecision, LayerDecision, ConflictDetail, LayerStatus, LayerInfo) are defined in interfaces.go
