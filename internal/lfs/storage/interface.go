package storage

import (
	"context"
	"time"
)

// SignedURL은 GCS Signed URL 정보를 담습니다.
type SignedURL struct {
	URL       string
	Headers   map[string]string
	ExpiresAt time.Time
}

// Backend는 LFS 오브젝트 스토리지 백엔드 인터페이스입니다.
// GCS, S3, 로컬 파일시스템 등 다양한 구현체를 지원합니다.
type Backend interface {
	// GenerateUploadURL은 클라이언트가 직접 스토리지에 업로드할 수 있는 Signed URL을 생성합니다.
	GenerateUploadURL(ctx context.Context, owner, repo, oid string, size int64) (*SignedURL, error)

	// GenerateDownloadURL은 클라이언트가 직접 스토리지에서 다운로드할 수 있는 Signed URL을 생성합니다.
	GenerateDownloadURL(ctx context.Context, owner, repo, oid string) (*SignedURL, error)

	// Exists는 오브젝트가 스토리지에 존재하는지 확인합니다.
	Exists(ctx context.Context, owner, repo, oid string) (bool, error)

	// ObjectSize는 스토리지에 저장된 오브젝트의 크기를 반환합니다.
	ObjectSize(ctx context.Context, owner, repo, oid string) (int64, error)

	// Delete는 스토리지에서 오브젝트를 삭제합니다.
	Delete(ctx context.Context, owner, repo, oid string) error
}
