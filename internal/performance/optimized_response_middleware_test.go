package performance

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func runGin(handler gin.HandlerFunc) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	handler(c)
	return w
}

func TestNewOptimizedJSONResponseMiddleware(t *testing.T) {
	m := NewOptimizedJSONResponseMiddleware("v2.0")
	require.NotNil(t, m)
	assert.Equal(t, "v2.0", m.version)
}

func TestSendSuccessResponseOptimized_MapPayload(t *testing.T) {
	m := NewOptimizedJSONResponseMiddleware("v1")
	w := runGin(func(c *gin.Context) {
		c.Set("startTime", time.Now().Add(-time.Millisecond))
		c.Set("requestID", "req-99")
		m.SendSuccessResponseOptimized(c, map[string]interface{}{"count": 7}, "ok")
	})

	assert.Equal(t, http.StatusOK, w.Code)
	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	assert.Equal(t, true, out["success"])
	assert.Equal(t, "ok", out["message"])
	assert.Equal(t, float64(7), out["count"]) // flattened
	meta := out["meta"].(map[string]interface{})
	assert.Equal(t, "req-99", meta["request_id"])
}

func TestSendSuccessResponseOptimized_StructPayload(t *testing.T) {
	m := NewOptimizedJSONResponseMiddleware("v1")
	type item struct {
		Name string `json:"name"`
	}
	w := runGin(func(c *gin.Context) {
		m.SendSuccessResponseOptimized(c, item{Name: "thing"}, "ok")
	})

	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	data := out["data"].(map[string]interface{})
	assert.Equal(t, "thing", data["name"])
}

func TestSendSuccessResponseOptimized_NilPayload(t *testing.T) {
	m := NewOptimizedJSONResponseMiddleware("v1")
	w := runGin(func(c *gin.Context) {
		m.SendSuccessResponseOptimized(c, nil, "empty")
	})
	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	assert.Equal(t, "empty", out["message"])
	_, hasData := out["data"]
	assert.False(t, hasData)
}

func TestSendSuccessResponseWithPaginationOptimized(t *testing.T) {
	m := NewOptimizedJSONResponseMiddleware("v1")
	w := runGin(func(c *gin.Context) {
		m.SendSuccessResponseWithPaginationOptimized(c,
			[]string{"a", "b"},
			map[string]interface{}{"total": 2},
			"listed")
	})

	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	assert.Equal(t, true, out["success"])
	data := out["data"].([]interface{})
	assert.Len(t, data, 2)
	pg := out["pagination"].(map[string]interface{})
	assert.Equal(t, float64(2), pg["total"])
}

func TestSendSuccessResponseWithPaginationOptimized_NilFields(t *testing.T) {
	m := NewOptimizedJSONResponseMiddleware("v1")
	w := runGin(func(c *gin.Context) {
		m.SendSuccessResponseWithPaginationOptimized(c, nil, nil, "empty")
	})
	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	assert.Equal(t, "empty", out["message"])
}

func TestStreamingDiffResponseHandler(t *testing.T) {
	h := NewStreamingDiffResponseHandler("v1")
	require.NotNil(t, h)

	files := []interface{}{
		map[string]interface{}{"path": "a.go"},
	}
	w := runGin(func(c *gin.Context) {
		h.StreamDiffResponseOptimized(c, "base", "head",
			files,
			map[string]interface{}{"additions": 1},
			map[string]interface{}{"page": 1})
	})

	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	assert.Equal(t, true, out["success"])
	data := out["data"].(map[string]interface{})
	assert.Equal(t, "base", data["base"])
	assert.Equal(t, "head", data["head"])
	filesOut := data["files"].([]interface{})
	assert.Len(t, filesOut, 1)
	pg := out["pagination"].(map[string]interface{})
	assert.Equal(t, float64(1), pg["page"])
}

func TestStreamingDiffResponseHandler_NoPagination(t *testing.T) {
	h := NewStreamingDiffResponseHandler("v1")
	w := runGin(func(c *gin.Context) {
		h.StreamDiffResponseOptimized(c, "b", "h", nil,
			map[string]interface{}{"x": 1}, nil)
	})
	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	_, hasPg := out["pagination"]
	assert.False(t, hasPg)
}

func TestStreamingCommitsResponseHandler(t *testing.T) {
	h := NewStreamingCommitsResponseHandler("v1")
	require.NotNil(t, h)

	groups := []interface{}{
		map[string]interface{}{"date": "2024-01-01"},
	}
	w := runGin(func(c *gin.Context) {
		h.StreamCommitsResponseOptimized(c, groups, map[string]interface{}{"total": 1})
	})

	var out map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	assert.Equal(t, true, out["success"])
	results := out["results"].([]interface{})
	assert.Len(t, results, 1)
	pg := out["pagination"].(map[string]interface{})
	assert.Equal(t, float64(1), pg["total"])
}
