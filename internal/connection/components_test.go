package connection

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

// TestConnectionMetadata tests the metadata component in isolation
func TestConnectionMetadata_Isolation(t *testing.T) {
	metadata := NewConnectionMetadata()

	// Test initial state
	owner, repo, userID, protoV2 := metadata.GetConnectionInfo()
	if owner != "" || repo != "" || userID != "" || protoV2 {
		t.Error("Initial state should be empty")
	}

	// Test setting values
	metadata.SetConnectionInfo("test-owner", "test-repo", "user123", true)

	if metadata.GetOwner() != "test-owner" {
		t.Errorf("Owner mismatch: got %s", metadata.GetOwner())
	}

	if metadata.GetRepo() != "test-repo" {
		t.Errorf("Repo mismatch: got %s", metadata.GetRepo())
	}

	if metadata.GetUserID() != "user123" {
		t.Errorf("UserID mismatch: got %s", metadata.GetUserID())
	}

	if !metadata.IsProtoV2() {
		t.Error("ProtoV2 should be true")
	}

	// Test tenant ID generation
	if tenantID := metadata.(*connectionMetadata).GetTenantID(); tenantID != "test-owner/test-repo" {
		t.Errorf("Tenant ID mismatch: got %s", tenantID)
	}
}

func TestConnectionMetadata_ConcurrentAccess(t *testing.T) {
	metadata := NewConnectionMetadata()
	done := make(chan bool, 20)

	// Concurrent writers
	for i := 0; i < 10; i++ {
		go func(id int) {
			defer func() { done <- true }()
			for j := 0; j < 100; j++ {
				metadata.SetConnectionInfo("owner", "repo", "user", true)
			}
		}(i)
	}

	// Concurrent readers
	for i := 0; i < 10; i++ {
		go func(id int) {
			defer func() { done <- true }()
			for j := 0; j < 100; j++ {
				_, _, _, _ = metadata.GetConnectionInfo()
			}
		}(i)
	}

	// Wait for completion
	for i := 0; i < 20; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Concurrent access test timed out")
		}
	}
}

func TestIOWrapper_Isolation(t *testing.T) {
	mockConn := &MockTCPConn{
		readBuffer: []byte("Hello, IO Wrapper!"),
	}

	config := &IOWrapperConfig{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		ProgressCallback: func(bytes int64) {
			// Progress callback for testing
			if bytes <= 0 {
				t.Errorf("Progress callback received invalid bytes: %d", bytes)
			}
		},
	}

	wrapper := NewIOWrapper(mockConn, config)
	defer wrapper.Close()

	// Test read
	buffer := make([]byte, 10)
	n, err := wrapper.Read(buffer)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if n == 0 {
		t.Error("No bytes read")
	}

	// Test write
	writeData := []byte("test write")
	n, err = wrapper.Write(writeData)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != len(writeData) {
		t.Errorf("Write length mismatch: expected %d, got %d", len(writeData), n)
	}

	// Test flush
	if err := wrapper.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Test reader/writer access
	if reader := wrapper.GetReader(); reader == nil {
		t.Error("Reader should not be nil")
	}

	if writer := wrapper.GetWriter(); writer == nil {
		t.Error("Writer should not be nil")
	}

	// Test close
	if err := wrapper.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Operations should fail after close
	_, err = wrapper.Read(buffer)
	if err == nil {
		t.Error("Read should fail after close")
	}
}

func TestConnectionLifecycle_Isolation(t *testing.T) {
	mockConn := &MockTCPConn{}
	closeCallbackCalled := false

	config := &ConnectionLifecycleConfig{
		OnClose: func() {
			closeCallbackCalled = true
		},
	}

	lifecycle := NewConnectionLifecycle(mockConn, config)

	// Test initial state
	if !lifecycle.IsAlive() {
		t.Error("Should be alive initially")
	}

	if lifecycle.IsClosed() {
		t.Error("Should not be closed initially")
	}

	// Test remote address
	if addr := lifecycle.GetRemoteAddr(); addr == nil {
		t.Error("Remote address should not be nil")
	}

	// Test last used tracking
	lifecycle.UpdateLastUsed()
	lastUsed := lifecycle.(*connectionLifecycle).GetLastUsed()
	if time.Since(lastUsed) > time.Second {
		t.Error("Last used time should be recent")
	}

	// Test idle duration
	idleDuration := lifecycle.(*connectionLifecycle).GetIdleDuration()
	if idleDuration < 0 {
		t.Error("Idle duration should not be negative")
	}

	// Test close
	if err := lifecycle.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if lifecycle.IsAlive() {
		t.Error("Should not be alive after close")
	}

	if !lifecycle.IsClosed() {
		t.Error("Should be closed after close")
	}

	if !closeCallbackCalled {
		t.Error("Close callback should have been called")
	}

	// Double close should be safe
	if err := lifecycle.Close(); err != nil {
		t.Fatalf("Double close failed: %v", err)
	}
}

func TestTimeoutManager_Isolation(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockConn := &MockTCPConn{}

	policy := &TimeoutPolicy{
		HandshakeTimeout:      100 * time.Millisecond,
		AuthTimeout:           200 * time.Millisecond,
		ProtocolNegTimeout:    300 * time.Millisecond,
		OperationIdleTimeout:  500 * time.Millisecond,
		OperationMaxTimeout:   1 * time.Second,
		StallThreshold:        100 * time.Millisecond,
		ProgressCheckInterval: 50 * time.Millisecond,
	}

	// Create a mock progress tracker
	progressTracker := NewTransferProgressTracker(policy.StallThreshold)

	config := &TimeoutManagerConfig{
		Policy:          policy,
		ProgressTracker: progressTracker,
		Logger:          logger,
	}

	timeoutManager := NewTimeoutManager(mockConn, config)
	defer timeoutManager.Stop()

	// Test initial phase
	if phase := timeoutManager.GetCurrentPhase(); phase != PhaseHandshake {
		t.Errorf("Initial phase should be handshake, got %v", phase)
	}

	// Test phase transitions
	ctx := context.Background()
	phases := []ConnectionPhase{PhaseAuth, PhaseProtocolNegotiation, PhaseOperation}

	for _, phase := range phases {
		if err := timeoutManager.SetPhase(ctx, phase); err != nil {
			t.Errorf("Failed to set phase %v: %v", phase, err)
		}

		if currentPhase := timeoutManager.GetCurrentPhase(); currentPhase != phase {
			t.Errorf("Phase mismatch: expected %v, got %v", phase, currentPhase)
		}
	}

	// Test timeout checking
	if err := timeoutManager.IsWithinLimits(ctx); err != nil {
		t.Errorf("Should be within limits initially: %v", err)
	}

	// Test stop
	timeoutManager.Stop()

	// Operations should fail after stop
	if err := timeoutManager.SetPhase(ctx, PhaseHandshake); err == nil {
		t.Error("SetPhase should fail after stop")
	}
}

func TestTimeoutManager_TimeoutDetection(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockConn := &MockTCPConn{}

	policy := &TimeoutPolicy{
		HandshakeTimeout:      50 * time.Millisecond, // Very short for testing
		AuthTimeout:           50 * time.Millisecond,
		ProtocolNegTimeout:    50 * time.Millisecond,
		OperationIdleTimeout:  100 * time.Millisecond,
		OperationMaxTimeout:   100 * time.Millisecond,
		StallThreshold:        50 * time.Millisecond,
		ProgressCheckInterval: 10 * time.Millisecond,
	}

	progressTracker := NewTransferProgressTracker(policy.StallThreshold)

	config := &TimeoutManagerConfig{
		Policy:          policy,
		ProgressTracker: progressTracker,
		Logger:          logger,
	}

	timeoutManager := NewTimeoutManager(mockConn, config)
	defer timeoutManager.Stop()

	ctx := context.Background()

	// Test handshake timeout
	// Wait longer than handshake timeout
	time.Sleep(100 * time.Millisecond)

	if err := timeoutManager.IsWithinLimits(ctx); err == nil {
		t.Error("Should timeout during handshake phase")
	} else {
		if timeoutErr, ok := err.(*TimeoutError); ok {
			if timeoutErr.Type != "handshake_timeout" {
				t.Errorf("Expected handshake_timeout, got %s", timeoutErr.Type)
			}
		} else {
			t.Errorf("Expected TimeoutError, got %T", err)
		}
	}

	// Test operation phase stall detection
	if err := timeoutManager.SetPhase(ctx, PhaseOperation); err != nil {
		t.Fatalf("Failed to set operation phase: %v", err)
	}

	// Wait for stall detection
	time.Sleep(100 * time.Millisecond)

	if err := timeoutManager.IsWithinLimits(ctx); err == nil {
		t.Error("Should detect stall during operation phase")
	} else {
		if timeoutErr, ok := err.(*TimeoutError); ok {
			if timeoutErr.Type != "operation_stalled" {
				t.Errorf("Expected operation_stalled, got %s", timeoutErr.Type)
			}
		} else if !IsTimeoutError(err) {
			t.Errorf("Expected timeout error, got %v", err)
		}
	}
}

func TestProgressTracker_StallDetection(t *testing.T) {
	stallThreshold := 100 * time.Millisecond
	tracker := NewTransferProgressTracker(stallThreshold)

	// Should not be stalled initially
	if tracker.IsStalled() {
		t.Error("Should not be stalled initially")
	}

	// Record some progress
	tracker.RecordProgress(100)
	if tracker.IsStalled() {
		t.Error("Should not be stalled after progress")
	}

	// Wait for stall threshold
	time.Sleep(150 * time.Millisecond)

	// Should be stalled now
	if !tracker.IsStalled() {
		t.Error("Should be stalled after threshold")
	}

	// Record more progress
	tracker.RecordProgress(50)
	if tracker.IsStalled() {
		t.Error("Should not be stalled after new progress")
	}
}

func TestProgressTracker_PackObjectsPhase(t *testing.T) {
	stallThreshold := 50 * time.Millisecond
	tracker := NewTransferProgressTracker(stallThreshold)

	// Normal phase - should detect stall quickly
	time.Sleep(100 * time.Millisecond)
	if !tracker.IsStalled() {
		t.Error("Should detect stall in normal phase")
	}

	// Reset and test pack objects phase
	tracker.RecordProgress(100)
	tracker.SetPackObjectsPhase(true)

	// Wait longer than normal stall threshold
	time.Sleep(100 * time.Millisecond)

	// Should not be stalled in pack objects phase
	if tracker.IsStalled() {
		t.Error("Should not detect stall during pack objects phase")
	}

	// Exit pack objects phase
	tracker.SetPackObjectsPhase(false)

	// Wait and should detect stall again
	time.Sleep(100 * time.Millisecond)
	if !tracker.IsStalled() {
		t.Error("Should detect stall after exiting pack objects phase")
	}
}

func BenchmarkConnectionMetadata_GetConnectionInfo(b *testing.B) {
	metadata := NewConnectionMetadata()
	metadata.SetConnectionInfo("owner", "repo", "user", true)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = metadata.GetConnectionInfo()
	}
}

func BenchmarkIOWrapper_Read(b *testing.B) {
	mockConn := &MockTCPConn{
		readBuffer: make([]byte, 1024*1024), // 1MB buffer
	}

	wrapper := NewIOWrapper(mockConn, DefaultIOWrapperConfig())
	defer wrapper.Close()

	buffer := make([]byte, 1024)

	b.ResetTimer()
	b.SetBytes(int64(len(buffer)))

	for i := 0; i < b.N; i++ {
		mockConn.readPos = 0 // Reset position
		_, err := wrapper.Read(buffer)
		if err != nil {
			b.Fatalf("Read failed: %v", err)
		}
	}
}
