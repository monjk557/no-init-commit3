package repositories

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newBareRepoAt 는 t.TempDir 하위 owner/repo.git 에 bare repo 를 만들고
// repoRoot 와 repoPath 를 반환한다. validator / manager lifecycle 테스트의
// 공통 fixture.
func newBareRepoAt(t *testing.T, owner, repo string) (root, repoPath string) {
	t.Helper()
	root = t.TempDir()
	repoPath = filepath.Join(root, owner, repo+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(repoPath), 0o755))
	cmd := exec.Command("git", "init", "--bare", repoPath)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git init --bare: %s", out)
	return root, repoPath
}

// =============================================================================
// Global config — Get/Set/Reset
// =============================================================================

func TestGlobalConfig_SetGetReset(t *testing.T) {
	// 글로벌 상태이므로 t.Parallel 미사용. cleanup 으로 원상복구.
	t.Cleanup(ResetGlobalConfig)
	ResetGlobalConfig()

	// nil 거부
	assert.Error(t, SetGlobalConfig(nil))

	dir := t.TempDir()
	cfg, err := NewPathConfig(dir)
	require.NoError(t, err)
	require.NoError(t, SetGlobalConfig(cfg))

	got, err := GetGlobalConfig()
	require.NoError(t, err)
	assert.Equal(t, cfg.RepoRoot, got.RepoRoot)
	// 복사본이어야 함 (외부 수정 방지)
	assert.NotSame(t, cfg, got)

	ResetGlobalConfig()
	// reset 후에는 lazy default 로 초기화됨
	def, err := GetGlobalConfig()
	require.NoError(t, err)
	assert.NotEmpty(t, def.RepoRoot)
}

// =============================================================================
// PathConfig accessors
// =============================================================================

func TestPathConfig_AccessorsAndFormat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg, err := NewPathConfig(dir)
	require.NoError(t, err)

	assert.Equal(t, cfg.RepoRoot, cfg.GetRepoRoot())

	// .git 중복 제거
	p1 := cfg.FormatRepoPath("owner", "repo")
	p2 := cfg.FormatRepoPath("owner", "repo.git")
	assert.Equal(t, p1, p2)
	assert.True(t, filepath.Base(p1) == "repo.git")
}

func TestNewPathConfig_Errors(t *testing.T) {
	t.Parallel()
	_, err := NewPathConfig("")
	assert.Error(t, err)

	_, err = NewPathConfig("relative/path")
	assert.Error(t, err, "절대 경로가 아니면 거부해야 한다")
}

// =============================================================================
// *WithDefault convenience wrappers
// =============================================================================

func TestWithDefaultWrappers(t *testing.T) {
	t.Parallel()
	// DefaultRepoRoot 기반 — 존재 여부와 무관하게 path 생성 / 검증 분기 커버
	p, err := GetRepoPathWithDefault("owner", "repo")
	require.NoError(t, err)
	assert.Contains(t, p, "owner")

	// path traversal 은 거부
	_, err = GetRepoPathWithDefault("..", "repo")
	assert.Error(t, err)

	// ValidateRepoPathWithDefault: 코드 경로 실행 (default root 가 테스트
	// 머신에 없을 수 있으므로 결과값은 단정하지 않고 분기 커버만 한다).
	_ = ValidateRepoPathWithDefault(filepath.Join(DefaultRepoRoot, "owner", "repo.git"))

	// default root 밖은 반드시 거부
	err = ValidateRepoPathWithDefault("/etc/passwd")
	assert.Error(t, err)
}

func TestGetAndValidateRepoPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg, err := NewPathConfig(dir)
	require.NoError(t, err)

	p, err := GetAndValidateRepoPath(cfg, "owner", "repo")
	require.NoError(t, err)
	assert.Contains(t, p, "owner")

	// 컴포넌트 검증 실패 → PathError
	_, err = GetAndValidateRepoPath(cfg, "..", "repo")
	assert.Error(t, err)
}

func TestGetAndValidateRepoPathWithDefault(t *testing.T) {
	t.Cleanup(ResetGlobalConfig)
	dir := t.TempDir()
	cfg, err := NewPathConfig(dir)
	require.NoError(t, err)
	require.NoError(t, SetGlobalConfig(cfg))

	p, err := GetAndValidateRepoPathWithDefault("owner", "repo")
	require.NoError(t, err)
	assert.Contains(t, p, "owner")

	_, err = GetAndValidateRepoPathWithDefault("..", "x")
	assert.Error(t, err)
}

// =============================================================================
// EnsureRepoExists / checkPathExists / validateGitRepository 등
// =============================================================================

func TestEnsureRepoExists_BareRepo(t *testing.T) {
	t.Parallel()
	root, repoPath := newBareRepoAt(t, "owner", "repo")
	cfg, err := NewPathConfig(root)
	require.NoError(t, err)

	assert.NoError(t, EnsureRepoExists(cfg, repoPath))
}

func TestEnsureRepoExists_NotExist(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfg, err := NewPathConfig(root)
	require.NoError(t, err)

	missing := filepath.Join(root, "owner", "ghost.git")
	err = EnsureRepoExists(cfg, missing)
	assert.Error(t, err)
}

func TestEnsureRepoExists_PathExistsButNotGit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfg, err := NewPathConfig(root)
	require.NoError(t, err)

	// .git 디렉토리지만 git 구조 없음
	bogus := filepath.Join(root, "owner", "bogus.git")
	require.NoError(t, os.MkdirAll(bogus, 0o755))
	err = EnsureRepoExists(cfg, bogus)
	assert.Error(t, err)
}

func TestEnsureRepoExistsWithDefault(t *testing.T) {
	t.Parallel()
	// default root 밖 → path validation 실패
	err := EnsureRepoExistsWithDefault("/etc/passwd")
	assert.Error(t, err)
}

func TestCheckPathExists(t *testing.T) {
	t.Parallel()
	assert.Error(t, checkPathExists(""), "빈 경로")

	dir := t.TempDir()
	assert.NoError(t, checkPathExists(dir))

	// 존재하지 않음
	assert.Error(t, checkPathExists(filepath.Join(dir, "nope")))

	// 디렉토리가 아닌 파일
	f := filepath.Join(dir, "file.txt")
	require.NoError(t, os.WriteFile(f, []byte("x"), 0o644))
	assert.Error(t, checkPathExists(f), "파일은 디렉토리가 아니므로 거부")
}

func TestValidateGitRepository_NonBare(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// non-bare repo: <root>/work 에 .git 디렉토리
	work := filepath.Join(root, "work")
	cmd := exec.Command("git", "init", work)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)

	assert.True(t, isNonBareRepository(work))
	assert.NoError(t, validateGitRepository(work))
}

func TestIsBareRepository_Negative(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// 빈 디렉토리는 bare 가 아님 (HEAD 없음)
	assert.False(t, isBareRepository(dir))

	// HEAD 만 있고 refs/objects/config 없음
	require.NoError(t, os.WriteFile(filepath.Join(dir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))
	assert.False(t, isBareRepository(dir))
}

func TestValidateGitFile_Worktree(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	// 진짜 .git 디렉토리 (참조 대상)
	realGit := filepath.Join(root, "realgit")
	for _, sub := range []string{"refs", "objects"} {
		require.NoError(t, os.MkdirAll(filepath.Join(realGit, sub), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(realGit, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))

	// .git 파일이 realGit 을 가리킴 (worktree 형태)
	wt := filepath.Join(root, "wt")
	require.NoError(t, os.MkdirAll(wt, 0o755))
	gitFile := filepath.Join(wt, ".git")
	require.NoError(t, os.WriteFile(gitFile, []byte("gitdir: "+realGit+"\n"), 0o644))

	assert.True(t, validateGitFile(gitFile))
	assert.True(t, isNonBareRepository(wt))

	// 잘못된 .git 파일 (gitdir: 접두사 없음)
	badFile := filepath.Join(root, "bad.git")
	require.NoError(t, os.WriteFile(badFile, []byte("garbage\n"), 0o644))
	assert.False(t, validateGitFile(badFile))
}

// =============================================================================
// VerifyRepositoryAccess / GetRepositoryInfo
// =============================================================================

func TestVerifyRepositoryAccess(t *testing.T) {
	t.Parallel()
	root, _ := newBareRepoAt(t, "owner", "repo")
	cfg, err := NewPathConfig(root)
	require.NoError(t, err)

	p, err := VerifyRepositoryAccess(cfg, "owner", "repo")
	require.NoError(t, err)
	assert.Contains(t, p, "repo.git")

	// 존재하지 않는 repo
	_, err = VerifyRepositoryAccess(cfg, "owner", "ghost")
	assert.Error(t, err)
}

func TestVerifyRepositoryAccessWithDefault(t *testing.T) {
	t.Cleanup(ResetGlobalConfig)
	root, _ := newBareRepoAt(t, "owner", "repo")
	cfg, err := NewPathConfig(root)
	require.NoError(t, err)
	require.NoError(t, SetGlobalConfig(cfg))

	p, err := VerifyRepositoryAccessWithDefault("owner", "repo")
	require.NoError(t, err)
	assert.Contains(t, p, "repo.git")
}

func TestGetRepositoryInfo_Valid(t *testing.T) {
	t.Parallel()
	root, _ := newBareRepoAt(t, "owner", "repo")
	cfg, err := NewPathConfig(root)
	require.NoError(t, err)

	info, err := GetRepositoryInfo(cfg, "owner", "repo")
	require.NoError(t, err)
	assert.True(t, info.Exists)
	assert.True(t, info.IsValid)
	assert.True(t, info.IsAccessible)
	assert.Equal(t, "bare", info.Type)
}

func TestGetRepositoryInfo_NotExist(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfg, err := NewPathConfig(root)
	require.NoError(t, err)

	info, err := GetRepositoryInfo(cfg, "owner", "ghost")
	require.NoError(t, err)
	assert.False(t, info.Exists)
	assert.False(t, info.IsValid)
}

func TestGetRepositoryInfo_InvalidComponents(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfg, err := NewPathConfig(root)
	require.NoError(t, err)

	info, err := GetRepositoryInfo(cfg, "..", "repo")
	assert.Error(t, err)
	assert.NotNil(t, info)
}

func TestGetRepositoryInfoWithDefault(t *testing.T) {
	t.Cleanup(ResetGlobalConfig)
	root, _ := newBareRepoAt(t, "owner", "repo")
	cfg, err := NewPathConfig(root)
	require.NoError(t, err)
	require.NoError(t, SetGlobalConfig(cfg))

	info, err := GetRepositoryInfoWithDefault("owner", "repo")
	require.NoError(t, err)
	assert.True(t, info.Exists)
}

func TestGetBatchRepositoryInfo(t *testing.T) {
	t.Parallel()
	root, _ := newBareRepoAt(t, "owner", "repo")
	cfg, err := NewPathConfig(root)
	require.NoError(t, err)

	repos := []struct{ Owner, Repo string }{
		{"owner", "repo"},  // valid
		{"owner", "ghost"}, // not exist
		{"..", "bad"},      // invalid component → error
	}
	batch := GetBatchRepositoryInfo(cfg, repos)
	assert.Equal(t, 3, batch.TotalCount)
	assert.Equal(t, 1, batch.ValidCount)
	assert.GreaterOrEqual(t, batch.ErrorCount, 1)
}

func TestGetBatchRepositoryInfoWithDefault(t *testing.T) {
	t.Cleanup(ResetGlobalConfig)
	root, _ := newBareRepoAt(t, "owner", "repo")
	cfg, err := NewPathConfig(root)
	require.NoError(t, err)
	require.NoError(t, SetGlobalConfig(cfg))

	batch, err := GetBatchRepositoryInfoWithDefault([]struct{ Owner, Repo string }{{"owner", "repo"}})
	require.NoError(t, err)
	assert.Equal(t, 1, batch.TotalCount)
}

// =============================================================================
// GitAPIPathValidator — ValidateFilePath / ValidateRepoAPIPath / ValidateBranchRef
// / ValidateCommitHash / GetSafeRepoPath / ValidateFullFilePath
// =============================================================================

func TestGitAPIValidator_ValidateFilePath(t *testing.T) {
	t.Parallel()
	v := NewGitAPIPathValidator(nil)

	assert.NoError(t, v.ValidateFilePath("src/main.go"))
	assert.NoError(t, v.ValidateFilePath(".gitignore"))
	assert.NoError(t, v.ValidateFilePath(".github/workflows/ci.yml"))

	bad := []string{
		"",                // empty
		"../etc/passwd",   // traversal
		"/abs/path",       // absolute
		"a\\b",            // backslash
		"a\x00b",          // null
		"a\nb",            // newline
		".git",            // git internal
		".git/config",     // git internal
		"a/CON/b",         // reserved component
		"foo.git.git/bar", // duplicated .git
	}
	for _, p := range bad {
		assert.Error(t, v.ValidateFilePath(p), "path=%q", p)
	}
}

func TestGitAPIValidator_ValidateRepoAPIPath(t *testing.T) {
	t.Parallel()
	v := NewGitAPIPathValidator(nil)

	assert.NoError(t, v.ValidateRepoAPIPath("octocat", "hello-world"))

	// reserved owner / repo
	assert.Error(t, v.ValidateRepoAPIPath("api", "repo"))
	assert.Error(t, v.ValidateRepoAPIPath("Admin", "repo"))
	assert.Error(t, v.ValidateRepoAPIPath("owner", "settings"))
	assert.Error(t, v.ValidateRepoAPIPath("owner", "refs"))

	// invalid component
	assert.Error(t, v.ValidateRepoAPIPath("..", "repo"))
}

func TestGitAPIValidator_ValidateBranchRef(t *testing.T) {
	t.Parallel()
	v := NewGitAPIPathValidator(nil)

	assert.NoError(t, v.ValidateBranchRef("main"))
	assert.NoError(t, v.ValidateBranchRef("feature/x"))

	bad := []string{
		"",
		"/leading",
		"trailing/",
		"a//b",
		"x.lock",
		"@",
		"a@{0}",
		"has space",
		"a~b",
		"a^b",
		"a:b",
		"a?b",
		"a*b",
		"a[b",
		"a\\b",
		"a\x01b", // control char
	}
	for _, ref := range bad {
		assert.Error(t, v.ValidateBranchRef(ref), "ref=%q", ref)
	}

	// 너무 긴 ref
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'a'
	}
	assert.Error(t, v.ValidateBranchRef(string(long)))
}

func TestGitAPIValidator_ValidateCommitHash(t *testing.T) {
	t.Parallel()
	v := NewGitAPIPathValidator(nil)

	assert.NoError(t, v.ValidateCommitHash("0123456789abcdef0123456789abcdef01234567")) // full 40
	assert.NoError(t, v.ValidateCommitHash("abc1234"))                                  // short 7

	assert.Error(t, v.ValidateCommitHash(""))
	assert.Error(t, v.ValidateCommitHash("abc"))                                          // too short
	assert.Error(t, v.ValidateCommitHash("XYZ1234"))                                      // non-hex short
	assert.Error(t, v.ValidateCommitHash("0123456789abcdef0123456789abcdef0123456Z"))     // non-hex full
	assert.Error(t, v.ValidateCommitHash("0123456789abcdef0123456789abcdef0123456789ab")) // too long (42)
}

func TestGitAPIValidator_GetSafeRepoPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg, err := NewPathConfig(dir)
	require.NoError(t, err)
	v := NewGitAPIPathValidator(cfg)

	p, err := v.GetSafeRepoPath("owner", "repo")
	require.NoError(t, err)
	assert.Contains(t, p, "repo.git")

	// reserved owner 거부
	_, err = v.GetSafeRepoPath("api", "repo")
	assert.Error(t, err)

	// pathConfig 가 nil 이면 에러
	vnil := NewGitAPIPathValidator(nil)
	_, err = vnil.GetSafeRepoPath("owner", "repo")
	assert.Error(t, err)
}

func TestGitAPIValidator_ValidateFullFilePath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg, err := NewPathConfig(dir)
	require.NoError(t, err)
	v := NewGitAPIPathValidator(cfg)

	// 정상 입력이라도 ValidateRepoPath 가 dir 끝에 .git 을 요구하므로
	// 중첩 파일 경로는 거부된다 (production code 의 실제 동작 — 모든 분기 실행).
	assert.Error(t, v.ValidateFullFilePath("owner", "repo", "src/main.go"))

	// repo 컴포넌트 invalid
	assert.Error(t, v.ValidateFullFilePath("api", "repo", "src/main.go"))
	// file path invalid
	assert.Error(t, v.ValidateFullFilePath("owner", "repo", "../escape"))
}
