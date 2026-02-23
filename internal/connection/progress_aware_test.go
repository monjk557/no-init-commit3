package connection

import (
	"context"
	"net"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

// MockTCPConn is a mock implementation of net.Conn for testing
type MockTCPConn struct {
	readBuffer  []byte
	writeBuffer []byte
	closed      bool
	readPos     int
}

func (m *MockTCPConn) Read(b []byte) (int, error) {
	if m.closed {
		return 0, net.ErrClosed
	}
	remaining := len(m.readBuffer) - m.readPos
	if remaining == 0 {
		// Simulate slow/stalled connection by blocking
		time.Sleep(100 * time.Millisecond)
		return 0, nil
	}

	n := copy(b, m.readBuffer[m.readPos:])
	m.readPos += n
	return n, nil
}

func (m *MockTCPConn) Write(b []byte) (int, error) {
	if m.closed {
		return 0, net.ErrClosed
	}
	m.writeBuffer = append(m.writeBuffer, b...)
	return len(b), nil
}

func (m *MockTCPConn) Close() error {
	m.closed = true
	return nil
}

func (m *MockTCPConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080}
}

func (m *MockTCPConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345}
}

func (m *MockTCPConn) SetDeadline(t time.Time) error {
	return nil
}

func (m *MockTCPConn) SetReadDeadline(t time.Time) error {
	return nil
}

func (m *MockTCPConn) SetWriteDeadline(t time.Time) error {
	return nil
}

func TestProgressAwareConnection_PhaseTransitions(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockConn := &MockTCPConn{}

	policy := &TimeoutPolicy{
		HandshakeTimeout:      2 * time.Second,
		AuthTimeout:           3 * time.Second,
		ProtocolNegTimeout:    4 * time.Second,
		OperationIdleTimeout:  10 * time.Second,
		OperationMaxTimeout:   60 * time.Second,
		StallThreshold:        5 * time.Second,
		ProgressCheckInterval: 1 * time.Second,
	}

	conn := NewProgressAwareConnection(mockConn, policy, logger)
	defer conn.Close()

	// Test phase transitions
	phases := []ConnectionPhase{
		PhaseHandshake,
		PhaseAuth,
		PhaseProtocolNegotiation,
		PhaseOperation,
	}

	for _, phase := range phases {
		if err := conn.SetPhase(phase); err != nil {
			t.Errorf("Failed to set phase %v: %v", phase, err)
		}
	}
}

func TestProgressAwareConnection_ProgressTracking(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockConn := &MockTCPConn{
		readBuffer: []byte("Hello, Git World!"),
	}

	policy := DefaultTimeoutPolicy()
	policy.StallThreshold = 1 * time.Second // Short threshold for testing

	conn := NewProgressAwareConnection(mockConn, policy, logger)
	defer conn.Close()

	// Set to operation phase
	if err := conn.SetPhase(PhaseOperation); err != nil {
		t.Fatalf("Failed to set operation phase: %v", err)
	}

	// Simulate reading data (should record progress)
	buffer := make([]byte, 10)
	n, err := conn.Read(buffer)
	if err != nil {
		t.Errorf("Failed to read: %v", err)
	}

	if n == 0 {
		t.Error("No bytes read")
	}

	// Check progress was recorded
	tracker := conn.GetProgressTracker()
	bytes, rate, duration := tracker.GetProgress()

	if bytes == 0 {
		t.Error("No progress recorded")
	}

	t.Logf("Progress: %d bytes, %f bytes/sec, %v duration", bytes, rate, duration)

	// Connection should be alive initially
	if !conn.IsAlive() {
		t.Error("Connection should be alive with recent progress")
	}
}

func TestProgressAwareConnection_StallDetection(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockConn := &MockTCPConn{
		readBuffer: []byte{}, // Empty buffer to simulate stall
	}

	policy := DefaultTimeoutPolicy()
	policy.StallThreshold = 500 * time.Millisecond // Very short for testing
	policy.ProgressCheckInterval = 100 * time.Millisecond

	conn := NewProgressAwareConnection(mockConn, policy, logger)
	defer conn.Close()

	// Set to operation phase
	if err := conn.SetPhase(PhaseOperation); err != nil {
		t.Fatalf("Failed to set operation phase: %v", err)
	}

	// Wait for stall detection
	time.Sleep(1 * time.Second)

	// Connection should eventually be detected as stalled
	tracker := conn.GetProgressTracker()
	if !tracker.IsStalled() {
		t.Error("Connection should be detected as stalled")
	}
}

func TestConcurrencyController_OperationLimiting(t *testing.T) {
	logger := zaptest.NewLogger(t)
	metrics := NewMetricsCollector("test")

	config := &ConcurrencyConfig{
		MaxConcurrentClones: 2,
		MaxConcurrentPushes: 1,
		PerTenantLimit:      1,
		PerIPLimit:          1,
		QueueSize:           10,
	}

	controller := NewConcurrencyController(config, metrics, logger)
	defer controller.Stop()

	ctx := context.Background()

	// First clone should succeed
	err := controller.RequestOperation(ctx, OperationClone, "tenant1", "192.168.1.1")
	if err != nil {
		t.Errorf("First clone should succeed: %v", err)
	}

	// Second clone for same tenant should fail (per-tenant limit)
	err = controller.RequestOperation(ctx, OperationClone, "tenant1", "192.168.1.2")
	if err == nil {
		t.Error("Second clone for same tenant should fail due to per-tenant limit")
	}

	// Clone for different tenant should succeed
	err = controller.RequestOperation(ctx, OperationClone, "tenant2", "192.168.1.3")
	if err != nil {
		t.Errorf("Clone for different tenant should succeed: %v", err)
	}

	// Release operations
	controller.ReleaseOperation(OperationClone, "tenant1", "192.168.1.1")
	controller.ReleaseOperation(OperationClone, "tenant2", "192.168.1.3")
}

func TestAtomicPushHandler_RefUpdatePreparation(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// This is a basic test - in real scenario you'd need a proper git repository
	handler := NewAtomicPushHandler(nil, "test-op-123", logger)

	// Test ref update parsing
	refUpdates := []string{
		"0000000000000000000000000000000000000000 abcd1234567890abcd1234567890abcd12345678 refs/heads/main",
		"abcd1234567890abcd1234567890abcd12345678 efgh1234567890efgh1234567890efgh12345678 refs/heads/feature",
	}

	// This would fail without a real repository, but tests the parsing logic
	err := handler.PreparePushOperation(refUpdates)
	if err == nil {
		t.Error("Should fail without a real repository")
	}

	// Test the error handling
	if err := handler.HandlePushTimeout(); err != nil {
		t.Errorf("HandlePushTimeout should not fail: %v", err)
	}
}

func TestTimeoutPolicy_Defaults(t *testing.T) {
	policy := DefaultTimeoutPolicy()

	if policy.HandshakeTimeout <= 0 {
		t.Error("HandshakeTimeout should be positive")
	}

	if policy.AuthTimeout <= 0 {
		t.Error("AuthTimeout should be positive")
	}

	if policy.OperationIdleTimeout <= 0 {
		t.Error("OperationIdleTimeout should be positive")
	}

	if policy.OperationMaxTimeout <= 0 {
		t.Error("OperationMaxTimeout should be positive")
	}

	if policy.StallThreshold <= 0 {
		t.Error("StallThreshold should be positive")
	}

	// Verify relationships
	if policy.OperationIdleTimeout >= policy.OperationMaxTimeout {
		t.Error("OperationIdleTimeout should be less than OperationMaxTimeout")
	}

	if policy.StallThreshold >= policy.OperationIdleTimeout {
		t.Error("StallThreshold should be less than OperationIdleTimeout")
	}
}

func TestMovingAverage(t *testing.T) {
	ma := NewMovingAverage(3)

	// Test empty average
	if avg := ma.Average(); avg != 0 {
		t.Errorf("Empty average should be 0, got %f", avg)
	}

	// Add values
	ma.Add(10.0)
	if avg := ma.Average(); avg != 10.0 {
		t.Errorf("Single value average should be 10.0, got %f", avg)
	}

	ma.Add(20.0)
	if avg := ma.Average(); avg != 15.0 {
		t.Errorf("Two value average should be 15.0, got %f", avg)
	}

	ma.Add(30.0)
	if avg := ma.Average(); avg != 20.0 {
		t.Errorf("Three value average should be 20.0, got %f", avg)
	}

	// Test window overflow
	ma.Add(40.0)
	if avg := ma.Average(); avg != 30.0 { // (20+30+40)/3
		t.Errorf("Overflow average should be 30.0, got %f", avg)
	}
}

// Integration test that verifies the old timeout behavior fails
func TestCurrentTimeoutBehaviorFails(t *testing.T) {
	t.Skip("This test simulates the old behavior - would require actual TCP server")
	// This test would simulate a 40-second large repo clone
	// With old 30s absolute timeout, it should fail
	// This demonstrates the problem we're solving
}

// Integration test that verifies the new behavior succeeds
func TestProgressAwareTimeoutSucceeds(t *testing.T) {
	t.Skip("This test requires actual Git repository integration")
	// This test would simulate the same 40-second repo clone
	// With progress-aware timeout, it should succeed because there's continuous progress
	// This demonstrates the solution working
}

func TestPackObjectsPhaseHandling(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockConn := &MockTCPConn{}

	policy := DefaultTimeoutPolicy()
	policy.StallThreshold = 1 * time.Second

	conn := NewProgressAwareConnection(mockConn, policy, logger)
	defer conn.Close()

	tracker := conn.GetProgressTracker()

	// Normal phase - should detect stall quickly
	if tracker.IsStalled() {
		t.Error("Should not be stalled initially")
	}

	// Set pack-objects phase
	tracker.SetPackObjectsPhase(true)

	// Wait longer than normal stall threshold
	time.Sleep(1500 * time.Millisecond)

	// In pack-objects phase, should allow longer silence
	if tracker.IsStalled() {
		t.Error("Should not detect stall during pack-objects phase")
	}

	// Exit pack-objects phase
	tracker.SetPackObjectsPhase(false)

	// Now should detect stall more quickly
	time.Sleep(1500 * time.Millisecond)
	if !tracker.IsStalled() {
		t.Error("Should detect stall after exiting pack-objects phase")
	}
}
