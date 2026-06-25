package performance

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamingJSONOptimizer_SuccessResponse(t *testing.T) {
	sjo := NewStreamingJSONOptimizer()
	meta := map[string]interface{}{"request_id": "r1"}

	t.Run("map payload is flattened", func(t *testing.T) {
		var buf bytes.Buffer
		payload := map[string]interface{}{"count": 5}
		require.NoError(t, sjo.WriteOptimizedSuccessResponse(&buf, payload, "ok", meta))

		var out map[string]interface{}
		require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
		assert.Equal(t, true, out["success"])
		assert.Equal(t, "ok", out["message"])
		assert.Equal(t, float64(5), out["count"]) // flattened, not nested under data
		_, hasData := out["data"]
		assert.False(t, hasData)
	})

	t.Run("non-map payload wrapped in data", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, sjo.WriteOptimizedSuccessResponse(&buf, []int{1, 2, 3}, "ok", meta))
		var out map[string]interface{}
		require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
		data := out["data"].([]interface{})
		require.Len(t, data, 3)
	})

	t.Run("nil payload", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, sjo.WriteOptimizedSuccessResponse(&buf, nil, "ok", meta))
		var out map[string]interface{}
		require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
		_, hasData := out["data"]
		assert.False(t, hasData)
	})
}

func TestStreamingJSONOptimizer_PaginatedResponse(t *testing.T) {
	sjo := NewStreamingJSONOptimizer()
	var buf bytes.Buffer
	pagination := map[string]interface{}{"page": 1, "total": 100}
	require.NoError(t, sjo.WriteOptimizedPaginatedResponse(&buf, []string{"x"}, pagination, "ok"))

	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
	assert.Equal(t, true, out["success"])
	pg := out["pagination"].(map[string]interface{})
	assert.Equal(t, float64(1), pg["page"])
}

func TestDiscardWriter(t *testing.T) {
	var dw discardWriter
	n, err := dw.Write([]byte("anything"))
	require.NoError(t, err)
	assert.Equal(t, len("anything"), n)
}

func TestStreamingResponseComparison_Benchmarks(t *testing.T) {
	src := NewStreamingResponseComparison()
	payload := map[string]interface{}{"data": "value"}

	trad := src.BenchmarkTraditionalResponse(payload, "msg", 5)
	opt := src.BenchmarkOptimizedResponse(payload, "msg", 5)

	assert.GreaterOrEqual(t, int64(trad), int64(0))
	assert.GreaterOrEqual(t, int64(opt), int64(0))

	// Nil payload path for the traditional benchmark.
	tradNil := src.BenchmarkTraditionalResponse(nil, "msg", 2)
	assert.GreaterOrEqual(t, int64(tradNil), int64(0))
}

func TestPerformanceAnalytics_Analyze(t *testing.T) {
	pa := NewPerformanceAnalytics()
	payload := map[string]interface{}{"key": "value", "n": 1}

	result := pa.AnalyzeResponseOptimization(payload, 10)
	assert.Equal(t, 10, result.Iterations)
	assert.Greater(t, result.PayloadSize, 0)
	assert.NotEmpty(t, result.GetOptimizationImpact())
}

func TestEstimatePayloadSize(t *testing.T) {
	assert.Equal(t, 0, estimatePayloadSize(nil))
	size := estimatePayloadSize(map[string]string{"a": "b"})
	assert.Greater(t, size, 0)

	// Unmarshalable value -> 0.
	assert.Equal(t, 0, estimatePayloadSize(make(chan int)))
}

func TestResponseOptimizationResult_Impact(t *testing.T) {
	tests := []struct {
		pct  float64
		want string
	}{
		{70, "Excellent - Major performance gain"},
		{40, "Good - Significant improvement"},
		{20, "Moderate - Noticeable improvement"},
		{5, "Minor - Small improvement"},
		{0, "No improvement - Needs optimization"},
		{-10, "No improvement - Needs optimization"},
	}
	for _, tt := range tests {
		r := &ResponseOptimizationResult{ImprovementPercent: tt.pct}
		assert.Equal(t, tt.want, r.GetOptimizationImpact())
	}
}
