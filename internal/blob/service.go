package blob

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/collabops/git-executor/internal/repositories"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// MaxBlobSize는 blob의 최대 크기입니다 (50MB)
const MaxBlobSize = 50 * 1024 * 1024

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
// repoWrapper가 제공되면 캐시된 repository를 사용하고, nil이면 새로 엽니다.
func (s *BlobService) GetBlobContent(ctx context.Context, owner, repo, path, ref string, repoWrapper *repositories.RepositoryWrapper) ([]byte, string, error) {
	var content []byte
	var err error

	// 이미 열린 repository가 있으면 사용
	if repoWrapper != nil {
		// RepositoryWrapper에서 *gogit.Repository를 가져와서 직접 blob 가져오기
		gogitRepo := repoWrapper.GetRepo()
		content, err = getBlobFromGogitRepo(ctx, gogitRepo, path, ref)
		if err != nil {
			return nil, "", fmt.Errorf("file not found: %w", err)
		}
	} else {
		// 저장소 경로 검증 및 가져오기
		repoPath, err := repositories.GetAndValidateRepoPath(s.pathConfig, owner, repo)
		if err != nil {
			return nil, "", fmt.Errorf("repository not found: %w", err)
		}

		// 저장소 열기 (캐시 사용)
		gitRepo, err := repositories.OpenRepositorySimple(repoPath)
		if err != nil {
			return nil, "", fmt.Errorf("failed to open repository: %w", err)
		}
		defer func() { _ = gitRepo.Close() }()

		// Blob 내용 가져오기
		content, err = getBlobFromGogitRepo(ctx, gitRepo.GetRepo(), path, ref)
		if err != nil {
			return nil, "", fmt.Errorf("file not found: %w", err)
		}
	}

	// Content-Type 감지
	contentType := detectContentType(content, path)

	return content, contentType, nil
}

// getBlobFromGogitRepo는 *gogit.Repository에서 직접 blob을 가져옵니다.
func getBlobFromGogitRepo(ctx context.Context, repo *gogit.Repository, path, ref string) ([]byte, error) {
	// ref 해석 (ResolveReference와 동일한 로직)
	var commitHash plumbing.Hash
	if ref == "" || ref == "HEAD" {
		head, err := repo.Head()
		if err != nil {
			return nil, fmt.Errorf("failed to get HEAD: %w", err)
		}
		commitHash = head.Hash()
	} else {
		// Try to resolve as a reference first
		refObj, err := repo.Reference(plumbing.ReferenceName(ref), false)
		if err == nil {
			commitHash = refObj.Hash()
		} else {
			// Try with refs/heads/ prefix
			refObj, err = repo.Reference(plumbing.ReferenceName("refs/heads/"+ref), false)
			if err == nil {
				commitHash = refObj.Hash()
			} else {
				// If not a reference, try as a hash
				hash := plumbing.NewHash(ref)
				if hash == plumbing.ZeroHash {
					return nil, fmt.Errorf("failed to resolve reference: %s", ref)
				}
				commitHash = hash
			}
		}
	}

	// 커밋 가져오기
	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get commit: %w", err)
	}

	// 트리 가져오기
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get tree: %w", err)
	}

	// 파일 찾기
	file, err := tree.File(path)
	if err != nil {
		return nil, fmt.Errorf("file not found: %w", err)
	}

	// 파일 크기 체크
	if file.Size > MaxBlobSize {
		return nil, fmt.Errorf("file is too large (%d bytes > %d bytes)", file.Size, MaxBlobSize)
	}

	// 컨텍스트 취소 확인
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Blob 내용 읽기
	reader, err := file.Blob.Reader()
	if err != nil {
		return nil, fmt.Errorf("failed to read blob: %w", err)
	}
	defer func() { _ = reader.Close() }()

	// 제한된 리더 사용 (MaxBlobSize + 1 바이트까지만 읽기)
	limitedReader := io.LimitReader(reader, MaxBlobSize+1)
	content, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read blob content: %w", err)
	}

	// 실제로 제한을 초과했는지 확인
	if len(content) > MaxBlobSize {
		return nil, fmt.Errorf("file is too large (%d bytes > %d bytes)", len(content), MaxBlobSize)
	}

	return content, nil
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
