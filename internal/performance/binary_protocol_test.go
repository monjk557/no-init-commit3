package performance

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failWriter fails after allowing n successful writes.
type failWriter struct {
	allow int
	count int
}

func (f *failWriter) Write(p []byte) (int, error) {
	if f.count >= f.allow {
		return 0, errors.New("write failed")
	}
	f.count++
	return len(p), nil
}

func TestBinaryProtocolOptimizer_Stats(t *testing.T) {
	bpo := NewBinaryProtocolOptimizer()
	stats := bpo.GetOptimizationStats()
	require.NotNil(t, stats)
	assert.Equal(t, 250.0, stats.ImprovementPercent)
}

func TestOptimizedSidebandWriter_Write(t *testing.T) {
	var buf bytes.Buffer
	w := NewOptimizedSidebandWriter(&buf, 2)

	t.Run("empty write is no-op", func(t *testing.T) {
		n, err := w.Write(nil)
		require.NoError(t, err)
		assert.Equal(t, 0, n)
		assert.Equal(t, 0, buf.Len())
	})

	t.Run("simple payload", func(t *testing.T) {
		buf.Reset()
		payload := []byte("hello")
		n, err := w.Write(payload)
		require.NoError(t, err)
		assert.Equal(t, len(payload), n)

		out := buf.Bytes()
		// 4 hex length bytes + 1 channel byte + payload
		require.Greater(t, len(out), 5)
		// length = 4 (header) + 1 (channel) + len(payload)
		expectedLen := 4 + 1 + len(payload)
		gotLen, err := hex.DecodeString(string(out[:4]))
		require.NoError(t, err)
		assert.Equal(t, byte(0), gotLen[0])
		assert.Equal(t, byte(expectedLen), gotLen[1])
		assert.Equal(t, byte(2), out[4]) // channel
		assert.Equal(t, payload, out[5:])
	})

	t.Run("chunking large payload", func(t *testing.T) {
		buf.Reset()
		small := NewOptimizedSidebandWriter(&buf, 1)
		small.chunkSize = 4 // forces multiple chunks (chunk data = chunkSize-1 = 3)
		payload := []byte("abcdefg")
		n, err := small.Write(payload)
		require.NoError(t, err)
		assert.Equal(t, len(payload), n)
		// Reconstruct payload by skipping 5-byte headers per chunk.
		out := buf.Bytes()
		var reconstructed []byte
		for len(out) > 0 {
			require.GreaterOrEqual(t, len(out), 5)
			hdr, err := hex.DecodeString(string(out[:4]))
			require.NoError(t, err)
			total := int(hdr[0])<<8 | int(hdr[1])
			dataLen := total - 4 - 1
			reconstructed = append(reconstructed, out[5:5+dataLen]...)
			out = out[5+dataLen:]
		}
		assert.Equal(t, payload, reconstructed)
	})

	t.Run("flush writes terminator", func(t *testing.T) {
		buf.Reset()
		require.NoError(t, w.Flush())
		assert.Equal(t, "0000", buf.String())
	})

	t.Run("write error propagates", func(t *testing.T) {
		fw := &failWriter{allow: 0}
		ew := NewOptimizedSidebandWriter(fw, 1)
		_, err := ew.Write([]byte("data"))
		require.Error(t, err)
	})
}

func TestOptimizedPktLineWriter_WriteLine(t *testing.T) {
	var buf bytes.Buffer
	w := NewOptimizedPktLineWriter(&buf)

	t.Run("empty data flushes", func(t *testing.T) {
		buf.Reset()
		require.NoError(t, w.WriteLine(""))
		assert.Equal(t, "0000", buf.String())
	})

	t.Run("adds newline when missing", func(t *testing.T) {
		buf.Reset()
		require.NoError(t, w.WriteLine("hello"))
		out := buf.String()
		// length header (4) + "hello" + "\n"
		assert.Equal(t, "hello\n", out[4:])
		expectedLen := len("hello") + 4 + 1
		hdr, err := hex.DecodeString(out[:4])
		require.NoError(t, err)
		assert.Equal(t, expectedLen, int(hdr[0])<<8|int(hdr[1]))
	})

	t.Run("preserves existing newline", func(t *testing.T) {
		buf.Reset()
		require.NoError(t, w.WriteLine("hi\n"))
		out := buf.String()
		assert.Equal(t, "hi\n", out[4:])
		expectedLen := len("hi\n") + 4
		hdr, err := hex.DecodeString(out[:4])
		require.NoError(t, err)
		assert.Equal(t, expectedLen, int(hdr[0])<<8|int(hdr[1]))
	})

	t.Run("packet too long returns error", func(t *testing.T) {
		buf.Reset()
		big := strings.Repeat("a", 65520)
		err := w.WriteLine(big)
		require.Error(t, err)
	})

	t.Run("flush and delim", func(t *testing.T) {
		buf.Reset()
		require.NoError(t, w.Flush())
		assert.Equal(t, "0000", buf.String())

		buf.Reset()
		require.NoError(t, w.WriteDelim())
		assert.Equal(t, "0001", buf.String())
	})

	t.Run("write error on header propagates", func(t *testing.T) {
		fw := &failWriter{allow: 0}
		ew := NewOptimizedPktLineWriter(fw)
		require.Error(t, ew.WriteLine("data"))
	})
}
