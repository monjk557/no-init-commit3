package gc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 64 hex char 의 mock OID 생성 helper (1 자리 prefix 로 충분히 distinct).
func mkOID(prefix byte) string {
	return string(prefix) + strings.Repeat("0", 63)
}

func lfsPointerContent(oid string, size int64) string {
	return fmt.Sprintf(
		"version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize %d\n",
		oid, size,
	)
}

// initBareRepo 는 bare git repo 를 생성하고 workdir 를 통해 첫 commit 을 만든다.
// workdir 안의 파일들을 commit 하고 bare repo 에 push.
//
// 반환값: bare repo path.
func initBareRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	tempDir := t.TempDir()
	bareDir := filepath.Join(tempDir, "bare.git")
	workDir := filepath.Join(tempDir, "work")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	require.NoError(t, os.MkdirAll(workDir, 0o755))

	// bare init
	require.NoError(t, runGit(t, "", "init", "--bare", "-b", "main", bareDir))

	// work init + commit
	require.NoError(t, runGit(t, workDir, "init", "-b", "main"))
	require.NoError(t, runGit(t, workDir, "config", "user.email", "test@example.com"))
	require.NoError(t, runGit(t, workDir, "config", "user.name", "Test"))

	for path, content := range files {
		fullPath := filepath.Join(workDir, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0o755))
		require.NoError(t, os.WriteFile(fullPath, []byte(content), 0o644))
	}
	require.NoError(t, runGit(t, workDir, "add", "."))
	require.NoError(t, runGit(t, workDir, "commit", "-m", "fixture"))
	require.NoError(t, runGit(t, workDir, "remote", "add", "origin", bareDir))
	require.NoError(t, runGit(t, workDir, "push", "origin", "main"))

	return bareDir
}

func runGit(t *testing.T, workDir string, args ...string) error {
	t.Helper()
	cmd := exec.Command("git", args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("git %v: %s", args, out)
		return err
	}
	return nil
}

func TestScanReachableOIDs_LFSPointersDetected(t *testing.T) {
	oid1 := mkOID('a')
	oid2 := mkOID('b')

	bareDir := initBareRepo(t, map[string]string{
		"README.md":      "# Test repo (no LFS)\n",
		"design.psd":     lfsPointerContent(oid1, 5_000_000),
		"video/clip.mp4": lfsPointerContent(oid2, 100_000_000),
	})

	resp, err := ScanReachableOIDs(context.Background(), bareDir)
	require.NoError(t, err)
	require.NotNil(t, resp)

	sort.Strings(resp.ReachableOIDs)
	assert.Equal(t, []string{oid1, oid2}, resp.ReachableOIDs)
	assert.Equal(t, 2, resp.LFSPointerBlobs)
	assert.GreaterOrEqual(t, resp.ScannedBlobs, 1, "최소한 README.md 의 blob 은 scan 대상")
	assert.False(t, resp.Truncated)
	assert.True(t, resp.Success)
	assert.GreaterOrEqual(t, resp.ScannedRefs, 1)
}

func TestScanReachableOIDs_NoLFSPointers(t *testing.T) {
	bareDir := initBareRepo(t, map[string]string{
		"README.md":   "# Pure repo\n",
		"src/main.go": "package main\nfunc main() {}\n",
	})

	resp, err := ScanReachableOIDs(context.Background(), bareDir)
	require.NoError(t, err)
	assert.Empty(t, resp.ReachableOIDs)
	assert.Zero(t, resp.LFSPointerBlobs)
	assert.False(t, resp.Truncated)
}

func TestScanReachableOIDs_DedupesAcrossPaths(t *testing.T) {
	oid := mkOID('c')
	// 같은 OID 가 두 경로에 들어 있음 — set 으로 dedup 되어야 함.
	bareDir := initBareRepo(t, map[string]string{
		"a/file.bin": lfsPointerContent(oid, 1024),
		"b/file.bin": lfsPointerContent(oid, 1024),
	})

	resp, err := ScanReachableOIDs(context.Background(), bareDir)
	require.NoError(t, err)
	assert.Equal(t, []string{oid}, resp.ReachableOIDs)
	// 같은 content 는 git 이 자동 dedup — 1개 blob, 1개 pointer
	assert.Equal(t, 1, resp.LFSPointerBlobs)
}

func TestScanReachableOIDs_SkipsLargeBlobs(t *testing.T) {
	// 300 byte 초과 blob 은 LFS pointer 가 아니므로 candidate 에서 제외 →
	// content read 비용 절감. cap 안의 blob 만 scan 대상.
	largeContent := strings.Repeat("x", 1_000)
	oid := mkOID('d')
	bareDir := initBareRepo(t, map[string]string{
		"big.txt":     largeContent,
		"pointer.bin": lfsPointerContent(oid, 2_000),
	})

	resp, err := ScanReachableOIDs(context.Background(), bareDir)
	require.NoError(t, err)
	assert.Equal(t, []string{oid}, resp.ReachableOIDs)
	assert.Equal(t, 1, resp.LFSPointerBlobs)
}

func TestScanReachableOIDs_IgnoresUnreachableBlobs(t *testing.T) {
	// 첫 commit 에 LFS pointer 가 있고, force-push 로 unreachable 시켰을 때
	// rev-list --all 이 해당 blob 을 더 이상 enumerate 하지 않는지.
	oidReachable := mkOID('a')
	oidUnreachable := mkOID('e')

	tempDir := t.TempDir()
	bareDir := filepath.Join(tempDir, "bare.git")
	workDir := filepath.Join(tempDir, "work")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	require.NoError(t, os.MkdirAll(workDir, 0o755))

	require.NoError(t, runGit(t, "", "init", "--bare", "-b", "main", bareDir))
	require.NoError(t, runGit(t, workDir, "init", "-b", "main"))
	require.NoError(t, runGit(t, workDir, "config", "user.email", "test@example.com"))
	require.NoError(t, runGit(t, workDir, "config", "user.name", "Test"))

	// 첫 commit — unreachable 이 될 LFS pointer
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "tmp.bin"),
		[]byte(lfsPointerContent(oidUnreachable, 100)), 0o644))
	require.NoError(t, runGit(t, workDir, "add", "."))
	require.NoError(t, runGit(t, workDir, "commit", "-m", "first"))

	// orphan branch 로 reset — 첫 commit 의 blob 은 reachable 안 됨
	require.NoError(t, runGit(t, workDir, "checkout", "--orphan", "fresh"))
	require.NoError(t, runGit(t, workDir, "rm", "-rf", "."))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "kept.bin"),
		[]byte(lfsPointerContent(oidReachable, 200)), 0o644))
	require.NoError(t, runGit(t, workDir, "add", "."))
	require.NoError(t, runGit(t, workDir, "commit", "-m", "fresh"))
	require.NoError(t, runGit(t, workDir, "branch", "-D", "main"))
	require.NoError(t, runGit(t, workDir, "branch", "-m", "main"))
	require.NoError(t, runGit(t, workDir, "remote", "add", "origin", bareDir))
	require.NoError(t, runGit(t, workDir, "push", "-f", "origin", "main"))

	resp, err := ScanReachableOIDs(context.Background(), bareDir)
	require.NoError(t, err)
	// bare repo 에 push 한 ref 에서 도달 가능한 oid 만 있어야 함
	assert.Contains(t, resp.ReachableOIDs, oidReachable)
	assert.NotContains(t, resp.ReachableOIDs, oidUnreachable, "unreachable OID 는 결과에 포함되면 안 됨")
}
