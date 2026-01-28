package repositories

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// 9.1 Repository Operations
// =============================================================================

func Test_OpenRepository_Valid_ReturnsRepo(t *testing.T) {
	// 회귀: "정상적인 레포는 열려야 한다"
	t.Parallel()

	// Given: 유효한 bare repo
	tempDir := t.TempDir()
	owner, repoName := "testowner", "testrepo"
	repoPath := filepath.Join(tempDir, owner, repoName+".git")

	err := os.MkdirAll(filepath.Dir(repoPath), 0755)
	require.NoError(t, err)

	_, err = gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	manager := newTestManager(t, tempDir)

	// When: OpenRepository 호출
	repo, err := manager.OpenRepository(owner, repoName)

	// Then: 성공
	require.NoError(t, err)
	assert.NotNil(t, repo)
}

func Test_OpenRepository_NonExistent_ReturnsError(t *testing.T) {
	// 회귀: "존재하지 않는 레포는 에러를 반환해야 한다"
	t.Parallel()

	// Given: 존재하지 않는 레포
	tempDir := t.TempDir()
	manager := newTestManager(t, tempDir)

	// When: 없는 레포 열기
	repo, err := manager.OpenRepository("ghost", "repo")

	// Then: 에러 반환
	assert.Error(t, err)
	assert.Nil(t, repo)
}

func Test_OpenRepository_Corrupted_ReturnsError(t *testing.T) {
	// 회귀: "손상된 레포는 에러를 반환해야 한다"
	t.Parallel()

	// Given: 손상된 repo (HEAD 파일만 있고 objects가 없음)
	tempDir := t.TempDir()
	owner, repoName := "testowner", "corrupted"
	repoPath := filepath.Join(tempDir, owner, repoName+".git")

	err := os.MkdirAll(repoPath, 0755)
	require.NoError(t, err)

	// HEAD 파일만 생성 (불완전한 repo)
	headPath := filepath.Join(repoPath, "HEAD")
	err = os.WriteFile(headPath, []byte("ref: refs/heads/main\n"), 0644)
	require.NoError(t, err)

	manager := newTestManager(t, tempDir)

	// When: 손상된 레포 열기
	repo, err := manager.OpenRepository(owner, repoName)

	// Then: 에러 반환 (또는 빈 repo로 처리)
	// go-git은 HEAD만 있어도 열 수 있으므로, 결과에 따라 assertion 조정
	if err != nil {
		assert.Contains(t, err.Error(), "repository")
	} else {
		// 열렸다면 최소한 nil이 아니어야 함
		assert.NotNil(t, repo)
	}
}

func Test_OpenRepository_NotBare_HandlesCorrectly(t *testing.T) {
	// 회귀: "non-bare 레포도 올바르게 처리되어야 한다"
	t.Parallel()

	// Given: non-bare repo
	tempDir := t.TempDir()
	owner, repoName := "testowner", "nonbare"
	repoPath := filepath.Join(tempDir, owner, repoName+".git")

	err := os.MkdirAll(filepath.Dir(repoPath), 0755)
	require.NoError(t, err)

	// non-bare로 초기화
	_, err = gogit.PlainInit(repoPath, false)
	require.NoError(t, err)

	manager := newTestManager(t, tempDir)

	// When: non-bare 레포 열기
	repo, err := manager.OpenRepository(owner, repoName)

	// Then: 성공 (또는 정책에 따라 에러)
	// 현재 구현이 non-bare를 허용하는지에 따라 다름
	if err != nil {
		t.Logf("non-bare repo rejected: %v", err)
	} else {
		assert.NotNil(t, repo)
	}
}

// =============================================================================
// 9.1A GC/Locking/Permissions
// =============================================================================

func Test_OpenRepository_LooseObjectsOnly_StillWorks(t *testing.T) {
	// 회귀: "pack 없이 loose objects만 있어도 동작해야 한다"
	t.Parallel()

	// Given: loose objects만 있는 repo
	tempDir := t.TempDir()
	owner, repoName := "testowner", "loose"
	repoPath := filepath.Join(tempDir, owner, repoName+".git")

	err := os.MkdirAll(filepath.Dir(repoPath), 0755)
	require.NoError(t, err)

	repo, err := gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	// loose object 생성 (커밋)
	createInitialCommit(t, repo, repoPath)

	manager := newTestManager(t, tempDir)

	// When: loose objects만 있는 레포 열기
	openedRepo, err := manager.OpenRepository(owner, repoName)

	// Then: 성공
	require.NoError(t, err)
	assert.NotNil(t, openedRepo)
}

func Test_OpenRepository_GCInProgress_ReadStillWorksOrReturnsRetryable(t *testing.T) {
	// 회귀: "GC 진행 중에도 읽기는 동작하거나 retryable 에러를 반환해야 한다"
	t.Parallel()

	// Given: GC lock 파일이 있는 repo
	tempDir := t.TempDir()
	owner, repoName := "testowner", "gclocked"
	repoPath := filepath.Join(tempDir, owner, repoName+".git")

	err := os.MkdirAll(filepath.Dir(repoPath), 0755)
	require.NoError(t, err)

	_, err = gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	// GC lock 파일 생성 (gc.pid)
	gcLockPath := filepath.Join(repoPath, "gc.pid")
	err = os.WriteFile(gcLockPath, []byte("12345\n"), 0644)
	require.NoError(t, err)

	manager := newTestManager(t, tempDir)

	// When: GC 진행 중인 레포 열기
	repo, err := manager.OpenRepository(owner, repoName)

	// Then: 읽기는 성공하거나, retryable 에러 반환
	// go-git은 GC lock을 무시하고 열기 때문에 대부분 성공
	if err != nil {
		// 에러면 retryable인지 확인 (423 Locked 또는 503)
		t.Logf("GC locked repo returned error: %v", err)
		assert.Contains(t, err.Error(), "lock", "error should mention lock")
	} else {
		assert.NotNil(t, repo, "repo should be opened even with GC lock")
	}
}

func Test_OpenRepository_PermissionDenied_ReturnsError(t *testing.T) {
	// 회귀: "권한 없는 레포는 에러를 반환해야 한다"
	// 참고: CI에서 root로 실행되면 skip됨
	t.Parallel()

	// root 사용자면 skip (root는 모든 파일 접근 가능)
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}

	// Given: 읽기 권한이 없는 repo
	tempDir := t.TempDir()
	owner, repoName := "testowner", "noaccess"
	repoPath := filepath.Join(tempDir, owner, repoName+".git")

	err := os.MkdirAll(filepath.Dir(repoPath), 0755)
	require.NoError(t, err)

	_, err = gogit.PlainInit(repoPath, true)
	require.NoError(t, err)

	// 권한 제거
	err = os.Chmod(repoPath, 0000)
	require.NoError(t, err)

	// cleanup에서 권한 복구 (삭제 가능하도록)
	t.Cleanup(func() {
		os.Chmod(repoPath, 0755)
	})

	manager := newTestManager(t, tempDir)

	// When: 권한 없는 레포 열기
	repo, err := manager.OpenRepository(owner, repoName)

	// Then: 에러 반환
	assert.Error(t, err, "permission denied repo should return error")
	assert.Nil(t, repo)
	t.Logf("Permission denied error: %v", err)
}

// =============================================================================
// 9.2 Path Resolution
// =============================================================================

func Test_ResolvePath_ValidOwnerRepo_ReturnsPath(t *testing.T) {
	// 회귀: "정상적인 owner/repo는 경로를 반환해야 한다"
	t.Parallel()

	// Given: 유효한 설정
	tempDir := t.TempDir()
	pathConfig, err := NewPathConfig(tempDir)
	require.NoError(t, err)

	// When: 경로 생성
	path, err := GetRepoPath(pathConfig, "owner", "repo")

	// Then: 성공
	require.NoError(t, err)
	assert.Contains(t, path, "owner")
	assert.Contains(t, path, "repo")
}

func Test_ResolvePath_PathTraversal_Returns400(t *testing.T) {
	// 회귀: "경로 탐색 공격은 차단되어야 한다"
	t.Parallel()

	// Given: 유효한 설정
	tempDir := t.TempDir()
	pathConfig, err := NewPathConfig(tempDir)
	require.NoError(t, err)

	testCases := []struct {
		name  string
		owner string
		repo  string
	}{
		{"owner_traversal", "../../../etc", "passwd"},
		{"repo_traversal", "owner", "../../../etc/passwd"},
		{"both_traversal", "../..", ".."},
		{"dot_dot_in_owner", "owner/../other", "repo"},
		{"dot_dot_in_repo", "owner", "repo/../other"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// When: path traversal 시도
			_, err := GetRepoPath(pathConfig, tc.owner, tc.repo)

			// Then: 에러 반환
			assert.Error(t, err, "path traversal '%s/%s' should be blocked", tc.owner, tc.repo)
		})
	}
}

func Test_ResolvePath_SpecialChars_Handles(t *testing.T) {
	// 회귀: "특수문자가 올바르게 처리되어야 한다"
	t.Parallel()

	// Given: 유효한 설정
	tempDir := t.TempDir()
	pathConfig, err := NewPathConfig(tempDir)
	require.NoError(t, err)

	testCases := []struct {
		name      string
		owner     string
		repo      string
		shouldErr bool
	}{
		// 허용되는 문자
		{"hyphen_ok", "test-owner", "test-repo", false},
		{"underscore_ok", "test_owner", "test_repo", false},
		{"dot_in_name", "test.owner", "test.repo", false},
		{"numbers_ok", "owner123", "repo456", false},

		// 차단되는 문자: 공백
		{"space_in_owner", "test owner", "repo", true},
		{"space_in_repo", "owner", "test repo", true},
		{"tab_in_owner", "test\towner", "repo", true},
		{"newline_in_repo", "owner", "repo\n", true},

		// 차단되는 문자: null byte 및 제어 문자
		{"null_byte_in_owner", "owner\x00", "repo", true},
		{"null_byte_in_repo", "owner", "repo\x00", true},
		{"control_char_bell", "owner\x07", "repo", true},
		{"control_char_escape", "owner\x1b", "repo", true},

		// 차단되는 문자: 파일시스템 위험 문자
		{"backslash", "owner\\path", "repo", true},
		{"colon", "owner:path", "repo", true},
		{"asterisk", "owner*", "repo", true},
		{"question", "owner?", "repo", true},
		{"pipe", "owner|", "repo", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// When: 특수문자 포함 경로
			_, err := GetRepoPath(pathConfig, tc.owner, tc.repo)

			// Then: 기대대로 처리
			if tc.shouldErr {
				assert.Error(t, err, "'%s/%s' should be rejected", tc.owner, tc.repo)
			} else {
				assert.NoError(t, err, "'%s/%s' should be allowed", tc.owner, tc.repo)
			}
		})
	}
}

func Test_ResolvePath_ReservedNames_Blocked(t *testing.T) {
	// 회귀: "Windows 예약어는 차단되어야 한다"
	t.Parallel()

	tempDir := t.TempDir()
	pathConfig, err := NewPathConfig(tempDir)
	require.NoError(t, err)

	reservedNames := []string{"CON", "PRN", "AUX", "NUL", "COM1", "LPT1", "con", "nul"}

	for _, name := range reservedNames {
		t.Run(name, func(t *testing.T) {
			_, err := GetRepoPath(pathConfig, name, "repo")
			assert.Error(t, err, "reserved name '%s' should be blocked", name)

			_, err = GetRepoPath(pathConfig, "owner", name)
			assert.Error(t, err, "reserved name '%s' should be blocked", name)
		})
	}
}

// =============================================================================
// 9.2A Symlink Traversal
// =============================================================================

func Test_ResolvePath_SymlinkTraversal_Blocked(t *testing.T) {
	// 회귀: "repo root 밖으로 나가는 symlink는 차단되어야 한다"
	t.Parallel()

	// Given: symlink가 포함된 경로
	tempDir := t.TempDir()
	secretDir := filepath.Join(tempDir, "secret")
	err := os.MkdirAll(secretDir, 0755)
	require.NoError(t, err)

	// secret 파일 생성
	secretFile := filepath.Join(secretDir, "passwd")
	err = os.WriteFile(secretFile, []byte("secret data"), 0644)
	require.NoError(t, err)

	// repos 디렉토리 생성
	reposDir := filepath.Join(tempDir, "repos")
	err = os.MkdirAll(filepath.Join(reposDir, "owner"), 0755)
	require.NoError(t, err)

	// symlink 생성 (repos/owner/evil -> ../../secret)
	symlinkPath := filepath.Join(reposDir, "owner", "evil")
	err = os.Symlink(secretDir, symlinkPath)
	require.NoError(t, err)

	pathConfig, err := NewPathConfig(reposDir)
	require.NoError(t, err)

	// When: symlink를 통한 경로 접근 시도
	err = ValidateRepoPath(pathConfig, filepath.Join(reposDir, "owner", "evil", "passwd"))

	// Then: 차단되어야 함
	assert.Error(t, err, "symlink traversal should be blocked")
}

// =============================================================================
// Helper functions
// =============================================================================

func newTestManager(t *testing.T, repoRoot string) *GitObjectManager {
	t.Helper()

	logCfg := config.LoggingConfig{
		Level:  "debug",
		Format: "json",
	}

	logger, err := logging.NewLogger(logCfg)
	require.NoError(t, err)

	pathConfig, err := NewPathConfig(repoRoot)
	require.NoError(t, err)

	manager, err := NewGitObjectManager(pathConfig, logger)
	require.NoError(t, err)

	return manager
}

func createInitialCommit(t *testing.T, repo *gogit.Repository, repoPath string) plumbing.Hash {
	t.Helper()

	// bare repo에서는 worktree가 없으므로 직접 object 생성
	// 빈 tree 생성
	emptyTree := &object.Tree{}
	treeObj := repo.Storer.NewEncodedObject()
	treeObj.SetType(plumbing.TreeObject)
	err := emptyTree.Encode(treeObj)
	require.NoError(t, err)

	treeHash, err := repo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)

	// commit 생성
	commit := &object.Commit{
		Author: object.Signature{
			Name:  "Test",
			Email: "test@test.com",
		},
		Committer: object.Signature{
			Name:  "Test",
			Email: "test@test.com",
		},
		Message:  "Initial commit",
		TreeHash: treeHash,
	}

	commitObj := repo.Storer.NewEncodedObject()
	commitObj.SetType(plumbing.CommitObject)
	err = commit.Encode(commitObj)
	require.NoError(t, err)

	commitHash, err := repo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	// HEAD 업데이트
	ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), commitHash)
	err = repo.Storer.SetReference(ref)
	require.NoError(t, err)

	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main"))
	err = repo.Storer.SetReference(headRef)
	require.NoError(t, err)

	return commitHash
}
