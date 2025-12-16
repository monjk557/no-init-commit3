package overview

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/blob"
	"github.com/collabops/git-executor/internal/branches"
	"github.com/collabops/git-executor/internal/commits"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/collabops/git-executor/internal/tags"
	"github.com/gin-gonic/gin"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
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

	return &RepositoryOverviewHandler{
		config:        cfg,
		logger:        logger,
		pathConfig:    pathConfig,
		pathValidator: pathValidator,
		repoManager:   repoManager,
		blobService:   blobService,
		service:       service,
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

// HandleGetRepositoryOverview는 저장소 개요를 위한 GET /api/v1/repository/{owner}/{repo} 요청을 처리합니다
func (h *RepositoryOverviewHandler) HandleGetRepositoryOverview(c *gin.Context) {
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

	gitRepo := repoCtx.repoWrapper.GetRepo()
	branchesList := repoCtx.branchesList
	repoPath := repoCtx.repoPath

	// path 설정 (tree overview와 동일한 방식)
	repoCtx.path = path

	// 최신 커밋 1개만 가져오기 (성능 최적화)
	commitsList, err := commits.GetCommits(repoCtx.repoWrapper, ctx, repoCtx.ref, 1, 0)
	if err != nil || len(commitsList) == 0 {
		// 빈 저장소
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

	latestCommit := commitsList[0]

	// 커밋 개수만 세기 (성능 최적화: 커밋 객체를 메모리에 로드하지 않고 카운트만)
	commitsCount, err := commits.CountCommits(repoCtx.repoWrapper, ctx, repoCtx.ref)
	if err != nil {
		h.logger.Warn("Failed to count commits, using fallback", zap.Error(err))
		// 폴백: 최소한 1개는 있다는 것을 알 수 있음
		commitsCount = 1
	}

	// 트리 항목 가져오기 (커밋 정보 포함) - tree overview와 동일한 방식
	// 성능 최적화: 커밋 정보 조회는 선택적으로 처리 (기본값: false)
	includeCommits := c.Query("include_commits") == "true"
	treeEntriesWithCommit, err := h.getTreeWithCommit(ctx, gitRepo, repoCtx.repoPath, repoCtx.ref, repoCtx.path, includeCommits)
	if err != nil {
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

	// 태그 가져오기
	// Repository overview에서는 태그 목록만 필요하므로 적절한 limit 설정
	tagsList, _, err := tags.GetTags(repoCtx.repoWrapper, 100, 0)
	if err != nil {
		h.logger.Warn("Failed to get tags", zap.Error(err))
		tagsList = []tags.Tag{}
	}

	// 원격 저장소 가져오기
	remotesList, err := h.service.GetRemotes(ctx, gitRepo)
	if err != nil {
		h.logger.Warn("Failed to get remotes", zap.Error(err))
		remotesList = []RemoteInfo{}
	}

	// README 가져오기
	// README 크기 제한 (100KB) - 큰 README는 메모리 사용량을 증가시킴
	readmeContent := ""
	readmeData, _, err := h.blobService.GetBlobContent(ctx, owner, repo, "README.md", repoCtx.ref, repoCtx.repoWrapper)
	if err != nil {
		// README를 찾을 수 없는 것은 오류가 아님 - "not found" 오류인지 확인
		errStr := err.Error()
		if !strings.Contains(errStr, "not found") && !strings.Contains(errStr, "file not found") {
			h.logger.Warn("Failed to get README", zap.Error(err))
		}
	} else {
		// 100KB 제한
		const maxReadmeSize = 100 * 1024
		if len(readmeData) > maxReadmeSize {
			readmeContent = string(readmeData[:maxReadmeSize])
			h.logger.Warn("README truncated", zap.Int("original_size", len(readmeData)), zap.Int("truncated_size", maxReadmeSize))
		} else {
			readmeContent = string(readmeData)
		}
	}

	// 언어 분포 가져오기 (성능 최적화: 선택적으로 처리)
	// include_languages 파라미터가 true일 때만 계산
	var languageDistribution LanguageDistribution
	if c.Query("include_languages") == "true" {
		languageDistribution = h.service.GetLanguageDistribution(ctx, gitRepo, repoCtx.ref)
	} else {
		languageDistribution = LanguageDistribution{
			Languages:  []LanguageStats{},
			TotalBytes: 0,
			TotalFiles: 0,
		}
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
			Commits:  commitsCount,
			Branches: len(branchesList),
			Tags:     len(tagsList),
			Remotes:  len(remotesList),
			Total:    commitsCount + len(branchesList) + len(tagsList) + len(remotesList),
		},
		Readme:               readmeContent,
		LanguageDistribution: languageDistribution,
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

	// 트리 항목 가져오기 (커밋 정보 포함)
	gitRepo := repoCtx.repoWrapper.GetRepo()
	includeCommits := c.Query("include_commits") == "true"
	treeEntriesWithCommit, err := h.getTreeWithCommit(ctx, gitRepo, repoCtx.repoPath, repoCtx.ref, repoCtx.path, includeCommits)
	if err != nil {
		// 실제 저장소 손상 오류인 경우
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "repository corrupted"})
		return
	}

	// 최신 커밋 1개만 가져오기 (성능 최적화)
	commitsList, err := commits.GetCommits(repoCtx.repoWrapper, ctx, repoCtx.ref, 1, 0)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "repository corrupted"})
		return
	}

	// 최신 커밋
	var latestCommit commits.Commit
	if len(commitsList) > 0 {
		latestCommit = commitsList[0]
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
	} else {
		// 기본값: 최신 커밋만 포함
		allCommits = commitsList
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
		LatestCommit: convertCommitToGitCommit(latestCommit),
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

	// tree entries 가져오기 (커밋 정보 포함)
	// blob overview에서는 파일의 부모 디렉토리 경로를 사용
	parentPath := ""
	if idx := strings.LastIndex(path, "/"); idx != -1 {
		parentPath = path[:idx]
	}
	gitRepo := repoCtx.repoWrapper.GetRepo()
	includeCommits := c.Query("include_commits") == "true"
	treeEntriesWithCommit, err := h.getTreeWithCommit(ctx, gitRepo, repoCtx.repoPath, repoCtx.ref, parentPath, includeCommits)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "repository corrupted"})
		return
	}

	// 최신 커밋 1개만 가져오기 (성능 최적화)
	commitsList, err := commits.GetCommits(repoCtx.repoWrapper, ctx, repoCtx.ref, 1, 0)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "repository corrupted"})
		return
	}

	// 최신 커밋
	var latestCommit commits.Commit
	if len(commitsList) > 0 {
		latestCommit = commitsList[0]
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
	} else {
		// 기본값: 최신 커밋만 포함
		allCommits = commitsList
	}

	// 응답 구성
	response := BlobOverviewResponse{
		Success:      true,
		Content:      string(blobContent),
		Lines:        lines,
		Size:         size,
		Tree:         treeEntriesWithCommit,
		Branches:     convertBranchesToGitBranches(repoCtx.branchesList),
		LatestCommit: convertCommitToGitCommit(latestCommit),
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
							aheadCount, aErr := countCommitsBetween(gitRepo, mergeBase, commit)
							if aErr == nil {
								branchItem.Ahead = aheadCount
							}
							// 뒤처진 커밋 수 계산(기본→브랜치)
							behindCount, bErr := countCommitsBetween(gitRepo, mergeBase, defaultCommit)
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

	// 트리 항목 가져오기 (커밋 정보 포함)
	includeCommits := c.Query("include_commits") == "true"
	treeEntriesWithCommit, err := h.getTreeWithCommit(ctx, gitRepo, repoCtx.repoPath, repoCtx.ref, repoCtx.path, includeCommits)
	if err != nil {
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

	// 응답 구성
	response := TreeWithCommitOverviewResponse{
		Success:    true,
		Repository: fmt.Sprintf("%s/%s", owner, repo),
		Owner:      owner,
		Name:       repo,
		Path:       path,
		Tree:       treeEntriesWithCommit,
		Branches:   convertBranchesToGitBranches(repoCtx.branchesList),
	}

	c.JSON(http.StatusOK, response)
}

// getTreeWithCommit는 트리 항목과 각 항목의 최근 커밋 정보를 함께 가져옵니다.
// includeCommits가 false이면 커밋 정보를 조회하지 않아 성능이 크게 향상됩니다.
func (h *RepositoryOverviewHandler) getTreeWithCommit(ctx context.Context, gitRepo *gogit.Repository, repoPath, ref, path string, includeCommits bool) ([]GitTreeEntryWithCommit, error) {
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
	var targetTree *object.Tree = treeObj
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

	// 각 tree entry에 대해 커밋 정보 가져오기 (commit service 활용)
	// 중요: 커밋 정보 조회 실패해도 모든 tree entry는 반드시 포함되어야 함
	treeEntriesWithCommit := make([]GitTreeEntryWithCommit, 0, len(targetTree.Entries))
	for _, entry := range targetTree.Entries {
		// Path는 full path from repository root
		fullPath := entry.Name
		if basePath != "" {
			fullPath = basePath + "/" + entry.Name
		}

		// Entry type 결정
		entryType := "blob"
		if entry.Mode == filemode.Dir {
			entryType = "tree"
		} else if entry.Mode == filemode.Submodule {
			entryType = "commit"
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

		// 각 entry에 대한 최근 커밋 가져오기 (overview service 활용)
		// 커밋 정보는 선택사항이므로 에러가 발생해도 tree entry는 반드시 포함
		var commitInfo *GitCommit

		// 성능 최적화: includeCommits가 false이면 커밋 정보를 조회하지 않음
		if includeCommits {
			// 커밋 정보 조회를 안전하게 수행 (에러가 발생해도 tree entry는 포함)
			func() {
				defer func() {
					if r := recover(); r != nil {
						// panic이 발생해도 tree entry는 포함
						h.logger.Error("Panic while getting commit for path, continuing without commit info",
							zap.String("path", fullPath),
							zap.String("type", entryType),
							zap.Any("panic", r))
					}
				}()

				if entryType == "blob" || entryType == "tree" {
					// 파일이나 디렉토리의 경우 해당 경로의 최근 커밋 조회
					// tree인 경우 하위를 재귀적으로 탐색하여 가장 최근 커밋을 가져옴
					isTree := entryType == "tree"

					commit, err := h.service.GetLatestCommitForPath(ctx, gitRepo, ref, fullPath, isTree)
					if err != nil {
						// 커밋 조회 실패는 경고만 하고 계속 진행 (tree entry는 포함)
						h.logger.Warn("Failed to get commit for path, continuing without commit info",
							zap.String("path", fullPath),
							zap.String("type", entryType),
							zap.Error(err))
					} else {
						commitInfo = commit
					}
				}
			}()
		}

		// 커밋 정보가 없어도 tree entry는 반드시 포함
		treeEntriesWithCommit = append(treeEntriesWithCommit, GitTreeEntryWithCommit{
			Type:         entryType,
			Name:         entry.Name,
			Path:         fullPath,
			Mode:         &modeStr,
			Hash:         &hashStr,
			Size:         size,
			LatestCommit: commitInfo,
		})
	}

	return treeEntriesWithCommit, nil
}

// countCommitsBetween counts commits between two commits (from excluded, to included)
// Iterates from 'to' backwards until 'from' hash is found
func countCommitsBetween(repo *gogit.Repository, from, to *object.Commit) (int, error) {
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
