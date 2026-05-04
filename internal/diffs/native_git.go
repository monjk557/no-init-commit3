package diffs

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/gitutil"
)

// ---------------------------------------------------------------------------
// Native git: diff 계산
// ---------------------------------------------------------------------------
//
// go-git의 object.DiffTree + change.Patch()를 native git exec으로 대체합니다.
// C 최적화된 git diff 엔진이 직접 unified diff를 생성하므로
// go-git 대비 수~수십 배 빠릅니다 (특히 대규모 diff에서).
// ---------------------------------------------------------------------------

const (
	nativeGitTimeout    = 10 * time.Second
	nativeGitRefTimeout = 3 * time.Second
	// Git's well-known empty-tree object SHA-1. Verify with: git hash-object -t tree --stdin </dev/null
	emptyTreeHash = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
)

// nativeFileMeta는 git diff에서 추출한 파일별 메타데이터입니다.
type nativeFileMeta struct {
	Path      string
	OldPath   string // 리네임 시
	Status    FileDiffStatus
	Additions int
	Deletions int
	IsBinary  bool
}

// ---------------------------------------------------------------------------
// Top-level diff calculation functions (replacing go-git based methods)
// ---------------------------------------------------------------------------

// nativeCalculateCommitDiff는 두 커밋 간의 diff를 native git으로 계산합니다.
func nativeCalculateCommitDiff(ctx context.Context, repoPath, base, head string, contextLines int) (*DiffResponse, error) {
	baseHash, err := nativeResolveRef(ctx, repoPath, base)
	if err != nil {
		return nil, fmt.Errorf("failed to get base commit %s: %w", base, err)
	}
	headHash, err := nativeResolveRef(ctx, repoPath, head)
	if err != nil {
		return nil, fmt.Errorf("failed to get head commit %s: %w", head, err)
	}

	return nativeBuildDiffResponse(ctx, repoPath, baseHash, headHash, base, head, contextLines)
}

// nativeCalculateSingleCommitDiff는 단일 커밋의 diff를 native git으로 계산합니다.
func nativeCalculateSingleCommitDiff(ctx context.Context, repoPath, sha string, contextLines int) (*SingleCommitDiffResponse, error) {
	// 커밋 메타데이터 가져오기
	commitMeta, err := nativeGetCommitMetadata(ctx, repoPath, sha)
	if err != nil {
		return nil, fmt.Errorf("failed to get commit %s: %w", sha, err)
	}

	// 부모 커밋 확인
	var baseHash string
	if len(commitMeta.Parents) > 0 {
		baseHash = commitMeta.Parents[0]
	} else {
		// 초기 커밋: 빈 트리와 비교
		baseHash = emptyTreeHash
	}

	diffResp, err := nativeBuildDiffResponse(ctx, repoPath, baseHash, sha, baseHash, sha, contextLines)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate commit diff: %w", err)
	}

	// base/head 설정
	if len(commitMeta.Parents) > 0 {
		diffResp.Base = commitMeta.Parents[0]
	} else {
		diffResp.Base = "empty_tree"
	}
	diffResp.Head = sha

	return &SingleCommitDiffResponse{
		Success:      true,
		Commit:       commitMeta,
		DiffResponse: diffResp,
	}, nil
}

// nativeCalculateBranchDiff는 두 브랜치 간의 diff를 native git으로 계산합니다.
func nativeCalculateBranchDiff(ctx context.Context, repoPath, baseBranch, headBranch string, contextLines int) (*DiffResponse, error) {
	baseHash, err := nativeResolveRef(ctx, repoPath, baseBranch)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve base branch %s: %w", baseBranch, err)
	}
	headHash, err := nativeResolveRef(ctx, repoPath, headBranch)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve head branch %s: %w", headBranch, err)
	}

	// merge base 찾기
	mergeBase, err := nativeMergeBase(ctx, repoPath, baseHash, headHash)
	if err != nil {
		return nil, fmt.Errorf("failed to find merge base: %w", err)
	}

	// merge base와 head 간 diff 계산
	diffResp, err := nativeBuildDiffResponse(ctx, repoPath, mergeBase, headHash, baseBranch, headBranch, contextLines)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate branch diff: %w", err)
	}

	// ahead/behind 계산
	commitStats, err := nativeCalculateCommitCounts(ctx, repoPath, baseHash, headHash, mergeBase)
	if err != nil {
		fmt.Printf("Warning: failed to calculate commit counts: %v\n", err)
		commitStats = &CommitStats{Ahead: 0, Behind: 0}
	}

	// 날짜별 커밋 목록
	commitsByDate, err := nativeGetCommitsByDate(ctx, repoPath, mergeBase, headHash)
	if err != nil {
		fmt.Printf("Warning: failed to get commits by date: %v\n", err)
		commitsByDate = []CommitsByDate{}
	}

	diffResp.Commits = commitStats
	diffResp.CommitsByDate = commitsByDate
	diffResp.MergeBase = mergeBase
	diffResp.EffectiveBase = mergeBase
	diffResp.DiffMode = string(DiffModeMergeBase)

	return diffResp, nil
}

// fileDiffNotFoundError는 파일이 effective_base/head 양쪽 모두에 존재하지 않을 때 반환됩니다.
// 핸들러는 이 에러 타입을 보고 404를 응답해야 합니다.
type fileDiffNotFoundError struct {
	Path string
}

func (e *fileDiffNotFoundError) Error() string {
	return fmt.Sprintf("file not found in either base or head: %s", e.Path)
}

// IsFileDiffNotFound는 에러가 파일 부재로 인한 것인지 확인합니다.
func IsFileDiffNotFound(err error) bool {
	if err == nil {
		return false
	}
	_, ok := err.(*fileDiffNotFoundError)
	return ok
}

// nativeCalculateFileDiff는 두 참조 간의 특정 파일에 대한 diff를 native git으로 계산합니다.
//
// effectiveBase/headHash는 이미 해시로 해석된 값을 받습니다 (caller가 ResolvedRange로 미리 해석).
// 이 함수 자체는 mode를 모르고, 해시 기준 2-dot diff만 수행합니다 — mode 처리는 호출자 책임.
//
// 빈 status 처리 (정정된 시맨틱):
//   - 양쪽에 동일 blob → 200 응답 + 빈 hunks (load_status=loaded, additions=0, deletions=0)
//   - 양쪽에 모두 없음 → fileDiffNotFoundError (호출자가 404로 매핑)
func nativeCalculateFileDiff(ctx context.Context, repoPath, filePath, effectiveBase, headHash string, contextLines int) (*DiffResponse, error) {
	// 단일 파일 status 확인
	statusOut, err := nativeRunGitCmd(ctx, repoPath, nativeGitTimeout,
		"diff", "--name-status", "-M", effectiveBase+".."+headHash, "--", filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to get file status: %w", err)
	}

	statusLine := strings.TrimSpace(statusOut)
	if statusLine == "" {
		// 변경 status가 비었다고 해서 곧장 not-found가 아님 — base와 head 양쪽에 동일 blob으로
		// 존재하는 정상 케이스가 있다 (예: feature 브랜치의 파일이 main에도 같은 내용으로 들어간 상태).
		// 양쪽 ref에 파일이 존재하는지 직접 확인해서 분기.
		baseHas := nativeBlobExistsAtRef(ctx, repoPath, effectiveBase, filePath)
		headHas := nativeBlobExistsAtRef(ctx, repoPath, headHash, filePath)
		if !baseHas && !headHas {
			return nil, &fileDiffNotFoundError{Path: filePath}
		}

		// 한쪽에만 존재하는데 status가 비는 케이스는 git 동작상 있을 수 없지만,
		// 방어적으로 빈 diff 응답으로 처리 (additions/deletions 0).
		// 양쪽에 동일 blob → 변경 없음 응답.
		fileDiff := FileDiff{
			Path:       filePath,
			Status:     FileDiffStatusModified, // 변경 없음을 나타내는 안전한 default
			Additions:  0,
			Deletions:  0,
			IsBinary:   isBinaryFileByExtension(filePath),
			Hunks:      []Hunk{},
			LoadStatus: FileLoadStatusLoaded,
		}
		return &DiffResponse{
			Success: true,
			Files:   []FileDiff{fileDiff},
			Stats:   DiffStats{FilesChanged: 0, TotalAdditions: 0, TotalDeletions: 0},
		}, nil
	}

	// 파일 status 파싱
	status := parseFileStatus(statusLine)

	// 바이너리 체크
	isBinary := isBinaryFileByExtension(filePath)

	// numstat으로 stats 가져오기
	numstatOut, err := nativeRunGitCmd(ctx, repoPath, nativeGitTimeout,
		"diff", "--numstat", "-M", effectiveBase+".."+headHash, "--", filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to get file stats: %w", err)
	}

	var additions, deletions int
	numstatLine := strings.TrimSpace(numstatOut)
	if numstatLine != "" {
		parts := strings.Fields(numstatLine)
		if len(parts) >= 2 {
			if parts[0] == "-" {
				isBinary = true
			} else {
				additions, _ = strconv.Atoi(parts[0])
				deletions, _ = strconv.Atoi(parts[1])
			}
		}
	}

	fileDiff := FileDiff{
		Path:      filePath,
		Status:    status,
		Additions: additions,
		Deletions: deletions,
		IsBinary:  isBinary,
		Hunks:     []Hunk{},
	}

	// LoadStatus 설정
	if isBinary {
		fileDiff.LoadStatus = FileLoadStatusBinary
	} else if contextLines <= 0 {
		fileDiff.LoadStatus = FileLoadStatusCollapsed
	} else {
		changedLines := additions + deletions
		if changedLines > MaxChangedLines {
			fileDiff.LoadStatus = FileLoadStatusTooLarge
		} else {
			patchText, err := nativeGetFilePatch(ctx, repoPath, effectiveBase, headHash, filePath, contextLines)
			if err != nil {
				fileDiff.LoadStatus = FileLoadStatusCollapsed
			} else if patchText == "" {
				fileDiff.LoadStatus = FileLoadStatusLoaded
			} else {
				lineChanges, hunks, parseErr := GenerateLineChangesFromPatch(patchText, contextLines)
				if parseErr != nil {
					fileDiff.LoadStatus = FileLoadStatusCollapsed
				} else if len(lineChanges) > MaxFileLines {
					fileDiff.LoadStatus = FileLoadStatusTooLarge
				} else {
					fileDiff.Changes = lineChanges
					fileDiff.Hunks = hunks
					fileDiff.LoadStatus = FileLoadStatusLoaded
				}
			}
		}
	}

	stats := DiffStats{
		FilesChanged:   1,
		TotalAdditions: additions,
		TotalDeletions: deletions,
	}

	return &DiffResponse{
		Success: true,
		Files:   []FileDiff{fileDiff},
		Stats:   stats,
	}, nil
}

// nativeGeneratePatch는 native git으로 patch를 생성합니다.
func nativeGeneratePatch(ctx context.Context, repoPath, base, head string) (string, error) {
	baseHash, err := nativeResolveRef(ctx, repoPath, base)
	if err != nil {
		return "", fmt.Errorf("failed to resolve base reference %s: %w", base, err)
	}
	headHash, err := nativeResolveRef(ctx, repoPath, head)
	if err != nil {
		return "", fmt.Errorf("failed to resolve head reference %s: %w", head, err)
	}

	return nativeRunGitCmd(ctx, repoPath, nativeGitTimeout,
		"diff", "-M", baseHash+".."+headHash)
}

// ---------------------------------------------------------------------------
// Core diff building
// ---------------------------------------------------------------------------

// nativeBuildDiffResponse는 두 커밋 간의 diff를 native git으로 계산하여 DiffResponse를 생성합니다.
func nativeBuildDiffResponse(ctx context.Context, repoPath, baseHash, headHash, baseName, headName string, contextLines int) (*DiffResponse, error) {
	// 파일 목록 + 메타데이터 가져오기
	fileMetas, err := nativeGetDiffFileList(ctx, repoPath, baseHash, headHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get diff file list: %w", err)
	}

	// FileDiff 객체 빌드
	fileDiffs := make([]FileDiff, 0, len(fileMetas))
	for _, meta := range fileMetas {
		fileDiff := FileDiff{
			Path:      meta.Path,
			Status:    meta.Status,
			Additions: meta.Additions,
			Deletions: meta.Deletions,
			IsBinary:  meta.IsBinary,
			Hunks:     []Hunk{},
		}
		if meta.OldPath != "" {
			fileDiff.OldPath = meta.OldPath
		}
		fileDiffs = append(fileDiffs, fileDiff)
	}

	// 상위 N개 비-바이너리 파일에 대해 상세 diff 가져오기
	filesWithChangesCount := 0
	for i := range fileDiffs {
		if filesWithChangesCount >= MaxFilesWithChanges {
			break
		}
		if fileDiffs[i].IsBinary || contextLines <= 0 {
			continue
		}
		// 변경된 라인 수 체크
		changedLines := fileDiffs[i].Additions + fileDiffs[i].Deletions
		if changedLines > MaxChangedLines {
			fileDiffs[i].LoadStatus = FileLoadStatusTooLarge
			continue
		}

		diffPath := fileDiffs[i].Path
		patchText, err := nativeGetFilePatch(ctx, repoPath, baseHash, headHash, diffPath, contextLines)
		if err != nil {
			fileDiffs[i].LoadStatus = FileLoadStatusCollapsed
			continue
		}

		if patchText == "" {
			fileDiffs[i].LoadStatus = FileLoadStatusLoaded
			filesWithChangesCount++
			continue
		}

		lineChanges, hunks, err := GenerateLineChangesFromPatch(patchText, contextLines)
		if err != nil {
			fileDiffs[i].LoadStatus = FileLoadStatusCollapsed
			continue
		}

		if len(lineChanges) > MaxFileLines {
			fileDiffs[i].LoadStatus = FileLoadStatusTooLarge
			continue
		}

		fileDiffs[i].Changes = lineChanges
		fileDiffs[i].Hunks = hunks
		fileDiffs[i].LoadStatus = FileLoadStatusLoaded
		filesWithChangesCount++
	}

	// LoadStatus가 아직 비어있는 파일들에 대해 post-processing
	for i := range fileDiffs {
		if fileDiffs[i].LoadStatus != "" {
			continue
		}
		if fileDiffs[i].IsBinary {
			fileDiffs[i].LoadStatus = FileLoadStatusBinary
		} else if contextLines <= 0 {
			fileDiffs[i].LoadStatus = FileLoadStatusCollapsed
		} else {
			// MaxFilesWithChanges 초과로 처리되지 않은 파일
			fileDiffs[i].LoadStatus = FileLoadStatusCollapsed
		}
	}

	// 통계 계산
	var stats DiffStats
	for _, fd := range fileDiffs {
		stats.FilesChanged++
		stats.TotalAdditions += fd.Additions
		stats.TotalDeletions += fd.Deletions
	}

	truncated := filesWithChangesCount >= MaxFilesWithChanges

	return &DiffResponse{
		Success:   true,
		Base:      baseName,
		Head:      headName,
		Files:     fileDiffs,
		Stats:     stats,
		Truncated: truncated,
	}, nil
}

// ---------------------------------------------------------------------------
// Helper functions: git command wrappers
// ---------------------------------------------------------------------------

// nativeRunGitCmd는 git 명령을 실행하고 stdout을 반환합니다.
func nativeRunGitCmd(ctx context.Context, repoPath string, timeout time.Duration, args ...string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	fullArgs := append([]string{"-c", "core.quotePath=false", "--git-dir", repoPath}, args...)
	cmd := exec.CommandContext(cmdCtx, "git", fullArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return "", fmt.Errorf("git %s: %s: %w", args[0], strings.TrimSpace(stderr.String()), err)
	}
	return string(out), nil
}

// nativeResolveRef는 ref를 커밋 해시로 resolve합니다.
func nativeResolveRef(ctx context.Context, repoPath, ref string) (string, error) {
	target := ref
	if target == "" {
		target = "HEAD"
	}

	out, err := nativeRunGitCmd(ctx, repoPath, nativeGitRefTimeout, "rev-parse", "--verify", target)
	if err != nil {
		// 브랜치 이름으로 재시도
		out, err = nativeRunGitCmd(ctx, repoPath, nativeGitRefTimeout, "rev-parse", "--verify", "refs/heads/"+target)
		if err != nil {
			// 태그로 재시도
			out, err = nativeRunGitCmd(ctx, repoPath, nativeGitRefTimeout, "rev-parse", "--verify", "refs/tags/"+target)
			if err != nil {
				return "", fmt.Errorf("reference %s not found", ref)
			}
		}
	}
	return strings.TrimSpace(out), nil
}

// nativeMergeBase는 두 커밋의 merge base를 찾습니다.
func nativeMergeBase(ctx context.Context, repoPath, a, b string) (string, error) {
	out, err := nativeRunGitCmd(ctx, repoPath, nativeGitRefTimeout, "merge-base", a, b)
	if err != nil {
		return "", fmt.Errorf("failed to find merge base: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// ResolvedRange는 diff 요청에서 base/head를 해시로 해석하고 mode에 맞춰
// effective_base를 미리 치환한 결과입니다. 모든 하위 git 호출은 EffectiveBase를
// base 인자로 사용해야 file list와 file diff가 동일 기준에서 계산됩니다.
type ResolvedRange struct {
	Mode          DiffMode
	BaseHash      string // 입력 base를 resolve한 해시
	HeadHash      string // 입력 head를 resolve한 해시
	MergeBase     string // merge-base(BaseHash, HeadHash). resolve 실패 시 빈 문자열.
	EffectiveBase string // direct: BaseHash. merge_base: MergeBase (없으면 BaseHash로 fallback).
}

// nativeResolveDiffRange는 base/head를 해시로 변환하고 mode에 따른 effective_base를 계산합니다.
//
// merge_base 모드에서 merge-base 계산이 실패하면 (히스토리가 끊긴 저장소 등의 예외 케이스)
// EffectiveBase = BaseHash로 fallback합니다 (direct와 같은 동작). 이 경우 호출자는
// MergeBase=="" 사실로 fallback 여부를 식별할 수 있습니다.
//
// 응답 메타에 BaseHash/HeadHash/MergeBase/EffectiveBase를 모두 노출하면 호출자가
// 트리(merge-base)와 file diff(effective_base) 정렬 여부를 직접 검증할 수 있습니다.
func nativeResolveDiffRange(ctx context.Context, repoPath, base, head string, mode DiffMode) (*ResolvedRange, error) {
	if !mode.IsValid() {
		mode = DiffModeMergeBase
	}

	baseHash, err := nativeResolveRef(ctx, repoPath, base)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve base reference %s: %w", base, err)
	}
	headHash, err := nativeResolveRef(ctx, repoPath, head)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve head reference %s: %w", head, err)
	}

	rr := &ResolvedRange{
		Mode:     mode,
		BaseHash: baseHash,
		HeadHash: headHash,
	}

	// merge-base는 모드와 무관하게 가능한 경우 항상 계산합니다 (관측 가능성 + direct 모드도 응답에 노출).
	// 실패는 비치명: direct에서는 영향 없음. merge_base 모드에서는 BaseHash로 fallback.
	if mb, mbErr := nativeMergeBase(ctx, repoPath, baseHash, headHash); mbErr == nil {
		rr.MergeBase = mb
	}

	switch mode {
	case DiffModeMergeBase:
		if rr.MergeBase != "" {
			rr.EffectiveBase = rr.MergeBase
		} else {
			rr.EffectiveBase = baseHash
		}
	default: // direct
		rr.EffectiveBase = baseHash
	}

	return rr, nil
}

// nativeBlobExistsAtRef는 ref:path가 git 객체 데이터베이스에 존재하는지 확인합니다.
// nativeCalculateFileDiff의 empty-status 분기에서 "양쪽에 동일 blob"과 "양쪽에 없음"을 구분하기 위해 사용.
func nativeBlobExistsAtRef(ctx context.Context, repoPath, ref, path string) bool {
	_, err := nativeRunGitCmd(ctx, repoPath, nativeGitRefTimeout,
		"cat-file", "-e", ref+":"+path)
	return err == nil
}

// nativeCalculateCommitCounts는 ahead/behind 커밋 수를 계산합니다.
func nativeCalculateCommitCounts(ctx context.Context, repoPath, baseHash, headHash, mergeBase string) (*CommitStats, error) {
	// merge base에서 head까지의 커밋 수 (ahead)
	aheadOut, err := nativeRunGitCmd(ctx, repoPath, nativeGitRefTimeout,
		"rev-list", "--count", mergeBase+".."+headHash)
	if err != nil {
		return nil, fmt.Errorf("failed to count ahead commits: %w", err)
	}
	ahead, _ := strconv.Atoi(strings.TrimSpace(aheadOut))

	// merge base에서 base까지의 커밋 수 (behind)
	behindOut, err := nativeRunGitCmd(ctx, repoPath, nativeGitRefTimeout,
		"rev-list", "--count", mergeBase+".."+baseHash)
	if err != nil {
		return nil, fmt.Errorf("failed to count behind commits: %w", err)
	}
	behind, _ := strconv.Atoi(strings.TrimSpace(behindOut))

	return &CommitStats{
		Ahead:  ahead,
		Behind: behind,
	}, nil
}

// nativeGetCommitMetadata는 커밋의 메타데이터를 가져옵니다.
func nativeGetCommitMetadata(ctx context.Context, repoPath, sha string) (*CommitMetadata, error) {
	format := "%H%x00%an%x00%ae%x00%aI%x00%s%x00%P"
	out, err := nativeRunGitCmd(ctx, repoPath, nativeGitRefTimeout,
		"log", "-1", "--format="+format, sha)
	if err != nil {
		return nil, fmt.Errorf("failed to get commit %s: %w", sha, err)
	}

	line := strings.TrimSpace(out)
	if line == "" {
		return nil, fmt.Errorf("commit not found: %s", sha)
	}

	parts := strings.SplitN(line, "\x00", 6)
	if len(parts) < 6 {
		return nil, fmt.Errorf("unexpected commit format: got %d fields", len(parts))
	}

	var parents []string
	if parts[5] != "" {
		parents = strings.Split(parts[5], " ")
	}

	return &CommitMetadata{
		SHA:     parts[0],
		Author:  parts[1],
		Email:   parts[2],
		Date:    parts[3],
		Message: strings.TrimSpace(parts[4]),
		Parents: parents,
	}, nil
}

// ---------------------------------------------------------------------------
// Diff file list parsing
// ---------------------------------------------------------------------------

// nativeGetDiffFileList는 두 커밋 간의 변경된 파일 목록을 가져옵니다.
// --name-status와 --numstat를 별도로 실행하고 결과를 병합합니다.
func nativeGetDiffFileList(ctx context.Context, repoPath, base, head string) ([]nativeFileMeta, error) {
	diffRange := base + ".." + head

	// 1. --name-status로 파일 상태 가져오기
	nameStatusOut, err := nativeRunGitCmd(ctx, repoPath, nativeGitTimeout,
		"diff", "-M", "--name-status", diffRange)
	if err != nil {
		return nil, fmt.Errorf("git diff --name-status: %w", err)
	}

	// 2. --numstat로 additions/deletions 가져오기
	numstatOut, err := nativeRunGitCmd(ctx, repoPath, nativeGitTimeout,
		"diff", "-M", "--numstat", diffRange)
	if err != nil {
		return nil, fmt.Errorf("git diff --numstat: %w", err)
	}

	// 파싱
	statusEntries := parseNameStatus(strings.TrimSpace(nameStatusOut))
	numstatEntries := parseNumstat(strings.TrimSpace(numstatOut))

	// 파일 메타데이터 빌드 (name-status 기반으로 정렬 유지)
	result := make([]nativeFileMeta, 0, len(statusEntries))
	for i, entry := range statusEntries {
		meta := nativeFileMeta{
			Path:    entry.path,
			OldPath: entry.oldPath,
			Status:  entry.status,
		}

		// numstat에서 stats 매칭 (인덱스 기반 - 동일 순서 보장)
		if i < len(numstatEntries) {
			meta.Additions = numstatEntries[i].additions
			meta.Deletions = numstatEntries[i].deletions
			meta.IsBinary = numstatEntries[i].isBinary
		}

		// 확장자 기반 바이너리 체크 (보완)
		if !meta.IsBinary {
			meta.IsBinary = isBinaryFileByExtension(meta.Path)
		}

		result = append(result, meta)
	}

	return result, nil
}

type nameStatusEntry struct {
	status  FileDiffStatus
	path    string
	oldPath string
}

type numstatEntry struct {
	additions int
	deletions int
	isBinary  bool
}

// parseNameStatus는 git diff --name-status 출력을 파싱합니다.
func parseNameStatus(output string) []nameStatusEntry {
	if output == "" {
		return nil
	}

	lines := strings.Split(output, "\n")
	result := make([]nameStatusEntry, 0, len(lines))

	for _, line := range lines {
		if line == "" {
			continue
		}
		entry := nameStatusEntry{}

		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}

		statusCode := parts[0]
		switch {
		case statusCode == "M":
			entry.status = FileDiffStatusModified
			entry.path = gitutil.UnquoteGitPath(parts[1])
		case statusCode == "A":
			entry.status = FileDiffStatusAdded
			entry.path = gitutil.UnquoteGitPath(parts[1])
		case statusCode == "D":
			entry.status = FileDiffStatusDeleted
			entry.path = gitutil.UnquoteGitPath(parts[1])
		case strings.HasPrefix(statusCode, "R"):
			entry.status = FileDiffStatusRenamed
			if len(parts) >= 3 {
				entry.oldPath = gitutil.UnquoteGitPath(parts[1])
				entry.path = gitutil.UnquoteGitPath(parts[2])
			} else {
				entry.path = gitutil.UnquoteGitPath(parts[1])
			}
		default:
			entry.status = FileDiffStatusModified
			entry.path = gitutil.UnquoteGitPath(parts[1])
		}

		result = append(result, entry)
	}

	return result
}

// parseNumstat는 git diff --numstat 출력을 파싱합니다.
func parseNumstat(output string) []numstatEntry {
	if output == "" {
		return nil
	}

	lines := strings.Split(output, "\n")
	result := make([]numstatEntry, 0, len(lines))

	for _, line := range lines {
		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		entry := numstatEntry{}
		if parts[0] == "-" && parts[1] == "-" {
			entry.isBinary = true
		} else {
			entry.additions, _ = strconv.Atoi(parts[0])
			entry.deletions, _ = strconv.Atoi(parts[1])
		}

		result = append(result, entry)
	}

	return result
}

// ---------------------------------------------------------------------------
// Per-file diff
// ---------------------------------------------------------------------------

// nativeGetFilePatch는 단일 파일의 unified diff를 가져옵니다.
func nativeGetFilePatch(ctx context.Context, repoPath, base, head, path string, contextLines int) (string, error) {
	return nativeRunGitCmd(ctx, repoPath, nativeGitTimeout,
		"diff", fmt.Sprintf("-U%d", contextLines), "-M", base+".."+head, "--", path)
}

// ---------------------------------------------------------------------------
// Commits by date
// ---------------------------------------------------------------------------

// nativeGetCommitsByDate는 두 커밋 사이의 커밋들을 날짜별로 그룹화합니다.
func nativeGetCommitsByDate(ctx context.Context, repoPath, from, to string) ([]CommitsByDate, error) {
	format := "%H%x00%an%x00%ae%x00%aI%x00%s%x00%P"
	out, err := nativeRunGitCmd(ctx, repoPath, nativeGitTimeout,
		"log", "--format="+format, from+".."+to)
	if err != nil {
		return nil, fmt.Errorf("failed to get commit log: %w", err)
	}

	raw := strings.TrimSpace(out)
	if raw == "" {
		return []CommitsByDate{}, nil
	}

	// 날짜별로 커밋 그룹화
	byDate := make(map[string][]CommitMetadata)
	lines := strings.Split(raw, "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, "\x00", 6)
		if len(parts) < 6 {
			continue
		}

		sha := parts[0]
		authorName := parts[1]
		authorEmail := parts[2]
		authorDateStr := parts[3]
		message := parts[4]
		parentStr := parts[5]

		var parents []string
		if parentStr != "" {
			parents = strings.Split(parentStr, " ")
		}

		// ISO 날짜에서 YYYY-MM-DD 추출
		dateKey := authorDateStr
		if len(dateKey) >= 10 {
			dateKey = dateKey[:10]
		}

		commitMeta := CommitMetadata{
			SHA:     sha,
			Author:  authorName,
			Email:   authorEmail,
			Date:    authorDateStr,
			Message: strings.TrimSpace(message),
			Parents: parents,
		}

		byDate[dateKey] = append(byDate[dateKey], commitMeta)
	}

	// 맵을 슬라이스로 변환
	var result []CommitsByDate
	for date, commits := range byDate {
		result = append(result, CommitsByDate{
			Date:    date,
			Commits: commits,
		})
	}

	// 날짜 내림차순 정렬
	sort.Slice(result, func(i, j int) bool {
		return result[i].Date > result[j].Date
	})

	return result, nil
}

// ---------------------------------------------------------------------------
// Utilities
// ---------------------------------------------------------------------------

// parseFileStatus는 name-status 출력 한 줄에서 파일 상태를 파싱합니다.
func parseFileStatus(line string) FileDiffStatus {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return FileDiffStatusModified
	}
	switch {
	case parts[0] == "A":
		return FileDiffStatusAdded
	case parts[0] == "D":
		return FileDiffStatusDeleted
	case strings.HasPrefix(parts[0], "R"):
		return FileDiffStatusRenamed
	default:
		return FileDiffStatusModified
	}
}

// nativeExpandContextLines는 blob에서 특정 라인 범위만 읽어 CONTEXT LineChange로 반환합니다.
// git diff를 실행하지 않고 blob 단일 읽기만 수행하므로 매우 경량입니다.
func nativeExpandContextLines(ctx context.Context, repoPath, ref, path string, fromOld, toOld, fromNew int) (*ExpandContextResponse, error) {
	// blob 내용 읽기 (git show ref:path)
	refHash, err := nativeResolveRef(ctx, repoPath, ref)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve ref '%s': %w", ref, err)
	}

	content, err := nativeRunGitCmd(ctx, repoPath, nativeGitTimeout, "show", refHash+":"+path)
	if err != nil {
		return nil, fmt.Errorf("file not found: %s at %s", path, ref)
	}

	lines := SplitLines(content)
	totalLines := len(lines)

	// 범위 클램핑
	if fromOld < 1 {
		fromOld = 1
	}
	if toOld > totalLines {
		toOld = totalLines
	}
	if fromOld > toOld {
		return &ExpandContextResponse{
			Success:  true,
			Lines:    []LineChange{},
			Path:     path,
			StartOld: fromOld,
			StartNew: fromNew,
			Count:    0,
		}, nil
	}

	// LineChange 생성 (0-based 인덱스 변환)
	result := make([]LineChange, 0, toOld-fromOld+1)
	oldLine := fromOld
	newLine := fromNew
	for i := fromOld - 1; i < toOld; i++ {
		// Text는 마커 접두사 없이 순수 라인 내용만 — Type=CONTEXT로 구분.
		result = append(result, LineChange{
			Type:           LineChangeTypeContext,
			BlobLineNumber: intPtr(oldLine),
			Text:           lines[i],
			Left:           intPtr(oldLine),
			Right:          intPtr(newLine),
		})
		oldLine++
		newLine++
	}

	return &ExpandContextResponse{
		Success:  true,
		Lines:    result,
		Path:     path,
		StartOld: fromOld,
		StartNew: fromNew,
		Count:    len(result),
	}, nil
}

// isBinaryByExtensionForDiff는 파일 확장자로 바이너리 여부를 판단합니다.
// diffs 패키지에서 사용하는 래퍼 함수입니다.
func isBinaryByExtensionForDiff(fileName string) bool {
	ext := strings.ToLower(filepath.Ext(fileName))
	if ext == "" {
		return false
	}
	binaryExtensions := map[string]bool{
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".bmp": true, ".ico": true, ".svg": true,
		".pdf": true, ".zip": true, ".tar": true, ".gz": true, ".bz2": true, ".xz": true, ".7z": true,
		".exe": true, ".dll": true, ".so": true, ".dylib": true, ".bin": true,
		".mp3": true, ".mp4": true, ".avi": true, ".mov": true, ".wmv": true,
		".woff": true, ".woff2": true, ".ttf": true, ".eot": true,
		".jar": true, ".war": true, ".class": true,
		".pyc": true, ".o": true, ".a": true, ".lib": true,
		".db": true, ".sqlite": true, ".sqlite3": true,
	}
	return binaryExtensions[ext]
}
