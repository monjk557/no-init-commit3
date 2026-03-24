package tree

// TreeEntry는 Git 트리의 개별 항목(파일 또는 디렉토리)을 나타냅니다.
// 항목의 유형, 이름, 전체 경로를 포함합니다.

type EntryType string

const (
	// EntryTypeBlob - 파일
	EntryTypeBlob   EntryType = "blob"
	EntryTypeTree   EntryType = "tree"
	EntryTypeCommit EntryType = "commit"
)

type TreeEntry struct {
	Type string `json:"type"` // "tree" or "blob"
	Name string `json:"name"` // Entry name
	Path string `json:"path"` // Full path
}

// TreeEntriesResponse는 트리 항목 목록 요청에 대한 응답을 나타냅니다 (flat, data 래퍼 없음).
type TreeEntriesResponse struct {
	Success bool        `json:"success"`
	Entries []TreeEntry `json:"entries"`
}

// TreeEntriesRecursiveResponse는 재귀적 트리 항목 목록 요청에 대한 응답을 나타냅니다 (flat).
type TreeEntriesRecursiveResponse struct {
	Success bool                   `json:"success"`
	Tree    map[string][]TreeEntry `json:"tree"`
}

// TreeSearchResponse는 파일명 검색 요청에 대한 응답을 나타냅니다.
type TreeSearchResponse struct {
	Success    bool        `json:"success"`
	Query      string      `json:"query"`
	Entries    []TreeEntry `json:"entries"`
	TotalCount int         `json:"total_count"`
	Truncated  bool        `json:"truncated"`
}

// 파일 검색 기본 제한값
const (
	DefaultSearchLimit = 100
	MaxSearchLimit     = 1000
)
