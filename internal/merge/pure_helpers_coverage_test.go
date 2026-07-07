package merge

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 회귀: "executor.go 의 메시지/타입 헬퍼는 build tag 없는 기본 빌드에서도
// 커버되어야 한다" — executor_test.go 는 //go:build gogit 이라 기본 빌드에서
// 제외된다. 동일 로직을 native 타입 (NativeCommit) 기반으로 재검증한다.

func Test_generateMergeMessage_AllBranches_Cov(t *testing.T) {
	t.Parallel()

	req := &MergeRequest{SourceRef: "feature", TargetRef: "main"}

	ff := generateMergeMessage(&MergeResult{IsFastForward: true}, req)
	require.Contains(t, ff, "Fast-forward merge")
	require.Contains(t, ff, "feature")
	require.Contains(t, ff, "main")

	conflict := generateMergeMessage(&MergeResult{HasConflicts: true}, req)
	require.Contains(t, conflict, "completed with conflicts resolved")

	clean := generateMergeMessage(&MergeResult{}, req)
	require.Contains(t, clean, "completed successfully")
	require.NotContains(t, clean, "Fast-forward")
	require.NotContains(t, clean, "conflicts resolved")
}

func Test_getMergeType_AllBranches_Cov(t *testing.T) {
	t.Parallel()

	require.Equal(t, "fast-forward", getMergeType(&MergeResult{IsFastForward: true}))
	require.Equal(t, "three-way-with-conflicts", getMergeType(&MergeResult{HasConflicts: true}))
	require.Equal(t, "three-way", getMergeType(&MergeResult{}))
}

func Test_getMergeTypeForMethod_AllBranches_Cov(t *testing.T) {
	t.Parallel()

	require.Equal(t, "squash", getMergeTypeForMethod(MergeMethodSquash, &MergeResult{}))
	require.Equal(t, "rebase", getMergeTypeForMethod(MergeMethodRebase, &MergeResult{}))
	require.Equal(t, "fast-forward", getMergeTypeForMethod(MergeMethodMerge, &MergeResult{IsFastForward: true}))
	// empty method falls through to default → getMergeType
	require.Equal(t, "three-way", getMergeTypeForMethod(MergeMethod(""), &MergeResult{}))
}

func Test_commitToInfo_Native_Cov(t *testing.T) {
	t.Parallel()

	when := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	commit := &NativeCommit{
		Hash:    "abc123",
		Message: "fix: something",
		Author:  NativeSignature{Name: "Author", Email: "a@example.com", When: when},
		Committer: NativeSignature{
			Name:  "Committer",
			Email: "c@example.com",
			When:  when,
		},
	}

	h := &MergeOperationsHandler{}
	got := h.commitToInfo(commit)

	require.Equal(t, "abc123", got["sha"])
	require.Equal(t, "fix: something", got["message"])
	require.NotNil(t, got["author"])
	require.NotNil(t, got["committer"])
	require.Equal(t, when, got["created_at"])
}

// ---------------------------------------------------------------------------
// commit message generators (strategy_merge.go / strategy_squash.go)
// ---------------------------------------------------------------------------

func Test_generateMergeCommitMessage_Cov(t *testing.T) {
	t.Parallel()

	// Custom message wins.
	custom := generateMergeCommitMessage(&MergeRequest{Message: "custom msg"})
	require.Equal(t, "custom msg", custom)

	// Default strips refs/heads/ prefix.
	def := generateMergeCommitMessage(&MergeRequest{
		SourceRef: "refs/heads/feature",
		TargetRef: "refs/heads/main",
	})
	require.Equal(t, "Merge branch 'feature' into 'main'", def)
}

func Test_generateSquashCommitMessage_Cov(t *testing.T) {
	t.Parallel()

	custom := generateSquashCommitMessage(&MergeRequest{Message: "squashed"})
	require.Equal(t, "squashed", custom)

	def := generateSquashCommitMessage(&MergeRequest{
		SourceRef: "refs/heads/feature",
		TargetRef: "main",
	})
	require.Equal(t, "Squash merge branch 'feature' into 'main'", def)
}

func Test_resolveRefName_Cov(t *testing.T) {
	t.Parallel()

	require.Equal(t, "refs/heads/main", resolveRefName("main"))
	require.Equal(t, "refs/tags/v1", resolveRefName("refs/tags/v1"))
	require.Equal(t, "refs/heads/feature/x", resolveRefName("feature/x"))
}

// ---------------------------------------------------------------------------
// pure_signature.go
// ---------------------------------------------------------------------------

func Test_CreateSignature_Cov(t *testing.T) {
	t.Parallel()

	fallbackTime := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)

	// nil input, nil fallback → default identity.
	sig := CreateSignature(nil, nil, fallbackTime)
	require.Equal(t, "Git Executor", sig.Name)
	require.Equal(t, "git@executor.com", sig.Email)
	require.Equal(t, fallbackTime, sig.When)

	// nil input with fallback → fallback returned.
	fb := &NativeSignature{Name: "FB", Email: "fb@x.com", When: fallbackTime}
	require.Equal(t, *fb, CreateSignature(nil, fb, time.Now()))

	// zero date in input → fallbackTime substituted.
	sig = CreateSignature(&SignatureInput{Name: "A", Email: "a@x.com"}, nil, fallbackTime)
	require.Equal(t, fallbackTime, sig.When)

	// explicit date preserved.
	explicit := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	sig = CreateSignature(&SignatureInput{Name: "A", Email: "a@x.com", Date: explicit}, nil, fallbackTime)
	require.Equal(t, explicit, sig.When)
}

func Test_CreateAuthorSignature_Cov(t *testing.T) {
	t.Parallel()

	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)

	// nil req → fallback path.
	fb := &NativeSignature{Name: "FB", Email: "fb@x.com", When: now}
	require.Equal(t, *fb, CreateAuthorSignature(nil, fb, now))

	// req without author → fallback path.
	require.Equal(t, *fb, CreateAuthorSignature(&MergeRequest{}, fb, now))

	// req with author → uses author info.
	sig := CreateAuthorSignature(&MergeRequest{Author: &AuthorInfo{Name: "Me", Email: "me@x.com"}}, fb, now)
	require.Equal(t, "Me", sig.Name)
	require.Equal(t, "me@x.com", sig.Email)
	require.Equal(t, now, sig.When) // zero date → now fallback
}

func Test_CreateCommitterSignature_Cov(t *testing.T) {
	t.Parallel()

	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	author := &NativeSignature{Name: "A", Email: "a@x.com", When: now}

	// no committer, author provided → author returned.
	require.Equal(t, *author, CreateCommitterSignature(&MergeRequest{}, author, now))

	// no committer, no author → default identity.
	sig := CreateCommitterSignature(nil, nil, now)
	require.Equal(t, "Git Executor", sig.Name)

	// committer specified → uses committer.
	sig = CreateCommitterSignature(&MergeRequest{Committer: &AuthorInfo{Name: "C", Email: "c@x.com"}}, author, now)
	require.Equal(t, "C", sig.Name)
	require.Equal(t, "c@x.com", sig.Email)
}

func Test_CreateRebaseAuthorSignature_Cov(t *testing.T) {
	t.Parallel()

	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	orig := NativeSignature{Name: "Orig", Email: "orig@x.com", When: time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)}

	// nil author → original preserved.
	require.Equal(t, orig, CreateRebaseAuthorSignature(nil, orig, now))
	require.Equal(t, orig, CreateRebaseAuthorSignature(&MergeRequest{}, orig, now))

	// author override with zero date → original author date preserved.
	sig := CreateRebaseAuthorSignature(&MergeRequest{Author: &AuthorInfo{Name: "New", Email: "new@x.com"}}, orig, now)
	require.Equal(t, "New", sig.Name)
	require.Equal(t, orig.When, sig.When)

	// author override with explicit date.
	explicit := time.Date(2022, 5, 5, 0, 0, 0, 0, time.UTC)
	sig = CreateRebaseAuthorSignature(&MergeRequest{Author: &AuthorInfo{Name: "New", Email: "new@x.com", Date: explicit}}, orig, now)
	require.Equal(t, explicit, sig.When)
}

func Test_CreateRebaseCommitterSignature_Cov(t *testing.T) {
	t.Parallel()

	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	author := NativeSignature{Name: "A", Email: "a@x.com"}

	// no committer → author name/email with now.
	sig := CreateRebaseCommitterSignature(nil, author, now)
	require.Equal(t, "A", sig.Name)
	require.Equal(t, now, sig.When)

	// committer with zero date → now.
	sig = CreateRebaseCommitterSignature(&MergeRequest{Committer: &AuthorInfo{Name: "C", Email: "c@x.com"}}, author, now)
	require.Equal(t, "C", sig.Name)
	require.Equal(t, now, sig.When)

	// committer with explicit date.
	explicit := time.Date(2022, 5, 5, 0, 0, 0, 0, time.UTC)
	sig = CreateRebaseCommitterSignature(&MergeRequest{Committer: &AuthorInfo{Name: "C", Email: "c@x.com", Date: explicit}}, author, now)
	require.Equal(t, explicit, sig.When)
}

// ---------------------------------------------------------------------------
// gitcmd.go conversions
// ---------------------------------------------------------------------------

func Test_nativeCommitToInfo_Cov(t *testing.T) {
	t.Parallel()

	require.Nil(t, nativeCommitToInfo(nil))

	when := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	c := &NativeCommit{
		Hash:      "deadbeef",
		Message:   "msg",
		TreeHash:  "tree123",
		Author:    NativeSignature{Name: "A", Email: "a@x.com", When: when},
		Committer: NativeSignature{Name: "C", Email: "c@x.com", When: when},
	}
	info := nativeCommitToInfo(c)
	require.Equal(t, "deadbeef", info.SHA)
	require.Equal(t, "msg", info.Message)
	require.Equal(t, "tree123", info.TreeSHA)
	require.Equal(t, "A", info.Author.Name)
	require.Equal(t, "c@x.com", info.Committer.Email)
	require.Equal(t, when.Format(time.RFC3339), info.CreatedAt)
}

func Test_toNativeSignature_Cov(t *testing.T) {
	t.Parallel()

	require.Equal(t, NativeSignature{}, toNativeSignature(nil))

	when := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	a := &AuthorInfo{Name: "A", Email: "a@x.com", Date: when}
	got := toNativeSignature(a)
	require.Equal(t, "A", got.Name)
	require.Equal(t, "a@x.com", got.Email)
	require.Equal(t, when, got.When)
}

// ---------------------------------------------------------------------------
// types_errors.go
// ---------------------------------------------------------------------------

// Test_ConcurrentUpdateError_ErrorAndUnwrap_Cov 는 유일하게 남은 typed error
// (handler 에서 errors.As 로 감지) 의 Error()/Unwrap() 을 검증한다. Issue-5360
// 에서 나머지 dead 택소노미(ToAPIError/APIError/NotFoundError 등)는 제거됨.
func Test_ConcurrentUpdateError_ErrorAndUnwrap_Cov(t *testing.T) {
	t.Parallel()

	cu := ConcurrentUpdateError{Resource: "ref"}
	require.Equal(t, "concurrent update on resource: ref", cu.Error())
	require.Equal(t, "cc", ConcurrentUpdateError{Message: "cc"}.Error())
	require.ErrorIs(t, cu, ErrConflict)
}

// ---------------------------------------------------------------------------
// validator.go
// ---------------------------------------------------------------------------

func Test_validateMergeRequest_Cov(t *testing.T) {
	t.Parallel()

	h := &MergeOperationsHandler{}

	require.NoError(t, h.validateMergeRequest(&MergeRequest{
		SourceRef: "a", TargetRef: "b", Method: MergeMethodMerge, Strategy: MergeStrategyRecursive,
	}))

	require.ErrorContains(t, h.validateMergeRequest(&MergeRequest{TargetRef: "b"}), "source_ref is required")
	require.ErrorContains(t, h.validateMergeRequest(&MergeRequest{SourceRef: "a"}), "target_ref is required")
	require.ErrorContains(t, h.validateMergeRequest(&MergeRequest{SourceRef: "a", TargetRef: "b", Method: "bogus"}), "invalid merge method")
	require.ErrorContains(t, h.validateMergeRequest(&MergeRequest{SourceRef: "a", TargetRef: "b", Strategy: "bogus"}), "invalid merge strategy")

	long := make([]byte, 5001)
	require.ErrorContains(t, h.validateMergeRequest(&MergeRequest{SourceRef: "a", TargetRef: "b", Message: string(long)}), "too long")

	require.ErrorContains(t, h.validateMergeRequest(&MergeRequest{SourceRef: "a", TargetRef: "b", Author: &AuthorInfo{Email: "e"}}), "author name cannot be empty")
	require.ErrorContains(t, h.validateMergeRequest(&MergeRequest{SourceRef: "a", TargetRef: "b", Author: &AuthorInfo{Name: "n"}}), "author email cannot be empty")
	require.ErrorContains(t, h.validateMergeRequest(&MergeRequest{SourceRef: "a", TargetRef: "b", Committer: &AuthorInfo{Email: "e"}}), "committer name cannot be empty")
	require.ErrorContains(t, h.validateMergeRequest(&MergeRequest{SourceRef: "a", TargetRef: "b", Committer: &AuthorInfo{Name: "n"}}), "committer email cannot be empty")
}
