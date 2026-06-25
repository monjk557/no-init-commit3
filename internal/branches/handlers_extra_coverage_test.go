package branches

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// handlers_extra_coverage_test.go — 실제 커밋/브랜치를 가진 bare 레포로
// HandleBranchStats / HandleValidateReopen / HandleBulkDeleteBranches 커버.

// buildBareRepoWithBranches는 master/feature가 1커밋씩 분기한 bare 레포를
// baseDir/owner/repo.git 에 만든다.
func buildBareRepoWithBranches(t *testing.T, baseDir, owner, repo string) {
	t.Helper()
	work := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(work, "init", "-q", "-b", "master")
	run(work, "config", "user.email", "t@example.com")
	run(work, "config", "user.name", "T")
	run(work, "commit", "--allow-empty", "-q", "-m", "base")
	run(work, "branch", "feature")
	run(work, "commit", "--allow-empty", "-q", "-m", "master2")
	run(work, "checkout", "-q", "feature")
	run(work, "commit", "--allow-empty", "-q", "-m", "feat2")
	run(work, "checkout", "-q", "master")

	barePath := filepath.Join(baseDir, owner, repo+".git")
	if err := os.MkdirAll(filepath.Dir(barePath), 0o755); err != nil {
		t.Fatal(err)
	}
	run("", "clone", "--bare", "-q", work, barePath)
}

func statsRouter(h *BranchOperationsHandler) *gin.Engine {
	r := gin.New()
	r.POST("/repository/:owner/:repo/branches/stats", adapt(h.HandleBranchStats))
	r.POST("/repository/:owner/:repo/branches/validate-reopen", adapt(h.HandleValidateReopen))
	r.POST("/repository/:owner/:repo/branches/bulk-delete", adapt(h.HandleBulkDeleteBranches))
	return r
}

func post(r *gin.Engine, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestHandleBranchStats_Coverage(t *testing.T) {
	h, root := newHandler(t)
	buildBareRepoWithBranches(t, root, "o", "r")
	r := statsRouter(h)

	// happy path
	w := post(r, "/repository/o/r/branches/stats", `{"branches":["feature","master"]}`)
	requireStatus(t, w, http.StatusOK)
	requireBodyContains(t, w, "feature")

	// empty list → 400 (binding min=1)
	w = post(r, "/repository/o/r/branches/stats", `{"branches":[]}`)
	requireStatus(t, w, http.StatusBadRequest)

	// missing repo → graceful zero stats (path valid, git fails per-branch → 200)
	w = post(r, "/repository/o/missing/branches/stats", `{"branches":["feature"]}`)
	requireStatus(t, w, http.StatusOK)
}

func TestHandleValidateReopen_Coverage(t *testing.T) {
	h, root := newHandler(t)
	buildBareRepoWithBranches(t, root, "o", "r")
	r := statsRouter(h)

	// both branches exist → 200
	w := post(r, "/repository/o/r/branches/validate-reopen",
		`{"source_branch_name":"feature","target_branch_name":"master"}`)
	requireStatus(t, w, http.StatusOK)

	// source missing → 409
	w = post(r, "/repository/o/r/branches/validate-reopen",
		`{"source_branch_name":"ghost","target_branch_name":"master"}`)
	requireStatus(t, w, http.StatusConflict)

	// missing required field → 400
	w = post(r, "/repository/o/r/branches/validate-reopen", `{"source_branch_name":"feature"}`)
	requireStatus(t, w, http.StatusBadRequest)

	// missing repo → source branch not found → 409
	w = post(r, "/repository/o/missing/branches/validate-reopen",
		`{"source_branch_name":"feature","target_branch_name":"master"}`)
	requireStatus(t, w, http.StatusConflict)
}

func TestHandleBulkDeleteBranches_Coverage(t *testing.T) {
	h, root := newHandler(t)
	buildBareRepoWithBranches(t, root, "o", "r")
	r := statsRouter(h)

	// delete existing branch → 200 success
	w := post(r, "/repository/o/r/branches/bulk-delete", `{"branches":["feature"]}`)
	requireStatus(t, w, http.StatusOK)
	requireBodyContains(t, w, "feature")

	// invalid branch name → 200 with per-item failure (handler returns 200)
	w = post(r, "/repository/o/r/branches/bulk-delete", `{"branches":["bad..name"]}`)
	requireStatus(t, w, http.StatusOK)
	requireBodyContains(t, w, "invalid branch name")

	// empty list → 400
	w = post(r, "/repository/o/r/branches/bulk-delete", `{"branches":[]}`)
	requireStatus(t, w, http.StatusBadRequest)
}
