package commits

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// activityRouter 는 commits 라우트가 등록된 gin 엔진을 만든다 (full-router 패턴).
func activityRouter(t *testing.T, handler *CommitsHandler) *gin.Engine {
	t.Helper()
	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)
	return router
}

// seedRecentCommit 는 testowner/testrepo 의 main 에 현재 시각 커밋 하나를 쌓는다.
func seedRecentCommit(t *testing.T, handler *CommitsHandler, path, content string) {
	t.Helper()
	req := MultiFileCommitRequest{
		Message: "seed " + path,
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: path, Operation: "create", Content: content},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c, handler.HandleMultiFileCommit)
	requireStatus(t, w, http.StatusOK)
}

func getActivity(t *testing.T, router *gin.Engine, url string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// 최근 커밋들이 dense 히스토그램으로 집계되어야 한다.
func Test_CommitActivity_ReturnsDenseHistogram(t *testing.T) {
	t.Parallel()

	handler, _ := newCommitsHandlerWithRepo(t)
	seedRecentCommit(t, handler, "a.txt", "a")
	seedRecentCommit(t, handler, "b.txt", "b")
	seedRecentCommit(t, handler, "c.txt", "c")

	router := activityRouter(t, handler)
	w := getActivity(t, router, "/repos/testowner/testrepo/commits/activity?ref=main&days=30")
	require.Equal(t, http.StatusOK, w.Code)

	var resp CommitActivityResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	require.True(t, resp.Success)
	require.Equal(t, 30, resp.Days)
	require.Len(t, resp.Buckets, 30, "buckets must be dense (length == days)")

	// 초기 커밋(2024-01-01)은 윈도우 밖 → 최근 3개만 집계.
	require.Equal(t, 3, resp.Total)

	// 버킷은 오래된→최신 오름차순, 경계는 since/until 과 일치.
	require.Equal(t, resp.Since, resp.Buckets[0].Date)
	require.Equal(t, resp.Until, resp.Buckets[len(resp.Buckets)-1].Date)
	require.Less(t, resp.Buckets[0].Date, resp.Buckets[len(resp.Buckets)-1].Date)

	// 버킷 합 == Total (dense fill 이 커밋을 누락/중복하지 않음).
	sum := 0
	for _, b := range resp.Buckets {
		sum += b.Count
	}
	require.Equal(t, resp.Total, sum)
}

// days 미지정 시 default(30) 로 동작해야 한다.
func Test_CommitActivity_DefaultDays(t *testing.T) {
	t.Parallel()

	handler, _ := newCommitsHandlerWithRepo(t)
	router := activityRouter(t, handler)

	w := getActivity(t, router, "/repos/testowner/testrepo/commits/activity?ref=main")
	require.Equal(t, http.StatusOK, w.Code)

	var resp CommitActivityResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, DefaultActivityDays, resp.Days)
	require.Len(t, resp.Buckets, DefaultActivityDays)
}

// 윈도우 밖(오래된) 커밋만 있으면 전부 0 으로 채워진 dense 히스토그램.
func Test_CommitActivity_OldCommitsOnlyReturnZeros(t *testing.T) {
	t.Parallel()

	handler, _ := newCommitsHandlerWithRepo(t) // 초기 커밋은 2024-01-01
	router := activityRouter(t, handler)

	w := getActivity(t, router, "/repos/testowner/testrepo/commits/activity?ref=main&days=7")
	require.Equal(t, http.StatusOK, w.Code)

	var resp CommitActivityResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Equal(t, 7, resp.Days)
	require.Len(t, resp.Buckets, 7)
	require.Equal(t, 0, resp.Total)
	for _, b := range resp.Buckets {
		require.Equal(t, 0, b.Count)
	}
}

// days 파라미터 범위/형식 검증 → 400.
func Test_CommitActivity_InvalidDays(t *testing.T) {
	t.Parallel()

	handler, _ := newCommitsHandlerWithRepo(t)
	router := activityRouter(t, handler)

	for _, days := range []string{"0", "366", "-1", "abc"} {
		w := getActivity(t, router, "/repos/testowner/testrepo/commits/activity?ref=main&days="+days)
		require.Equalf(t, http.StatusBadRequest, w.Code, "days=%s must be rejected", days)
	}
}

// group_by 는 "" 또는 committer_date 만 허용 → 그 외 400.
func Test_CommitActivity_InvalidGroupBy(t *testing.T) {
	t.Parallel()

	handler, _ := newCommitsHandlerWithRepo(t)
	router := activityRouter(t, handler)

	w := getActivity(t, router, "/repos/testowner/testrepo/commits/activity?ref=main&group_by=author_date")
	require.Equal(t, http.StatusBadRequest, w.Code)
}

// committer_date 그룹핑도 정상 동작해야 한다.
func Test_CommitActivity_CommitterDateGroupBy(t *testing.T) {
	t.Parallel()

	handler, _ := newCommitsHandlerWithRepo(t)
	seedRecentCommit(t, handler, "a.txt", "a")

	router := activityRouter(t, handler)
	w := getActivity(t, router, "/repos/testowner/testrepo/commits/activity?ref=main&days=30&group_by=committer_date")
	require.Equal(t, http.StatusOK, w.Code)

	var resp CommitActivityResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Equal(t, 1, resp.Total)
}

// 존재하지 않는 repo → 4xx client error. ValidateRepoPath 는 경로 안전성만
// 검증하고 존재 여부는 확인하지 않으므로(존재하지-않는-경로 허용), 이후 git log
// 가 에러를 내며 400 을 반환한다. 이는 형제 엔드포인트 HandleCommitsAPI 와 동일한
// 동작이다 (git 에러 → BadRequest). 500 이나 200 이 아님을 회귀로 못박는다.
func Test_CommitActivity_MissingRepoReturnsClientError(t *testing.T) {
	t.Parallel()

	handler, _ := newCommitsHandlerWithRepo(t)
	router := activityRouter(t, handler)

	w := getActivity(t, router, "/repos/testowner/missingrepo/commits/activity?days=30")
	require.Equal(t, http.StatusBadRequest, w.Code)
}

// /commits/activity 가 /commits/:sha 로 잘못 라우팅되지 않아야 한다 (등록 순서 회귀).
func Test_CommitActivity_NotCapturedByShaRoute(t *testing.T) {
	t.Parallel()

	handler, _ := newCommitsHandlerWithRepo(t)
	router := activityRouter(t, handler)

	w := getActivity(t, router, "/repos/testowner/testrepo/commits/activity?days=30")
	require.Equal(t, http.StatusOK, w.Code)

	var resp CommitActivityResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// sha detail 응답이 아니라 activity 응답이어야 함.
	require.Equal(t, 30, resp.Days)
	require.NotNil(t, resp.Buckets)
}
