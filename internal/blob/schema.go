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

// BlobBinaryResponse는 바이너리 파일일 때 content 없이 메타데이터만 반환하는 응답입니다.
// 클라이언트는 is_binary 필드로 바이너리 여부를 분기합니다.
type BlobBinaryResponse struct {
	Success     bool   `json:"success"`      // 항상 true
	IsBinary    bool   `json:"is_binary"`    // 항상 true
	ContentType string `json:"content_type"` // MIME type
	Size        int    `json:"size"`         // 파일 크기 (bytes)
	Path        string `json:"path"`         // 파일 경로
}

// BlobErrorResponse represents a standardized error response for blob endpoints.
// 규약: 모든 JSON 에러 응답은 success=false, error 필수.
type BlobErrorResponse struct {
	Success bool   `json:"success"`           // MUST be false
	Error   string `json:"error"`             // 에러 메시지
	Code    string `json:"code,omitempty"`    // 선택
	Details string `json:"details,omitempty"` // 선택
}
