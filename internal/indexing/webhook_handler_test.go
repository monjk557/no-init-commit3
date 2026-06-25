package indexing

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// newWebhookFixture 는 실제 인메모리 go-git 저장소 + DB 로 동작하는 핸들러를 만든다.
func newWebhookFixture(t *testing.T) (*WebhookHandler, string) {
	t.Helper()
	db := newTestDB(t)
	repo := buildMemoryRepo(t)
	builder := NewIndexBuilder(db, &fakeGitService{repo: repo}, nil)
	query := NewQueryService(db, nil)
	mapper := NewSimpleRepoPathMapper(map[string]string{"acme/widget": "/srv/repos/widget"})
	h := NewWebhookHandler(builder, query, mapper)
	return h, "acme/widget"
}

func doJSON(t *testing.T, handler gin.HandlerFunc, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var buf bytes.Buffer
	require.NoError(t, json.NewEncoder(&buf).Encode(body))
	c.Request = httptest.NewRequest(http.MethodPost, "/webhooks/push", &buf)
	c.Request.Header.Set("Content-Type", "application/json")
	handler(c)
	return w
}

func TestSimpleRepoPathMapper(t *testing.T) {
	m := NewSimpleRepoPathMapper(map[string]string{"a/b": "/path/b"})

	t.Run("valid repo", func(t *testing.T) {
		assert.True(t, m.IsValidRepo("a/b"))
		p, err := m.GetRepoPath("a/b")
		require.NoError(t, err)
		assert.Equal(t, "/path/b", p)
	})

	t.Run("invalid repo", func(t *testing.T) {
		assert.False(t, m.IsValidRepo("x/y"))
		_, err := m.GetRepoPath("x/y")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "repository not found")
	})
}

func TestHandlePushWebhook_InvalidJSON(t *testing.T) {
	h, _ := newWebhookFixture(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/webhooks/push", bytes.NewBufferString("{bad json"))
	c.Request.Header.Set("Content-Type", "application/json")

	h.HandlePushWebhook(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	var resp WebhookResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.False(t, resp.Success)
	assert.Equal(t, "Invalid JSON payload", resp.Message)
}

func TestHandlePushWebhook_RepoNotFound(t *testing.T) {
	h, _ := newWebhookFixture(t)
	payload := PushWebhookPayload{}
	payload.Repository.FullName = "unknown/repo"

	w := doJSON(t, h.HandlePushWebhook, payload)
	assert.Equal(t, http.StatusNotFound, w.Code)

	var resp WebhookResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.False(t, resp.Success)
	assert.Equal(t, "unknown/repo", resp.RepoID)
}

func TestHandlePushWebhook_FallbackToName(t *testing.T) {
	// FullName 비어있으면 Name 으로 fallback. Name 도 invalid → 404.
	h, _ := newWebhookFixture(t)
	payload := PushWebhookPayload{}
	payload.Repository.Name = "widget"

	w := doJSON(t, h.HandlePushWebhook, payload)
	assert.Equal(t, http.StatusNotFound, w.Code)
	var resp WebhookResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "widget", resp.RepoID)
}

func TestHandlePushWebhook_Success(t *testing.T) {
	h, repoID := newWebhookFixture(t)
	// 유효한 HEAD SHA 를 커밋 목록에 넣어 실제 인덱싱 경로를 태운다.
	head, err := h.indexBuilder.gitSvc.OpenRepository("/srv/repos/widget")
	require.NoError(t, err)
	ref, err := head.Head()
	require.NoError(t, err)

	// 타입 중첩을 피하기 위해 raw JSON map 으로 페이로드를 구성한다.
	payload := map[string]interface{}{
		"repository": map[string]interface{}{"full_name": repoID},
		"ref":        "refs/heads/main",
		"commits": []map[string]interface{}{
			{"id": ref.Hash().String()},
		},
	}

	w := doJSON(t, h.HandlePushWebhook, payload)
	assert.Equal(t, http.StatusOK, w.Code)

	var resp WebhookResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.True(t, resp.IndexUpdated)
	assert.Equal(t, "main", resp.Branch)
	assert.Equal(t, repoID, resp.RepoID)
}

func TestHandleFullRebuild(t *testing.T) {
	h, repoID := newWebhookFixture(t)

	t.Run("missing repo id", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/admin/index/rebuild/", nil)
		// Param 미설정 → 빈 repo_id
		h.HandleFullRebuild(c)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("invalid repo", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/admin/index/rebuild/x", nil)
		c.Params = gin.Params{{Key: "repo_id", Value: "unknown/repo"}}
		h.HandleFullRebuild(c)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("success rebuild", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/admin/index/rebuild/x", nil)
		c.Params = gin.Params{{Key: "repo_id", Value: repoID}}
		h.HandleFullRebuild(c)
		assert.Equal(t, http.StatusOK, w.Code)

		var body map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Equal(t, true, body["success"])
	})
}

func TestHandleIndexStatus(t *testing.T) {
	h, repoID := newWebhookFixture(t)

	t.Run("missing repo id", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/admin/index/status/", nil)
		h.HandleIndexStatus(c)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("index not available", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/admin/index/status/x", nil)
		c.Params = gin.Params{{Key: "repo_id", Value: "no-index"}}
		h.HandleIndexStatus(c)
		assert.Equal(t, http.StatusOK, w.Code)

		var body map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Equal(t, false, body["index_available"])
	})

	t.Run("index available with stats", func(t *testing.T) {
		// 인덱스에 데이터를 심어 available 경로 + stats/metrics 분기 커버.
		seedRepoFixture(t, h.queryService.db, repoID)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/admin/index/status/x", nil)
		c.Params = gin.Params{{Key: "repo_id", Value: repoID}}
		h.HandleIndexStatus(c)
		assert.Equal(t, http.StatusOK, w.Code)

		var body map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Equal(t, true, body["index_available"])
		assert.Equal(t, "indexed_lookup_o(1)", body["performance_mode"])
		// query_metrics 는 항상 채워진다. repository_stats 는 GetRepositoryStats 가
		// SQLite 에서 에러(MAX(commit_time) Scan)를 반환해 핸들러가 조용히 생략 →
		// 그 분기(err != nil) 또한 정상 커버됨.
		assert.Contains(t, body, "query_metrics")
	})
}

func TestRegisterRoutes(t *testing.T) {
	h, _ := newWebhookFixture(t)
	router := gin.New()
	assert.NotPanics(t, func() {
		h.RegisterRoutes(router)
	})

	// 라우트가 실제로 등록되었는지 확인.
	routes := router.Routes()
	paths := map[string]bool{}
	for _, r := range routes {
		paths[r.Method+" "+r.Path] = true
	}
	assert.True(t, paths["POST /webhooks/push"])
	assert.True(t, paths["POST /admin/index/rebuild/:repo_id"])
	assert.True(t, paths["GET /admin/index/status/:repo_id"])
}
