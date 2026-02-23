package diffs

import (
	"fmt"
)

// 공통 요청 검증 및 응답 유틸리티

// RequestParams는 diff 엔드포인트의 공통 요청 파라미터를 보유합니다
type RequestParams struct {
	Owner string
	Repo  string
	Base  string
	Head  string
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
	Success       bool            `json:"success"`
	Base          string          `json:"base"`
	Head          string          `json:"head"`
	Files         []FileDiff      `json:"files"`
	Stats         DiffStats       `json:"stats"`
	Commits       *CommitStats    `json:"commits,omitempty"`         // 브랜치 diff용
	CommitsByDate []CommitsByDate `json:"commits_by_date,omitempty"` // 날짜별 커밋 목록
	MergeBase     string          `json:"merge_base,omitempty"`      // 브랜치 diff용
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

// FileDiff는 단일 파일의 변경사항을 나타냅니다
type FileDiff struct {
	Path      string                 `json:"path"`
	Status    FileDiffStatus         `json:"status"` // "added", "deleted", "modified", "renamed"
	Additions int                    `json:"additions"`
	Deletions int                    `json:"deletions"`
	Changes   []LineChange           `json:"changes"` // omitempty 제거: 필드는 항상 포함 (nil이면 null로 표시)
	Hunks     []Hunk                 `json:"hunks"`   // hunk 목록. LineChange.hunk_index로 참조 (항상 포함, 없으면 [])
	IsBinary  bool                   `json:"is_binary"`
	OldPath   string                 `json:"old_path,omitempty"` // 이름 변경시 사용
	Metadata  map[string]interface{} `json:"metadata,omitempty"` // 파일 메타데이터 (해시, 모드)
}

// LineChange는 diff의 단일 라인 변경을 나타냅니다. 라인 번호(left_line/right_line)로 접근 가능하며, hunk_index로 Hunks를 참조합니다.
type LineChange struct {
	Type         LineChangeType         `json:"type"`              // "add", "del", "context", "hunk"
	LeftLineNum  int                    `json:"left_line"`         // 구 파일 라인 번호. 추가 라인이면 0
	RightLineNum int                    `json:"right_line"`        // 신 파일 라인 번호. 삭제 라인이면 0
	HunkIndex    int                    `json:"hunk_index"`        // 이 라인이 속한 hunk 인덱스 (FileDiff.Hunks 참조)
	Content      string                 `json:"content"`           // diff 접두사(공백/+/-)가 포함된 라인 내용
	Meta         map[string]interface{} `json:"meta,omitempty"`    // 라인 단위 메타 (필요 시)
	Comment      string                 `json:"comment,omitempty"` // 리뷰용 코멘트 (비어 있으면 생략)
}

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

// LineChangeType은 diff 라인 변경의 타입을 나타냅니다
type LineChangeType string

const (
	LineChangeTypeAdd     LineChangeType = "add"     // 라인 추가
	LineChangeTypeDel     LineChangeType = "del"     // 라인 삭제
	LineChangeTypeContext LineChangeType = "context" // 컨텍스트 라인 (변경 없음)
	LineChangeTypeHunk    LineChangeType = "hunk"    // hunk 헤더. content에 "@@ -n,m +n,m @@" — changes[] 한 번 순회로 렌더
)

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
