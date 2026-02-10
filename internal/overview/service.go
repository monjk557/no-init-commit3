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
	"github.com/go-git/go-git/v5/plumbing/filemode"
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
			// 하위 경로인지 확인 (디렉토리 경로 자체는 커밋에 포함되지 않으므로 하위 파일만 체크)
			// normalizedPath가 "src"인 경우 "src/main.go", "src/utils/helper.go" 등을 포함
			normalizedPathWithSlash := normalizedPath + "/"
			pathFilter = func(path string) bool {
				return strings.HasPrefix(path, normalizedPathWithSlash) || path == normalizedPath
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
		// 첫 번째 커밋만 가져오고 중지 (성능 최적화)
		return io.EOF
	})

	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("failed to iterate commits: %w", err)
	}

	if latestCommit == nil {

		fallbackIter, err := repo.Log(&gogit.LogOptions{
			From: commitHash,
		})
		if err != nil {
			return nil, nil
		}
		defer fallbackIter.Close()

		err = fallbackIter.ForEach(func(c *object.Commit) error {
			currentTree, err := c.Tree()
			if err != nil {
				return nil
			}

			var parentTree *object.Tree
			if len(c.ParentHashes) > 0 {
				parentCommit, err := repo.CommitObject(c.ParentHashes[0])
				if err == nil {
					parentTree, _ = parentCommit.Tree()
				}
			}

			var currEntry *object.TreeEntry
			var parentEntry *object.TreeEntry

			if normalizedPath != "" {
				currEntry, _ = currentTree.FindEntry(normalizedPath)
				if parentTree != nil {
					parentEntry, _ = parentTree.FindEntry(normalizedPath)
				}
			} else {
				// 루트 경로는 항상 트리 존재
				currEntry = &object.TreeEntry{}
				if parentTree != nil {
					parentEntry = &object.TreeEntry{}
				}
			}

			// 둘 다 없으면 변화 없음
			if currEntry == nil && parentEntry == nil {
				return nil
			}

			// 신규 생성 / 삭제 / hash 변경
			if currEntry == nil || parentEntry == nil ||
				currEntry.Hash != parentEntry.Hash {
				latestCommit = c
				return io.EOF
			}

			return nil
		})

		if err != nil && err != io.EOF {
			return nil, nil
		}

		if latestCommit == nil {
			return nil, nil
		}
	}

	// GitCommit으로 변환
	return s.convertCommit(latestCommit), nil
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
	type pendingPath struct {
		info CommitPathInfo
	}
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

		for path, p := range pending {
			if diffMatchesPath(changes, path, p.info.IsTree) {
				result[path] = s.convertCommit(c)
				delete(pending, path)
			}
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

	found := false
	_ = tree.Files().ForEach(func(f *object.File) error {
		if f.Name == path || strings.HasPrefix(f.Name, path+"/") {
			found = true
			return io.EOF
		}
		return nil
	})
	return found
}

func diffMatchesPath(changes object.Changes, path string, isTree bool) bool {
	for _, ch := range changes {
		from := ch.From.Name
		to := ch.To.Name

		if !isTree {
			if from == path || to == path {
				return true
			}
			continue
		}

		if path == "" {
			return true
		}

		if from == path || to == path ||
			strings.HasPrefix(from, path+"/") ||
			strings.HasPrefix(to, path+"/") {
			return true
		}
	}
	return false
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
	name       string
	fullPath   string
	modeStr    string
	hashStr    string
	size       *int64
	commitInfo *GitCommit
}

// GetTreeWithCommit는 트리 항목과 각 항목의 최근 커밋 정보를 함께 가져옵니다.
func (s *RepositoryOverviewService) GetTreeWithCommit(ctx context.Context, gitRepo *gogit.Repository, ref, path string) ([]GitTreeEntryWithCommit, error) {
	// ref를 커밋 해시로 해석
	refObj, err := gitRepo.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		return nil, fmt.Errorf("failed to resolve reference: %w", err)
	}

	// Get commit
	commit, err := gitRepo.CommitObject(*refObj)
	if err != nil {
		return nil, fmt.Errorf("failed to get commit: %w", err)
	}

	// Get tree
	treeObj, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get tree: %w", err)
	}

	// Navigate to the specified path
	targetTree := treeObj
	if path != "" {
		treeEntry, err := treeObj.FindEntry(path)
		if err != nil {
			return nil, fmt.Errorf("failed to find path: %w", err)
		}
		if treeEntry.Mode != filemode.Dir {
			return nil, fmt.Errorf("path is not a tree: %s", path)
		}
		targetTree, err = gitRepo.TreeObject(treeEntry.Hash)
		if err != nil {
			return nil, fmt.Errorf("failed to get target tree: %w", err)
		}
	}

	// Normalize base path (remove leading/trailing slashes)
	basePath := strings.Trim(path, "/")

	entries := make([]entryInfo, len(targetTree.Entries))
	for i, entry := range targetTree.Entries {
		// Path는 full path from repository root
		fullPath := entry.Name
		if basePath != "" {
			fullPath = basePath + "/" + entry.Name
		}

		// Entry type 결정
		var entryType string
		switch entry.Mode {
		case filemode.Dir:
			entryType = "tree"
		case filemode.Submodule:
			entryType = "commit"
		default:
			entryType = "blob"
		}

		// Mode를 문자열로 변환 (Unix permissions)
		modeStr := fmt.Sprintf("%o", entry.Mode)
		// Hash를 문자열로 변환
		hashStr := entry.Hash.String()

		// Size 계산 (blob인 경우만)
		var size *int64
		if entryType == "blob" {
			// Blob 객체 가져오기
			blobObj, err := gitRepo.BlobObject(entry.Hash)
			if err == nil {
				blobSize := blobObj.Size
				size = &blobSize
			}
		}

		entries[i] = entryInfo{
			index:     i,
			entryType: entryType,
			name:      entry.Name,
			fullPath:  fullPath,
			modeStr:   modeStr,
			hashStr:   hashStr,
			size:      size,
		}
	}

	// 커밋 정보를 병렬로 조회
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

	// 병렬로 커밋 정보 조회
	commitMap := s.GetLatestCommitsForPaths(ctx, gitRepo, ref, pathInfos)

	// 결과를 entries에 매핑
	for path, commit := range commitMap {
		if idx, ok := pathToIndex[path]; ok {
			entries[idx].commitInfo = commit
		}
	}

	// 정렬: 트리(디렉토리)가 먼저 오고, 그 다음 blob(파일), 같은 타입 내에서는 파일명 아스키 정렬
	sort.Slice(entries, func(i, j int) bool {
		// 타입별 우선순위 결정
		var typeI, typeJ int
		switch entries[i].entryType {
		case "tree":
			typeI = 0
		case "blob":
			typeI = 1
		case "commit":
			typeI = 2
		default:
			typeI = 3
		}

		switch entries[j].entryType {
		case "tree":
			typeJ = 0
		case "blob":
			typeJ = 1
		case "commit":
			typeJ = 2
		default:
			typeJ = 3
		}

		// 타입이 다르면 타입 우선순위로 정렬 (작은 값이 먼저)
		if typeI != typeJ {
			return typeI < typeJ
		}

		// 같은 타입이면 파일명으로 아스키 정렬
		return entries[i].name < entries[j].name
	})

	// 결과를 정렬된 순서대로 변환
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

	return treeEntriesWithCommit, nil
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
