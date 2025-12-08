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
