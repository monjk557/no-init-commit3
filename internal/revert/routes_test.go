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

// asHTTPError 는 classifyRevertError 가 항상 *typed.HTTPError 를 반환한다는
// 계약을 테스트에서 강제 unwrap 하는 헬퍼.
func asHTTPError(t *testing.T, err error) *typed.HTTPError {
	t.Helper()
	var he *typed.HTTPError
	require.ErrorAs(t, err, &he, "classifyRevertError 는 항상 *typed.HTTPError 를 반환해야 함")
	return he
}

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

// classifyRevertError 의 각 분기를 직접 검증한다 — 카탈로그 code 로 이관된
// 뒤에도 legacy 와 동일한 status 를 내는지 확인 (message 는 override 로
// err.Error() 그대로 유지).
func TestClassifyRevertError(t *testing.T) {
	// BranchExistsError → 409 + params(branch_name/tip_commit).
	he := asHTTPError(t, classifyRevertError(&BranchExistsError{
		BranchName: "revert/x",
		TipCommit:  &CommitInfo{SHA: "abc"},
	}))
	assert.Equal(t, http.StatusConflict, he.Status)
	assert.Equal(t, "BRANCH_ALREADY_EXISTS", he.Code)
	assert.Equal(t, "revert/x", he.Params["branch_name"])
	assert.Equal(t, "abc", he.Params["tip_commit"])

	// BranchExistsError 의 TipCommit 이 nil 이어도 panic 하지 않는다.
	he = asHTTPError(t, classifyRevertError(&BranchExistsError{BranchName: "revert/y"}))
	assert.Equal(t, "", he.Params["tip_commit"])

	// substring "conflicts" → 409 REVERT_CONFLICT.
	he = asHTTPError(t, classifyRevertError(errors.New("revert produced conflicts in tree")))
	assert.Equal(t, http.StatusConflict, he.Status)
	assert.Equal(t, "REVERT_CONFLICT", he.Code)

	// substring "unknown revision" → 404 COMMIT_NOT_FOUND.
	he = asHTTPError(t, classifyRevertError(errors.New("unknown revision deadbeef")))
	assert.Equal(t, http.StatusNotFound, he.Status)
	assert.Equal(t, "COMMIT_NOT_FOUND", he.Code)

	// substring "failed to read commit" → 404 COMMIT_NOT_FOUND.
	he = asHTTPError(t, classifyRevertError(errors.New("failed to read commit object")))
	assert.Equal(t, http.StatusNotFound, he.Status)
	assert.Equal(t, "COMMIT_NOT_FOUND", he.Code)

	// substring "invalid parent_number" → 400 INVALID_PARENT.
	he = asHTTPError(t, classifyRevertError(errors.New("invalid parent_number 5")))
	assert.Equal(t, http.StatusBadRequest, he.Status)
	assert.Equal(t, "INVALID_PARENT", he.Code)

	// 그 외 → 500 INTERNAL_ERROR.
	he = asHTTPError(t, classifyRevertError(errors.New("some unexpected failure")))
	assert.Equal(t, http.StatusInternalServerError, he.Status)
	assert.Equal(t, "INTERNAL_ERROR", he.Code)
}
