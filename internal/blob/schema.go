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

// BlobErrorResponse represents a standardized error response for blob endpoints
type BlobErrorResponse struct {
	Error   string `json:"error"`
	Code    string `json:"code,omitempty"`
	Details string `json:"details,omitempty"`
}
