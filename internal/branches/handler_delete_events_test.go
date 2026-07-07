package branches

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/events"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// handler_delete_events_test.go — HandleDeleteBranch 의 이벤트 발행 분기와
// NewBranchOperationsHandler 의 Events.Enabled 분기를 커버한다. 실제 Redis 는
// 붙이지 않고 fake RedisStreamClient 를 주입한다 (외부 인프라 불필요).

// fakeStreamClient 는 events.RedisStreamClient 를 만족하는 in-memory 스텁이다.
type fakeStreamClient struct {
	mu     sync.Mutex
	events []*events.CommitEvent
	err    error
	wg     sync.WaitGroup
}

func (f *fakeStreamClient) AddCommitEventToStream(_ context.Context, ev *events.CommitEvent) error {
	defer f.wg.Done()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	return f.err
}

func (f *fakeStreamClient) Close() error { return nil }

func (f *fakeStreamClient) captured() []*events.CommitEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*events.CommitEvent, len(f.events))
	copy(out, f.events)
	return out
}

// deleteWithEvents 는 fake events client 를 주입한 뒤 삭제 요청을 보내고,
// 비동기 이벤트 발행 goroutine 이 끝날 때까지 기다린다.
func deleteWithEvents(t *testing.T, publishErr error, userID string) []*events.CommitEvent {
	t.Helper()

	handler, repoDir := newHandler(t)
	repoPath := buildBareRepoForDelete(t, repoDir)
	_ = repoPath

	fake := &fakeStreamClient{err: publishErr}
	fake.wg.Add(1)
	handler.eventsClient = fake

	router := gin.New()
	router.DELETE("/repository/:owner/:repo/branches/*branch", adapt(handler.HandleDeleteBranch))

	req := httptest.NewRequest("DELETE", "/repository/o/r/branches/feature", nil)
	if userID != "" {
		req.Header.Set("X-USER-ID", userID)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)
	fake.wg.Wait() // 비동기 발행 완료 대기
	return fake.captured()
}

// buildBareRepoForDelete reuses the shared bare-repo builder (master + feature).
func buildBareRepoForDelete(t *testing.T, root string) string {
	t.Helper()
	buildBareRepoWithBranches(t, root, "o", "r")
	return root
}

func Test_HandleDeleteBranch_PublishesEvent(t *testing.T) {
	t.Run("publishes branch delete event with user id", func(t *testing.T) {
		captured := deleteWithEvents(t, nil, "user-42")
		require.Len(t, captured, 1)
		ev := captured[0]
		require.Equal(t, events.EventTypeBranchDelete, ev.EventType)
		require.Equal(t, "refs/heads/feature", ev.RefName)
		require.Equal(t, "user-42", ev.UserID)
		require.NotEmpty(t, ev.CommitHash)
	})

	t.Run("publish failure is swallowed, response still 200", func(t *testing.T) {
		// AddCommitEventToStream 이 에러를 반환해도 삭제 응답은 성공이어야 한다
		// (fire-and-forget). warn 로그 경로를 커버한다.
		captured := deleteWithEvents(t, context.DeadlineExceeded, "")
		require.Len(t, captured, 1)
	})
}

func Test_NewBranchOperationsHandler_EventsEnabled_NoAddress_WarnsAndContinues(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
		Git:     config.GitConfig{RepoRoot: t.TempDir()},
		Events:  config.EventsConfig{Enabled: true}, // NodeAddress/RedisURL 비어있음 → 연결 실패
	}

	h, err := NewBranchOperationsHandler(cfg)

	// 이벤트 클라이언트 초기화 실패는 치명적이지 않다 — 핸들러는 생성되고
	// eventsClient 는 nil 로 남는다.
	require.NoError(t, err)
	require.NotNil(t, h)
	require.Nil(t, h.eventsClient)
}
