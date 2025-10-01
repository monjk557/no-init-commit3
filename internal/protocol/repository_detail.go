package protocol

import (
	"fmt"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/git"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repository"
	"github.com/collabops/git-executor/internal/schemas/responses"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type RepositoryDetailHandler struct {
	config             *config.Config
	logger             *logging.Logger
	pathConfig         *repository.PathConfig
	pathValidator      *repository.GitAPIPathValidator
	objectManager      *git.GitObjectManager
	repoManager        *git.DefaultRepositoryManager
	responseMiddleware *api.JSONResponseMiddleware
	errorMapper        *api.GitErrorMapper
}

func NewRepositoryDetailHandler(cfg *config.Config) (*RepositoryDetailHandler, error) {
	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	pathConfig, err := repository.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	objectManager, err := git.NewGitObjectManager(pathConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create object manager: %w", err)
	}

	responseMiddleware := api.NewJSONResponseMiddleware(logger, "v1")
	errorMapper := api.NewGitErrorMapper()

	pathValidator := repository.NewGitAPIPathValidator(pathConfig)

	repoManager, err := git.NewRepositoryManager(pathConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create repository manager: %w", err)
	}

	return &RepositoryDetailHandler{
		config:             cfg,
		logger:             logger,
		pathConfig:         pathConfig,
		pathValidator:      pathValidator,
		objectManager:      objectManager,
		repoManager:        repoManager,
		responseMiddleware: responseMiddleware,
		errorMapper:        errorMapper,
	}, nil
}

// HandleGetRepositoryDetail handles GET /api/v1/repository/{owner}/{repo} for repository detail
func (h *RepositoryDetailHandler) HandleGetRepositoryDetail(c *gin.Context) {
	ctx := c.Request.Context()
	owner := c.Param("owner")
	repo := c.Param("repo")
	refs := c.Query("refs")

	// 1. parameter 조합 검증
	repoPath, err := repository.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "repository_not_found")
		c.JSON(404, gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// 2. repository 존재 확인
	exists, err := h.repoManager.RepositoryExists(ctx, repoPath)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "repository_not_found")
		c.JSON(404, gin.H{"success": false, "error": apiErr.Message})
		return
	}
	if !exists {
		apiErr := h.errorMapper.MapError(err, "repository_not_found")
		c.JSON(404, gin.H{"success": false, "error": apiErr.Message})
		return
	}

	gitRepo, err := git.OpenRepositorySimple(repoPath)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "repository_corrupted")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// 3. repository description 가져오기
	description, err := gitRepo.GetDescription(ctx)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "repository_corrupted")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// 4. default branch 확인
	defaultBranch, err := gitRepo.GetRefs(ctx)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "branch_not_found")
		c.JSON(404, gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// 5. refs가 생략되었으면 default branch 사용
	if refs == "" {
		refs = defaultBranch
	}

	// 6. branches 가져오기
	branches, err := gitRepo.GetBranches(ctx)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "repository_corrupted")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// 7. refs가 제공된 경우, 실존하는 브랜치인지 확인
	if refs != "" {
		branchExists := false
		for _, branch := range branches {
			if branch.Name == refs {
				branchExists = true
				break
			}
		}

		if !branchExists {
			refErr := fmt.Errorf("reference '%s' not found in repository", refs)
			apiErr := h.errorMapper.MapError(refErr, "reference_not_found")
			c.JSON(404, gin.H{"success": false, "error": apiErr.Message})
			return
		}
		h.logger.Info("refs validated successfully",
			zap.String("refs", refs))
	}

	// 5. commit history 가져오기 (빈 저장소 확인용)
	commits, err := gitRepo.GetCommits(ctx, refs, 10, 0)
	emptyRepository := false
	if err != nil || len(commits) == 0 {
		emptyRepository = true
	}

	// commit이 없는 빈 저장소인 경우 early return
	if emptyRepository {
		response := responses.RepositoryDetailResponse{
			Repository:    fmt.Sprintf("%s/%s", owner, repo),
			Owner:         owner,
			Name:          repo,
			Path:          repoPath,
			Description:   description,
			DefaultBranch: defaultBranch,
			Tree:          []git.TreeEntry{},
			LatestCommits: git.Commit{},
			Branches:      []git.Branch{},
			Tags:          []git.Tag{},
			Count: responses.RepositoryCount{
				Commits:  0,
				Branches: 0,
				Tags:     0,
				Remotes:  0,
				Total:    0,
			},
			Readme: "",
		}
		api.SendSuccess(c, response, "Empty repository - no commits found")
		return
	}

	// commits는 이미 위에서 가져왔으므로 여기서는 tree entries 가져오기
	latestCommit := commits[0]

	// 5. tree entries 가져오기 (커밋 정보 포함)
	treeEntries, err := gitRepo.GetTreeWithCommits(ctx, refs)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "get_tree")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// 7. tags 가져오기
	tags, err := gitRepo.GetTags(ctx)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "get_tags")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}
	h.logger.Info("tags", zap.Any("tags", tags))

	// 8. remotes 가져오기
	remotes, err := gitRepo.GetRemotes(ctx)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "get_remotes")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}
	h.logger.Info("remotes", zap.Any("remotes", remotes))

	// 9. readme 가져오기
	readmeData, err := gitRepo.GetBlob(ctx, "README.md", refs)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "get_readme")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// 10. response 생성
	response := responses.RepositoryDetailResponse{
		Repository:    fmt.Sprintf("%s/%s", owner, repo),
		Owner:         owner,
		Name:          repo,
		Path:          repoPath,
		Description:   description,
		DefaultBranch: defaultBranch,
		Tree:          treeEntries,
		LatestCommits: latestCommit,
		Branches:      branches,
		Tags:          tags,
		Count: responses.RepositoryCount{
			Commits:  len(commits),
			Branches: len(branches),
			Tags:     len(tags),
			Remotes:  len(remotes),
			Total:    len(commits) + len(branches) + len(tags) + len(remotes),
		},
		Readme: string(readmeData),
	}

	api.SendSuccess(c, response, "Repository detail retrieved successfully")

}
