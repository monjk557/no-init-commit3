package performance

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamingJSONWriter_Object(t *testing.T) {
	s := NewZeroCopyJSONStreamer()
	var buf bytes.Buffer
	w := s.NewStreamingJSONWriter(&buf)
	defer w.Close()

	require.NoError(t, w.WriteObjectStart())
	require.NoError(t, w.WriteField("name", "alice"))
	require.NoError(t, w.WriteField("age", 30))
	require.NoError(t, w.WriteObjectEnd())

	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
	assert.Equal(t, "alice", out["name"])
	assert.Equal(t, float64(30), out["age"])
}

func TestStreamingJSONWriter_Array(t *testing.T) {
	s := NewZeroCopyJSONStreamer()
	var buf bytes.Buffer
	w := s.NewStreamingJSONWriter(&buf)
	defer w.Close()

	require.NoError(t, w.WriteArrayStart())
	require.NoError(t, w.WriteValue(1))
	require.NoError(t, w.WriteValue(2))
	require.NoError(t, w.WriteValue("three"))
	require.NoError(t, w.WriteArrayEnd())

	var out []interface{}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
	require.Len(t, out, 3)
	assert.Equal(t, float64(1), out[0])
	assert.Equal(t, "three", out[2])
}

func TestStreamingJSONWriter_RawField(t *testing.T) {
	s := NewZeroCopyJSONStreamer()
	var buf bytes.Buffer
	w := s.NewStreamingJSONWriter(&buf)

	require.NoError(t, w.WriteObjectStart())
	require.NoError(t, w.WriteField("a", 1))
	require.NoError(t, w.WriteRawField("b", []byte(`{"nested":true}`)))
	require.NoError(t, w.WriteObjectEnd())
	w.Close()

	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
	assert.Equal(t, float64(1), out["a"])
	nested, ok := out["b"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, true, nested["nested"])
}

func TestStreamingJSONWriter_NestedObjectsInArray(t *testing.T) {
	s := NewZeroCopyJSONStreamer()
	var buf bytes.Buffer
	w := s.NewStreamingJSONWriter(&buf)

	require.NoError(t, w.WriteArrayStart())
	require.NoError(t, w.WriteObjectStart())
	require.NoError(t, w.WriteField("k", "v"))
	require.NoError(t, w.WriteObjectEnd())
	require.NoError(t, w.WriteArrayEnd())
	w.Close()

	// Array of one object.
	assert.True(t, bytes.HasPrefix(buf.Bytes(), []byte("[")))
}

func TestOptimizedResponseWriter_WriteSuccessResponse(t *testing.T) {
	orw := NewOptimizedResponseWriter()

	t.Run("with payload", func(t *testing.T) {
		var buf bytes.Buffer
		payload := map[string]interface{}{"id": 42}
		require.NoError(t, orw.WriteSuccessResponse(&buf, payload, "ok"))

		var out map[string]interface{}
		require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
		assert.Equal(t, true, out["success"])
		assert.Equal(t, "ok", out["message"])
		data := out["data"].(map[string]interface{})
		assert.Equal(t, float64(42), data["id"])
		meta := out["meta"].(map[string]interface{})
		assert.Equal(t, "v1.0", meta["version"])
	})

	t.Run("nil payload omits data", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, orw.WriteSuccessResponse(&buf, nil, "empty"))
		var out map[string]interface{}
		require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
		_, hasData := out["data"]
		assert.False(t, hasData)
	})
}

func TestStreamingDiffResponseWriter(t *testing.T) {
	w := NewStreamingDiffResponseWriter()
	var buf bytes.Buffer

	files := []interface{}{
		map[string]interface{}{"path": "a.go"},
		map[string]interface{}{"path": "b.go"},
	}
	stats := map[string]interface{}{"additions": 10}

	require.NoError(t, w.StreamDiffResponse(&buf, "base123", "head456", files, stats))

	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
	assert.Equal(t, "base123", out["base"])
	assert.Equal(t, "head456", out["head"])
	filesOut := out["files"].([]interface{})
	require.Len(t, filesOut, 2)
}

func TestStreamingCommitsResponseWriter(t *testing.T) {
	w := NewStreamingCommitsResponseWriter()
	var buf bytes.Buffer

	groups := []interface{}{
		map[string]interface{}{"date": "2024-01-01"},
	}
	pagination := map[string]interface{}{"total": 1}

	require.NoError(t, w.StreamCommitsResponse(&buf, groups, pagination))

	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
	results := out["results"].([]interface{})
	require.Len(t, results, 1)
	pg := out["pagination"].(map[string]interface{})
	assert.Equal(t, float64(1), pg["total"])
}

func TestGlobalStreamingOptimizer(t *testing.T) {
	require.NotNil(t, GlobalStreamingOptimizer)
	var buf bytes.Buffer
	w := GlobalStreamingOptimizer.NewStreamingJSONWriter(&buf)
	require.NoError(t, w.WriteObjectStart())
	require.NoError(t, w.WriteField("ok", true))
	require.NoError(t, w.WriteObjectEnd())
	w.Close()
	assert.Contains(t, buf.String(), "ok")
}
