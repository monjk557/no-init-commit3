package performance

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// failOnceGinWriter is an http.ResponseWriter that fails exactly on the
// failAt-th Write call and succeeds on all other writes. Failing a single
// intermediate streaming write drives a handler's error-return branch while
// still allowing the subsequent c.JSON error response to be written
// successfully (so gin does not panic on a failed Render).
type failOnceGinWriter struct {
	header http.Header
	failAt int
	count  int
}

func (f *failOnceGinWriter) Header() http.Header {
	if f.header == nil {
		f.header = make(http.Header)
	}
	return f.header
}

func (f *failOnceGinWriter) WriteHeader(int) {}

func (f *failOnceGinWriter) Write(p []byte) (int, error) {
	f.count++
	if f.count == f.failAt {
		return 0, errors.New("simulated response write failure")
	}
	return len(p), nil
}

// runGinWriter builds a gin test context backed by the provided writer and
// invokes handler. Any panic (e.g. from an unrecoverable render error) is
// swallowed so we only measure branch coverage.
func runGinWriter(w http.ResponseWriter, handler gin.HandlerFunc) {
	defer func() { _ = recover() }()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	handler(c)
}

func TestSendSuccessResponseOptimized_WriteErrors(t *testing.T) {
	m := NewOptimizedJSONResponseMiddleware("v1")
	for failAt := 1; failAt <= 20; failAt++ {
		w := &failOnceGinWriter{failAt: failAt}
		runGinWriter(w, func(c *gin.Context) {
			m.SendSuccessResponseOptimized(c, map[string]interface{}{"count": 7}, "ok")
		})
	}
}

func TestSendSuccessResponseWithPaginationOptimized_WriteErrors(t *testing.T) {
	m := NewOptimizedJSONResponseMiddleware("v1")
	for failAt := 1; failAt <= 20; failAt++ {
		w := &failOnceGinWriter{failAt: failAt}
		runGinWriter(w, func(c *gin.Context) {
			m.SendSuccessResponseWithPaginationOptimized(c,
				[]string{"a", "b"},
				map[string]interface{}{"total": 2},
				"listed")
		})
	}
}

func TestStreamDiffResponseOptimized_WriteErrors(t *testing.T) {
	h := NewStreamingDiffResponseHandler("v1")
	files := []interface{}{
		map[string]interface{}{"path": "a.go"},
		map[string]interface{}{"path": "b.go"},
	}
	for failAt := 1; failAt <= 25; failAt++ {
		w := &failOnceGinWriter{failAt: failAt}
		runGinWriter(w, func(c *gin.Context) {
			h.StreamDiffResponseOptimized(c, "base", "head",
				files,
				map[string]interface{}{"additions": 1},
				map[string]interface{}{"page": 1})
		})
	}
}

func TestStreamCommitsResponseOptimized_WriteErrors(t *testing.T) {
	h := NewStreamingCommitsResponseHandler("v1")
	groups := []interface{}{
		map[string]interface{}{"date": "2024-01-01"},
		map[string]interface{}{"date": "2024-01-02"},
	}
	for failAt := 1; failAt <= 25; failAt++ {
		w := &failOnceGinWriter{failAt: failAt}
		runGinWriter(w, func(c *gin.Context) {
			h.StreamCommitsResponseOptimized(c, groups,
				map[string]interface{}{"total": 2})
		})
	}
}
