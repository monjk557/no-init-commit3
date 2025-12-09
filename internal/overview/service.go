package overview

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/go-enry/go-enry/v2"
	"github.com/go-enry/go-enry/v2/data"
	gogit "github.com/go-git/go-git/v5"
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
