package gc

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/config"
)

// handler_extra_coverage_test.go — NewHandler + HandleScanReachableOIDs를 실제 레포로 커버.

func buildGCRepo(t *testing.T, root, owner, repo string) {
	t.Helper()
	work := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "c1")

	bare := filepath.Join(root, owner, repo+".git")
	if err := os.MkdirAll(filepath.Dir(bare), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "clone", "--bare", "-q", work, bare)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
}

func TestLFSGC_HandleScanReachableOIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	buildGCRepo(t, root, "o", "r")
	h, err := NewHandler(&config.Config{
		Logging: config.LoggingConfig{Level: "error", Format: "json"},
		Git:     config.GitConfig{RepoRoot: root},
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	ctx := func(owner, repo string) *gin.Context {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/x", nil)
		c.Params = gin.Params{{Key: "owner", Value: owner}, {Key: "repo", Value: repo}}
		return c
	}

	// happy: LFS 포인터 없는 레포 → 빈 결과, 에러 없음
	if _, err := h.HandleScanReachableOIDs(ctx("o", "r")); err != nil {
		t.Fatalf("happy: %v", err)
	}
	// 존재하지 않는 repo → 404
	if _, err := h.HandleScanReachableOIDs(ctx("o", "missing")); err == nil {
		t.Error("expected error for missing repo")
	}
}
