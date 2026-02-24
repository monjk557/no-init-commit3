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
}

// NewRepositoryOverviewService는 새로운 RepositoryOverviewService를 생성합니다.
func NewRepositoryOverviewService(logger *logging.Logger, pathConfig *repositories.PathConfig, repoManager *repositories.DefaultRepositoryManager) (*RepositoryOverviewService, error) {
	objectManager, err := repositories.NewGitObjectManager(pathConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create object manager: %w", err)
	}

	// Initialize performance configuration and optimizers
	perfConfig := performance.NewPerformanceConfig()
	commitGraphOpt := performance.NewCommitGraphOptimizer(perfConfig, logger.Logger)
	objectCache := performance.NewObjectCache(perfConfig, logger.Logger)
	fdPool := performance.NewFileDescriptorPool(perfConfig, logger.Logger)

	return &RepositoryOverviewService{
		logger:         logger,
		pathConfig:     pathConfig,
		repoManager:    repoManager,
		objectManager:  objectManager,
		perfConfig:     perfConfig,
		commitGraphOpt: commitGraphOpt,
		objectCache:    objectCache,
		fdPool:         fdPool,
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

// entryInfo는 각 tree entry의 기본 정보를 담는 내부 타입입니다.
type entryInfo struct {
	index      int
	entryType  string
	sortKey    int // pre-computed: 0=tree,1=blob,2=commit,3=other (avoids switch per comparison)
	name       string
	fullPath   string
	modeStr    string
	hashStr    string
	size       *int64
	commitInfo *GitCommit
}

// entryTypeSortKey returns a pre-computed sort priority for entry type.
func entryTypeSortKey(et string) int {
	switch et {
	case "tree":
		return 0
	case "blob":
		return 1
	case "commit":
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
			entryType = "commit"
		default:
			entryType = "blob"
		}

		modeStr := strconv.FormatUint(uint64(entry.Mode), 8)
		hashStr := entry.Hash.String()

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
