package fork

import (
	stdctx "context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/collabops/git-executor/internal/api/typed"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
)

// --- 공통 헬퍼 ---

func testLogger(t *testing.T) *logging.Logger {
	t.Helper()
	l, err := logging.NewLogger(config.LoggingConfig{
		Level: "error", Format: "text", Output: "stdout", Development: true,
	})
	require.NoError(t, err)
	return l
}

func gitEnv() []string {
	return []string{
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t.com", "GIT_AUTHOR_DATE=2024-01-01T00:00:00+00:00",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t.com", "GIT_COMMITTER_DATE=2024-01-01T00:00:00+00:00",
	}
}

func trim(b []byte) string { return strings.TrimRight(string(b), "\r\n") }

func hashObject(t *testing.T, repoPath, content string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", repoPath, "hash-object", "-w", "--stdin")
	cmd.Stdin = strings.NewReader(content)
	out, err := cmd.Output()
	require.NoError(t, err)
	return trim(out)
}

func makeTree(t *testing.T, repoPath, spec string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", repoPath, "mktree")
	cmd.Stdin = strings.NewReader(spec)
	out, err := cmd.Output()
	require.NoError(t, err)
	return trim(out)
}

func commitTree(t *testing.T, repoPath, treeHash, msg string, parents ...string) string {
	t.Helper()
	args := []string{"--git-dir", repoPath, "commit-tree", treeHash, "-m", msg}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	cmd := exec.Command("git", args...)
	cmd.Env = append(cmd.Environ(), gitEnv()...)
	out, err := cmd.Output()
	require.NoError(t, err)
	return trim(out)
}

// newBareRepoAt 는 주어진 owner/repo 레이아웃에 맞춰 root/{owner}/{repo}.git
// bare repo 를 생성하고 첫 커밋을 만든다. repoPath 와 commit SHA 를 돌려준다.
func newBareRepoAt(t *testing.T, root, owner, repo string) (repoPath, commitSHA string) {
	t.Helper()
	repoPath = filepath.Join(root, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0o755))
	out, err := exec.Command("git", "init", "--bare", repoPath).CombinedOutput()
	require.NoError(t, err, string(out))
	blob := hashObject(t, repoPath, "hi\n")
	tree := makeTree(t, repoPath, "100644 blob "+blob+"\tf.txt\n")
	commitSHA = commitTree(t, repoPath, tree, "init")
	require.NoError(t, exec.Command("git", "--git-dir", repoPath, "update-ref", "refs/heads/main", commitSHA).Run())
	require.NoError(t, exec.Command("git", "--git-dir", repoPath, "symbolic-ref", "HEAD", "refs/heads/main").Run())
	return
}

// newRoot 는 symlink 가 해소된 임시 repo root 를 돌려준다 (macOS /var → /private/var).
func newRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return resolved
}

func newPathConfig(t *testing.T, root string) *repositories.PathConfig {
	t.Helper()
	pc, err := repositories.NewPathConfig(root)
	require.NoError(t, err)
	return pc
}

// addBranch 는 bare repo 에 새 브랜치를 첫 커밋 위에 만든다.
func addBranch(t *testing.T, repoPath, branch, commit string) {
	t.Helper()
	require.NoError(t, exec.Command("git", "--git-dir", repoPath, "update-ref", "refs/heads/"+branch, commit).Run())
}

// addCommitOnBranch 는 기존 브랜치 위에 새 커밋을 쌓고 새 SHA 를 돌려준다.
func addCommitOnBranch(t *testing.T, repoPath, branch, content, parent string) string {
	t.Helper()
	blob := hashObject(t, repoPath, content)
	tree := makeTree(t, repoPath, "100644 blob "+blob+"\tf.txt\n")
	sha := commitTree(t, repoPath, tree, "update", parent)
	require.NoError(t, exec.Command("git", "--git-dir", repoPath, "update-ref", "refs/heads/"+branch, sha).Run())
	return sha
}

// --- ForkService 테스트 ---

func TestForkService_ForkRepository_Success(t *testing.T) {
	root := newRoot(t)
	srcPath, _ := newBareRepoAt(t, root, "alice", "proj")
	pc := newPathConfig(t, root)
	svc := NewForkService(testLogger(t), pc)

	res, err := svc.ForkRepository(stdctx.Background(), "alice", "proj", "bob", "proj", "user-1")
	require.NoError(t, err)
	assert.Equal(t, "alice", res.SourceOwner)
	assert.Equal(t, "bob", res.TargetOwner)
	assert.Equal(t, "main", res.DefaultBranch)

	// 타겟 디렉터리가 실제로 생성되었는지
	_, statErr := os.Stat(res.TargetPath)
	require.NoError(t, statErr)

	// upstream remote 가 source 를 가리키는지
	url, err := nativeRemoteGetURL(stdctx.Background(), res.TargetPath, upstreamRemoteName)
	require.NoError(t, err)
	assert.Equal(t, srcPath, url)

	// origin remote 는 제거되었는지
	remotes, err := nativeListRemotes(stdctx.Background(), res.TargetPath)
	require.NoError(t, err)
	assert.NotContains(t, remotes, "origin")
	assert.Contains(t, remotes, upstreamRemoteName)

	// 포크로 인식되고 메타데이터가 정확한지
	assert.True(t, svc.IsFork(res.TargetPath))
	meta, err := svc.GetForkMetadata(res.TargetPath)
	require.NoError(t, err)
	assert.Equal(t, "alice", meta.SourceOwner)
	assert.Equal(t, "proj", meta.SourceRepo)
	assert.Equal(t, "user-1", meta.ForkedBy)
	assert.Equal(t, srcPath, meta.SourcePath)
}

func TestForkService_ForkRepository_SourceNotFound(t *testing.T) {
	root := newRoot(t)
	pc := newPathConfig(t, root)
	svc := NewForkService(testLogger(t), pc)

	_, err := svc.ForkRepository(stdctx.Background(), "ghost", "missing", "bob", "proj", "")
	// 회귀: 존재하지 않는 소스는 ErrSourceNotFound
	assert.ErrorIs(t, err, ErrSourceNotFound)
}

func TestForkService_ForkRepository_TargetExists(t *testing.T) {
	root := newRoot(t)
	newBareRepoAt(t, root, "alice", "proj")
	newBareRepoAt(t, root, "bob", "proj") // 타겟이 이미 존재
	pc := newPathConfig(t, root)
	svc := NewForkService(testLogger(t), pc)

	_, err := svc.ForkRepository(stdctx.Background(), "alice", "proj", "bob", "proj", "")
	// 회귀: 타겟이 이미 존재하면 ErrForkAlreadyExists
	assert.ErrorIs(t, err, ErrForkAlreadyExists)
}

func TestForkService_ForkRepository_InvalidTargetComponents(t *testing.T) {
	root := newRoot(t)
	newBareRepoAt(t, root, "alice", "proj")
	pc := newPathConfig(t, root)
	svc := NewForkService(testLogger(t), pc)

	_, err := svc.ForkRepository(stdctx.Background(), "alice", "proj", "../escape", "proj", "")
	// 회귀: traversal 시도하는 타겟 컴포넌트는 거부
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrForkAlreadyExists)
}

func TestForkService_IsFork_NonFork(t *testing.T) {
	root := newRoot(t)
	srcPath, _ := newBareRepoAt(t, root, "alice", "proj")
	svc := NewForkService(testLogger(t), newPathConfig(t, root))
	assert.False(t, svc.IsFork(srcPath))

	_, err := svc.GetForkMetadata(srcPath)
	// 회귀: 메타데이터 없는 repo 는 ErrNotAFork
	assert.ErrorIs(t, err, ErrNotAFork)
}

func TestForkService_GetForkMetadata_Corrupted(t *testing.T) {
	root := newRoot(t)
	srcPath, _ := newBareRepoAt(t, root, "alice", "proj")
	svc := NewForkService(testLogger(t), newPathConfig(t, root))

	require.NoError(t, os.WriteFile(filepath.Join(srcPath, metadataFileName), []byte("{not json"), 0o644))
	_, err := svc.GetForkMetadata(srcPath)
	// 회귀: 깨진 JSON 은 parse 에러 (ErrNotAFork 아님)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNotAFork)
}

func TestForkService_SyncUpstream_FastForward(t *testing.T) {
	root := newRoot(t)
	_, srcCommit := newBareRepoAt(t, root, "alice", "proj")
	pc := newPathConfig(t, root)
	svc := NewForkService(testLogger(t), pc)

	// 포크 먼저 생성 (upstream remote + metadata 설정됨)
	res, err := svc.ForkRepository(stdctx.Background(), "alice", "proj", "bob", "proj", "")
	require.NoError(t, err)

	// upstream(alice) main 에 새 커밋을 쌓아서 fork 가 뒤처지게 만든다
	srcPath := filepath.Join(root, "alice", "proj.git")
	addCommitOnBranch(t, srcPath, "main", "world\n", srcCommit)

	out, err := svc.SyncUpstream(stdctx.Background(), res.TargetPath)
	require.NoError(t, err)
	require.Len(t, out.BranchesSynced, 1)
	assert.Equal(t, "main", out.BranchesSynced[0].Name)
	assert.Equal(t, 1, out.BranchesSynced[0].CommitsBehind)
	assert.Empty(t, out.Conflicts)
}

func TestForkService_SyncUpstream_DivergedConflict(t *testing.T) {
	root := newRoot(t)
	_, srcCommit := newBareRepoAt(t, root, "alice", "proj")
	pc := newPathConfig(t, root)
	svc := NewForkService(testLogger(t), pc)

	res, err := svc.ForkRepository(stdctx.Background(), "alice", "proj", "bob", "proj", "")
	require.NoError(t, err)

	// upstream 과 fork 가 같은 부모 위에서 서로 다른 커밋으로 분기
	srcPath := filepath.Join(root, "alice", "proj.git")
	addCommitOnBranch(t, srcPath, "main", "upstream-change\n", srcCommit)
	addCommitOnBranch(t, res.TargetPath, "main", "fork-change\n", srcCommit)

	out, err := svc.SyncUpstream(stdctx.Background(), res.TargetPath)
	require.NoError(t, err)
	assert.Empty(t, out.BranchesSynced)
	require.Len(t, out.Conflicts, 1)
	// 회귀: 분기된 브랜치는 conflict 로 보고
	assert.Contains(t, out.Conflicts[0].Reason, "diverged")
}

func TestForkService_SyncUpstream_AlreadyInSync(t *testing.T) {
	root := newRoot(t)
	newBareRepoAt(t, root, "alice", "proj")
	pc := newPathConfig(t, root)
	svc := NewForkService(testLogger(t), pc)

	res, err := svc.ForkRepository(stdctx.Background(), "alice", "proj", "bob", "proj", "")
	require.NoError(t, err)

	// 변경 없이 sync → 동기화/충돌 모두 비어있음
	out, err := svc.SyncUpstream(stdctx.Background(), res.TargetPath)
	require.NoError(t, err)
	assert.Empty(t, out.BranchesSynced)
	assert.Empty(t, out.Conflicts)
}

func TestForkService_SyncUpstream_LocalOnlyBranchSkipped(t *testing.T) {
	root := newRoot(t)
	_, srcCommit := newBareRepoAt(t, root, "alice", "proj")
	pc := newPathConfig(t, root)
	svc := NewForkService(testLogger(t), pc)

	res, err := svc.ForkRepository(stdctx.Background(), "alice", "proj", "bob", "proj", "")
	require.NoError(t, err)

	// fork 에만 있는 브랜치는 upstream 에 없으므로 skip
	addBranch(t, res.TargetPath, "feature", srcCommit)
	out, err := svc.SyncUpstream(stdctx.Background(), res.TargetPath)
	require.NoError(t, err)
	assert.Empty(t, out.BranchesSynced)
	assert.Empty(t, out.Conflicts)
}

func TestForkService_SyncUpstream_NotAFork(t *testing.T) {
	root := newRoot(t)
	srcPath, _ := newBareRepoAt(t, root, "alice", "proj")
	svc := NewForkService(testLogger(t), newPathConfig(t, root))

	_, err := svc.SyncUpstream(stdctx.Background(), srcPath)
	// 회귀: 메타데이터 없는 repo 는 sync 불가
	assert.ErrorIs(t, err, ErrNotAFork)
}

func TestForkService_SyncUpstream_UpstreamMoved(t *testing.T) {
	root := newRoot(t)
	_, srcCommit := newBareRepoAt(t, root, "alice", "proj")
	pc := newPathConfig(t, root)
	svc := NewForkService(testLogger(t), pc)

	res, err := svc.ForkRepository(stdctx.Background(), "alice", "proj", "bob", "proj", "")
	require.NoError(t, err)

	// upstream remote URL 을 의도적으로 엉뚱한 값으로 바꿔서 set-url 복구 경로를 탄다
	require.NoError(t, nativeRemoteSetURL(stdctx.Background(), res.TargetPath, upstreamRemoteName, "/nonexistent/path.git"))
	srcPath := filepath.Join(root, "alice", "proj.git")
	addCommitOnBranch(t, srcPath, "main", "moved\n", srcCommit)

	out, err := svc.SyncUpstream(stdctx.Background(), res.TargetPath)
	require.NoError(t, err)
	// metadata.SourcePath 로 set-url 복구 후 fetch → fast-forward 성공
	require.Len(t, out.BranchesSynced, 1)
}

func TestForkService_SyncUpstream_RemoteReadded(t *testing.T) {
	root := newRoot(t)
	_, srcCommit := newBareRepoAt(t, root, "alice", "proj")
	pc := newPathConfig(t, root)
	svc := NewForkService(testLogger(t), pc)

	res, err := svc.ForkRepository(stdctx.Background(), "alice", "proj", "bob", "proj", "")
	require.NoError(t, err)

	// upstream remote 를 완전히 제거 → get-url 실패 → re-add 경로
	require.NoError(t, nativeRemoteRemove(stdctx.Background(), res.TargetPath, upstreamRemoteName))
	srcPath := filepath.Join(root, "alice", "proj.git")
	addCommitOnBranch(t, srcPath, "main", "readd\n", srcCommit)

	out, err := svc.SyncUpstream(stdctx.Background(), res.TargetPath)
	require.NoError(t, err)
	require.Len(t, out.BranchesSynced, 1)
}

func TestForkService_SyncUpstream_UpstreamGone(t *testing.T) {
	root := newRoot(t)
	newBareRepoAt(t, root, "alice", "proj")
	pc := newPathConfig(t, root)
	svc := NewForkService(testLogger(t), pc)

	res, err := svc.ForkRepository(stdctx.Background(), "alice", "proj", "bob", "proj", "")
	require.NoError(t, err)

	// source repo 를 삭제 → upstream 소멸
	require.NoError(t, os.RemoveAll(filepath.Join(root, "alice")))
	_, err = svc.SyncUpstream(stdctx.Background(), res.TargetPath)
	// 회귀: 소스가 사라지면 ErrUpstreamNotFound
	assert.ErrorIs(t, err, ErrUpstreamNotFound)
}

// --- native_git 직접 테스트 (서비스 경로로 안 닿는 함수) ---

func TestNativeGit_Helpers(t *testing.T) {
	root := newRoot(t)
	repoPath, commit := newBareRepoAt(t, root, "alice", "proj")
	ctx := stdctx.Background()

	t.Run("RevListCount", func(t *testing.T) {
		n, err := nativeRevListCount(ctx, repoPath, "main")
		require.NoError(t, err)
		assert.Equal(t, 1, n)
	})

	t.Run("ForEachRef empty pattern returns nil", func(t *testing.T) {
		refs, err := nativeForEachRef(ctx, repoPath, "refs/remotes/none/")
		require.NoError(t, err)
		assert.Nil(t, refs)
	})

	t.Run("SymbolicRef returns HEAD target", func(t *testing.T) {
		ref, err := nativeSymbolicRef(ctx, repoPath)
		require.NoError(t, err)
		assert.Equal(t, "refs/heads/main", ref)
	})

	t.Run("SetSymbolicRef changes HEAD", func(t *testing.T) {
		addBranch(t, repoPath, "dev", commit)
		require.NoError(t, nativeSetSymbolicRef(ctx, repoPath, "refs/heads/dev"))
		ref, err := nativeSymbolicRef(ctx, repoPath)
		require.NoError(t, err)
		assert.Equal(t, "refs/heads/dev", ref)
	})

	t.Run("MergeBase of single commit is itself", func(t *testing.T) {
		base, err := nativeMergeBase(ctx, repoPath, commit, commit)
		require.NoError(t, err)
		assert.Equal(t, commit, base)
	})

	t.Run("error paths return errors", func(t *testing.T) {
		_, err := nativeRevListCount(ctx, repoPath, "refs/heads/does-not-exist")
		assert.Error(t, err)
		_, err = nativeMergeBase(ctx, repoPath, "deadbeef", "deadbeef")
		assert.Error(t, err)
		_, err = nativeRemoteGetURL(ctx, repoPath, "nope")
		assert.Error(t, err)
		err = nativeFetchUpstream(ctx, repoPath, "nope")
		assert.Error(t, err)
	})
}

func TestNativeGit_SymbolicRef_Unborn(t *testing.T) {
	// HEAD 가 unborn(가리키는 브랜치 없음)인 빈 bare repo → symbolic-ref -q 가
	// exit 1 → 에러 아닌 빈 문자열 반환 (회귀: exit code 1 분기).
	root := newRoot(t)
	repoPath, commit := newBareRepoAt(t, root, "alice", "proj")
	// HEAD 를 커밋 SHA 로 detach → symbolic-ref -q 가 exit 1 반환.
	require.NoError(t, exec.Command("git", "--git-dir", repoPath, "update-ref", "--no-deref", "HEAD", commit).Run())

	ref, symErr := nativeSymbolicRef(stdctx.Background(), repoPath)
	require.NoError(t, symErr)
	assert.Empty(t, ref)
}

func TestNativeGit_CloneBareError(t *testing.T) {
	// 존재하지 않는 source → clone 실패
	err := nativeCloneBare(stdctx.Background(), "/nonexistent/src.git", filepath.Join(t.TempDir(), "dest.git"))
	assert.Error(t, err)
}

// --- Handler 테스트 ---

// invokeTyped 는 typed handler 의 결과를 gin.Context 에 직접 쓴다 — wrapper mirror.
func invokeTyped[Resp any](c *gin.Context, fn func(*gin.Context) (Resp, error)) {
	resp, err := fn(c)
	if err != nil {
		var he *typed.HTTPError
		if errors.As(err, &he) {
			c.JSON(he.Status, gin.H{"success": false, "error": he.Message})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, resp)
}

func adapt[Resp any](fn func(*gin.Context) (Resp, error)) gin.HandlerFunc {
	return func(c *gin.Context) { invokeTyped(c, fn) }
}

// adaptNoBody 는 body 없는 typed handler 를 mapper 와 함께 mount 한다 —
// RegisterNoBody + WithErrorMapper wrapper mirror.
func adaptNoBody[Resp any](
	fn func(*gin.Context) (Resp, error),
	mapper func(error) (int, any),
) gin.HandlerFunc {
	return func(c *gin.Context) {
		resp, err := fn(c)
		if err != nil {
			status, body := mapper(err)
			c.JSON(status, body)
			return
		}
		c.JSON(http.StatusOK, resp)
	}
}

// adaptJSON 은 body 를 받는 typed handler 를 위한 adapter — RegisterJSON wrapper
// 처럼 body 를 bind 한 뒤 결과를 응답에 매핑. mapper 로 sentinel 에러 상태 변환.
func adaptJSON[Req, Resp any](
	fn func(*gin.Context, Req) (Resp, error),
	mapper func(error) (int, any),
) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req Req
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
			return
		}
		resp, err := fn(c, req)
		if err != nil {
			if mapper != nil {
				status, body := mapper(err)
				c.JSON(status, body)
				return
			}
			var he *typed.HTTPError
			if errors.As(err, &he) {
				c.JSON(he.Status, gin.H{"success": false, "error": he.Message})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, resp)
	}
}

func newHandler(t *testing.T, root string) *ForkHandler {
	t.Helper()
	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: root},
		Logging: config.LoggingConfig{Level: "error", Format: "text", Output: "stdout", Development: true},
		Events:  config.EventsConfig{Enabled: false},
	}
	h, err := NewForkHandler(cfg)
	require.NoError(t, err)
	return h
}

func TestNewForkHandler_EmptyRepoRoot(t *testing.T) {
	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: ""},
		Logging: config.LoggingConfig{Level: "error", Format: "text", Output: "stdout", Development: true},
		Events:  config.EventsConfig{Enabled: false},
	}
	_, err := NewForkHandler(cfg)
	// 회귀: 빈 repo root 는 path config 생성 실패
	assert.Error(t, err)
}

func TestHandler_ForkRepository_Created(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := newRoot(t)
	newBareRepoAt(t, root, "alice", "proj")
	h := newHandler(t, root)

	router := gin.New()
	router.POST("/repos/:owner/:repo/forks", adaptJSON(h.HandleForkRepository, forkRepositoryErrorMapper))

	body := strings.NewReader(`{"owner":"bob","repo":"proj"}`)
	req := httptest.NewRequest(http.MethodPost, "/repos/alice/proj/forks", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-USER-ID", "user-9")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp ForkRepositoryResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.Equal(t, "bob/proj", resp.Repository)
	assert.Equal(t, "alice/proj", resp.SourceRepository)
	assert.Equal(t, "user-9", resp.ForkMetadata.ForkedBy)

	// 실제로 fork 디렉터리가 생성됐는지
	_, statErr := os.Stat(filepath.Join(root, "bob", "proj.git"))
	require.NoError(t, statErr)
}

func TestHandler_ForkRepository_DefaultRepoNameAndBranch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := newRoot(t)
	srcPath, commit := newBareRepoAt(t, root, "alice", "proj")
	addBranch(t, srcPath, "develop", commit)
	h := newHandler(t, root)

	router := gin.New()
	router.POST("/repos/:owner/:repo/forks", adaptJSON(h.HandleForkRepository, forkRepositoryErrorMapper))

	// repo 미지정 → source repo 이름 재사용, default_branch override 적용
	body := strings.NewReader(`{"owner":"carol","default_branch":"develop"}`)
	req := httptest.NewRequest(http.MethodPost, "/repos/alice/proj/forks", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp ForkRepositoryResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "carol/proj", resp.Repository)
	assert.Equal(t, "develop", resp.DefaultBranch)
}

func TestHandler_ForkRepository_InvalidTargetPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := newRoot(t)
	newBareRepoAt(t, root, "alice", "proj")
	h := newHandler(t, root)

	router := gin.New()
	router.POST("/repos/:owner/:repo/forks", adaptJSON(h.HandleForkRepository, forkRepositoryErrorMapper))

	// body 의 타겟 owner 에 traversal 시도 → 타겟 path 검증 400
	body := strings.NewReader(`{"owner":"../escape","repo":"proj"}`)
	req := httptest.NewRequest(http.MethodPost, "/repos/alice/proj/forks", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 회귀: traversal 타겟은 400 BadRequest
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

func TestHandler_ForkRepository_SourceNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := newRoot(t)
	h := newHandler(t, root)

	router := gin.New()
	router.POST("/repos/:owner/:repo/forks", adaptJSON(h.HandleForkRepository, forkRepositoryErrorMapper))

	body := strings.NewReader(`{"owner":"bob","repo":"proj"}`)
	req := httptest.NewRequest(http.MethodPost, "/repos/ghost/missing/forks", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 회귀: 존재하지 않는 소스 → mapper 가 404 매핑
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}

func TestHandler_ForkRepository_MissingOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := newRoot(t)
	newBareRepoAt(t, root, "alice", "proj")
	h := newHandler(t, root)

	router := gin.New()
	router.POST("/repos/:owner/:repo/forks", adaptJSON(h.HandleForkRepository, forkRepositoryErrorMapper))

	// owner 누락 → binding:"required" 위반 → 400
	body := strings.NewReader(`{"repo":"proj"}`)
	req := httptest.NewRequest(http.MethodPost, "/repos/alice/proj/forks", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

func TestHandler_GetForkInfo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := newRoot(t)
	newBareRepoAt(t, root, "alice", "proj")
	h := newHandler(t, root)

	router := gin.New()
	router.GET("/repos/:owner/:repo/fork", adapt(h.HandleGetForkInfo))
	router.POST("/repos/:owner/:repo/forks", adaptJSON(h.HandleForkRepository, forkRepositoryErrorMapper))

	// 1) 포크 전 → is_fork=false
	req := httptest.NewRequest(http.MethodGet, "/repos/alice/proj/fork", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var info ForkInfoResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &info))
	assert.False(t, info.IsFork)

	// 2) 포크 생성
	body := strings.NewReader(`{"owner":"bob","repo":"proj"}`)
	preq := httptest.NewRequest(http.MethodPost, "/repos/alice/proj/forks", body)
	preq.Header.Set("Content-Type", "application/json")
	pw := httptest.NewRecorder()
	router.ServeHTTP(pw, preq)
	require.Equal(t, http.StatusOK, pw.Code, pw.Body.String())

	// 3) 포크된 repo → is_fork=true + metadata
	req2 := httptest.NewRequest(http.MethodGet, "/repos/bob/proj/fork", nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusOK, w2.Code, w2.Body.String())
	var info2 ForkInfoResponse
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &info2))
	assert.True(t, info2.IsFork)
	require.NotNil(t, info2.ForkMetadata)
	assert.Equal(t, "alice", info2.ForkMetadata.SourceOwner)
}

func TestHandler_GetForkInfo_InvalidPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := newRoot(t)
	h := newHandler(t, root)

	router := gin.New()
	router.GET("/repos/:owner/:repo/fork", adapt(h.HandleGetForkInfo))

	// repo 에 traversal(..) 포함 → path 검증 400
	req := httptest.NewRequest(http.MethodGet, "/repos/alice/be..ev/fork", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	// 회귀: traversal 경로는 400
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

func TestHandler_SyncUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := newRoot(t)
	_, srcCommit := newBareRepoAt(t, root, "alice", "proj")
	h := newHandler(t, root)

	// 포크 생성 (service 직접 사용)
	res, err := h.service.ForkRepository(stdctx.Background(), "alice", "proj", "bob", "proj", "")
	require.NoError(t, err)
	srcPath := filepath.Join(root, "alice", "proj.git")
	addCommitOnBranch(t, srcPath, "main", "world\n", srcCommit)
	_ = res

	router := gin.New()
	router.POST("/repos/:owner/:repo/fork/sync", adaptNoBody(h.HandleSyncUpstream, syncUpstreamErrorMapper))

	req := httptest.NewRequest(http.MethodPost, "/repos/bob/proj/fork/sync", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp SyncUpstreamResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	require.Len(t, resp.BranchesSynced, 1)
	assert.Equal(t, "main", resp.BranchesSynced[0].Name)
}

func TestHandler_SyncUpstream_NotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := newRoot(t)
	h := newHandler(t, root)

	router := gin.New()
	router.POST("/repos/:owner/:repo/fork/sync", adaptNoBody(h.HandleSyncUpstream, syncUpstreamErrorMapper))

	req := httptest.NewRequest(http.MethodPost, "/repos/ghost/missing/fork/sync", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	// 회귀: 메타데이터 없는(=fork 아님) repo → mapper 가 400 매핑
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

// --- routes.go 테스트 ---

func TestRegisterRoutes_SpecOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// 회귀: nil handler (spec-only) 등록은 panic 하지 않아야 함
	assert.NotPanics(t, func() {
		RegisterRoutes(r, nil)
	})
}

func TestRegisterRoutes_WithHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := newRoot(t)
	newBareRepoAt(t, root, "alice", "proj")
	h := newHandler(t, root)

	r := gin.New()
	grp := r.Group("/repos/:owner/:repo")
	require.NotPanics(t, func() {
		RegisterRoutes(grp, h)
	})

	// 등록된 라우트로 실제 포크 호출 → 201 (WithStatus(Created))
	body := strings.NewReader(`{"owner":"bob","repo":"proj"}`)
	req := httptest.NewRequest(http.MethodPost, "/repos/alice/proj/forks", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusCreated, w.Code, w.Body.String())
}

func TestForkRepositoryErrorMapper(t *testing.T) {
	t.Run("HTTPError takes precedence", func(t *testing.T) {
		status, _ := forkRepositoryErrorMapper(typed.NewHTTPError(http.StatusBadRequest, "bad"))
		assert.Equal(t, http.StatusBadRequest, status)
	})
	t.Run("ErrSourceNotFound -> 404", func(t *testing.T) {
		status, _ := forkRepositoryErrorMapper(ErrSourceNotFound)
		assert.Equal(t, http.StatusNotFound, status)
	})
	t.Run("ErrForkAlreadyExists -> 409", func(t *testing.T) {
		status, _ := forkRepositoryErrorMapper(ErrForkAlreadyExists)
		assert.Equal(t, http.StatusConflict, status)
	})
	t.Run("unknown -> 500", func(t *testing.T) {
		status, _ := forkRepositoryErrorMapper(errors.New("boom"))
		assert.Equal(t, http.StatusInternalServerError, status)
	})
}

func TestSyncUpstreamErrorMapper(t *testing.T) {
	t.Run("HTTPError takes precedence", func(t *testing.T) {
		status, _ := syncUpstreamErrorMapper(typed.NewHTTPError(http.StatusTeapot, "x"))
		assert.Equal(t, http.StatusTeapot, status)
	})
	t.Run("ErrNotAFork -> 400", func(t *testing.T) {
		status, _ := syncUpstreamErrorMapper(ErrNotAFork)
		assert.Equal(t, http.StatusBadRequest, status)
	})
	t.Run("ErrUpstreamNotFound -> 404", func(t *testing.T) {
		status, _ := syncUpstreamErrorMapper(ErrUpstreamNotFound)
		assert.Equal(t, http.StatusNotFound, status)
	})
	t.Run("unknown -> 500", func(t *testing.T) {
		status, _ := syncUpstreamErrorMapper(errors.New("boom"))
		assert.Equal(t, http.StatusInternalServerError, status)
	})
}
