package commits

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// newTestLogger 는 테스트용 로거를 생성한다 (다른 테스트의 패턴과 동일).
func newTestLogger(t *testing.T) *logging.Logger {
	t.Helper()
	logger, err := logging.NewLogger(config.LoggingConfig{
		Level:       "info",
		Format:      "text",
		Output:      "stdout",
		Development: true,
	})
	require.NoError(t, err)
	return logger
}

// =============================================================================
// native_git.go — 0% 커버리지 함수들
// bare repo fixture (newBareRepoNative + createInitialCommitNative) 재사용.
// =============================================================================

func Test_nativeCatFileType_Commit_ReturnsCommit(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	commitHash := createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))
	ctx := context.Background()

	typ, err := nativeCatFileType(ctx, repoPath, commitHash)

	require.NoError(t, err)
	require.Equal(t, "commit", typ)
}

func Test_nativeCatFileType_Blob_ReturnsBlob(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	ctx := context.Background()
	blobHash, err := nativeHashObject(ctx, repoPath, []byte("hello"))
	require.NoError(t, err)

	typ, err := nativeCatFileType(ctx, repoPath, blobHash)

	require.NoError(t, err)
	require.Equal(t, "blob", typ)
}

func Test_nativeCatFileType_Missing_ReturnsError(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	ctx := context.Background()

	_, err := nativeCatFileType(ctx, repoPath, strings.Repeat("0", 40))

	require.Error(t, err)
	require.Contains(t, err.Error(), "object not found")
}

func Test_nativeGetCommitInfo_Found_ReturnsCommit(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	commitHash := createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))
	ctx := context.Background()

	commit, err := nativeGetCommitInfo(ctx, repoPath, commitHash)

	require.NoError(t, err)
	require.Equal(t, commitHash, commit.Sha)
	require.Equal(t, "init", commit.Message)
	require.Equal(t, "Test", commit.Author.Name)
	require.Equal(t, "test@example.com", commit.Author.Email)
}

func Test_nativeGetCommitInfo_Missing_ReturnsNotFound(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))
	ctx := context.Background()

	_, err := nativeGetCommitInfo(ctx, repoPath, strings.Repeat("a", 40))

	require.Error(t, err)
}

func Test_nativeRevListCountRange_CountsExclusive(t *testing.T) {
	t.Parallel()

	// 5개 커밋 → HEAD vs HEAD~? 비교. base 제외 카운트.
	barePath := createBareRepoWithNCommitsNative(t, 5)
	ctx := context.Background()

	head, err := nativeResolveRef(ctx, barePath, "HEAD")
	require.NoError(t, err)
	base, err := nativeResolveRef(ctx, barePath, "HEAD~3")
	require.NoError(t, err)

	count, err := nativeRevListCountRange(ctx, barePath, head, base)

	require.NoError(t, err)
	require.Equal(t, 3, count)
}

func Test_nativeRevListCountRange_InvalidRef_ReturnsError(t *testing.T) {
	t.Parallel()

	barePath := createBareRepoWithNCommitsNative(t, 2)
	ctx := context.Background()

	_, err := nativeRevListCountRange(ctx, barePath, "nope", "alsonope")

	require.Error(t, err)
}

func Test_nativeDiffNameOnlyCount_CountsChangedFiles(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	ctx := context.Background()

	// base: a.txt
	blobA, err := nativeHashObject(ctx, repoPath, []byte("A"))
	require.NoError(t, err)
	tree1, err := nativeMkTree(ctx, repoPath, []nativeMkTreeEntry{
		{Mode: "100644", Type: "blob", Hash: blobA, Name: "a.txt"},
	})
	require.NoError(t, err)
	author := &NativeSignature{Name: "T", Email: "t@e.com", When: time.Now()}
	c1, err := nativeCommitTree(ctx, repoPath, tree1, nil, "c1", author, author)
	require.NoError(t, err)

	// second: a.txt changed + b.txt added
	blobA2, err := nativeHashObject(ctx, repoPath, []byte("A2"))
	require.NoError(t, err)
	blobB, err := nativeHashObject(ctx, repoPath, []byte("B"))
	require.NoError(t, err)
	tree2, err := nativeMkTree(ctx, repoPath, []nativeMkTreeEntry{
		{Mode: "100644", Type: "blob", Hash: blobA2, Name: "a.txt"},
		{Mode: "100644", Type: "blob", Hash: blobB, Name: "b.txt"},
	})
	require.NoError(t, err)
	c2, err := nativeCommitTree(ctx, repoPath, tree2, []string{c1}, "c2", author, author)
	require.NoError(t, err)

	count, err := nativeDiffNameOnlyCount(ctx, repoPath, c1, c2)

	require.NoError(t, err)
	require.Equal(t, 2, count)
}

func Test_nativeDiffNameOnlyCount_NoChanges_ReturnsZero(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	commitHash := createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))
	ctx := context.Background()

	count, err := nativeDiffNameOnlyCount(ctx, repoPath, commitHash, commitHash)

	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func Test_nativeDiffNameOnlyCount_InvalidHash_ReturnsError(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	ctx := context.Background()

	_, err := nativeDiffNameOnlyCount(ctx, repoPath, "bad1", "bad2")

	require.Error(t, err)
}

func Test_nativeGetCommitFullMessage_ReturnsBody(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	ctx := context.Background()

	blob, err := nativeHashObject(ctx, repoPath, []byte("x"))
	require.NoError(t, err)
	tree, err := nativeMkTree(ctx, repoPath, []nativeMkTreeEntry{
		{Mode: "100644", Type: "blob", Hash: blob, Name: "x.txt"},
	})
	require.NoError(t, err)
	author := &NativeSignature{Name: "T", Email: "t@e.com", When: time.Now()}
	fullMsg := "subject line\n\nbody paragraph here"
	commitHash, err := nativeCommitTree(ctx, repoPath, tree, nil, fullMsg, author, author)
	require.NoError(t, err)

	msg, err := nativeGetCommitFullMessage(ctx, repoPath, commitHash)

	require.NoError(t, err)
	require.Contains(t, msg, "subject line")
	require.Contains(t, msg, "body paragraph here")
}

func Test_nativeGetCommitFullMessage_Missing_ReturnsError(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	ctx := context.Background()

	_, err := nativeGetCommitFullMessage(ctx, repoPath, strings.Repeat("f", 40))

	require.Error(t, err)
}

// =============================================================================
// transaction.go — 0% / 저커버리지 함수들
// =============================================================================

func Test_TransactionResult_OperationCounts(t *testing.T) {
	t.Parallel()

	tr := &TransactionResult{
		SuccessfulOperations: []string{"op1", "op2", "op3"},
		FailedOperations:     map[string]error{"op4": context.Canceled},
	}

	require.Equal(t, 3, tr.GetSuccessfulOperationCount())
	require.Equal(t, 1, tr.GetFailedOperationCount())
}

func Test_TransactionResult_EmptyCounts(t *testing.T) {
	t.Parallel()

	tr := &TransactionResult{}

	require.Equal(t, 0, tr.GetSuccessfulOperationCount())
	require.Equal(t, 0, tr.GetFailedOperationCount())
}

func newActiveTxn(t *testing.T, tm *TransactionManager, repoPath, baseHash string) *Transaction {
	t.Helper()
	commitInfo := CommitInfo{
		Message: "msg",
		Author: NativeSignature{
			Name:  "Test",
			Email: "test@example.com",
			When:  time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
		},
		Parents: []string{baseHash},
	}
	txn, err := tm.BeginTransaction(context.Background(), repoPath, baseHash, commitInfo, TransactionOptions{
		Timeout: 30 * time.Second,
	})
	require.NoError(t, err)
	return txn
}

func Test_TransactionManager_RollbackTransaction_Active_Succeeds(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	baseHash := createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))
	tm := NewTransactionManager(newTestLogger(t))
	txn := newActiveTxn(t, tm, repoPath, baseHash)

	require.NoError(t, tm.AddOperation(context.Background(), txn, TransactionOperation{
		Type: FileOperationUpdate,
		FileChange: FileChange{
			Path:      "a.txt",
			Operation: FileOperationUpdate,
			Content:   []byte("A2"),
		},
	}))

	err := tm.RollbackTransaction(context.Background(), txn)

	require.NoError(t, err)
	require.Equal(t, TransactionStateRolledBack, txn.State)
	require.Equal(t, TransactionStateRolledBack, txn.Operations[0].State)
}

func Test_TransactionManager_RollbackTransaction_AlreadyRolledBack_ReturnsError(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	baseHash := createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))
	tm := NewTransactionManager(newTestLogger(t))
	txn := newActiveTxn(t, tm, repoPath, baseHash)

	require.NoError(t, tm.RollbackTransaction(context.Background(), txn))

	err := tm.RollbackTransaction(context.Background(), txn)

	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot rollback transaction in state")
}

func Test_TransactionManager_RollbackTransaction_Committed_ReturnsError(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	baseHash := createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))
	tm := NewTransactionManager(newTestLogger(t))
	txn := newActiveTxn(t, tm, repoPath, baseHash)

	require.NoError(t, tm.AddOperation(context.Background(), txn, TransactionOperation{
		Type: FileOperationUpdate,
		FileChange: FileChange{
			Path:      "a.txt",
			Operation: FileOperationUpdate,
			Content:   []byte("A2"),
		},
	}))
	_, err := tm.CommitTransaction(context.Background(), txn)
	require.NoError(t, err)

	err = tm.RollbackTransaction(context.Background(), txn)

	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot rollback transaction in state")
}

func Test_TransactionManager_GetTransaction_FoundAndMissing(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	baseHash := createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))
	tm := NewTransactionManager(newTestLogger(t))
	txn := newActiveTxn(t, tm, repoPath, baseHash)

	got, err := tm.GetTransaction(txn.ID)
	require.NoError(t, err)
	require.Equal(t, txn.ID, got.ID)

	_, err = tm.GetTransaction("nonexistent-id")
	require.Error(t, err)
	require.Contains(t, err.Error(), "transaction not found")
}

func Test_TransactionManager_ListActiveTransactions_ReturnsAll(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	baseHash := createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))
	tm := NewTransactionManager(newTestLogger(t))

	require.Empty(t, tm.ListActiveTransactions())

	txn1 := newActiveTxn(t, tm, repoPath, baseHash)
	txn2 := newActiveTxn(t, tm, repoPath, baseHash)

	list := tm.ListActiveTransactions()
	require.Len(t, list, 2)

	ids := map[string]bool{}
	for _, tx := range list {
		ids[tx.ID] = true
	}
	require.True(t, ids[txn1.ID])
	require.True(t, ids[txn2.ID])
}

func Test_TransactionManager_CleanupTransaction_RemovesEntry(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	baseHash := createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))
	tm := NewTransactionManager(newTestLogger(t))
	txn := newActiveTxn(t, tm, repoPath, baseHash)

	tm.CleanupTransaction(txn.ID)

	_, err := tm.GetTransaction(txn.ID)
	require.Error(t, err)
	require.Empty(t, tm.ListActiveTransactions())
}

func Test_createCommitNative_WithAuthorOnly_UsesAuthorAsCommitter(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	ctx := context.Background()

	blob, err := nativeHashObject(ctx, repoPath, []byte("x"))
	require.NoError(t, err)
	tree, err := nativeMkTree(ctx, repoPath, []nativeMkTreeEntry{
		{Mode: "100644", Type: "blob", Hash: blob, Name: "x.txt"},
	})
	require.NoError(t, err)

	author := NativeSignature{Name: "Alice", Email: "alice@e.com", When: time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)}
	result, err := createCommitNative(ctx, repoPath, tree, CommitInfo{
		Message: "commit msg",
		Author:  author,
		Parents: nil,
	})

	require.NoError(t, err)
	require.NotEmpty(t, result.Hash)
	require.Equal(t, tree, result.TreeHash)
	require.Equal(t, "commit msg", result.Message)
	require.Equal(t, author, result.Author)
	require.Equal(t, author, result.Committer, "committer should default to author")
}

func Test_createCommitNative_WithDistinctCommitter(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	ctx := context.Background()

	blob, err := nativeHashObject(ctx, repoPath, []byte("y"))
	require.NoError(t, err)
	tree, err := nativeMkTree(ctx, repoPath, []nativeMkTreeEntry{
		{Mode: "100644", Type: "blob", Hash: blob, Name: "y.txt"},
	})
	require.NoError(t, err)

	author := NativeSignature{Name: "Alice", Email: "alice@e.com", When: time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)}
	committer := NativeSignature{Name: "Bob", Email: "bob@e.com", When: time.Date(2024, 5, 2, 0, 0, 0, 0, time.UTC)}

	result, err := createCommitNative(ctx, repoPath, tree, CommitInfo{
		Message:   "msg2",
		Author:    author,
		Committer: &committer,
	})

	require.NoError(t, err)
	require.Equal(t, committer, result.Committer)
}

func Test_createCommitNative_InvalidTree_ReturnsError(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	ctx := context.Background()

	_, err := createCommitNative(ctx, repoPath, strings.Repeat("0", 40), CommitInfo{
		Message: "x",
		Author:  NativeSignature{Name: "A", Email: "a@e.com", When: time.Now()},
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to create commit")
}

// =============================================================================
// validator.go — 추가 분기 커버리지 (delete/create/update/move/encoding)
// =============================================================================

func Test_validateMultiFileOperation_DeleteWithContent_ReturnsError(t *testing.T) {
	t.Parallel()

	err := validateMultiFileOperation(&MultiFileOperation{
		Path:      "a.txt",
		Operation: OperationDelete,
		Content:   "should not be here",
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "content should not be provided for delete")
}

func Test_validateMultiFileOperation_DeleteNoContent_OK(t *testing.T) {
	t.Parallel()

	err := validateMultiFileOperation(&MultiFileOperation{
		Path:      "a.txt",
		Operation: OperationDelete,
	})

	require.NoError(t, err)
}

func Test_validateMultiFileOperation_CreateEmptyContent_OK(t *testing.T) {
	t.Parallel()

	// 빈 파일 생성은 허용한다 (GitHub PUT /contents 의 content:"" 와 동일).
	err := validateMultiFileOperation(&MultiFileOperation{
		Path:      "a.txt",
		Operation: OperationCreate,
	})

	require.NoError(t, err)
}

func Test_validateMultiFileOperation_UpdateEmptyContent_OK(t *testing.T) {
	t.Parallel()

	// update 로 기존 파일을 빈 파일로 만드는 것도 허용한다.
	err := validateMultiFileOperation(&MultiFileOperation{
		Path:      "a.txt",
		Operation: OperationUpdate,
	})

	require.NoError(t, err)
}

func Test_validateMultiFileOperation_UpdateWithContent_OK(t *testing.T) {
	t.Parallel()

	err := validateMultiFileOperation(&MultiFileOperation{
		Path:      "a.txt",
		Operation: OperationUpdate,
		Content:   "x",
	})

	require.NoError(t, err)
}

func Test_validateMultiFileOperation_InvalidOperation_ReturnsError(t *testing.T) {
	t.Parallel()

	err := validateMultiFileOperation(&MultiFileOperation{
		Path:      "a.txt",
		Operation: "explode",
		Content:   "x",
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid operation type")
}

func Test_validateMultiFileOperation_InvalidPath_ReturnsError(t *testing.T) {
	t.Parallel()

	err := validateMultiFileOperation(&MultiFileOperation{
		Path:      "../escape.txt",
		Operation: OperationCreate,
		Content:   "x",
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid file path")
}

func Test_validateMultiFileOperation_MoveMissingOriginalPath_ReturnsError(t *testing.T) {
	t.Parallel()

	err := validateMultiFileOperation(&MultiFileOperation{
		Path:      "new.txt",
		Operation: OperationMove,
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "original_path is required")
}

func Test_validateMultiFileOperation_MoveSamePath_ReturnsError(t *testing.T) {
	t.Parallel()

	err := validateMultiFileOperation(&MultiFileOperation{
		Path:         "same.txt",
		Operation:    OperationMove,
		OriginalPath: "same.txt",
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be the same")
}

func Test_validateMultiFileOperation_MoveInvalidOriginalPath_ReturnsError(t *testing.T) {
	t.Parallel()

	err := validateMultiFileOperation(&MultiFileOperation{
		Path:         "new.txt",
		Operation:    OperationMove,
		OriginalPath: "../escape.txt",
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid original_path")
}

func Test_validateMultiFileOperation_MoveValid_OK(t *testing.T) {
	t.Parallel()

	err := validateMultiFileOperation(&MultiFileOperation{
		Path:         "new.txt",
		Operation:    OperationMove,
		OriginalPath: "old.txt",
	})

	require.NoError(t, err)
}

func Test_validateMultiFileOperation_InvalidEncoding_ReturnsError(t *testing.T) {
	t.Parallel()

	err := validateMultiFileOperation(&MultiFileOperation{
		Path:      "a.txt",
		Operation: OperationCreate,
		Content:   "x",
		Encoding:  "latin-1",
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid encoding")
}

func Test_validateMultiFileOperation_Base64Encoding_OK(t *testing.T) {
	t.Parallel()

	err := validateMultiFileOperation(&MultiFileOperation{
		Path:      "a.bin",
		Operation: OperationCreate,
		Content:   "aGVsbG8=",
		Encoding:  "base64",
	})

	require.NoError(t, err)
}

func Test_validateBasicPath_Variants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{"empty", "", "cannot be empty"},
		{"too long", strings.Repeat("a", 4097), "too long"},
		{"traversal", "a/../b", "path traversal"},
		{"absolute", "/etc/passwd", "cannot be absolute"},
		{"backslash", "a\\b", "backslashes"},
		{"valid", "dir/sub/file.txt", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateBasicPath(tc.path)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// =============================================================================
// handler.go — HandleComparisonSummary / HandleGetCommitDetail (0% → covered)
// invokeTyped 헬퍼로 typed handler 호출.
// =============================================================================

func Test_HandleComparisonSummary_Success(t *testing.T) {
	t.Parallel()

	handler, repoPath := newCommitsHandlerWithRepo(t)
	ctx := context.Background()

	// main 위에 2개 더 commit 추가 (createInitialCommitNative 가 main 을 덮어씀
	// → repoPath 는 go-git PlainInit 으로 이미 main 보유). 추가 커밋을 쌓는다.
	headHash, err := nativeResolveRef(ctx, repoPath, "refs/heads/main")
	require.NoError(t, err)

	// feature 브랜치: main 으로부터 2 커밋 추가
	blob1, err := nativeHashObject(ctx, repoPath, []byte("f1"))
	require.NoError(t, err)
	tree1, err := nativeMkTree(ctx, repoPath, []nativeMkTreeEntry{
		{Mode: "100644", Type: "blob", Hash: blob1, Name: "f1.txt"},
	})
	require.NoError(t, err)
	author := &NativeSignature{Name: "T", Email: "t@e.com", When: time.Now()}
	commit1, err := nativeCommitTree(ctx, repoPath, tree1, []string{headHash}, "feat1", author, author)
	require.NoError(t, err)
	require.NoError(t, nativeUpdateRef(ctx, repoPath, "refs/heads/feature", commit1))

	req := ComparisonSummaryRequest{SourceRef: "feature", TargetRef: "main"}
	c, w := newRequest(t, "GET", "/repos/testowner/testrepo/commits/compare/summary?source_ref=feature&target_ref=main", nil)
	c.Params = gin.Params{
		{Key: "owner", Value: "testowner"},
		{Key: "repo", Value: "testrepo"},
	}

	invokeTyped(c, func(ctx *gin.Context) (ComparisonSummaryResponse, error) {
		return handler.HandleComparisonSummary(ctx, req)
	})

	requireStatus(t, w, http.StatusOK)
	var resp ComparisonSummaryResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Equal(t, 1, resp.CommitCount)
	require.Equal(t, 1, resp.ChangedFileCount)
	require.Equal(t, commit1, resp.SourceSha)
	require.Equal(t, headHash, resp.TargetSha)
	require.Equal(t, headHash, resp.MergeBase)
}

func Test_HandleComparisonSummary_InvalidSourceRef_Returns400(t *testing.T) {
	t.Parallel()

	handler, _ := newCommitsHandlerWithRepo(t)

	req := ComparisonSummaryRequest{SourceRef: "no-such-ref", TargetRef: "main"}
	c, w := newRequest(t, "GET", "/repos/testowner/testrepo/commits/compare/summary", nil)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}

	invokeTyped(c, func(ctx *gin.Context) (ComparisonSummaryResponse, error) {
		return handler.HandleComparisonSummary(ctx, req)
	})

	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "failed to resolve source ref")
}

func Test_HandleComparisonSummary_InvalidTargetRef_Returns400(t *testing.T) {
	t.Parallel()

	handler, _ := newCommitsHandlerWithRepo(t)

	req := ComparisonSummaryRequest{SourceRef: "main", TargetRef: "no-such-ref"}
	c, w := newRequest(t, "GET", "/repos/testowner/testrepo/commits/compare/summary", nil)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}

	invokeTyped(c, func(ctx *gin.Context) (ComparisonSummaryResponse, error) {
		return handler.HandleComparisonSummary(ctx, req)
	})

	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "failed to resolve target ref")
}

// 존재하지 않는 repo 경로지만 path 검증을 통과 → git resolve 단계에서 실패
// (400). validateAndGetRepoPath 통과 후 ref resolve 실패 분기 커버리지.
func Test_HandleComparisonSummary_MissingRepoRefResolveFails_Returns400(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	req := ComparisonSummaryRequest{SourceRef: "main", TargetRef: "main"}
	c, w := newRequest(t, "GET", "/repos/ghost/ghost/commits/compare/summary", nil)
	c.Params = gin.Params{{Key: "owner", Value: "ghost"}, {Key: "repo", Value: "ghost"}}

	invokeTyped(c, func(ctx *gin.Context) (ComparisonSummaryResponse, error) {
		return handler.HandleComparisonSummary(ctx, req)
	})

	requireStatus(t, w, http.StatusBadRequest)
}

func Test_HandleGetCommitDetail_Success(t *testing.T) {
	t.Parallel()

	handler, repoPath := newCommitsHandlerWithRepo(t)
	ctx := context.Background()

	headHash, err := nativeResolveRef(ctx, repoPath, "refs/heads/main")
	require.NoError(t, err)

	c, w := newRequest(t, "GET", "/repos/testowner/testrepo/commits/"+headHash, nil)
	c.Params = gin.Params{
		{Key: "owner", Value: "testowner"},
		{Key: "repo", Value: "testrepo"},
		{Key: "sha", Value: headHash},
	}

	invokeTyped(c, handler.HandleGetCommitDetail)

	requireStatus(t, w, http.StatusOK)
	var resp CommitDetailResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Equal(t, headHash, resp.Sha)
	require.NotEmpty(t, resp.Message)
	require.NotEmpty(t, resp.TreeHash)
}

func Test_HandleGetCommitDetail_EmptySha_Returns400(t *testing.T) {
	t.Parallel()

	handler, _ := newCommitsHandlerWithRepo(t)

	c, w := newRequest(t, "GET", "/repos/testowner/testrepo/commits/", nil)
	c.Params = gin.Params{
		{Key: "owner", Value: "testowner"},
		{Key: "repo", Value: "testrepo"},
		{Key: "sha", Value: ""},
	}

	invokeTyped(c, handler.HandleGetCommitDetail)

	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "commit sha is required")
}

// 존재하지 않는 sha 는 git 이 "bad object" 로 실패한다. ClassifyGitError 가
// 이를 OBJECT_NOT_FOUND(404) 로 분류 — 없는 커밋은 client 관점에서 not found
// 이지 서버 결함(500)이 아니다 (Issue-5577 후속).
func Test_HandleGetCommitDetail_BadObject_Returns404(t *testing.T) {
	t.Parallel()

	handler, _ := newCommitsHandlerWithRepo(t)

	missing := strings.Repeat("a", 40)
	c, w := newRequest(t, "GET", "/repos/testowner/testrepo/commits/"+missing, nil)
	c.Params = gin.Params{
		{Key: "owner", Value: "testowner"},
		{Key: "repo", Value: "testrepo"},
		{Key: "sha", Value: missing},
	}

	invokeTyped(c, handler.HandleGetCommitDetail)

	requireStatus(t, w, http.StatusNotFound)
}

// repo 경로 검증은 통과하지만 git 레벨에서 실패하는 케이스. "zzz" 는
// unknown revision → ClassifyGitError 가 REF_NOT_FOUND(404) 로 분류한다.
func Test_HandleGetCommitDetail_GitLevelFailure_Returns404(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	c, w := newRequest(t, "GET", "/repos/testowner/testrepo/commits/zzz", nil)
	c.Params = gin.Params{
		{Key: "owner", Value: "testowner"},
		{Key: "repo", Value: "testrepo"},
		{Key: "sha", Value: "zzz"},
	}

	invokeTyped(c, handler.HandleGetCommitDetail)

	requireStatus(t, w, http.StatusNotFound)
}

// =============================================================================
// fast_import.go — ExecuteFastImportCommit / writeFastImportStream / writeBlob
// bare repo 에 fast-import 로 커밋을 만들고 결과 트리/ref 를 검증한다.
// =============================================================================

func strPtr(s string) *string { return &s }

func Test_ExecuteFastImportCommit_RootCommit_CreatesFiles(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	ctx := context.Background()
	author := NativeSignature{Name: "T", Email: "t@e.com", When: time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)}

	res, err := ExecuteFastImportCommit(ctx, &FastImportCommitRequest{
		RepoPath:     repoPath,
		Branch:       "main",
		BaseTreeHash: "",
		Changes: []FileChange{
			{Operation: FileOperationCreate, Path: "a.txt", Content: []byte("A")},
			{Operation: FileOperationCreate, Path: "dir/b.txt", Content: []byte("B"), Mode: strPtr("100644")},
		},
		CommitInfo: CommitInfo{Message: "root commit", Author: author},
	})

	require.NoError(t, err)
	require.NotEmpty(t, res.CommitHash)
	require.NotEmpty(t, res.TreeHash)

	entries, err := nativeLsTree(ctx, repoPath, res.TreeHash)
	require.NoError(t, err)
	paths := map[string]bool{}
	for _, e := range entries {
		paths[e.Path] = true
	}
	require.True(t, paths["a.txt"])
	require.True(t, paths["dir/b.txt"])
}

// 빈 파일 생성이 실제로 git 의 빈 blob 으로 커밋되는지 확인한다.
// validator 만 풀고 하위 경로가 빈 내용을 못 다루면 여기서 깨진다.
func Test_ExecuteFastImportCommit_EmptyContent_CreatesEmptyBlob(t *testing.T) {
	t.Parallel()

	const gitEmptyBlob = "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"

	repoPath := newBareRepoNative(t)
	ctx := context.Background()
	author := NativeSignature{Name: "T", Email: "t@e.com", When: time.Date(2024, 6, 5, 12, 0, 0, 0, time.UTC)}

	res, err := ExecuteFastImportCommit(ctx, &FastImportCommitRequest{
		RepoPath:     repoPath,
		Branch:       "main",
		BaseTreeHash: "",
		Changes: []FileChange{
			{Operation: FileOperationCreate, Path: "empty.txt"},
			{Operation: FileOperationCreate, Path: "nonempty.txt", Content: []byte("x")},
		},
		CommitInfo: CommitInfo{Message: "empty file", Author: author},
	})

	require.NoError(t, err)

	entries, err := nativeLsTree(ctx, repoPath, res.TreeHash)
	require.NoError(t, err)

	byPath := map[string]nativeLsTreeEntry{}
	for _, e := range entries {
		byPath[e.Path] = e
	}

	empty, ok := byPath["empty.txt"]
	require.True(t, ok, "empty.txt 가 트리에 없음")
	require.Equal(t, gitEmptyBlob, empty.Hash)
	require.Equal(t, "100644", empty.Mode)

	// 같은 커밋의 일반 파일은 영향 없음
	require.Equal(t, "nonempty.txt", byPath["nonempty.txt"].Path)
	require.NotEqual(t, gitEmptyBlob, byPath["nonempty.txt"].Hash)
}

// update 로 기존 파일을 빈 파일로 만드는 경로. 내용이 있던 blob 이 실제로
// 빈 blob 으로 교체되는지 확인한다.
func Test_ExecuteFastImportCommit_UpdateToEmpty_TruncatesBlob(t *testing.T) {
	t.Parallel()

	const gitEmptyBlob = "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"

	repoPath := newBareRepoNative(t)
	ctx := context.Background()
	author := NativeSignature{Name: "T", Email: "t@e.com", When: time.Date(2024, 6, 6, 12, 0, 0, 0, time.UTC)}

	base, err := ExecuteFastImportCommit(ctx, &FastImportCommitRequest{
		RepoPath:     repoPath,
		Branch:       "main",
		BaseTreeHash: "",
		Changes: []FileChange{
			{Operation: FileOperationCreate, Path: "f.txt", Content: []byte("has content")},
		},
		CommitInfo: CommitInfo{Message: "base", Author: author},
	})
	require.NoError(t, err)

	res, err := ExecuteFastImportCommit(ctx, &FastImportCommitRequest{
		RepoPath:     repoPath,
		Branch:       "main",
		BaseTreeHash: base.TreeHash,
		Changes: []FileChange{
			{Operation: FileOperationUpdate, Path: "f.txt"},
		},
		CommitInfo: CommitInfo{
			Message: "truncate",
			Author:  author,
			Parents: []string{base.CommitHash},
		},
	})
	require.NoError(t, err)

	entries, err := nativeLsTree(ctx, repoPath, res.TreeHash)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "f.txt", entries[0].Path)
	require.Equal(t, gitEmptyBlob, entries[0].Hash)
}

func Test_ExecuteFastImportCommit_UpdateDeleteMove_AppliesAll(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	ctx := context.Background()
	author := NativeSignature{Name: "T", Email: "t@e.com", When: time.Date(2024, 6, 2, 12, 0, 0, 0, time.UTC)}

	// 1단계: root commit 으로 base 파일들 생성
	base, err := ExecuteFastImportCommit(ctx, &FastImportCommitRequest{
		RepoPath:     repoPath,
		Branch:       "main",
		BaseTreeHash: "",
		Changes: []FileChange{
			{Operation: FileOperationCreate, Path: "keep.txt", Content: []byte("keep")},
			{Operation: FileOperationCreate, Path: "old.txt", Content: []byte("old")},
			{Operation: FileOperationCreate, Path: "todelete.txt", Content: []byte("del")},
		},
		CommitInfo: CommitInfo{Message: "base", Author: author},
	})
	require.NoError(t, err)

	// 2단계: update keep, delete todelete, move old->moved (content 변경 없음)
	res, err := ExecuteFastImportCommit(ctx, &FastImportCommitRequest{
		RepoPath:     repoPath,
		Branch:       "main",
		BaseTreeHash: base.TreeHash,
		Changes: []FileChange{
			{Operation: FileOperationUpdate, Path: "keep.txt", Content: []byte("keep2")},
			{Operation: FileOperationDelete, Path: "todelete.txt"},
			{Operation: FileOperationMove, OriginalPath: "old.txt", Path: "moved.txt"},
		},
		CommitInfo: CommitInfo{Message: "changes", Author: author, Parents: []string{base.CommitHash}},
	})
	require.NoError(t, err)

	entries, err := nativeLsTree(ctx, repoPath, res.TreeHash)
	require.NoError(t, err)
	paths := map[string]bool{}
	for _, e := range entries {
		paths[e.Path] = true
	}
	require.True(t, paths["keep.txt"])
	require.True(t, paths["moved.txt"])
	require.False(t, paths["old.txt"], "old.txt should be renamed away")
	require.False(t, paths["todelete.txt"], "todelete.txt should be deleted")
}

func Test_ExecuteFastImportCommit_MoveWithContent_ReplacesBlob(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	ctx := context.Background()
	author := NativeSignature{Name: "T", Email: "t@e.com", When: time.Date(2024, 6, 3, 12, 0, 0, 0, time.UTC)}

	base, err := ExecuteFastImportCommit(ctx, &FastImportCommitRequest{
		RepoPath:     repoPath,
		Branch:       "main",
		BaseTreeHash: "",
		Changes: []FileChange{
			{Operation: FileOperationCreate, Path: "src.txt", Content: []byte("original")},
		},
		CommitInfo: CommitInfo{Message: "base", Author: author},
	})
	require.NoError(t, err)

	res, err := ExecuteFastImportCommit(ctx, &FastImportCommitRequest{
		RepoPath:     repoPath,
		Branch:       "main",
		BaseTreeHash: base.TreeHash,
		Changes: []FileChange{
			{Operation: FileOperationMove, OriginalPath: "src.txt", Path: "dst.txt", Content: []byte("rewritten")},
		},
		CommitInfo: CommitInfo{Message: "move+content", Author: author, Parents: []string{base.CommitHash}},
	})
	require.NoError(t, err)

	entries, err := nativeLsTree(ctx, repoPath, res.TreeHash)
	require.NoError(t, err)
	dstHash := ""
	for _, e := range entries {
		if e.Path == "dst.txt" {
			dstHash = e.Hash
		}
		require.NotEqual(t, "src.txt", e.Path, "src.txt should be gone")
	}
	require.NotEmpty(t, dstHash)
}

func Test_ExecuteFastImportCommit_WithDistinctCommitter(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	ctx := context.Background()
	author := NativeSignature{Name: "Auth", Email: "auth@e.com", When: time.Date(2024, 6, 4, 12, 0, 0, 0, time.UTC)}
	committer := NativeSignature{Name: "Comm", Email: "comm@e.com", When: time.Date(2024, 6, 5, 12, 0, 0, 0, time.UTC)}

	res, err := ExecuteFastImportCommit(ctx, &FastImportCommitRequest{
		RepoPath:     repoPath,
		Branch:       "main",
		BaseTreeHash: "",
		Changes: []FileChange{
			{Operation: FileOperationCreate, Path: "a.txt", Content: []byte("A")},
		},
		CommitInfo: CommitInfo{Message: "msg", Author: author, Committer: &committer},
	})

	require.NoError(t, err)
	require.NotEmpty(t, res.CommitHash)
}

func Test_ExecuteFastImportCommit_BadRepoPath_ReturnsError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	author := NativeSignature{Name: "T", Email: "t@e.com", When: time.Now()}

	_, err := ExecuteFastImportCommit(ctx, &FastImportCommitRequest{
		RepoPath:     "/nonexistent/repo.git",
		Branch:       "main",
		BaseTreeHash: "",
		Changes: []FileChange{
			{Operation: FileOperationCreate, Path: "a.txt", Content: []byte("A")},
		},
		CommitInfo: CommitInfo{Message: "no repo", Author: author},
	})

	require.Error(t, err)
}

func Test_PreValidateChanges_RootCommit_NonCreateRejected(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	err := PreValidateChanges(ctx, "/nonexistent", "", []FileChange{
		{Operation: FileOperationUpdate, Path: "a.txt"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "only create operations are allowed")
}

func Test_PreValidateChanges_DetectsConflicts(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	ctx := context.Background()
	base := createInitialCommitNative(t, repoPath, "exists.txt", []byte("x"))
	baseTree, err := nativeGetTreeHash(ctx, repoPath, base)
	require.NoError(t, err)

	tests := []struct {
		name    string
		changes []FileChange
		wantErr string
	}{
		{
			name:    "create existing",
			changes: []FileChange{{Operation: FileOperationCreate, Path: "exists.txt", Content: []byte("y")}},
			wantErr: "already exists",
		},
		{
			name:    "update missing",
			changes: []FileChange{{Operation: FileOperationUpdate, Path: "ghost.txt", Content: []byte("y")}},
			wantErr: "not found",
		},
		{
			name:    "delete missing",
			changes: []FileChange{{Operation: FileOperationDelete, Path: "ghost.txt"}},
			wantErr: "not found",
		},
		{
			name:    "move missing original",
			changes: []FileChange{{Operation: FileOperationMove, OriginalPath: "ghost.txt", Path: "n.txt"}},
			wantErr: "not found",
		},
		{
			name:    "move to existing",
			changes: []FileChange{{Operation: FileOperationMove, OriginalPath: "exists.txt", Path: "exists.txt"}},
			wantErr: "already exists",
		},
		{
			name:    "move empty original",
			changes: []FileChange{{Operation: FileOperationMove, OriginalPath: "", Path: "n.txt"}},
			wantErr: "original_path is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := PreValidateChanges(ctx, repoPath, baseTree, tc.changes)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func Test_PreValidateChanges_ValidBatch_OK(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	ctx := context.Background()
	base := createInitialCommitNative(t, repoPath, "exists.txt", []byte("x"))
	baseTree, err := nativeGetTreeHash(ctx, repoPath, base)
	require.NoError(t, err)

	err = PreValidateChanges(ctx, repoPath, baseTree, []FileChange{
		{Operation: FileOperationCreate, Path: "new.txt", Content: []byte("n")},
		{Operation: FileOperationUpdate, Path: "exists.txt", Content: []byte("x2")},
		{Operation: FileOperationDelete, Path: "exists.txt"},
		{Operation: FileOperationCreate, Path: "exists.txt", Content: []byte("re")}, // delete 후 재생성 허용
	})
	require.NoError(t, err)
}

// =============================================================================
// transaction.go — CommitTransaction 추가 분기
// =============================================================================

func Test_TransactionManager_CommitTransaction_NoValidOps_ReturnsError(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	tm := NewTransactionManager(newTestLogger(t))

	// root commit 트랜잭션 (BaseTreeHash empty) — 작업 없이 커밋 시도.
	txn, err := tm.BeginTransaction(context.Background(), repoPath, "", CommitInfo{
		Message: "empty",
		Author:  NativeSignature{Name: "T", Email: "t@e.com", When: time.Now()},
	}, TransactionOptions{Timeout: 30 * time.Second})
	require.NoError(t, err)

	_, err = tm.CommitTransaction(context.Background(), txn)

	require.Error(t, err)
	require.Contains(t, err.Error(), "no valid operations to commit")
	require.Equal(t, TransactionStateFailed, txn.State)
}

func Test_TransactionManager_CommitTransaction_WrongState_ReturnsError(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	baseHash := createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))
	tm := NewTransactionManager(newTestLogger(t))
	txn := newActiveTxn(t, tm, repoPath, baseHash)

	require.NoError(t, tm.RollbackTransaction(context.Background(), txn))

	_, err := tm.CommitTransaction(context.Background(), txn)

	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot commit transaction in state")
}

func Test_TransactionManager_AddOperation_WrongState_ReturnsError(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	baseHash := createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))
	tm := NewTransactionManager(newTestLogger(t))
	txn := newActiveTxn(t, tm, repoPath, baseHash)

	require.NoError(t, tm.RollbackTransaction(context.Background(), txn))

	err := tm.AddOperation(context.Background(), txn, TransactionOperation{
		Type:       FileOperationUpdate,
		FileChange: FileChange{Path: "a.txt", Operation: FileOperationUpdate, Content: []byte("x")},
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot add operation to transaction in state")
}

func Test_TransactionManager_ValidateTransaction_WrongState_ReturnsError(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	baseHash := createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))
	tm := NewTransactionManager(newTestLogger(t))
	txn := newActiveTxn(t, tm, repoPath, baseHash)

	require.NoError(t, tm.RollbackTransaction(context.Background(), txn))

	err := tm.ValidateTransaction(context.Background(), txn)

	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot validate transaction in state")
}

func Test_TransactionManager_ValidateTransaction_FileNotFound_ReturnsError(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	baseHash := createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))
	tm := NewTransactionManager(newTestLogger(t))
	txn := newActiveTxn(t, tm, repoPath, baseHash)

	require.NoError(t, tm.AddOperation(context.Background(), txn, TransactionOperation{
		Type:       FileOperationUpdate,
		FileChange: FileChange{Path: "ghost.txt", Operation: FileOperationUpdate, Content: []byte("x")},
	}))

	err := tm.ValidateTransaction(context.Background(), txn)

	require.Error(t, err)
	require.Contains(t, err.Error(), "file not found")
}

func Test_TransactionManager_ValidateTransaction_MoveOriginalMissing_ReturnsError(t *testing.T) {
	t.Parallel()

	repoPath := newBareRepoNative(t)
	baseHash := createInitialCommitNative(t, repoPath, "a.txt", []byte("A"))
	tm := NewTransactionManager(newTestLogger(t))
	txn := newActiveTxn(t, tm, repoPath, baseHash)

	require.NoError(t, tm.AddOperation(context.Background(), txn, TransactionOperation{
		Type: FileOperationMove,
		FileChange: FileChange{
			Path:         "new.txt",
			OriginalPath: "ghost.txt",
			Operation:    FileOperationMove,
		},
	}))

	err := tm.ValidateTransaction(context.Background(), txn)

	require.Error(t, err)
	require.Contains(t, err.Error(), "file not found for move operation")
}

// =============================================================================
// routes.go — RegisterRoutes (nil spec-only + real handler)
// =============================================================================

func Test_RegisterRoutes_NilHandler_SpecOnly(t *testing.T) {
	t.Parallel()

	router := gin.New()
	require.NotPanics(t, func() {
		RegisterRoutes(router, nil)
	})
}

func Test_RegisterRoutes_RealHandler_RegistersEndpoints(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)
	router := gin.New()

	require.NotPanics(t, func() {
		RegisterRoutes(router, handler)
	})

	// GET /commits/:sha 가 실제로 라우팅되는지 확인 (etc).
	routes := router.Routes()
	paths := map[string]bool{}
	for _, r := range routes {
		paths[r.Method+" "+r.Path] = true
	}
	require.True(t, paths["GET /commits"])
	require.True(t, paths["GET /commits/compare/summary"])
	require.True(t, paths["GET /commits/:sha"])
	require.True(t, paths["POST /commits"])
}
