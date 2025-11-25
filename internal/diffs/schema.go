package diffs

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

// FileDiff는 단일 파일의 변경사항을 나타냅니다
type FileDiff struct {
	Path      string                 `json:"path"`
	Status    FileDiffStatus         `json:"status"` // "added", "deleted", "modified", "renamed"
	Additions int                    `json:"additions"`
	Deletions int                    `json:"deletions"`
	Changes   []LineChange           `json:"changes,omitempty"`
	IsBinary  bool                   `json:"is_binary"`
	OldPath   string                 `json:"old_path,omitempty"` // 이름 변경시 사용
	Metadata  map[string]interface{} `json:"metadata,omitempty"` // 파일 메타데이터 (해시, 모드)
}

// LineChange는 diff의 단일 라인 변경을 나타냅니다
type LineChange struct {
	Type    LineChangeType `json:"type"`    // "add", "del", "context"
	LineNum int            `json:"line"`    // 파일 내 라인 번호
	Content string         `json:"content"` // diff 접두사가 포함된 라인 내용
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
)
