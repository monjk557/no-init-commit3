package core_test

import (
	"testing"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewMergePreviewHandler tests merge preview handler creation
func TestNewMergePreviewHandler(t *testing.T) {
	// 중앙에서 관리하는 설정 사용
	cfg := config.NewTestConfig("/tmp/test-repos")

	handler, err := protocol.NewMergePreviewHandler(cfg)
	require.NoError(t, err)
	require.NotNil(t, handler)
}

// TestNewMergePreviewHandlerWithNilConfig tests error handling
func TestNewMergePreviewHandlerWithNilConfig(t *testing.T) {
	handler, err := protocol.NewMergePreviewHandler(nil)
	assert.Error(t, err)
	assert.Nil(t, handler)
	assert.Contains(t, err.Error(), "config cannot be nil")
}

// TestMergePreviewHandlerStructure tests handler structure
func TestMergePreviewHandlerStructure(t *testing.T) {
	// 중앙에서 관리하는 설정 사용
	cfg := config.NewTestConfig("/tmp/test-repos")

	handler, err := protocol.NewMergePreviewHandler(cfg)
	require.NoError(t, err)
	require.NotNil(t, handler)

	// Test that handler has required components
	// Note: We can't test unexported fields directly, but we can test that
	// the handler was created successfully without errors
}

// TestMergePreviewWithInvalidRepoRoot tests error handling for invalid repo root
func TestMergePreviewWithInvalidRepoRoot(t *testing.T) {
	// 중앙에서 관리하는 설정 사용하되, repo root를 빈 값으로 오버라이드
	cfg := config.NewTestConfigWithOverrides("", config.WithRepoRoot(""))

	handler, err := protocol.NewMergePreviewHandler(cfg)
	// This might succeed or fail depending on implementation
	// We just test that it doesn't panic
	if err != nil {
		assert.Contains(t, err.Error(), "path config")
	} else {
		assert.NotNil(t, handler)
	}
}

// TestMergePreviewWithInvalidLoggingConfig tests error handling for invalid logging config
func TestMergePreviewWithInvalidLoggingConfig(t *testing.T) {
	// 중앙에서 관리하는 설정 사용하되, invalid logging level로 오버라이드
	cfg := config.NewTestConfigWithOverrides(
		"/tmp/test-repos",
		config.WithLogging("invalid-level", "json"),
	)

	handler, err := protocol.NewMergePreviewHandler(cfg)
	// This might succeed or fail depending on implementation
	// We just test that it doesn't panic
	if err != nil {
		assert.Contains(t, err.Error(), "logger")
	} else {
		assert.NotNil(t, handler)
	}
}

// BenchmarkNewMergePreviewHandler benchmarks handler creation
func BenchmarkNewMergePreviewHandler(b *testing.B) {
	// 중앙에서 관리하는 설정 사용
	cfg := config.NewTestConfig("/tmp/test-repos")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		handler, err := protocol.NewMergePreviewHandler(cfg)
		if err != nil {
			b.Fatal(err)
		}
		_ = handler
	}
}
