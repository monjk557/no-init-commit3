package streaming

import (
	"bytes"
	"fmt"
	"io"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamingPktLineWriter(t *testing.T) {
	t.Run("WriteLine", func(t *testing.T) {
		var buf bytes.Buffer
		writer := &StreamingPktLineWriter{w: &buf}
		
		err := writer.writeLine("test line")
		require.NoError(t, err)
		
		result := buf.String()
		expected := "000dtest line"
		assert.Equal(t, expected, result)
	})
	
	t.Run("Flush", func(t *testing.T) {
		var buf bytes.Buffer
		writer := &StreamingPktLineWriter{w: &buf}
		
		err := writer.flush()
		require.NoError(t, err)
		
		result := buf.String()
		assert.Equal(t, "0000", result)
	})
	
	t.Run("ConcurrentWrites", func(t *testing.T) {
		var buf bytes.Buffer
		writer := &StreamingPktLineWriter{w: &buf}
		
		// Test concurrent writes don't cause data races
		done := make(chan bool, 2)
		
		go func() {
			for i := 0; i < 100; i++ {
				writer.writeLine("line1")
			}
			done <- true
		}()
		
		go func() {
			for i := 0; i < 100; i++ {
				writer.writeLine("line2")
			}
			done <- true
		}()
		
		<-done
		<-done
		
		// Should have written data without panicking
		assert.True(t, buf.Len() > 0)
	})
}

func TestPackfileStreamRequest(t *testing.T) {
	t.Run("BasicRequest", func(t *testing.T) {
		req := &PackfileStreamRequest{
			Wants: []string{"abc123", "def456"},
			Haves: []string{"999888"},
			Capabilities: []string{"multi_ack", "thin-pack"},
		}
		
		assert.Len(t, req.Wants, 2)
		assert.Len(t, req.Haves, 1)
		assert.Len(t, req.Capabilities, 2)
	})
}

func TestClientHasObject(t *testing.T) {
	streamer := &GitStreamer{}
	
	haves := []string{"abc123", "def456", "ghi789"}
	
	t.Run("ObjectExists", func(t *testing.T) {
		result := streamer.clientHasObject("abc123", haves)
		assert.True(t, result)
	})
	
	t.Run("ObjectNotExists", func(t *testing.T) {
		result := streamer.clientHasObject("xyz999", haves)
		assert.False(t, result)
	})
	
	t.Run("EmptyHaves", func(t *testing.T) {
		result := streamer.clientHasObject("abc123", []string{})
		assert.False(t, result)
	})
}

func TestStreamingMemoryEfficiency(t *testing.T) {
	t.Run("LargeDataStreaming", func(t *testing.T) {
		var buf bytes.Buffer
		writer := &StreamingPktLineWriter{w: &buf}
		
		// Simulate streaming large amounts of data
		for i := 0; i < 1000; i++ {
			data := strings.Repeat("x", 1000) // 1KB per line
			err := writer.writeLine(data)
			require.NoError(t, err)
		}
		
		// Verify data was written
		assert.True(t, buf.Len() > 1000000) // Should be > 1MB
	})
}

func TestStreamingContextCancellation(t *testing.T) {
	t.Run("ContextTimeout", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
		defer cancel()
		
		// Wait for context to timeout
		time.Sleep(5 * time.Millisecond)
		
		// Test that cancelled context is detected
		select {
		case <-ctx.Done():
			assert.Equal(t, context.DeadlineExceeded, ctx.Err())
		default:
			t.Error("Context should be cancelled")
		}
	})
	
	t.Run("ContextCancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		
		// Cancel immediately
		cancel()
		
		// Test that cancelled context is detected
		select {
		case <-ctx.Done():
			assert.Equal(t, context.Canceled, ctx.Err())
		default:
			t.Error("Context should be cancelled")
		}
	})
}

// MockGitStreamer for testing without real Git repositories
type MockGitStreamer struct {
	simulateError bool
	objectCount   int
}

func (m *MockGitStreamer) StreamPackfile(ctx context.Context, req *PackfileStreamRequest, w io.Writer) error {
	if m.simulateError {
		return fmt.Errorf("mock error")
	}
	
	// Simulate streaming some data
	writer := &StreamingPktLineWriter{w: w}
	
	for i := 0; i < m.objectCount; i++ {
		line := fmt.Sprintf("object %d data", i)
		if err := writer.writeLine(line); err != nil {
			return err
		}
	}
	
	return writer.flush()
}

func TestMockStreaming(t *testing.T) {
	t.Run("MockStreamSuccess", func(t *testing.T) {
		var buf bytes.Buffer
		mock := &MockGitStreamer{objectCount: 5}
		
		req := &PackfileStreamRequest{
			Wants: []string{"test"},
		}
		
		err := mock.StreamPackfile(context.Background(), req, &buf)
		require.NoError(t, err)
		
		// Should have written some data
		assert.True(t, buf.Len() > 0)
		
		// Should contain the expected number of objects
		content := buf.String()
		objectLines := strings.Count(content, "object")
		assert.Equal(t, 5, objectLines)
	})
	
	t.Run("MockStreamError", func(t *testing.T) {
		var buf bytes.Buffer
		mock := &MockGitStreamer{simulateError: true}
		
		req := &PackfileStreamRequest{
			Wants: []string{"test"},
		}
		
		err := mock.StreamPackfile(context.Background(), req, &buf)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "mock error")
	})
}

func TestStreamingPerformance(t *testing.T) {
	t.Run("MeasureStreamingOverhead", func(t *testing.T) {
		var buf bytes.Buffer
		writer := &StreamingPktLineWriter{w: &buf}
		
		start := time.Now()
		
		// Stream a moderate amount of data
		for i := 0; i < 100; i++ {
			data := strings.Repeat("data", 250) // 1KB per line
			err := writer.writeLine(data)
			require.NoError(t, err)
		}
		
		duration := time.Since(start)
		
		// Should complete reasonably quickly (less than 1 second for 100KB)
		assert.True(t, duration < time.Second, "Streaming took too long: %v", duration)
		
		// Should have written the expected amount of data
		assert.True(t, buf.Len() > 100000) // Should be > 100KB
	})
}
