package overview

import (
	"encoding/json"
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
)

// overview 의 default_branch 는 HEAD 에서 파생해야 한다.
//
// 메타데이터 DB 의 default_branch 는 push 이벤트로 비동기 갱신되므로, 읽기
// 경로가 그 값을 쓰면 "master 를 첫 push 했는데 화면은 여전히 main 을 찾는"
// 상태가 갱신 전까지 남는다. git 을 직접 보는 이 필드가 그 창을 없앤다.
func TestHandleGetRepositoryOverview_DefaultBranchFromHead(t *testing.T) {
	cases := []struct {
		name       string
		head       string
		withCommit bool
		wantBranch string
		wantEmpty  bool
	}{
		{
			name:       "초기 커밋 없는 저장소 — unborn HEAD 이름을 그대로 노출",
			head:       "main",
			withCommit: false,
			wantBranch: "main",
			wantEmpty:  true,
		},
		{
			name:       "master 로 첫 push 된 저장소 — HEAD 를 따라 master",
			head:       "master",
			withCommit: true,
			wantBranch: "master",
			wantEmpty:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler, tempDir, cleanup := setupTestHandler(t)
			defer cleanup()

			owner, repo := "acme", "svc"
			repoPath := filepath.Join(tempDir, owner, repo+".git")
			require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0o755))
			gitBare(t, tempDir, "init", "-q", "--bare", "-b", tc.head, repoPath)
			if tc.withCommit {
				tree := gitBareStdin(t, repoPath, "", "mktree")
				commit := gitBareStdin(t, repoPath, "init\n", "commit-tree", tree)
				gitBare(t, repoPath, "update-ref", "refs/heads/"+tc.head, commit)
			}

			router := gin.New()
			router.GET("/repos/:owner/:repo/overview", adaptQuery(handler.HandleGetRepositoryOverview))

			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest("GET", "/repos/"+owner+"/"+repo+"/overview", nil))
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())

			var resp RepositoryOverviewResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, tc.wantBranch, resp.DefaultBranch)
			assert.Equal(t, tc.wantEmpty, resp.IsEmpty)
		})
	}
}

func gitBare(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %s\n%s", strings.Join(args, " "), string(out))
	return strings.TrimSpace(string(out))
}

func gitBareStdin(t *testing.T, repoPath, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %s\n%s", strings.Join(args, " "), string(out))
	return strings.TrimSpace(string(out))
}
