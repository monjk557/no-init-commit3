package blob

// BlobRequestParams는 blob 엔드포인트의 요청 매개변수들을 포함합니다.
// 소유자, 저장소, 파일 경로, 참조 및 사용자 ID를 포함합니다.
type BlobRequestParams struct {
	Owner  string
	Repo   string
	Path   string
	Ref    string
	UserID string
}

// BlobErrorResponse represents a standardized error response for blob endpoints.
// 규약: 모든 JSON 에러 응답은 success=false, error 필수.
type BlobErrorResponse struct {
	Success bool   `json:"success"`           // MUST be false
	Error   string `json:"error"`             // 에러 메시지
	Code    string `json:"code,omitempty"`    // 선택
	Details string `json:"details,omitempty"` // 선택
}
