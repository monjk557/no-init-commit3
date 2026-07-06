package diffs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/collabops/git-executor/internal/api/typed"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// HandleCompareChanged 는 base 기준 old_head / new_head 두 CR diff 가 실제 파일
// 내용상 다른지(content_changed) 판정합니다 (Issue-5254).
//
// 리뷰 승인 content-aware stale (api-service Issue-5056 / CR-7028) 의 신호원 —
// push 로 source head 가 옮겨가도 diff(base..old) 와 diff(base..new) 가 같으면
// (trivial rebase / no-code-change) content_changed=false → api-service 가 승인을
// dismiss 하지 않고 carry 한다. commit sha / 메시지 등 메타데이터는 무시하고
// 파일 변경 내용만 본다.
func (h *DiffHandler) HandleCompareChanged(c *gin.Context) (CompareChangedResponse, error) {
	ctx := c.Request.Context()

	params, err := h.validateAndExtractParams(c)
	if err != nil {
		h.logger.Error("compare-changed: invalid params", zap.Error(err))
		return CompareChangedResponse{}, typed.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	var req CompareChangedRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return CompareChangedResponse{}, typed.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		h.logger.Error("compare-changed: invalid repo path", zap.Error(err))
		return CompareChangedResponse{}, typed.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	// 같은 sha 면 diff 계산 없이 변경 없음 (fast path).
	if req.OldHeadSHA == req.NewHeadSHA {
		return CompareChangedResponse{
			Success: true, BaseSHA: req.BaseSHA,
			OldHeadSHA: req.OldHeadSHA, NewHeadSHA: req.NewHeadSHA,
			ContentChanged: false,
		}, nil
	}

	changed, err := compareDiffChanged(ctx, repoPath, req.BaseSHA, req.OldHeadSHA, req.NewHeadSHA)
	if err != nil {
		h.logger.Error("compare-changed: compute failed", zap.Error(err))
		return CompareChangedResponse{}, typed.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return CompareChangedResponse{
		Success: true, BaseSHA: req.BaseSHA,
		OldHeadSHA: req.OldHeadSHA, NewHeadSHA: req.NewHeadSHA,
		ContentChanged: changed,
	}, nil
}

// compareDiffChanged 는 diff(base..oldHead) 와 diff(base..newHead) 의 content
// signature 를 비교해 다른지 반환합니다.
func compareDiffChanged(ctx context.Context, repoPath, base, oldHead, newHead string) (bool, error) {
	oldDiff, err := nativeCalculateCommitDiff(ctx, repoPath, base, oldHead, 0)
	if err != nil {
		return false, fmt.Errorf("old diff (%s..%s): %w", base, oldHead, err)
	}
	newDiff, err := nativeCalculateCommitDiff(ctx, repoPath, base, newHead, 0)
	if err != nil {
		return false, fmt.Errorf("new diff (%s..%s): %w", base, newHead, err)
	}
	return diffContentSignature(oldDiff) != diffContentSignature(newDiff), nil
}

// diffContentSignature 는 diff 의 파일 변경 내용을 파일경로 정렬 후 canonical 하게
// 직렬화하여 sha256 hex 로 반환합니다. 두 signature 가 같으면 파일/라인 변경 내용이
// 동일합니다 (commit sha / hunk 헤더 line 번호 등 volatile 값은 반영하지 않음 —
// LineChange.Type + Text 만 사용).
func diffContentSignature(d *DiffResponse) string {
	if d == nil {
		return ""
	}
	files := make([]FileDiff, len(d.Files))
	copy(files, d.Files)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	var b strings.Builder
	for _, f := range files {
		fmt.Fprintf(&b, "F\x1e%s\x1f%s\x1f%s\x1f%t\n", f.Path, f.Status, f.OldPath, f.IsBinary)
		for _, ch := range f.Changes {
			// HUNK 라인의 Text 는 "@@ -n,m +n,m @@" 로 line 번호가 들어가 volatile —
			// content 비교엔 제외하고 ADD/DELETE/CONTEXT 의 실제 텍스트만 본다.
			if ch.Type == LineChangeTypeHunk {
				continue
			}
			fmt.Fprintf(&b, "L\x1e%s\x1f%s\n", ch.Type, ch.Text)
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
