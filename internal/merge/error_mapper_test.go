package merge

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/api/typed"
)

// Test_mergeErrorMapper_InjectsFlatCodeAndParams 는 responseErr 의 code/params
// 가 응답 body 에 flat 하게 주입되고 (source_ref/target_ref/conflict_count),
// 기존 conflicts[] 같은 리치 필드는 보존되는지 검증한다 (Issue-5360).
func Test_mergeErrorMapper_InjectsFlatCodeAndParams(t *testing.T) {
	t.Parallel()

	rerr := &responseErr{
		status: http.StatusConflict,
		code:   string(api.ErrCodeMergeConflict),
		params: map[string]string{
			"source_ref":     "feature",
			"target_ref":     "main",
			"conflict_count": "3",
		},
		body: gin.H{
			"success":   false,
			"error":     "Merge has 3 conflicts requiring resolution",
			"conflicts": []map[string]any{{"file_path": "conflict.txt"}},
		},
	}

	status, body := mergeErrorMapper(rerr)
	require.Equal(t, http.StatusConflict, status)

	m, ok := body.(gin.H)
	require.True(t, ok, "body should be gin.H")
	require.Equal(t, false, m["success"])
	require.Equal(t, string(api.ErrCodeMergeConflict), m["code"])
	require.NotNil(t, m["conflicts"], "existing conflicts[] must be preserved")

	params, ok := m["params"].(map[string]string)
	require.True(t, ok, "params should be map[string]string")
	require.Equal(t, "feature", params["source_ref"])
	require.Equal(t, "main", params["target_ref"])
	require.Equal(t, "3", params["conflict_count"])
}

// Test_mergeErrorMapper_ConcurrentUpdate_UsesCodeKeyNotErrorCode 는 동시성
// 충돌 응답이 표준 `code` 키를 쓰고 (legacy `error_code` 아님) params 는
// 생략되는지 검증한다 (Decision 6).
func Test_mergeErrorMapper_ConcurrentUpdate_UsesCodeKeyNotErrorCode(t *testing.T) {
	t.Parallel()

	rerr := &responseErr{
		status: http.StatusConflict,
		code:   string(api.ErrCodeConcurrentUpdate),
		body: gin.H{
			"success": false,
			"error":   "target ref advanced concurrently",
		},
	}

	status, body := mergeErrorMapper(rerr)
	require.Equal(t, http.StatusConflict, status)

	m, ok := body.(gin.H)
	require.True(t, ok)
	require.Equal(t, "CONCURRENT_UPDATE", m["code"])
	require.NotContains(t, m, "error_code", "legacy error_code key must be gone")
	require.NotContains(t, m, "params", "params omitted when empty")
}

// Test_mergeErrorMapper_HTTPErrorWithCode 는 typed.HTTPError 경로도 flat
// code/params 를 싣는지 검증한다 (merge ref-resolve REF_NOT_FOUND 경로).
func Test_mergeErrorMapper_HTTPErrorWithCode(t *testing.T) {
	t.Parallel()

	he := typed.NewHTTPErrorWithCode(http.StatusNotFound,
		"Failed to resolve source ref: unknown revision",
		string(api.ErrCodeRefNotFound), map[string]string{"ref": "feature"})

	status, body := mergeErrorMapper(he)
	require.Equal(t, http.StatusNotFound, status)

	m, ok := body.(gin.H)
	require.True(t, ok)
	require.Equal(t, "REF_NOT_FOUND", m["code"])
	params, ok := m["params"].(map[string]string)
	require.True(t, ok)
	require.Equal(t, "feature", params["ref"])
}

// Test_mergeErrorMapper_PlainHTTPError_StaysTwoKeys 는 code 없는 HTTPError 가
// legacy 2 키 shape 를 유지하는지 회귀 가드한다.
func Test_mergeErrorMapper_PlainHTTPError_StaysTwoKeys(t *testing.T) {
	t.Parallel()

	status, body := mergeErrorMapper(typed.NewHTTPError(http.StatusBadRequest, "bad"))
	require.Equal(t, http.StatusBadRequest, status)

	m, ok := body.(gin.H)
	require.True(t, ok)
	require.Len(t, m, 2, "code 없는 HTTPError 는 {success, error} 2 키만")
}
