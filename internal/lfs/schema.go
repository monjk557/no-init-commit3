package lfs

import "time"

// BatchRequest는 Git LFS Batch API 요청 구조체입니다.
// https://github.com/git-lfs/git-lfs/blob/main/docs/api/batch.md
type BatchRequest struct {
	Operation string       `json:"operation"` // "upload" 또는 "download"
	Transfers []string     `json:"transfers"` // ["basic"] (기본값)
	Objects   []ObjectSpec `json:"objects"`
	Ref       *RefSpec     `json:"ref,omitempty"`
}

// RefSpec은 LFS 요청의 Git 참조 정보입니다.
type RefSpec struct {
	Name string `json:"name"`
}

// ObjectSpec은 LFS 오브젝트 식별 정보입니다.
type ObjectSpec struct {
	OID  string `json:"oid"`  // SHA-256 hex string
	Size int64  `json:"size"` // 바이트 단위
}

// BatchResponse는 Git LFS Batch API 응답 구조체입니다.
type BatchResponse struct {
	Transfer string         `json:"transfer"`
	Objects  []ObjectResult `json:"objects"`
}

// ObjectResult는 개별 오브젝트의 처리 결과입니다.
type ObjectResult struct {
	OID           string            `json:"oid"`
	Size          int64             `json:"size"`
	Authenticated bool              `json:"authenticated,omitempty"`
	Actions       map[string]Action `json:"actions,omitempty"`
	Error         *ObjectError      `json:"error,omitempty"`
}

// Action은 클라이언트가 수행할 HTTP 액션 (upload/download/verify) 정보입니다.
type Action struct {
	Href      string            `json:"href"`
	Header    map[string]string `json:"header,omitempty"`
	ExpiresIn int               `json:"expires_in"` // 초 단위
	ExpiresAt time.Time         `json:"expires_at,omitempty"`
}

// ObjectError는 개별 오브젝트 처리 실패 시 에러 정보입니다.
type ObjectError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// VerifyRequest는 업로드 완료 확인 요청입니다.
type VerifyRequest struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

// LockRequest는 파일 잠금 생성 요청입니다.
type LockRequest struct {
	Path string   `json:"path"`
	Ref  *RefSpec `json:"ref,omitempty"`
}

// LockResponse는 파일 잠금 생성 응답입니다.
type LockResponse struct {
	Lock    LockInfo `json:"lock"`
	Message string   `json:"message,omitempty"`
}

// LockInfo는 잠금 정보입니다.
type LockInfo struct {
	ID       string    `json:"id"`
	Path     string    `json:"path"`
	LockedAt time.Time `json:"locked_at"`
	Owner    *Owner    `json:"owner,omitempty"`
}

// Owner는 잠금 소유자 정보입니다.
type Owner struct {
	Name string `json:"name"`
}

// LockListResponse는 잠금 목록 응답입니다.
type LockListResponse struct {
	Locks      []LockInfo `json:"locks"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

// LockVerifyRequest는 잠금 검증 요청입니다.
type LockVerifyRequest struct {
	Cursor string   `json:"cursor,omitempty"`
	Limit  int      `json:"limit,omitempty"`
	Ref    *RefSpec `json:"ref,omitempty"`
}

// LockVerifyResponse는 잠금 검증 응답입니다.
type LockVerifyResponse struct {
	Ours       []LockInfo `json:"ours"`
	Theirs     []LockInfo `json:"theirs"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

// UnlockRequest는 잠금 해제 요청입니다.
type UnlockRequest struct {
	Force bool     `json:"force,omitempty"`
	Ref   *RefSpec `json:"ref,omitempty"`
}

// UnlockResponse는 잠금 해제 응답입니다.
type UnlockResponse struct {
	Lock LockInfo `json:"lock"`
}

// UsageResponse는 LFS 사용량 조회 응답입니다.
type UsageResponse struct {
	Owner     string         `json:"owner"`
	Storage   StorageUsage   `json:"storage"`
	Bandwidth BandwidthUsage `json:"bandwidth"`
	Repos     []RepoUsage    `json:"repositories,omitempty"`
}

// StorageUsage는 스토리지 사용량입니다.
type StorageUsage struct {
	UsedBytes   int64 `json:"used_bytes"`
	LimitBytes  int64 `json:"limit_bytes"`
	ObjectCount int64 `json:"object_count"`
}

// BandwidthUsage는 대역폭 사용량입니다.
type BandwidthUsage struct {
	Month      string `json:"month"` // "2026-03"
	UsedBytes  int64  `json:"used_bytes"`
	LimitBytes int64  `json:"limit_bytes"`
}

// RepoUsage는 레포지토리별 사용량입니다.
type RepoUsage struct {
	Repo         string `json:"repo"`
	StorageBytes int64  `json:"storage_bytes"`
	ObjectCount  int64  `json:"object_count"`
}
