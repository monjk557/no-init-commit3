package overview

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/collabops/git-executor/internal/blob"
	"github.com/collabops/git-executor/internal/branches"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/observability"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
	"github.com/go-git/go-git/v5/plumbing"
	"go.uber.org/zap"
)

type RepositoryOverviewHandler struct {
	config        *config.Config
	logger        *logging.Logger
	pathConfig    *repositories.PathConfig
	pathValidator *repositories.GitAPIPathValidator
	repoManager   *repositories.DefaultRepositoryManager
	blobService   *blob.BlobService
	service       *RepositoryOverviewService
	overviewCache *OverviewCache
	commitIndex   *CommitIndex
}

func NewRepositoryOverviewHandler(cfg *config.Config) (*RepositoryOverviewHandler, error) {
	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	pathValidator := repositories.NewGitAPIPathValidator(pathConfig)

	repoManager, err := repositories.NewRepositoryManager(pathConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create repository manager: %w", err)
	}

	blobService := blob.NewBlobService(pathConfig)
	service, err := NewRepositoryOverviewService(logger, pathConfig, repoManager, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create overview service: %w", err)
	}

	overviewCache := NewOverviewCache(cfg.Git.MaxCacheSize, cfg.Git.CacheTTL)
	commitIndex := NewCommitIndex(cfg.Git.MaxCacheSize, service, logger)

	return &RepositoryOverviewHandler{
		config:        cfg,
		logger:        logger,
		pathConfig:    pathConfig,
		pathValidator: pathValidator,
		repoManager:   repoManager,
		blobService:   blobService,
		service:       service,
		overviewCache: overviewCache,
		commitIndex:   commitIndex,
	}, nil
}

// repositoryContext는 저장소 관련 공통 컨텍스트를 담는 구조체입니다.
type repositoryContext struct {
	owner        string
	repo         string
	ref          string
	path         string
	repoPath     string
	repoWrapper  *repositories.RepositoryWrapper
	branchesList []BranchInfo
}

// validateAndOpenRepository는 저장소 검증 및 열기를 수행하는 공통 로직입니다.
func (h *RepositoryOverviewHandler) validateAndOpenRepository(ctx context.Context, c *gin.Context, owner, repo, ref string) (*repositoryContext, bool) {
	// ref가 제공되지 않으면 기본값으로 HEAD 사용
	if ref == "" {
		ref = "HEAD"
	}

	// 경로 구성 요소 검증
	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo)); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return nil, false
	}

	// 저장소 경로 가져오기
	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return nil, false
	}

	// Acquire read lock before any repo access so that pack writes (push/commit)
	// cannot race with our reads. The caller is responsible for releasing via:
	//   defer repositories.GetRepoRWLock(repoCtx.repoPath).RUnlock()
	repoLock := repositories.GetRepoRWLock(repoPath)
	repoLock.RLock()

	// 저장소 열기: 캐시 없이 PlainOpen만 사용 (packfile repo에서 캐시된 storer 동시 접근 시 panic 방지).
	repoWrapper, err := repositories.OpenRepositoryNoCache(repoPath)
	if err != nil {
		repoLock.RUnlock()
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return nil, false
	}

	gitRepo := repoWrapper.GetRepo()

	// 브랜치 가져오기
	branchesList, err := branches.GetBranches(gitRepo)
	if err != nil {
		h.logger.Error("overview: GetBranches failed",
			zap.String("owner", owner),
			zap.String("repo", repo),
			zap.String("repoPath", repoPath),
			zap.Error(err),
		)
		repoLock.RUnlock()
		_ = repoWrapper.Close()
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "repository corrupted"})
		return nil, false
	}

	// ref가 제공된 경우 검증 (ref를 해석 가능한지 확인)
	if ref != "" && ref != "HEAD" {
		_, err := repoWrapper.ResolveReference(ref)
		if err != nil {
			repoLock.RUnlock()
			_ = repoWrapper.Close()
			c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "reference not found"})
			return nil, false
		}
	}

	// branches.SimpleBranchInfo를 overview.BranchInfo로 변환
	// 슬라이스 용량을 미리 할당하여 재할당 방지
	overviewBranches := make([]BranchInfo, 0, len(branchesList))
	for _, b := range branchesList {
		overviewBranches = append(overviewBranches, BranchInfo{
			Name:      b.Name,
			Hash:      b.Hash,
			IsRemote:  b.IsRemote,
			IsCurrent: b.IsCurrent,
		})
	}

	return &repositoryContext{
		owner:        owner,
		repo:         repo,
		ref:          ref,
		repoPath:     repoPath,
		repoWrapper:  repoWrapper,
		branchesList: overviewBranches,
	}, true
}

// overviewTimeout is the hard request-level timeout for the overview handler.
// Individual sub-tasks respect ctx cancellation, so the total wall time is bounded.
const overviewTimeout = 15 * time.Second

// overviewCommitCountCap is the maximum number of commits to iterate when counting.
// Beyond this, the count is approximate (UI should show "100k+").
const overviewCommitCountCap = 100_000

// overviewResult holds the result of running overview parallel tasks (for retry).
type overviewResult struct {
	// native git path: *GitCommit을 직접 사용
	nativeLatestCommit    *GitCommit
	latestCommitFound     bool
	commitsCount          int
	commitsApproximate    bool
	treeEntriesWithCommit []GitTreeEntryWithCommit
	treeErr               error
	nativeTagsList        []GitTag
	remotesList           []RemoteInfo
	readmeContent         string
}

// runOverviewTasks는 native git CLI를 사용하여 overview 병렬 태스크를 실행합니다.
// go-git의 OpenRepositoryNoCache를 제거하여 repo open 오버헤드를 완전히 제거합니다 (3.2초 → <100ms).
func (h *RepositoryOverviewHandler) runOverviewTasks(ctx context.Context, repoPath, ref, path, refHash string) overviewResult {
	return h.runOverviewTasksNative(ctx, repoPath, ref, path, refHash)
}

// runOverviewTasksNative runs parallel overview sub-tasks using native git CLI.
// go-git의 OpenRepositoryNoCache를 제거하여 repo open 오버헤드를 완전히 제거합니다.
func (h *RepositoryOverviewHandler) runOverviewTasksNative(
	ctx context.Context, repoPath, ref, path, refHash string,
) overviewResult {
	var (
		mu  sync.Mutex
		res overviewResult
		wg  sync.WaitGroup
	)

	// goroutine 1: latest commit (native git log -1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		commit, err := NativeGetLatestCommit(ctx, repoPath, ref)
		mu.Lock()
		if err == nil && commit != nil {
			res.nativeLatestCommit = commit
			res.latestCommitFound = true
		}
		mu.Unlock()
	}()

	// goroutine 2: commit count (native git rev-list --count) + cache
	wg.Add(1)
	go func() {
		defer wg.Done()
		if refHash != "" {
			if cached, ok := h.overviewCache.GetCommitCount(repoPath, refHash); ok {
				mu.Lock()
				res.commitsCount = cached
				mu.Unlock()
				return
			}
		}
		count, approx, err := NativeCountCommits(ctx, repoPath, ref)
		mu.Lock()
		if err != nil {
			h.logger.Warn("Native count commits failed, using fallback", zap.Error(err))
			res.commitsCount = 1
		} else {
			res.commitsCount = count
			res.commitsApproximate = approx
			if refHash != "" && !approx {
				h.overviewCache.SetCommitCount(repoPath, refHash, count)
			}
		}
		mu.Unlock()
	}()

	// goroutine 3: tree entries (native git ls-tree — already exists)
	wg.Add(1)
	go func() {
		defer wg.Done()
		entries, err := NativeGetTreeEntries(ctx, repoPath, ref, path)
		mu.Lock()
		res.treeEntriesWithCommit = entries
		res.treeErr = err
		mu.Unlock()
	}()

	// goroutine 4: tags (native git tag -l)
	wg.Add(1)
	go func() {
		defer wg.Done()
		tl, err := NativeGetTags(ctx, repoPath, 100)
		mu.Lock()
		if err != nil {
			res.nativeTagsList = []GitTag{}
		} else {
			res.nativeTagsList = tl
		}
		mu.Unlock()
	}()

	// goroutine 5: remotes (native git remote -v)
	wg.Add(1)
	go func() {
		defer wg.Done()
		rl, err := NativeGetRemotes(ctx, repoPath)
		mu.Lock()
		if err != nil {
			res.remotesList = []RemoteInfo{}
		} else {
			res.remotesList = rl
		}
		mu.Unlock()
	}()

	// goroutine 6: README blob (native git show)
	wg.Add(1)
	go func() {
		defer wg.Done()
		readmeData, err := NativeGetBlobContent(ctx, repoPath, ref, "README.md")
		mu.Lock()
		if err == nil {
			const maxReadmeSize = 100 * 1024
			if len(readmeData) > maxReadmeSize {
				res.readmeContent = string(readmeData[:maxReadmeSize])
			} else {
				res.readmeContent = string(readmeData)
			}
		}
		mu.Unlock()
	}()

	wg.Wait()
	return res
}

// isObjectNotFoundForPack returns true if the result suggests object-not-found due to pack not yet visible (e.g. after push).
func isObjectNotFoundForPack(res overviewResult, branchesList []BranchInfo) bool {
	if res.treeErr != nil && strings.Contains(strings.ToLower(res.treeErr.Error()), "object not found") {
		return true
	}
	if !res.latestCommitFound && len(branchesList) > 0 {
		return true
	}
	return false
}

// HandleGetRepositoryOverview는 저장소 개요를 위한 GET /api/v1/repository/{owner}/{repo} 요청을 처리합니다.
// 모든 비동기 작업을 병렬로 실행하여 wall time = max(개별) 로 줄입니다.
// push 직후 pack 미반영 시 object not found가 나올 수 있으므로, 그때 캐시 무효화 후 한 번 재시도합니다.
func (h *RepositoryOverviewHandler) HandleGetRepositoryOverview(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), overviewTimeout)
	defer cancel()

	owner := c.Param("owner")
	repo := c.Param("repo")
	ref := c.Query("ref")
	path := c.Query("path")

	var repoCtx *repositoryContext
	var branchesList []BranchInfo
	var res overviewResult
	repoPath := ""

	for attempt := 0; attempt < 2; attempt++ {
		if attempt == 1 {
			repositories.GetSimpleCache().Invalidate(repoPath)
			h.logger.Debug("Overview retry after cache invalidate (pack visibility)", zap.String("repoPath", repoPath))
		}

		var ok bool
		repoCtx, ok = h.validateAndOpenRepository(ctx, c, owner, repo, ref)
		if !ok {
			return
		}
		repoPath = repoCtx.repoPath
		branchesList = repoCtx.branchesList

		// native git으로 refHash resolve (go-git ResolveReference 대체)
		refHash := ""
		if resolved, resolveErr := nativeResolveRef(ctx, repoPath, repoCtx.ref); resolveErr == nil {
			refHash = resolved
		}

		// Native git 태스크 실행 (go-git repo open 불필요)
		res = h.runOverviewTasks(ctx, repoPath, repoCtx.ref, path, refHash)

		if !isObjectNotFoundForPack(res, branchesList) {
			break
		}

		// object-not-found for pack: release this attempt's lock and repo, then retry.
		repositories.GetRepoRWLock(repoCtx.repoPath).RUnlock()
		_ = repoCtx.repoWrapper.Close()
		repoCtx = nil
	}

	// repoCtx is non-nil only when we broke out with a valid open repo (lock still held).
	// Release once here regardless of whether attempt 0 or 1 succeeded.
	if repoCtx != nil {
		defer repositories.GetRepoRWLock(repoCtx.repoPath).RUnlock()
		defer func() { _ = repoCtx.repoWrapper.Close() }()
	}

	// 빈 저장소 처리 (latestCommit을 못 찾은 경우)
	if !res.latestCommitFound {
		response := RepositoryOverviewResponse{
			Success:       true,
			Message:       "Repository overview fetched successfully",
			Repository:    fmt.Sprintf("%s/%s", owner, repo),
			Owner:         owner,
			Name:          repo,
			Path:          repoPath,
			Tree:          []GitTreeEntryWithCommit{},
			LatestCommits: GitCommit{},
			Branches:      convertBranchesToGitBranches(branchesList),
			Tags:          []GitTag{},
			Count: RepositoryCount{
				Commits:  0,
				Branches: len(branchesList),
				Tags:     0,
				Remotes:  0,
				Total:    len(branchesList),
			},
			Readme: "",
			LanguageDistribution: LanguageDistribution{
				Languages:  []LanguageStats{},
				TotalBytes: 0,
				TotalFiles: 0,
			},
		}
		c.JSON(http.StatusOK, response)
		return
	}

	// Tree 에러 처리
	if res.treeErr != nil {
		errMsg := strings.ToLower(res.treeErr.Error())
		h.logger.Error("overview: tree failed",
			zap.String("owner", owner),
			zap.String("repo", repo),
			zap.String("ref", ref),
			zap.String("path", path),
			zap.Error(res.treeErr),
		)
		if strings.Contains(errMsg, "not a tree object") ||
			strings.Contains(errMsg, "does not exist") ||
			strings.Contains(errMsg, "path not found") {
			c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "path not found"})
			return
		}
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "repository corrupted"})
		return
	}

	// Native GitCommit → 응답 GitCommit 변환
	latestCommit := GitCommit{}
	if res.nativeLatestCommit != nil {
		latestCommit = *res.nativeLatestCommit
	}

	// 응답 구성
	response := RepositoryOverviewResponse{
		Success:       true,
		Message:       "Repository overview fetched successfully",
		Repository:    fmt.Sprintf("%s/%s", owner, repo),
		Owner:         owner,
		Name:          repo,
		Path:          repoPath,
		Tree:          res.treeEntriesWithCommit,
		LatestCommits: latestCommit,
		Branches:      convertBranchesToGitBranches(branchesList),
		Tags:          res.nativeTagsList,
		Count: RepositoryCount{
			Commits:            res.commitsCount,
			Branches:           len(branchesList),
			Tags:               len(res.nativeTagsList),
			Remotes:            len(res.remotesList),
			Total:              res.commitsCount + len(branchesList) + len(res.nativeTagsList) + len(res.remotesList),
			CommitsApproximate: res.commitsApproximate,
		},
		Readme: res.readmeContent,
		LanguageDistribution: LanguageDistribution{
			Languages:  []LanguageStats{},
			TotalBytes: 0,
			TotalFiles: 0,
		},
	}

	c.JSON(http.StatusOK, response)
}

// HandleGetTreeOverview는 트리 개요를 위한 GET /api/v1/repository/{owner}/{repo}/tree 요청을 처리합니다.
// Native git 엔진 사용: go-git OpenRepositoryNoCache → native git exec.
func (h *RepositoryOverviewHandler) HandleGetTreeOverview(c *gin.Context) {
	ctx := c.Request.Context()
	owner := c.Param("owner")
	repo := c.Param("repo")
	ref := c.Query("ref")
	path := c.Query("path")

	// 공통 검증 및 저장소 열기 (RLock + 브랜치 목록)
	repoCtx, ok := h.validateAndOpenRepository(ctx, c, owner, repo, ref)
	if !ok {
		return
	}
	defer repositories.GetRepoRWLock(repoCtx.repoPath).RUnlock()
	defer func() { _ = repoCtx.repoWrapper.Close() }()

	// 트리 항목 (native git ls-tree)
	treeEntries, err := NativeGetTreeEntries(ctx, repoCtx.repoPath, repoCtx.ref, path)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "repository corrupted"})
		return
	}

	// path 기준 최신 커밋 (native git log -1 -- path)
	var latestCommit *GitCommit
	if path != "" {
		latestCommit, _ = NativeGetLatestCommitForPath(ctx, repoCtx.repoPath, repoCtx.ref, path)
	} else {
		latestCommit, _ = NativeGetLatestCommit(ctx, repoCtx.repoPath, repoCtx.ref)
	}

	// 커밋 목록 (native git log)
	var commitsList []GitCommit
	if c.Query("include_all_commits") == "true" {
		const maxCommitsForList = 1000
		commitsList, err = NativeGetCommits(ctx, repoCtx.repoPath, repoCtx.ref, maxCommitsForList)
		if err != nil {
			h.logger.Warn("Failed to get all commits for tree overview", zap.Error(err))
			commitsList = []GitCommit{}
		}
	} else if latestCommit != nil {
		commitsList = []GitCommit{*latestCommit}
	}

	if commitsList == nil {
		commitsList = []GitCommit{}
	}

	response := TreeOverviewResponse{
		Success:      true,
		Repository:   fmt.Sprintf("%s/%s", owner, repo),
		Owner:        owner,
		Name:         repo,
		Path:         path,
		Tree:         treeEntries,
		Branches:     convertBranchesToGitBranches(repoCtx.branchesList),
		LatestCommit: latestCommit,
		Commits:      commitsList,
	}

	c.JSON(http.StatusOK, response)
}

// HandleGetBlobOverview는 블럭 개요를 위한 GET /api/v1/repository/{owner}/{repo}/blob 요청을 처리합니다.
// Native git 엔진 사용: go-git → native git exec.
func (h *RepositoryOverviewHandler) HandleGetBlobOverview(c *gin.Context) {
	ctx := c.Request.Context()
	owner := c.Param("owner")
	repo := c.Param("repo")
	path := c.Query("path")
	ref := c.Query("ref")

	if path == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "Missing required parameter: path",
		})
		return
	}

	// 공통 검증 및 저장소 열기 (RLock + 브랜치 목록)
	repoCtx, ok := h.validateAndOpenRepository(ctx, c, owner, repo, ref)
	if !ok {
		return
	}
	defer repositories.GetRepoRWLock(repoCtx.repoPath).RUnlock()
	defer func() { _ = repoCtx.repoWrapper.Close() }()

	// blob 내용 조회 (native git show)
	blobContent, err := NativeGetBlobContent(ctx, repoCtx.repoPath, repoCtx.ref, path)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "file not found"})
		return
	}

	lines := strings.Count(strings.ReplaceAll(string(blobContent), "\r\n", "\n"), "\n") + 1
	size := int64(len(blobContent))

	// 부모 디렉터리 트리 (native git ls-tree)
	parentPath := ""
	if idx := strings.LastIndex(path, "/"); idx != -1 {
		parentPath = path[:idx]
	}
	treeEntries, err := NativeGetTreeEntries(ctx, repoCtx.repoPath, repoCtx.ref, parentPath)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "repository corrupted"})
		return
	}

	// path 기준 최신 커밋 (native git log -1 -- path)
	latestCommit, _ := NativeGetLatestCommitForPath(ctx, repoCtx.repoPath, repoCtx.ref, path)

	// 커밋 목록 (native git log)
	var commitsList []GitCommit
	if c.Query("include_all_commits") == "true" {
		const maxCommitsForList = 1000
		commitsList, err = NativeGetCommits(ctx, repoCtx.repoPath, repoCtx.ref, maxCommitsForList)
		if err != nil {
			h.logger.Warn("Failed to get all commits for blob overview", zap.Error(err))
			commitsList = []GitCommit{}
		}
	} else if latestCommit != nil {
		commitsList = []GitCommit{*latestCommit}
	}

	if commitsList == nil {
		commitsList = []GitCommit{}
	}

	response := BlobOverviewResponse{
		Success:      true,
		Content:      string(blobContent),
		Lines:        lines,
		Size:         size,
		Tree:         treeEntries,
		Branches:     convertBranchesToGitBranches(repoCtx.branchesList),
		LatestCommit: latestCommit,
		Commits:      commitsList,
	}

	c.JSON(http.StatusOK, response)
}

// HandleGetBranchesOverview는 브랜치 개요를 위한 GET /api/v1/repository/{owner}/{repo}/overview/branches 요청을 처리합니다
func (h *RepositoryOverviewHandler) HandleGetBranchesOverview(c *gin.Context) {
	owner := c.Param("owner")
	repo := c.Param("repo")
	ref := c.Query("ref")
	userEmail := c.Query("email")

	// email 파라미터 필수 검증 (v1과 일관성 유지)
	if userEmail == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "Missing required parameter: email",
		})
		return
	}

	// ref가 제공되지 않으면 기본값으로 HEAD 사용
	if ref == "" {
		ref = "HEAD"
	}

	// 경로 구성 요소 검증
	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo)); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return
	}

	// 저장소 경로 가져오기
	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return
	}

	// 캐시 없이 열기 (packfile repo panic 방지)
	repoWrapper, err := repositories.OpenRepositoryNoCache(repoPath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return
	}
	defer func() { _ = repoWrapper.Close() }()

	gitRepo := repoWrapper.GetRepo()

	// ref가 제공된 경우 검증 (ref를 해석 가능한지 확인)
	if ref != "" && ref != "HEAD" {
		_, err := repoWrapper.ResolveReference(ref)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "reference not found"})
			return
		}
	}

	// 모든 참조(브랜치) 조회
	refs, err := gitRepo.References()
	if err != nil {
		h.logger.Error("failed to get references", zap.Error(err))
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "repository corrupted"})
		return
	}
	defer refs.Close()

	var defaultBranches []BranchListItem
	var yoursBranches []BranchListItem
	var activeBranches []BranchListItem
	var staleBranches []BranchListItem
	var defaultBranch string
	allBranchesCount := 0

	// 기본 브랜치 확인
	headRef, err := gitRepo.Head()
	if err == nil {
		defaultBranch = headRef.Name().Short()
	}

	// 각 참조 처리
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		// 브랜치 참조만 처리
		if ref.Name().IsBranch() {
			allBranchesCount++
			branchName := ref.Name().Short()

			// 커밋 정보 조회
			commit, err := gitRepo.CommitObject(ref.Hash())
			if err != nil {
				h.logger.Warn("Failed to get commit for branch",
					zap.String("branch", branchName),
					zap.Error(err))
				return nil // 다른 브랜치 계속 처리
			}

			// 커밋 작성자 가져오기
			author := commit.Author.Name
			if author == "" {
				author = commit.Author.Email
			}

			// 마지막 커밋 이후 경과 일수 계산
			daysSinceLastCommit := int(time.Since(commit.Author.When).Hours() / 24)

			// 분류 신호 계산
			isStale := daysSinceLastCommit >= 120
			isYours := false
			if userEmail != "" {
				isYours = strings.EqualFold(commit.Author.Email, userEmail) ||
					strings.EqualFold(commit.Author.Name, userEmail)
			}

			// 브랜치 아이템 생성; 정렬/표시는 최신 커밋 시간 기준
			sortTime := commit.Author.When
			branchItem := BranchListItem{
				Name:                branchName,
				Updated:             sortTime.Format("Jan 2, 2006"),
				UpdatedBy:           author,
				CommitSHA:           commit.Hash.String()[:8],
				CommitMessage:       commit.Message,
				IsDefault:           branchName == defaultBranch,
				Behind:              0,
				Ahead:               0,
				CreatedBy:           commit.Author.Email,
				DaysSinceLastCommit: daysSinceLastCommit,
				SortTime:            sortTime,
			}

			// 기본 브랜치 대비 앞섬/뒤처짐 계산(GitHub 스타일)
			if defaultBranch != "" && branchName != defaultBranch {
				// 기본 브랜치 커밋 해시 조회
				defaultRefName := plumbing.ReferenceName(fmt.Sprintf("refs/heads/%s", defaultBranch))
				defaultRef, derr := gitRepo.Reference(defaultRefName, false)
				if derr == nil {
					defaultCommit, derr := gitRepo.CommitObject(defaultRef.Hash())
					if derr == nil {
						// 공통 조상(merge base) 찾기
						mergeBases, mErr := commit.MergeBase(defaultCommit)
						if mErr == nil && len(mergeBases) > 0 {
							mergeBase := mergeBases[0]
							// 앞선 커밋 수 계산(브랜치→기본)
							aheadCount, aErr := h.service.CountCommitsBetween(gitRepo, mergeBase, commit)
							if aErr == nil {
								branchItem.Ahead = aheadCount
							}
							// 뒤처진 커밋 수 계산(기본→브랜치)
							behindCount, bErr := h.service.CountCommitsBetween(gitRepo, mergeBase, defaultCommit)
							if bErr == nil {
								branchItem.Behind = behindCount
							}
						}
					}
				}
			}

			// 그룹 분류(기본 제외하고 중복 포함 가능)
			if branchItem.IsDefault {
				defaultBranches = append(defaultBranches, branchItem)
			} else {
				if isYours {
					yoursBranches = append(yoursBranches, branchItem)
				}
				if isStale {
					staleBranches = append(staleBranches, branchItem)
				} else {
					// Active: 최근 업데이트, 기본 브랜치 아님
					activeBranches = append(activeBranches, branchItem)
				}
			}
		}
		return nil
	})

	if err != nil {
		h.logger.Error("Error processing references", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "Error processing references"})
		return
	}

	// 최신 생성/업데이트 순(내림차순)으로 정렬하고 Yours/Active/Stale는 각 5개로 제한
	sort.Slice(yoursBranches, func(i, j int) bool {
		if yoursBranches[i].SortTime.Equal(yoursBranches[j].SortTime) {
			return yoursBranches[i].Name > yoursBranches[j].Name
		}
		return yoursBranches[i].SortTime.After(yoursBranches[j].SortTime)
	})
	if len(yoursBranches) > 5 {
		yoursBranches = yoursBranches[:5]
	}

	sort.Slice(activeBranches, func(i, j int) bool {
		if activeBranches[i].SortTime.Equal(activeBranches[j].SortTime) {
			return activeBranches[i].Name > activeBranches[j].Name
		}
		return activeBranches[i].SortTime.After(activeBranches[j].SortTime)
	})
	if len(activeBranches) > 5 {
		activeBranches = activeBranches[:5]
	}

	sort.Slice(staleBranches, func(i, j int) bool {
		if staleBranches[i].SortTime.Equal(staleBranches[j].SortTime) {
			return staleBranches[i].Name > staleBranches[j].Name
		}
		return staleBranches[i].SortTime.After(staleBranches[j].SortTime)
	})
	if len(staleBranches) > 5 {
		staleBranches = staleBranches[:5]
	}

	// nil 슬라이스를 빈 배열로 변환 (JSON에서 null 대신 [] 반환)
	if defaultBranches == nil {
		defaultBranches = []BranchListItem{}
	}
	if yoursBranches == nil {
		yoursBranches = []BranchListItem{}
	}
	if activeBranches == nil {
		activeBranches = []BranchListItem{}
	}
	if staleBranches == nil {
		staleBranches = []BranchListItem{}
	}

	// 응답 생성
	response := BranchesOverviewResponse{
		Success:    true,
		Default:    defaultBranches,
		Yours:      yoursBranches,
		Active:     activeBranches,
		Stale:      staleBranches,
		TotalCount: allBranchesCount,
	}

	h.logger.Info("Successfully retrieved branches overview",
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.Int("total_branches", allBranchesCount))

	c.JSON(http.StatusOK, response)
}

// HandleGetTreeWithCommitOverview는 트리와 커밋 정보를 함께 반환하는 GET /api/v1/repository/{owner}/{repo}/overview/tree/commits 요청을 처리합니다.
//
// Native git 엔진 사용: go-git의 DiffTree (9초) → native git log (6ms), 1,500배 개선.
func (h *RepositoryOverviewHandler) HandleGetTreeWithCommitOverview(c *gin.Context) {
	ctx := c.Request.Context()
	start := time.Now()
	m := observability.GetApplicationMetrics()

	owner := c.Param("owner")
	repo := c.Param("repo")
	ref := c.Query("ref")
	path := c.Query("path")

	// 공통 검증 및 저장소 열기 (RLock은 내부에서 획득)
	repoCtx, ok := h.validateAndOpenRepository(ctx, c, owner, repo, ref)
	if !ok {
		return
	}
	defer repositories.GetRepoRWLock(repoCtx.repoPath).RUnlock()
	defer func() { _ = repoCtx.repoWrapper.Close() }()

	repoCtx.path = path

	// --- NATIVE GIT PATH ---
	// 1. 트리 구조 로드 (native git ls-tree).
	treeEntries, err := NativeGetTreeEntries(ctx, repoCtx.repoPath, repoCtx.ref, repoCtx.path)
	if err != nil {
		m.RecordTreeWithCommit("error", time.Since(start), 0)
		// git ls-tree 실패는 거의 항상 경로 문제 (존재하지 않음, 파일 경로, 잘못된 ref 등).
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "path not found"})
		return
	}

	// 2. 각 엔트리의 최신 커밋 조회 (native git log, 동시 실행).
	paths := make([]string, 0, len(treeEntries))
	for _, e := range treeEntries {
		if e.Type == "blob" || e.Type == "tree" {
			paths = append(paths, e.Path)
		}
	}

	commitMap, meta := GetLatestCommitsNative(ctx, repoCtx.repoPath, repoCtx.ref, paths)

	// 3. 트리 엔트리에 커밋 정보 오버레이.
	for i := range treeEntries {
		if commit, ok := commitMap[treeEntries[i].Path]; ok {
			treeEntries[i].LatestCommit = commit
		}
	}

	m.RecordTreeWithCommit("native_complete", time.Since(start), len(treeEntries))

	if h.logger != nil {
		h.logger.Debug("tree/commits native git",
			zap.String("repo", fmt.Sprintf("%s/%s", owner, repo)),
			zap.String("path", path),
			zap.Int("entries", len(treeEntries)),
			zap.Int("resolved", len(commitMap)),
			zap.Duration("elapsed", time.Since(start)),
		)
	}

	response := TreeWithCommitOverviewResponse{
		Success:    true,
		Repository: fmt.Sprintf("%s/%s", owner, repo),
		Owner:      owner,
		Name:       repo,
		Path:       path,
		Tree:       treeEntries,
		Meta:       &meta,
	}

	c.JSON(http.StatusOK, response)
}

func (h *RepositoryOverviewHandler) HandlePostLatestCommitsBatch(c *gin.Context) {
	ctx := c.Request.Context()
	start := time.Now()
	m := observability.GetApplicationMetrics()

	owner := c.Param("owner")
	repo := c.Param("repo")

	var req LatestCommitsBatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid request body"})
		return
	}
	if strings.TrimSpace(req.Ref) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "Missing required field: ref"})
		return
	}
	if len(req.Paths) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "Missing required field: paths"})
		return
	}

	dirPath := strings.Trim(req.DirPath, "/")
	if dirPath != "" {
		if err := h.pathValidator.ValidateFilePath(dirPath); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
			return
		}
	}

	// 공통 검증 및 저장소 열기 (RLock은 내부에서 획득)
	repoCtx, ok := h.validateAndOpenRepository(ctx, c, owner, repo, req.Ref)
	if !ok {
		return
	}
	defer repositories.GetRepoRWLock(repoCtx.repoPath).RUnlock()
	defer func() { _ = repoCtx.repoWrapper.Close() }()

	// resolved ref hash (fixed)
	resolvedHash, err := repoCtx.repoWrapper.ResolveReference(repoCtx.ref)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "reference not found"})
		return
	}
	resolvedRefHash := resolvedHash.String()

	// Normalize/validate paths and build CommitPathInfo
	seen := make(map[string]struct{}, len(req.Paths))
	pathInfos := make([]CommitPathInfo, 0, len(req.Paths))
	for _, p := range req.Paths {
		p = strings.Trim(p, "/")
		if p == "" {
			continue
		}

		full := p
		if dirPath != "" {
			full = dirPath + "/" + p
		}

		if err := h.pathValidator.ValidateFilePath(full); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
			return
		}

		if _, ok := seen[full]; ok {
			continue
		}
		seen[full] = struct{}{}

		// Policy: this endpoint resolves latest commits for exact file paths by default.
		pathInfos = append(pathInfos, CommitPathInfo{Path: full, IsTree: false})
	}
	if len(pathInfos) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "paths must not be empty"})
		return
	}

	// Cache key: dirPath + stable hash(paths)
	sortedFull := make([]string, 0, len(seen))
	for full := range seen {
		sortedFull = append(sortedFull, full)
	}
	sort.Strings(sortedFull)
	hh := sha1.Sum([]byte(strings.Join(sortedFull, "\n")))
	pathsHash := hex.EncodeToString(hh[:])
	cachePathKey := dirPath + "|" + pathsHash

	// Cache hit: check complete first, then partial (anti-thundering).
	if cachedCommits, isComplete, found := h.overviewCache.GetLatestCommitsBatchAny(repoCtx.repoPath, resolvedRefHash, cachePathKey); found {
		cutReason := "cache_hit"
		incomplete := !isComplete
		if !isComplete {
			cutReason = "cache_hit_partial"
		}
		m.RecordLatestCommitsBatch(cutReason, time.Since(start), len(pathInfos), 0)
		resp := LatestCommitsBatchResponse{
			Success:       true,
			Repository:    fmt.Sprintf("%s/%s", owner, repo),
			Owner:         owner,
			Name:          repo,
			DirPath:       dirPath,
			CommitsByPath: cachedCommits,
			Meta: LatestCommitsMeta{
				ResolvedRefHash: resolvedRefHash,
				Incomplete:      incomplete,
				ScannedCommits:  0,
				TimeBudgetMs:    0,
				CutReason:       cutReason,
			},
		}
		c.JSON(http.StatusOK, resp)
		return
	}

	// Compute with singleflight (enforced for BOTH partial and complete — thundering herd prevention).
	sfKey := fmt.Sprintf("latest_commits_batch|%s|%s|%s", repoCtx.repoPath, resolvedRefHash, cachePathKey)
	v, err, shared := h.overviewCache.Do(sfKey, func() (any, error) {
		timeBudget := latestCommitsTimeBudget()
		maxCommits := latestCommitsMaxCommits()

		commits, meta := h.service.GetLatestCommitsForPathsWithMeta(ctx, repoCtx.repoWrapper.GetRepo(), resolvedHash, pathInfos, timeBudget, maxCommits)
		// Cache complete → long TTL, partial → short TTL (2s, anti-thundering).
		if !meta.Incomplete {
			h.overviewCache.SetLatestCommitsBatch(repoCtx.repoPath, resolvedRefHash, cachePathKey, commits)
		} else {
			h.overviewCache.SetLatestCommitsBatchPartial(repoCtx.repoPath, resolvedRefHash, cachePathKey, commits)
		}
		return struct {
			commits map[string]*GitCommit
			meta    LatestCommitsMeta
		}{commits: commits, meta: meta}, nil
	})
	if err != nil {
		m.RecordLatestCommitsBatch("error", time.Since(start), len(pathInfos), 0)
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "repository corrupted"})
		return
	}
	if shared {
		m.RecordOverviewSingleflightShared("latest_commits_batch")
	}

	out := v.(struct {
		commits map[string]*GitCommit
		meta    LatestCommitsMeta
	})
	out.meta.ResolvedRefHash = resolvedRefHash

	// Missing paths list (relative to dirPath input)
	missing := make([]string, 0)
	for _, p := range req.Paths {
		p = strings.Trim(p, "/")
		if p == "" {
			continue
		}
		full := p
		if dirPath != "" {
			full = dirPath + "/" + p
		}
		if _, ok := out.commits[full]; !ok {
			missing = append(missing, p)
		}
	}
	sort.Strings(missing)

	outcome := "complete"
	if out.meta.Incomplete {
		outcome = "incomplete"
		m.RecordLatestCommitsBatchCutReason(out.meta.CutReason)
	}
	m.RecordLatestCommitsBatch(outcome, time.Since(start), len(pathInfos), out.meta.ScannedCommits)

	resp := LatestCommitsBatchResponse{
		Success:       true,
		Repository:    fmt.Sprintf("%s/%s", owner, repo),
		Owner:         owner,
		Name:          repo,
		DirPath:       dirPath,
		CommitsByPath: out.commits,
		Meta:          out.meta,
		MissingPaths:  missing,
	}
	c.JSON(http.StatusOK, resp)
}
