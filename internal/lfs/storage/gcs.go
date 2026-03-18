package storage

import (
	"context"
	"fmt"
	"time"

	gcs "cloud.google.com/go/storage"
	"go.uber.org/zap"
)

// GCSBackend는 Google Cloud Storage를 사용하는 LFS 스토리지 백엔드입니다.
// Signed URL을 통해 클라이언트가 GCS에 직접 업로드/다운로드하므로
// git-executor를 경유하지 않아 서버 부하가 없습니다.
type GCSBackend struct {
	client     *gcs.Client
	bucketName string
	urlExpiry  time.Duration
	logger     *zap.Logger
}

// NewGCSBackend는 새로운 GCS 백엔드를 생성합니다.
// Application Default Credentials (ADC) 또는 Workload Identity를 사용합니다.
func NewGCSBackend(ctx context.Context, bucketName string, urlExpiry time.Duration, logger *zap.Logger) (*GCSBackend, error) {
	client, err := gcs.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCS client: %w", err)
	}

	if logger == nil {
		logger = zap.NewNop()
	}

	return &GCSBackend{
		client:     client,
		bucketName: bucketName,
		urlExpiry:  urlExpiry,
		logger:     logger,
	}, nil
}

// objectPath는 GCS 버킷 내 오브젝트 경로를 생성합니다.
// 디렉토리 과부하 방지를 위해 OID 앞 4자리로 2단계 샤딩합니다.
// 형식: {owner}/{repo}/lfs/{oid[0:2]}/{oid[2:4]}/{oid}
func objectPath(owner, repo, oid string) string {
	return fmt.Sprintf("%s/%s/lfs/%s/%s/%s", owner, repo, oid[:2], oid[2:4], oid)
}

// GenerateUploadURL은 GCS Signed URL을 생성하여 클라이언트가 직접 업로드할 수 있게 합니다.
func (g *GCSBackend) GenerateUploadURL(ctx context.Context, owner, repo, oid string, size int64) (*SignedURL, error) {
	objPath := objectPath(owner, repo, oid)
	expiresAt := time.Now().Add(g.urlExpiry)

	url, err := g.client.Bucket(g.bucketName).SignedURL(objPath, &gcs.SignedURLOptions{
		Method:  "PUT",
		Expires: expiresAt,
		Headers: []string{
			"Content-Type:application/octet-stream",
			fmt.Sprintf("Content-Length:%d", size),
		},
		Scheme: gcs.SigningSchemeV4,
	})
	if err != nil {
		g.logger.Error("failed to generate upload signed URL",
			zap.String("owner", owner),
			zap.String("repo", repo),
			zap.String("oid", oid),
			zap.Error(err))
		return nil, fmt.Errorf("failed to generate upload URL: %w", err)
	}

	return &SignedURL{
		URL: url,
		Headers: map[string]string{
			"Content-Type": "application/octet-stream",
		},
		ExpiresAt: expiresAt,
	}, nil
}

// GenerateDownloadURL은 GCS Signed URL을 생성하여 클라이언트가 직접 다운로드할 수 있게 합니다.
func (g *GCSBackend) GenerateDownloadURL(ctx context.Context, owner, repo, oid string) (*SignedURL, error) {
	objPath := objectPath(owner, repo, oid)
	expiresAt := time.Now().Add(g.urlExpiry)

	url, err := g.client.Bucket(g.bucketName).SignedURL(objPath, &gcs.SignedURLOptions{
		Method:  "GET",
		Expires: expiresAt,
		Scheme:  gcs.SigningSchemeV4,
	})
	if err != nil {
		g.logger.Error("failed to generate download signed URL",
			zap.String("owner", owner),
			zap.String("repo", repo),
			zap.String("oid", oid),
			zap.Error(err))
		return nil, fmt.Errorf("failed to generate download URL: %w", err)
	}

	return &SignedURL{
		URL:       url,
		ExpiresAt: expiresAt,
	}, nil
}

// Exists는 GCS에 오브젝트가 존재하는지 확인합니다.
func (g *GCSBackend) Exists(ctx context.Context, owner, repo, oid string) (bool, error) {
	objPath := objectPath(owner, repo, oid)
	_, err := g.client.Bucket(g.bucketName).Object(objPath).Attrs(ctx)
	if err == gcs.ErrObjectNotExist {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to check object existence: %w", err)
	}
	return true, nil
}

// ObjectSize는 GCS에 저장된 오브젝트의 크기를 반환합니다.
func (g *GCSBackend) ObjectSize(ctx context.Context, owner, repo, oid string) (int64, error) {
	objPath := objectPath(owner, repo, oid)
	attrs, err := g.client.Bucket(g.bucketName).Object(objPath).Attrs(ctx)
	if err == gcs.ErrObjectNotExist {
		return 0, fmt.Errorf("object not found: %s", oid)
	}
	if err != nil {
		return 0, fmt.Errorf("failed to get object size: %w", err)
	}
	return attrs.Size, nil
}

// Delete는 GCS에서 오브젝트를 삭제합니다.
func (g *GCSBackend) Delete(ctx context.Context, owner, repo, oid string) error {
	objPath := objectPath(owner, repo, oid)
	err := g.client.Bucket(g.bucketName).Object(objPath).Delete(ctx)
	if err == gcs.ErrObjectNotExist {
		return nil // 이미 없으면 성공으로 처리
	}
	if err != nil {
		return fmt.Errorf("failed to delete object: %w", err)
	}
	return nil
}

// Close는 GCS 클라이언트를 정리합니다.
func (g *GCSBackend) Close() error {
	return g.client.Close()
}
