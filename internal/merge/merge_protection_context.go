package merge

import (
	"context"
	"os"

	"go.uber.org/zap"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/policy"
)

// MergeProtectionContext carries protection settings through the merge operation
type MergeProtectionContext struct {
	// Global protection settings
	ProtectionEnabled     bool
	EnvironmentProtection bool

	// Method-specific protection
	SquashProtection  bool
	RegularProtection bool
	RebaseProtection  bool

	// Operational context
	IsSquash        bool
	IsRebase        bool
	DestructiveMode bool // When true, allows environment file deletion

	// Safety settings
	SafetyMode       string
	FallbackStrategy string

	// Policy Engine Integration (Always Enabled)
	PolicyEngine policy.PolicyEngine
	Logger       *zap.Logger
}

// NewMergeProtectionContext creates protection context from config
func NewMergeProtectionContext(cfg *config.Config, isSquash, isRebase bool) *MergeProtectionContext {
	// Initialize logger
	logger := zap.NewNop()
	if cfg.Logging.Level != "" {
		if l, err := zap.NewDevelopment(); err == nil {
			logger = l
		}
	}

	ctx := &MergeProtectionContext{
		ProtectionEnabled:     cfg.MergeProtection.Enabled,
		EnvironmentProtection: cfg.MergeProtection.EnvironmentProtection.Enabled,
		SquashProtection:      cfg.MergeProtection.EnvironmentProtection.EnableForSquashMerge,
		RegularProtection:     cfg.MergeProtection.EnvironmentProtection.EnableForRegularMerge,
		RebaseProtection:      cfg.MergeProtection.EnvironmentProtection.EnableForRebaseMerge,
		IsSquash:              isSquash,
		IsRebase:              isRebase,
		SafetyMode:            cfg.MergeProtection.SafetyMode,
		FallbackStrategy:      cfg.MergeProtection.FallbackStrategy,
		Logger:                logger,

		// Legacy patterns removed - now using PolicyEngine
		// EnvironmentFilePatterns field is deprecated
	}

	// Always initialize PolicyEngine (feature flag removed)
	ctx.PolicyEngine = ctx.initializePolicyEngine(logger)

	// Calculate if we should be in destructive mode
	ctx.DestructiveMode = ctx.calculateDestructiveMode()

	return ctx
}

// Feature flag function removed - PolicyEngine now always enabled

// initializePolicyEngine creates and initializes the PolicyEngine
func (ctx *MergeProtectionContext) initializePolicyEngine(logger *zap.Logger) policy.PolicyEngine {
	// Try to find policy configuration files
	configPaths := []string{
		"configs/policies/environment-protection.yml",
		"./configs/policies/environment-protection.yml",
		"../configs/policies/environment-protection.yml",
		"/etc/collabops/policies/environment-protection.yml",
	}

	var policyConfigPath string
	for _, path := range configPaths {
		if _, err := os.Stat(path); err == nil {
			policyConfigPath = path
			break
		}
	}

	if policyConfigPath == "" {
		logger.Warn("No policy configuration found, using NullPolicyEngine",
			zap.Strings("searched_paths", configPaths))
		return &policy.NullPolicyEngine{}
	}

	// Create file-based provider
	provider := policy.NewFileBasedProvider(policyConfigPath)
	engine := policy.NewEngine(provider, logger)

	// Initialize the engine
	if err := engine.Initialize(context.Background()); err != nil {
		logger.Error("Failed to initialize PolicyEngine, falling back to NullPolicyEngine",
			zap.Error(err),
			zap.String("config_path", policyConfigPath))
		return &policy.NullPolicyEngine{}
	}

	logger.Info("PolicyEngine initialized successfully",
		zap.String("config_path", policyConfigPath),
		zap.String("engine_status", engine.GetPolicyStatus().ConfigSource))

	return engine
}

// calculateDestructiveMode determines if files should be deleted
func (ctx *MergeProtectionContext) calculateDestructiveMode() bool {
	// If global protection is disabled, allow destruction
	if !ctx.ProtectionEnabled {
		return true
	}

	// If environment protection is disabled, allow destruction
	if !ctx.EnvironmentProtection {
		return true
	}

	// Check method-specific protection
	if ctx.IsSquash && !ctx.SquashProtection {
		return true
	}

	if !ctx.IsSquash && !ctx.IsRebase && !ctx.RegularProtection {
		return true
	}

	if ctx.IsRebase && !ctx.RebaseProtection {
		return true
	}

	// If all protections are enabled, do not allow destruction
	return false
}

// ShouldPreserveFile determines if a specific file should be preserved using PolicyEngine
func (ctx *MergeProtectionContext) ShouldPreserveFile(filePath string) bool {
	// Always use PolicyEngine (legacy fallback removed)
	if ctx.PolicyEngine == nil {
		ctx.Logger.Warn("PolicyEngine not initialized, defaulting to preserve",
			zap.String("file", filePath))
		return true // Safe default: preserve file
	}

	return ctx.shouldPreserveFileUsingPolicyEngine(filePath)
}

// shouldPreserveFileUsingPolicyEngine uses PolicyEngine to determine file preservation
func (ctx *MergeProtectionContext) shouldPreserveFileUsingPolicyEngine(filePath string) bool {
	// Always preserve in non-destructive mode
	if !ctx.DestructiveMode {
		ctx.Logger.Debug("Preserving file (non-destructive mode)",
			zap.String("file", filePath),
			zap.Bool("destructive_mode", ctx.DestructiveMode))
		return true
	}

	// In destructive mode, consult PolicyEngine
	decision, err := ctx.PolicyEngine.EvaluateFileProtection(context.Background(), filePath)
	if err != nil {
		ctx.Logger.Error("PolicyEngine evaluation failed, defaulting to preserve",
			zap.String("file", filePath),
			zap.Error(err))
		return true // Safe default: preserve file
	}

	// If file is protected by policy, preserve it
	if decision.Protected {
		ctx.Logger.Debug("File protected by policy engine",
			zap.String("file", filePath),
			zap.String("reason", decision.Reason),
			zap.Bool("protected", decision.Protected))
		return true
	}

	// File is not protected and we're in destructive mode - allow deletion
	ctx.Logger.Debug("File not protected, allowing deletion",
		zap.String("file", filePath),
		zap.String("reason", decision.Reason),
		zap.Bool("destructive_mode", ctx.DestructiveMode))
	return false
}

// Legacy functions removed - now using PolicyEngine for all file protection decisions

// GetProtectionStatus returns a human-readable protection status
func (ctx *MergeProtectionContext) GetProtectionStatus() string {
	if ctx.DestructiveMode {
		return "DESTRUCTIVE_MODE_ENABLED"
	}
	return "PROTECTION_MODE_ENABLED"
}

// GetEffectiveProtectionDescription returns detailed protection description
func (ctx *MergeProtectionContext) GetEffectiveProtectionDescription() string {
	if ctx.DestructiveMode {
		return "Environment files will be deleted if removed by source branch (original Git behavior)"
	}
	return "Environment files will be preserved even if removed by source branch (protected behavior)"
}
