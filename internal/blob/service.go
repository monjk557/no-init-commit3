package blob

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// MaxBlobSize는 blob의 최대 크기입니다 (50MB)
const MaxBlobSize = 50 * 1024 * 1024

// BlobService는 blob 관련 비즈니스 로직을 처리하는 서비스입니다.
type BlobService struct{}

// NewBlobService는 새로운 BlobService를 생성합니다.
func NewBlobService() *BlobService {
	return &BlobService{}
}

// GetBlobContent는 저장소에서 blob 내용을 가져옵니다.
// native git show를 사용합니다 (go-git 대체).
func (s *BlobService) GetBlobContent(ctx context.Context, repoPath, path, ref string) ([]byte, string, error) {
	content, err := NativeGetBlobContent(ctx, repoPath, ref, path)
	if err != nil {
		return nil, "", fmt.Errorf("file not found: %w", err)
	}

	// 파일 크기 체크
	if len(content) > MaxBlobSize {
		return nil, "", fmt.Errorf("file is too large (%d bytes > %d bytes)", len(content), MaxBlobSize)
	}

	// Content-Type 감지
	contentType := DetectContentType(content, path)

	return content, contentType, nil
}

// DetectContentType는 파일 내용과 이름을 기반으로 Content-Type을 결정합니다.
func DetectContentType(content []byte, filename string) string {
	// 파일 확장자로 먼저 시도
	ext := filepath.Ext(filename)
	if contentType := mime.TypeByExtension(ext); contentType != "" {
		return contentType
	}

	// 내용 기반 감지
	contentType := http.DetectContentType(content)

	// 일반적인 경우에 대한 특별 처리
	if contentType == "text/plain; charset=utf-8" {
		switch ext {
		case ".json":
			return "application/json"
		case ".yaml", ".yml":
			return "application/x-yaml"
		case ".xml":
			return "application/xml"
		case ".css":
			return "text/css"
		case ".js":
			return "application/javascript"
		case ".html", ".htm":
			return "text/html"
		case ".md", ".markdown":
			return "text/markdown"
		case ".go":
			return "text/x-go"
		case ".py":
			return "text/x-python"
		case ".sh":
			return "application/x-sh"
		}
	}

	return contentType
}

// IsBinaryContent는 내용이 바이너리인지 확인합니다.
func IsBinaryContent(content []byte, contentType string) bool {
	// Content-Type 확인
	if strings.HasPrefix(contentType, "text/") ||
		strings.HasPrefix(contentType, "application/json") ||
		strings.HasPrefix(contentType, "application/xml") ||
		strings.HasPrefix(contentType, "application/javascript") {
		return false
	}

	// UTF-8 유효성 확인
	return !utf8.Valid(content)
}
