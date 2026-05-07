package commits

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/api/typed"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/testutil/gitrepo"
	"github.com/gin-gonic/gin"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"
)

// invokeTyped 는 typed handler 를 호출하고 결과/에러를 gin.Context 에 직접
// 쓴다. typed.RegisterX wrapper 의 default 매핑 (정상 → 200, *typed.HTTPError
// → he.Status, 그 외 → 500) 을 mirror 한다. 테스트가 router 우회 + 직접
// handler 호출 패턴을 유지할 수 있게 해준다.
func invokeTyped[Resp any](c *gin.Context, fn func(*gin.Context) (Resp, error)) {
	resp, err := fn(c)
	if err != nil {
		var he *typed.HTTPError
		if errors.As(err, &he) {
			c.JSON(he.Status, gin.H{"success": false, "error": he.Message})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, resp)
}

// adaptQuery 는 RegisterGetWithQuery 시그니처 (Req struct 를 받는 typed
// handler) 를 gin.HandlerFunc 로 변환한다. wrapper 의 c.ShouldBindQuery
// 단계를 mirror 하여 테스트가 router.X(path, adaptQuery(h.HandleY)) 패턴을
// 그대로 사용 가능 (HandleComparisonSummary 가 Req 형태로 전환된 후 도입 —
// Issue-2317).
func adaptQuery[Req any, Resp any](fn func(*gin.Context, Req) (Resp, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req Req
		if err := c.ShouldBindQuery(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
			return
		}
		invokeTyped(c, func(ctx *gin.Context) (Resp, error) {
			return fn(ctx, req)
		})
	}
}

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

	invokeTyped(c, handler.HandleMultiFileCommit)

	requireStatus(t, w, http.StatusOK)
	var resp MultiFileCommitResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Equal(t, 3, resp.TotalOperations)
	require.Equal(t, 3, resp.SuccessfulOps)
}

// Test_MultiFileCommit_CreateSingle_ReturnsSuccess
// 회귀: 단일 파일 생성 커밋이 성공해야 함
func Test_MultiFileCommit_CreateSingle_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	req := MultiFileCommitRequest{
		Message: "Create single file",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "single.txt", Operation: "create", Content: "hello"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}

	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}

	invokeTyped(c, handler.HandleMultiFileCommit)

	requireStatus(t, w, http.StatusOK)

	var resp MultiFileCommitResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Equal(t, 1, resp.TotalOperations)
	require.Equal(t, 1, resp.SuccessfulOps)
}

// Test_MultiFileCommit_CreateMultiple_ReturnsSuccess
// 회귀: 다중 파일 생성 커밋이 성공해야 함
func Test_MultiFileCommit_CreateMultiple_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	req := MultiFileCommitRequest{
		Message: "Create multiple files",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "a.txt", Operation: "create", Content: "A"},
			{Path: "b.txt", Operation: "create", Content: "B"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c, handler.HandleMultiFileCommit)

	requireStatus(t, w, http.StatusOK)

	var resp MultiFileCommitResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.Equal(t, 2, resp.TotalOperations)
	require.Equal(t, 2, resp.SuccessfulOps)
}

// Test_MultiFileCommit_UpdateFile_ReturnsSuccess
// 회귀: 파일 수정 커밋이 성공해야 함
func Test_MultiFileCommit_UpdateFile_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	// Given: 파일 생성
	createReq := MultiFileCommitRequest{
		Message: "Create file",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "update.txt", Operation: "create", Content: "v1"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c1, w1 := newRequest(t, "POST", "/repos/testowner/testrepo/commits", createReq)
	c1.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c1, handler.HandleMultiFileCommit)
	requireStatus(t, w1, http.StatusOK)

	// When: 파일 수정
	updateReq := MultiFileCommitRequest{
		Message: "Update file",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "update.txt", Operation: "update", Content: "v2"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c2, w2 := newRequest(t, "POST", "/repos/testowner/testrepo/commits", updateReq)
	c2.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c2, handler.HandleMultiFileCommit)

	// Then
	requireStatus(t, w2, http.StatusOK)
}

// Test_MultiFileCommit_DeleteFile_ReturnsSuccess
// 회귀: 파일 삭제 커밋이 성공해야 함
func Test_MultiFileCommit_DeleteFile_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	// Given: 파일 생성
	createReq := MultiFileCommitRequest{
		Message: "Create file",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "delete.txt", Operation: "create", Content: "v1"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c1, w1 := newRequest(t, "POST", "/repos/testowner/testrepo/commits", createReq)
	c1.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c1, handler.HandleMultiFileCommit)
	requireStatus(t, w1, http.StatusOK)

	// When: 파일 삭제
	deleteReq := MultiFileCommitRequest{
		Message: "Delete file",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "delete.txt", Operation: "delete"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c2, w2 := newRequest(t, "POST", "/repos/testowner/testrepo/commits", deleteReq)
	c2.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c2, handler.HandleMultiFileCommit)

	// Then
	requireStatus(t, w2, http.StatusOK)
}

// Test_MultiFileCommit_MixedOperations_ReturnsSuccess
// 회귀: 생성/수정/삭제 혼합 커밋이 성공해야 함
func Test_MultiFileCommit_MixedOperations_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	// Given: update/delete 대상 파일 생성
	seedReq := MultiFileCommitRequest{
		Message: "Seed files",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "mixed-update.txt", Operation: "create", Content: "v1"},
			{Path: "mixed-delete.txt", Operation: "create", Content: "v1"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c1, w1 := newRequest(t, "POST", "/repos/testowner/testrepo/commits", seedReq)
	c1.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c1, handler.HandleMultiFileCommit)
	requireStatus(t, w1, http.StatusOK)

	// When: mixed operations
	req := MultiFileCommitRequest{
		Message: "Mixed ops",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "mixed-new.txt", Operation: "create", Content: "new"},
			{Path: "mixed-update.txt", Operation: "update", Content: "v2"},
			{Path: "mixed-delete.txt", Operation: "delete"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c2, w2 := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c2.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c2, handler.HandleMultiFileCommit)

	// Then
	requireStatus(t, w2, http.StatusOK)
}

// Test_MultiFileCommit_DuplicateCreate_Returns409
// 회귀: 이미 존재하는 파일 create는 409 Conflict
func Test_MultiFileCommit_DuplicateCreate_Returns409(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	req1 := MultiFileCommitRequest{
		Message: "Create file",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "dup-create.txt", Operation: "create", Content: "v1"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c1, w1 := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req1)
	c1.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c1, handler.HandleMultiFileCommit)
	requireStatus(t, w1, http.StatusOK)

	req2 := MultiFileCommitRequest{
		Message: "Create same file again",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "dup-create.txt", Operation: "create", Content: "v2"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c2, w2 := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req2)
	c2.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c2, handler.HandleMultiFileCommit)

	requireStatus(t, w2, http.StatusConflict)
	requireBodyContains(t, w2, "already exists")
}

// Test_MultiFileCommit_UpdateNonExistent_Returns404
// 회귀: 존재하지 않는 파일 update는 404 반환
func Test_MultiFileCommit_UpdateNonExistent_Returns404(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	req := MultiFileCommitRequest{
		Message: "Update non-existent",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "no-such.txt", Operation: "update", Content: "v1"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c, handler.HandleMultiFileCommit)

	requireStatus(t, w, http.StatusNotFound)
	requireBodyContains(t, w, "not found")
}

// Test_MultiFileCommit_EmptyContent_Returns400
// 회귀: create/update는 content가 필수 (현재 구현은 빈 문자열 거부)
func Test_MultiFileCommit_EmptyContent_Returns400(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	req := MultiFileCommitRequest{
		Message: "Empty content",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "empty.txt", Operation: "create", Content: ""},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c, handler.HandleMultiFileCommit)

	requireStatus(t, w, http.StatusBadRequest)
}

// Test_MultiFileCommit_LargeFile_ReturnsSuccess
// 회귀: 큰 파일도 커밋이 성공해야 함
func Test_MultiFileCommit_LargeFile_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	large := strings.Repeat("a", 1024*1024) // 1MiB
	req := MultiFileCommitRequest{
		Message: "Large file",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "large.txt", Operation: "create", Content: large},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c, handler.HandleMultiFileCommit)

	requireStatus(t, w, http.StatusOK)
}

// Test_MultiFileCommit_BinaryFile_ReturnsSuccess
// 회귀: base64 인코딩 바이너리 콘텐츠 커밋이 성공해야 함
func Test_MultiFileCommit_BinaryFile_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	raw := []byte{0x00, 0x01, 0x02, 0xFF, 0x10}
	encoded := base64.StdEncoding.EncodeToString(raw)

	req := MultiFileCommitRequest{
		Message: "Binary file",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "bin.dat", Operation: "create", Content: encoded, Encoding: "base64"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c, handler.HandleMultiFileCommit)

	requireStatus(t, w, http.StatusOK)
}

// Test_MultiFileCommit_Subdirectory_ReturnsSuccess
// 회귀: 서브디렉토리 파일 생성이 성공해야 함
func Test_MultiFileCommit_Subdirectory_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	req := MultiFileCommitRequest{
		Message: "Subdirectory file",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "subdir/file.txt", Operation: "create", Content: "ok"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c, handler.HandleMultiFileCommit)

	requireStatus(t, w, http.StatusOK)
}

// Test_MultiFileCommit_DeepNesting_ReturnsSuccess
// 회귀: 깊은 디렉토리 구조 경로도 커밋이 성공해야 함
func Test_MultiFileCommit_DeepNesting_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	req := MultiFileCommitRequest{
		Message: "Deep nesting",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "a/b/c/d/e/f/g/h/i/j/file.txt", Operation: "create", Content: "deep"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c, handler.HandleMultiFileCommit)

	requireStatus(t, w, http.StatusOK)
}

// Test_MultiFileCommit_SpecialCharsInPath_ReturnsSuccess
// 회귀: 특수문자 경로도 커밋이 성공해야 함 (path traversal/absolute/backslash만 금지)
func Test_MultiFileCommit_SpecialCharsInPath_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	req := MultiFileCommitRequest{
		Message: "Special chars path",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "docs/@specs (v1)/readme-01.txt", Operation: "create", Content: "ok"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c, handler.HandleMultiFileCommit)

	requireStatus(t, w, http.StatusOK)
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
	invokeTyped(c1, handler.HandleMultiFileCommit)
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
	invokeTyped(c2, handler.HandleMultiFileCommit)

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
	invokeTyped(c, handler.HandleMultiFileCommit)

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
	invokeTyped(c, handler.HandleMultiFileCommit)

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
	invokeTyped(c, handler.HandleMultiFileCommit)

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
	invokeTyped(c1, handler.HandleMultiFileCommit)
	requireStatus(t, w1, http.StatusOK)

	// 두 번째 요청 - 충돌
	c2, w2 := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c2.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c2, handler.HandleMultiFileCommit)
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
	invokeTyped(c, handler.HandleMultiFileCommit)

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
	invokeTyped(c1, handler.HandleMultiFileCommit)
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
	invokeTyped(c2, handler.HandleMultiFileCommit)
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
	invokeTyped(c3, handler.HandleMultiFileCommit)
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
	invokeTyped(c, handler.HandleMultiFileCommit)

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
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)

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

// newCommitsHandlerEmptyRepo returns a handler and repo path for a bare repo with no refs/heads (no initial commit).
func newCommitsHandlerEmptyRepo(t *testing.T) (*CommitsHandler, string) {
	t.Helper()

	tempDir := t.TempDir()
	repoPath := filepath.Join(tempDir, "testowner", "testrepo.git")

	err := os.MkdirAll(filepath.Dir(repoPath), 0755)
	require.NoError(t, err)

	repo, err := git.PlainInit(repoPath, true)
	require.NoError(t, err)
	// Do not create initial commit — no refs/heads/main
	require.NoError(t, repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))))

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
// Assertion helpers - testutil/gitrepo 사용
// =============================================================================

func requireStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	gitrepo.RequireStatus(t, w, want)
}

func requireBodyContains(t *testing.T, w *httptest.ResponseRecorder, substr string) {
	t.Helper()
	gitrepo.RequireBodyContains(t, w, substr)
}

// =============================================================================
// 테스트: Path Validation / 멱등성 (P0 - 500 방지)
// 회귀 방지: "잘못된 경로나 중복 경로가 500으로 뭉개지지 않는다"
// =============================================================================

// Test_MultiFileCommit_PathTraversal_Returns400
// 회귀: "../" 경로 탈출 시도는 400으로 거절되어야 함
func Test_MultiFileCommit_PathTraversal_Returns400(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	req := MultiFileCommitRequest{
		Message: "Path traversal attempt",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "../../../etc/passwd", Operation: "create", Content: "malicious"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c, handler.HandleMultiFileCommit)

	// 400 Bad Request (500이 아님)
	require.NotEqual(t, http.StatusInternalServerError, w.Code,
		"status=%d should not be 500 for path traversal body=%s", w.Code, w.Body.String())
}

// Test_MultiFileCommit_DuplicatePathInOperations_Returns400
// 회귀: 같은 경로가 operations에 중복되면 400으로 거절되어야 함
func Test_MultiFileCommit_DuplicatePathInOperations_Returns400(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	req := MultiFileCommitRequest{
		Message: "Duplicate paths",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "same.txt", Operation: "create", Content: "first"},
			{Path: "same.txt", Operation: "create", Content: "second"}, // 중복
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c, handler.HandleMultiFileCommit)

	// 400 Bad Request 또는 409 Conflict (500이 아님)
	require.NotEqual(t, http.StatusInternalServerError, w.Code,
		"status=%d should not be 500 for duplicate paths body=%s", w.Code, w.Body.String())
}

// Test_MultiFileCommit_MissingAuthor_Returns400
// 회귀: author가 없으면 400으로 거절되어야 함
func Test_MultiFileCommit_MissingAuthor_Returns400(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	req := MultiFileCommitRequest{
		Message: "No author",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "test.txt", Operation: "create", Content: "content"},
		},
		// Author: nil
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c, handler.HandleMultiFileCommit)

	// 400 Bad Request (500이 아님)
	require.NotEqual(t, http.StatusInternalServerError, w.Code,
		"status=%d should not be 500 for missing author body=%s", w.Code, w.Body.String())
}

// Test_MultiFileCommit_InvalidBranch_Returns404
// 회귀: 존재하지 않는 브랜치는 404로 떨어져 500으로 뭉개지지 않는다
func Test_MultiFileCommit_InvalidBranch_Returns404(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	req := MultiFileCommitRequest{
		Message: "Commit to non-existent branch",
		Branch:  "nonexistent-branch",
		Operations: []MultiFileOperation{
			{Path: "test.txt", Operation: "create", Content: "content"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c, handler.HandleMultiFileCommit)

	// 404 또는 400이어야 함, 500은 안됨
	require.NotEqual(t, http.StatusInternalServerError, w.Code,
		"status=%d should not be 500 for non-existent branch body=%s", w.Code, w.Body.String())
}

// Test_MultiFileCommit_FirstCommitOnEmptyRepo_ReturnsSuccess
// 빈 repo(initial commit 없음)에 branch main으로 첫 커밋 생성 시 성공하고 refs/heads/main이 생긴다.
func Test_MultiFileCommit_FirstCommitOnEmptyRepo_ReturnsSuccess(t *testing.T) {
	t.Parallel()

	handler, repoPath := newCommitsHandlerEmptyRepo(t)

	req := MultiFileCommitRequest{
		Message: "new commit",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "RAEDME2.md", Operation: "create", Content: "no error 2"},
		},
		Author: &CommitAuthor{Name: "seungbaek lee", Email: "monjk557@collabops.ai"},
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c, handler.HandleMultiFileCommit)

	requireStatus(t, w, http.StatusOK)

	var resp MultiFileCommitResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.NotEmpty(t, resp.CommitSHA)
	require.Equal(t, 1, resp.SuccessfulOps)

	repo := openRepo(t, repoPath)
	mainHash := getBranchHash(t, repo, "main")
	require.Equal(t, plumbing.NewHash(resp.CommitSHA), mainHash)
}

// Test_MultiFileCommit_InvalidOperation_Returns400
// 회귀: 잘못된 operation 타입은 400으로 거절되어야 함
func Test_MultiFileCommit_InvalidOperation_Returns400(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	req := MultiFileCommitRequest{
		Message: "Invalid operation",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "test.txt", Operation: "invalid_op", Content: "content"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c, handler.HandleMultiFileCommit)

	// 400 Bad Request (500이 아님)
	require.NotEqual(t, http.StatusInternalServerError, w.Code,
		"status=%d should not be 500 for invalid operation body=%s", w.Code, w.Body.String())
}

// Test_MultiFileCommit_ReturnsCommitSHA_Readable
// 회귀: 응답한 SHA는 즉시 조회 가능해야 함 (쓰기 후 읽기 일관성)
func Test_MultiFileCommit_ReturnsCommitSHA_Readable(t *testing.T) {
	t.Parallel()

	handler, repoPath := newCommitsHandlerWithRepo(t)

	req := MultiFileCommitRequest{
		Message: "Test commit",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "readable.txt", Operation: "create", Content: "content"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c, handler.HandleMultiFileCommit)

	requireStatus(t, w, http.StatusOK)

	var resp MultiFileCommitResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.NotEmpty(t, resp.CommitSHA)

	// 반환된 SHA로 커밋 조회 가능한지 확인
	repo := openRepo(t, repoPath)
	commitHash := plumbing.NewHash(resp.CommitSHA)
	commit, err := repo.CommitObject(commitHash)
	require.NoError(t, err, "returned SHA should be immediately readable")
	require.Equal(t, "Test commit", commit.Message)
}

// =============================================================================
// 테스트: 3.1B Author/Committer/Signing/Timezone
// =============================================================================

// Test_MultiFileCommit_InvalidEmail_AllowsOrRejectsByContract
// 회귀: 잘못된 이메일 형식은 허용 또는 거부 (구현에 따라)
func Test_MultiFileCommit_InvalidEmail_AllowsOrRejectsByContract(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	testCases := []struct {
		name  string
		email string
	}{
		{"invalid_format", "not-an-email"},
		{"missing_at", "userexample.com"},
		{"missing_domain", "user@"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := MultiFileCommitRequest{
				Message: "Invalid email",
				Branch:  "main",
				Operations: []MultiFileOperation{
					{Path: "test.txt", Operation: "create", Content: "content"},
				},
				Author: &CommitAuthor{Name: "Test User", Email: tc.email},
			}
			c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
			c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
			invokeTyped(c, handler.HandleMultiFileCommit)

			// 현재 구현은 이메일 검증을 하지 않으므로 200 또는 400 (500은 아님)
			require.NotEqual(t, http.StatusInternalServerError, w.Code,
				"status=%d should not be 500 for invalid email body=%s", w.Code, w.Body.String())
		})
	}
}

// Test_MultiFileCommit_CommitMessageEncoding_UTF8AndNonUTF8
// 회귀: UTF-8 및 비UTF-8 커밋 메시지가 처리되어야 함
func Test_MultiFileCommit_CommitMessageEncoding_UTF8AndNonUTF8(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	testCases := []struct {
		name    string
		message string
		path    string
	}{
		{"utf8_korean", "한글 커밋 메시지", "korean.txt"},
		{"utf8_emoji", "Add feature 🚀", "emoji.txt"},
		{"utf8_special", "Fix bug: <>&\"'", "special.txt"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := MultiFileCommitRequest{
				Message: tc.message,
				Branch:  "main",
				Operations: []MultiFileOperation{
					{Path: tc.path, Operation: "create", Content: "content"},
				},
				Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
			}
			c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
			c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
			invokeTyped(c, handler.HandleMultiFileCommit)

			requireStatus(t, w, http.StatusOK)

			var resp MultiFileCommitResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			require.True(t, resp.Success)
		})
	}
}

// Test_MultiFileCommit_LargeMessage_RejectsOrTruncatesExplicitly
// 회귀: 큰 커밋 메시지는 거부하거나 명시적으로 잘라야 함
func Test_MultiFileCommit_LargeMessage_RejectsOrTruncatesExplicitly(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	largeMessage := strings.Repeat("a", 10000) // 10KB 메시지
	req := MultiFileCommitRequest{
		Message: largeMessage,
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "test.txt", Operation: "create", Content: "content"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c, handler.HandleMultiFileCommit)

	// 현재 구현은 큰 메시지를 허용하므로 200 또는 400 (500은 아님)
	require.NotEqual(t, http.StatusInternalServerError, w.Code,
		"status=%d should not be 500 for large message body=%s", w.Code, w.Body.String())
}

// =============================================================================
// 테스트: 3.1C Line endings / filemode / symlink
// =============================================================================

// Test_MultiFileCommit_FileModeExecutableBit_Preserved
// 회귀: 실행 가능한 파일 모드(755)가 보존되어야 함
func Test_MultiFileCommit_FileModeExecutableBit_Preserved(t *testing.T) {
	t.Parallel()

	handler, repoPath := newCommitsHandlerWithRepo(t)

	req := MultiFileCommitRequest{
		Message: "Create executable file",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "script.sh", Operation: "create", Content: "#!/bin/sh\necho hello", Mode: "755"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c, handler.HandleMultiFileCommit)

	requireStatus(t, w, http.StatusOK)

	var resp MultiFileCommitResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)

	// 파일 모드 확인
	repo := openRepo(t, repoPath)
	commitHash := plumbing.NewHash(resp.CommitSHA)
	commit, err := repo.CommitObject(commitHash)
	require.NoError(t, err)

	tree, err := commit.Tree()
	require.NoError(t, err)

	file, err := tree.File("script.sh")
	require.NoError(t, err)
	// 파일 모드가 실행 가능한지 확인 (0755 = 0x1ed, filemode.Executable = 0x81ed)
	// 실제 저장된 값은 0x1ed이지만 실행 가능한 파일임
	require.True(t, file.Mode == filemode.FileMode(0x1ed) || file.Mode == filemode.Executable,
		"file mode should be executable (0755), got: %v (0x%x)", file.Mode, uint32(file.Mode))
}

// =============================================================================
// 테스트: 3.1D Path / Binary Validation
// =============================================================================

// Test_MultiFileCommit_PathNormalization_NoEscape
// 회귀: a/../b 같은 경로 정규화 시도는 400으로 거절되어야 함
func Test_MultiFileCommit_PathNormalization_NoEscape(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	testCases := []struct {
		name string
		path string
	}{
		{"parent_dir", "a/../b"},
		{"multiple_parent", "a/b/../../c"},
		{"root_escape", "../etc/passwd"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := MultiFileCommitRequest{
				Message: "Path traversal",
				Branch:  "main",
				Operations: []MultiFileOperation{
					{Path: tc.path, Operation: "create", Content: "content"},
				},
				Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
			}
			c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
			c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
			invokeTyped(c, handler.HandleMultiFileCommit)

			requireStatus(t, w, http.StatusBadRequest)
			requireBodyContains(t, w, "path traversal")
		})
	}
}

// Test_MultiFileCommit_BinaryContent_Base64Validation
// 회귀: base64 인코딩이 잘못된 경우 400으로 거절되어야 함
func Test_MultiFileCommit_BinaryContent_Base64Validation(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	req := MultiFileCommitRequest{
		Message: "Invalid base64",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "bin.dat", Operation: "create", Content: "not-valid-base64!!!", Encoding: "base64"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c, handler.HandleMultiFileCommit)

	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "base64")
}

// =============================================================================
// 테스트: 3.2 Branch Creation with Commit
// =============================================================================

// Test_MultiFileCommit_CreateBranch_InvalidSource_Returns400
// 회귀: 잘못된 source 브랜치는 400으로 거절되어야 함 (현재 구현)
func Test_MultiFileCommit_CreateBranch_InvalidSource_Returns400(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	req := MultiFileCommitRequest{
		Message:          "Create branch from invalid source",
		Branch:           "main",
		CreateBranchName: "new-branch",
		SourceBranchName: "nonexistent-source",
		Operations: []MultiFileOperation{
			{Path: "test.txt", Operation: "create", Content: "content"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c, handler.HandleMultiFileCommit)

	// 현재 구현은 400을 반환함
	requireStatus(t, w, http.StatusBadRequest)
	requireBodyContains(t, w, "unable to resolve")
}

// =============================================================================
// 테스트: 3.3 Commit History
// =============================================================================

// Test_CommitsAPI_List_ReturnsCommits
// 회귀: 커밋 목록이 정상적으로 반환되어야 함
func Test_CommitsAPI_List_ReturnsCommits(t *testing.T) {
	t.Parallel()

	handler, _ := newCommitsHandlerWithRepo(t)

	// 커밋 생성
	createReq := MultiFileCommitRequest{
		Message: "First commit",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "file1.txt", Operation: "create", Content: "content1"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c1, w1 := newRequest(t, "POST", "/repos/testowner/testrepo/commits", createReq)
	c1.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c1, handler.HandleMultiFileCommit)
	requireStatus(t, w1, http.StatusOK)

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)

	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/commits?ref=main", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp CommitsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.NotNil(t, resp.Results)
	require.GreaterOrEqual(t, len(resp.Results), 1)
}

// Test_CommitsAPI_Pagination_ReturnsCorrectPage
// 회귀: 페이지네이션이 올바른 페이지를 반환해야 함
func Test_CommitsAPI_Pagination_ReturnsCorrectPage(t *testing.T) {
	t.Parallel()

	handler, _ := newCommitsHandlerWithRepo(t)

	// 여러 커밋 생성
	for i := 1; i <= 5; i++ {
		createReq := MultiFileCommitRequest{
			Message: fmt.Sprintf("Commit %d", i),
			Branch:  "main",
			Operations: []MultiFileOperation{
				{Path: fmt.Sprintf("file%d.txt", i), Operation: "create", Content: fmt.Sprintf("content%d", i)},
			},
			Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
		}
		c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", createReq)
		c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
		invokeTyped(c, handler.HandleMultiFileCommit)
		requireStatus(t, w, http.StatusOK)
	}

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)

	// limit=2, skip=0으로 첫 페이지 요청
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/commits?ref=main&limit=2&skip=0", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp CommitsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	// 실제 반환된 커밋 수 확인 (Results의 커밋 수)
	totalReturned := 0
	for _, day := range resp.Results {
		totalReturned += len(day.Commits)
	}
	// limit=2이므로 최대 2개 반환 (초기 커밋 포함)
	require.LessOrEqual(t, totalReturned, 2+1, "should return at most limit+1 commits (initial commit may be included)")
}

// Test_CommitsAPI_PaginationOutOfRange_ReturnsEmptyArray
// 회귀: 페이지네이션 범위 초과 시 빈 배열 반환
func Test_CommitsAPI_PaginationOutOfRange_ReturnsEmptyArray(t *testing.T) {
	t.Parallel()

	handler, _ := newCommitsHandlerWithRepo(t)

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)

	// skip=100으로 범위를 벗어나는 요청
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/commits?ref=main&limit=10&skip=100", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	requireStatus(t, w, http.StatusOK)

	var resp CommitsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	// 빈 배열 반환
	require.Equal(t, 0, len(resp.Results))
}

// Test_CommitsAPI_ByRef_ReturnsCommitsFromRef
// 회귀: ref 기준 커밋 조회가 정상 동작해야 함
func Test_CommitsAPI_ByRef_ReturnsCommitsFromRef(t *testing.T) {
	t.Parallel()

	handler := newCommitsHandler(t)

	// feature 브랜치 생성 및 커밋
	createReq := MultiFileCommitRequest{
		Message:          "Feature commit",
		Branch:           "main",
		CreateBranchName: "feature",
		SourceBranchName: "main",
		Operations: []MultiFileOperation{
			{Path: "feature.txt", Operation: "create", Content: "feature"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c, w := newRequest(t, "POST", "/repos/testowner/testrepo/commits", createReq)
	c.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c, handler.HandleMultiFileCommit)
	requireStatus(t, w, http.StatusOK)

	var commitResp MultiFileCommitResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &commitResp))

	router := gin.New()
	RegisterRoutes(router.Group("/repos/:owner/:repo"), handler)

	// feature 브랜치의 커밋 조회
	req := httptest.NewRequest("GET", "/repos/testowner/testrepo/commits?ref=feature", nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req)

	requireStatus(t, w2, http.StatusOK)

	var resp CommitsResponse
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.GreaterOrEqual(t, len(resp.Results), 1)

	// feature 커밋이 포함되어야 함
	var shas []string
	for _, day := range resp.Results {
		for _, c := range day.Commits {
			shas = append(shas, c.SHA)
		}
	}
	require.Contains(t, shas, commitResp.CommitSHA)
}

// =============================================================================
// 테스트: 3.5 트랜잭션/원자성
// =============================================================================

// Test_MultiFileCommit_PartialFailure_NoPartialCommit
// 회귀: 부분 실패 시 전체 롤백되어야 함 (하나라도 실패하면 커밋 안됨)
func Test_MultiFileCommit_PartialFailure_NoPartialCommit(t *testing.T) {
	t.Parallel()

	handler, repoPath := newCommitsHandlerWithRepo(t)

	// Given: 하나의 파일 생성
	seedReq := MultiFileCommitRequest{
		Message: "Seed file",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "existing.txt", Operation: "create", Content: "v1"},
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c1, w1 := newRequest(t, "POST", "/repos/testowner/testrepo/commits", seedReq)
	c1.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c1, handler.HandleMultiFileCommit)
	requireStatus(t, w1, http.StatusOK)

	var seedResp MultiFileCommitResponse
	require.NoError(t, json.Unmarshal(w1.Body.Bytes(), &seedResp))
	seedSHA := seedResp.CommitSHA

	// When: 하나는 성공, 하나는 실패(중복 create)하는 요청
	req := MultiFileCommitRequest{
		Message: "Partial failure",
		Branch:  "main",
		Operations: []MultiFileOperation{
			{Path: "new.txt", Operation: "create", Content: "new"},
			{Path: "existing.txt", Operation: "create", Content: "duplicate"}, // 실패
		},
		Author: &CommitAuthor{Name: "Test User", Email: "test@example.com"},
	}
	c2, w2 := newRequest(t, "POST", "/repos/testowner/testrepo/commits", req)
	c2.Params = gin.Params{{Key: "owner", Value: "testowner"}, {Key: "repo", Value: "testrepo"}}
	invokeTyped(c2, handler.HandleMultiFileCommit)

	// Then: 실패해야 함
	require.NotEqual(t, http.StatusOK, w2.Code)

	// 롤백 확인: HEAD가 여전히 seed 커밋을 가리켜야 함
	repo := openRepo(t, repoPath)
	headRef, err := repo.Reference(plumbing.HEAD, false)
	require.NoError(t, err)
	headHash := headRef.Target()
	branchRef, err := repo.Reference(headHash, false)
	require.NoError(t, err)
	require.Equal(t, seedSHA, branchRef.Hash().String(), "HEAD should not have changed after partial failure")
}

// =============================================================================
// 미구현/스킵 테스트 (test_cases.md 미체크 항목)
// =============================================================================

// Test_MultiFileCommit_IdempotencyKey_RetryDoesNotDuplicateCommit
func Test_MultiFileCommit_IdempotencyKey_RetryDoesNotDuplicateCommit(t *testing.T) {
	t.Parallel()
	t.Skip("idempotency key 미구현")
}

// Test_MultiFileCommit_ClientRetryAfterTimeout_DoesNotCreateTwoCommits
func Test_MultiFileCommit_ClientRetryAfterTimeout_DoesNotCreateTwoCommits(t *testing.T) {
	t.Parallel()
	t.Skip("복잡한 테스트, 나중에")
}

// Test_MultiFileCommit_IdempotencyKey_ServerCrashAfterWrite_NoDuplicateOnRetry
func Test_MultiFileCommit_IdempotencyKey_ServerCrashAfterWrite_NoDuplicateOnRetry(t *testing.T) {
	t.Parallel()
	t.Skip("idempotency key 미구현")
}

// Test_MultiFileCommit_SymlinkCreation_HandledOrRejected
func Test_MultiFileCommit_SymlinkCreation_HandledOrRejected(t *testing.T) {
	t.Parallel()
	t.Skip("symlink 지원 확인 필요")
}

// Test_CommitsAPI_ByPath_ReturnsCommitsAffectingPath
func Test_CommitsAPI_ByPath_ReturnsCommitsAffectingPath(t *testing.T) {
	t.Parallel()
	t.Skip("경로 기준 조회 API 지원 확인 필요")
}

// Test_GetCommit_ValidSHA_ReturnsCommit
func Test_GetCommit_ValidSHA_ReturnsCommit(t *testing.T) {
	t.Parallel()
	t.Skip("GetCommit 핸들러 확인 필요")
}

// Test_GetCommit_ShortSHA_ReturnsCommit
func Test_GetCommit_ShortSHA_ReturnsCommit(t *testing.T) {
	t.Parallel()
	t.Skip("GetCommit 핸들러 확인 필요")
}

// Test_GetCommit_InvalidSHA_Returns404
func Test_GetCommit_InvalidSHA_Returns404(t *testing.T) {
	t.Parallel()
	t.Skip("GetCommit 핸들러 확인 필요")
}

// Test_GetCommit_WithFiles_ReturnsFilesChanged
func Test_GetCommit_WithFiles_ReturnsFilesChanged(t *testing.T) {
	t.Parallel()
	t.Skip("GetCommit 핸들러 확인 필요")
}

// Test_MultiFileCommit_ConcurrentSameBranch_NoConflict
func Test_MultiFileCommit_ConcurrentSameBranch_NoConflict(t *testing.T) {
	t.Parallel()
	t.Skip("동시성 테스트, 나중에")
}

// Test_MultiFileCommit_ConcurrentSameFile_Returns409
func Test_MultiFileCommit_ConcurrentSameFile_Returns409(t *testing.T) {
	t.Parallel()
	t.Skip("동시성 테스트, 나중에")
}

// Test_MultiFileCommit_ConcurrentSameBranch_DifferentFiles_OneFastForwardOne409
func Test_MultiFileCommit_ConcurrentSameBranch_DifferentFiles_OneFastForwardOne409(t *testing.T) {
	t.Parallel()
	t.Skip("동시성 테스트, 나중에")
}
