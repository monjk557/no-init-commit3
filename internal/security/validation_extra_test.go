package security

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/collabops/git-executor/internal/logging"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

// 기존 validation_test.go 가 다루지 않는 순수 검증 경로를 채운다.

// --- 생성자 / 설정 ---

// Test_NewValidationFramework_NilConfig_UsesDefaults nil config 시 기본값 사용
func Test_NewValidationFramework_NilConfig_UsesDefaults(t *testing.T) {
	logger := &logging.Logger{Logger: zaptest.NewLogger(t)}
	vf, err := NewValidationFramework(nil, logger)
	require.NoError(t, err)
	require.NotNil(t, vf)
	require.Equal(t, int64(100*1024*1024), vf.config.MaxFileSize)
}

// Test_NewValidationFramework_NilLogger_Errors logger 없으면 에러
func Test_NewValidationFramework_NilLogger_Errors(t *testing.T) {
	vf, err := NewValidationFramework(DefaultValidationConfig(), nil)
	require.Error(t, err)
	require.Nil(t, vf)
}

// Test_DefaultValidationConfig_HasBannedExtensions 기본 금지 확장자 포함
func Test_DefaultValidationConfig_HasBannedExtensions(t *testing.T) {
	cfg := DefaultValidationConfig()
	require.Contains(t, cfg.BannedFileExtensions, ".exe")
	require.Contains(t, cfg.BannedFileExtensions, ".sh")
	require.True(t, cfg.EnablePathCleaning)
	require.True(t, cfg.EnableContentValidation)
}

// --- isAPIEndpoint ---

// Test_isAPIEndpoint_Matrix API 접두사 매칭 테이블
func Test_isAPIEndpoint_Matrix(t *testing.T) {
	vf := validationFrameworkForTest(t)
	cases := []struct {
		path string
		want bool
	}{
		{"/repos/o/r", true},
		{"/api/repos/o/r", true},
		{"/healthz", false},
		{"/metrics", false},
		{"", false},
		{"/repo/o/r", false},
	}
	for _, tc := range cases {
		require.Equalf(t, tc.want, vf.isAPIEndpoint(tc.path), "path=%q", tc.path)
	}
}

// Test_Middleware_SkipsNonAPIEndpoint 비-API 경로는 검증 스킵
func Test_Middleware_SkipsNonAPIEndpoint(t *testing.T) {
	vf := validationFrameworkForTest(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(vf.ValidateRequestMiddleware())
	r.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusOK) })

	// 경로 traversal 이 들어가도 비-API 라 통과해야 함
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}

// --- validatePathComponent (직접 호출, 순수) ---

func Test_validatePathComponent_Table(t *testing.T) {
	vf := validationFrameworkForTest(t)
	cases := []struct {
		name      string
		component string
		wantErr   bool
	}{
		{"valid", "my-repo_1.git", false},
		{"empty", "", true},
		{"too long", strings.Repeat("a", 256), true},
		{"null byte", "a\x00b", true},
		{"traversal", "a..b", true},
		{"leading slash", "/abs", true},
		{"invalid chars", "a b", true},
		{"invalid special", "repo@name", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := vf.validatePathComponent(tc.component, "owner")
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// --- validateFilePath (직접 호출, 순수) ---

func Test_validateFilePath_Table(t *testing.T) {
	vf := validationFrameworkForTest(t)
	cases := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"valid nested", "src/main/app.go", false},
		{"valid dotfile", "config/.env.example", false},
		{"empty", "", true},
		{"too long", strings.Repeat("a", 4097), true},
		{"null byte", "a\x00b", true},
		{"traversal", "../secret", true},
		{"absolute", "/etc/passwd", true},
		{"git internal", ".git/config", true},
		{"banned ext exe", "malware.exe", true},
		{"banned ext sh", "run.sh", true},
		{"unclean trailing slash dot", "foo/./bar", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := vf.validateFilePath(tc.path)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// Test_validateFilePath_NoCleaning_AllowsUnclean PathCleaning off 시 unclean 허용
func Test_validateFilePath_NoCleaning_AllowsUnclean(t *testing.T) {
	cfg := DefaultValidationConfig()
	cfg.EnablePathCleaning = false
	cfg.EnableContentValidation = false
	vf, err := NewValidationFramework(cfg, &logging.Logger{Logger: zaptest.NewLogger(t)})
	require.NoError(t, err)

	// 정리 비활성 + ".." 없음 → 통과
	require.NoError(t, vf.validateFilePath("foo/./bar"))
	// 정리 비활성이어도 banned ext 검증은 꺼져 있으므로 .exe 통과
	require.NoError(t, vf.validateFilePath("a.exe"))
}

// --- validateBranchName (직접 호출, 순수) ---

func Test_validateBranchName_Table(t *testing.T) {
	vf := validationFrameworkForTest(t)
	cases := []struct {
		name    string
		branch  string
		wantErr bool
	}{
		{"valid", "feature/my-branch", false},
		{"valid release", "release-1.2.3", false},
		{"empty", "", true},
		{"too long", strings.Repeat("a", 256), true},
		{"invalid space", "my branch", true},
		{"leading dash", "-bad", true},
		{"trailing dot", "bad.", true},
		{"invalid tilde", "ma~in", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := vf.validateBranchName(tc.branch)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// --- validateRef (직접 호출, 순수) ---

func Test_validateRef_Table(t *testing.T) {
	vf := validationFrameworkForTest(t)
	cases := []struct {
		name    string
		ref     string
		wantErr bool
	}{
		{"commit hash", "a1b2c3d", false},
		{"full commit hash", "0123456789abcdef0123456789abcdef01234567", false},
		{"ref name", "refs/heads/main", false},
		{"empty", "", true},
		{"invalid space", "refs heads", true},
		{"invalid colon", "refs:main", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := vf.validateRef(tc.ref)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// --- 커스텀 validator/v10 함수 (struct 검증 경유) ---

// Test_validateFileOperationRequest_Body 정상/비정상 body 검증
func Test_validateFileOperationRequest_Body(t *testing.T) {
	vf := validationFrameworkForTest(t)

	valid := []byte(`{"content":"hello","commit_message":"msg","branch":"main","author_name":"a","author_email":"a@b.com"}`)
	require.NoError(t, vf.validateFileOperationRequest(valid))

	// 잘못된 JSON
	require.Error(t, vf.validateFileOperationRequest([]byte(`{not json`)))

	// 필수 필드 누락 (commit_message)
	missing := []byte(`{"content":"hello","branch":"main","author_name":"a","author_email":"a@b.com"}`)
	require.Error(t, vf.validateFileOperationRequest(missing))

	// 잘못된 이메일
	badEmail := []byte(`{"content":"hi","commit_message":"m","branch":"main","author_name":"a","author_email":"notanemail"}`)
	require.Error(t, vf.validateFileOperationRequest(badEmail))

	// git_branch 위반 (브랜치에 공백)
	badBranch := []byte(`{"content":"hi","commit_message":"m","branch":"bad branch","author_name":"a","author_email":"a@b.com"}`)
	require.Error(t, vf.validateFileOperationRequest(badBranch))
}

// Test_validateFileOperationRequest_ContentTooLarge content 가 MaxFileSize 초과
func Test_validateFileOperationRequest_ContentTooLarge(t *testing.T) {
	cfg := DefaultValidationConfig()
	cfg.MaxFileSize = 5
	vf, err := NewValidationFramework(cfg, &logging.Logger{Logger: zaptest.NewLogger(t)})
	require.NoError(t, err)

	body := []byte(`{"content":"0123456789","commit_message":"m","branch":"main","author_name":"a","author_email":"a@b.com"}`)
	require.Error(t, vf.validateFileOperationRequest(body))
}

// Test_validateFileDeleteRequest_Body delete 요청 검증
func Test_validateFileDeleteRequest_Body(t *testing.T) {
	vf := validationFrameworkForTest(t)
	valid := []byte(`{"commit_message":"del","branch":"main","author_name":"a","author_email":"a@b.com"}`)
	require.NoError(t, vf.validateFileDeleteRequest(valid))

	require.Error(t, vf.validateFileDeleteRequest([]byte(`{bad`)))
	require.Error(t, vf.validateFileDeleteRequest([]byte(`{"branch":"main"}`)))
}

// Test_validateMultiFileCommitRequest_Body 멀티파일 커밋 검증
func Test_validateMultiFileCommitRequest_Body(t *testing.T) {
	vf := validationFrameworkForTest(t)
	valid := []byte(`{"files":[{"path":"a.txt","operation":"create","content":"x"}],"commit_message":"m","branch":"main","author_name":"a","author_email":"a@b.com"}`)
	require.NoError(t, vf.validateMultiFileCommitRequest(valid))

	require.Error(t, vf.validateMultiFileCommitRequest([]byte(`{bad`)))

	// 잘못된 operation
	badOp := []byte(`{"files":[{"path":"a.txt","operation":"nuke","content":"x"}],"commit_message":"m","branch":"main","author_name":"a","author_email":"a@b.com"}`)
	require.Error(t, vf.validateMultiFileCommitRequest(badOp))

	// git_path 위반 (traversal)
	badPath := []byte(`{"files":[{"path":"../a.txt","operation":"create","content":"x"}],"commit_message":"m","branch":"main","author_name":"a","author_email":"a@b.com"}`)
	require.Error(t, vf.validateMultiFileCommitRequest(badPath))
}

// Test_validateMultiFileCommitRequest_TooManyFiles 파일 수 초과
func Test_validateMultiFileCommitRequest_TooManyFiles(t *testing.T) {
	cfg := DefaultValidationConfig()
	cfg.MaxFilesPerCommit = 1
	vf, err := NewValidationFramework(cfg, &logging.Logger{Logger: zaptest.NewLogger(t)})
	require.NoError(t, err)

	body := []byte(`{"files":[{"path":"a.txt","operation":"create","content":"x"},{"path":"b.txt","operation":"create","content":"y"}],"commit_message":"m","branch":"main","author_name":"a","author_email":"a@b.com"}`)
	require.Error(t, vf.validateMultiFileCommitRequest(body))
}

// Test_validateBranchOperationRequest_Body 브랜치 작업 검증
func Test_validateBranchOperationRequest_Body(t *testing.T) {
	vf := validationFrameworkForTest(t)
	require.NoError(t, vf.validateBranchOperationRequest([]byte(`{"branch_name":"feature/x"}`)))
	require.NoError(t, vf.validateBranchOperationRequest([]byte(`{"branch_name":"feature/x","start_point":"main"}`)))
	require.Error(t, vf.validateBranchOperationRequest([]byte(`{bad`)))
	require.Error(t, vf.validateBranchOperationRequest([]byte(`{"branch_name":"bad branch"}`)))
}

// Test_validateMergeRequest_Body 머지 요청 검증
func Test_validateMergeRequest_Body(t *testing.T) {
	vf := validationFrameworkForTest(t)
	valid := []byte(`{"source_branch":"feat","target_branch":"main","commit_message":"m","author_name":"a","author_email":"a@b.com"}`)
	require.NoError(t, vf.validateMergeRequest(valid))
	require.Error(t, vf.validateMergeRequest([]byte(`{bad`)))
	require.Error(t, vf.validateMergeRequest([]byte(`{"source_branch":"feat"}`)))
}

// --- validateRequestBody (미들웨어 경유, 엔드포인트 라우팅) ---

// reqWithBody 는 body 검증 미들웨어를 거치는 라우터 요청을 생성한다.
func runBodyValidation(t *testing.T, method, path, contentType, body string) int {
	t.Helper()
	vf := validationFrameworkForTest(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(vf.ValidateRequestMiddleware())
	handler := func(c *gin.Context) { c.Status(http.StatusOK) }
	r.PUT("/repos/:owner/:repo/files/:path", handler)
	r.DELETE("/repos/:owner/:repo/files/:path", handler)
	r.POST("/repos/:owner/:repo/commits", handler)
	r.POST("/repos/:owner/:repo/branches", handler)
	r.PUT("/repos/:owner/:repo/branches", handler)
	r.POST("/repos/:owner/:repo/merge", handler)

	req := httptest.NewRequest(method, path, nil)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.ContentLength = int64(len(body))
	req.Body = io.NopCloser(strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec.Code
}

// Test_RequestBody_NonJSONContentType_Rejected JSON 아닌 Content-Type 거부
func Test_RequestBody_NonJSONContentType_Rejected(t *testing.T) {
	code := runBodyValidation(t, http.MethodPut, "/repos/o/r/files/a.txt", "text/plain", `hello`)
	require.Equal(t, http.StatusBadRequest, code)
}

// Test_RequestBody_DeleteEndpoint_Valid DELETE 파일 엔드포인트 라우팅
func Test_RequestBody_DeleteEndpoint_Valid(t *testing.T) {
	body := `{"commit_message":"del","branch":"main","author_name":"a","author_email":"a@b.com"}`
	code := runBodyValidation(t, http.MethodDelete, "/repos/o/r/files/a.txt", "application/json", body)
	require.Equal(t, http.StatusOK, code)
}

// Test_RequestBody_CommitsEndpoint_Valid POST /commits 라우팅
func Test_RequestBody_CommitsEndpoint_Valid(t *testing.T) {
	body := `{"files":[{"path":"a.txt","operation":"create","content":"x"}],"commit_message":"m","branch":"main","author_name":"a","author_email":"a@b.com"}`
	code := runBodyValidation(t, http.MethodPost, "/repos/o/r/commits", "application/json", body)
	require.Equal(t, http.StatusOK, code)
}

// Test_RequestBody_BranchesEndpoint_Valid POST /branches 라우팅
func Test_RequestBody_BranchesEndpoint_Valid(t *testing.T) {
	body := `{"branch_name":"feature/x"}`
	code := runBodyValidation(t, http.MethodPost, "/repos/o/r/branches", "application/json", body)
	require.Equal(t, http.StatusOK, code)
}

// Test_RequestBody_MergeEndpoint_Invalid POST /merge 잘못된 body
func Test_RequestBody_MergeEndpoint_Invalid(t *testing.T) {
	body := `{"source_branch":"feat"}`
	code := runBodyValidation(t, http.MethodPost, "/repos/o/r/merge", "application/json", body)
	require.Equal(t, http.StatusBadRequest, code)
}

// Test_RequestBody_UnmatchedEndpoint_Passes 매칭 안되는 엔드포인트는 body 검증 생략
func Test_RequestBody_UnmatchedEndpoint_Passes(t *testing.T) {
	vf := validationFrameworkForTest(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(vf.ValidateRequestMiddleware())
	r.POST("/repos/:owner/:repo/unknown", func(c *gin.Context) { c.Status(http.StatusOK) })

	body := `{"anything":"goes"}`
	req := httptest.NewRequest(http.MethodPost, "/repos/o/r/unknown", nil)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))
	req.Body = io.NopCloser(strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}

// --- 커스텀 validator 함수 직접 호출용: struct 검증 경유로 git_ref 분기 확인 ---

// Test_GitRef_Validator_Branches git_ref 커스텀 validator 분기 (start_point)
func Test_GitRef_Validator_Branches(t *testing.T) {
	vf := validationFrameworkForTest(t)

	// commit hash 형태 start_point
	require.NoError(t, vf.validateBranchOperationRequest([]byte(`{"branch_name":"x","start_point":"a1b2c3d"}`)))
	// 잘못된 ref (공백)
	require.Error(t, vf.validateBranchOperationRequest([]byte(`{"branch_name":"x","start_point":"bad ref"}`)))
}
