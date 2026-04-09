package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"
)

// LocalBackend는 로컬 파일시스템을 사용하는 LFS 스토리지 백엔드입니다.
// GCS와 달리 Signed URL 대신 git-executor 서버를 통해 파일을 프록시합니다.
type LocalBackend struct {
	basePath  string
	baseURL   string
	urlExpiry time.Duration
	logger    *zap.Logger
}

// NewLocalBackend는 새로운 로컬 파일시스템 백엔드를 생성합니다.
// basePath: LFS 오브젝트를 저장할 루트 디렉토리
// baseURL: git-executor 서버의 외부 접근 가능한 URL (예: http://git-executor:8080)
func NewLocalBackend(basePath, baseURL string, urlExpiry time.Duration, logger *zap.Logger) (*LocalBackend, error) {
	if err := os.MkdirAll(basePath, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create LFS storage directory: %w", err)
	}

	if logger == nil {
		logger = zap.NewNop()
	}

	return &LocalBackend{
		basePath:  basePath,
		baseURL:   baseURL,
		urlExpiry: urlExpiry,
		logger:    logger,
	}, nil
}

// objectPath는 로컬 파일시스템의 오브젝트 경로를 생성합니다.
// 디렉토리 과부하 방지를 위해 OID 앞 4자리로 2단계 샤딩합니다.
// 형식: {basePath}/{owner}/{repo}/lfs/{oid[0:2]}/{oid[2:4]}/{oid}
func (l *LocalBackend) objectPath(owner, repo, oid string) string {
	return filepath.Join(l.basePath, owner, repo, "lfs", oid[:2], oid[2:4], oid)
}

// GenerateUploadURL은 git-executor 서버의 업로드 엔드포인트 URL을 반환합니다.
// 클라이언트는 이 URL로 PUT 요청을 보내 파일을 업로드합니다.
func (l *LocalBackend) GenerateUploadURL(_ context.Context, owner, repo, oid string, _ int64) (*SignedURL, error) {
	expiresAt := time.Now().Add(l.urlExpiry)

	url := fmt.Sprintf("%s/repos/%s/%s/info/lfs/objects/storage/%s", l.baseURL, owner, repo, oid)

	return &SignedURL{
		URL: url,
		Headers: map[string]string{
			"Content-Type": "application/octet-stream",
		},
		ExpiresAt: expiresAt,
	}, nil
}

// GenerateDownloadURL은 git-executor 서버의 다운로드 엔드포인트 URL을 반환합니다.
// 클라이언트는 이 URL로 GET 요청을 보내 파일을 다운로드합니다.
func (l *LocalBackend) GenerateDownloadURL(_ context.Context, owner, repo, oid string) (*SignedURL, error) {
	expiresAt := time.Now().Add(l.urlExpiry)

	url := fmt.Sprintf("%s/repos/%s/%s/info/lfs/objects/storage/%s", l.baseURL, owner, repo, oid)

	return &SignedURL{
		URL:       url,
		ExpiresAt: expiresAt,
	}, nil
}

// Exists는 로컬 파일시스템에 오브젝트가 존재하는지 확인합니다.
func (l *LocalBackend) Exists(_ context.Context, owner, repo, oid string) (bool, error) {
	path := l.objectPath(owner, repo, oid)
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to check object existence: %w", err)
	}
	return true, nil
}

// ObjectSize는 로컬 파일시스템에 저장된 오브젝트의 크기를 반환합니다.
func (l *LocalBackend) ObjectSize(_ context.Context, owner, repo, oid string) (int64, error) {
	path := l.objectPath(owner, repo, oid)
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return 0, fmt.Errorf("object not found: %s", oid)
	}
	if err != nil {
		return 0, fmt.Errorf("failed to get object size: %w", err)
	}
	return info.Size(), nil
}

// Delete는 로컬 파일시스템에서 오브젝트를 삭제합니다.
func (l *LocalBackend) Delete(_ context.Context, owner, repo, oid string) error {
	path := l.objectPath(owner, repo, oid)
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil // 이미 없으면 성공으로 처리
	}
	if err != nil {
		return fmt.Errorf("failed to delete object: %w", err)
	}
	return nil
}

// StoreObject는 io.Reader로부터 데이터를 읽어 로컬 파일시스템에 저장합니다.
func (l *LocalBackend) StoreObject(owner, repo, oid string, reader io.Reader) (int64, error) {
	path := l.objectPath(owner, repo, oid)

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, fmt.Errorf("failed to create object directory: %w", err)
	}

	// 임시 파일에 먼저 쓰고 rename하여 원자적 쓰기 보장
	tmpFile, err := os.CreateTemp(dir, ".lfs-upload-*")
	if err != nil {
		return 0, fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	written, err := io.Copy(tmpFile, reader)
	if closeErr := tmpFile.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(tmpPath)
		return 0, fmt.Errorf("failed to write object: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return 0, fmt.Errorf("failed to finalize object: %w", err)
	}

	l.logger.Debug("stored LFS object",
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.String("oid", oid),
		zap.Int64("size", written))

	return written, nil
}

// OpenObject는 로컬 파일시스템에서 오브젝트 파일을 열어 반환합니다.
// 호출자가 반환된 파일을 Close해야 합니다.
func (l *LocalBackend) OpenObject(owner, repo, oid string) (*os.File, error) {
	path := l.objectPath(owner, repo, oid)
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open object: %w", err)
	}
	return f, nil
}
