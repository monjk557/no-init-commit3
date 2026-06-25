package protocol

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/collabops/git-executor/internal/performance"
)

// upload_pack_extra_coverage_test.go — 실제 bare 레포 + 버퍼로 sideband writer,
// ls-refs, pack-objects(raw/http) 커버.

// buildBareRepoForPack는 커밋 1개를 가진 bare 레포 경로와 HEAD SHA를 반환한다.
func buildBareRepoForPack(t *testing.T) (string, string) {
	t.Helper()
	work := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run(work, "init", "-q", "-b", "main")
	run(work, "config", "user.email", "t@example.com")
	run(work, "config", "user.name", "T")
	run(work, "commit", "--allow-empty", "-q", "-m", "c1")
	sha := run(work, "rev-parse", "HEAD")

	bare := filepath.Join(t.TempDir(), "repo.git")
	run("", "clone", "--bare", "-q", work, bare)
	return bare, sha
}

func TestSidebandWriter(t *testing.T) {
	var buf bytes.Buffer
	sb := NewSidebandWriter(&buf, 0x01)
	if _, err := sb.Write([]byte("hello sideband")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := sb.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("expected sideband framed output")
	}
}

func TestHandleLsRefsNative(t *testing.T) {
	bare, _ := buildBareRepoForPack(t)
	ctx := context.Background()

	var buf bytes.Buffer
	pw := performance.NewOptimizedPktLineWriter(&buf)
	req := &LsRefsReq{Symrefs: true, Peel: true, RefPrefix: []string{"HEAD", "refs/heads/"}}
	if err := HandleLsRefsNative(ctx, bare, req, pw); err != nil {
		t.Fatalf("HandleLsRefsNative: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("expected ls-refs output")
	}
}

func TestRunPackObjects(t *testing.T) {
	bare, sha := buildBareRepoForPack(t)
	ctx := context.Background()

	var raw bytes.Buffer
	if err := RunGitPackObjectsRaw(ctx, bare, &FetchReq{Wants: []string{sha}}, &raw); err != nil {
		t.Fatalf("RunGitPackObjectsRaw: %v", err)
	}
	if raw.Len() == 0 {
		t.Error("expected raw pack data")
	}

	var http bytes.Buffer
	if err := RunPackObjectsForHTTP(ctx, bare, &FetchReq{Wants: []string{sha}}, &http); err != nil {
		t.Fatalf("RunPackObjectsForHTTP: %v", err)
	}
	if http.Len() == 0 {
		t.Error("expected sideband-framed pack data")
	}

	// no wants → error path
	if err := RunGitPackObjectsRaw(ctx, bare, &FetchReq{Wants: nil}, &bytes.Buffer{}); err == nil {
		t.Error("expected error for no wants")
	}
}
