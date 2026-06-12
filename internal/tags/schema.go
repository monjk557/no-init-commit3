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

// GetTagsRequest 는 GET /tags 의 query parameter 를 나타낸다.
//
// 브랜치 list(GET /branches)의 GetBranchesRequest 와 동일한 패턴 —
// RegisterGetWithQuery 로 등록하여 wrapper 가 form 태그를 reflect 하고 spec
// generator 가 OpenAPI query parameter (in: query) 로 expand 한다. orchestrator
// auto-gen client 가 limit/skip/search/sort 인자를 누락하지 않도록 노출하기 위함.
type GetTagsRequest struct {
	Limit  string `form:"limit"`
	Skip   string `form:"skip"`
	Search string `form:"search"` // 태그 이름 부분일치 필터 (대소문자 무시), 브랜치 list 와 동일
	Sort   string `form:"sort"`   // "newest" (기본) | "oldest" | "name"
}

// CreateTagRequest는 태그 생성을 위한 요청 구조체입니다.
type CreateTagRequest struct {
	Name      string  `json:"name" binding:"required"` // 태그 이름 (필수)
	Hash      string  `json:"hash" binding:"required"` // 커밋 해시 (필수)
	Message   string  `json:"message,omitempty"`       // 태그 메시지 (Annotated Tag용)
	Tagger    *Tagger `json:"tagger,omitempty"`        // 태그 작성자 정보 (Annotated Tag용)
	Annotated bool    `json:"annotated,omitempty"`     // Annotated Tag 여부 (기본값: false = Lightweight Tag)
}

type CreateTagResponse struct {
	Success bool `json:"success"`
	Tag     *Tag `json:"tag"`
}

// DeleteTagResponse 는 DELETE /repos/{owner}/{repo}/tags/{tag} 의 응답.
// Tag 는 삭제된 태그의 이름 (string) — runtime 응답과 일치 (이전 정의의
// `*Tag` 는 path 에서 참조되지 않은 orphan 이었음).
type DeleteTagResponse struct {
	Success bool   `json:"success"`
	Tag     string `json:"tag"`
}

// BulkDeleteTagsRequest는 대량 태그 삭제를 위한 요청을 나타냅니다.
type BulkDeleteTagsRequest struct {
	Tags []string `json:"tags" binding:"required,min=1"`
}

// BulkDeleteTagResult는 개별 태그 삭제 결과를 나타냅니다.
type BulkDeleteTagResult struct {
	TagName string `json:"tag_name"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// BulkDeleteTagsResponse는 대량 태그 삭제 응답을 나타냅니다.
type BulkDeleteTagsResponse struct {
	Success   bool                  `json:"success"`
	Total     int                   `json:"total"`
	Succeeded int                   `json:"succeeded"`
	Failed    int                   `json:"failed"`
	Results   []BulkDeleteTagResult `json:"results"`
}
