package revert

import (
	"errors"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/collabops/git-executor/internal/api/typed"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
)

// RegisterRoutes 는 nil handler (spec-only) 와 실제 handler 양쪽에서
// panic 없이 동작해야 한다.
func TestRegisterRoutes_NilHandler(t *testing.T) {
	r := gin.New()
	assert.NotPanics(t, func() {
		RegisterRoutes(r, nil)
	})
	// POST /revert 가 등록되었는지 라우트 목록으로 확인.
	found := false
	for _, route := range r.Routes() {
		if route.Method == http.MethodPost && route.Path == "/revert" {
			found = true
		}
	}
	assert.True(t, found, "POST /revert should be registered")
}

func TestRegisterRoutes_RealHandler(t *testing.T) {
	pathConfig, err := repositories.NewPathConfig(t.TempDir())
	require.NoError(t, err)
	h := &RevertHandler{
		pathConfig: pathConfig,
		logger:     &logging.Logger{Logger: zap.NewNop()},
	}

	r := gin.New()
	assert.NotPanics(t, func() {
		RegisterRoutes(r, h)
	})
}

// revertErrorMapper 의 각 분기를 직접 검증한다.
func TestRevertErrorMapper(t *testing.T) {
	// typed.HTTPError → 그대로 status + {success,error}.
	status, body := revertErrorMapper(typed.NewHTTPError(http.StatusBadRequest, "bad"))
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, false, body.(gin.H)["success"])

	// BranchExistsError → 409 + extra 필드.
	status, body = revertErrorMapper(&BranchExistsError{
		BranchName: "revert/x",
		TipCommit:  &CommitInfo{SHA: "abc"},
	})
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "BRANCH_ALREADY_EXISTS", body.(gin.H)["code"])
	assert.Equal(t, "revert/x", body.(gin.H)["revert_branch"])

	// substring "conflicts" → 409 REVERT_CONFLICT.
	status, body = revertErrorMapper(errors.New("revert produced conflicts in tree"))
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "REVERT_CONFLICT", body.(gin.H)["code"])

	// substring "unknown revision" → 404 COMMIT_NOT_FOUND.
	status, body = revertErrorMapper(errors.New("unknown revision deadbeef"))
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, "COMMIT_NOT_FOUND", body.(gin.H)["code"])

	// substring "failed to read commit" → 404 COMMIT_NOT_FOUND.
	status, body = revertErrorMapper(errors.New("failed to read commit object"))
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, "COMMIT_NOT_FOUND", body.(gin.H)["code"])

	// substring "invalid parent_number" → 400 INVALID_PARENT.
	status, body = revertErrorMapper(errors.New("invalid parent_number 5"))
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "INVALID_PARENT", body.(gin.H)["code"])

	// 그 외 → 500.
	status, body = revertErrorMapper(errors.New("some unexpected failure"))
	assert.Equal(t, http.StatusInternalServerError, status)
	assert.Equal(t, false, body.(gin.H)["success"])
}
