package commits

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// 백필 엔드포인트는 전 ref 커밋을 sha/email/authored_at 삼중항으로 준다.
func Test_CommitContributions_ReturnsCommits(t *testing.T) {
	t.Parallel()

	handler, _ := newCommitsHandlerWithRepo(t)
	seedRecentCommit(t, handler, "a.txt", "a")
	seedRecentCommit(t, handler, "b.txt", "b")

	router := activityRouter(t, handler)
	w := getActivity(t, router, "/repos/testowner/testrepo/commits/contributions")
	requireStatus(t, w, http.StatusOK)

	var resp ContributionsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Equal(t, DefaultContributionsLimit, resp.Limit)
	require.False(t, resp.HasMore)
	require.NotEmpty(t, resp.Commits)

	for _, c := range resp.Commits {
		require.NotEmpty(t, c.SHA)
		require.NotEmpty(t, c.AuthorEmail)
		// 오프셋을 보존한 RFC3339 여야 한다 (설계 D8).
		require.Regexp(t, `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}([+-]\d{2}:\d{2}|Z)$`, c.AuthoredAt)
	}
}

// limit 을 넘기면 has_more 가 서고, offset 으로 다음 페이지가 이어져야 한다.
func Test_CommitContributions_Pagination(t *testing.T) {
	t.Parallel()

	handler, _ := newCommitsHandlerWithRepo(t)
	seedRecentCommit(t, handler, "a.txt", "a")
	seedRecentCommit(t, handler, "b.txt", "b")

	router := activityRouter(t, handler)

	first := getActivity(t, router, "/repos/testowner/testrepo/commits/contributions?limit=1")
	requireStatus(t, first, http.StatusOK)
	var page1 ContributionsResponse
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &page1))
	require.Len(t, page1.Commits, 1)
	require.True(t, page1.HasMore, "커밋이 2개 이상이면 다음 페이지가 있어야 한다")

	second := getActivity(t, router, "/repos/testowner/testrepo/commits/contributions?limit=1&offset=1")
	requireStatus(t, second, http.StatusOK)
	var page2 ContributionsResponse
	require.NoError(t, json.Unmarshal(second.Body.Bytes(), &page2))
	require.Len(t, page2.Commits, 1)
	require.NotEqual(t, page1.Commits[0].SHA, page2.Commits[0].SHA, "페이지가 겹치면 안 된다")
}

func Test_CommitContributions_InvalidPaging(t *testing.T) {
	t.Parallel()

	handler, _ := newCommitsHandlerWithRepo(t)
	router := activityRouter(t, handler)

	for _, q := range []string{"limit=0", "limit=5001", "limit=abc", "offset=-1", "offset=x"} {
		w := getActivity(t, router, "/repos/testowner/testrepo/commits/contributions?"+q)
		requireStatus(t, w, http.StatusBadRequest)
	}
}

// "contributions" 가 /:sha 로 잡히면 안 된다 (라우트 등록 순서 회귀 방지).
func Test_CommitContributions_NotCapturedAsSha(t *testing.T) {
	t.Parallel()

	handler, _ := newCommitsHandlerWithRepo(t)
	router := activityRouter(t, handler)

	w := getActivity(t, router, "/repos/testowner/testrepo/commits/contributions")
	requireStatus(t, w, http.StatusOK)

	var resp ContributionsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
}
