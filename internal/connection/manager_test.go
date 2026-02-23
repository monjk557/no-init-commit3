package connection

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

func TestConnectionManager_ComponentIntegration(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockConn := &MockTCPConn{
		readBuffer: []byte("Hello, refactored world!"),
	}

	config := &ConnectionManagerConfig{
		Policy: &TimeoutPolicy{
			HandshakeTimeout:      2 * time.Second,
			AuthTimeout:           3 * time.Second,
			ProtocolNegTimeout:    4 * time.Second,
			OperationIdleTimeout:  10 * time.Second,
			OperationMaxTimeout:   60 * time.Second,
			StallThreshold:        5 * time.Second,
			ProgressCheckInterval: 1 * time.Second,
		},
		Logger: logger,
	}

	manager, err := NewConnectionManager(mockConn, config)
	if err != nil {
		t.Fatalf("Failed to create connection manager: %v", err)
	}
	defer manager.Close()

	// Test phase transitions
	phases := []ConnectionPhase{
		PhaseHandshake,
		PhaseAuth,
		PhaseProtocolNegotiation,
		PhaseOperation,
	}

	for _, phase := range phases {
		if err := manager.SetPhase(phase); err != nil {
			t.Errorf("Failed to set phase %v: %v", phase, err)
		}

		if currentPhase := manager.GetCurrentPhase(); currentPhase != phase {
			t.Errorf("Phase mismatch: expected %v, got %v", phase, currentPhase)
		}
	}

	// Test IO operations
	buffer := make([]byte, 10)
	n, err := manager.Read(buffer)
	if err != nil {
		t.Errorf("Failed to read: %v", err)
	}

	if n == 0 {
		t.Error("No bytes read")
	}

	// Test connection health
	if !manager.IsAlive() {
		t.Error("Manager should be alive after successful operations")
	}

	// Test metadata
	manager.SetConnectionInfo("owner", "repo", "user123", true)
	owner, repo, userID, protoV2 := manager.GetConnectionInfo()

	if owner != "owner" || repo != "repo" || userID != "user123" || !protoV2 {
		t.Errorf("Metadata mismatch: got (%s, %s, %s, %t)", owner, repo, userID, protoV2)
	}

	// Test progress tracking
	tracker := manager.GetProgressTracker()
	bytes, rate, duration := tracker.GetProgress()

	if bytes == 0 {
		t.Error("No progress recorded")
	}

	t.Logf("Progress: %d bytes, %f bytes/sec, %v duration", bytes, rate, duration)

	// Test health status
	status := manager.GetHealthStatus()
	if !status["alive"].(bool) {
		t.Error("Health status should show alive")
	}

	if status["repository"] != "owner/repo" {
		t.Errorf("Repository in health status incorrect: %v", status["repository"])
	}
}

func TestConnectionFactory_Creation(t *testing.T) {
	logger := zaptest.NewLogger(t)

	config := &ConnectionFactoryConfig{
		Logger: logger,
		DefaultPolicy: &TimeoutPolicy{
			HandshakeTimeout:      1 * time.Second,
			AuthTimeout:           2 * time.Second,
			ProtocolNegTimeout:    3 * time.Second,
			OperationIdleTimeout:  10 * time.Second,
			OperationMaxTimeout:   60 * time.Second,
			StallThreshold:        5 * time.Second,
			ProgressCheckInterval: 1 * time.Second,
		},
	}

	factory := NewConnectionFactory(config)

	mockConn := &MockTCPConn{
		readBuffer: []byte("Factory test data"),
	}

	// Test basic creation
	manager1, err := factory.CreateConnection(mockConn, nil)
	if err != nil {
		t.Fatalf("Failed to create connection via factory: %v", err)
	}
	defer manager1.Close()

	// Test creation with custom policy
	customPolicy := &TimeoutPolicy{
		HandshakeTimeout:      500 * time.Millisecond,
		AuthTimeout:           1 * time.Second,
		ProtocolNegTimeout:    2 * time.Second,
		OperationIdleTimeout:  5 * time.Second,
		OperationMaxTimeout:   30 * time.Second,
		StallThreshold:        2 * time.Second,
		ProgressCheckInterval: 500 * time.Millisecond,
	}

	manager2, err := factory.CreateConnection(mockConn, customPolicy)
	if err != nil {
		t.Fatalf("Failed to create connection with custom policy: %v", err)
	}
	defer manager2.Close()

	// Test context creation
	ctx := context.Background()
	manager3, err := factory.CreateConnectionWithContext(ctx, mockConn, nil)
	if err != nil {
		t.Fatalf("Failed to create connection with context: %v", err)
	}
	defer manager3.Close()

	// Test test connection creation
	testManager, err := factory.CreateTestConnection(mockConn)
	if err != nil {
		t.Fatalf("Failed to create test connection: %v", err)
	}
	defer testManager.Close()

	// Verify all managers are functional
	managers := []ConnectionManager{manager1, manager2, manager3, testManager}
	for i, manager := range managers {
		if !manager.IsAlive() {
			t.Errorf("Manager %d should be alive", i)
		}

		buffer := make([]byte, 5)
		if _, err := manager.Read(buffer); err != nil {
			t.Errorf("Manager %d failed to read: %v", i, err)
		}
	}
}

func TestConnectionManager_ErrorHandling(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Test creation with nil connection
	_, err := NewConnectionManager(nil, &ConnectionManagerConfig{Logger: logger})
	if err == nil {
		t.Error("Should fail with nil connection")
	}

	// Test with closed connection
	mockConn := &MockTCPConn{closed: true}
	manager, err := NewConnectionManager(mockConn, &ConnectionManagerConfig{Logger: logger})
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Operations should fail gracefully
	buffer := make([]byte, 10)
	_, err = manager.Read(buffer)
	if err == nil {
		t.Error("Read should fail on closed connection")
	}

	// Write to buffer (may succeed due to buffering)
	_, err = manager.Write([]byte("test"))

	// Flush should definitely fail on closed connection
	err = manager.Flush()
	if err == nil {
		t.Error("Flush should fail on closed connection")
	}

	manager.Close()
}

func TestConnectionManager_ConcurrentAccess(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockConn := &MockTCPConn{
		readBuffer: make([]byte, 1024), // Large buffer for concurrent access
	}

	manager, err := NewConnectionManager(mockConn, &ConnectionManagerConfig{Logger: logger})
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	defer manager.Close()

	// Test concurrent metadata access
	done := make(chan bool, 10)

	// Concurrent readers
	for i := 0; i < 5; i++ {
		go func(id int) {
			defer func() { done <- true }()
			for j := 0; j < 100; j++ {
				owner, repo, userID, protoV2 := manager.GetConnectionInfo()
				_ = owner
				_ = repo
				_ = userID
				_ = protoV2
			}
		}(i)
	}

	// Concurrent writers
	for i := 0; i < 5; i++ {
		go func(id int) {
			defer func() { done <- true }()
			for j := 0; j < 100; j++ {
				manager.SetConnectionInfo("owner", "repo", "user", true)
			}
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Concurrent access test timed out")
		}
	}
}

func TestConnectionManager_ProgressIntegration(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockConn := &MockTCPConn{
		readBuffer: make([]byte, 1024),
	}

	// Fill buffer with test data
	for i := range mockConn.readBuffer {
		mockConn.readBuffer[i] = byte(i % 256)
	}

	config := &ConnectionManagerConfig{
		Policy: &TimeoutPolicy{
			StallThreshold:        1 * time.Second,
			ProgressCheckInterval: 100 * time.Millisecond,
		},
		Logger: logger,
	}

	manager, err := NewConnectionManager(mockConn, config)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	defer manager.Close()

	// Set to operation phase for progress tracking
	if err := manager.SetPhase(PhaseOperation); err != nil {
		t.Fatalf("Failed to set operation phase: %v", err)
	}

	// Read data in chunks to generate progress
	buffer := make([]byte, 64)
	totalRead := 0

	for totalRead < len(mockConn.readBuffer) {
		n, err := manager.Read(buffer)
		if err != nil {
			break // Expected when we reach end of buffer
		}
		totalRead += n

		// Check progress
		tracker := manager.GetProgressTracker()
		bytes, _, _ := tracker.GetProgress()
		if bytes == 0 {
			t.Error("Progress should be recorded")
		}

		// Should not be stalled during active reading
		if tracker.IsStalled() {
			t.Error("Should not be stalled during active reading")
		}
	}

	t.Logf("Total bytes read through manager: %d", totalRead)

	// Wait a bit and check stall detection
	time.Sleep(1500 * time.Millisecond)

	tracker := manager.GetProgressTracker()
	if !tracker.IsStalled() {
		t.Error("Should be stalled after idle period")
	}
}

func BenchmarkConnectionManager_ReadWrite(b *testing.B) {
	logger := zaptest.NewLogger(b)
	mockConn := &MockTCPConn{
		readBuffer: make([]byte, 1024*1024), // 1MB buffer
	}

	manager, err := NewConnectionManager(mockConn, &ConnectionManagerConfig{Logger: logger})
	if err != nil {
		b.Fatalf("Failed to create manager: %v", err)
	}
	defer manager.Close()

	buffer := make([]byte, 1024)

	b.ResetTimer()
	b.SetBytes(int64(len(buffer)))

	for i := 0; i < b.N; i++ {
		mockConn.readPos = 0 // Reset position for each iteration
		_, err := manager.Read(buffer)
		if err != nil {
			b.Fatalf("Read failed: %v", err)
		}
	}
}
