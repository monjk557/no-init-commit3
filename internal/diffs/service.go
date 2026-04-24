package diffs

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-enry/go-enry/v2"
)

// generatePatch는 커밋 범위에 대한 Git patch 형식 출력을 생성합니다.
// native git diff를 사용합니다 (go-git 대체).
func (h *DiffHandler) generatePatch(repoPath, base, head string) (string, error) {
	return nativeGeneratePatch(context.Background(), repoPath, base, head)
}

// calculateCommitDiff는 두 커밋 간의 실제 diff 계산을 수행합니다.
// native git diff를 사용합니다 (go-git 대체).
func (h *DiffHandler) calculateCommitDiff(ctx context.Context, repoPath, base, head string, contextLines int) (*DiffResponse, error) {
	return nativeCalculateCommitDiff(ctx, repoPath, base, head, contextLines)
}

// calculateSingleCommitDiff는 부모 커밋에 대한 단일 커밋의 diff 계산을 수행합니다.
// native git diff를 사용합니다 (go-git 대체).
func (h *DiffHandler) calculateSingleCommitDiff(ctx context.Context, repoPath, sha string, contextLines int) (*SingleCommitDiffResponse, error) {
	return nativeCalculateSingleCommitDiff(ctx, repoPath, sha, contextLines)
}

// hunkHeaderRegex parses @@ -startOld[,countOld] +startNew[,countNew] @@
var hunkHeaderRegex = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// parseHunkHeader returns startOld, countOld, startNew, countNew. count omitted means 1.
func parseHunkHeader(line string) (startOld, countOld, startNew, countNew int, ok bool) {
	matches := hunkHeaderRegex.FindStringSubmatch(line)
	if matches == nil {
		return 0, 0, 0, 0, false
	}
	startOld, _ = strconv.Atoi(matches[1])
	startNew, _ = strconv.Atoi(matches[3])
	countOld, countNew = 1, 1
	if matches[2] != "" {
		countOld, _ = strconv.Atoi(matches[2])
	}
	if matches[4] != "" {
		countNew, _ = strconv.Atoi(matches[4])
	}
	return startOld, countOld, startNew, countNew, true
}

// GenerateLineChangesFromPatch parses unified diff patch text and returns line changes with left/right line numbers and hunks.
// contextLines: 수정된 라인 위아래로 포함할 컨텍스트 라인 수(예: 3). 0이면 컨텍스트 trimming 없이 전부 포함.
func GenerateLineChangesFromPatch(patchText string, contextLines int) ([]LineChange, []Hunk, error) {
	lines := SplitLines(patchText)
	var lineChanges []LineChange
	var hunks []Hunk
	hunkIndex := -1
	leftLine, rightLine := 0, 0

	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		// @@ hunk header — diffLines에 type "HUNK"로 끼워 넣어 프론트가 한 번만 순회하며 렌더링 가능
		if startOld, countOld, startNew, countNew, ok := parseHunkHeader(line); ok {
			hunkIndex++
			hunks = append(hunks, Hunk{
				StartOld: startOld,
				CountOld: countOld,
				StartNew: startNew,
				CountNew: countNew,
				Header:   line,
			})
			lineChanges = append(lineChanges, LineChange{
				Type:           LineChangeTypeHunk,
				BlobLineNumber: intPtr(startOld),
				Text:           line,
				Left:           intPtr(startOld),
				Right:          intPtr(startNew),
				HunkIndex:      hunkIndex,
			})
			leftLine, rightLine = startOld, startNew
			continue
		}
		// "\ No newline at end of file" 감지 → 직전 라인에 경고 플래그 설정
		if strings.HasPrefix(line, "\\ ") {
			if len(lineChanges) > 0 {
				lineChanges[len(lineChanges)-1].DisplayNoNewLineWarning = true
			}
			continue
		}
		// skip non-content lines: "diff --git", "index", "similarity" etc.
		if len(line) > 0 && line[0] != ' ' && line[0] != '+' && line[0] != '-' {
			continue
		}
		// skip file headers: "--- a/..." and "+++ b/..."
		if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
			continue
		}
		var prefix byte = ' '
		if len(line) > 0 {
			prefix = line[0]
		}
		var changeType LineChangeType
		var leftPtr, rightPtr *int
		var blobLinePtr *int
		switch prefix {
		case '-':
			changeType = LineChangeTypeDel
			leftPtr = intPtr(leftLine)
			blobLinePtr = intPtr(leftLine)
			leftLine++
		case '+':
			changeType = LineChangeTypeAdd
			rightPtr = intPtr(rightLine)
			// ADD 라인은 blobLineNumber가 null
			rightLine++
		case ' ':
			changeType = LineChangeTypeContext
			leftPtr = intPtr(leftLine)
			rightPtr = intPtr(rightLine)
			blobLinePtr = intPtr(leftLine)
			leftLine++
			rightLine++
		default:
			continue
		}

		idx := hunkIndex
		if idx < 0 {
			idx = 0
		}
		// Text는 마커 접두사(+/-/공백) 제외한 순수 라인 내용만 포함.
		// 변경 종류는 Type 필드로 구분 (ADD/DELETE/CONTEXT).
		lineChanges = append(lineChanges, LineChange{
			Type:           changeType,
			BlobLineNumber: blobLinePtr,
			Text:           line[1:],
			Left:           leftPtr,
			Right:          rightPtr,
			HunkIndex:      idx,
		})
	}

	if contextLines <= 0 {
		return lineChanges, hunks, nil
	}
	// 수정된 라인 위아래 contextLines줄만 남기기
	return trimContextAroundChanges(lineChanges, contextLines), hunks, nil
}

// trimContextAroundChanges keeps only context lines within contextLines of a change (add/del).
func trimContextAroundChanges(changes []LineChange, contextLines int) []LineChange {
	if len(changes) == 0 {
		return changes
	}
	keep := make([]bool, len(changes))
	for i := range changes {
		if changes[i].Type == LineChangeTypeHunk {
			keep[i] = true
			continue
		}
		if changes[i].Type != LineChangeTypeContext {
			keep[i] = true
			continue
		}
		// context line: keep if within contextLines of any change
		for d := 1; d <= contextLines; d++ {
			if i-d >= 0 && (changes[i-d].Type == LineChangeTypeAdd || changes[i-d].Type == LineChangeTypeDel) {
				keep[i] = true
				break
			}
			if i+d < len(changes) && (changes[i+d].Type == LineChangeTypeAdd || changes[i+d].Type == LineChangeTypeDel) {
				keep[i] = true
				break
			}
		}
	}
	var out []LineChange
	for i, k := range keep {
		if !k {
			continue
		}
		out = append(out, changes[i])
	}
	return out
}

// GenerateLineChanges는 라인별 diff 정보를 생성합니다 (기존 API 호환).
func (h *DiffHandler) GenerateLineChanges(lines []string, contextLines int) []LineChange {
	return GenerateLineChanges(lines, contextLines)
}

// GenerateLineChanges는 라인별 diff 정보를 생성하는 공통 함수입니다 (patch 문자열 없이 사용 시).
func GenerateLineChanges(lines []string, contextLines int) []LineChange {
	var lineChanges []LineChange

	for _, line := range lines {
		if len(line) == 0 {
			continue
		}

		var changeType LineChangeType
		var text string
		switch line[0] {
		case '+':
			changeType = LineChangeTypeAdd
			text = line[1:]
		case '-':
			changeType = LineChangeTypeDel
			text = line[1:]
		case ' ':
			changeType = LineChangeTypeContext
			text = line[1:]
		default:
			// 접두사가 없는 비정상 라인은 그대로 CONTEXT로 보존 (후처리 호환용).
			changeType = LineChangeTypeContext
			text = line
		}

		lineChanges = append(lineChanges, LineChange{
			Type: changeType,
			Text: text,
		})
	}

	return lineChanges
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

// CalculateBranchDiff는 merge base 분석을 통해 두 브랜치 간의 diff 계산을 수행합니다.
// native git diff를 사용합니다 (go-git 대체).
func (h *DiffHandler) CalculateBranchDiff(ctx context.Context, repoPath, baseBranch, headBranch string, contextLines int) (*DiffResponse, error) {
	return nativeCalculateBranchDiff(ctx, repoPath, baseBranch, headBranch, contextLines)
}

// CalculateMergeBaseDiff는 merge-base 기준으로 두 ref 간의 diff를 계산합니다.
// Change Request 페이지에서 source 브랜치가 도입한 변경만 보여주기 위한 package-level 함수입니다.
// baseBranch = merge INTO (target), headBranch = merge FROM (source).
func CalculateMergeBaseDiff(ctx context.Context, repoPath, baseBranch, headBranch string, contextLines int) (*DiffResponse, error) {
	return nativeCalculateBranchDiff(ctx, repoPath, baseBranch, headBranch, contextLines)
}

// calculateFileDiff는 두 참조 간의 특정 파일에 대한 diff를 계산합니다.
// native git diff를 사용합니다 (go-git 대체).
func (h *DiffHandler) calculateFileDiff(ctx context.Context, repoPath, filePath, base, head string, contextLines int, _ string) (*DiffResponse, error) {
	return nativeCalculateFileDiff(ctx, repoPath, filePath, base, head, contextLines)
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
