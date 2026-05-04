// classify.go — ReviewComment line-anchor staleness classification.
//
// Review Dismiss 2.0 (Phase B, simplified) — §14.
// 3-state: active / stale / orphaned. SHIFTED/REVERTED는 active로 통합.
// 알고리즘:
//  1. head sha에 base sha로부터 diff 계산
//  2. anchor의 path를 old-path 기준으로 diff에서 찾음
//  3. 없으면(파일 변경 없음) → active
//  4. status=deleted or is_binary → orphaned
//  5. renamed → new_path 전달
//  6. hunk의 old-range가 anchor range와 겹치면 → stale, 아니면 active
package diffs

import (
	"context"
	"net/http"

	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// HandleClassifyAnchors는 POST /repos/:owner/:repo/diff/classify-anchors를 처리합니다.
//
// ReviewComment anchor의 head sha 기준 staleness를 배치로 계산합니다.
// UpdateChangeRequestService가 source_commit_sha 변경 시 이 엔드포인트를 호출해
// 기존 comment의 line_anchor_status_cache를 갱신합니다.
func (h *DiffHandler) HandleClassifyAnchors(c *gin.Context) {
	ctx := c.Request.Context()

	params, err := h.validateAndExtractParams(c)
	if err != nil {
		h.logger.Error("classify-anchors: invalid params", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	var req ClassifyAnchorsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}
	if len(req.Anchors) == 0 {
		c.JSON(http.StatusOK, ClassifyAnchorsResponse{
			Success: true,
			BaseSHA: req.BaseSHA,
			HeadSHA: req.HeadSHA,
			Results: []AnchorResult{},
		})
		return
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		h.logger.Error("classify-anchors: invalid repo path", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}

	results, err := classifyAnchors(ctx, repoPath, req.BaseSHA, req.HeadSHA, req.Anchors)
	if err != nil {
		h.logger.Error("classify-anchors: compute failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, ClassifyAnchorsResponse{
		Success: true,
		BaseSHA: req.BaseSHA,
		HeadSHA: req.HeadSHA,
		Results: results,
	})
}

// classifyAnchors는 repo path + base/head SHA + anchor 목록으로 분류 결과를 반환합니다.
//
// 순수 함수 형태로 추출해 유닛 테스트가 용이하도록 만들었습니다.
// Base가 unreachable이면 모든 anchor가 orphaned (전체 diff 실패).
func classifyAnchors(
	ctx context.Context,
	repoPath string,
	baseSHA string,
	headSHA string,
	anchors []AnchorRequest,
) ([]AnchorResult, error) {
	results := make([]AnchorResult, 0, len(anchors))

	// base == head: 아무 변경도 없음 → 전부 active
	if baseSHA == headSHA {
		for _, a := range anchors {
			results = append(results, AnchorResult{ID: a.ID, Status: AnchorStatusActive})
		}
		return results, nil
	}

	diff, err := nativeCalculateCommitDiff(ctx, repoPath, baseSHA, headSHA, 0)
	if err != nil {
		// base unreachable 등 diff 계산 자체 실패: 모든 anchor orphaned로 간주
		for _, a := range anchors {
			results = append(results, AnchorResult{ID: a.ID, Status: AnchorStatusOrphaned})
		}
		return results, nil
	}

	// fileDiff를 old-path (변경 전 경로) 기준으로 인덱싱.
	// - renamed: OldPath → fileDiff (Path가 새 경로)
	// - deleted: Path → fileDiff (OldPath는 비어있을 수 있음)
	// - modified: Path → fileDiff
	// - added: old-path가 없음 (anchor가 매칭될 일 없음)
	byOldPath := make(map[string]*FileDiff, len(diff.Files))
	for i := range diff.Files {
		fd := &diff.Files[i]
		key := fd.Path
		if fd.Status == FileDiffStatusRenamed && fd.OldPath != "" {
			key = fd.OldPath
		}
		if fd.Status == FileDiffStatusAdded {
			continue // anchor는 base 기준이므로 added 파일은 매칭 대상 아님
		}
		byOldPath[key] = fd
	}

	for _, a := range anchors {
		results = append(results, classifyOneAnchor(byOldPath, a))
	}
	return results, nil
}

// classifyOneAnchor는 단일 anchor를 분류합니다. 순수 함수로 추출해 테스트 용이.
func classifyOneAnchor(byOldPath map[string]*FileDiff, a AnchorRequest) AnchorResult {
	fd, ok := byOldPath[a.Path]
	if !ok {
		// 파일에 아무 변경 없음 → active
		return AnchorResult{ID: a.ID, Status: AnchorStatusActive}
	}

	if fd.Status == FileDiffStatusDeleted {
		return AnchorResult{ID: a.ID, Status: AnchorStatusOrphaned}
	}
	if fd.IsBinary {
		return AnchorResult{ID: a.ID, Status: AnchorStatusOrphaned}
	}

	// rename 시 new_path는 결과에 포함해 프론트가 새 위치로 표시.
	var newPath string
	if fd.Status == FileDiffStatusRenamed {
		newPath = fd.Path
	}

	// hunk overlap 검사 (old-side 라인 기준)
	// hunk 범위: [StartOld, StartOld + CountOld - 1]
	// anchor 범위: [StartLine, EndLine]
	// overlap: !(hunkEnd < anchorStart || hunkStart > anchorEnd)
	for _, h := range fd.Hunks {
		hunkStart := h.StartOld
		// CountOld == 0인 경우 "pure insertion at this position" — old 라인 번호가 실제로 존재하지 않음.
		// 그 경우 hunk는 old-file 상 anchor 라인과 겹치지 않으므로 overlap이 아닌 것으로 취급.
		if h.CountOld == 0 {
			continue
		}
		hunkEnd := hunkStart + h.CountOld - 1
		if !(hunkEnd < a.StartLine || hunkStart > a.EndLine) {
			return AnchorResult{ID: a.ID, Status: AnchorStatusStale, NewPath: newPath}
		}
	}

	return AnchorResult{ID: a.ID, Status: AnchorStatusActive, NewPath: newPath}
}
