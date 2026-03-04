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
	emptyTreeHash       = "4b825dc642cb6eb9a060e54bf899d69f82cf279"
)

// nativeFileMeta는 git diff에서 추출한 파일별 메타데이터입니다.
type nativeFileMeta struct {
	Path      string
	OldPath   string // 리네임 시
	Status    FileDiffStatus
	Additions int
	Deletions int
	IsBinary  bool
	OldMode   string
	NewMode   string
	OldHash   string
	NewHash   string
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

	return diffResp, nil
}

// nativeCalculateFileDiff는 두 참조 간의 특정 파일에 대한 diff를 native git으로 계산합니다.
func nativeCalculateFileDiff(ctx context.Context, repoPath, filePath, base, head string, contextLines int) (*DiffResponse, error) {
	baseHash, err := nativeResolveRef(ctx, repoPath, base)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve base reference %s: %w", base, err)
	}
	headHash, err := nativeResolveRef(ctx, repoPath, head)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve head reference %s: %w", head, err)
	}

	// 단일 파일 status 확인
	statusOut, err := nativeRunGitCmd(ctx, repoPath, nativeGitTimeout,
		"diff", "--name-status", "-M", baseHash+".."+headHash, "--", filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to get file status: %w", err)
	}

	statusLine := strings.TrimSpace(statusOut)
	if statusLine == "" {
		return nil, fmt.Errorf("file not found in either base or head: %s", filePath)
	}

	// 파일 status 파싱
	status := parseFileStatus(statusLine)

	// 바이너리 체크
	isBinary := isBinaryFileByExtension(filePath)

	// numstat으로 stats 가져오기
	numstatOut, err := nativeRunGitCmd(ctx, repoPath, nativeGitTimeout,
		"diff", "--numstat", "-M", baseHash+".."+headHash, "--", filePath)
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
			patchText, err := nativeGetFilePatch(ctx, repoPath, baseHash, headHash, filePath, contextLines)
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
		Base:    base,
		Head:    head,
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
		// 메타데이터 설정
		metadata := make(map[string]interface{})
		if meta.OldMode != "" && meta.OldMode != "000000" {
			metadata["old_mode"] = meta.OldMode
		}
		if meta.NewMode != "" && meta.NewMode != "000000" {
			metadata["new_mode"] = meta.NewMode
		}
		if meta.OldHash != "" && !isZeroHash(meta.OldHash) {
			metadata["old_hash"] = meta.OldHash
		}
		if meta.NewHash != "" && !isZeroHash(meta.NewHash) {
			metadata["new_hash"] = meta.NewHash
		}
		if len(metadata) > 0 {
			fileDiff.Metadata = metadata
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

	out, err := cmd.Output()
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

	// 3. --raw로 mode/hash 메타데이터 가져오기
	rawOut, err := nativeRunGitCmd(ctx, repoPath, nativeGitTimeout,
		"diff", "-M", "--raw", diffRange)
	if err != nil {
		// raw는 선택적 - 실패해도 계속 진행
		rawOut = ""
	}

	// 파싱
	statusEntries := parseNameStatus(strings.TrimSpace(nameStatusOut))
	numstatEntries := parseNumstat(strings.TrimSpace(numstatOut))
	rawEntries := parseRawDiff(strings.TrimSpace(rawOut))

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

		// raw에서 mode/hash 매칭
		if i < len(rawEntries) {
			meta.OldMode = rawEntries[i].oldMode
			meta.NewMode = rawEntries[i].newMode
			meta.OldHash = rawEntries[i].oldHash
			meta.NewHash = rawEntries[i].newHash
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

type rawDiffEntry struct {
	oldMode string
	newMode string
	oldHash string
	newHash string
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

// parseRawDiff는 git diff --raw 출력을 파싱합니다.
// 형식: :old_mode new_mode old_hash new_hash status\tpath[\tpath2]
func parseRawDiff(output string) []rawDiffEntry {
	if output == "" {
		return nil
	}

	lines := strings.Split(output, "\n")
	result := make([]rawDiffEntry, 0, len(lines))

	for _, line := range lines {
		if !strings.HasPrefix(line, ":") {
			continue
		}

		// ':'를 제거하고 tab으로 분리
		line = line[1:]
		tabParts := strings.SplitN(line, "\t", 2)
		if len(tabParts) < 1 {
			continue
		}

		// 메타 부분 파싱: "old_mode new_mode old_hash new_hash status"
		metaParts := strings.Fields(tabParts[0])
		if len(metaParts) < 5 {
			continue
		}

		entry := rawDiffEntry{
			oldMode: metaParts[0],
			newMode: metaParts[1],
			oldHash: metaParts[2],
			newHash: metaParts[3],
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

// isZeroHash는 해시가 모두 0인지 확인합니다.
func isZeroHash(hash string) bool {
	for _, c := range hash {
		if c != '0' {
			return false
		}
	}
	return len(hash) > 0
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
