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

// TreeEntriesResponse는 트리 항목 목록 요청에 대한 응답을 나타냅니다.
// 성공 여부와 트리 항목들의 배열을 포함합니다.
type TreeEntriesResponse struct {
	Success bool        `json:"success"`
	Data    []TreeEntry `json:"data"`
}

// TreeEntriesRecursiveResponse는 재귀적 트리 항목 목록 요청에 대한 응답을 나타냅니다.
// 성공 여부와 경로별 트리 항목들의 맵을 포함합니다.
type TreeEntriesRecursiveResponse struct {
	Success bool                   `json:"success"`
	Data    map[string][]TreeEntry `json:"data"`
}
