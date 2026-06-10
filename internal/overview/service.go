package overview

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/collabops/git-executor/internal/cache"
	"github.com/collabops/git-executor/internal/indexing"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/performance"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/go-enry/go-enry/v2"
	"github.com/go-enry/go-enry/v2/data"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// RepositoryOverviewService는 저장소 개요 관련 비즈니스 로직을 처리합니다.
type RepositoryOverviewService struct {
	logger         *logging.Logger
	pathConfig     *repositories.PathConfig
	repoManager    *repositories.DefaultRepositoryManager
	objectManager  *repositories.GitObjectManager
	perfConfig     *performance.PerformanceConfig
	commitGraphOpt *performance.CommitGraphOptimizer
	objectCache    *performance.ObjectCache
	fdPool         *performance.FileDescriptorPool

	// Phase 2 개선: 스마트 페이지네이션 + 다층 캐싱
	smartPagination *SmartPagination
	multiTierCache  *cache.MultiTierCache[TreeWithCommitOverviewResponse]

	// Week 3 개선: 커밋 인덱싱 시스템 (O(C×N×F) → O(1) 최적화)
	queryService   *indexing.QueryService
	indexBuilder   *indexing.IndexBuilder
	enableIndexing bool
}

// NewRepositoryOverviewService는 새로운 RepositoryOverviewService를 생성합니다.
func NewRepositoryOverviewService(logger *logging.Logger, pathConfig *repositories.PathConfig, repoManager *repositories.DefaultRepositoryManager, db *gorm.DB) (*RepositoryOverviewService, error) {
	objectManager, err := repositories.NewGitObjectManager(pathConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create object manager: %w", err)
	}

	// Initialize performance configuration and optimizers
	perfConfig := performance.NewPerformanceConfig()
	commitGraphOpt := performance.NewCommitGraphOptimizer(perfConfig, logger.Logger)
	objectCache := performance.NewObjectCache(perfConfig, logger.Logger)
	fdPool := performance.NewFileDescriptorPool(perfConfig, logger.Logger)

	// Phase 2 개선: 스마트 페이지네이션 초기화
	smartPagination := NewSmartPagination(logger)

	// Phase 2 개선: 다층 캐싱 시스템 초기화
	// 오버뷰 캐시는 Redis 를 사용하지 않는다 — Go in-memory L1 만 사용한다 (Issue-3658).
	// 이전의 CACHE_REDIS_URL 기반 L2 는 ACL/URL 파싱 미지원으로 사실상 비활성 상태였고,
	// 캐시 경로를 event bus Redis 와 분리하기 위해 제거했다.
	multiTierCacheConfig := &cache.MultiTierConfig{
		L1MaxSize:             1000,             // 메모리에 1000개 항목
		L1DefaultTTL:          10 * time.Minute, // L1 캐시 10분 TTL
		L1CleanupInterval:     1 * time.Minute,  // 1분마다 정리
		L2Enabled:             false,            // Redis L2 미사용 (in-memory L1 전용)
		EnablePerformanceMode: true,             // 성능 모드 활성화
	}

	multiTierCache, err := cache.NewMultiTierCache[TreeWithCommitOverviewResponse](multiTierCacheConfig, logger.Logger)
	if err != nil {
		logger.Warn("Failed to initialize multi-tier cache, continuing without cache",
			zap.Error(err))
		// 캐시 없이 계속 진행 (degraded mode)
		multiTierCache = nil
	} else {
		logger.Info("Multi-tier cache system enabled - targeting 40% performance improvement on repeated requests")
	}

	// Week 3 개선: 커밋 인덱싱 시스템 초기화
	var queryService *indexing.QueryService
	var indexBuilder *indexing.IndexBuilder
	enableIndexing := db != nil

	if enableIndexing {
		// Git service implementation for indexing
		gitSvc := &GitServiceImpl{
			pathConfig: pathConfig,
			logger:     logger,
		}

		// Query service for O(1) commit lookups
		queryConfig := &indexing.QueryConfig{
			QueryTimeout:         5 * time.Second,
			MaxPathsPerQuery:     1000,
			EnableQueryProfiling: true,
			CacheResults:         true,
			CacheTTL:             5 * time.Minute,
		}
		queryService = indexing.NewQueryService(db, queryConfig)

		// Index builder for maintaining commit index
		indexConfig := &indexing.IndexConfig{
			BatchSize:          100,
			MaxConcurrency:     4,
			IndexTimeout:       30 * time.Minute,
			IncrementalTimeout: 5 * time.Minute,
			MaxCommitsPerRun:   10000,
			EnableMetrics:      true,
		}
		indexBuilder = indexing.NewIndexBuilder(db, gitSvc, indexConfig)

		logger.Info("Commit indexing system enabled - targeting 97% performance improvement",
			zap.String("target", "8,450ms → 200ms"))
	} else {
		logger.Info("Commit indexing system disabled - database not available")
	}

	return &RepositoryOverviewService{
		logger:         logger,
		pathConfig:     pathConfig,
		repoManager:    repoManager,
		objectManager:  objectManager,
		perfConfig:     perfConfig,
		commitGraphOpt: commitGraphOpt,
		objectCache:    objectCache,
		fdPool:         fdPool,

		// Phase 2 개선: 스마트 페이지네이션 + 다층 캐싱
		smartPagination: smartPagination,
		multiTierCache:  multiTierCache,

		// Week 3 개선: 커밋 인덱싱
		queryService:   queryService,
		indexBuilder:   indexBuilder,
		enableIndexing: enableIndexing,
	}, nil
}

// EnsureRepositoryOptimized ensures that performance optimizations are applied to the repository
func (s *RepositoryOverviewService) EnsureRepositoryOptimized(repoPath string) error {
	if !s.perfConfig.EnableCommitGraphOptimization {
		return nil
	}

	// Auto-generate commit-graph for better performance
	return s.commitGraphOpt.EnsureCommitGraph(repoPath)
}

// GetPerformanceStats returns current performance optimization statistics
func (s *RepositoryOverviewService) GetPerformanceStats(repoPath string) map[string]interface{} {
	stats := s.commitGraphOpt.GetOptimizationStats(repoPath)

	// Add object cache statistics
	objectCacheStats := s.objectCache.GetStats()
	stats["object_cache_hit_rate"] = objectCacheStats.HitRate
	stats["object_cache_size"] = objectCacheStats.CacheSize

	// Add file descriptor pool statistics
	fdStats := s.fdPool.GetStats()
	stats["fd_pool_hit_rate"] = fdStats.HitRate
	stats["fd_pool_size"] = fdStats.PoolSize
	stats["fd_pool_active"] = fdStats.ActiveFDs

	return stats
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
	// NOTE: GitHub/GitLab(linguist) 기준과 최대한 유사하게 맞추기 위해
	// vendored/dotfile/documentation/configuration/generated 파일은 기본 제외합니다.
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

	// PERFORMANCE OPTIMIZATION: Use smart language detection with sampling + parallel processing
	// Expected improvement: 50-80% faster through intelligent sampling and better parallelism

	var finalLanguages []LanguageStats
	var totalBytes int64
	var totalFiles int

	// Try smart language detection first (optimized with sampling)
	smartDetector := performance.NewSmartLanguageDetector(s.logger.Logger)
	smartLangStats, sTotalBytes, sTotalFiles, smartErr := smartDetector.ProcessTreeWithSampling(ctx, tree)

	if smartErr == nil {
		// Success! Use smart detection results
		s.logger.Debug("Smart language detection successful",
			zap.Int("languages", len(smartLangStats)),
			zap.Int("total_files", sTotalFiles),
			zap.Int64("total_bytes", sTotalBytes))

		totalBytes = sTotalBytes
		totalFiles = sTotalFiles

		finalLanguages = make([]LanguageStats, 0, len(smartLangStats))
		for _, langStat := range smartLangStats {
			finalLanguages = append(finalLanguages, LanguageStats{
				Language:   langStat.Language,
				Bytes:      langStat.Bytes,
				Percentage: langStat.Percentage,
				Files:      langStat.Files,
			})
		}
	} else {
		// Fallback to basic parallel processing, then sequential
		s.logger.Warn("Smart language detection failed, falling back", zap.Error(smartErr))

		// Try parallel processing as fallback
		parallelProcessor := performance.NewParallelTreeProcessor(s.logger.Logger)
		parallelLangStats, pTotalBytes, pTotalFiles, parallelErr := parallelProcessor.ProcessTreeForLanguages(ctx, tree)

		if parallelErr == nil {
			s.logger.Debug("Fallback parallel processing successful")
			totalBytes = pTotalBytes
			totalFiles = pTotalFiles

			finalLanguages = make([]LanguageStats, 0, len(parallelLangStats))
			for _, langStat := range parallelLangStats {
				finalLanguages = append(finalLanguages, LanguageStats{
					Language:   langStat.Language,
					Bytes:      langStat.Bytes,
					Percentage: langStat.Percentage,
					Files:      langStat.Files,
				})
			}
		} else {
			// Final fallback to sequential processing
			s.logger.Warn("All optimizations failed, using sequential fallback", zap.Error(parallelErr))

			langStats := make(map[string]struct {
				bytes int64
				files int
			})

			err = tree.Files().ForEach(func(file *object.File) error {
				// 순회 중 컨텍스트 취소 확인
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}

				// GitHub/GitLab(linguist) 기준에 맞춰 기본 제외 대상 필터링
				// - vendored / dotfile / documentation / configuration / generated
				// (generated는 content 기반이므로 아래에서 처리)
				if enry.IsVendor(file.Name) {
					return nil
				}
				if enry.IsDotFile(file.Name) {
					return nil
				}
				if enry.IsDocumentation(file.Name) {
					return nil
				}
				if enry.IsConfiguration(file.Name) {
					return nil
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

				// generated 파일 제외 (content 기반)
				if enry.IsGenerated(file.Name, content) {
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

			// Convert sequential results to final format
			finalLanguages = make([]LanguageStats, 0, len(langStats))
			for lang, stats := range langStats {
				percentage := float64(0)
				if totalBytes > 0 {
					percentage = (float64(stats.bytes) / float64(totalBytes)) * 100
				}

				finalLanguages = append(finalLanguages, LanguageStats{
					Language:   lang,
					Bytes:      stats.bytes,
					Percentage: percentage,
					Files:      stats.files,
				})
			}
		}
	}

	// Common result processing (both parallel and sequential paths arrive here)
	languages := finalLanguages

	// 바이트 수로 정렬 (내림차순)
	sort.Slice(languages, func(i, j int) bool {
		return languages[i].Bytes > languages[j].Bytes
	})

	// 출력 규칙:
	// - 최대 top 5만 노출
	// - 노출되는 개별 언어는 1% 이상
	// - 나머지는 Others로 묶어서 합이 100%가 되도록 구성
	const (
		minLanguagePercentage = 1.0
		maxLanguagesToShow    = 5
	)

	filtered := make([]LanguageStats, 0, maxLanguagesToShow+1)
	var othersBytes int64
	var othersFiles int
	sumVisiblePercentage := 0.0

	for _, lang := range languages {
		if lang.Percentage >= minLanguagePercentage && len(filtered) < maxLanguagesToShow {
			filtered = append(filtered, lang)
			sumVisiblePercentage += lang.Percentage
			continue
		}

		othersBytes += lang.Bytes
		othersFiles += lang.Files
	}

	othersPercentage := 0.0
	if othersBytes > 0 {
		othersPercentage = 100.0 - sumVisiblePercentage
		// 부동소수점 오차 방어 (합이 100%를 넘거나 음수가 되는 케이스 방지)
		if othersPercentage < 0 {
			othersPercentage = 0
		} else if othersPercentage > 100 {
			othersPercentage = 100
		}

		filtered = append(filtered, LanguageStats{
			Language:   "Others",
			Bytes:      othersBytes,
			Percentage: othersPercentage,
			Files:      othersFiles,
		})
	}

	return LanguageDistribution{
		Languages:  filtered,
		TotalBytes: totalBytes,
		TotalFiles: totalFiles,
	}
}

// GetLatestCommitForPath는 특정 경로의 최근 커밋을 가져옵니다.
// timeBudget/maxCommits로 제한되어 timeout을 방지합니다.
// blob인 경우 해당 파일의 최근 커밋, tree인 경우 해당 경로가 변경된 최근 커밋을 반환합니다.
func (s *RepositoryOverviewService) GetLatestCommitForPath(ctx context.Context, repo *gogit.Repository, ref, filePath string, isTree bool) (*GitCommit, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	commitHash, err := s.objectManager.ResolveReference(repo, ref)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve reference: %w", err)
	}

	normalizedPath := strings.Trim(filePath, "/")

	// Fast-path: root("")의 최신 커밋 = ref가 가리키는 HEAD commit 자체.
	// GetLatestCommitsForPathsWithMeta → DiffTree는 subtree를 재귀 로드하므로
	// 디렉터리가 많은 repo에서 첫 번째 iteration만으로 수초 소요.
	// root는 어떤 변경이든 매칭되므로 DiffTree 없이 HEAD commit을 즉시 반환.
	if normalizedPath == "" {
		commit, cerr := repo.CommitObject(commitHash)
		if cerr != nil {
			return nil, fmt.Errorf("failed to get commit: %w", cerr)
		}
		return s.convertCommit(commit), nil
	}

	pathInfos := []CommitPathInfo{{Path: normalizedPath, IsTree: isTree}}
	tb := latestCommitsTimeBudget()
	mc := latestCommitsMaxCommits()
	commitMap, _ := s.GetLatestCommitsForPathsWithMeta(ctx, repo, commitHash, pathInfos, tb, mc)
	return commitMap[normalizedPath], nil
}

// convertCommit은 object.Commit을 GitCommit으로 변환합니다.
func (s *RepositoryOverviewService) convertCommit(commit *object.Commit) *GitCommit {
	return &GitCommit{
		SHA:          commit.Hash.String(),
		Message:      commit.Message,
		Author:       convertAuthorToGitAuthorFromObject(commit.Author),
		Committer:    convertAuthorToGitAuthorFromObject(commit.Committer),
		ParentHashes: convertParentHashes(commit.ParentHashes),
		TreeHash:     commit.TreeHash.String(),
	}
}

// CommitPathInfo represents a path and its type for commit lookup
type CommitPathInfo struct {
	Path   string
	IsTree bool
}

type pendingPath struct {
	info CommitPathInfo
}

func (s *RepositoryOverviewService) GetLatestCommitsForPathsWithMeta(
	ctx context.Context,
	repo *gogit.Repository,
	from plumbing.Hash,
	pathInfos []CommitPathInfo,
	timeBudget time.Duration,
	maxCommits int,
) (map[string]*GitCommit, LatestCommitsMeta) {

	result := make(map[string]*GitCommit, len(pathInfos))
	meta := LatestCommitsMeta{
		ResolvedRefHash: from.String(),
		TimeBudgetMs:    int(timeBudget / time.Millisecond),
	}
	if len(pathInfos) == 0 {
		meta.CutReason = "complete"
		return result, meta
	}

	pending := make(map[string]pendingPath, len(pathInfos))
	for _, pi := range pathInfos {
		pending[pi.Path] = pendingPath{info: pi}
	}

	// PERFORMANCE OPTIMIZATION: Use commit-graph optimized log traversal
	// Research shows 27.1% performance improvement with commit-graph
	logOptions, isOptimized := s.commitGraphOpt.OptimizeLogTraversal(repo, from)
	if isOptimized && s.perfConfig.EnablePerformanceMonitoring {
		s.logger.Debug("Using commit-graph optimized log traversal",
			zap.String("from_hash", from.String()),
			zap.Int("path_count", len(pathInfos)))
	}

	iter, err := repo.Log(logOptions)
	if err != nil {
		meta.Incomplete = true
		meta.CutReason = "error"
		return result, meta
	}
	defer iter.Close()

	start := time.Now()
	scanned := 0
	incomplete := false
	cutReason := ""

	walkErr := iter.ForEach(func(c *object.Commit) error {
		select {
		case <-ctx.Done():
			incomplete = true
			cutReason = "context_cancelled"
			return io.EOF
		default:
		}

		if timeBudget > 0 && time.Since(start) > timeBudget {
			incomplete = true
			cutReason = "time_budget"
			return io.EOF
		}
		if maxCommits > 0 && scanned >= maxCommits {
			incomplete = true
			cutReason = "max_commits"
			return io.EOF
		}
		scanned++

		if len(pending) == 0 {
			cutReason = "complete"
			return io.EOF
		}

		// Fast-path: root("") pending이 있으면 첫 번째 커밋에서 즉시 해결.
		// DiffTree는 subtree 오브젝트를 재귀 로드하므로 대형 repo에서 수초 소요.
		// root는 어떤 변경이든 매칭되므로 현재 커밋을 바로 할당.
		if p, ok := pending[""]; ok && p.info.IsTree {
			gc := s.convertCommit(c)
			result[""] = gc
			delete(pending, "")
			if len(pending) == 0 {
				cutReason = "complete"
				return io.EOF
			}
		}

		if len(pending) == 0 {
			cutReason = "complete"
			return io.EOF
		}

		currentTree, err := c.Tree()
		if err != nil {
			return nil
		}

		var parentTree *object.Tree
		if len(c.ParentHashes) > 0 {
			parent, err := repo.CommitObject(c.ParentHashes[0])
			if err == nil {
				parentTree, _ = parent.Tree()
			}
		}

		if parentTree == nil {
			for path, p := range pending {
				if matchInTree(currentTree, path, p.info.IsTree) {
					result[path] = s.convertCommit(c)
					delete(pending, path)
				}
			}
			return nil
		}

		changes, err := object.DiffTree(parentTree, currentTree)
		if err != nil {
			return nil
		}

		// Always use changes-driven matching (feature flags removed).
		gc := s.convertCommit(c)
		for _, ch := range changes {
			applyChangePathToPending(result, pending, gc, ch.From.Name)
			applyChangePathToPending(result, pending, gc, ch.To.Name)
		}

		return nil
	})

	_ = walkErr
	if cutReason == "" {
		if len(pending) == 0 {
			cutReason = "complete"
		} else {
			incomplete = true
			cutReason = "end_of_history"
		}
	}

	meta.Incomplete = incomplete
	meta.CutReason = cutReason
	meta.ScannedCommits = scanned
	return result, meta
}

func (s *RepositoryOverviewService) GetLatestCommitsForPaths(
	ctx context.Context,
	repo *gogit.Repository,
	ref string,
	pathInfos []CommitPathInfo,
) map[string]*GitCommit {

	result := make(map[string]*GitCommit, len(pathInfos))
	if len(pathInfos) == 0 {
		return result
	}

	// 아직 매칭 안 된 path들
	pending := make(map[string]pendingPath, len(pathInfos))
	for _, pi := range pathInfos {
		pending[pi.Path] = pendingPath{info: pi}
	}

	// ref 해석
	hash, err := repo.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		return result
	}

	iter, err := repo.Log(&gogit.LogOptions{From: *hash})
	if err != nil {
		return result
	}
	defer iter.Close()

	_ = iter.ForEach(func(c *object.Commit) error {
		// 취소 체크
		select {
		case <-ctx.Done():
			return io.EOF
		default:
		}

		// 전부 찾았으면 종료
		if len(pending) == 0 {
			return io.EOF
		}

		currentTree, err := c.Tree()
		if err != nil {
			return nil
		}

		var parentTree *object.Tree
		if len(c.ParentHashes) > 0 {
			parent, err := repo.CommitObject(c.ParentHashes[0])
			if err == nil {
				parentTree, _ = parent.Tree()
			}
		}

		// 초기 커밋 처리
		if parentTree == nil {
			for path, p := range pending {
				if matchInTree(currentTree, path, p.info.IsTree) {
					result[path] = s.convertCommit(c)
					delete(pending, path)
				}
			}
			return nil
		}

		// diff 계산
		changes, err := object.DiffTree(parentTree, currentTree)
		if err != nil {
			return nil
		}

		// Always use changes-driven matching (feature flags removed).
		gc := s.convertCommit(c)
		for _, ch := range changes {
			applyChangePathToPending(result, pending, gc, ch.From.Name)
			applyChangePathToPending(result, pending, gc, ch.To.Name)
		}

		return nil
	})

	return result
}

func matchInTree(tree *object.Tree, path string, isTree bool) bool {
	if path == "" {
		return true
	}

	if !isTree {
		_, err := tree.File(path)
		return err == nil
	}

	// Prefer direct tree entry lookup over scanning all files (which can be O(total files)).
	entry, err := tree.FindEntry(path)
	if err != nil {
		return false
	}
	return entry.Mode == filemode.Dir
}

func applyChangePathToPending(result map[string]*GitCommit, pending map[string]pendingPath, commit *GitCommit, changedPath string) {
	if changedPath == "" {
		return
	}

	// Root tree path ("") matches any change in the previous implementation for isTree==true.
	if p, ok := pending[""]; ok && p.info.IsTree {
		result[""] = commit
		delete(pending, "")
	}

	// Exact match for pending files.
	if p, ok := pending[changedPath]; ok && !p.info.IsTree {
		result[changedPath] = commit
		delete(pending, changedPath)
	}

	// For pending directories, walk up the changed path's parents and match by equality on dir paths.
	// This mirrors the previous `strings.HasPrefix(path+"/")` semantics without scanning all pending paths.
	for dir := parentDirGitPath(changedPath); dir != ""; dir = parentDirGitPath(dir) {
		if p, ok := pending[dir]; ok && p.info.IsTree {
			result[dir] = commit
			delete(pending, dir)
		}
	}
}

func parentDirGitPath(p string) string {
	// git paths are slash-separated, regardless of OS.
	if p == "" {
		return ""
	}
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
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

// loadGitmodulesFromTree 는 commit tree 에서 .gitmodules blob 을 찾아 파싱합니다.
// blob 이 없거나 파싱 실패 시 nil 을 반환하며, 호출자는 이를 "submodule 부가
// 정보가 비어 있는" 정상 케이스로 처리합니다. go-git 의 cached blob access
// 를 활용해 별도 git exec 없이 tree 에서 직접 읽습니다.
func (s *RepositoryOverviewService) loadGitmodulesFromTree(gitRepo *gogit.Repository, root *object.Tree) *repositories.GitmodulesIndex {
	if root == nil {
		return nil
	}
	entry, err := root.FindEntry(".gitmodules")
	if err != nil {
		return nil
	}
	if entry.Mode != filemode.Regular && entry.Mode != filemode.Executable {
		return nil
	}
	blob, err := s.objectCache.GetBlob(gitRepo, entry.Hash)
	if err != nil {
		return nil
	}
	reader, err := blob.Reader()
	if err != nil {
		return nil
	}
	defer reader.Close()
	content, err := io.ReadAll(reader)
	if err != nil {
		return nil
	}
	return repositories.ParseGitmodules(content)
}

// entryInfo는 각 tree entry의 기본 정보를 담는 내부 타입입니다.
type entryInfo struct {
	index      int
	entryType  string
	sortKey    int // pre-computed: 0=tree,1=blob,2=submodule,3=other (avoids switch per comparison)
	name       string
	fullPath   string
	modeStr    string
	hashStr    string
	size       *int64
	commitInfo *GitCommit
	submodule  *SubmoduleInfo
}

// entryTypeSortKey returns a pre-computed sort priority for entry type.
func entryTypeSortKey(et string) int {
	switch et {
	case "tree":
		return 0
	case "blob":
		return 1
	case "submodule", "commit":
		return 2
	default:
		return 3
	}
}

// GetTreeOnly는 트리 항목만 가져옵니다. 각 항목의 latest_commit은 채우지 않습니다.
// blob overview 등 커밋 조회 없이 디렉터리 목록만 필요할 때 사용합니다.
func (s *RepositoryOverviewService) GetTreeOnly(ctx context.Context, gitRepo *gogit.Repository, ref, path string) ([]GitTreeEntryWithCommit, error) {
	entries, _, err := s.getTreeEntries(ctx, gitRepo, ref, path, false)
	return entries, err
}

// GetTreeWithCommit는 트리 항목과 각 항목의 최근 커밋 정보를 함께 가져옵니다.
// Always applies N-based caps (no uncapped path). Returns meta for partial status.
func (s *RepositoryOverviewService) GetTreeWithCommit(ctx context.Context, gitRepo *gogit.Repository, ref, path string) ([]GitTreeEntryWithCommit, LatestCommitsMeta, error) {
	return s.getTreeEntries(ctx, gitRepo, ref, path, true)
}

// getTreeEntries returns tree entries; when withCommits is true also fetches latest commit per path (capped).
func (s *RepositoryOverviewService) getTreeEntries(ctx context.Context, gitRepo *gogit.Repository, ref, path string, withCommits bool) ([]GitTreeEntryWithCommit, LatestCommitsMeta, error) {
	emptyMeta := LatestCommitsMeta{}

	refObj, err := gitRepo.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		return nil, emptyMeta, fmt.Errorf("failed to resolve reference: %w", err)
	}

	commit, err := gitRepo.CommitObject(*refObj)
	if err != nil {
		return nil, emptyMeta, fmt.Errorf("failed to get commit: %w", err)
	}

	treeObj, err := commit.Tree()
	if err != nil {
		return nil, emptyMeta, fmt.Errorf("failed to get tree: %w", err)
	}

	targetTree := treeObj
	if path != "" {
		treeEntry, err := treeObj.FindEntry(path)
		if err != nil {
			return nil, emptyMeta, fmt.Errorf("failed to find path: %w", err)
		}
		if treeEntry.Mode != filemode.Dir {
			return nil, emptyMeta, fmt.Errorf("path is not a tree: %s", path)
		}
		targetTree, err = gitRepo.TreeObject(treeEntry.Hash)
		if err != nil {
			return nil, emptyMeta, fmt.Errorf("failed to get target tree: %w", err)
		}
	}

	// .gitmodules 는 항상 root tree 에 있다 — targetTree (path 하위) 가 아니라
	// 최초의 treeObj 에서 읽어야 함.
	gitmods := s.loadGitmodulesFromTree(gitRepo, treeObj)

	basePath := strings.Trim(path, "/")

	entries := make([]entryInfo, len(targetTree.Entries))
	type blobTask struct {
		idx  int
		hash plumbing.Hash
	}
	blobParallelism := blobSizeParallelism()
	blobTasks := make([]blobTask, 0, len(targetTree.Entries))
	for i, entry := range targetTree.Entries {
		fullPath := entry.Name
		if basePath != "" {
			fullPath = basePath + "/" + entry.Name
		}

		var entryType string
		switch entry.Mode {
		case filemode.Dir:
			entryType = "tree"
		case filemode.Submodule:
			entryType = "submodule"
		default:
			entryType = "blob"
		}

		modeStr := strconv.FormatUint(uint64(entry.Mode), 8)
		hashStr := entry.Hash.String()

		var submodule *SubmoduleInfo
		if entryType == "submodule" {
			submodule = &SubmoduleInfo{Commit: hashStr}
			if cfg := gitmods.LookupByPath(fullPath); cfg != nil {
				submodule.URL = cfg.URL
				submodule.Branch = cfg.Branch
			}
			if submodule.URL != "" {
				submodule.Parsed = repositories.ParseRepoURL(submodule.URL)
			}
		}

		var size *int64
		if entryType == "blob" {
			if blobParallelism > 1 {
				blobTasks = append(blobTasks, blobTask{idx: i, hash: entry.Hash})
			} else {
				// PERFORMANCE OPTIMIZATION: Use cached blob access
				// TARGET: Reduce 31.55% CPU overhead from BlobObject calls
				blobObj, err := s.objectCache.GetBlob(gitRepo, entry.Hash)
				if err == nil {
					blobSize := blobObj.Size
					size = &blobSize
				}
			}
		}

		entries[i] = entryInfo{
			index:     i,
			entryType: entryType,
			sortKey:   entryTypeSortKey(entryType),
			name:      entry.Name,
			fullPath:  fullPath,
			modeStr:   modeStr,
			hashStr:   hashStr,
			size:      size,
			submodule: submodule,
		}
	}

	if blobParallelism > 1 && len(blobTasks) > 0 {
		parallelism := blobParallelism
		if parallelism <= 0 {
			parallelism = 4
		}
		sem := make(chan struct{}, parallelism)
		var wg sync.WaitGroup

		// PERFORMANCE OPTIMIZATION: Use batch blob loading for parallel processing
		// Extract all hashes for batch processing
		var blobHashes []plumbing.Hash
		var taskMap = make(map[plumbing.Hash][]blobTask)
		for _, t := range blobTasks {
			blobHashes = append(blobHashes, t.hash)
			taskMap[t.hash] = append(taskMap[t.hash], t)
		}

		// Load blobs in batch (much more efficient than individual calls)
		if len(blobHashes) > 0 {
			blobResults, err := s.objectCache.GetBlobBatch(gitRepo, blobHashes)
			if err == nil {
				for hash, blob := range blobResults {
					blobSize := blob.Size
					for _, task := range taskMap[hash] {
						entries[task.idx].size = &blobSize
					}
				}
			} else {
				// Fallback to individual calls if batch fails
				for _, t := range blobTasks {
					select {
					case <-ctx.Done():
						goto waitBlobs
					default:
					}

					wg.Add(1)
					sem <- struct{}{}
					go func(task blobTask) {
						defer wg.Done()
						defer func() { <-sem }()

						blobObj, err := s.objectCache.GetBlob(gitRepo, task.hash)
						if err == nil {
							blobSize := blobObj.Size
							entries[task.idx].size = &blobSize
						}
					}(t)
				}
			}
		}

	waitBlobs:
		wg.Wait()
	}

	if withCommits {
		pathInfos := make([]CommitPathInfo, 0, len(entries))
		pathToIndex := make(map[string]int, len(entries))

		for i := range entries {
			if entries[i].entryType == "blob" || entries[i].entryType == "tree" {
				isTree := entries[i].entryType == "tree"
				pathInfos = append(pathInfos, CommitPathInfo{
					Path:   entries[i].fullPath,
					IsTree: isTree,
				})
				pathToIndex[entries[i].fullPath] = i
			}
		}

		tb := timeBudgetForEntryCount(len(entries))
		mc := maxCommitsForEntryCount(len(entries))
		commitMap, meta := s.GetLatestCommitsForPathsWithMeta(ctx, gitRepo, *refObj, pathInfos, tb, mc)

		for path, commit := range commitMap {
			if idx, ok := pathToIndex[path]; ok {
				entries[idx].commitInfo = commit
			}
		}

		sort.Slice(entries, func(i, j int) bool {
			if entries[i].sortKey != entries[j].sortKey {
				return entries[i].sortKey < entries[j].sortKey
			}
			return entries[i].name < entries[j].name
		})

		treeEntriesWithCommit := make([]GitTreeEntryWithCommit, len(entries))
		for i, e := range entries {
			treeEntriesWithCommit[i] = GitTreeEntryWithCommit{
				Type:         e.entryType,
				Name:         e.name,
				Path:         e.fullPath,
				Mode:         &e.modeStr,
				Hash:         &e.hashStr,
				Size:         e.size,
				LatestCommit: e.commitInfo,
				Submodule:    e.submodule,
			}
		}
		return treeEntriesWithCommit, meta, nil
	}

	// without commits: sort and convert with LatestCommit = nil
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].sortKey != entries[j].sortKey {
			return entries[i].sortKey < entries[j].sortKey
		}
		return entries[i].name < entries[j].name
	})

	treeEntriesWithCommit := make([]GitTreeEntryWithCommit, len(entries))
	for i, e := range entries {
		treeEntriesWithCommit[i] = GitTreeEntryWithCommit{
			Type:         e.entryType,
			Name:         e.name,
			Path:         e.fullPath,
			Mode:         &e.modeStr,
			Hash:         &e.hashStr,
			Size:         e.size,
			LatestCommit: nil,
			Submodule:    e.submodule,
		}
	}
	return treeEntriesWithCommit, emptyMeta, nil
}

// CountCommitsBetween counts commits between two commits (from excluded, to included)
// Iterates from 'to' backwards until 'from' hash is found
func (s *RepositoryOverviewService) CountCommitsBetween(repo *gogit.Repository, from, to *object.Commit) (int, error) {
	if from.Hash == to.Hash {
		return 0, nil
	}

	iter, err := repo.Log(&gogit.LogOptions{From: to.Hash})
	if err != nil {
		return 0, fmt.Errorf("failed to get commit log: %w", err)
	}
	defer iter.Close()

	count := 0
	fromHash := from.Hash.String()

	walkErr := iter.ForEach(func(c *object.Commit) error {
		if c.Hash.String() == fromHash {
			return fmt.Errorf("stop iteration")
		}
		count++
		return nil
	})

	if walkErr != nil && walkErr.Error() == "stop iteration" {
		return count, nil
	}
	if walkErr == nil {
		return count, nil
	}
	return 0, fmt.Errorf("failed to iterate commits: %w", walkErr)
}

// ============================================================================
// Phase 2 개선: 스마트 페이지네이션 적용 메서드들
// ============================================================================

// GetTreeWithCommitPaginated는 페이지네이션이 적용된 트리 조회를 제공합니다.
// Phase 2 개선: 다층 캐싱으로 567ms → 200ms 성능 개선 + 40% 반복 요청 개선을 목표로 합니다.
func (s *RepositoryOverviewService) GetTreeWithCommitPaginated(
	ctx context.Context,
	gitRepo *gogit.Repository,
	req *TreeDirectoryRequest,
	baseURL string,
) (*TreeWithCommitOverviewResponse, error) {
	startTime := time.Now()
	repoID := fmt.Sprintf("%s/%s", req.Owner, req.Repo)

	logger := s.logger.With(
		zap.String("repo_id", repoID),
		zap.String("ref", req.Ref),
		zap.String("path", req.Path),
		zap.Int("page", req.Page),
		zap.Int("per_page", req.PerPage),
	)

	logger.Info("Starting cached paginated tree retrieval")

	// Phase 2 개선: 먼저 캐시에서 전체 디렉토리 구조 조회 시도
	var fullDirectoryResponse *TreeWithCommitOverviewResponse
	var cacheHit bool

	if s.multiTierCache != nil {
		cacheStartTime := time.Now()
		cacheKey := fmt.Sprintf("tree:%s:%s:%s", repoID, req.Ref, req.Path)
		cachedResponse, found := s.multiTierCache.Get(ctx, cacheKey)

		if found {
			fullDirectoryResponse = &cachedResponse
			cacheHit = true
			logger.Info("Cache hit - full directory data retrieved from cache",
				zap.Duration("cache_lookup_time", time.Since(cacheStartTime)),
				zap.Int("cached_entries", len(cachedResponse.Tree)),
			)
		}
	}

	// 캐시 미스인 경우에만 Git에서 데이터 조회
	if !cacheHit {
		logger.Debug("Cache miss - fetching from Git repository")

		// 1. 전체 트리 구조 가져오기 (커밋 정보 없이)
		allEntries, err := s.GetTreeOnly(ctx, gitRepo, req.Ref, req.Path)
		if err != nil {
			return nil, fmt.Errorf("failed to get tree entries: %w", err)
		}

		logger.Debug("Retrieved tree structure from Git",
			zap.Int("total_entries", len(allEntries)),
			zap.Duration("tree_fetch_time", time.Since(startTime)),
		)

		// 2. 전체 엔트리에 대해 커밋 정보 조회 (캐싱을 위해)
		if len(allEntries) > 0 {
			commitStartTime := time.Now()
			entriesWithCommits, meta, err := s.getCommitInfoForEntries(ctx, gitRepo, req.Ref, allEntries)
			if err != nil {
				logger.Warn("Failed to get commit info, caching tree only", zap.Error(err))
				entriesWithCommits = allEntries
			}

			// 전체 응답 구성 (캐시 저장용)
			fullDirectoryResponse = &TreeWithCommitOverviewResponse{
				Success:    true,
				Repository: repoID,
				Owner:      req.Owner,
				Name:       req.Repo,
				Path:       req.Path,
				Tree:       entriesWithCommits,
				Meta:       &meta,
			}

			logger.Debug("Retrieved commit info from Git",
				zap.Duration("commit_fetch_time", time.Since(commitStartTime)),
				zap.Int("entries_with_commits", len(entriesWithCommits)),
			)

			// 3. 캐시에 저장 (비동기로 성능 영향 최소화)
			if s.multiTierCache != nil {
				go func() {
					cacheCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()

					storeCacheKey := fmt.Sprintf("tree:%s:%s:%s", repoID, req.Ref, req.Path)
					if err := s.multiTierCache.Set(cacheCtx, storeCacheKey, *fullDirectoryResponse); err != nil {
						logger.Warn("Failed to store in cache", zap.Error(err))
					} else {
						logger.Debug("Successfully cached directory listing",
							zap.String("repo_id", repoID),
							zap.String("cache_key", storeCacheKey),
						)
					}
				}()
			}
		}
	}

	// 4. 스마트 페이지네이션 적용
	optimalPerPage := s.smartPagination.DeterminePageSize(ctx, repoID, req.Path, req.PerPage)

	// 페이지 기본값 설정
	page := req.Page
	if page < 1 {
		page = 1
	}

	// 전체 데이터에서 페이지네이션 적용
	var allEntries []GitTreeEntryWithCommit
	if fullDirectoryResponse != nil {
		allEntries = fullDirectoryResponse.Tree
	}

	response, pagination := s.smartPagination.ApplyPagination(allEntries, page, optimalPerPage, baseURL)

	// 5. 최종 응답 구성
	response.Success = true
	response.Repository = repoID
	response.Owner = req.Owner
	response.Name = req.Repo
	response.Path = req.Path
	response.Pagination = pagination

	// 메타 정보 포함 (캐시된 데이터에서 가져온 경우)
	if fullDirectoryResponse != nil && fullDirectoryResponse.Meta != nil {
		response.Meta = fullDirectoryResponse.Meta
	}

	totalTime := time.Since(startTime)
	logger.Info("Completed cached paginated tree retrieval",
		zap.Duration("total_time", totalTime),
		zap.Bool("cache_hit", cacheHit),
		zap.Int("returned_entries", len(response.Tree)),
		zap.Int("total_files", pagination.TotalFiles),
		zap.Int("page", pagination.Page),
		zap.Int("per_page", pagination.PerPage),
	)

	// 6. 성능 모니터링에 결과 기록
	s.smartPagination.monitor.RecordResponseTime(repoID, totalTime)

	return response, nil
}

// getCommitInfoForEntries는 특정 엔트리들에 대해서만 커밋 정보를 조회합니다.
// 전체 디렉토리가 아닌 페이지에 해당하는 파일들만 처리하여 성능을 크게 개선합니다.
func (s *RepositoryOverviewService) getCommitInfoForEntries(
	ctx context.Context,
	gitRepo *gogit.Repository,
	ref string,
	entries []GitTreeEntryWithCommit,
) ([]GitTreeEntryWithCommit, LatestCommitsMeta, error) {
	if len(entries) == 0 {
		return entries, LatestCommitsMeta{}, nil
	}

	logger := s.logger.With(
		zap.String("ref", ref),
		zap.Int("entry_count", len(entries)),
	)

	startTime := time.Now()

	// 타임 버젯 설정 (페이지네이션으로 인해 더 적은 파일들을 처리하므로 여유있게)
	timeBudgetMs := 5000 // 5초
	if len(entries) < 20 {
		timeBudgetMs = 1000 // 1초 (작은 페이지는 빠르게)
	}

	// 각 엔트리를 CommitPathInfo로 변환
	pathInfos := make([]CommitPathInfo, len(entries))
	for i, entry := range entries {
		pathInfos[i] = CommitPathInfo{
			Path:   entry.Path,
			IsTree: entry.Type == "tree",
		}
	}

	logger.Debug("Starting commit lookup for paths",
		zap.Int("path_count", len(pathInfos)),
		zap.Int("time_budget_ms", timeBudgetMs),
	)

	// 기존 GetLatestCommitsForPathsWithMeta 메서드 활용 (배치 최적화)
	refObj, err := gitRepo.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		return entries, LatestCommitsMeta{}, fmt.Errorf("failed to resolve reference: %w", err)
	}

	// 커밋 정보 조회 (기존 최적화된 로직 활용)
	timeBudget := time.Duration(timeBudgetMs) * time.Millisecond
	maxCommits := 1000 // 기본값

	commitsByPath, meta := s.GetLatestCommitsForPathsWithMeta(
		ctx,
		gitRepo,
		*refObj,
		pathInfos,
		timeBudget,
		maxCommits,
	)

	// 결과를 엔트리에 적용
	for i := range entries {
		if commitInfo, exists := commitsByPath[entries[i].Path]; exists {
			entries[i].LatestCommit = commitInfo
		}
	}

	logger.Debug("Completed commit lookup",
		zap.Duration("lookup_time", time.Since(startTime)),
		zap.Int("commits_found", len(commitsByPath)),
		zap.Int("scanned_commits", meta.ScannedCommits),
	)

	return entries, meta, nil
}

// ============================================================================
// Phase 2 개선: 캐시 관리 메서드들
// ============================================================================

// Close gracefully shuts down the overview service and its caching components
func (s *RepositoryOverviewService) Close() error {
	s.logger.Info("Shutting down Repository Overview Service")

	var errors []error

	// Close multi-tier cache
	if s.multiTierCache != nil {
		if err := s.multiTierCache.Close(); err != nil {
			errors = append(errors, fmt.Errorf("multi-tier cache close error: %w", err))
		}
	}

	// Close smart pagination (if it has cleanup)
	if s.smartPagination != nil {
		// SmartPagination doesn't have a close method, so nothing to do
	}

	if len(errors) > 0 {
		return fmt.Errorf("service shutdown errors: %v", errors)
	}

	s.logger.Info("Repository Overview Service shutdown completed")
	return nil
}

// InvalidateRepositoryCache invalidates all cache entries for a specific repository
// This should be called when repository data changes (e.g., after push, merge)
func (s *RepositoryOverviewService) InvalidateRepositoryCache(ctx context.Context, repoID string) error {
	if s.multiTierCache == nil {
		return nil
	}

	s.logger.Info("Invalidating repository cache", zap.String("repo_id", repoID))

	// Pattern to match all cache entries for this repository
	pattern := fmt.Sprintf("dir:%s:*", repoID)

	err := s.multiTierCache.InvalidateByPattern(ctx, pattern)
	if err != nil {
		s.logger.Error("Failed to invalidate repository cache",
			zap.String("repo_id", repoID),
			zap.Error(err))
		return fmt.Errorf("cache invalidation failed: %w", err)
	}

	s.logger.Info("Repository cache invalidated successfully", zap.String("repo_id", repoID))
	return nil
}

// InvalidateDirectoryCache invalidates cache for a specific directory path
func (s *RepositoryOverviewService) InvalidateDirectoryCache(ctx context.Context, repoID, ref, path string) error {
	if s.multiTierCache == nil {
		return nil
	}

	cacheKey := fmt.Sprintf("tree:%s:%s:%s", repoID, ref, path)
	s.logger.Debug("Invalidating directory cache",
		zap.String("repo_id", repoID),
		zap.String("ref", ref),
		zap.String("path", path),
		zap.String("cache_key", cacheKey))

	err := s.multiTierCache.InvalidateKey(ctx, cacheKey)
	if err != nil {
		s.logger.Error("Failed to invalidate directory cache",
			zap.String("cache_key", cacheKey),
			zap.Error(err))
		return fmt.Errorf("directory cache invalidation failed: %w", err)
	}

	return nil
}

// GetCacheMetrics returns comprehensive cache performance metrics
func (s *RepositoryOverviewService) GetCacheMetrics() map[string]interface{} {
	if s.multiTierCache == nil {
		return map[string]interface{}{
			"cache_enabled": false,
			"message":       "Multi-tier cache not available",
		}
	}

	metrics := s.multiTierCache.GetMetrics()
	healthCheck := s.multiTierCache.HealthCheck(context.Background())

	return map[string]interface{}{
		"cache_enabled": true,
		"metrics": map[string]interface{}{
			"total_requests": metrics.TotalRequests,
			"l1_hits":        metrics.L1Hits,
			"l1_misses":      metrics.L1Misses,
			"l2_hits":        metrics.L2Hits,
			"l2_misses":      metrics.L2Misses,
			"warming_events": metrics.WarmingEvents,
		},
		"health":             healthCheck,
		"overall_hit_ratio":  s.multiTierCache.GetOverallHitRatio(),
		"performance_target": "90% → 98% cache hit rate, 40% improvement on repeated requests",
		"cache_info":         s.multiTierCache.GetCacheInfo(),
	}
}

// WarmDirectoryCache proactively warms cache for a directory
func (s *RepositoryOverviewService) WarmDirectoryCache(ctx context.Context, gitRepo *gogit.Repository, repoID, ref, path string) error {
	if s.multiTierCache == nil {
		return nil
	}

	s.logger.Debug("Warming directory cache",
		zap.String("repo_id", repoID),
		zap.String("ref", ref),
		zap.String("path", path))

	// Check if already cached
	cacheKey := fmt.Sprintf("tree:%s:%s:%s", repoID, ref, path)
	if _, found := s.multiTierCache.Get(ctx, cacheKey); found {
		s.logger.Debug("Directory already cached, skipping warming", zap.String("cache_key", cacheKey))
		return nil
	}

	// Fetch data to warm cache
	startTime := time.Now()
	allEntries, err := s.GetTreeOnly(ctx, gitRepo, ref, path)
	if err != nil {
		return fmt.Errorf("failed to get tree entries for warming: %w", err)
	}

	if len(allEntries) > 0 {
		// Get commit info for caching
		entriesWithCommits, meta, err := s.getCommitInfoForEntries(ctx, gitRepo, ref, allEntries)
		if err != nil {
			s.logger.Warn("Failed to get commit info for warming, using tree only", zap.Error(err))
			entriesWithCommits = allEntries
		}

		// Create full response for caching
		fullResponse := &TreeWithCommitOverviewResponse{
			Success:    true,
			Repository: repoID,
			Path:       path,
			Tree:       entriesWithCommits,
			Meta:       &meta,
		}

		// Store in cache
		err = s.multiTierCache.Set(ctx, cacheKey, *fullResponse)
		if err != nil {
			return fmt.Errorf("failed to store warmed data in cache: %w", err)
		}

		s.logger.Info("Successfully warmed directory cache",
			zap.String("repo_id", repoID),
			zap.String("cache_key", cacheKey),
			zap.Int("entries", len(entriesWithCommits)),
			zap.Duration("warming_time", time.Since(startTime)))
	}

	return nil
}
