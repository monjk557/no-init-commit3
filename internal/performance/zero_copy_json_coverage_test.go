package performance

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failWriterAt is an io.Writer that returns an error on the failAt-th write
// (1-indexed) and succeeds on every other write. It lets tests drive the
// error-handling branches of the streaming writers at a precise point.
type failWriterAt struct {
	failAt int
	count  int
}

func (f *failWriterAt) Write(p []byte) (int, error) {
	f.count++
	if f.count == f.failAt {
		return 0, errors.New("simulated write failure")
	}
	return len(p), nil
}

// TestNewZeroCopyJSONStreamer_Pools exercises the lazily-invoked sync.Pool New
// constructors, which are otherwise never triggered by the happy path.
func TestNewZeroCopyJSONStreamer_Pools(t *testing.T) {
	s := NewZeroCopyJSONStreamer()
	require.NotNil(t, s)

	// Force the pool New closures to run.
	enc := s.encoderPool.Get()
	assert.NotNil(t, enc)
	buf := s.bufferPool.Get()
	assert.NotNil(t, buf)
	b, ok := buf.([]byte)
	require.True(t, ok)
	assert.Equal(t, 0, len(b))
	assert.Equal(t, 16384, cap(b))
}

// TestStreamingJSONWriter_PopScopeEmpty drives popScope with an empty scope
// stack (the neutral-reset branch) by closing one more container than was
// opened.
func TestStreamingJSONWriter_PopScopeEmpty(t *testing.T) {
	s := NewZeroCopyJSONStreamer()
	var buf bytes.Buffer
	w := s.NewStreamingJSONWriter(&buf)
	defer w.Close()

	require.NoError(t, w.WriteObjectStart())
	require.NoError(t, w.WriteObjectEnd()) // pops the pushed parent scope
	// Second end pops with an empty scope stack -> neutral reset branch.
	require.NoError(t, w.WriteObjectEnd())
	assert.False(t, w.inObject)
	assert.False(t, w.inArray)
	assert.False(t, w.first)
}

func TestWriteSuccessResponse_WriteErrors(t *testing.T) {
	orw := NewOptimizedResponseWriter()
	payload := map[string]interface{}{"id": 42, "name": "thing"}

	// Walk the failure point across all internal writes; each iteration covers
	// a different error-return branch. A large failAt exercises the happy path.
	for failAt := 1; failAt <= 25; failAt++ {
		fw := &failWriterAt{failAt: failAt}
		// Errors are expected; we only care about branch coverage / no panic.
		_ = orw.WriteSuccessResponse(fw, payload, "message")
	}

	// Sanity: happy path still produces valid output.
	var buf bytes.Buffer
	require.NoError(t, orw.WriteSuccessResponse(&buf, payload, "ok"))
	assert.Contains(t, buf.String(), `"success":true`)
}

func TestStreamDiffResponse_WriteErrors(t *testing.T) {
	w := NewStreamingDiffResponseWriter()
	files := []interface{}{
		map[string]interface{}{"path": "a.go"},
		map[string]interface{}{"path": "b.go"},
	}
	stats := map[string]interface{}{"additions": 3}

	for failAt := 1; failAt <= 25; failAt++ {
		fw := &failWriterAt{failAt: failAt}
		_ = w.StreamDiffResponse(fw, "base", "head", files, stats)
	}
}

func TestStreamCommitsResponse_WriteErrors(t *testing.T) {
	w := NewStreamingCommitsResponseWriter()
	groups := []interface{}{
		map[string]interface{}{"date": "2024-01-01"},
		map[string]interface{}{"date": "2024-01-02"},
	}
	pagination := map[string]interface{}{"total": 2}

	for failAt := 1; failAt <= 25; failAt++ {
		fw := &failWriterAt{failAt: failAt}
		_ = w.StreamCommitsResponse(fw, groups, pagination)
	}
}
