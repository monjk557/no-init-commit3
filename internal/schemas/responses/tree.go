// Package responses는 Git 트리(디렉토리 구조) 관련 HTTP 응답의 JSON 스키마를 정의합니다.
// Git 저장소의 디렉토리 구조와 파일 목록을 반환하는 데이터 구조체들을 포함합니다.
package responses

// TreeEntry는 Git 트리의 개별 항목(파일 또는 디렉토리)을 나타냅니다.
// 항목의 유형, 이름, 전체 경로를 포함합니다.
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
