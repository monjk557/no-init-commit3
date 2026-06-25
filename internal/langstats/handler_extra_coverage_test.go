package langstats

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/config"
)

// handler_extra_coverage_test.go — NewHandler + HandleGetLanguageStats를 실제 레포로 커버.

func buildLangRepo(t *testing.T, root, owner, repo string) {
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
	if err := os.WriteFile(filepath.Join(work, "main.go"), []byte("package main\nfunc main(){}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("# hi\n"), 0o644); err != nil {
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

func langHandler(t *testing.T, root string) *Handler {
	t.Helper()
	h, err := NewHandler(&config.Config{
		Logging: config.LoggingConfig{Level: "error", Format: "json"},
		Git:     config.GitConfig{RepoRoot: root},
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

func langCtx(owner, repo, ref string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	url := "/repos/" + owner + "/" + repo + "/languages"
	if ref != "" {
		url += "?ref=" + ref
	}
	c.Request = httptest.NewRequest("GET", url, nil)
	c.Params = gin.Params{{Key: "owner", Value: owner}, {Key: "repo", Value: repo}}
	return c, w
}

func TestLangstats_HandleGetLanguageStats(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	buildLangRepo(t, root, "o", "r")
	h := langHandler(t, root)

	// happy path
	c, _ := langCtx("o", "r", "main")
	if _, err := h.HandleGetLanguageStats(c); err != nil {
		t.Fatalf("happy path err: %v", err)
	}

	// 기본 ref (HEAD)
	c2, _ := langCtx("o", "r", "")
	if _, err := h.HandleGetLanguageStats(c2); err != nil {
		t.Fatalf("default ref err: %v", err)
	}

	// 존재하지 않는 repo → 에러
	c3, _ := langCtx("o", "missing", "main")
	if _, err := h.HandleGetLanguageStats(c3); err == nil {
		t.Error("expected error for missing repo")
	}
}
