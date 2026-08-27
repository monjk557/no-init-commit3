package fork

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/config"
)

// newHandlerWithEvents 는 miniredis 를 event bus 로 붙인 ForkHandler 를 만든다.
// HandleForkRepository 의 eventsClient != nil 발행 경로를 커버하기 위함.
func newHandlerWithEvents(t *testing.T, root, redisAddr string) *ForkHandler {
	t.Helper()
	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: root},
		Logging: config.LoggingConfig{Level: "error", Format: "text", Output: "stdout", Development: true},
		Events: config.EventsConfig{
			Enabled:       true,
			Mode:          "standalone",
			NodeAddress:   redisAddr,
			SocketTimeout: 2 * time.Second,
		},
	}
	h, err := NewForkHandler(cfg)
	require.NoError(t, err)
	require.NotNil(t, h.eventsClient, "eventsClient should be wired via miniredis")
	return h
}

// fork 성공 시 event bus 로 CommitEvent 가 발행되는 경로를 커버한다.
func TestHandler_ForkRepository_PublishesEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)
	root := newRoot(t)
	newBareRepoAt(t, root, "alice", "proj")
	h := newHandlerWithEvents(t, root, mr.Addr())
	t.Cleanup(func() {
		if h.eventsClient != nil {
			_ = h.eventsClient.Close()
		}
	})

	router := gin.New()
	router.POST("/repos/:owner/:repo/forks", adaptJSON(h.HandleForkRepository, nil))

	// default_branch 를 지정해 symbolic-ref 세팅 경로도 함께 커버.
	body := strings.NewReader(`{"owner":"bob","repo":"proj","default_branch":"main"}`)
	req := httptest.NewRequest(http.MethodPost, "/repos/alice/proj/forks", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-USER-ID", "user-42")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp ForkRepositoryResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.Equal(t, "bob/proj", resp.Repository)

	// 발행 goroutine 이 stream 에 기록할 시간을 준다 (fire-and-forget).
	require.Eventually(t, func() bool {
		return mr.Exists("git:events")
	}, 3*time.Second, 20*time.Millisecond, "fork event should reach the stream")
}
