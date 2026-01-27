package commits

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// =============================================================================
// 테스트: MultiFileCommit - 다중 파일 커밋
// 회귀 방지: "다중 파일 커밋 API가 올바른 상태 코드와 응답을 반환해야 함"
// =============================================================================

// Test_MultiFileCommit_CreateMultipleFiles_ReturnsSuccess
// 회귀: 여러 파일 생성 커밋이 성공해야 함
func Test_MultiFileCommit_CreateMultipleFiles_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	req := MultiFileCommitRequest{
		Message: "Add initial project files",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "src/main.js", Operation: "create", Content: "console.log('Hello World');"},
			{Path: "src/style.css", Operation: "create", Content: "body { background: #f0f0f0; }"},
			{Path: "package.json", Operation: "create", Content: `{ "name": "test-project" }`},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}

	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{
		{Key: "owner", Value: "testowner"},
		{Key: "repo", Value: "testrepo"},
	}

	handler.HandleMultiFileCommit(c)

	requireStatus(t, w, http.StatusOK)
	var resp MultiFileCommitResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Equal(t, 3, resp.TotalOperations)
	require.Equal(t, 3, resp.SuccessfulOps)
}

// Test_MultiFileCommit_DuplicateFile_Returns409
// 회귀: 이미 존재하는 파일 생성 시 409 Conflict 반환
func Test_MultiFileCommit_DuplicateFile_Returns409(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	// 먼저 파일 생성
	req1 := MultiFileCommitRequest{
		Message: "Create initial file",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "duplicate.txt", Operation: "create", Content: "First"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c1, w1 := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req1)
	c1.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleMultiFileCommit(c1)
	requireStatus(t, w1, http.StatusOK)

	// 같은 파일 다시 생성 시도
	req2 := MultiFileCommitRequest{
		Message: "Create duplicate",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "duplicate.txt", Operation: "create", Content: "Second"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c2, w2 := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req2)
	c2.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleMultiFileCommit(c2)

	requireStatus(t, w2, http.StatusConflict)
	requireBodyContains(t, w2, "already exists")
}

// Test_MultiFileCommit_DeleteNonExistent_Returns404
// 회귀: 존재하지 않는 파일 삭제 시 404 반환
func Test_MultiFileCommit_DeleteNonExistent_Returns404(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	req := MultiFileCommitRequest{
		Message: "Delete non-existent files",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "nonexistent.txt", Operation: "delete"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleMultiFileCommit(c)

	requireStatus(t, w, http.StatusNotFound)
	requireBodyContains(t, w, "not found")
}

// Test_MultiFileCommit_EmptyOperations_Returns400
// 회귀: 빈 작업 목록은 400 반환
func Test_MultiFileCommit_EmptyOperations_Returns400(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	req := MultiFileCommitRequest{
		Message:    "Empty operations",
		Branch:     "main",
		Operations: []MultiFileOperation{},
		Author:     &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleMultiFileCommit(c)

	requireStatus(t, w, http.StatusBadRequest)
}

// =============================================================================
// 테스트: CreateBranch with Commit
// 회귀 방지: "브랜치 생성과 커밋이 원자적으로 동작해야 함"
// =============================================================================

// Test_MultiFileCommit_CreateBranch_ReturnsSuccess
// 회귀: create_branch_name으로 새 브랜치 생성 및 커밋이 성공해야 함
func Test_MultiFileCommit_CreateBranch_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler, repoPath := newCommitsHandlerWithRepo(t)

	req := MultiFileCommitRequest{
		Message:          "init",
		Branch:           "main",
		CreateBranchName: "feature/test",
		SourceBranchName: "main",
		Author:           &CommitAuthor{Name: "Test User", Email: "test@example.com"},
		Operations: []MultiFileOperation{
			{Path: "README.md", Operation: "create", Content: "hello"},
		},
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleMultiFileCommit(c)

	requireStatus(t, w, http.StatusOK)

	var resp MultiFileCommitResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.NotEmpty(t, resp.CommitSHA)

	// 브랜치가 실제로 생성되었는지 확인
	repo := openRepo(t, repoPath)
	branchHash := getBranchHash(t, repo, "feature/test")
	require.Equal(t, plumbing.NewHash(resp.CommitSHA), branchHash)
}

// Test_MultiFileCommit_CreateBranch_DuplicateName_Returns409
// 회귀: 이미 존재하는 브랜치 이름으로 생성 시 409 반환
func Test_MultiFileCommit_CreateBranch_DuplicateName_Returns409(t *testing.T) {
	t.Parallel()

	handler, _ := newCommitsHandlerWithRepo(t)

	req := MultiFileCommitRequest{
		Message:          "init",
		Branch:           "main",
		CreateBranchName: "feature/dup",
		SourceBranchName: "main",
		Author:           &CommitAuthor{Name: "Test User", Email: "test@example.com"},
		Operations: []MultiFileOperation{
			{Path: "README.md", Operation: "create", Content: "hello"},
		},
	}

	// 첫 번째 요청 - 성공
	c1, w1 := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c1.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleMultiFileCommit(c1)
	requireStatus(t, w1, http.StatusOK)

	// 두 번째 요청 - 충돌
	c2, w2 := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c2.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleMultiFileCommit(c2)
	requireStatus(t, w2, http.StatusConflict)
}

// Test_MultiFileCommit_CreateBranch_CommitFailure_RollbacksBranch
// 회귀: 커밋 실패 시 새로 생성한 브랜치가 롤백되어야 함
func Test_MultiFileCommit_CreateBranch_CommitFailure_RollbacksBranch(t *testing.T) {
	t.Parallel()

	handler, repoPath := newCommitsHandlerWithRepo(t)

	req := MultiFileCommitRequest{
		Message:          "should-fail",
		Branch:           "main",
		CreateBranchName: "newbranch-rollback",
		SourceBranchName: "main",
		Author:           &CommitAuthor{Name: "Test User", Email: "test@example.com"},
		Operations: []MultiFileOperation{
			{Path: "missing.txt", Operation: "delete"}, // 존재하지 않는 파일 삭제 -> 실패
		},
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleMultiFileCommit(c)

	requireStatus(t, w, http.StatusNotFound)

	// 롤백 확인: 브랜치가 생성되지 않아야 함
	repo := openRepo(t, repoPath)
	_, err := repo.Reference(plumbing.ReferenceName("refs/heads/newbranch-rollback"), false)
	require.Error(t, err, "branch should be rolled back after commit failure")

	// HEAD가 여전히 main을 가리키는지 확인
	headRef, err := repo.Reference(plumbing.HEAD, false)
	require.NoError(t, err)
	require.Equal(t, plumbing.ReferenceName("refs/heads/main"), headRef.Target())
}

// =============================================================================
// 테스트: File Operations (CRUD)
// 회귀 방지: "파일 생성/수정/삭제 작업이 올바르게 동작해야 함"
// =============================================================================

// Test_FileOperations_CreateUpdateDelete_ReturnsSuccess
// 회귀: 파일 생성 -> 수정 -> 삭제 순서로 동작해야 함
func Test_FileOperations_CreateUpdateDelete_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	// 1. Create
	createReq := MultiFileCommitRequest{
		Message: "Create file",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "test.txt", Operation: "create", Content: "Initial content"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c1, w1 := newRequest(t, "POST", "/repos/testowner/testrepo/commits", createReq)
	c1.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleMultiFileCommit(c1)
	requireStatus(t, w1, http.StatusOK)

	// 2. Update
	updateReq := MultiFileCommitRequest{
		Message: "Update file",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "test.txt", Operation: "update", Content: "Updated content"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c2, w2 := newRequest(t, "POST", "/repos/testowner/testrepo/commits", updateReq)
	c2.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleMultiFileCommit(c2)
	requireStatus(t, w2, http.StatusOK)

	// 3. Delete
	deleteReq := MultiFileCommitRequest{
		Message: "Delete file",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "test.txt", Operation: "delete"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c3, w3 := newRequest(t, "POST", "/repos/testowner/testrepo/commits", deleteReq)
	c3.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleMultiFileCommit(c3)
	requireStatus(t, w3, http.StatusOK)
}

// Test_MultiFileCommit_Subdirectories_ReturnsSuccess
// 회귀: 서브디렉토리 파일 생성이 성공해야 함
func Test_MultiFileCommit_Subdirectories_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	req := MultiFileCommitRequest{
		Message: "Add files in subdirectories",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "src/main.js", Operation: "create", Content: "console.log('main');"},
			{Path: "src/utils/helper.js", Operation: "create", Content: "export function helper() {}"},
			{Path: "docs/README.md", Operation: "create", Content: "# Documentation"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	handler.HandleMultiFileCommit(c)

	requireStatus(t, w, http.StatusOK)
	var resp MultiFileCommitResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Equal(t, 3, resp.SuccessfulOps)
}

// =============================================================================
// 테스트: CommitsAPI - 커밋 조회
// 회귀 방지: "커밋 조회 API가 올바르게 동작해야 함"
// =============================================================================

// Test_CommitsAPI_CompareMode_ReturnsOnlyAheadCommits
// 회귀: source_ref와 target_ref 비교 시 ahead 커밋만 반환해야 함
func Test_CommitsAPI_CompareMode_ReturnsOnlyAheadCommits(t *testing.T) {
	t.Parallel()

	handler, repoPath := newCommitsHandlerWithRepo(t)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	repo := openRepo(t, repoPath)

	// Base commit A on main
	baseHash := getBranchHash(t, repo, "main")
	baseCommit, err := repo.CommitObject(baseHash)
	require.NoError(t, err)
	treeHash := baseCommit.TreeHash

	// Create newbranch commit B from A
	newBranchHash := makeCommit(t, repo, treeHash, "Commit on newbranch", []plumbing.Hash{baseHash}, now)
	setBranchRef(t, repo, "newbranch", newBranchHash)

	// Create merge commit M on main: parents A + B
	mergeHash := makeCommit(t, repo, treeHash, "Merge newbranch into main", []plumbing.Hash{baseHash, newBranchHash}, now)
	setBranchRef(t, repo, "main", mergeHash)

	// Create an extra commit on main
	mainTipHash := makeCommit(t, repo, treeHash, "Main continues", []plumbing.Hash{mergeHash}, now)
	setBranchRef(t, repo, "main", mainTipHash)

	// Branch feat from merge commit M and add one commit
	setBranchRef(t, repo, "feat", mergeHash)
	featHash := makeCommit(t, repo, treeHash, "Feat commit", []plumbing.Hash{mergeHash}, now)
	setBranchRef(t, repo, "feat", featHash)

	router := gin.New()
	router.GET("/repos/:owner/:repo/commits", handler.HandleCommitsAPI)

	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/commits?source_ref=feat&target_ref=main&limit=30&skip=0", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp CommitsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Equal(t, mergeHash.String(), resp.MergeBase)

	// feat 커밋만 포함되어야 함
	var shas []string
	for _, day := range resp.Results {
		for _, c := range day.Commits {
			shas = append(shas, c.SHA)
		}
	}
	require.Contains(t, shas, featHash.String(), "should include feat commit")
	require.NotContains(t, shas, mergeHash.String(), "should NOT include merge-base commit")
}

// =============================================================================
// Helper functions
// 원칙: boilerplate만 추상화, 핵심 로직은 테스트 본문에
// =============================================================================

func newCommitsHandler(t *testing.T) *CommitsHandler {
	t.Helper()

	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "testowner", "testrepo.git")

	err := os.MkdirAll(filepath.Dir(repoPath), 0755)
	require.NoError(t, err)

	repo, err := git.PlainInit(repoPath, true)
	require.NoError(t, err)

	_ = ensureInitialCommit(t, repo, "main")

	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
	}

	handler, err := NewCommitsHandler(cfg)
	require.NoError(t, err)

	return handler
}

func newCommitsHandlerWithRepo(t *testing.T) (*CommitsHandler, string) {
	t.Helper()

	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "testowner", "testrepo.git")

	err := os.MkdirAll(filepath.Dir(repoPath), 0755)
	require.NoError(t, err)

	repo, err := git.PlainInit(repoPath, true)
	require.NoError(t, err)

	_ = ensureInitialCommit(t, repo, "main")

	cfg := &config.Config{
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
	}

	handler, err := NewCommitsHandler(cfg)
	require.NoError(t, err)

	return handler, repoPath
}

func newRequest(t *testing.T, method, url string, body interface{}) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	var bodyReader io.Reader
	if body != nil {
		jsonBody, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(jsonBody)
	}

	c.Request = httptest.NewRequest(method, url, bodyReader)
	if body != nil {
		c.Request.Header.Set("Content-Type", "application/json")
	}

	return c, w
}

func ensureInitialCommit(t *testing.T, repo *git.Repository, defaultBranch string) plumbing.Hash {
	t.Helper()
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	storer := repo.Storer

	// Create an empty tree
	tree := &object.Tree{Entries: []object.TreeEntry{}}
	treeObj := storer.NewEncodedObject()
	require.NoError(t, tree.Encode(treeObj))
	treeHash, err := storer.SetEncodedObject(treeObj)
	require.NoError(t, err)

	// Create an initial commit
	commit := &object.Commit{
		Message:  "Initial commit",
		TreeHash: treeHash,
		Author: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  now,
		},
		Committer: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  now,
		},
	}

	commitObj := storer.NewEncodedObject()
	require.NoError(t, commit.Encode(commitObj))
	commitHash, err := storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	branchRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/"+defaultBranch), commitHash)
	require.NoError(t, storer.SetReference(branchRef))
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, branchRef.Name())
	require.NoError(t, storer.SetReference(headRef))

	return commitHash
}

func makeCommit(t *testing.T, repo *git.Repository, treeHash plumbing.Hash, message string, parents []plumbing.Hash, when time.Time) plumbing.Hash {
	t.Helper()

	commit := &object.Commit{
		Message:      message,
		TreeHash:     treeHash,
		ParentHashes: parents,
		Author: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  when,
		},
		Committer: object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  when,
		},
	}

	obj := repo.Storer.NewEncodedObject()
	require.NoError(t, commit.Encode(obj))
	hash, err := repo.Storer.SetEncodedObject(obj)
	require.NoError(t, err)
	return hash
}

func setBranchRef(t *testing.T, repo *git.Repository, branch string, hash plumbing.Hash) {
	t.Helper()
	ref := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/"+branch), hash)
	require.NoError(t, repo.Storer.SetReference(ref))
}

func getBranchHash(t *testing.T, repo *git.Repository, branch string) plumbing.Hash {
	t.Helper()
	ref, err := repo.Reference(plumbing.ReferenceName("refs/heads/"+branch), false)
	require.NoError(t, err)
	return ref.Hash()
}

func openRepo(t *testing.T, path string) *git.Repository {
	t.Helper()
	repo, err := git.PlainOpen(path)
	require.NoError(t, err)
	return repo
}

// =============================================================================
// Assertion helpers
// 원칙: 실패 메시지에 body 포함, 정답 강제
// =============================================================================

func requireStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status=%d want=%d body=%s", w.Code, want, w.Body.String())
	}
}

func requireBodyContains(t *testing.T, w *httptest.ResponseRecorder, substr string) {
	t.Helper()
	if !strings.Contains(w.Body.String(), substr) {
		t.Errorf("body does not contain %q: %s", substr, w.Body.String())
	}
}
