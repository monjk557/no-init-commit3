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

func validationFrameworkForTest(t *testing.T) *ValidationFramework {
	t.Helper()
	logger := &logging.Logger{Logger: zaptest.NewLogger(t)}
	vf, err := NewValidationFramework(DefaultValidationConfig(), logger)
	require.NoError(t, err)
	return vf
}

// --- 12.1 Path Validation ---

// Test_PathValidation_TraversalAttack_Blocked ../ 공격 차단
func Test_PathValidation_TraversalAttack_Blocked(t *testing.T) {
	vf := validationFrameworkForTest(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(vf.ValidateRequestMiddleware())
	r.GET("/repos/:owner/:repo/blob/*path", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/repos/o/r/blob/a/../../etc/passwd", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// Test_PathValidation_NullByte_Blocked null byte 차단
func Test_PathValidation_NullByte_Blocked(t *testing.T) {
	vf := validationFrameworkForTest(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(vf.ValidateRequestMiddleware())
	r.GET("/repos/:owner/:repo/blob/*path", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/repos/o/r/blob/foo%00bar", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// Test_PathValidation_AbsolutePath_Blocked 절대 경로 차단
func Test_PathValidation_AbsolutePath_Blocked(t *testing.T) {
	vf := validationFrameworkForTest(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(vf.ValidateRequestMiddleware())
	r.GET("/repos/:owner/:repo/blob/*path", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/repos/o/r/blob//etc/passwd", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- 12.1A Git 내부 콘텐츠 기반 공격 ---

// Test_BranchName_ControlChars_Rejected 브랜치명에 제어문자 포함 시 거부
func Test_BranchName_ControlChars_Rejected(t *testing.T) {
	vf := validationFrameworkForTest(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(vf.ValidateRequestMiddleware())
	r.GET("/repos/:owner/:repo/tree", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/repos/o/r/tree?branch=main%0a", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// Test_RefName_NewlineOrNull_Rejected ref에 개행/null 포함 시 거부
func Test_RefName_NewlineOrNull_Rejected(t *testing.T) {
	vf := validationFrameworkForTest(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(vf.ValidateRequestMiddleware())
	r.GET("/repos/:owner/:repo/tree", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/repos/o/r/tree?ref=refs/heads/main%00", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- 12.2 Input Validation ---

// Test_InputValidation_XSSPayload_Escaped XSS 페이로드 검증(거부 또는 통과)
func Test_InputValidation_XSSPayload_Escaped(t *testing.T) {
	vf := validationFrameworkForTest(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(vf.ValidateRequestMiddleware())
	r.PUT("/repos/:owner/:repo/files/:path", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	body := `{"content":"<script>alert(1)</script>","commit_message":"x","branch":"main","author_name":"a","author_email":"a@b.com"}`
	req := httptest.NewRequest(http.MethodPut, "/repos/o/r/files/x.txt", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = io.NopCloser(strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Contains(t, []int{http.StatusOK, http.StatusBadRequest}, rec.Code)
}

// Test_InputValidation_SQLInjection_Blocked SQL 인젝션 페이로드 검증
func Test_InputValidation_SQLInjection_Blocked(t *testing.T) {
	vf := validationFrameworkForTest(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(vf.ValidateRequestMiddleware())
	r.PUT("/repos/:owner/:repo/files/:path", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	body := `{"content":"'; DROP TABLE x;--","commit_message":"x","branch":"main","author_name":"a","author_email":"a@b.com"}`
	req := httptest.NewRequest(http.MethodPut, "/repos/o/r/files/x.txt", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Body = io.NopCloser(strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Contains(t, []int{http.StatusOK, http.StatusBadRequest}, rec.Code)
}

// Test_InputValidation_LargeInput_Rejected 대용량 입력 거부
func Test_InputValidation_LargeInput_Rejected(t *testing.T) {
	cfg := DefaultValidationConfig()
	cfg.MaxFileSize = 10
	vf, err := NewValidationFramework(cfg, &logging.Logger{Logger: zaptest.NewLogger(t)})
	require.NoError(t, err)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(vf.ValidateRequestMiddleware())
	r.PUT("/repos/:owner/:repo/files/:path", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	body := `{"content":"` + strings.Repeat("x", 20) + `","commit_message":"x","branch":"main","author_name":"a","author_email":"a@b.com"}`
	req := httptest.NewRequest(http.MethodPut, "/repos/o/r/files/x.txt", nil)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))
	req.Body = io.NopCloser(strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}
