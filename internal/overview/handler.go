package overview

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/collabops/git-executor/internal/blob"
	"github.com/collabops/git-executor/internal/branches"
	"github.com/collabops/git-executor/internal/commits"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/collabops/git-executor/internal/tags"
	"github.com/collabops/git-executor/internal/tree"
	"github.com/gin-gonic/gin"
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

	// 저장소 존재 여부 확인
	exists, err := h.repoManager.RepositoryExists(repoPath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return nil, false
	}
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return nil, false
	}

	// 저장소 열기
	repoWrapper, err := h.repoManager.OpenRepository(repoPath)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "repository corrupted"})
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

	// ref가 제공된 경우 검증
	if ref != "" && ref != "HEAD" {
		branchExists := false
		for _, branch := range branchesList {
			if branch.Name == ref {
				branchExists = true
				break
			}
		}
		if !branchExists {
			_ = repoWrapper.Close()
			c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "reference not found"})
			return nil, false
		}
	}

	// branches.SimpleBranchInfo를 overview.BranchInfo로 변환
	var overviewBranches []BranchInfo
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

	// 커밋 가져오기 (빈 저장소 확인 및 최신 커밋용)
	commitsList, err := commits.GetCommits(repoCtx.repoWrapper, ctx, repoCtx.ref, 9999999, 0)
	if err != nil || len(commitsList) == 0 {
		// 빈 저장소
		response := RepositoryOverviewResponse{
			Repository:    fmt.Sprintf("%s/%s", owner, repo),
			Owner:         owner,
			Name:          repo,
			Path:          repoPath,
			Tree:          []GitTreeEntry{},
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

	// 트리 항목 가져오기
	treeEntries, err := tree.GetTreeEntries(h.pathConfig, h.logger, repoPath, repoCtx.ref, path)
	if err != nil {

		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "repository corrupted"})
		return
	}

	// 태그 가져오기
	tagsList, _, err := tags.GetTags(repoCtx.repoWrapper, 9999999, 0)
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
	readmeContent := ""
	readmeData, _, err := h.blobService.GetBlobContent(ctx, owner, repo, "README.md", repoCtx.ref)
	if err != nil {
		// README를 찾을 수 없는 것은 오류가 아님 - "not found" 오류인지 확인
		errStr := err.Error()
		if !strings.Contains(errStr, "not found") && !strings.Contains(errStr, "file not found") {
			h.logger.Warn("Failed to get README", zap.Error(err))
		}
	} else {
		readmeContent = string(readmeData)
	}

	// 언어 분포 가져오기
	languageDistribution := h.service.GetLanguageDistribution(ctx, gitRepo, repoCtx.ref)

	// 응답 구성
	response := RepositoryOverviewResponse{
		Repository:    fmt.Sprintf("%s/%s", owner, repo),
		Owner:         owner,
		Name:          repo,
		Path:          repoPath,
		Tree:          convertTreeEntriesToGitTreeEntries(treeEntries),
		LatestCommits: convertCommitToGitCommit(latestCommit),
		Branches:      convertBranchesToGitBranches(branchesList),
		Tags:          convertTagsToGitTags(tagsList),
		Count: RepositoryCount{
			Commits:  len(commitsList),
			Branches: len(branchesList),
			Tags:     len(tagsList),
			Remotes:  len(remotesList),
			Total:    len(commitsList) + len(branchesList) + len(tagsList) + len(remotesList),
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

	// 트리 항목 가져오기
	treeEntries, err := tree.GetTreeEntries(h.pathConfig, h.logger, repoCtx.repoPath, repoCtx.ref, repoCtx.path)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "repository corrupted"})
		return
	}

	// 커밋 가져오기
	commitsList, err := commits.GetCommits(repoCtx.repoWrapper, ctx, repoCtx.ref, 9999999, 0)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "repository corrupted"})
		return
	}

	// 최신 커밋
	var latestCommit commits.Commit
	if len(commitsList) > 0 {
		latestCommit = commitsList[0]
	}

	// 응답 구성
	response := TreeOverviewResponse{
		Success:      true,
		Tree:         convertTreeEntriesToGitTreeEntries(treeEntries),
		Branches:     convertBranchesToGitBranches(repoCtx.branchesList),
		LatestCommit: convertCommitToGitCommit(latestCommit),
		Commits:      convertCommitsToGitCommits(commitsList),
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
	blobContent, _, err := h.blobService.GetBlobContent(ctx, owner, repo, path, repoCtx.ref)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "file not found"})
		return
	}

	// Lines 계산
	lines := strings.Count(strings.ReplaceAll(string(blobContent), "\r\n", "\n"), "\n") + 1
	size := int64(len(blobContent))

	// tree entries 가져오기
	treeEntries, err := tree.GetTreeEntries(h.pathConfig, h.logger, repoCtx.repoPath, repoCtx.ref, "")
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "repository corrupted"})
		return
	}

	// 커밋 가져오기 (전체 커밋 히스토리)
	commitsList, err := commits.GetCommits(repoCtx.repoWrapper, ctx, repoCtx.ref, 9999999, 0)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "error": "repository corrupted"})
		return
	}

	// 최신 커밋
	var latestCommit commits.Commit
	if len(commitsList) > 0 {
		latestCommit = commitsList[0]
	}

	// 응답 구성
	response := BlobOverviewResponse{
		Success:      true,
		Content:      string(blobContent),
		Lines:        lines,
		Size:         size,
		Tree:         convertTreeEntriesToGitTreeEntries(treeEntries),
		Branches:     convertBranchesToGitBranches(repoCtx.branchesList),
		LatestCommit: convertCommitToGitCommit(latestCommit),
		Commits:      convertCommitsToGitCommits(commitsList),
	}

	c.JSON(http.StatusOK, response)
}
