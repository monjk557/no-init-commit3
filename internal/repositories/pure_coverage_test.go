package repositories

import (
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

// =============================================================================
// url_parse.go — 추가 경로 (git://, ssh 무포트, scp user@ 없음, 빈 owner 등)
// =============================================================================

func TestParseRepoURL_GitScheme(t *testing.T) {
	t.Parallel()
	got := ParseRepoURL("git://example.com/owner/repo.git")
	if assert.NotNil(t, got) {
		assert.Equal(t, "example.com", got.Host)
		assert.Equal(t, "owner", got.Owner)
		assert.Equal(t, "repo", got.Repo)
	}
}

func TestParseRepoURL_SSHNoUserNoPort(t *testing.T) {
	t.Parallel()
	got := ParseRepoURL("ssh://example.com/owner/repo")
	if assert.NotNil(t, got) {
		assert.Equal(t, "example.com", got.Host)
		assert.Equal(t, "owner", got.Owner)
		assert.Equal(t, "repo", got.Repo)
	}
}

func TestParseRepoURL_SCPWithoutUser(t *testing.T) {
	t.Parallel()
	// user@ 없는 scp-style: host:owner/repo
	got := ParseRepoURL("example.com:owner/repo.git")
	if assert.NotNil(t, got) {
		assert.Equal(t, "example.com", got.Host)
		assert.Equal(t, "owner", got.Owner)
		assert.Equal(t, "repo", got.Repo)
	}
}

func TestParseRepoURL_AdditionalRejects(t *testing.T) {
	t.Parallel()
	cases := []string{
		"   ",                     // 공백만 → trim 후 빈 문자열
		":owner/repo",             // colon 이 맨 앞 (colon<=0)
		"git@:owner/repo",         // host 부분 비어있음
		"git@host:",               // pathPart 비어있음
		"git@host:123",            // pathPart 에 / 없음 (port 형태)
		"https://host/onlyone",    // owner/repo 부족
		"https:///owner/repo",     // host 비어있음
		"ftp://host/owner/repo",   // 지원하지 않는 scheme
		"https://host/owner/.git", // repo 가 .git 만 → 빈 repo
		"https://host//repo.git",  // owner 비어있음 (trim 후 single seg)
	}
	for _, in := range cases {
		assert.Nil(t, ParseRepoURL(in), "%q should be rejected", in)
	}
}

func TestParseRepoURL_DeepNamespaceTrailingSlash(t *testing.T) {
	t.Parallel()
	// trailing slash + 깊은 namespace → owner 가 join 됨
	got := ParseRepoURL("https://gitlab.com/a/b/c/repo.git/")
	if assert.NotNil(t, got) {
		assert.Equal(t, "gitlab.com", got.Host)
		assert.Equal(t, "a/b/c", got.Owner)
		assert.Equal(t, "repo", got.Repo)
	}
}

// =============================================================================
// repo_locks.go — per-repo RWLock 캐싱 / 동시성
// =============================================================================

func TestGetRepoRWLock_SamePathSameLock(t *testing.T) {
	t.Parallel()
	l1 := GetRepoRWLock("/tmp/lock-test-a.git")
	l2 := GetRepoRWLock("/tmp/lock-test-a.git")
	assert.Same(t, l1, l2, "같은 path 는 동일 lock 인스턴스를 반환해야 한다")
}

func TestGetRepoRWLock_DifferentPathDifferentLock(t *testing.T) {
	t.Parallel()
	l1 := GetRepoRWLock("/tmp/lock-test-b.git")
	l2 := GetRepoRWLock("/tmp/lock-test-c.git")
	assert.NotSame(t, l1, l2, "다른 path 는 별도 lock 을 반환해야 한다")
}

func TestGetRepoRWLock_Usable(t *testing.T) {
	t.Parallel()
	l := GetRepoRWLock("/tmp/lock-test-usable.git")
	// 실제로 lock/unlock 이 동작해야 함
	l.Lock()
	l.Unlock()
	l.RLock()
	l.RUnlock()
}

func TestGetRepoRWLock_ConcurrentGet(t *testing.T) {
	t.Parallel()
	// 동시에 같은 path 로 get 해도 동일 인스턴스 + race 없음 (go test -race 대비)
	const path = "/tmp/lock-test-concurrent.git"
	var wg sync.WaitGroup
	locks := make([]*sync.RWMutex, 16)
	for i := range locks {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			locks[idx] = GetRepoRWLock(path)
		}(i)
	}
	wg.Wait()
	for i := 1; i < len(locks); i++ {
		assert.Same(t, locks[0], locks[i])
	}
}

// =============================================================================
// errors.go / validator.go error types — Error() / Unwrap()
// =============================================================================

func TestSentinelErrors_Distinct(t *testing.T) {
	t.Parallel()
	// 각 sentinel 이 서로 구분되는 식별자를 가져야 함
	all := []error{ErrInvalidRef, ErrUnsupportedRef, ErrAmbiguousRef, ErrObjectNotFound, ErrRefNotFound}
	for i := 0; i < len(all); i++ {
		assert.NotEmpty(t, all[i].Error())
		for j := i + 1; j < len(all); j++ {
			assert.NotErrorIs(t, all[i], all[j])
		}
	}
}

func TestPathError_ErrorAndUnwrap(t *testing.T) {
	t.Parallel()
	base := errors.New("boom")

	withPath := &PathError{Op: "validate", Path: "/repos/x.git", Err: base}
	assert.Contains(t, withPath.Error(), "validate")
	assert.Contains(t, withPath.Error(), "/repos/x.git")
	assert.Contains(t, withPath.Error(), "boom")
	assert.ErrorIs(t, withPath, base)
	assert.Same(t, base, withPath.Unwrap())

	noPath := &PathError{Op: "generate", Err: base}
	assert.Contains(t, noPath.Error(), "generate")
	assert.NotContains(t, noPath.Error(), "for path")
	assert.ErrorIs(t, noPath, base)
}

func TestValidationError_Error(t *testing.T) {
	t.Parallel()
	withVal := &ValidationError{Component: "owner", Value: "bad name", Reason: "has space"}
	assert.Contains(t, withVal.Error(), "owner")
	assert.Contains(t, withVal.Error(), "bad name")
	assert.Contains(t, withVal.Error(), "has space")

	noVal := &ValidationError{Component: "global_config", Reason: "cannot be nil"}
	assert.Contains(t, noVal.Error(), "global_config")
	assert.Contains(t, noVal.Error(), "cannot be nil")
	assert.NotContains(t, noVal.Error(), "''")
}

func TestRepositoryError_Error(t *testing.T) {
	t.Parallel()
	e := &RepositoryError{Path: "/repos/x.git", Type: "not_found", Detail: "missing HEAD"}
	assert.Contains(t, e.Error(), "/repos/x.git")
	assert.Contains(t, e.Error(), "not_found")
	assert.Contains(t, e.Error(), "missing HEAD")
}

// =============================================================================
// gitmodules.go helpers — isGitmodulesMissingError / IsEmpty / LookupByPath nil
// =============================================================================

func TestIsGitmodulesMissingError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		msg  string
		want bool
	}{
		{"", false},
		{"fatal: path '.gitmodules' does not exist in 'HEAD'", true},
		{"fatal: path '.gitmodules' exists on disk, but not in 'HEAD'", true},
		{"fatal: Not a valid object name HEAD:.gitmodules", true},
		{"some unrelated git failure", false},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, isGitmodulesMissingError(c.msg), "msg=%q", c.msg)
	}
}

func TestGitmodulesIndex_NilSafe(t *testing.T) {
	t.Parallel()
	var idx *GitmodulesIndex
	assert.True(t, idx.IsEmpty())
	assert.Nil(t, idx.LookupByPath("anything"))

	empty := &GitmodulesIndex{}
	assert.True(t, empty.IsEmpty())
	assert.Nil(t, empty.LookupByPath("anything"))
}

func TestParseSubmoduleSectionHeader_Rejects(t *testing.T) {
	t.Parallel()
	// 직접 호출로 ok=false 분기 커버
	for _, line := range []string{
		`[core]`,
		`[submodule]`,       // 따옴표 없는 이름
		`[submodule ""]`,    // 빈 이름
		`[remote "origin"]`, // submodule 아님
	} {
		_, ok := parseSubmoduleSectionHeader(line)
		assert.False(t, ok, "line=%q", line)
	}
	name, ok := parseSubmoduleSectionHeader(`[submodule "vendor/x"]`)
	assert.True(t, ok)
	assert.Equal(t, "vendor/x", name)
}
