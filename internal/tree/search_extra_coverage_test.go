package tree

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/config"
)

// search_extra_coverage_test.go — SearchFiles(git ls-tree) + HandleTreeSearch 커버.

func buildSearchRepo(t *testing.T, root, owner, repo string) string {
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
	for _, f := range []string{"main.go", "README.md", "src/util.go", "src/helper.go"} {
		p := filepath.Join(work, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
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
	return bare
}

func TestSearchFiles(t *testing.T) {
	root := t.TempDir()
	bare := buildSearchRepo(t, root, "o", "r")

	// ".go" 검색 → 3개 매치
	entries, total, err := SearchFiles(bare, "main", "go", 10)
	if err != nil {
		t.Fatalf("SearchFiles: %v", err)
	}
	if total < 3 || len(entries) < 3 {
		t.Errorf("expected >=3 go files, got total=%d len=%d", total, len(entries))
	}

	// limit 적용 → results <= limit 이지만 total은 전체
	entries2, total2, err := SearchFiles(bare, "main", "go", 1)
	if err != nil || len(entries2) != 1 || total2 < 3 {
		t.Errorf("limit: len=%d total=%d err=%v", len(entries2), total2, err)
	}

	// 빈 query → 전체 매치
	_, totalAll, err := SearchFiles(bare, "main", "", 100)
	if err != nil || totalAll < 4 {
		t.Errorf("empty query total=%d err=%v", totalAll, err)
	}

	// 잘못된 ref → 에러
	if _, _, err := SearchFiles(bare, "no-such-ref", "go", 10); err == nil {
		t.Error("expected error for bad ref")
	}
}

func TestHandleTreeSearch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	buildSearchRepo(t, root, "o", "r")
	h, err := NewTreeHandler(&config.Config{
		Logging: config.LoggingConfig{Level: "error", Format: "json"},
		Git:     config.GitConfig{RepoRoot: root},
	})
	if err != nil {
		t.Fatalf("NewTreeHandler: %v", err)
	}

	ctx := func(owner, repo string) *gin.Context {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/x", nil)
		c.Params = gin.Params{{Key: "owner", Value: owner}, {Key: "repo", Value: repo}}
		return c
	}

	// happy
	if _, err := h.HandleTreeSearch(ctx("o", "r"), TreeSearchRequest{Ref: "main", Query: "go", Limit: 10}); err != nil {
		t.Fatalf("happy: %v", err)
	}
	// empty query → 400
	if _, err := h.HandleTreeSearch(ctx("o", "r"), TreeSearchRequest{Ref: "main", Query: ""}); err == nil {
		t.Error("expected 400 for empty query")
	}
	// missing repo → 404
	if _, err := h.HandleTreeSearch(ctx("o", "missing"), TreeSearchRequest{Ref: "main", Query: "go"}); err == nil {
		t.Error("expected 404 for missing repo")
	}
}
