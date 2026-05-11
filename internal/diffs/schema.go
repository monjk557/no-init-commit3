package diffs

import (
	"fmt"

	"github.com/collabops/git-executor/internal/repositories"
)

// 공통 요청 검증 및 응답 유틸리티

// DiffMode controls how (base, head)가 서로 어떻게 비교되는지 결정합니다.
//
//   - DiffModeMergeBase: effective_base = merge-base(base, head). 3-dot 시맨틱과 동일.
//     CR/branch 비교 흐름의 기본값 — 트리(file list)와 단일 파일 diff가 같은 변경 집합을 보도록 정렬.
//   - DiffModeDirect: effective_base = base. 2-dot 직접 비교.
//     커밋 vs 커밋, patch 등 base/head를 그대로 비교해야 하는 케이스에 사용.
type DiffMode string

const (
	DiffModeMergeBase DiffMode = "merge_base"
	DiffModeDirect    DiffMode = "direct"
)

// IsValid는 DiffMode가 알려진 값인지 검사합니다.
func (m DiffMode) IsValid() bool {
	switch m {
	case DiffModeMergeBase, DiffModeDirect:
		return true
	default:
		return false
	}
}

// RequestParams는 diff 엔드포인트의 공통 요청 파라미터를 보유합니다
type RequestParams struct {
	Owner string
	Repo  string
	Base  string
	Head  string
}

// BranchDiffQueryRequest 는 GET /diff/branches 의 query parameter 를
// 나타낸다 — base/head 외 page, page_size 등은 handler 본문에서 c.Query 로
// 읽으며 본 struct 에는 web 이 실제로 전달하는 base/head 만 노출 (Issue-2317
// 후속).
type BranchDiffQueryRequest struct {
	Base string `form:"base" binding:"required"`
	Head string `form:"head" binding:"required"`
}

// FileDiffRequest 는 GET /diff/file 의 query parameter 를 나타낸다 —
// full_context / diff_mode / type / page / page_size 는 handler 본문에서
// c.Query 로 읽으며 본 struct 에는 web 이 실제로 전달하는 path/base/head 만
// 노출 (Issue-2317 후속).
type FileDiffRequest struct {
	Path string `form:"path" binding:"required"`
	Base string `form:"base" binding:"required"`
	Head string `form:"head" binding:"required"`
}

// ExpandContextRequest 는 GET /diff/expand-context 의 query parameter 를
// 나타낸다.
//
// 두 가지 호출 모드 (web 사용):
//   - "from_old/to_old/from_new" 직접 범위 지정 — base server-side mode
//   - "start_old/start_new/count/direction" anchor + direction — web 의
//     hunk expansion UI 가 사용하는 모드
//
// from_*/to_* 와 start_* 의 mutual exclusivity, side/diff_mode/type 등
// 보조 파라미터의 검증·default 는 handler 본문에서 수행한다 — 기존 custom
// error message 보존.
type ExpandContextRequest struct {
	Path      string `form:"path" binding:"required"`
	Base      string `form:"base" binding:"required"`
	Head      string `form:"head" binding:"required"`
	StartOld  string `form:"start_old"`
	StartNew  string `form:"start_new"`
	Count     string `form:"count"`
	Direction string `form:"direction"`
}

// PaginationParams는 페이지네이션 파라미터를 보유합니다
type PaginationParams struct {
	Page     int  `json:"page"`
	PageSize int  `json:"page_size"`
	Total    int  `json:"total"`
	HasMore  bool `json:"has_more"`
}

// PaginatedDiffResponse는 페이지네이션이 포함된 DiffResponse를 확장합니다
type PaginatedDiffResponse struct {
	Success bool `json:"success"`
	*DiffResponse
	Pagination *PaginationParams `json:"pagination,omitempty"`
}

// DiffResponse는 diff 엔드포인트의 응답 구조를 나타냅니다
type DiffResponse struct {
	Success bool       `json:"success"`
	Base    string     `json:"base"`
	Head    string     `json:"head"`
	Files   []FileDiff `json:"files"`
	Stats   DiffStats  `json:"stats"`
	// DiffMode는 이 응답을 만드는 데 사용된 비교 모드입니다 (CR file list와의 정렬 검증용).
	DiffMode string `json:"diff_mode,omitempty"`
	// EffectiveBase는 실제 git 호출에서 base로 사용된 hash입니다.
	// merge_base 모드에서는 MergeBase와 동일, direct 모드에서는 입력 base의 resolved hash.
	EffectiveBase string          `json:"effective_base,omitempty"`
	Commits       *CommitStats    `json:"commits,omitempty"`         // 브랜치 diff용
	CommitsByDate []CommitsByDate `json:"commits_by_date,omitempty"` // 날짜별 커밋 목록
	MergeBase     string          `json:"merge_base,omitempty"`      // base/head의 merge-base hash (정보용)
	Truncated     bool            `json:"truncated,omitempty"`       // 응답 크기 제한으로 인해 일부 파일이 제외되었는지 여부
}

// SingleCommitDiffResponse는 단일 커밋 diff 엔드포인트의 응답 구조를 나타냅니다
type SingleCommitDiffResponse struct {
	Success bool            `json:"success"`
	Commit  *CommitMetadata `json:"commit"`
	*DiffResponse
}

// CommitMetadata는 커밋 정보를 나타냅니다
type CommitMetadata struct {
	SHA     string   `json:"sha"`
	Author  string   `json:"author"`
	Email   string   `json:"email"`
	Date    string   `json:"date"`
	Message string   `json:"message"`
	Parents []string `json:"parents"`
}

// CommitStats는 브랜치 비교를 위한 커밋 수 차이를 나타냅니다
type CommitStats struct {
	Ahead  int `json:"ahead"`  // base에 없는 head의 커밋들
	Behind int `json:"behind"` // head에 없는 base의 커밋들
}

// CommitsByDate는 날짜별로 그룹화된 커밋 목록을 나타냅니다
type CommitsByDate struct {
	Date    string           `json:"date"`    // YYYY-MM-DD 형식의 날짜
	Commits []CommitMetadata `json:"commits"` // 해당 날짜의 커밋 목록
}

// Hunk는 unified diff의 한 덩어리(@@ ... @@)를 나타냅니다. Header는 프론트에서 그대로 표시·context pagination 등에 사용.
type Hunk struct {
	StartOld int    `json:"start_old"` // 구 파일에서 hunk 시작 라인
	CountOld int    `json:"count_old"` // 구 파일에서 hunk 라인 수
	StartNew int    `json:"start_new"` // 신 파일에서 hunk 시작 라인
	CountNew int    `json:"count_new"` // 신 파일에서 hunk 라인 수
	Header   string `json:"header"`    // 원본 라인 e.g. "@@ -78,2 +78,2 @@" — 그대로 표시·버튼용
}

// SubmoduleChange는 gitlink (mode 160000) entry 의 commit pin 변화를 나타냅니다.
// 일반 파일 diff 와 달리 line-level changes 가 의미가 없고 — 응답은 from/to commit
// SHA + (가능하면) .gitmodules 의 url 만 노출됩니다. 클라이언트는 이 페이로드로
// "submodule pin 이 어디서 어디로 갔는지" 를 단일 카드로 렌더링할 수 있습니다.
type SubmoduleChange struct {
	FromCommit string                      `json:"from_commit"`      // base 시점 pinned SHA (added 일 때 빈 문자열)
	ToCommit   string                      `json:"to_commit"`        // head 시점 pinned SHA (deleted 일 때 빈 문자열)
	URL        string                      `json:"url,omitempty"`    // .gitmodules 에서 발견된 url (없으면 빈 문자열)
	Parsed     *repositories.ParsedRepoURL `json:"parsed,omitempty"` // url 파싱 성공 시 (host/owner/repo)
}

// FileDiff는 단일 파일의 변경사항을 나타냅니다
type FileDiff struct {
	Path            string           `json:"path"`
	Status          FileDiffStatus   `json:"status"` // "added", "deleted", "modified", "renamed"
	Additions       int              `json:"additions"`
	Deletions       int              `json:"deletions"`
	Changes         []LineChange     `json:"diffLines"` // omitempty 제거: 필드는 항상 포함 (nil이면 null로 표시)
	Hunks           []Hunk           `json:"-"`         // 내부 전용 (expand-context 등), JSON 응답에서 제외
	IsBinary        bool             `json:"is_binary"`
	LoadStatus      FileLoadStatus   `json:"loadStatus"`                 // 파일 diff 로드 상태: loaded, collapsed, too_large, binary, submodule
	OldPath         string           `json:"old_path,omitempty"`         // 이름 변경시 사용
	SubmoduleChange *SubmoduleChange `json:"submodule_change,omitempty"` // gitlink 변경 시에만 채워짐
}

// LineChange는 diff의 단일 라인 변경을 나타냅니다.
type LineChange struct {
	Type                    LineChangeType `json:"type"`                    // "ADD", "DELETE", "CONTEXT", "HUNK"
	BlobLineNumber          *int           `json:"blobLineNumber"`          // old 파일 라인 번호 (ADD일 경우 null)
	DisplayNoNewLineWarning bool           `json:"displayNoNewLineWarning"` // "\ No newline at end of file" 경고
	Text                    string         `json:"text"`                    // 라인 내용 (마커 접두사 없음 — 변경 종류는 Type 필드로 분기). HUNK 타입일 경우 "@@ -n,m +n,m @@" 헤더 전체.
	Left                    *int           `json:"left"`                    // 구 파일 라인 번호 (ADD일 경우 null)
	Right                   *int           `json:"right"`                   // 신 파일 라인 번호 (DELETE일 경우 null)
	HunkIndex               int            `json:"-"`                       // 내부용: 이 라인이 속한 hunk 인덱스
}

// intPtr는 int 포인터를 생성하는 헬퍼 함수입니다.
func intPtr(n int) *int { return &n }

// DiffStats는 diff의 전체 통계를 나타냅니다
type DiffStats struct {
	TotalAdditions int `json:"total_additions"`
	TotalDeletions int `json:"total_deletions"`
	FilesChanged   int `json:"files_changed"`
}

// FileDiffStatus는 파일 변경 상태를 나타냅니다
type FileDiffStatus string

const (
	FileDiffStatusAdded    FileDiffStatus = "added"    // 파일 추가됨
	FileDiffStatusDeleted  FileDiffStatus = "deleted"  // 파일 삭제됨
	FileDiffStatusModified FileDiffStatus = "modified" // 파일 수정됨
	FileDiffStatusRenamed  FileDiffStatus = "renamed"  // 파일 이름 변경됨
)

// FileLoadStatus는 파일 diff의 로드 상태를 나타냅니다
type FileLoadStatus string

const (
	FileLoadStatusLoaded    FileLoadStatus = "loaded"    // Changes[] 포함됨 — 바로 렌더
	FileLoadStatusCollapsed FileLoadStatus = "collapsed" // MaxFilesWithChanges 초과로 미로드 — /diff/file 개별 조회 가능
	FileLoadStatusTooLarge  FileLoadStatus = "too_large" // MaxChangedLines/MaxFileLines 초과 — 표시 불가
	FileLoadStatusBinary    FileLoadStatus = "binary"    // 바이너리 파일
	FileLoadStatusSubmodule FileLoadStatus = "submodule" // gitlink (mode 160000) — line diff 대신 submodule_change 사용
)

// LineChangeType은 diff 라인 변경의 타입을 나타냅니다
type LineChangeType string

const (
	LineChangeTypeAdd     LineChangeType = "ADD"     // 라인 추가
	LineChangeTypeDel     LineChangeType = "DELETE"  // 라인 삭제
	LineChangeTypeContext LineChangeType = "CONTEXT" // 컨텍스트 라인 (변경 없음)
	LineChangeTypeHunk    LineChangeType = "HUNK"    // hunk 헤더. text에 "@@ -n,m +n,m @@" — diffLines 한 번 순회로 렌더
)

// ExpandContextResponse는 hunk 사이 숨겨진 context 라인 확장 응답입니다.
type ExpandContextResponse struct {
	Success  bool         `json:"success"`
	Lines    []LineChange `json:"lines"`     // CONTEXT 타입 LineChange 목록
	Path     string       `json:"path"`      // 파일 경로
	StartOld int          `json:"start_old"` // 반환된 첫 번째 old 라인 번호
	StartNew int          `json:"start_new"` // 반환된 첫 번째 new 라인 번호
	Count    int          `json:"count"`     // 반환된 라인 수
}

// DiffSizeLimits는 diff 크기 제한을 정의합니다
const (
	// MaxFileSize는 단일 파일의 최대 크기 (5MB)
	MaxFileSize = 5 * 1024 * 1024
	// MaxFileLines는 단일 파일의 최대 라인 수
	MaxFileLines = 50000
	// MaxTotalFiles는 전체 diff의 최대 파일 수 (메타데이터만 포함하는 경우 제한 없음)
	MaxTotalFiles = 100000
	// MaxTotalLines는 전체 diff의 최대 라인 수
	MaxTotalLines = 100000
	// MaxPatchSize는 단일 patch의 최대 크기 (10MB)
	MaxPatchSize = 10 * 1024 * 1024
	// MaxChangedLines는 단일 파일의 최대 변경 라인 수 (additions + deletions)
	// 이 값을 초과하면 해당 파일의 diff 내용(Changes)을 로드하지 않음
	MaxChangedLines = 10000
	// MaxFilesWithChanges는 Changes(라인별 diff)를 포함할 최대 파일 수
	// 나머지 파일들은 메타데이터만 포함하고 Changes는 nil로 설정
	MaxFilesWithChanges = 10
)

// DiffTooLargeError는 diff가 너무 커서 표시할 수 없을 때 사용하는 에러입니다
type DiffTooLargeError struct {
	Reason string
}

func (e *DiffTooLargeError) Error() string {
	return fmt.Sprintf("This diff is too large to display: %s", e.Reason)
}

// DiffFileSearchResult는 diff에서 검색된 파일 항목입니다.
type DiffFileSearchResult struct {
	Path      string         `json:"path"`
	OldPath   string         `json:"old_path,omitempty"`
	Status    FileDiffStatus `json:"status"`
	Additions int            `json:"additions"`
	Deletions int            `json:"deletions"`
	IsBinary  bool           `json:"is_binary"`
}

// DiffFileSearchResponse는 diff 파일 검색 응답입니다.
type DiffFileSearchResponse struct {
	Success bool   `json:"success"`
	Query   string `json:"query"`
	// Type은 deprecated된 별칭입니다. 새 호출자는 DiffMode를 사용하세요. ("commit"/"branch" 매핑)
	Type          string                 `json:"type"`
	DiffMode      string                 `json:"diff_mode,omitempty"`
	Base          string                 `json:"base"`
	Head          string                 `json:"head"`
	EffectiveBase string                 `json:"effective_base,omitempty"`
	MergeBase     string                 `json:"merge_base,omitempty"`
	Files         []DiffFileSearchResult `json:"files"`
	TotalCount    int                    `json:"total_count"`
}

// ========================================================================
// ReviewComment line-anchor classification (Code Review 2.0 Phase B).
// See classify.go for implementation.
// ========================================================================

// AnchorStatus는 ReviewComment 라인 anchor의 상태를 나타냅니다.
type AnchorStatus string

const (
	// AnchorStatusActive: anchor 라인 범위가 base..head 사이에 변경되지 않음.
	AnchorStatusActive AnchorStatus = "active"
	// AnchorStatusStale: anchor 라인 범위 내에 hunk가 존재함 (내용 변경).
	AnchorStatusStale AnchorStatus = "stale"
	// AnchorStatusOrphaned: 파일 자체가 삭제/바이너리 변환됨. anchor 의미 상실.
	AnchorStatusOrphaned AnchorStatus = "orphaned"
)

// AnchorRequest는 단일 ReviewComment의 anchor 정보입니다.
type AnchorRequest struct {
	ID        string `json:"id"`         // 결과 매칭용 식별자 (예: ReviewComment.id)
	Path      string `json:"path"`       // anchor 작성 시점 파일 경로 (base 기준)
	StartLine int    `json:"start_line"` // 1-based, inclusive
	EndLine   int    `json:"end_line"`   // 1-based, inclusive
}

// AnchorResult는 classify 결과 단일 항목입니다.
type AnchorResult struct {
	ID      string       `json:"id"`
	Status  AnchorStatus `json:"status"`
	NewPath string       `json:"new_path,omitempty"` // rename 시 head 기준 새 경로
}

// ClassifyAnchorsRequest는 POST /diff/classify-anchors 요청 바디입니다.
type ClassifyAnchorsRequest struct {
	BaseSHA string          `json:"base_sha" binding:"required"`
	HeadSHA string          `json:"head_sha" binding:"required"`
	Anchors []AnchorRequest `json:"anchors" binding:"required"`
}

// ClassifyAnchorsResponse는 POST /diff/classify-anchors 응답입니다.
type ClassifyAnchorsResponse struct {
	Success bool           `json:"success"`
	BaseSHA string         `json:"base_sha"`
	HeadSHA string         `json:"head_sha"`
	Results []AnchorResult `json:"results"`
}
