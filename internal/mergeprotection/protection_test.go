package mergeprotection

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/collabops/git-executor/internal/config"
)

// 회귀: "merge protection 평가가 파일 패턴별로 정확히 동작해야 함"

func baseConfig() config.MergeProtectionConfig {
	return config.MergeProtectionConfig{
		Enabled: true,
		EnvironmentProtection: config.EnvironmentProtectionConfig{
			ProtectEnvFiles:      true,
			ProtectConfigFiles:   true,
			ProtectDockerFiles:   true,
			ProtectDatabaseFiles: true,
			CriticalPaths:        []string{"infra/*"},
		},
	}
}

func TestEvaluateProtection_Disabled(t *testing.T) {
	cfg := baseConfig()
	cfg.Enabled = false
	res := EvaluateProtection(cfg, []string{".env", "Dockerfile"})
	assert.True(t, res.Allowed)
	assert.Equal(t, "protection_disabled", res.Reason)
	assert.Empty(t, res.BlockedFiles)
}

func TestEvaluateProtection_NoProtectedFiles(t *testing.T) {
	res := EvaluateProtection(baseConfig(), []string{"src/main.go", "README.md"})
	assert.True(t, res.Allowed)
	assert.Equal(t, "no_protected_files", res.Reason)
	assert.Empty(t, res.BlockedFiles)
}

func TestEvaluateProtection_BlocksProtectedFiles(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"env file", ".env"},
		{"env variant", "deploy/.env.production"},
		{"config dir", "config/app.toml"},
		{"yaml file", "app.yaml"},
		{"yml file", "app.yml"},
		{"docker compose", "docker-compose.yml"},
		{"dockerfile", "build/Dockerfile"},
		{"database path", "database/schema.sql"},
		{"conf suffix", "nginx.conf"},
		{"critical path", "infra/secrets"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := EvaluateProtection(baseConfig(), []string{tt.path})
			assert.False(t, res.Allowed)
			assert.Equal(t, "protected_files_detected", res.Reason)
			assert.Equal(t, []string{tt.path}, res.BlockedFiles)
		})
	}
}

func TestEvaluateProtection_ReportsAllBlocked(t *testing.T) {
	res := EvaluateProtection(baseConfig(), []string{".env", "src/ok.go", "Dockerfile"})
	assert.False(t, res.Allowed)
	assert.ElementsMatch(t, []string{".env", "Dockerfile"}, res.BlockedFiles)
}

func TestEvaluateProtection_FlagsRespected(t *testing.T) {
	// 모든 패턴 보호 플래그 끄면 protected 로 잡히지 않음
	cfg := config.MergeProtectionConfig{
		Enabled:               true,
		EnvironmentProtection: config.EnvironmentProtectionConfig{},
	}
	res := EvaluateProtection(cfg, []string{".env", "config/x.yaml", "Dockerfile", "database/x.sql"})
	assert.True(t, res.Allowed)
	assert.Equal(t, "no_protected_files", res.Reason)
}
