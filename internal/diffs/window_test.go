package diffs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWindowLineChanges 는 LineChange 인덱스 윈도잉(Issue-3159)의 슬라이싱·메타데이터·
// mid-hunk 합성 HUNK 헤더 동작을 검증한다.
func TestWindowLineChanges(t *testing.T) {
	t.Parallel()

	// HUNK, CONTEXT(1/1), DEL(2), ADD(2), CONTEXT(3/3)
	all := []LineChange{
		{Type: LineChangeTypeHunk, Left: intPtr(1), Right: intPtr(1), BlobLineNumber: intPtr(1), Text: "@@ -1 +1 @@"},
		{Type: LineChangeTypeContext, Left: intPtr(1), Right: intPtr(1), BlobLineNumber: intPtr(1), Text: "ctx"},
		{Type: LineChangeTypeDel, Left: intPtr(2), BlobLineNumber: intPtr(2), Text: "old"},
		{Type: LineChangeTypeAdd, Right: intPtr(2), Text: "new"},
		{Type: LineChangeTypeContext, Left: intPtr(3), Right: intPtr(3), BlobLineNumber: intPtr(3), Text: "ctx2"},
	}

	t.Run("window starts at hunk header — no synthetic header", func(t *testing.T) {
		out, meta := windowLineChanges(all, 0, 2)
		assert.Len(t, out, 2)
		assert.Equal(t, LineChangeTypeHunk, out[0].Type)
		assert.Equal(t, 0, meta.Offset)
		assert.Equal(t, 2, meta.Limit)
		assert.Equal(t, 2, meta.Returned)
		assert.Equal(t, 5, meta.TotalLines)
		assert.True(t, meta.HasMore)
	})

	t.Run("window starts mid-hunk — synthetic header prepended", func(t *testing.T) {
		out, meta := windowLineChanges(all, 2, 2)
		// 합성 HUNK 헤더 1개 + window 2개
		require.Len(t, out, 3)
		assert.Equal(t, LineChangeTypeHunk, out[0].Type)
		require.NotNil(t, out[0].Left)
		assert.Equal(t, 2, *out[0].Left, "synthetic header old line == 첫 DEL 라인의 left")
		// Returned 회계에는 합성 헤더 미포함 → 다음 윈도우는 offset+Returned 로 이어받음
		assert.Equal(t, 2, meta.Returned)
		assert.Equal(t, 2, meta.Offset)
		assert.True(t, meta.HasMore)
	})

	t.Run("limit<=0 returns full remainder", func(t *testing.T) {
		out, meta := windowLineChanges(all, 0, 0)
		assert.Len(t, out, 5)
		assert.Equal(t, 5, meta.Returned)
		assert.False(t, meta.HasMore)
	})

	t.Run("offset past end is clamped", func(t *testing.T) {
		out, meta := windowLineChanges(all, 999, 10)
		assert.Empty(t, out)
		assert.Equal(t, 0, meta.Returned)
		assert.False(t, meta.HasMore)
		assert.Equal(t, 5, meta.Offset, "offset 는 total 로 clamp")
	})

	t.Run("last window has_more false", func(t *testing.T) {
		_, meta := windowLineChanges(all, 4, 10)
		assert.Equal(t, 1, meta.Returned)
		assert.False(t, meta.HasMore)
	})
}

// TestFileDiff_WindowedLargeFile 는 /diff/file 이 MaxChangedLines 초과 파일을
// too_large dead-end 대신 윈도우로 부분 로드하는지 검증한다 (Issue-3159 회귀).
func TestFileDiff_WindowedLargeFile(t *testing.T) {
	repoPath, baseSHA, headSHA, cleanup := setupTestRepositoryForLargeChanges(t)
	defer cleanup()

	// repoPath = <tempDir>/testowner/testrepo.git → 두 단계 상위가 repo root
	tempDir := filepath.Dir(filepath.Dir(repoPath))
	handler := newDiffHandler(t, tempDir)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v2/repository/:owner/:repo/diff/file", adaptQuery(handler.HandleFileDiff))

	doRequest := func(query string) DiffResponse {
		url := fmt.Sprintf("/api/v2/repository/testowner/testrepo/diff/file?path=large.txt&base=%s&head=%s%s",
			baseSHA, headSHA, query)
		req, _ := http.NewRequest("GET", url, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
		var resp DiffResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		require.True(t, resp.Success)
		require.Len(t, resp.Files, 1)
		return resp
	}

	t.Run("explicit window returns bounded loaded slice", func(t *testing.T) {
		resp := doRequest("&line_offset=0&line_limit=2000")
		f := resp.Files[0]
		assert.Equal(t, FileLoadStatusLoaded, f.LoadStatus, "큰 파일도 윈도우 모드로 loaded")
		require.NotNil(t, f.Window)
		assert.Greater(t, f.TotalChangedLines, MaxChangedLines)
		assert.Equal(t, 2000, f.Window.Returned)
		assert.True(t, f.Window.HasMore)
		assert.Greater(t, f.Window.TotalLines, 2000)
		assert.NotEmpty(t, f.Changes)
		// offset 0 → window[0] 가 HUNK → 합성 헤더 없음
		assert.Len(t, f.Changes, 2000)
	})

	t.Run("continuation window picks up where prior left off", func(t *testing.T) {
		resp := doRequest("&line_offset=2000&line_limit=2000")
		f := resp.Files[0]
		assert.Equal(t, FileLoadStatusLoaded, f.LoadStatus)
		require.NotNil(t, f.Window)
		assert.Equal(t, 2000, f.Window.Offset)
		assert.Equal(t, 2000, f.Window.Returned)
		// mid-hunk 시작 → 합성 HUNK 헤더 1개가 앞에 붙어 Changes 는 2001
		require.NotEmpty(t, f.Changes)
		assert.Equal(t, LineChangeTypeHunk, f.Changes[0].Type)
		assert.Len(t, f.Changes, 2001)
	})

	t.Run("line_limit is capped at MaxDiffWindowLines", func(t *testing.T) {
		resp := doRequest("&line_offset=0&line_limit=999999")
		f := resp.Files[0]
		require.NotNil(t, f.Window)
		assert.LessOrEqual(t, f.Window.Returned, MaxDiffWindowLines)
	})

	t.Run("no window params auto-windows large file instead of too_large", func(t *testing.T) {
		resp := doRequest("")
		f := resp.Files[0]
		assert.Equal(t, FileLoadStatusLoaded, f.LoadStatus, "윈도우 파라미터 없어도 큰 파일은 loaded (too_large dead-end 회복)")
		require.NotNil(t, f.Window)
		assert.Equal(t, 0, f.Window.Offset)
		assert.Equal(t, DefaultDiffWindowLines, f.Window.Returned)
		assert.True(t, f.Window.HasMore)
	})
}
