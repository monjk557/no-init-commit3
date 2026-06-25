package revert

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
)

func init() {
	// 핸들러 테스트는 gin httptest 를 사용하므로 TestMode 로 고정.
	gin.SetMode(gin.TestMode)
}

// ---------------------------------------------------------------------------
// adapt / invokeTyped — typed handler 를 gin.HandlerFunc 로 감싸는 헬퍼.
// revert 는 WithErrorMapper(revertErrorMapper) 를 쓰므로 wrapper 를 그대로 mirror.
// ---------------------------------------------------------------------------

func invokeTyped[Resp any](c *gin.Context, fn func(*gin.Context) (Resp, error)) {
	resp, err := fn(c)
	if err != nil {
		// routes.go 의 revertErrorMapper 와 동일한 매핑을 적용.
		status, body := revertErrorMapper(err)
		c.JSON(status, body)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func adapt[Resp any](fn func(*gin.Context) (Resp, error)) gin.HandlerFunc {
	return func(c *gin.Context) { invokeTyped(c, fn) }
}

// ---------------------------------------------------------------------------
// 핸들러 테스트용 베어 레포 + 핸들러 빌더
// ---------------------------------------------------------------------------

// newTestHandler 는 repoRoot 를 가리키는 RevertHandler 를 구성한다.
// NewRevertHandler 는 config + Redis 의존성이 있으므로, 결정적 테스트를 위해
// 구조체를 직접 만든다 (eventsClient 는 옵션).
func newTestHandler(t *testing.T, repoRoot string, eventsClient events.RedisStreamClient) *RevertHandler {
	t.Helper()
	pathConfig, err := repositories.NewPathConfig(repoRoot)
	require.NoError(t, err)
	return &RevertHandler{
		pathConfig:   pathConfig,
		logger:       &logging.Logger{Logger: zap.NewNop()},
		eventsClient: eventsClient,
	}
}

// setupBareRepoWithCommits 는 repoRoot/owner/repo.git 위치에 베어 레포를 만들고
// main 브랜치에 2개 커밋을 쌓는다. 두 번째 커밋 SHA 를 반환한다.
func setupBareRepoWithCommits(t *testing.T, repoRoot, owner, repo string) (bareDir, secondCommit string) {
	t.Helper()

	// 베어 레포 생성.
	bareDir = filepath.Join(repoRoot, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(bareDir), 0o755))
	runH(t, "", "git", "init", "--bare", "-b", "main", bareDir)

	// 임시 워크 디렉터리에서 커밋을 만들고 베어 레포로 push.
	work := t.TempDir()
	runH(t, work, "git", "init", "-b", "main")
	runH(t, work, "git", "config", "user.name", "Test Author")
	runH(t, work, "git", "config", "user.email", "test@example.com")

	require.NoError(t, os.WriteFile(filepath.Join(work, "README.md"), []byte("# init"), 0o644))
	runH(t, work, "git", "add", ".")
	runH(t, work, "git", "commit", "-m", "initial commit")

	require.NoError(t, os.WriteFile(filepath.Join(work, "feature.txt"), []byte("feature"), 0o644))
	runH(t, work, "git", "add", ".")
	runH(t, work, "git", "commit", "-m", "add feature")
	secondCommit = runH(t, work, "git", "rev-parse", "HEAD")

	runH(t, work, "git", "remote", "add", "origin", bareDir)
	runH(t, work, "git", "push", "origin", "main")

	return bareDir, secondCommit
}

func runH(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "cmd %s %v failed: %s", name, args, string(out))
	return string(bytes.TrimSpace(out))
}

// buildRouter 는 adapt 헬퍼로 revert endpoint 를 등록한 라우터를 만든다.
func buildRouter(h *RevertHandler) *gin.Engine {
	r := gin.New()
	r.POST("/repos/:owner/:repo/revert", adapt(h.HandleRevert))
	return r
}

func doRevert(t *testing.T, r *gin.Engine, owner, repo string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	switch b := body.(type) {
	case string:
		payload = []byte(b)
	default:
		var err error
		payload, err = json.Marshal(b)
		require.NoError(t, err)
	}
	req := httptest.NewRequest(http.MethodPost, "/repos/"+owner+"/"+repo+"/revert", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ---------------------------------------------------------------------------
// HandleRevert — 성공 경로
// ---------------------------------------------------------------------------

func TestHandleRevert_Success(t *testing.T) {
	repoRoot := t.TempDir()
	_, commitSHA := setupBareRepoWithCommits(t, repoRoot, "acme", "widgets")
	h := newTestHandler(t, repoRoot, nil)
	r := buildRouter(h)

	w := doRevert(t, r, "acme", "widgets", RevertRequest{
		CommitSHA:  commitSHA,
		TargetRef:  "refs/heads/main",
		BranchName: "revert/feature",
	})

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	var resp RevertResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.Equal(t, "revert/feature", resp.RevertBranch)
	require.NotNil(t, resp.RevertCommit)
	require.NotNil(t, resp.RevertedCommit)
	assert.Equal(t, commitSHA, resp.RevertedCommit.SHA)
}

// fakeEventsClient 는 publishRevertEvent 경로를 커버하기 위한 가짜 client.
type fakeEventsClient struct {
	mu      sync.Mutex
	events  []*events.CommitEvent
	addErr  error
	closeOK bool
}

func (f *fakeEventsClient) AddCommitEventToStream(_ context.Context, e *events.CommitEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
	return f.addErr
}

func (f *fakeEventsClient) Close() error {
	f.closeOK = true
	return nil
}

func TestHandleRevert_Success_PublishesEvent(t *testing.T) {
	repoRoot := t.TempDir()
	_, commitSHA := setupBareRepoWithCommits(t, repoRoot, "acme", "evented")
	fake := &fakeEventsClient{}
	h := newTestHandler(t, repoRoot, fake)
	r := buildRouter(h)

	w := doRevert(t, r, "acme", "evented", RevertRequest{
		CommitSHA:  commitSHA,
		TargetRef:  "refs/heads/main",
		BranchName: "revert/evt",
	})
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	// publishRevertEvent 는 고루틴으로 stream 에 쓴다 — 잠깐 대기 후 확인.
	require.Eventually(t, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.events) == 1
	}, 2*time.Second, 10*time.Millisecond, "event should be published")

	fake.mu.Lock()
	ev := fake.events[0]
	fake.mu.Unlock()
	assert.Equal(t, events.EventTypeCommit, ev.EventType)
	assert.Equal(t, "refs/heads/revert/evt", ev.RefName)
}

// ---------------------------------------------------------------------------
// HandleRevert — 검증 / 에러 매핑 경로
// ---------------------------------------------------------------------------

func TestHandleRevert_MissingCommitSHA(t *testing.T) {
	repoRoot := t.TempDir()
	setupBareRepoWithCommits(t, repoRoot, "acme", "widgets")
	r := buildRouter(newTestHandler(t, repoRoot, nil))

	// commit_sha 누락 → gin binding:"required" 실패 → Invalid JSON 400.
	w := doRevert(t, r, "acme", "widgets", RevertRequest{
		TargetRef: "refs/heads/main",
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleRevert_MissingTargetRef(t *testing.T) {
	repoRoot := t.TempDir()
	_, commitSHA := setupBareRepoWithCommits(t, repoRoot, "acme", "widgets")
	r := buildRouter(newTestHandler(t, repoRoot, nil))

	// target_ref 누락은 gin binding:"required" 로 먼저 걸려 Invalid JSON 400.
	// (validateRevertRequest 의 target_ref 분기는 별도 단위 테스트로 커버.)
	w := doRevert(t, r, "acme", "widgets", RevertRequest{
		CommitSHA: commitSHA,
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestValidateRevertRequest_Direct(t *testing.T) {
	h := &RevertHandler{}
	// target_ref 만 빠진 경우 validateRevertRequest 가 직접 에러.
	err := h.validateRevertRequest(&RevertRequest{CommitSHA: "abc"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "target_ref")
}

func TestHandleRevert_CustomCommitter(t *testing.T) {
	repoRoot := t.TempDir()
	_, commitSHA := setupBareRepoWithCommits(t, repoRoot, "acme", "committer")
	r := buildRouter(newTestHandler(t, repoRoot, nil))

	// Committer override → resolveCommitter 의 override 분기 커버.
	w := doRevert(t, r, "acme", "committer", RevertRequest{
		CommitSHA:  commitSHA,
		TargetRef:  "refs/heads/main",
		BranchName: "revert/committer",
		Message:    "custom revert message",
		Committer: &AuthorInfo{
			Name:  "Bot Committer",
			Email: "bot@collabops.io",
		},
	})
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	var resp RevertResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.RevertCommit)
	assert.Equal(t, "custom revert message", resp.RevertCommit.Message)
	assert.Equal(t, "Bot Committer", resp.RevertCommit.Committer.Name)
}

func TestHandleRevert_InvalidJSON(t *testing.T) {
	repoRoot := t.TempDir()
	setupBareRepoWithCommits(t, repoRoot, "acme", "widgets")
	r := buildRouter(newTestHandler(t, repoRoot, nil))

	w := doRevert(t, r, "acme", "widgets", "{not valid json")
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "Invalid JSON")
}

func TestHandleRevert_NegativeParentNumber(t *testing.T) {
	repoRoot := t.TempDir()
	setupBareRepoWithCommits(t, repoRoot, "acme", "widgets")
	r := buildRouter(newTestHandler(t, repoRoot, nil))

	// parent_number 가 음수 → validateRevertRequest 400.
	w := doRevert(t, r, "acme", "widgets", RevertRequest{
		CommitSHA:    "deadbeef",
		TargetRef:    "refs/heads/main",
		ParentNumber: -1,
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "parent_number")
}

func TestHandleRevert_InvalidPathComponents(t *testing.T) {
	repoRoot := t.TempDir()
	setupBareRepoWithCommits(t, repoRoot, "acme", "widgets")
	r := buildRouter(newTestHandler(t, repoRoot, nil))

	// owner 에 path traversal 시도 → validatePath 400.
	w := doRevert(t, r, "..", "widgets", RevertRequest{
		CommitSHA: "deadbeef",
		TargetRef: "refs/heads/main",
	})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandleRevert_RepoDirMissing(t *testing.T) {
	repoRoot := t.TempDir()
	// 레포를 만들지 않음 → performRevert 가 target ref resolve 단계에서
	// "not a git repository" 로 실패 → mapper 의 default 500 분기.
	r := buildRouter(newTestHandler(t, repoRoot, nil))

	w := doRevert(t, r, "ghost", "missing", RevertRequest{
		CommitSHA:  "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		TargetRef:  "refs/heads/main",
		BranchName: "revert/x",
	})
	assert.Equal(t, http.StatusInternalServerError, w.Code, "body: %s", w.Body.String())
}

func TestHandleRevert_CommitNotFound(t *testing.T) {
	repoRoot := t.TempDir()
	setupBareRepoWithCommits(t, repoRoot, "acme", "widgets")
	r := buildRouter(newTestHandler(t, repoRoot, nil))

	w := doRevert(t, r, "acme", "widgets", RevertRequest{
		CommitSHA:  "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		TargetRef:  "refs/heads/main",
		BranchName: "revert/none",
	})
	assert.Equal(t, http.StatusNotFound, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "COMMIT_NOT_FOUND")
}

func TestHandleRevert_DuplicateBranch_Conflict(t *testing.T) {
	repoRoot := t.TempDir()
	_, commitSHA := setupBareRepoWithCommits(t, repoRoot, "acme", "dup")
	r := buildRouter(newTestHandler(t, repoRoot, nil))

	// 첫 revert 성공.
	w1 := doRevert(t, r, "acme", "dup", RevertRequest{
		CommitSHA:  commitSHA,
		TargetRef:  "refs/heads/main",
		BranchName: "revert/dup",
	})
	require.Equal(t, http.StatusOK, w1.Code, "body: %s", w1.Body.String())

	// 같은 브랜치로 두 번째 revert → BranchExistsError → 409.
	w2 := doRevert(t, r, "acme", "dup", RevertRequest{
		CommitSHA:  commitSHA,
		TargetRef:  "refs/heads/main",
		BranchName: "revert/dup",
	})
	assert.Equal(t, http.StatusConflict, w2.Code, "body: %s", w2.Body.String())
	assert.Contains(t, w2.Body.String(), "BRANCH_ALREADY_EXISTS")
}

// ---------------------------------------------------------------------------
// NewRevertHandler — 생성자 경로
// ---------------------------------------------------------------------------

func TestNewRevertHandler_NilConfig(t *testing.T) {
	_, err := NewRevertHandler(nil)
	require.Error(t, err)
}
