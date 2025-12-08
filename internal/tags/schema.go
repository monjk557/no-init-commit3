package tags

import "time"

type Tag struct {
	Name        string  `json:"name"`
	Hash        string  `json:"hash"`
	IsAnnotated bool    `json:"is_annotated,omitempty"`
	Message     string  `json:"message,omitempty"`
	Tagger      *Tagger `json:"tagger,omitempty"`
}

type Tagger struct {
	Name  string    `json:"name"`
	Email string    `json:"email"`
	Date  time.Time `json:"date"`
}

// TagsResponse는 태그 목록 응답 구조를 나타냅니다.
type TagsResponse struct {
	Success    bool           `json:"success"`
	Message    string         `json:"message"`
	Results    []Tag          `json:"results"`
	Pagination PaginationInfo `json:"pagination"`
}

// PaginationInfo는 태그 응답의 페이지네이션 정보를 나타냅니다.
// Python PaginatedResponse와 호환되는 필드들을 포함합니다.
type PaginationInfo struct {
	Count       int     `json:"count"`        // 전체 항목 수
	Next        *string `json:"next"`         // 다음 페이지 URL
	Previous    *string `json:"previous"`     // 이전 페이지 URL
	CurrentPage int     `json:"current_page"` // 현재 페이지 번호
	TotalPages  int     `json:"total_pages"`  // 전체 페이지 수
	PageSize    int     `json:"page_size"`    // 페이지당 항목 수
}

// 기본 페이지네이션 값들
const (
	DefaultTagsLimit = 30
	MaxTagsLimit     = 100
	MinTagsLimit     = 1
)

// CreateTagRequest는 태그 생성을 위한 요청 구조체입니다.
type CreateTagRequest struct {
	Name      string  `json:"name" binding:"required"` // 태그 이름 (필수)
	Hash      string  `json:"hash" binding:"required"` // 커밋 해시 (필수)
	Message   string  `json:"message,omitempty"`       // 태그 메시지 (Annotated Tag용)
	Tagger    *Tagger `json:"tagger,omitempty"`        // 태그 작성자 정보 (Annotated Tag용)
	Annotated bool    `json:"annotated,omitempty"`     // Annotated Tag 여부 (기본값: false = Lightweight Tag)
}

// UpdateTagRequest는 태그 업데이트를 위한 요청 구조체입니다.
type UpdateTagRequest struct {
	Hash      string  `json:"hash" binding:"required"` // 새로운 커밋 해시 (필수)
	Message   string  `json:"message,omitempty"`       // 태그 메시지 (Annotated Tag로 변경 시)
	Tagger    *Tagger `json:"tagger,omitempty"`        // 태그 작성자 정보 (Annotated Tag로 변경 시)
	Annotated bool    `json:"annotated,omitempty"`     // Annotated Tag 여부
}
