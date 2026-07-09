// classify.go — ReviewComment line-anchor staleness classification.
//
// Review Dismiss 2.0 (Phase B, simplified) — §14.
// 3-state: active / stale / orphaned. SHIFTED/REVERTED는 active로 통합.
//
// 알고리즘 (Issue-2547 후 — content-aware via per-anchor CommitSHA):
//  1. 각 anchor 의 "effective base SHA" 결정: anchor.CommitSHA 가 있으면 그것, 없으면 request.BaseSHA
//  2. unique effective base 별로 한 번씩 diff 계산 (다중 commit_sha 그룹화 — N+1 회피)
//  3. anchor 의 path 를 그 diff 의 old-path 기준으로 찾음
//  4. 없으면(파일 변경 없음) → active
//  5. status=deleted or is_binary → orphaned
//  6. renamed → new_path 전달
//  7. hunk의 old-range가 anchor range와 겹치면 → stale, 아니면 active
//
// CommitSHA 가 비어있을 때는 legacy 시맨틱 (request.BaseSHA 단일 사용) 유지.
package diffs

import (
	"context"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// HandleClassifyAnchors는 POST /repos/{owner}/{repo}/diff/classify-anchors를 처리합니다.
//
// ReviewComment anchor의 head sha 기준 staleness를 배치로 계산합니다.
func (h *DiffHandler) HandleClassifyAnchors(c *gin.Context) (ClassifyAnchorsResponse, error) {
	ctx := c.Request.Context()

	params, err := h.validateAndExtractParams(c)
	if err != nil {
		h.logger.Error("classify-anchors: invalid params", zap.Error(err))
		return ClassifyAnchorsResponse{}, api.NewCoded(api.ErrCodeInvalidInput, err.Error())
	}

	var req ClassifyAnchorsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return ClassifyAnchorsResponse{}, api.NewCoded(api.ErrCodeInvalidInput, err.Error())
	}
	if len(req.Anchors) == 0 {
		return ClassifyAnchorsResponse{
			Success: true,
			BaseSHA: req.BaseSHA,
			HeadSHA: req.HeadSHA,
			Results: []AnchorResult{},
		}, nil
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		h.logger.Error("classify-anchors: invalid repo path", zap.Error(err))
		return ClassifyAnchorsResponse{}, api.NewCoded(api.ErrCodeInvalidInput, err.Error())
	}

	results, err := classifyAnchors(ctx, repoPath, req.BaseSHA, req.HeadSHA, req.Anchors)
	if err != nil {
		h.logger.Error("classify-anchors: compute failed", zap.Error(err))
		return ClassifyAnchorsResponse{}, api.NewCoded(api.ErrCodeInternalError, err.Error())
	}

	return ClassifyAnchorsResponse{
		Success: true,
		BaseSHA: req.BaseSHA,
		HeadSHA: req.HeadSHA,
		Results: results,
	}, nil
}

// effectiveBaseSHA 는 anchor 의 분류 기준 base SHA 를 반환합니다.
// anchor 가 자체 CommitSHA 를 가지면 그것을, 없으면 request 의 globalBase 로 fallback.
func effectiveBaseSHA(a AnchorRequest, globalBase string) string {
	if a.CommitSHA != "" {
		return a.CommitSHA
	}
	return globalBase
}

// buildOldPathIndex 는 DiffResponse 의 파일 목록을 old-path 기준 lookup map 으로
// 변환합니다.
//
//   - renamed: OldPath → fileDiff (Path 는 새 경로)
//   - deleted: Path → fileDiff (OldPath 가 비어있을 수 있음)
//   - modified: Path → fileDiff
//   - added: anchor 의 path 와 매칭될 수 없으므로 색인 제외
//     (added 파일에 anchor 가 있다는 것은 그 파일이 anchor 생성 시점에 이미 존재했다는
//     뜻이고, 그 시점은 anchor.CommitSHA — added 라는 결과는 base 가 잘못 지정됐을 때
//     발생. 이 경우 classifyOneAnchor 가 "변경 없음"으로 보고 active 처리하는 것이
//     legacy 동작이며 보존한다.)
func buildOldPathIndex(diff *DiffResponse) map[string]*FileDiff {
	index := make(map[string]*FileDiff, len(diff.Files))
	for i := range diff.Files {
		fd := &diff.Files[i]
		key := fd.Path
		if fd.Status == FileDiffStatusRenamed && fd.OldPath != "" {
			key = fd.OldPath
		}
		if fd.Status == FileDiffStatusAdded {
			continue
		}
		index[key] = fd
	}
	return index
}

// classifyAnchors 는 repo path + global base/head SHA + anchor 목록으로 분류 결과를
// 반환합니다.
//
// Issue-2547: 각 anchor 의 effective base (anchor.CommitSHA, 또는 fallback 으로
// globalBaseSHA) 별로 grouping 후 한 unique base 당 한 번씩 diff 를 계산합니다.
// 동일 commit_sha 를 가진 anchor 가 다수일 때 N+1 diff 호출을 회피합니다.
//
// 그룹 단위 base 가 unreachable 이면 그 그룹의 모든 anchor 만 orphaned (다른 그룹은
// 영향 없음). 결과 순서는 입력 anchors 순서를 보존합니다.
//
// 순수 함수 형태로 추출해 유닛 테스트가 용이하도록 만들었습니다.
func classifyAnchors(
	ctx context.Context,
	repoPath string,
	baseSHA string,
	headSHA string,
	anchors []AnchorRequest,
) ([]AnchorResult, error) {
	// unique effective base SHA 수집 — 한 번씩만 diff 계산하면 됨.
	uniqueBases := make(map[string]struct{}, len(anchors))
	for _, a := range anchors {
		uniqueBases[effectiveBaseSHA(a, baseSHA)] = struct{}{}
	}

	// base → byOldPath index (nil 이면 base==head sentinel).
	indexByBase := make(map[string]map[string]*FileDiff, len(uniqueBases))
	// base → diff 계산 실패 여부 (그룹 전체 orphaned 처리).
	failuresByBase := make(map[string]bool, len(uniqueBases))

	for base := range uniqueBases {
		if base == headSHA {
			// base == head: 변경 없음 — sentinel nil 로 표시, classify 시 active 분기.
			indexByBase[base] = nil
			continue
		}
		diff, err := nativeCalculateCommitDiff(ctx, repoPath, base, headSHA, 0)
		if err != nil {
			failuresByBase[base] = true
			continue
		}
		indexByBase[base] = buildOldPathIndex(diff)
	}

	// 입력 순서 보존 — anchor 순서대로 분류.
	results := make([]AnchorResult, 0, len(anchors))
	for _, a := range anchors {
		base := effectiveBaseSHA(a, baseSHA)
		if failuresByBase[base] {
			results = append(results, AnchorResult{ID: a.ID, Status: AnchorStatusOrphaned})
			continue
		}
		byOldPath, ok := indexByBase[base]
		if !ok {
			// 정상 흐름에서 발생하지 않음 — 방어적으로 orphaned.
			results = append(results, AnchorResult{ID: a.ID, Status: AnchorStatusOrphaned})
			continue
		}
		if byOldPath == nil {
			// base == head 그룹: 변경 없음 → new == old.
			results = append(results, AnchorResult{
				ID: a.ID, Status: AnchorStatusActive,
				NewStartLine: intPtr(a.StartLine), NewEndLine: intPtr(a.EndLine),
			})
			continue
		}
		results = append(results, classifyOneAnchor(byOldPath, a))
	}
	return results, nil
}

// classifyOneAnchor는 단일 anchor를 분류합니다. 순수 함수로 추출해 테스트 용이.
func classifyOneAnchor(byOldPath map[string]*FileDiff, a AnchorRequest) AnchorResult {
	fd, ok := byOldPath[a.Path]
	if !ok {
		// 파일에 아무 변경 없음 → active. 라인 밀림 없음 → new == old.
		return AnchorResult{
			ID: a.ID, Status: AnchorStatusActive,
			NewStartLine: intPtr(a.StartLine), NewEndLine: intPtr(a.EndLine),
		}
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

	// active — 앵커가 어떤 hunk 와도 안 겹침. 위쪽 hunk 들의 삽입/삭제 offset 만큼
	// 밀린 새 라인을 계산해 재배치용으로 반환한다.
	offset := lineOffsetAbove(fd.Hunks, a.StartLine)
	return AnchorResult{
		ID: a.ID, Status: AnchorStatusActive, NewPath: newPath,
		NewStartLine: intPtr(a.StartLine + offset),
		NewEndLine:   intPtr(a.EndLine + offset),
	}
}

// lineOffsetAbove 는 old-side 라인 anchorStart 보다 완전히 위에 있는 hunk 들의
// 순 라인 증감(count_new - count_old)을 합산한다. anchor 가 어떤 hunk 와도 겹치지
// 않는(active) 전제에서 old→new 라인 매핑에 쓰인다.
//
//   - CountOld > 0: hunk 의 old-side 끝(StartOld+CountOld-1) 이 anchorStart 보다
//     작으면 위에 있는 것 → offset += CountNew - CountOld.
//   - CountOld == 0 (순수 삽입): old 라인이 없으므로 삽입 지점 StartOld 가
//     anchorStart 보다 작으면 위에 있는 것 → offset += CountNew.
func lineOffsetAbove(hunks []Hunk, anchorStart int) int {
	offset := 0
	for _, h := range hunks {
		if h.CountOld == 0 {
			if h.StartOld < anchorStart {
				offset += h.CountNew
			}
			continue
		}
		if h.StartOld+h.CountOld-1 < anchorStart {
			offset += h.CountNew - h.CountOld
		}
	}
	return offset
}
