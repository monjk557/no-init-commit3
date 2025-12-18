package diffs

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
	"github.com/go-enry/go-enry/v2"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// generatePatch는 커밋 범위에 대한 Git patch 형식 출력을 생성합니다
func (h *DiffHandler) generatePatch(repoPath, base, head string) (string, error) {
	// 저장소 열기
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return "", fmt.Errorf("failed to open repository: %w", err)
	}

	// base와 head 참조를 커밋 객체로 해석
	baseCommit, err := h.resolveReferenceToCommit(repo, base)
	if err != nil {
		return "", fmt.Errorf("failed to resolve base reference %s: %w", base, err)
	}

	headCommit, err := h.resolveReferenceToCommit(repo, head)
	if err != nil {
		return "", fmt.Errorf("failed to resolve head reference %s: %w", head, err)
	}

	// go-git의 내장 patch 기능을 사용하여 patch 생성
	patch, err := baseCommit.Patch(headCommit)
	if err != nil {
		return "", fmt.Errorf("failed to generate patch: %w", err)
	}

	return patch.String(), nil
}

// calculateCommitDiff는 두 커밋 간의 실제 diff 계산을 수행합니다
func (h *DiffHandler) calculateCommitDiff(ctx context.Context, repoPath, base, head string, contextLines int) (*DiffResponse, error) {

	// 저장소 열기
	gitRepo, err := repositories.OpenRepositorySimple(repoPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = gitRepo.Close() }()

	// 커밋 객체 가져오기
	baseCommit, err := gitRepo.CommitObject(plumbing.NewHash(base))
	if err != nil {
		return nil, fmt.Errorf("failed to get base commit %s: %w", base, err)
	}

	headCommit, err := gitRepo.CommitObject(plumbing.NewHash(head))
	if err != nil {
		return nil, fmt.Errorf("failed to get head commit %s: %w", head, err)
	}

	// 커밋에서 트리 가져오기
	baseTree, err := baseCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get base tree: %w", err)
	}

	headTree, err := headCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get head tree: %w", err)
	}

	// 트리 간 diff 계산
	changes, err := object.DiffTree(baseTree, headTree)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate tree diff: %w", err)
	}

	// 변경 사항을 구조화된 응답으로 처리
	fileDiffs, stats, truncated, err := h.processChanges(changes, contextLines)
	if err != nil {
		return nil, fmt.Errorf("failed to process changes: %w", err)
	}

	return &DiffResponse{
		Success:   true,
		Base:      base,
		Head:      head,
		Files:     fileDiffs,
		Stats:     stats,
		Truncated: truncated,
	}, nil
}

// calculateSingleCommitDiff는 부모 커밋에 대한 단일 커밋의 diff 계산을 수행합니다
func (h *DiffHandler) calculateSingleCommitDiff(ctx context.Context, repoPath, sha string, contextLines int) (*SingleCommitDiffResponse, error) {
	// 캐시된 방식으로 저장소 열기
	gitRepoWrapper, err := repositories.OpenRepositorySimple(repoPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = gitRepoWrapper.Close() }()
	gitRepo := gitRepoWrapper.GetRepo()

	// 커밋 객체 가져오기
	commitHash := plumbing.NewHash(sha)
	commit, err := gitRepo.CommitObject(commitHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get commit %s: %w", sha, err)
	}

	// 커밋 메타데이터 추출
	commitMetadata := h.extractCommitMetadata(commit)

	// 다른 커밋 시나리오 처리
	var diffResp *DiffResponse

	if commit.NumParents() == 0 {
		// 초기 커밋 - 빈 트리와 비교
		diffResp, err = h.calculateInitialCommitDiff(ctx, commit, contextLines)
		if err != nil {
			return nil, fmt.Errorf("failed to calculate initial commit diff: %w", err)
		}
	} else {
		// 일반 커밋 - 첫 번째 부모와 비교
		parentCommit, err := commit.Parent(0)
		if err != nil {
			return nil, fmt.Errorf("failed to get parent commit: %w", err)
		}

		// 기존 커밋 diff 로직 재사용
		diffResp, err = h.calculateCommitDiffBetween(ctx, parentCommit, commit, contextLines)
		if err != nil {
			return nil, fmt.Errorf("failed to calculate commit diff: %w", err)
		}
	}

	// 커밋과 부모를 나타내도록 base와 head 설정
	if commit.NumParents() > 0 {
		parent, _ := commit.Parent(0)
		diffResp.Base = parent.Hash.String()
	} else {
		diffResp.Base = "empty_tree"
	}
	diffResp.Head = sha

	return &SingleCommitDiffResponse{
		Success:      true,
		Commit:       commitMetadata,
		DiffResponse: diffResp,
	}, nil
}

// extractCommitMetadata는 커밋 객체에서 메타데이터를 추출합니다
func (h *DiffHandler) extractCommitMetadata(commit *object.Commit) *CommitMetadata {
	var parents []string
	for _, parent := range commit.ParentHashes {
		parents = append(parents, parent.String())
	}

	return &CommitMetadata{
		SHA:     commit.Hash.String(),
		Author:  commit.Author.Name,
		Email:   commit.Author.Email,
		Date:    commit.Author.When.Format("2006-01-02T15:04:05Z07:00"),
		Message: strings.TrimSpace(commit.Message),
		Parents: parents,
	}
}

// calculateInitialCommitDiff는 부모가 없는 커밋(초기 커밋)에 대한 diff를 처리합니다
func (h *DiffHandler) calculateInitialCommitDiff(ctx context.Context, commit *object.Commit, contextLines int) (*DiffResponse, error) {
	// 커밋 트리 가져오기
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get commit tree: %w", err)
	}

	// 트리의 모든 파일을 추가로 처리하여 변경 사항 생성
	var fileDiffs []FileDiff
	var stats DiffStats
	filesWithChangesCount := 0 // Changes를 포함한 파일 수

	err = tree.Files().ForEach(func(file *object.File) error {
		// 컨텍스트 취소 확인
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// 파일 수가 너무 많으면 경고만 출력하고 계속 진행 (메타데이터는 모두 포함)
		if len(fileDiffs) > 0 && len(fileDiffs)%1000 == 0 {
			fmt.Printf("Processing file %d in initial commit...\n", len(fileDiffs))
		}

		// 파일 크기가 너무 크면 메타데이터만 포함하고 내용은 건너뛰기
		if file.Size > MaxFileSize {
			fileDiff := FileDiff{
				Path:      file.Name,
				Status:    FileDiffStatusAdded,
				Additions: 0, // 크기 제한으로 인해 정확한 라인 수를 알 수 없음
				Deletions: 0,
				IsBinary:  isBinaryFileByExtension(file.Name),
				Changes:   nil,
			}
			fileDiffs = append(fileDiffs, fileDiff)
			stats.FilesChanged++
			fmt.Printf("Warning: file %s is too large (%d bytes > %d bytes), metadata only\n", file.Name, file.Size, MaxFileSize)
			return nil
		}

		content, err := file.Contents()
		if err != nil {
			// 오류가 발생해도 최소한의 메타데이터는 포함
			fileDiff := FileDiff{
				Path:      file.Name,
				Status:    FileDiffStatusAdded,
				Additions: 0,
				Deletions: 0,
				IsBinary:  isBinaryFileByExtension(file.Name),
				Changes:   nil,
			}
			fileDiffs = append(fileDiffs, fileDiff)
			stats.FilesChanged++
			fmt.Printf("Error getting content for %s: %v (metadata only)\n", file.Name, err)
			return nil
		}

		lines := SplitLines(content)
		additions := len(lines)

		// 바이너리 파일 감지 (파일 확장자와 콘텐츠 모두 확인)
		isBinaryByExt := isBinaryFileByExtension(file.Name)
		isBinaryByContent := IsBinaryFile(content)
		isBinary := isBinaryByExt || isBinaryByContent

		// 파일 라인 수 제한 체크 - 너무 많으면 메타데이터만 포함
		if len(lines) > MaxFileLines {
			fileDiff := FileDiff{
				Path:      file.Name,
				Status:    FileDiffStatusAdded,
				Additions: additions,
				Deletions: 0,
				IsBinary:  isBinary,
				Changes:   nil,
			}
			fileDiffs = append(fileDiffs, fileDiff)
			stats.FilesChanged++
			stats.TotalAdditions += additions
			fmt.Printf("Warning: file %s has too many lines (%d > %d), metadata only\n", file.Name, len(lines), MaxFileLines)
			return nil
		}

		fileDiff := FileDiff{
			Path:      file.Name,
			Status:    FileDiffStatusAdded,
			Additions: additions,
			Deletions: 0,
			IsBinary:  isBinary,
			Changes:   nil, // 기본값은 nil
		}

		// 바이너리 파일의 경우 changes를 생성하지 않음
		if isBinary {
			fileDiffs = append(fileDiffs, fileDiff)
			stats.FilesChanged++
			stats.TotalAdditions += additions
			return nil
		}

		// 변경된 라인 수 체크 (additions만 있으므로 additions 기준)
		if additions > MaxChangedLines {
			// 변경된 라인이 너무 많으면 diff 내용(Changes)을 로드하지 않음
			fileDiff.Changes = nil
		} else if contextLines > 0 {
			// validation 통과한 파일 중에서 최대 10개까지만 changes 포함
			canIncludeChanges := filesWithChangesCount < MaxFilesWithChanges
			if canIncludeChanges {
				// 라인 변경 생성
				var lineChanges []LineChange
				for i, line := range lines {
					lineChanges = append(lineChanges, LineChange{
						Type:    LineChangeTypeAdd,
						LineNum: i + 1,
						Content: "+" + line,
					})
				}
				fileDiff.Changes = lineChanges
				filesWithChangesCount++
			} else {
				// 이미 10개를 초과한 경우 Changes를 포함하지 않음
				fileDiff.Changes = nil
			}
		} else {
			// contextLines가 0이면 Changes를 포함하지 않음
			fileDiff.Changes = nil
		}

		fileDiffs = append(fileDiffs, fileDiff)
		stats.FilesChanged++
		stats.TotalAdditions += additions

		return nil
	})

	if err != nil {
		// DiffTooLargeError인 경우에도 이미 처리된 파일들은 포함
		// 에러는 로깅만 하고 계속 진행
		if diffErr, ok := err.(*DiffTooLargeError); ok {
			fmt.Printf("Warning in initial commit: %s\n", diffErr.Reason)
		} else {
			fmt.Printf("Error iterating tree files: %v\n", err)
		}
		// 에러가 발생해도 이미 처리된 파일들은 반환
	}

	// 10개를 초과하는 파일이 있으면 truncated로 표시
	truncated := len(fileDiffs) > MaxFilesWithChanges

	return &DiffResponse{
		Files:     fileDiffs,
		Stats:     stats,
		Truncated: truncated,
	}, nil
}

// calculateCommitDiffBetween은 두 특정 커밋 객체 간의 diff를 계산합니다
func (h *DiffHandler) calculateCommitDiffBetween(ctx context.Context, baseCommit, headCommit *object.Commit, contextLines int) (*DiffResponse, error) {
	// 커밋에서 트리 가져오기
	baseTree, err := baseCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get base tree: %w", err)
	}

	headTree, err := headCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get head tree: %w", err)
	}

	// 트리 간 diff 계산
	changes, err := object.DiffTree(baseTree, headTree)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate tree diff: %w", err)
	}

	// 변경 사항을 구조화된 응답으로 처리
	fileDiffs, stats, truncated, err := h.processChanges(changes, contextLines)
	if err != nil {
		return nil, fmt.Errorf("failed to process changes: %w", err)
	}

	return &DiffResponse{
		Files:     fileDiffs,
		Stats:     stats,
		Truncated: truncated,
	}, nil
}

// processChanges는 go-git 변경 사항을 구조화된 FileDiff 객체로 변환합니다
// 모든 파일의 메타데이터는 포함하되, Changes(라인별 diff)는 validation 통과한 최대 10개 파일만 포함합니다
func (h *DiffHandler) processChanges(changes object.Changes, contextLines int) ([]FileDiff, DiffStats, bool, error) {
	// 1단계: 먼저 모든 파일의 메타데이터를 빠르게 수집 (patch 생성 없이)
	fileDiffs := make([]FileDiff, 0, len(changes))
	changeIndices := make([]int, 0, MaxFilesWithChanges) // changes가 필요한 파일 인덱스

	for i, change := range changes {
		fileName := GetFileName(change)
		fileDiff := FileDiff{
			Path:     fileName,
			Status:   h.getChangeStatus(change),
			Metadata: GetFileMetadata(change),
			Changes:  nil,
		}

		if change.From.Name != change.To.Name && change.From.Name != "" && change.To.Name != "" {
			fileDiff.OldPath = change.From.Name
		}

		// 바이너리 파일 체크 (확장자만으로 빠르게)
		fileDiff.IsBinary = isBinaryFileByExtension(fileName)

		// changes가 필요한 파일 후보로 추가 (바이너리가 아니고 contextLines > 0인 경우)
		if !fileDiff.IsBinary && contextLines > 0 && len(changeIndices) < MaxFilesWithChanges {
			changeIndices = append(changeIndices, i)
		}

		fileDiffs = append(fileDiffs, fileDiff)
	}

	// 2단계: changes가 필요한 파일만 patch 생성하여 처리
	filesWithChangesCount := 0
	for _, idx := range changeIndices {
		if filesWithChangesCount >= MaxFilesWithChanges {
			break
		}

		change := changes[idx]
		fileDiff, err := h.processChangeWithPatch(change, contextLines)
		if err != nil {
			// 에러 발생 시 메타데이터는 유지하고 통계만 업데이트
			if diffErr, ok := err.(*DiffTooLargeError); ok {
				fmt.Printf("Warning: %s - %s\n", fileDiffs[idx].Path, diffErr.Reason)
			} else {
				fmt.Printf("Error processing change for %s: %v\n", fileDiffs[idx].Path, err)
			}
			// 통계 정보가 있으면 업데이트
			if fileDiff.Additions > 0 || fileDiff.Deletions > 0 {
				fileDiffs[idx].Additions = fileDiff.Additions
				fileDiffs[idx].Deletions = fileDiff.Deletions
				fileDiffs[idx].IsBinary = fileDiff.IsBinary
			}
		} else {
			// 통계 정보 업데이트
			fileDiffs[idx].Additions = fileDiff.Additions
			fileDiffs[idx].Deletions = fileDiff.Deletions
			fileDiffs[idx].IsBinary = fileDiff.IsBinary

			// validation 통과한 파일만 changes 포함
			if fileDiff.Changes != nil {
				fileDiffs[idx].Changes = fileDiff.Changes
				filesWithChangesCount++
			}
		}
	}

	// 3단계: 통계 계산
	var stats DiffStats
	for _, fileDiff := range fileDiffs {
		stats.FilesChanged++
		stats.TotalAdditions += fileDiff.Additions
		stats.TotalDeletions += fileDiff.Deletions
	}

	// changes가 포함된 파일 수 제한 적용 (이미 2단계에서 처리됨)

	// 10개를 초과하는 파일이 있으면 truncated로 표시
	truncated := filesWithChangesCount >= MaxFilesWithChanges

	return fileDiffs, stats, truncated, nil
}

// processChangeWithPatch는 patch를 생성하여 단일 파일 변경을 처리합니다
// 이 함수는 changes가 필요한 파일에만 사용됩니다
func (h *DiffHandler) processChangeWithPatch(change *object.Change, contextLines int) (FileDiff, error) {
	fileName := GetFileName(change)
	fileDiff := FileDiff{
		Path:     fileName,
		Metadata: GetFileMetadata(change),
	}

	// 변경 상태 결정
	switch {
	case change.From.Name == "" && change.To.Name != "":
		fileDiff.Status = FileDiffStatusAdded
	case change.From.Name != "" && change.To.Name == "":
		fileDiff.Status = FileDiffStatusDeleted
	case change.From.Name != change.To.Name:
		fileDiff.Status = FileDiffStatusRenamed
		fileDiff.OldPath = change.From.Name
	default:
		fileDiff.Status = FileDiffStatusModified
	}

	// 라인 레벨 변경을 위한 patch 정보 가져오기
	patch, err := change.Patch()
	if err != nil {
		// patch를 가져올 수 없어도 메타데이터는 포함
		fileDiff.Additions = 0
		fileDiff.Deletions = 0
		fileDiff.IsBinary = isBinaryFileByExtension(fileName)
		fileDiff.Changes = nil
		return fileDiff, fmt.Errorf("failed to get patch: %w", err)
	}

	// patch 크기 체크
	patchText := patch.String()
	patchSize := len([]byte(patchText))
	if patchSize > MaxPatchSize {
		// patch가 너무 커도 메타데이터는 포함
		// patch에서 최소한의 통계 정보는 추출 시도
		additions, deletions := h.countPatchLines(patch)
		fileDiff.Additions = additions
		fileDiff.Deletions = deletions
		fileDiff.IsBinary = isBinaryFileByExtension(fileName)
		fileDiff.Changes = nil
		return fileDiff, &DiffTooLargeError{
			Reason: fmt.Sprintf("patch for file %s is too large (%d bytes > %d bytes)", fileName, patchSize, MaxPatchSize),
		}
	}

	// patch에서 실제 추가/삭제된 라인 수를 정확히 계산 (FilePatches 사용)
	additions, deletions := h.countPatchLines(patch)
	fileDiff.Additions = additions
	fileDiff.Deletions = deletions

	// 바이너리 파일 감지 (파일 확장자와 콘텐츠 모두 확인)
	isBinaryByExt := isBinaryFileByExtension(fileName)
	isBinaryByContent := IsBinaryFile(patchText)
	isBinary := isBinaryByExt || isBinaryByContent
	fileDiff.IsBinary = isBinary
	fileDiff.Changes = nil // 기본값은 nil

	// 바이너리 파일의 경우 changes를 생성하지 않음
	if isBinary {
		return fileDiff, nil
	}

	// 변경된 라인 수 체크 (additions + deletions)
	changedLines := additions + deletions
	if changedLines > MaxChangedLines {
		// 변경된 라인이 너무 많으면 diff 내용(Changes)을 로드하지 않음
		// 메타데이터(Additions, Deletions, Status 등)는 유지
		// Changes는 nil로 설정하여 diff 내용을 로드하지 않음을 표시
		fileDiff.Changes = nil
		return fileDiff, nil
	}

	// contextLines > 0이면 라인 레벨 변경 생성
	if contextLines > 0 {
		lines := SplitLines(patchText)

		// 파일 라인 수 제한 체크 - 너무 많으면 메타데이터만 포함
		if len(lines) > MaxFileLines {
			fileDiff.Changes = nil
			return fileDiff, &DiffTooLargeError{
				Reason: fmt.Sprintf("file %s has too many lines (%d > %d)", fileName, len(lines), MaxFileLines),
			}
		}

		lineChanges := h.GenerateLineChanges(lines, contextLines)
		fileDiff.Changes = lineChanges
	}

	return fileDiff, nil
}

// countPatchLines는 patch에서 실제 추가/삭제된 라인 수를 정확히 계산합니다
// diff 헤더(+++ b/file, --- a/file 등)는 제외하고 실제 변경 라인만 카운트합니다
func (h *DiffHandler) countPatchLines(patch *object.Patch) (additions, deletions int) {
	for _, filePatch := range patch.FilePatches() {
		for _, chunk := range filePatch.Chunks() {
			// chunk.Type()의 반환값:
			// 0 = Context (변경 없음, 컨텍스트 라인)
			// 1 = Add (추가된 라인)
			// 2 = Delete (삭제된 라인)
			switch chunk.Type() {
			case 1: // Add
				// Content에서 실제 라인 수 계산
				content := chunk.Content()
				// 개행 문자로 분할하여 라인 수 계산
				lines := strings.Split(content, "\n")
				// 마지막이 빈 문자열이면(개행으로 끝난 경우) 제거
				if len(lines) > 0 && lines[len(lines)-1] == "" {
					lines = lines[:len(lines)-1]
				}
				additions += len(lines)
			case 2: // Delete
				content := chunk.Content()
				lines := strings.Split(content, "\n")
				if len(lines) > 0 && lines[len(lines)-1] == "" {
					lines = lines[:len(lines)-1]
				}
				deletions += len(lines)
			}
		}
	}
	return additions, deletions
}

// GenerateLineChanges는 라인별 diff 정보를 생성합니다
func (h *DiffHandler) GenerateLineChanges(lines []string, contextLines int) []LineChange {
	return GenerateLineChanges(lines, contextLines)
}

// GenerateLineChanges는 라인별 diff 정보를 생성하는 공통 함수입니다
func GenerateLineChanges(lines []string, contextLines int) []LineChange {
	var lineChanges []LineChange
	lineNum := 1

	for _, line := range lines {
		if len(line) == 0 {
			continue
		}

		var changeType LineChangeType
		switch line[0] {
		case '+':
			changeType = LineChangeTypeAdd
		case '-':
			changeType = LineChangeTypeDel
		case ' ':
			changeType = LineChangeTypeContext
		default:
			changeType = LineChangeTypeContext
		}

		lineChanges = append(lineChanges, LineChange{
			Type:    changeType,
			LineNum: lineNum,
			Content: line,
		})

		if changeType != LineChangeTypeDel {
			lineNum++
		}
	}

	return lineChanges
}

// getFileName은 change 객체에서 파일 이름을 추출합니다
func GetFileName(change *object.Change) string {
	if change.To.Name != "" {
		return change.To.Name
	}
	return change.From.Name
}

// splitLines는 콘텐츠를 개별 라인으로 분할합니다
func SplitLines(content string) []string {
	if content == "" {
		return nil
	}

	var lines []string
	start := 0
	for i, char := range content {
		if char == '\n' {
			lines = append(lines, content[start:i])
			start = i + 1
		}
	}

	// 콘텐츠가 개행으로 끝나지 않으면 마지막 라인 추가
	if start < len(content) {
		lines = append(lines, content[start:])
	}

	return lines
}

// calculateBranchDiff는 merge base 분석을 통해 두 브랜치 간의 diff 계산을 수행합니다
func (h *DiffHandler) CalculateBranchDiff(ctx context.Context, repoPath, baseBranch, headBranch string, contextLines int) (*DiffResponse, error) {
	// 캐시된 방식으로 저장소 열기
	gitRepoWrapper, err := repositories.OpenRepositorySimple(repoPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = gitRepoWrapper.Close() }()

	gitRepo := gitRepoWrapper.GetRepo()
	// 브랜치 이름을 커밋으로 해석
	baseCommit, err := h.resolveBranchToCommit(gitRepo, baseBranch)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve base branch %s: %w", baseBranch, err)
	}

	headCommit, err := h.resolveBranchToCommit(gitRepo, headBranch)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve head branch %s: %w", headBranch, err)
	}

	// 정확한 비교를 위한 merge base 찾기
	mergeBase, err := h.findMergeBase(gitRepo, baseCommit, headCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to find merge base: %w", err)
	}

	// 커밋 수 계산 (ahead/behind)
	commitStats, err := h.calculateCommitCounts(gitRepo, baseCommit, headCommit, mergeBase)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate commit counts: %w", err)
	}

	// diff 계산을 위한 트리 가져오기 (정확한 브랜치 비교를 위해 merge base vs head 사용)
	mergeBaseTree, err := mergeBase.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get merge base tree: %w", err)
	}

	headTree, err := headCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get head tree: %w", err)
	}

	// merge base와 head 간 diff 계산
	changes, err := object.DiffTree(mergeBaseTree, headTree)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate tree diff: %w", err)
	}

	// 변경 사항을 구조화된 응답으로 처리
	fileDiffs, stats, truncated, err := h.processChanges(changes, contextLines)
	if err != nil {
		return nil, fmt.Errorf("failed to process changes: %w", err)
	}

	// 날짜별 커밋 목록 가져오기
	commitsByDate, err := h.getCommitsByDate(gitRepo, mergeBase, headCommit)
	if err != nil {
		// 날짜별 커밋 목록 실패는 경고로 처리하고 계속 진행
		fmt.Printf("Warning: failed to get commits by date: %v\n", err)
		commitsByDate = []CommitsByDate{}
	}

	return &DiffResponse{
		Success:       true,
		Base:          baseBranch,
		Head:          headBranch,
		Files:         fileDiffs,
		Stats:         stats,
		Commits:       commitStats,
		CommitsByDate: commitsByDate,
		MergeBase:     mergeBase.Hash.String(),
		Truncated:     truncated,
	}, nil
}

// resolveBranchToCommit은 브랜치 이름을 커밋 객체로 해석합니다
func (h *DiffHandler) resolveBranchToCommit(repo *gogit.Repository, branchName string) (*object.Commit, error) {
	// 먼저 참조로 해석 시도
	ref, err := repo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	if err != nil {
		// 브랜치 참조 실패시 직접 해시로 시도
		if plumbing.IsHash(branchName) {
			return repo.CommitObject(plumbing.NewHash(branchName))
		}
		return nil, fmt.Errorf("branch %s not found", branchName)
	}

	// 참조에서 커밋 가져오기
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, fmt.Errorf("failed to get commit from branch %s: %w", branchName, err)
	}

	return commit, nil
}

// findMergeBase는 두 커밋의 merge base (공통 조상)를 찾습니다
func (h *DiffHandler) findMergeBase(repo *gogit.Repository, commit1, commit2 *object.Commit) (*object.Commit, error) {
	// go-git의 merge base 알고리즘 사용
	mergeBaseCommits, err := commit1.MergeBase(commit2)
	if err != nil {
		return nil, fmt.Errorf("failed to find merge base: %w", err)
	}

	if len(mergeBaseCommits) == 0 {
		return nil, fmt.Errorf("no common ancestor found")
	}

	// 첫 번째 (가장 최근) merge base 반환
	return mergeBaseCommits[0], nil
}

// calculateCommitCounts는 각 브랜치가 몇 개의 커밋만큼 앞서있거나 뒤처져있는지 계산합니다
func (h *DiffHandler) calculateCommitCounts(repo *gogit.Repository, baseCommit, headCommit, mergeBase *object.Commit) (*CommitStats, error) {
	// merge base에서 head까지의 커밋 수 계산 (ahead)
	ahead, err := h.countCommitsBetween(repo, mergeBase, headCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to count ahead commits: %w", err)
	}

	// merge base에서 base까지의 커밋 수 계산 (behind)
	behind, err := h.countCommitsBetween(repo, mergeBase, baseCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to count behind commits: %w", err)
	}

	return &CommitStats{
		Ahead:  ahead,
		Behind: behind,
	}, nil
}

// getCommitsByDate는 두 커밋 사이의 커밋들을 날짜별로 그룹화합니다
func (h *DiffHandler) getCommitsByDate(repo *gogit.Repository, from, to *object.Commit) ([]CommitsByDate, error) {
	if from.Hash == to.Hash {
		return []CommitsByDate{}, nil
	}

	// 'to' 커밋에서 커밋 반복자 가져오기
	commitIter, err := repo.Log(&gogit.LogOptions{
		From: to.Hash,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get commit log: %w", err)
	}
	defer commitIter.Close()

	// 날짜별로 커밋을 그룹화할 맵
	commitsByDate := make(map[string][]CommitMetadata)
	fromHash := from.Hash.String()

	err = commitIter.ForEach(func(commit *object.Commit) error {
		if commit.Hash.String() == fromHash {
			// 'from' 커밋에 도달하면 중지
			return fmt.Errorf("stop iteration")
		}

		// 커밋 날짜를 YYYY-MM-DD 형식으로 변환
		date := commit.Author.When.Format("2006-01-02")

		// 커밋 메타데이터 생성
		commitMeta := CommitMetadata{
			SHA:     commit.Hash.String(),
			Author:  commit.Author.Name,
			Email:   commit.Author.Email,
			Date:    commit.Author.When.Format("2006-01-02 15:04:05"),
			Message: commit.Message,
			Parents: make([]string, len(commit.ParentHashes)),
		}

		// 부모 커밋 해시들 추가
		for i, parentHash := range commit.ParentHashes {
			commitMeta.Parents[i] = parentHash.String()
		}

		// 날짜별로 그룹화
		commitsByDate[date] = append(commitsByDate[date], commitMeta)
		return nil
	})

	// 'from' 커밋을 찾아서 중지했는지 확인
	if err != nil && err.Error() != "stop iteration" {
		return nil, fmt.Errorf("failed to iterate commits: %w", err)
	}

	// 맵을 슬라이스로 변환 (날짜 순으로 정렬)
	var result []CommitsByDate
	for date, commits := range commitsByDate {
		result = append(result, CommitsByDate{
			Date:    date,
			Commits: commits,
		})
	}

	// 날짜 순으로 정렬 (최신 날짜가 먼저)
	sort.Slice(result, func(i, j int) bool {
		return result[i].Date > result[j].Date
	})

	return result, nil
}

// countCommitsBetween은 두 커밋 사이의 커밋 수를 계산합니다 (from은 제외, to는 포함)
func (h *DiffHandler) countCommitsBetween(repo *gogit.Repository, from, to *object.Commit) (int, error) {
	if from.Hash == to.Hash {
		return 0, nil
	}

	// 'to' 커밋에서 커밋 반복자 가져오기
	commitIter, err := repo.Log(&gogit.LogOptions{
		From: to.Hash,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to get commit log: %w", err)
	}
	defer commitIter.Close()

	count := 0
	fromHash := from.Hash.String()

	err = commitIter.ForEach(func(commit *object.Commit) error {
		if commit.Hash.String() == fromHash {
			// 'from' 커밋에 도달하면 중지 (카운트하지 않음)
			return fmt.Errorf("stop iteration") // 반복을 중단하기 위해 오류 사용
		}
		count++
		return nil
	})

	// 'from' 커밋을 찾아서 중지했는지 확인 (정상 케이스)
	if err != nil && err.Error() == "stop iteration" {
		return count, nil
	}

	// 오류가 없으면 'from'을 찾지 못하고 루트까지 모든 커밋을 계산한 것
	// 이는 'from'이 'to'의 조상이 아니라는 의미
	if err == nil {
		return count, nil
	}

	return 0, fmt.Errorf("failed to iterate commits: %w", err)
}

// calculateFileDiff는 두 참조 간의 특정 파일에 대한 diff 계산을 수행합니다
func (h *DiffHandler) calculateFileDiff(ctx context.Context, repoPath, filePath, base, head string, contextLines int, format string) (*DiffResponse, error) {
	// 저장소 열기
	gitRepoWrapper, err := repositories.OpenRepositorySimple(repoPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = gitRepoWrapper.Close() }()

	gitRepo := gitRepoWrapper.GetRepo()
	// base와 head를 커밋 객체로 해석 (커밋 해시와 브랜치 이름 모두 지원)
	baseCommit, err := h.resolveReferenceToCommit(gitRepo, base)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve base reference %s: %w", base, err)
	}

	headCommit, err := h.resolveReferenceToCommit(gitRepo, head)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve head reference %s: %w", head, err)
	}

	// 커밋에서 트리 가져오기
	baseTree, err := baseCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get base tree: %w", err)
	}

	headTree, err := headCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get head tree: %w", err)
	}

	// 양쪽 트리에서 파일 엔트리 가져오기
	baseFile, baseErr := baseTree.File(filePath)
	headFile, headErr := headTree.File(filePath)

	// 파일이 하나 또는 양쪽 트리에 존재하지 않는 경우 처리
	var fileDiff FileDiff
	var stats DiffStats

	// 파일 상태를 결정하고 그에 따라 처리
	switch {
	case baseErr != nil && headErr != nil:
		// 파일이 양쪽 트리 모두에 존재하지 않음
		return nil, fmt.Errorf("file not found in either base or head: %s", filePath)

	case baseErr != nil && headErr == nil:
		// head에 파일이 추가됨
		fileDiff, err = h.processFileAddition(headFile, filePath, contextLines, format)
		if err != nil {
			return nil, fmt.Errorf("failed to process file addition: %w", err)
		}

	case baseErr == nil && headErr != nil:
		// head에서 파일이 삭제됨
		fileDiff, err = h.processFileDeletion(baseFile, filePath, contextLines, format)
		if err != nil {
			return nil, fmt.Errorf("failed to process file deletion: %w", err)
		}

	default:
		// 파일이 양쪽 트리 모두에 존재, 수정 diff 계산
		fileDiff, err = h.processFileModification(baseFile, headFile, filePath, contextLines, format)
		if err != nil {
			return nil, fmt.Errorf("failed to process file modification: %w", err)
		}
	}

	// 통계 업데이트
	stats.FilesChanged = 1
	stats.TotalAdditions = fileDiff.Additions
	stats.TotalDeletions = fileDiff.Deletions

	return &DiffResponse{
		Success: true,
		Base:    base,
		Head:    head,
		Files:   []FileDiff{fileDiff},
		Stats:   stats,
	}, nil
}

// resolveReferenceToCommit은 참조 (커밋 해시 또는 브랜치 이름)를 커밋 객체로 해석합니다
func (h *DiffHandler) resolveReferenceToCommit(repo *gogit.Repository, ref string) (*object.Commit, error) {
	// 먼저 커밋 해시로 시도
	if plumbing.IsHash(ref) {
		return repo.CommitObject(plumbing.NewHash(ref))
	}

	// 브랜치 참조로 시도
	branchRef, err := repo.Reference(plumbing.NewBranchReferenceName(ref), true)
	if err == nil {
		return repo.CommitObject(branchRef.Hash())
	}

	// 태그 참조로 시도
	tagRef, err := repo.Reference(plumbing.NewTagReferenceName(ref), true)
	if err == nil {
		// 태그는 커밋 또는 태그 객체를 가리킬 수 있음
		obj, err := repo.Object(plumbing.AnyObject, tagRef.Hash())
		if err != nil {
			return nil, fmt.Errorf("failed to resolve tag object: %w", err)
		}

		switch o := obj.(type) {
		case *object.Commit:
			return o, nil
		case *object.Tag:
			return repo.CommitObject(o.Target)
		default:
			return nil, fmt.Errorf("tag points to unsupported object type")
		}
	}

	return nil, fmt.Errorf("reference %s not found", ref)
}

// processFileAddition은 파일이 추가된 경우를 처리합니다 (head에만 존재)
func (h *DiffHandler) processFileAddition(file *object.File, filePath string, contextLines int, format string) (FileDiff, error) {
	// 파일 크기 체크
	if file.Size > MaxFileSize {
		return FileDiff{}, &DiffTooLargeError{
			Reason: fmt.Sprintf("file %s is too large (%d bytes > %d bytes)", filePath, file.Size, MaxFileSize),
		}
	}

	content, err := file.Contents()
	if err != nil {
		return FileDiff{}, fmt.Errorf("failed to get file contents: %w", err)
	}

	lines := SplitLines(content)
	additions := len(lines)

	// 파일 라인 수 제한 체크
	if len(lines) > MaxFileLines {
		return FileDiff{}, &DiffTooLargeError{
			Reason: fmt.Sprintf("file %s has too many lines (%d > %d)", filePath, len(lines), MaxFileLines),
		}
	}

	// 바이너리 파일 감지 (파일 확장자와 콘텐츠 모두 확인)
	isBinaryByExt := isBinaryFileByExtension(filePath)
	isBinaryByContent := isBinaryContent(content)
	isBinary := isBinaryByExt || isBinaryByContent

	fileDiff := FileDiff{
		Path:      filePath,
		Status:    FileDiffStatusAdded,
		Additions: additions,
		Deletions: 0,
		IsBinary:  isBinary,
		Changes:   nil, // 기본값은 nil
	}

	// 바이너리 파일의 경우 changes를 생성하지 않음
	if isBinary {
		return fileDiff, nil
	}

	// 변경된 라인 수 체크 (additions만 있으므로 additions 기준)
	if additions > MaxChangedLines {
		// 변경된 라인이 너무 많으면 diff 내용(Changes)을 로드하지 않음
		fileDiff.Changes = nil
		return fileDiff, nil
	}

	// contextLines > 0이면 라인 변경 생성
	if contextLines > 0 {
		var lineChanges []LineChange
		for i, line := range lines {
			lineChanges = append(lineChanges, LineChange{
				Type:    LineChangeTypeAdd,
				LineNum: i + 1,
				Content: "+" + line,
			})
		}
		fileDiff.Changes = lineChanges
	}

	return fileDiff, nil
}

// processFileDeletion은 파일이 삭제된 경우를 처리합니다 (base에만 존재)
func (h *DiffHandler) processFileDeletion(file *object.File, filePath string, contextLines int, format string) (FileDiff, error) {
	// 파일 크기 체크
	if file.Size > MaxFileSize {
		return FileDiff{}, &DiffTooLargeError{
			Reason: fmt.Sprintf("file %s is too large (%d bytes > %d bytes)", filePath, file.Size, MaxFileSize),
		}
	}

	content, err := file.Contents()
	if err != nil {
		return FileDiff{}, fmt.Errorf("failed to get file contents: %w", err)
	}

	lines := SplitLines(content)
	deletions := len(lines)

	// 파일 라인 수 제한 체크
	if len(lines) > MaxFileLines {
		return FileDiff{}, &DiffTooLargeError{
			Reason: fmt.Sprintf("file %s has too many lines (%d > %d)", filePath, len(lines), MaxFileLines),
		}
	}

	// 바이너리 파일 감지 (파일 확장자와 콘텐츠 모두 확인)
	isBinaryByExt := isBinaryFileByExtension(filePath)
	isBinaryByContent := isBinaryContent(content)
	isBinary := isBinaryByExt || isBinaryByContent

	fileDiff := FileDiff{
		Path:      filePath,
		Status:    FileDiffStatusDeleted,
		Additions: 0,
		Deletions: deletions,
		IsBinary:  isBinary,
		Changes:   nil, // 기본값은 nil
	}

	// 바이너리 파일의 경우 changes를 생성하지 않음
	if isBinary {
		return fileDiff, nil
	}

	// 변경된 라인 수 체크 (deletions만 있으므로 deletions 기준)
	if deletions > MaxChangedLines {
		// 변경된 라인이 너무 많으면 diff 내용(Changes)을 로드하지 않음
		fileDiff.Changes = nil
		return fileDiff, nil
	}

	// contextLines > 0이면 라인 변경 생성
	if contextLines > 0 {
		var lineChanges []LineChange
		for i, line := range lines {
			lineChanges = append(lineChanges, LineChange{
				Type:    LineChangeTypeDel,
				LineNum: i + 1,
				Content: "-" + line,
			})
		}
		fileDiff.Changes = lineChanges
	}

	return fileDiff, nil
}

// processFileModification은 파일이 수정된 경우를 처리합니다 (base와 head 모두에 존재)
func (h *DiffHandler) processFileModification(baseFile, headFile *object.File, filePath string, contextLines int, format string) (FileDiff, error) {
	// 파일 크기 체크 (base와 head 중 큰 것 기준)
	maxSize := baseFile.Size
	if headFile.Size > maxSize {
		maxSize = headFile.Size
	}
	if maxSize > MaxFileSize {
		return FileDiff{}, &DiffTooLargeError{
			Reason: fmt.Sprintf("file %s is too large (%d bytes > %d bytes)", filePath, maxSize, MaxFileSize),
		}
	}

	baseContent, err := baseFile.Contents()
	if err != nil {
		return FileDiff{}, fmt.Errorf("failed to get base file contents: %w", err)
	}

	headContent, err := headFile.Contents()
	if err != nil {
		return FileDiff{}, fmt.Errorf("failed to get head file contents: %w", err)
	}

	// 바이너리 파일 감지 (파일 확장자와 콘텐츠 모두 확인)
	isBinaryByExt := isBinaryFileByExtension(filePath)
	isBinaryByContent := isBinaryContent(baseContent) || isBinaryContent(headContent)
	isBinary := isBinaryByExt || isBinaryByContent

	fileDiff := FileDiff{
		Path:     filePath,
		Status:   FileDiffStatusModified,
		IsBinary: isBinary,
	}

	// 바이너리 파일의 경우 변경되었음만 표시
	if isBinary {
		return fileDiff, nil
	}

	// 텍스트 파일에 대한 라인 레벨 diff 계산
	baseLines := SplitLines(baseContent)
	headLines := SplitLines(headContent)

	// 파일 라인 수 제한 체크 (base와 head 중 큰 것 기준)
	maxLines := len(baseLines)
	if len(headLines) > maxLines {
		maxLines = len(headLines)
	}
	if maxLines > MaxFileLines {
		return FileDiff{}, &DiffTooLargeError{
			Reason: fmt.Sprintf("file %s has too many lines (%d > %d)", filePath, maxLines, MaxFileLines),
		}
	}

	// 간단한 라인 기반 diff 알고리즘
	lineChanges, additions, deletions := h.calculateLineDiff(baseLines, headLines, contextLines, format)

	fileDiff.Additions = additions
	fileDiff.Deletions = deletions

	// 변경된 라인 수 체크 (additions + deletions)
	changedLines := additions + deletions
	if changedLines > MaxChangedLines {
		// 변경된 라인이 너무 많으면 diff 내용(Changes)을 로드하지 않음
		fileDiff.Changes = nil
		return fileDiff, nil
	}

	fileDiff.Changes = lineChanges

	return fileDiff, nil
}

// calculateLineDiff는 라인 기반 diff 계산을 수행합니다
func (h *DiffHandler) calculateLineDiff(baseLines, headLines []string, contextLines int, format string) ([]LineChange, int, int) {
	var lineChanges []LineChange
	additions := 0
	deletions := 0

	// 간단한 diff 알고리즘: 공통 부분 수열을 찾고 변경 사항 식별
	// 이는 기본 구현 - 프로덕션용으로는 더 정교한 diff 알고리즘 사용 고려

	baseIndex := 0
	headIndex := 0
	lineNum := 1

	for baseIndex < len(baseLines) || headIndex < len(headLines) {
		if baseIndex >= len(baseLines) {
			// head에 남은 라인들은 추가
			for headIndex < len(headLines) {
				lineChanges = append(lineChanges, LineChange{
					Type:    LineChangeTypeAdd,
					LineNum: lineNum,
					Content: "+" + headLines[headIndex],
				})
				additions++
				headIndex++
				lineNum++
			}
		} else if headIndex >= len(headLines) {
			// base에 남은 라인들은 삭제
			for baseIndex < len(baseLines) {
				lineChanges = append(lineChanges, LineChange{
					Type:    LineChangeTypeDel,
					LineNum: lineNum,
					Content: "-" + baseLines[baseIndex],
				})
				deletions++
				baseIndex++
			}
		} else if baseLines[baseIndex] == headLines[headIndex] {
			// 라인이 동일하면 컨텍스트 윈도우 내에 있을 경우 컨텍스트로 추가
			lineChanges = append(lineChanges, LineChange{
				Type:    LineChangeTypeContext,
				LineNum: lineNum,
				Content: " " + baseLines[baseIndex],
			})
			baseIndex++
			headIndex++
			lineNum++
		} else {
			// 라인이 다르면 삭제와 추가로 표시
			lineChanges = append(lineChanges, LineChange{
				Type:    LineChangeTypeDel,
				LineNum: lineNum,
				Content: "-" + baseLines[baseIndex],
			})
			deletions++
			baseIndex++

			lineChanges = append(lineChanges, LineChange{
				Type:    LineChangeTypeAdd,
				LineNum: lineNum,
				Content: "+" + headLines[headIndex],
			})
			additions++
			headIndex++
			lineNum++
		}
	}

	return lineChanges, additions, deletions
}

// isBinaryContent는 콘텐츠가 바이너리로 보이는지 확인합니다
// 처음 8KB만 스캔하여 null 바이트(\0)가 하나라도 있으면 바이너리로 판단합니다
func isBinaryContent(content string) bool {
	const scanSize = 8 * 1024 // 8KB

	contentBytes := []byte(content)
	scanLength := len(contentBytes)
	if scanLength > scanSize {
		scanLength = scanSize
	}

	// 처음 8KB만 스캔하여 null 바이트 체크
	for i := 0; i < scanLength; i++ {
		if contentBytes[i] == 0 {
			return true
		}
	}

	return false
}

// validateAndExtractParams는 요청에서 공통 파라미터를 추출하고 검증합니다
func (h *DiffHandler) validateAndExtractParams(c *gin.Context) (*RequestParams, error) {
	// 경로 파라미터 추출
	owner := c.Param("owner")
	repo := c.Param("repo")
	base := c.Query("base")
	head := c.Query("head")

	// 필수 파라미터 검증
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("missing required parameters: owner or repo")
	}

	// 권한 확인을 위해 헤더에서 사용자 ID 가져오기

	return &RequestParams{
		Owner: owner,
		Repo:  repo,
		Base:  base,
		Head:  head,
	}, nil
}

// extractPaginationParams는 쿼리 문자열에서 페이지네이션 파라미터를 추출합니다
func extractPaginationParams(r *http.Request) PaginationParams {
	params := PaginationParams{
		Page:     1,  // 기본값: 페이지 1
		PageSize: 50, // 기본 페이지 크기
	}

	if pageStr := r.URL.Query().Get("page"); pageStr != "" {
		if page, err := strconv.Atoi(pageStr); err == nil && page > 0 {
			params.Page = page
		}
	}

	if sizeStr := r.URL.Query().Get("page_size"); sizeStr != "" {
		if size, err := strconv.Atoi(sizeStr); err == nil && size > 0 && size <= 200 {
			params.PageSize = size
		}
	}

	return params
}

// paginateFiles는 파일 diff에 페이지네이션을 적용합니다
func paginateFiles(files []FileDiff, pagination PaginationParams) ([]FileDiff, PaginationParams) {
	total := len(files)
	pagination.Total = total

	// 시작 및 종료 인덱스 계산
	start := (pagination.Page - 1) * pagination.PageSize
	if start >= total {
		pagination.HasMore = false
		return []FileDiff{}, pagination
	}

	end := start + pagination.PageSize
	if end > total {
		end = total
		pagination.HasMore = false
	} else {
		pagination.HasMore = true
	}

	return files[start:end], pagination
}

// IsBinaryFile은 콘텐츠와 확장자를 기반으로 파일이 바이너리인지 확인합니다
// 처음 8KB만 스캔하여 효율성을 높입니다
func IsBinaryFile(content string) bool {
	const scanSize = 8 * 1024 // 8KB

	contentBytes := []byte(content)
	scanLength := len(contentBytes)
	if scanLength > scanSize {
		scanLength = scanSize
	}

	// 처음 8KB만 enry 라이브러리에 전달
	return enry.IsBinary(contentBytes[:scanLength])
}

// isBinaryFileByExtension는 파일 확장자를 기반으로 바이너리 파일인지 확인합니다
func isBinaryFileByExtension(fileName string) bool {
	ext := strings.ToLower(filepath.Ext(fileName))
	if ext == "" {
		return false
	}

	// 일반적인 바이너리 파일 확장자 목록
	binaryExtensions := map[string]bool{
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".bmp": true, ".ico": true, ".svg": true,
		".pdf": true, ".zip": true, ".tar": true, ".gz": true, ".bz2": true, ".xz": true, ".7z": true,
		".exe": true, ".dll": true, ".so": true, ".dylib": true, ".bin": true,
		".mp3": true, ".mp4": true, ".avi": true, ".mov": true, ".wmv": true,
		".doc": true, ".docx": true, ".xls": true, ".xlsx": true, ".ppt": true, ".pptx": true,
		".woff": true, ".woff2": true, ".ttf": true, ".eot": true,
		".jar": true, ".war": true, ".ear": true, ".class": true,
		".pyc": true, ".pyo": true, ".o": true, ".a": true, ".lib": true,
		".db": true, ".sqlite": true, ".sqlite3": true,
		".dmg": true, ".iso": true, ".img": true,
	}

	return binaryExtensions[ext]
}

// getChangeStatus는 change 객체에서 파일 상태를 추출합니다
func (h *DiffHandler) getChangeStatus(change *object.Change) FileDiffStatus {
	switch {
	case change.From.Name == "" && change.To.Name != "":
		return FileDiffStatusAdded
	case change.From.Name != "" && change.To.Name == "":
		return FileDiffStatusDeleted
	case change.From.Name != change.To.Name:
		return FileDiffStatusRenamed
	default:
		return FileDiffStatusModified
	}
}

// GetFileMetadata는 파일 변경에 대한 메타데이터를 추출합니다
func GetFileMetadata(change *object.Change) map[string]interface{} {
	metadata := make(map[string]interface{})

	// 파일 모드 정보 추출
	if change.From.TreeEntry.Mode != 0 {
		metadata["old_mode"] = change.From.TreeEntry.Mode.String()
	}
	if change.To.TreeEntry.Mode != 0 {
		metadata["new_mode"] = change.To.TreeEntry.Mode.String()
	}

	// 파일 해시 정보 추출
	if !change.From.TreeEntry.Hash.IsZero() {
		metadata["old_hash"] = change.From.TreeEntry.Hash.String()
	}
	if !change.To.TreeEntry.Hash.IsZero() {
		metadata["new_hash"] = change.To.TreeEntry.Hash.String()
	}

	return metadata
}
