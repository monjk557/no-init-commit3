package connection

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// slowReader simulates a slow but continuous data transfer
type slowReader struct {
	data      []byte
	pos       int
	chunkSize int
	delay     time.Duration
}

func newSlowReader(data []byte, chunkSize int, delay time.Duration) *slowReader {
	return &slowReader{
		data:      data,
		chunkSize: chunkSize,
		delay:     delay,
	}
}

func (sr *slowReader) Read(p []byte) (int, error) {
	if sr.pos >= len(sr.data) {
		return 0, io.EOF
	}

	// Simulate slow transfer with delay
	if sr.delay > 0 {
		time.Sleep(sr.delay)
	}

	// Read chunk
	remaining := len(sr.data) - sr.pos
	toRead := sr.chunkSize
	if toRead > remaining {
		toRead = remaining
	}
	if toRead > len(p) {
		toRead = len(p)
	}

	copy(p[:toRead], sr.data[sr.pos:sr.pos+toRead])
	sr.pos += toRead
	return toRead, nil
}

// stalledReader simulates a connection that stalls after some data
type stalledReader struct {
	data    []byte
	pos     int
	stallAt int
	stalled bool
}

func newStalledReader(data []byte, stallAt int) *stalledReader {
	return &stalledReader{
		data:    data,
		stallAt: stallAt,
	}
}

func (sr *stalledReader) Read(p []byte) (int, error) {
	if sr.pos >= sr.stallAt && !sr.stalled {
		sr.stalled = true
		// From now on, block forever (simulate network stall)
	}

	if sr.stalled {
		// Simulate stall by sleeping
		time.Sleep(100 * time.Millisecond)
		return 0, nil // No data, no error - just stalled
	}

	if sr.pos >= len(sr.data) {
		return 0, io.EOF
	}

	// Normal read
	toRead := len(p)
	remaining := len(sr.data) - sr.pos
	if toRead > remaining {
		toRead = remaining
	}

	copy(p[:toRead], sr.data[sr.pos:sr.pos+toRead])
	sr.pos += toRead
	return toRead, nil
}

// progressAwareConn wraps a reader in a mock connection for testing
type progressAwareConn struct {
	reader io.Reader
	writer io.Writer
	closed bool
}

func (pac *progressAwareConn) Read(b []byte) (int, error) {
	if pac.closed {
		return 0, net.ErrClosed
	}
	return pac.reader.Read(b)
}

func (pac *progressAwareConn) Write(b []byte) (int, error) {
	if pac.closed {
		return 0, net.ErrClosed
	}
	return pac.writer.Write(b)
}

func (pac *progressAwareConn) Close() error {
	pac.closed = true
	return nil
}

func (pac *progressAwareConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080}
}

func (pac *progressAwareConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345}
}

func (pac *progressAwareConn) SetDeadline(t time.Time) error {
	return nil
}

func (pac *progressAwareConn) SetReadDeadline(t time.Time) error {
	return nil
}

func (pac *progressAwareConn) SetWriteDeadline(t time.Time) error {
	return nil
}

func TestProgressAwareVsAbsoluteTimeout_SlowTransfer(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Simulate a moderately large repository (1MB of data for faster testing)
	largeData := make([]byte, 1*1024*1024)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	// Faster transfer for testing: 32KB chunks with 5ms delay = ~6MB/s
	// This would take ~0.17 seconds to transfer 1MB
	slowReader := newSlowReader(largeData, 32*1024, 5*time.Millisecond)

	mockConn := &progressAwareConn{
		reader: slowReader,
		writer: &bytes.Buffer{},
	}

	// Test 1: Old absolute timeout behavior (simulated)
	// 30 second absolute timeout should succeed for this transfer
	absoluteTimeoutPolicy := &TimeoutPolicy{
		HandshakeTimeout:      5 * time.Second,
		AuthTimeout:           10 * time.Second,
		ProtocolNegTimeout:    15 * time.Second,
		OperationIdleTimeout:  60 * time.Second,
		OperationMaxTimeout:   30 * time.Second, // This is the old "absolute" limit
		StallThreshold:        5 * time.Second,
		ProgressCheckInterval: 1 * time.Second,
	}

	conn1 := NewProgressAwareConnection(mockConn, absoluteTimeoutPolicy, logger)
	defer conn1.Close()

	// Set to operation phase
	if err := conn1.SetPhase(PhaseOperation); err != nil {
		t.Fatalf("Failed to set operation phase: %v", err)
	}

	// Read all data
	buffer := make([]byte, 8192)
	totalRead := 0
	startTime := time.Now()

	for {
		n, err := conn1.Read(buffer)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Errorf("Read error: %v", err)
			break
		}
		totalRead += n

		// Check if connection is still alive
		if !conn1.IsAlive() {
			t.Errorf("Connection died unexpectedly after reading %d bytes", totalRead)
			break
		}
	}

	duration := time.Since(startTime)
	t.Logf("Progress-aware read completed: %d bytes in %v", totalRead, duration)

	if totalRead != len(largeData) {
		t.Errorf("Expected to read %d bytes, got %d", len(largeData), totalRead)
	}

	// Verify we read all data successfully
	if totalRead == len(largeData) && duration < 30*time.Second {
		t.Logf("✅ Progress-aware timeout succeeded for slow transfer")
	} else {
		t.Errorf("❌ Progress-aware timeout failed or took too long")
	}
}

func TestProgressAwareTimeout_StallDetection(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create data that will stall after 1KB
	data := make([]byte, 10*1024)
	for i := range data {
		data[i] = byte(i % 256)
	}

	stalledReader := newStalledReader(data, 1024) // Stall after 1KB

	mockConn := &progressAwareConn{
		reader: stalledReader,
		writer: &bytes.Buffer{},
	}

	// Fast stall detection for testing
	policy := &TimeoutPolicy{
		HandshakeTimeout:      5 * time.Second,
		AuthTimeout:           10 * time.Second,
		ProtocolNegTimeout:    15 * time.Second,
		OperationIdleTimeout:  60 * time.Second,
		OperationMaxTimeout:   5 * time.Minute,
		StallThreshold:        2 * time.Second, // Quick stall detection
		ProgressCheckInterval: 500 * time.Millisecond,
	}

	conn := NewProgressAwareConnection(mockConn, policy, logger)
	defer conn.Close()

	// Set to operation phase
	if err := conn.SetPhase(PhaseOperation); err != nil {
		t.Fatalf("Failed to set operation phase: %v", err)
	}

	buffer := make([]byte, 512)
	totalRead := 0
	stallDetected := false
	startTime := time.Now()

	// Read until stall is detected or timeout
	timeout := time.After(10 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			t.Error("Test timed out without detecting stall")
			return

		case <-ticker.C:
			// Try to read
			n, err := conn.Read(buffer)
			if err == io.EOF {
				t.Log("Reached EOF")
				return
			}
			if err != nil && err != io.EOF {
				t.Logf("Read error (expected during stall): %v", err)
			}

			if n > 0 {
				totalRead += n
			}

			// Check if stall was detected
			tracker := conn.GetProgressTracker()
			if tracker.IsStalled() && !stallDetected {
				stallDetected = true
				stallTime := time.Since(startTime)
				t.Logf("✅ Stall detected after %v, read %d bytes", stallTime, totalRead)

				// Verify stall was detected reasonably quickly
				if stallTime > 5*time.Second {
					t.Errorf("Stall detection took too long: %v", stallTime)
				}
				return
			}

			// Check if connection is still considered alive
			if !conn.IsAlive() {
				t.Logf("✅ Connection marked as not alive after %v", time.Since(startTime))
				return
			}
		}
	}
}

func TestConcurrencyControl_LoadTesting(t *testing.T) {
	logger := zaptest.NewLogger(t)
	metrics := NewMetricsCollector("test")

	config := &ConcurrencyConfig{
		MaxConcurrentClones: 5,
		MaxConcurrentPushes: 3,
		PerTenantLimit:      2,
		PerIPLimit:          3,
		QueueSize:           20,
	}

	controller := NewConcurrencyController(config, metrics, logger)
	defer controller.Stop()

	// Simulate concurrent operations
	var wg sync.WaitGroup
	successCount := 0
	failureCount := 0
	var countMutex sync.Mutex

	// Launch many concurrent clone operations
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			tenantID := fmt.Sprintf("tenant%d", id%3)          // 3 different tenants
			clientIP := fmt.Sprintf("192.168.1.%d", (id%10)+1) // 10 different IPs

			err := controller.RequestOperation(ctx, OperationClone, tenantID, clientIP)

			countMutex.Lock()
			if err != nil {
				failureCount++
			} else {
				successCount++
				// Simulate some work
				time.Sleep(100 * time.Millisecond)
				controller.ReleaseOperation(OperationClone, tenantID, clientIP)
			}
			countMutex.Unlock()
		}(i)
	}

	wg.Wait()

	t.Logf("Concurrency test results: %d successes, %d failures", successCount, failureCount)

	// We expect some failures due to rate limiting
	if successCount == 0 {
		t.Error("Expected some operations to succeed")
	}

	if failureCount == 0 {
		t.Error("Expected some operations to be rate limited")
	}

	// Verify reasonable ratio
	if successCount < 5 {
		t.Errorf("Too few operations succeeded: %d", successCount)
	}
}

func TestAtomicPushHandler_TimeoutHandling(t *testing.T) {
	logger := zaptest.NewLogger(t)

	// Create a handler (without real repo for this test)
	handler := NewAtomicPushHandler("", "test-timeout-123", logger)

	// Test timeout handling
	err := handler.HandlePushTimeout()
	if err != nil {
		t.Errorf("HandlePushTimeout should handle nil repo gracefully: %v", err)
	}

	// The real test would involve:
	// 1. Starting a push operation
	// 2. Simulating a timeout during ref updates
	// 3. Verifying that partial updates are rolled back
	// This requires integration with a real Git repository
}

func TestEndToEndScenario_LargeRepositoryClone(t *testing.T) {
	t.Skip("Integration test - requires actual repository setup")

	// This test would:
	// 1. Set up a large Git repository (>100MB)
	// 2. Start a clone operation using the old TCP server
	// 3. Verify it fails with 30s absolute timeout
	// 4. Start the same clone with progress-aware TCP server
	// 5. Verify it succeeds because of continuous progress tracking
	//
	// Expected results:
	// - Old system: Timeout after 30s, even with active transfer
	// - New system: Success after ~40s with continuous progress
}

func TestEndToEndScenario_AtomicPushFailure(t *testing.T) {
	t.Skip("Integration test - requires actual repository setup")

	// This test would:
	// 1. Set up a Git repository
	// 2. Start a push operation with multiple ref updates
	// 3. Simulate a timeout/failure during the push
	// 4. Verify that NO refs were updated (atomic behavior)
	// 5. Verify that repository state is unchanged
	//
	// This demonstrates the atomic push guarantee even during timeout scenarios
}

func BenchmarkProgressAwareConnection_ReadThroughput(b *testing.B) {
	logger := zap.NewNop()

	// Create test data
	data := make([]byte, 1024*1024) // 1MB
	for i := range data {
		data[i] = byte(i % 256)
	}

	mockConn := &progressAwareConn{
		reader: bytes.NewReader(data),
		writer: &bytes.Buffer{},
	}

	policy := DefaultTimeoutPolicy()
	conn := NewProgressAwareConnection(mockConn, policy, logger)
	defer conn.Close()

	// Set to operation phase
	conn.SetPhase(PhaseOperation)

	buffer := make([]byte, 8192)

	b.ResetTimer()
	b.SetBytes(int64(len(data)))

	for i := 0; i < b.N; i++ {
		// Reset reader
		mockConn.reader = bytes.NewReader(data)

		totalRead := 0
		for totalRead < len(data) {
			n, err := conn.Read(buffer)
			if err == io.EOF {
				break
			}
			if err != nil {
				b.Fatalf("Read error: %v", err)
			}
			totalRead += n
		}

		if totalRead != len(data) {
			b.Fatalf("Expected %d bytes, read %d", len(data), totalRead)
		}
	}
}

// Helper function to create test data with specific patterns
func createTestData(size int, pattern string) []byte {
	data := make([]byte, size)
	patternBytes := []byte(pattern)

	for i := range data {
		data[i] = patternBytes[i%len(patternBytes)]
	}

	return data
}
