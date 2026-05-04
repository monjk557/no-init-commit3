// revert 도메인의 typed 라우트 등록.
//
// server.go (runtime) 와 specs/spec_routes.go (spec 생성) 가 모두 이 함수를
// 호출하여 단일 source of truth 보장.
package revert

import (
	"errors"
	"net/http"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/collabops/git-executor/internal/api/typed"
)

const tag = "Revert"

// RegisterRoutes 는 revert 도메인의 모든 endpoint 를 등록한다. h 가 nil 이면
// spec-only 모드 (등록만, 호출 없음).
func RegisterRoutes(g gin.IRouter, h *RevertHandler) {
	var handleRevert func(*gin.Context) (RevertResponse, error)
	if h != nil {
		handleRevert = h.HandleRevert
	}

	// POST /revert: lenient bind + 다중 status 매핑.
	// performRevert 의 BranchExistsError → 409 (extra fields),
	// substring "conflicts" → 409 REVERT_CONFLICT,
	// substring "unknown revision"/"failed to read commit" → 404 COMMIT_NOT_FOUND,
	// substring "invalid parent_number" → 400 INVALID_PARENT.
	typed.RegisterNoBody(g, "POST", "/revert", tag,
		handleRevert,
		typed.WithSummary("HandleRevert"),
		typed.WithRequestBody(reflect.TypeOf(RevertRequest{})),
		typed.WithErrorMapper(revertErrorMapper),
	)
}

// revertErrorMapper 는 service 가 반환한 에러를 status + 응답으로 매핑한다.
// *typed.HTTPError 가 우선 — handler 본문에서 typed.NewHTTPError 로 wrap 한
// 단순 4xx 케이스는 {success, error} 형태로 보낸다. 그 외에는 BranchExistsError
// / substring 매칭으로 legacy switch 와 동일한 응답 (extra `code` 필드 포함)
// 을 만든다.
func revertErrorMapper(err error) (int, any) {
	var he *typed.HTTPError
	if errors.As(err, &he) {
		return he.Status, gin.H{"success": false, "error": he.Message}
	}

	var branchExistsErr *BranchExistsError
	if errors.As(err, &branchExistsErr) {
		return http.StatusConflict, gin.H{
			"success":       false,
			"error":         branchExistsErr.Error(),
			"code":          "BRANCH_ALREADY_EXISTS",
			"revert_branch": branchExistsErr.BranchName,
			"revert_commit": branchExistsErr.TipCommit,
		}
	}

	errMsg := err.Error()
	switch {
	case strings.Contains(errMsg, "conflicts"):
		return http.StatusConflict, gin.H{
			"success": false,
			"error":   errMsg,
			"code":    "REVERT_CONFLICT",
		}
	case strings.Contains(errMsg, "unknown revision") || strings.Contains(errMsg, "failed to read commit"):
		return http.StatusNotFound, gin.H{
			"success": false,
			"error":   errMsg,
			"code":    "COMMIT_NOT_FOUND",
		}
	case strings.Contains(errMsg, "invalid parent_number"):
		return http.StatusBadRequest, gin.H{
			"success": false,
			"error":   errMsg,
			"code":    "INVALID_PARENT",
		}
	}
	return http.StatusInternalServerError, gin.H{"success": false, "error": errMsg}
}
