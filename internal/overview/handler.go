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
	"github.com/collabops/git-executor/internal/commits"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/observability"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/collabops/git-executor/internal/tags"
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
	service, err := NewRepositoryOverviewService(logger, pathConfig, repoManager)
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

	// 저장소 열기 (존재 여부는 OpenRepository에서 확인)
	repoWrapper, err := h.repoManager.OpenRepository(repoPath)
	if err != nil {
		// repository not found 또는 corrupted
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return nil, false
	}

	gitRepo := repoWrapper.GetRepo()

	// 브랜치 가져오기
	branchesList, err := branches.GetBranches(gitRepo)
	if err != nil {
		_ = repoWrapper.Close()
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "repository corrupted"})
		return nil, false
	}

	// ref가 제공된 경우 검증 (ref를 해석 가능한지 확인)
	if ref != "" && ref != "HEAD" {
		// ref를 해석해서 유효한지 확인
		_, err := repoWrapper.ResolveReference(ref)
		if err != nil {
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

// HandleGetRepositoryOverview는 저장소 개요를 위한 GET /api/v1/repository/{owner}/{repo} 요청을 처리합니다.
// 모든 비동기 작업을 병렬로 실행하여 wall time = max(개별) 로 줄입니다.
func (h *RepositoryOverviewHandler) HandleGetRepositoryOverview(c *gin.Context) {
	// Request-level timeout: 어떤 sub-task도 이 시간을 넘기지 못하게 강제합니다.
	ctx, cancel := context.WithTimeout(c.Request.Context(), overviewTimeout)
	defer cancel()

	owner := c.Param("owner")
	repo := c.Param("repo")
	ref := c.Query("ref")
	path := c.Query("path")

	// 공통 검증 및 저장소 열기
	repoCtx, ok := h.validateAndOpenRepository(ctx, c, owner, repo, ref)
	if !ok {
		return
	}
	defer func() { _ = repoCtx.repoWrapper.Close() }()

	gitRepo := repoCtx.repoWrapper.GetRepo()
	branchesList := repoCtx.branchesList
	repoPath := repoCtx.repoPath
	repoCtx.path = path

	// ------------------------------------------------------------------
	// Resolve refHash once (fast, no I/O) — used by cache + all sub-tasks.
	// ------------------------------------------------------------------
	refHash := ""
	if resolvedHash, resolveErr := repoCtx.repoWrapper.ResolveReference(repoCtx.ref); resolveErr == nil {
		refHash = resolvedHash.String()
	}

	// ------------------------------------------------------------------
	// 모든 sub-task를 병렬 실행: wall time = max(개별)
	// GetCommits(1,0)도 병렬로 포함하여 sequential dependency 제거
	// ------------------------------------------------------------------
	var (
		mu                    sync.Mutex
		latestCommit          commits.Commit
		latestCommitFound     bool
		commitsCount          int
		commitsApproximate    bool
		treeEntriesWithCommit []GitTreeEntryWithCommit
		treeErr               error
		tagsList              []tags.Tag
		remotesList           []RemoteInfo
		readmeContent         string
	)

	var wg sync.WaitGroup

	// (0) GetCommits(1,0) — 빈 저장소 판별 + latestCommit (이전: sequential blocker)
	wg.Add(1)
	go func() {
		defer wg.Done()
		commitsList, err := commits.GetCommits(repoCtx.repoWrapper, ctx, repoCtx.ref, 1, 0)
		mu.Lock()
		if err == nil && len(commitsList) > 0 {
			latestCommit = commitsList[0]
			latestCommitFound = true
		}
		mu.Unlock()
	}()

	// (1) CountCommits — capped + cached
	wg.Add(1)
	go func() {
		defer wg.Done()

		// Cache hit?
		if refHash != "" {
			if cached, ok := h.overviewCache.GetCommitCount(repoPath, refHash); ok {
				mu.Lock()
				commitsCount = cached
				mu.Unlock()
				return
			}
		}

		r, err := commits.CountCommitsWithCap(repoCtx.repoWrapper, ctx, repoCtx.ref, overviewCommitCountCap)
		mu.Lock()
		if err != nil {
			h.logger.Warn("Failed to count commits, using fallback", zap.Error(err))
			commitsCount = 1
		} else {
			commitsCount = r.Count
			commitsApproximate = r.Approximate
			if refHash != "" {
				h.overviewCache.SetCommitCount(repoPath, refHash, r.Count)
			}
		}
		mu.Unlock()
	}()

	// (2) GetTreeOnly — 트리 구조만 (커밋 정보 없음)
	wg.Add(1)
	go func() {
		defer wg.Done()
		entries, err := h.service.GetTreeOnly(ctx, gitRepo, repoCtx.ref, repoCtx.path)
		mu.Lock()
		treeEntriesWithCommit = entries
		treeErr = err
		mu.Unlock()
	}()

	// (3) GetTags
	wg.Add(1)
	go func() {
		defer wg.Done()
		tl, _, err := tags.GetTags(repoCtx.repoWrapper, 100, 0)
		mu.Lock()
		if err != nil {
			tagsList = []tags.Tag{}
		} else {
			tagsList = tl
		}
		mu.Unlock()
	}()

	// (4) GetRemotes
	wg.Add(1)
	go func() {
		defer wg.Done()
		rl, err := h.service.GetRemotes(ctx, gitRepo)
		mu.Lock()
		if err != nil {
			remotesList = []RemoteInfo{}
		} else {
			remotesList = rl
		}
		mu.Unlock()
	}()

	// (5) README
	wg.Add(1)
	go func() {
		defer wg.Done()
		readmeData, _, err := h.blobService.GetBlobContent(ctx, owner, repo, "README.md", repoCtx.ref, repoCtx.repoWrapper)
		mu.Lock()
		if err == nil {
			const maxReadmeSize = 100 * 1024
			if len(readmeData) > maxReadmeSize {
				readmeContent = string(readmeData[:maxReadmeSize])
			} else {
				readmeContent = string(readmeData)
			}
		}
		mu.Unlock()
	}()

	wg.Wait()

	// 빈 저장소 처리 (latestCommit을 못 찾은 경우)
	if !latestCommitFound {
		response := RepositoryOverviewResponse{
			Success:       true,
			Message:       "Repository overview fetched successfully",
			Repository:    fmt.Sprintf("%s/%s", owner, repo),
			Owner:         owner,
			Name:          repo,
			Path:          repoPath,
			Tree:          []GitTreeEntryWithCommit{},
			LatestCommits: convertCommitToGitCommit(commits.Commit{}),
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
	if treeErr != nil {
		errMsg := strings.ToLower(treeErr.Error())
		if strings.Contains(errMsg, "failed to find path") ||
			strings.Contains(errMsg, "path not found") ||
			strings.Contains(errMsg, "path is not a tree") {
			c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "path not found"})
			return
		}
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "repository corrupted"})
		return
	}

	// 응답 구성
	response := RepositoryOverviewResponse{
		Success:       true,
		Message:       "Repository overview fetched successfully",
		Repository:    fmt.Sprintf("%s/%s", owner, repo),
		Owner:         owner,
		Name:          repo,
		Path:          repoPath,
		Tree:          treeEntriesWithCommit,
		LatestCommits: convertCommitToGitCommit(latestCommit),
		Branches:      convertBranchesToGitBranches(branchesList),
		Tags:          convertTagsToGitTags(tagsList),
		Count: RepositoryCount{
			Commits:            commitsCount,
			Branches:           len(branchesList),
			Tags:               len(tagsList),
			Remotes:            len(remotesList),
			Total:              commitsCount + len(branchesList) + len(tagsList) + len(remotesList),
			CommitsApproximate: commitsApproximate,
		},
		Readme: readmeContent,
		LanguageDistribution: LanguageDistribution{
			Languages:  []LanguageStats{},
			TotalBytes: 0,
			TotalFiles: 0,
		},
	}

	c.JSON(http.StatusOK, response)
}

// HandleGetTreeOverview는 트리 개요를 위한 GET /api/v1/repository/{owner}/{repo}/tree 요청을 처리합니다
func (h *RepositoryOverviewHandler) HandleGetTreeOverview(c *gin.Context) {
	ctx := c.Request.Context()
	owner := c.Param("owner")
	repo := c.Param("repo")
	ref := c.Query("ref")
	path := c.Query("path")

	// 공통 검증 및 저장소 열기
	repoCtx, ok := h.validateAndOpenRepository(ctx, c, owner, repo, ref)
	if !ok {
		return
	}
	defer func() { _ = repoCtx.repoWrapper.Close() }()

	repoCtx.path = path

	// 트리 항목만 가져오기 (커밋 정보 없음)
	gitRepo := repoCtx.repoWrapper.GetRepo()
	treeEntriesWithCommit, err := h.service.GetTreeOnly(ctx, gitRepo, repoCtx.ref, repoCtx.path)
	if err != nil {
		// 실제 저장소 손상 오류인 경우
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "repository corrupted"})
		return
	}

	// path 기준으로 최신 커밋 조회 (path가 없으면 루트("")의 최신 커밋)
	latestCommitGitCommit, err := h.service.GetLatestCommitForPath(ctx, gitRepo, repoCtx.ref, repoCtx.path, true)
	if err != nil {
		_ = err // 에러가 있어도 계속 진행 (latestCommitGitCommit은 nil로 유지)
	}

	// 커밋 목록은 선택적으로 가져오기 (성능 최적화)
	var allCommits []commits.Commit
	if c.Query("include_all_commits") == "true" {
		// 전체 커밋 목록이 필요한 경우에만 가져오기 (최대 1000개로 제한)
		const maxCommitsForList = 1000
		allCommits, err = commits.GetCommits(repoCtx.repoWrapper, ctx, repoCtx.ref, maxCommitsForList, 0)
		if err != nil {
			h.logger.Warn("Failed to get all commits for tree overview", zap.Error(err))
			allCommits = []commits.Commit{}
		}
	} else if latestCommitGitCommit != nil {
		// 기본값: 최신 커밋만 포함
		allCommits = []commits.Commit{{
			Sha:          latestCommitGitCommit.SHA,
			Message:      latestCommitGitCommit.Message,
			Author:       commits.CommitAuthor{Name: latestCommitGitCommit.Author.Name, Email: latestCommitGitCommit.Author.Email, Date: latestCommitGitCommit.Author.Date},
			Committer:    commits.CommitAuthor{Name: latestCommitGitCommit.Committer.Name, Email: latestCommitGitCommit.Committer.Email, Date: latestCommitGitCommit.Committer.Date},
			ParentHashes: latestCommitGitCommit.ParentHashes,
			TreeHash:     latestCommitGitCommit.TreeHash,
		}}
	}

	// 응답 구성
	response := TreeOverviewResponse{
		Success:      true,
		Repository:   fmt.Sprintf("%s/%s", owner, repo),
		Owner:        owner,
		Name:         repo,
		Path:         path,
		Tree:         treeEntriesWithCommit,
		Branches:     convertBranchesToGitBranches(repoCtx.branchesList),
		LatestCommit: latestCommitGitCommit,
		Commits:      convertCommitsToGitCommits(allCommits),
	}

	c.JSON(http.StatusOK, response)
}

// HandleGetBlobOverview는 블럭 개요를 위한 GET /api/v1/repository/{owner}/{repo}/blob 요청을 처리합니다
func (h *RepositoryOverviewHandler) HandleGetBlobOverview(c *gin.Context) {
	ctx := c.Request.Context()
	owner := c.Param("owner")
	repo := c.Param("repo")
	path := c.Query("path")
	ref := c.Query("ref")

	// Validate path parameter
	if path == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "Missing required parameter: path",
		})
		return
	}

	// 공통 검증 및 저장소 열기
	repoCtx, ok := h.validateAndOpenRepository(ctx, c, owner, repo, ref)
	if !ok {
		return
	}
	defer func() { _ = repoCtx.repoWrapper.Close() }()

	// blob 가져오기
	blobContent, _, err := h.blobService.GetBlobContent(ctx, owner, repo, path, repoCtx.ref, repoCtx.repoWrapper)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "file not found"})
		return
	}

	// Lines 계산
	lines := strings.Count(strings.ReplaceAll(string(blobContent), "\r\n", "\n"), "\n") + 1
	size := int64(len(blobContent))

	// blob overview에서는 부모 디렉터리 트리만 조회 (커밋 정보 없음, timeout 방지)
	gitRepo := repoCtx.repoWrapper.GetRepo()
	parentPath := ""
	if idx := strings.LastIndex(path, "/"); idx != -1 {
		parentPath = path[:idx]
	}
	treeEntriesWithCommit, err := h.service.GetTreeOnly(ctx, gitRepo, repoCtx.ref, parentPath)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "repository corrupted"})
		return
	}

	// path 기준으로 최신 커밋 조회 (blob이므로 isTree=false, timeBudget/maxCommits 적용됨)
	latestCommitGitCommit, err := h.service.GetLatestCommitForPath(ctx, gitRepo, repoCtx.ref, path, false)
	if err != nil {
		_ = err // 에러가 있어도 계속 진행 (latestCommitGitCommit은 nil로 유지)
	}

	// 커밋 목록은 선택적으로 가져오기 (성능 최적화)
	var allCommits []commits.Commit
	if c.Query("include_all_commits") == "true" {
		// 전체 커밋 목록이 필요한 경우에만 가져오기 (최대 1000개로 제한)
		const maxCommitsForList = 1000
		allCommits, err = commits.GetCommits(repoCtx.repoWrapper, ctx, repoCtx.ref, maxCommitsForList, 0)
		if err != nil {
			h.logger.Warn("Failed to get all commits for blob overview", zap.Error(err))
			allCommits = []commits.Commit{}
		}
	} else if latestCommitGitCommit != nil {
		// 기본값: 최신 커밋만 포함
		allCommits = []commits.Commit{{
			Sha:          latestCommitGitCommit.SHA,
			Message:      latestCommitGitCommit.Message,
			Author:       commits.CommitAuthor{Name: latestCommitGitCommit.Author.Name, Email: latestCommitGitCommit.Author.Email, Date: latestCommitGitCommit.Author.Date},
			Committer:    commits.CommitAuthor{Name: latestCommitGitCommit.Committer.Name, Email: latestCommitGitCommit.Committer.Email, Date: latestCommitGitCommit.Committer.Date},
			ParentHashes: latestCommitGitCommit.ParentHashes,
			TreeHash:     latestCommitGitCommit.TreeHash,
		}}
	}

	// 응답 구성
	response := BlobOverviewResponse{
		Success:      true,
		Content:      string(blobContent),
		Lines:        lines,
		Size:         size,
		Tree:         treeEntriesWithCommit,
		Branches:     convertBranchesToGitBranches(repoCtx.branchesList),
		LatestCommit: latestCommitGitCommit,
		Commits:      convertCommitsToGitCommits(allCommits),
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

	// 저장소 열기 (존재 여부는 OpenRepository에서 확인)
	repoWrapper, err := h.repoManager.OpenRepository(repoPath)
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

// HandleGetTreeWithCommitOverview는 트리와 커밋 정보를 함께 반환하는 GET /api/v1/repository/{owner}/{repo}/overview/tree/commits 요청을 처리합니다
func (h *RepositoryOverviewHandler) HandleGetTreeWithCommitOverview(c *gin.Context) {
	ctx := c.Request.Context()
	start := time.Now()
	m := observability.GetApplicationMetrics()

	owner := c.Param("owner")
	repo := c.Param("repo")
	ref := c.Query("ref")
	path := c.Query("path")

	// 공통 검증 및 저장소 열기
	repoCtx, ok := h.validateAndOpenRepository(ctx, c, owner, repo, ref)
	if !ok {
		return
	}
	defer func() { _ = repoCtx.repoWrapper.Close() }()

	repoCtx.path = path
	gitRepo := repoCtx.repoWrapper.GetRepo()

	// Resolve ref hash for index key.
	resolvedHash, resolveErr := repoCtx.repoWrapper.ResolveReference(repoCtx.ref)
	refHash := ""
	if resolveErr == nil {
		refHash = resolvedHash.String()
	}

	// --- INDEX LOOKUP -DP
	// If we have a pre-populated index entry for this dir, serve directly ("조회" not "계산").
	if refHash != "" && h.commitIndex != nil {
		if idxCommits, idxMeta, hit := h.commitIndex.Lookup(repoCtx.repoPath, refHash, repoCtx.path); hit {
			// We still need the tree entries (structure), so load tree only.
			treeEntriesWithCommit, _, err := h.service.GetTreeWithCommit(ctx, gitRepo, repoCtx.ref, repoCtx.path)
			if err == nil {
				// Overlay index commits onto tree entries.
				for i := range treeEntriesWithCommit {
					if c, ok := idxCommits[treeEntriesWithCommit[i].Path]; ok {
						treeEntriesWithCommit[i].LatestCommit = c
					}
				}
				idxMeta.CutReason = "index_hit"
				m.RecordTreeWithCommit("index_hit", time.Since(start), len(treeEntriesWithCommit))

				response := TreeWithCommitOverviewResponse{
					Success:    true,
					Repository: fmt.Sprintf("%s/%s", owner, repo),
					Owner:      owner,
					Name:       repo,
					Path:       path,
					Tree:       treeEntriesWithCommit,
					Meta:       &idxMeta,
				}
				c.JSON(http.StatusOK, response)
				return
			}
			// If tree loading failed, fall through to normal path.
		}
	}

	// --- NORMAL PATH (capped) ---
	treeEntriesWithCommit, treeMeta, err := h.service.GetTreeWithCommit(ctx, gitRepo, repoCtx.ref, repoCtx.path)
	if err != nil {
		m.RecordTreeWithCommit("error", time.Since(start), 0)
		// 경로 관련 오류인지 확인
		errMsg := strings.ToLower(err.Error())
		if strings.Contains(errMsg, "failed to find path") ||
			strings.Contains(errMsg, "path not found") ||
			strings.Contains(errMsg, "path is not a tree") {
			c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "path not found"})
			return
		}
		// 실제 저장소 손상 오류인 경우
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "repository corrupted"})
		return
	}

	outcome := "complete"
	if treeMeta.Incomplete {
		outcome = "incomplete"
	}
	m.RecordTreeWithCommit(outcome, time.Since(start), len(treeEntriesWithCommit))

	// Schedule background population for future index hits.
	if refHash != "" && h.commitIndex != nil && treeMeta.Incomplete {
		pathInfos := make([]CommitPathInfo, 0, len(treeEntriesWithCommit))
		for _, e := range treeEntriesWithCommit {
			if e.Type == "blob" || e.Type == "tree" {
				pathInfos = append(pathInfos, CommitPathInfo{
					Path:   e.Path,
					IsTree: e.Type == "tree",
				})
			}
		}
		h.commitIndex.ScheduleBackgroundPopulation(repoCtx.repoPath, refHash, repoCtx.path, gitRepo, pathInfos)
	}

	// If complete, store in index for instant future hits.
	if refHash != "" && h.commitIndex != nil && !treeMeta.Incomplete {
		commitMap := make(map[string]*GitCommit, len(treeEntriesWithCommit))
		for _, e := range treeEntriesWithCommit {
			if e.LatestCommit != nil {
				commitMap[e.Path] = e.LatestCommit
			}
		}
		h.commitIndex.Store(repoCtx.repoPath, refHash, repoCtx.path, commitMap, treeMeta)
	}

	// 응답 구성
	response := TreeWithCommitOverviewResponse{
		Success:    true,
		Repository: fmt.Sprintf("%s/%s", owner, repo),
		Owner:      owner,
		Name:       repo,
		Path:       path,
		Tree:       treeEntriesWithCommit,
		Meta:       &treeMeta,
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

	// 공통 검증 및 저장소 열기
	repoCtx, ok := h.validateAndOpenRepository(ctx, c, owner, repo, req.Ref)
	if !ok {
		return
	}
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
