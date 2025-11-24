package blob

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/collabops/git-executor/internal/git"
	"github.com/collabops/git-executor/internal/repositories"
)

// BlobService는 blob 관련 비즈니스 로직을 처리하는 서비스입니다.
type BlobService struct {
	pathConfig *repositories.PathConfig
}

// NewBlobService는 새로운 BlobService를 생성합니다.
func NewBlobService(pathConfig *repositories.PathConfig) *BlobService {
	return &BlobService{
		pathConfig: pathConfig,
	}
}

// GetBlobContent는 저장소에서 blob 내용을 가져옵니다.
func (s *BlobService) GetBlobContent(ctx context.Context, owner, repo, path, ref string) ([]byte, string, error) {
	// 저장소 경로 검증 및 가져오기
	repoPath, err := repositories.GetAndValidateRepoPath(s.pathConfig, owner, repo)
	if err != nil {
		return nil, "", fmt.Errorf("repository not found: %w", err)
	}

	// 저장소 열기
	gitRepo, err := git.OpenRepositorySimple(repoPath)
	if err != nil {
		return nil, "", fmt.Errorf("failed to open repository: %w", err)
	}
	defer func() { _ = gitRepo.Close() }()

	// Blob 내용 가져오기
	content, err := gitRepo.GetBlob(ctx, path, ref)
	if err != nil {
		return nil, "", fmt.Errorf("file not found: %w", err)
	}

	// Content-Type 감지
	contentType := detectContentType(content, path)

	return content, contentType, nil
}

// detectContentType는 파일 내용과 이름을 기반으로 Content-Type을 결정합니다.
func detectContentType(content []byte, filename string) string {
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
