// Package responses는 Git blob(파일 내용) 관련 HTTP 응답의 JSON 스키마를 정의합니다.
// Git 저장소에서 파일 내용을 조회한 결과를 반환하는 데이터 구조체를 포함합니다.
package responses

// BlobResponse는 blob(파일 내용) 요청에 대한 응답을 나타냅니다.
// 성공 여부와 파일의 내용을 포함합니다.
type BlobResponse struct {
	Success bool   `json:"success"`
	Content string `json:"content"`
}
