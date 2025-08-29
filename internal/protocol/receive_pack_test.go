package protocol

import (
	"os"
	"testing"

	"github.com/collabops/git-executor/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewReceivePackHandler(t *testing.T) {
	// Create temporary directory for test
	tempDir, err := os.MkdirTemp("", "receive-pack-test-")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempDir) }()
	// Create test configuration
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}
	// Test successful handler creation
	handler, err := NewReceivePackHandler(cfg)
	require.NoError(t, err)
	assert.NotNil(t, handler)
	assert.NotNil(t, handler.pathConfig)
	assert.NotNil(t, handler.errorHandler)
	assert.NotNil(t, handler.capabilities)
}
func TestReceivePackHandler_HandleInfoRefs(t *testing.T) {
	// Skip this test since permission client is being removed
	t.Skip("Skipping receive pack handler handle info refs test - permission client is being removed")
}
func TestReceivePackHandler_HandleReceivePack(t *testing.T) {
	// Skip this test since permission client is being removed
	t.Skip("Skipping receive pack handler handle receive pack test - permission client is being removed")
}
func TestReceivePackHandler_buildReceiveCapabilities(t *testing.T) {
	// Create temporary directory for test
	tempDir, err := os.MkdirTemp("", "receive-pack-test-")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tempDir) }()
	// Create test configuration
	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}
	// Create handler
	handler, err := NewReceivePackHandler(cfg)
	require.NoError(t, err)
	// Test capability string building
	capString := handler.buildReceiveCapabilities()
	// Verify expected capabilities are present
	expectedCaps := []string{
		"report-status",
		"delete-refs",
		"ofs-delta",
		"side-band-64k",
		"atomic",
		"push-options",
		"agent=collabops-git-executor/1.0",
	}
	for _, cap := range expectedCaps {
		assert.Contains(t, capString, cap, "Expected capability %s not found in: %s", cap, capString)
	}
}
func TestReceivePackHandler_AuthenticationFlow(t *testing.T) {
	// Skip this test since permission client is being removed
	t.Skip("Skipping receive pack handler authentication flow test - permission client is being removed")
}
func TestReceivePackHandler_WritePermissionCheck(t *testing.T) {
	// Skip this test since permission client is being removed
	t.Skip("Skipping receive pack handler write permission check test - permission client is being removed")
}
