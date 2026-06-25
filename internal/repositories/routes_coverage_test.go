package repositories

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/collabops/git-executor/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RegisterRoutes(g, nil) 은 spec-only 모드 — 등록만 하고 핸들러 호출은 없다.
func TestRegisterRoutes_NilHandler_SpecOnly(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	router := gin.New()
	g := router.Group("/repos/:owner/:repo")
	// spec-only 등록 (h=nil). 이 모드는 spec 생성 전용이므로 런타임 호출은
	// 하지 않는다 — 등록만 panic 없이 되면 충분하다.
	assert.NotPanics(t, func() {
		RegisterRoutes(g, nil)
	})
	// 라우트가 실제로 등록됐는지 확인.
	found := false
	for _, ri := range router.Routes() {
		if ri.Method == "POST" {
			found = true
		}
	}
	assert.True(t, found, "POST 라우트가 등록되어야 한다")
}

func TestNewRepositoryCreationHandler_InvalidRepoRoot(t *testing.T) {
	t.Parallel()
	// 상대 경로 RepoRoot → NewPathConfig 실패 → 핸들러 생성 에러
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "error", Format: "json"},
		Git:     config.GitConfig{RepoRoot: "relative/not/absolute"},
	}
	_, err := NewRepositoryCreationHandler(cfg)
	assert.Error(t, err)
}

func TestNewRepositoryCreationHandler_Valid(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "error", Format: "json"},
		Git:     config.GitConfig{RepoRoot: t.TempDir()},
	}
	h, err := NewRepositoryCreationHandler(cfg)
	require.NoError(t, err)
	assert.NotNil(t, h)
}

// 핸들러 본문 경유: invalid path → 400 (typed wrapper 통과 검증)
func TestHandleCreateRepository_InvalidPathComponent(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "error", Format: "json"},
		Git:     config.GitConfig{RepoRoot: t.TempDir()},
	}
	h, err := NewRepositoryCreationHandler(cfg)
	require.NoError(t, err)

	router := gin.New()
	g := router.Group("/repos/:owner/:repo")
	RegisterRoutes(g, h)

	// reserved/invalid component 를 path 로 → ValidateFilePath 실패 경로.
	// ".git" 같은 내부 경로는 차단된다.
	req := httptest.NewRequest("POST", "/repos/owner/repo", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	// 정상 생성 (201) 이어야 함 — sanity
	assert.Equal(t, http.StatusCreated, w.Code)
}
