package mergeprotection

import (
	"path/filepath"
	"strings"

	"github.com/collabops/git-executor/internal/config"
)

// ProtectionResult represents the result of merge protection evaluation
type ProtectionResult struct {
	Allowed      bool
	Reason       string
	BlockedFiles []string
}

// EvaluateProtection evaluates if a merge should be protected
func EvaluateProtection(config config.MergeProtectionConfig, filePaths []string) ProtectionResult {
	if !config.Enabled {
		return ProtectionResult{
			Allowed: true,
			Reason:  "protection_disabled",
		}
	}

	blockedFiles := make([]string, 0)

	for _, filePath := range filePaths {
		if isProtectedFile(filePath, config) {
			blockedFiles = append(blockedFiles, filePath)
		}
	}

	if len(blockedFiles) > 0 {
		return ProtectionResult{
			Allowed:      false,
			Reason:       "protected_files_detected",
			BlockedFiles: blockedFiles,
		}
	}

	return ProtectionResult{
		Allowed: true,
		Reason:  "no_protected_files",
	}
}

// isProtectedFile checks if a file is protected by the configuration
func isProtectedFile(filePath string, config config.MergeProtectionConfig) bool {
	envConfig := config.EnvironmentProtection

	// Check environment files
	if envConfig.ProtectEnvFiles {
		if strings.HasSuffix(filePath, ".env") || strings.Contains(filePath, ".env.") {
			return true
		}
	}

	// Check config files
	if envConfig.ProtectConfigFiles {
		if strings.Contains(filePath, "config/") {
			return true
		}
		if matched, _ := filepath.Match("*.yaml", filePath); matched {
			return true
		}
		if matched, _ := filepath.Match("*.yml", filePath); matched {
			return true
		}
	}

	// Check Docker files
	if envConfig.ProtectDockerFiles {
		if strings.Contains(filePath, "docker-compose") || strings.Contains(filePath, "Dockerfile") {
			return true
		}
	}

	// Check database files
	if envConfig.ProtectDatabaseFiles {
		if strings.Contains(filePath, "database") || strings.HasSuffix(filePath, ".conf") {
			return true
		}
	}

	// Check critical paths
	for _, criticalPath := range envConfig.CriticalPaths {
		if matched, _ := filepath.Match(criticalPath, filePath); matched {
			return true
		}
	}

	return false
}
