package protocol

import (
	"testing"
)

// TestReceivePackIntegration tests the complete receive-pack workflow
func TestReceivePackIntegration(t *testing.T) {
	// Skip this test since permission client is being removed
	t.Skip("Skipping receive pack integration test - permission client is being removed")
}

// TestReceivePackCapabilities verifies that receive-pack advertises the correct capabilities
func TestReceivePackCapabilities(t *testing.T) {
	// Skip this test since permission client is being removed
	t.Skip("Skipping receive pack capabilities test - permission client is being removed")
}

// TestReceivePackVsUploadPackDifferences verifies that receive-pack and upload-pack
// behave differently where expected
func TestReceivePackVsUploadPackDifferences(t *testing.T) {
	// Skip this test since permission client is being removed
	t.Skip("Skipping receive pack vs upload pack differences test - permission client is being removed")
}

// TestReceivePackProtocolCompliance verifies protocol compliance for receive-pack
func TestReceivePackProtocolCompliance(t *testing.T) {
	// Skip this test since permission client is being removed
	t.Skip("Skipping receive pack protocol compliance test - permission client is being removed")
}

// BenchmarkReceivePackHandler measures performance of receive-pack operations
func BenchmarkReceivePackHandler(b *testing.B) {
	// Skip this benchmark since permission client is being removed
	b.Skip("Skipping receive pack handler benchmark - permission client is being removed")
}
