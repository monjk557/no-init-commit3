package commits

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/events"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// buildTreeRecursive — nested directory tree building (via createTreeFromChanges)
// ---------------------------------------------------------------------------

// Test_CreateTreeFromChanges_NestedDirectories_BuildsSubtrees exercises the
// recursive subtree branches of buildTreeRecursive (dirPath != "" prefix
// handling, the parts>1 subdir detection, and the recursive descent) that the
// existing flat-file tests never reach.
func Test_CreateTreeFromChanges_NestedDirectories_BuildsSubtrees(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	ctx := context.Background()

	res, err := createTreeFromChanges(ctx, repoPath, "", []FileChange{
		{Operation: FileOperationCreate, Path: "a/b/c.txt", Content: []byte("deep")},
		{Operation: FileOperationCreate, Path: "a/b/d.txt", Content: []byte("d")},
		{Operation: FileOperationCreate, Path: "a/e.txt", Content: []byte("e")},
		{Operation: FileOperationCreate, Path: "root.txt", Content: []byte("r")},
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.TreeHash)

	entries, err := nativeLsTree(ctx, repoPath, res.TreeHash)
	require.NoError(t, err)

	paths := make(map[string]bool)
	for _, e := range entries {
		paths[e.Path] = true
	}

	require.True(t, paths["a/b/c.txt"], "nested a/b/c.txt should exist")
	require.True(t, paths["a/b/d.txt"], "nested a/b/d.txt should exist")
	require.True(t, paths["a/e.txt"], "nested a/e.txt should exist")
	require.True(t, paths["root.txt"], "root.txt should exist")
}

// Test_CreateTreeFromChanges_NestedUpdateOnExistingTree verifies buildTreeRecursive
// handles a non-empty base tree that already contains nested directories, then
// updates a deep file — exercising the prefix-filtering branch where files not
// under the current dirPath are skipped.
func Test_CreateTreeFromChanges_NestedUpdateOnExistingTree(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	ctx := context.Background()

	base, err := createTreeFromChanges(ctx, repoPath, "", []FileChange{
		{Operation: FileOperationCreate, Path: "src/main.go", Content: []byte("package main")},
		{Operation: FileOperationCreate, Path: "src/util/helper.go", Content: []byte("package util")},
		{Operation: FileOperationCreate, Path: "README.md", Content: []byte("# hi")},
	})
	require.NoError(t, err)

	updated, err := createTreeFromChanges(ctx, repoPath, base.TreeHash, []FileChange{
		{Operation: FileOperationUpdate, Path: "src/util/helper.go", Content: []byte("package util // v2")},
	})
	require.NoError(t, err)

	entries, err := nativeLsTree(ctx, repoPath, updated.TreeHash)
	require.NoError(t, err)

	paths := make(map[string]bool)
	for _, e := range entries {
		paths[e.Path] = true
	}
	require.True(t, paths["src/main.go"])
	require.True(t, paths["src/util/helper.go"])
	require.True(t, paths["README.md"])
}

// ---------------------------------------------------------------------------
// writeBlob — error-return branches via a failing io.Writer
// ---------------------------------------------------------------------------

// failWriter fails the (failAfter+1)-th Write call, allowing each of the three
// error-return branches inside writeBlob to be exercised deterministically.
type failWriter struct {
	failAfter int
	n         int
}

func (f *failWriter) Write(p []byte) (int, error) {
	f.n++
	if f.n > f.failAfter {
		return 0, errors.New("write boom")
	}
	return len(p), nil
}

func Test_writeBlob_WriterErrors_PropagatesError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		failAfter int
	}{
		{name: "header_fprintf_fails", failAfter: 0},
		{name: "content_write_fails", failAfter: 1},
		{name: "trailing_newline_fails", failAfter: 2},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			w := &failWriter{failAfter: tc.failAfter}
			err := writeBlob(w, 1, []byte("some content"))
			require.Error(t, err)
			require.Contains(t, err.Error(), "boom")
		})
	}
}

func Test_writeBlob_Success_WritesAllParts(t *testing.T) {
	t.Parallel()

	// failAfter high enough that no write fails.
	w := &failWriter{failAfter: 100}
	err := writeBlob(w, 3, []byte("ok"))
	require.NoError(t, err)
	require.Equal(t, 3, w.n) // header + content + trailing newline
}

// ---------------------------------------------------------------------------
// nativeMergeBase — error branches (no common ancestor vs. git-level failure)
// ---------------------------------------------------------------------------

// createUnrelatedRootNative creates a second parentless root commit stored at
// refs/heads/<ref> so its history shares no ancestor with main.
func createUnrelatedRootNative(t *testing.T, repoPath, ref, filename string, content []byte) string {
	t.Helper()
	ctx := context.Background()

	blobHash, err := nativeHashObject(ctx, repoPath, content)
	require.NoError(t, err)

	treeHash, err := nativeMkTree(ctx, repoPath, []nativeMkTreeEntry{
		{Mode: "100644", Type: "blob", Hash: blobHash, Name: filename},
	})
	require.NoError(t, err)

	author := &NativeSignature{
		Name:  "Other",
		Email: "other@example.com",
		When:  time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC),
	}
	commitHash, err := nativeCommitTree(ctx, repoPath, treeHash, nil, "unrelated root", author, author)
	require.NoError(t, err)

	require.NoError(t, nativeUpdateRef(ctx, repoPath, "refs/heads/"+ref, commitHash))
	return commitHash
}

func Test_nativeMergeBase_NoCommonAncestor_ReturnsError(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	mainHash := createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))
	otherHash := createUnrelatedRootNative(t, repoPath, "other", "b.txt", []byte("B"))

	_, err := nativeMergeBase(context.Background(), repoPath, mainHash, otherHash)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no common ancestor")
}

func Test_nativeMergeBase_InvalidHash_ReturnsError(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	mainHash := createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))

	_, err := nativeMergeBase(context.Background(), repoPath,
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", mainHash)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to find merge base")
}

func Test_nativeMergeBase_Success_ReturnsBase(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	base := createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))

	// Create a child commit on top of base.
	ctx := context.Background()
	baseTree, err := nativeGetTreeHash(ctx, repoPath, base)
	require.NoError(t, err)
	res, err := createTreeFromChanges(ctx, repoPath, baseTree, []FileChange{
		{Operation: FileOperationCreate, Path: "b.txt", Content: []byte("B")},
	})
	require.NoError(t, err)
	author := &NativeSignature{Name: "T", Email: "t@e.com", When: time.Now()}
	child, err := nativeCommitTree(ctx, repoPath, res.TreeHash, []string{base}, "child", author, author)
	require.NoError(t, err)

	got, err := nativeMergeBase(ctx, repoPath, base, child)
	require.NoError(t, err)
	require.Equal(t, base, got)
}

// ---------------------------------------------------------------------------
// GetCommits — cancellation and error branches
// ---------------------------------------------------------------------------

func Test_GetCommits_CancelledContext_ReturnsCtxErr(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := GetCommits(ctx, repoPath, "main", 10, 0)
	require.ErrorIs(t, err, context.Canceled)
}

func Test_GetCommits_InvalidRef_ReturnsError(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))

	_, err := GetCommits(context.Background(), repoPath, "does-not-exist", 10, 0)
	require.Error(t, err)
}

func Test_GetCommits_Success_ReturnsCommits(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))

	commits, err := GetCommits(context.Background(), repoPath, "main", 10, 0)
	require.NoError(t, err)
	require.Len(t, commits, 1)
}

// ---------------------------------------------------------------------------
// GetCommitsFromMergeBase — error branches
// ---------------------------------------------------------------------------

func Test_GetCommitsFromMergeBase_CancelledContext_ReturnsCtxErr(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := GetCommitsFromMergeBase(ctx, repoPath, "main", "main", 10, 0)
	require.ErrorIs(t, err, context.Canceled)
}

func Test_GetCommitsFromMergeBase_EmptyRefs_ReturnsError(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))

	_, _, err := GetCommitsFromMergeBase(context.Background(), repoPath, "", "main", 10, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "required")
}

func Test_GetCommitsFromMergeBase_BadSourceRef_ReturnsError(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))

	_, _, err := GetCommitsFromMergeBase(context.Background(), repoPath, "nope-source", "main", 10, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "source_ref")
}

func Test_GetCommitsFromMergeBase_BadTargetRef_ReturnsError(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))

	_, _, err := GetCommitsFromMergeBase(context.Background(), repoPath, "main", "nope-target", 10, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "target_ref")
}

func Test_GetCommitsFromMergeBase_NoCommonAncestor_ReturnsError(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))
	createUnrelatedRootNative(t, repoPath, "other", "b.txt", []byte("B"))

	_, _, err := GetCommitsFromMergeBase(context.Background(), repoPath, "main", "other", 10, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "merge base")
}

func Test_GetCommitsFromMergeBase_Success_ReturnsCommitsAndBase(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	base := createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))

	// Build a feature branch with one extra commit on top of main.
	ctx := context.Background()
	baseTree, err := nativeGetTreeHash(ctx, repoPath, base)
	require.NoError(t, err)
	res, err := createTreeFromChanges(ctx, repoPath, baseTree, []FileChange{
		{Operation: FileOperationCreate, Path: "feat.txt", Content: []byte("F")},
	})
	require.NoError(t, err)
	author := &NativeSignature{Name: "T", Email: "t@e.com", When: time.Now()}
	feat, err := nativeCommitTree(ctx, repoPath, res.TreeHash, []string{base}, "feature", author, author)
	require.NoError(t, err)
	require.NoError(t, nativeUpdateRef(ctx, repoPath, "refs/heads/feature", feat))

	commits, mergeBase, err := GetCommitsFromMergeBase(ctx, repoPath, "feature", "main", 10, 0)
	require.NoError(t, err)
	require.Equal(t, base, mergeBase)
	require.Len(t, commits, 1, "only the feature commit (past merge-base) should be listed")
	require.Equal(t, feat, commits[0].Sha)
}

// ---------------------------------------------------------------------------
// NewCommitsHandler — nil config, path-config failure, events-enabled fallback
// ---------------------------------------------------------------------------

func Test_NewCommitsHandler_NilConfig_ReturnsError(t *testing.T) {
	t.Parallel()

	_, err := NewCommitsHandler(nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "config cannot be nil")
}

func Test_NewCommitsHandler_EmptyRepoRoot_ReturnsError(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: ""},
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
	}
	_, err := NewCommitsHandler(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "path config")
}

func Test_NewCommitsHandler_EventsEnabledNoRedis_WarnsAndContinues(t *testing.T) {
	t.Parallel()

	// Events.Enabled=true but no Redis address configured → the constructor
	// logs a warning and continues with a nil events client (no error).
	cfg := &config.Config{
		Git:     config.GitConfig{RepoRoot: t.TempDir()},
		Logging: config.LoggingConfig{Level: "info", Format: "json"},
		Events:  config.EventsConfig{Enabled: true},
	}
	handler, err := NewCommitsHandler(cfg)
	require.NoError(t, err)
	require.NotNil(t, handler)
}

// ---------------------------------------------------------------------------
// validateAndGetRepoPath — default ref, path-validator failure, missing repo
// ---------------------------------------------------------------------------

func Test_validateAndGetRepoPath_DefaultsRefToHEAD(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	repoCtx, err := handler.validateAndGetRepoPath("testowner", "testrepo", "")
	require.NoError(t, err)
	require.Equal(t, "HEAD", repoCtx.ref)
	require.NotEmpty(t, repoCtx.repoPath)
}

func Test_validateAndGetRepoPath_InvalidPath_ReturnsNotFound(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	_, err := handler.validateAndGetRepoPath("../../etc", "passwd", "main")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

// ---------------------------------------------------------------------------
// addCommitEventToStream — nil client short-circuit and full publish path
// ---------------------------------------------------------------------------

// fakeEventsClient records published events and signals completion so the
// asynchronous publish goroutine can be awaited deterministically.
type fakeEventsClient struct {
	mu     sync.Mutex
	events []*events.CommitEvent
	done   chan struct{}
	err    error
}

func newFakeEventsClient() *fakeEventsClient {
	return &fakeEventsClient{done: make(chan struct{}, 1)}
}

func (f *fakeEventsClient) AddCommitEventToStream(ctx context.Context, event *events.CommitEvent) error {
	f.mu.Lock()
	f.events = append(f.events, event)
	f.mu.Unlock()
	select {
	case f.done <- struct{}{}:
	default:
	}
	return f.err
}

func (f *fakeEventsClient) Close() error { return nil }

func newServiceWithClient(t *testing.T, client events.RedisStreamClient) *CommitsService {
	t.Helper()
	logger := newTestLogger(t)
	tm := NewTransactionManager(logger)
	return NewCommitsService(logger, tm, client)
}

func Test_addCommitEventToStream_NilClient_NoOp(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	commitHash := createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))

	svc := newServiceWithClient(t, nil)
	// Must not panic and simply return early.
	svc.addCommitEventToStream(context.Background(), repoPath, "", commitHash, "main", "owner", "repo", "user-1")
}

func Test_addCommitEventToStream_MissingCommit_NoPublish(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))

	fake := newFakeEventsClient()
	svc := newServiceWithClient(t, fake)

	// Non-existent commit hash → nativeGetCommitInfo fails → early return, no publish.
	svc.addCommitEventToStream(context.Background(), repoPath, "",
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "main", "owner", "repo", "user-1")

	select {
	case <-fake.done:
		t.Fatal("no event should have been published for a missing commit")
	case <-time.After(200 * time.Millisecond):
		// expected: nothing published
	}
}

func Test_addCommitEventToStream_ValidCommit_PublishesEvent(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	commitHash := createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))

	fake := newFakeEventsClient()
	svc := newServiceWithClient(t, fake)

	// refName without refs/ prefix exercises the normalization branch.
	svc.addCommitEventToStream(context.Background(), repoPath, "oldrev", commitHash, "main", "owner", "repo", "user-42")

	select {
	case <-fake.done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected commit event to be published")
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Len(t, fake.events, 1)
	ev := fake.events[0]
	require.Equal(t, commitHash, ev.CommitHash)
	require.Equal(t, "oldrev", ev.OldRev)
	require.Equal(t, "refs/heads/main", ev.RefName)
	require.Equal(t, "user-42", ev.UserID)
}

func Test_addCommitEventToStream_QualifiedRefName_NotDoublePrefixed(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	commitHash := createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))

	fake := newFakeEventsClient()
	svc := newServiceWithClient(t, fake)

	// Already-qualified ref name should be passed through unchanged.
	svc.addCommitEventToStream(context.Background(), repoPath, "", commitHash, "refs/heads/dev", "owner", "repo", "user-9")

	select {
	case <-fake.done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected commit event to be published")
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Len(t, fake.events, 1)
	require.Equal(t, "refs/heads/dev", fake.events[0].RefName)
}
