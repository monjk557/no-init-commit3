package overview

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/go-enry/go-enry/v2"
	"github.com/go-enry/go-enry/v2/data"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"go.uber.org/zap"
)

// RepositoryOverviewService는 저장소 개요 관련 비즈니스 로직을 처리합니다.
type RepositoryOverviewService struct {
	logger        *logging.Logger
	pathConfig    *repositories.PathConfig
	repoManager   *repositories.DefaultRepositoryManager
	objectManager *repositories.GitObjectManager
}

// NewRepositoryOverviewService는 새로운 RepositoryOverviewService를 생성합니다.
func NewRepositoryOverviewService(logger *logging.Logger, pathConfig *repositories.PathConfig, repoManager *repositories.DefaultRepositoryManager) (*RepositoryOverviewService, error) {
	objectManager, err := repositories.NewGitObjectManager(pathConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create object manager: %w", err)
	}

	return &RepositoryOverviewService{
		logger:        logger,
		pathConfig:    pathConfig,
		repoManager:   repoManager,
		objectManager: objectManager,
	}, nil
}

// GetRemotes는 저장소의 모든 원격 저장소를 가져옵니다.
func (s *RepositoryOverviewService) GetRemotes(ctx context.Context, repo *gogit.Repository) ([]RemoteInfo, error) {
	remotes, err := repo.Remotes()
	if err != nil {
		return nil, fmt.Errorf("failed to get remotes: %w", err)
	}

	// 슬라이스 용량을 미리 할당하여 재할당 방지
	remoteList := make([]RemoteInfo, 0, len(remotes))
	for _, remote := range remotes {
		remoteList = append(remoteList, RemoteInfo{
			Name:      remote.Config().Name,
			URL:       remote.Config().URLs[0],
			IsDefault: remote.Config().Name == "origin",
		})
	}

	return remoteList, nil
}

// GetLanguageDistribution는 저장소의 언어 분포를 계산합니다.
func (s *RepositoryOverviewService) GetLanguageDistribution(ctx context.Context, repo *gogit.Repository, ref string) LanguageDistribution {
	// 컨텍스트 취소 확인
	select {
	case <-ctx.Done():
		return LanguageDistribution{
			Languages:  []LanguageStats{},
			TotalBytes: 0,
			TotalFiles: 0,
		}
	default:
	}

	// ref를 커밋 해시로 해석 (repositories 패키지 사용)
	commitHash, err := s.objectManager.ResolveReference(repo, ref)
	if err != nil {
		s.logger.Warn("Failed to resolve commit for language distribution", zap.String("ref", ref), zap.Error(err))
		return LanguageDistribution{
			Languages:  []LanguageStats{},
			TotalBytes: 0,
			TotalFiles: 0,
		}
	}

	// 커밋 객체 가져오기
	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		s.logger.Warn("Failed to get commit object for language distribution", zap.String("ref", ref), zap.Error(err))
		return LanguageDistribution{
			Languages:  []LanguageStats{},
			TotalBytes: 0,
			TotalFiles: 0,
		}
	}

	// 커밋에서 트리 가져오기
	tree, err := commit.Tree()
	if err != nil {
		s.logger.Warn("Failed to get tree from commit for language distribution", zap.String("ref", ref), zap.Error(err))
		return LanguageDistribution{
			Languages:  []LanguageStats{},
			TotalBytes: 0,
			TotalFiles: 0,
		}
	}

	// 언어 통계 맵: language -> {bytes, files}
	langStats := make(map[string]struct {
		bytes int64
		files int
	})

	var totalBytes int64
	var totalFiles int

	// 저장소의 모든 파일 순회
	err = tree.Files().ForEach(func(file *object.File) error {
		// 순회 중 컨텍스트 취소 확인
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// 바이너리 파일과 매우 큰 파일(1MB 이상) 건너뛰기
		if file.Size > 1024*1024 {
			return nil
		}

		// 언어 감지를 위해 파일 내용 가져오기
		reader, err := file.Reader()
		if err != nil {
			// 읽을 수 없는 파일은 건너뛰기
			return nil
		}

		content, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			// 읽을 수 없는 파일은 건너뛰기
			return nil
		}

		// 파일이 바이너리인지 확인
		if enry.IsBinary(content) {
			return nil
		}

		// go-enry를 사용하여 언어 감지
		language := enry.GetLanguage(file.Name, content)

		// 언어가 감지되지 않으면 파일명만으로 감지 시도
		if language == "" {
			language, _ = enry.GetLanguageByFilename(file.Name)
		}

		// 여전히 언어가 없으면 이 파일 건너뛰기
		if language == "" {
			return nil
		}

		// LanguagesGroup을 사용하여 파생 언어를 기본 언어로 매핑
		// 예: "TSX" -> "TypeScript", "JSX" -> "JavaScript"
		if baseLanguage, exists := data.LanguagesGroup[language]; exists {
			language = baseLanguage
		}

		// 통계 업데이트
		stats := langStats[language]
		stats.bytes += file.Size
		stats.files++
		langStats[language] = stats

		totalBytes += file.Size
		totalFiles++

		return nil
	})

	if err != nil && err != io.EOF {
		s.logger.Warn("Failed to iterate files for language distribution", zap.Error(err))
		return LanguageDistribution{
			Languages:  []LanguageStats{},
			TotalBytes: 0,
			TotalFiles: 0,
		}
	}

	// 맵을 슬라이스로 변환하고 비율 계산
	// 용량을 미리 할당하여 재할당 방지
	languages := make([]LanguageStats, 0, len(langStats))
	for lang, stats := range langStats {
		percentage := float64(0)
		if totalBytes > 0 {
			percentage = (float64(stats.bytes) / float64(totalBytes)) * 100
		}

		languages = append(languages, LanguageStats{
			Language:   lang,
			Bytes:      stats.bytes,
			Percentage: percentage,
			Files:      stats.files,
		})
	}

	// 바이트 수로 정렬 (내림차순)
	sort.Slice(languages, func(i, j int) bool {
		return languages[i].Bytes > languages[j].Bytes
	})

	return LanguageDistribution{
		Languages:  languages,
		TotalBytes: totalBytes,
		TotalFiles: totalFiles,
	}
}

// GetLatestCommitForPath는 특정 경로의 최근 커밋을 가져옵니다.
// blob인 경우 해당 파일의 최근 커밋, tree인 경우 하위를 재귀적으로 탐색하여 가장 최근 커밋을 반환합니다.
func (s *RepositoryOverviewService) GetLatestCommitForPath(ctx context.Context, repo *gogit.Repository, ref, filePath string, isTree bool) (*GitCommit, error) {
	// 컨텍스트 취소 확인
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// ref를 커밋 해시로 해석
	commitHash, err := s.objectManager.ResolveReference(repo, ref)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve reference: %w", err)
	}

	// 경로 정규화 (슬래시 제거 및 정리)
	normalizedPath := strings.Trim(filePath, "/")

	// 커밋 로그 가져오기 (경로 필터링)
	// PathFilter는 변경된 파일 경로만 필터링하므로, 디렉토리 경로 자체는 매칭되지 않음
	var pathFilter func(string) bool
	if isTree {
		// tree인 경우: 해당 경로로 시작하는 모든 하위 파일 경로 포함
		// 예: "src" -> "src/main.go", "src/utils/helper.go" 등 모두 포함
		if normalizedPath == "" {
			// 루트 경로인 경우 모든 경로 포함
			pathFilter = func(path string) bool {
				return true
			}
		} else {
			// 경로가 정확히 일치하거나 하위 경로인지 확인
			// 디렉토리 경로 자체는 커밋에 포함되지 않으므로 하위 파일만 체크
			pathFilter = func(path string) bool {
				return path == normalizedPath || strings.HasPrefix(path, normalizedPath+"/")
			}
		}
	} else {
		// blob인 경우: 정확히 일치하는 경로만
		pathFilter = func(path string) bool {
			return path == normalizedPath
		}
	}

	commitIter, err := repo.Log(&gogit.LogOptions{
		From:       commitHash,
		PathFilter: pathFilter,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get commit log: %w", err)
	}
	defer commitIter.Close()

	// 첫 번째 커밋 가져오기 (가장 최근 커밋)
	var latestCommit *object.Commit
	err = commitIter.ForEach(func(c *object.Commit) error {
		latestCommit = c
		// 첫 번째 커밋만 가져오고 중지
		return io.EOF
	})

	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("failed to iterate commits: %w", err)
	}

	if latestCommit == nil {
		// 해당 경로에 대한 커밋이 없는 경우 nil 반환 (에러 아님)
		return nil, nil
	}

	// GitCommit으로 변환
	return &GitCommit{
		SHA:          latestCommit.Hash.String(),
		Message:      latestCommit.Message,
		Author:       convertAuthorToGitAuthorFromObject(latestCommit.Author),
		Committer:    convertAuthorToGitAuthorFromObject(latestCommit.Committer),
		ParentHashes: convertParentHashes(latestCommit.ParentHashes),
		TreeHash:     latestCommit.TreeHash.String(),
	}, nil
}

// convertAuthorToGitAuthorFromObject는 object.Signature를 GitAuthor로 변환합니다.
func convertAuthorToGitAuthorFromObject(sig object.Signature) GitAuthor {
	return GitAuthor{
		Name:  sig.Name,
		Email: sig.Email,
		Date:  sig.When,
	}
}

// convertParentHashes는 plumbing.Hash 슬라이스를 string 슬라이스로 변환합니다.
func convertParentHashes(hashes []plumbing.Hash) []string {
	result := make([]string, len(hashes))
	for i, hash := range hashes {
		result[i] = hash.String()
	}
	return result
}
