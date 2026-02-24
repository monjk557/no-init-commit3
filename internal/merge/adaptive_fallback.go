package merge

import (
	"time"

	"go.uber.org/zap"
)

// AdaptiveFallbackPolicy implements intelligent fallback selection based on merge context
// This component learns from historical merge patterns to make better fallback decisions
type AdaptiveFallbackPolicy struct {
	logger                  *zap.Logger
	environmentPreservation bool
	learningEnabled         bool
	historicalData          []MergeOutcome
	decisionMatrix          *FallbackDecisionMatrix
}

// MergeOutcome represents the result of a previous merge operation
type MergeOutcome struct {
	Timestamp        time.Time
	SourceCommit     string
	TargetCommit     string
	Strategy         MergeStrategy
	IsSquash         bool
	ConflictCount    int
	AutoResolvable   int
	FallbackUsed     bool
	SuccessRate      float64
	EnvironmentLoss  bool
	UserSatisfaction float64
}

// FallbackDecisionMatrix contains rules for intelligent fallback selection
type FallbackDecisionMatrix struct {
	EnvironmentFiles FallbackRule
	ConfigFiles      FallbackRule
	SourceCode       FallbackRule
	Documentation    FallbackRule
	Dependencies     FallbackRule
	Default          FallbackRule
}

// FallbackRule defines the decision logic for a specific file type
type FallbackRule struct {
	PreferTarget            bool
	RequireManualResolution bool
	AllowDeletion           bool
	ConfidenceThreshold     float64
	Description             string
}

// FallbackRecommendation represents the recommended action for a fallback scenario
type FallbackRecommendation struct {
	Strategy          FallbackStrategy
	Confidence        float64
	Reasoning         string
	AlternativeAction string
	RequiresApproval  bool
}

// FallbackStrategy defines the type of fallback to use
type FallbackStrategy int

const (
	FallbackStrategyPreferTarget     FallbackStrategy = iota // Use target tree when possible
	FallbackStrategyPreferSource                             // Use source tree when possible
	FallbackStrategyIntelligentMerge                         // Create intelligent merge tree
	FallbackStrategyRequireManual                            // Force manual resolution
	FallbackStrategyAdaptive                                 // Use AI-driven decision
)

// NewAdaptiveFallbackPolicy creates a new adaptive fallback policy
func NewAdaptiveFallbackPolicy(logger *zap.Logger) *AdaptiveFallbackPolicy {
	return &AdaptiveFallbackPolicy{
		logger:                  logger,
		environmentPreservation: true,
		learningEnabled:         true,
		historicalData:          make([]MergeOutcome, 0),
		decisionMatrix:          createDefaultDecisionMatrix(),
	}
}

// RecommendFallback analyzes the merge context and recommends the best fallback strategy
func (p *AdaptiveFallbackPolicy) RecommendFallback(
	context FallbackContext,
) *FallbackRecommendation {
	// Analyze the merge context for patterns
	contentAnalysis := p.analyzeContentTypes(context.EnvironmentFiles)
	conflictSeverity := p.assessConflictSeverity(context.ConflictCount, context.AutoResolvable)
	historicalPattern := p.findHistoricalPattern(context)

	// Apply decision matrix based on content analysis
	strategy := p.selectStrategy(contentAnalysis, conflictSeverity, context)
	confidence := p.calculateConfidence(contentAnalysis, historicalPattern, context)

	recommendation := &FallbackRecommendation{
		Strategy:   strategy,
		Confidence: confidence,
		Reasoning:  p.generateReasoning(strategy, contentAnalysis, conflictSeverity, context),
	}

	// Override for environment preservation
	if p.environmentPreservation && len(context.EnvironmentFiles) > 0 {
		recommendation = p.applyEnvironmentProtection(recommendation, context)
	}

	p.logger.Info("Generated fallback recommendation",
		zap.String("strategy", p.strategyToString(recommendation.Strategy)),
		zap.Float64("confidence", recommendation.Confidence),
		zap.String("reasoning", recommendation.Reasoning))

	return recommendation
}

// analyzeContentTypes categorizes the types of files involved in the merge
func (p *AdaptiveFallbackPolicy) analyzeContentTypes(environmentFiles []string) map[string]int {
	analysis := map[string]int{
		"environment": len(environmentFiles),
		"config":      0,
		"source":      0,
		"docs":        0,
		"tests":       0,
	}

	// This would be enhanced with actual file analysis
	// For now, we focus on environment files
	return analysis
}

// assessConflictSeverity determines how severe the conflicts are
func (p *AdaptiveFallbackPolicy) assessConflictSeverity(totalConflicts, autoResolvable int) string {
	if totalConflicts == 0 {
		return "none"
	}

	resolutionRate := float64(autoResolvable) / float64(totalConflicts)

	switch {
	case resolutionRate >= 0.8:
		return "low"
	case resolutionRate >= 0.5:
		return "medium"
	case resolutionRate >= 0.2:
		return "high"
	default:
		return "critical"
	}
}

// findHistoricalPattern looks for similar merge scenarios in historical data
func (p *AdaptiveFallbackPolicy) findHistoricalPattern(context FallbackContext) *MergeOutcome {
	// Find the most similar historical merge
	var bestMatch *MergeOutcome
	bestSimilarity := 0.0

	for _, outcome := range p.historicalData {
		similarity := p.calculateSimilarity(context, outcome)
		if similarity > bestSimilarity {
			bestSimilarity = similarity
			bestMatch = &outcome
		}
	}

	return bestMatch
}

// calculateSimilarity computes similarity between current context and historical outcome
func (p *AdaptiveFallbackPolicy) calculateSimilarity(context FallbackContext, outcome MergeOutcome) float64 {
	similarity := 0.0

	// Strategy similarity (30%)
	if string(context.Strategy) == string(outcome.Strategy) {
		similarity += 0.3
	}

	// Squash similarity (20%)
	if context.IsSquash == outcome.IsSquash {
		similarity += 0.2
	}

	// Conflict pattern similarity (30%)
	conflictSimilarity := 1.0 - float64(abs(context.ConflictCount-outcome.ConflictCount))/float64(max(context.ConflictCount, outcome.ConflictCount)+1)
	similarity += 0.3 * conflictSimilarity

	// Environment involvement similarity (20%)
	envInvolved := len(context.EnvironmentFiles) > 0
	histEnvInvolved := outcome.EnvironmentLoss || string(outcome.Strategy) == "environment_aware"
	if envInvolved == histEnvInvolved {
		similarity += 0.2
	}

	return similarity
}

// selectStrategy chooses the best fallback strategy based on analysis
func (p *AdaptiveFallbackPolicy) selectStrategy(
	contentAnalysis map[string]int,
	conflictSeverity string,
	context FallbackContext,
) FallbackStrategy {
	// Environment files always get special treatment
	if contentAnalysis["environment"] > 0 {
		switch conflictSeverity {
		case "critical":
			return FallbackStrategyRequireManual
		case "high":
			return FallbackStrategyIntelligentMerge
		default:
			return FallbackStrategyPreferTarget
		}
	}

	// For squash merges with high conflicts, prefer intelligent merge
	if context.IsSquash && conflictSeverity == "critical" {
		return FallbackStrategyIntelligentMerge
	}

	// Default to target preference for non-squash
	if !context.IsSquash {
		return FallbackStrategyPreferTarget
	}

	// For squash, be more careful
	return FallbackStrategyAdaptive
}

// calculateConfidence computes confidence in the recommendation
func (p *AdaptiveFallbackPolicy) calculateConfidence(
	contentAnalysis map[string]int,
	historicalPattern *MergeOutcome,
	context FallbackContext,
) float64 {
	baseConfidence := 0.7

	// Higher confidence for environment protection
	if contentAnalysis["environment"] > 0 {
		baseConfidence += 0.2
	}

	// Historical pattern match increases confidence
	if historicalPattern != nil && historicalPattern.SuccessRate > 0.8 {
		baseConfidence += 0.1
	}

	// Lower confidence for complex conflicts
	if context.ConflictCount > 10 {
		baseConfidence -= 0.1
	}

	return minFloat(1.0, maxFloat(0.1, baseConfidence))
}

// generateReasoning creates human-readable reasoning for the recommendation
func (p *AdaptiveFallbackPolicy) generateReasoning(
	strategy FallbackStrategy,
	contentAnalysis map[string]int,
	conflictSeverity string,
	context FallbackContext,
) string {
	var reasoning string

	switch strategy {
	case FallbackStrategyPreferTarget:
		reasoning = "Preserving target branch changes to maintain environment configuration"
	case FallbackStrategyIntelligentMerge:
		reasoning = "Creating intelligent merge to balance source and target changes"
	case FallbackStrategyRequireManual:
		reasoning = "Complex conflicts require human review for safety"
	case FallbackStrategyAdaptive:
		reasoning = "Using adaptive strategy based on merge context"
	default:
		reasoning = "Standard fallback strategy"
	}

	if contentAnalysis["environment"] > 0 {
		reasoning += " (environment files protected)"
	}

	if context.IsSquash {
		reasoning += " (squash merge context)"
	}

	return reasoning
}

// applyEnvironmentProtection ensures environment files are always protected
func (p *AdaptiveFallbackPolicy) applyEnvironmentProtection(
	recommendation *FallbackRecommendation,
	context FallbackContext,
) *FallbackRecommendation {
	// Override strategy if it would risk environment loss
	if recommendation.Strategy == FallbackStrategyPreferSource && len(context.EnvironmentFiles) > 0 {
		recommendation.Strategy = FallbackStrategyPreferTarget
		recommendation.Reasoning += " [OVERRIDDEN: Environment protection active]"
		recommendation.AlternativeAction = "Source tree fallback blocked to preserve environment files"
	}

	return recommendation
}

// RecordOutcome records the outcome of a merge operation for learning
func (p *AdaptiveFallbackPolicy) RecordOutcome(outcome MergeOutcome) {
	if !p.learningEnabled {
		return
	}

	p.historicalData = append(p.historicalData, outcome)

	// Limit historical data size
	maxHistorySize := 1000
	if len(p.historicalData) > maxHistorySize {
		// Keep most recent entries
		p.historicalData = p.historicalData[len(p.historicalData)-maxHistorySize:]
	}

	p.logger.Debug("Recorded merge outcome for learning",
		zap.Time("timestamp", outcome.Timestamp),
		zap.Bool("environment_loss", outcome.EnvironmentLoss),
		zap.Float64("success_rate", outcome.SuccessRate))
}

// GetLearningStats returns statistics about the learning system
func (p *AdaptiveFallbackPolicy) GetLearningStats() map[string]interface{} {
	if len(p.historicalData) == 0 {
		return map[string]interface{}{
			"total_merges":     0,
			"learning_enabled": p.learningEnabled,
		}
	}

	var envLossCount, totalSquash, totalSuccess int
	var avgSuccessRate float64

	for _, outcome := range p.historicalData {
		if outcome.EnvironmentLoss {
			envLossCount++
		}
		if outcome.IsSquash {
			totalSquash++
		}
		if outcome.SuccessRate > 0.8 {
			totalSuccess++
		}
		avgSuccessRate += outcome.SuccessRate
	}

	totalMerges := len(p.historicalData)
	avgSuccessRate /= float64(totalMerges)

	return map[string]interface{}{
		"total_merges":          totalMerges,
		"environment_loss_rate": float64(envLossCount) / float64(totalMerges),
		"squash_merge_rate":     float64(totalSquash) / float64(totalMerges),
		"overall_success_rate":  avgSuccessRate,
		"success_percentage":    float64(totalSuccess) / float64(totalMerges),
		"learning_enabled":      p.learningEnabled,
	}
}

// strategyToString converts strategy enum to string
func (p *AdaptiveFallbackPolicy) strategyToString(strategy FallbackStrategy) string {
	switch strategy {
	case FallbackStrategyPreferTarget:
		return "prefer_target"
	case FallbackStrategyPreferSource:
		return "prefer_source"
	case FallbackStrategyIntelligentMerge:
		return "intelligent_merge"
	case FallbackStrategyRequireManual:
		return "require_manual"
	case FallbackStrategyAdaptive:
		return "adaptive"
	default:
		return "unknown"
	}
}

// createDefaultDecisionMatrix creates the default decision rules
func createDefaultDecisionMatrix() *FallbackDecisionMatrix {
	return &FallbackDecisionMatrix{
		EnvironmentFiles: FallbackRule{
			PreferTarget:            true,
			RequireManualResolution: false,
			AllowDeletion:           false,
			ConfidenceThreshold:     0.9,
			Description:             "Environment files must never be deleted",
		},
		ConfigFiles: FallbackRule{
			PreferTarget:            true,
			RequireManualResolution: false,
			AllowDeletion:           false,
			ConfidenceThreshold:     0.8,
			Description:             "Configuration files should prefer target",
		},
		SourceCode: FallbackRule{
			PreferTarget:            false,
			RequireManualResolution: false,
			AllowDeletion:           true,
			ConfidenceThreshold:     0.7,
			Description:             "Source code can use flexible strategy",
		},
		Documentation: FallbackRule{
			PreferTarget:            false,
			RequireManualResolution: false,
			AllowDeletion:           true,
			ConfidenceThreshold:     0.6,
			Description:             "Documentation has lower protection priority",
		},
		Dependencies: FallbackRule{
			PreferTarget:            true,
			RequireManualResolution: true,
			AllowDeletion:           false,
			ConfidenceThreshold:     0.85,
			Description:             "Dependencies require careful handling",
		},
		Default: FallbackRule{
			PreferTarget:            false,
			RequireManualResolution: false,
			AllowDeletion:           true,
			ConfidenceThreshold:     0.5,
			Description:             "Default fallback rule",
		},
	}
}

// Helper functions
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
