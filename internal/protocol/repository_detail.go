package protocol

import (
	"errors"
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
	ref := c.Query("ref")
	path := c.Query("path")

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
		// Repository does not exist - create a proper error message
		notFoundErr := fmt.Errorf("repository not found at path: %s", repoPath)
		apiErr := h.errorMapper.MapError(notFoundErr, "repository_not_found")
		c.JSON(404, gin.H{"success": false, "error": apiErr.Message})
		return
	}

	gitRepo, err := git.OpenRepositorySimple(repoPath)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "repository_corrupted")
		c.JSON(422, gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// 6. branches 가져오기
	branches, err := gitRepo.GetBranches(ctx)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "repository_corrupted")
		c.JSON(422, gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// 7. refs가 제공된 경우, 실존하는 브랜치인지 확인
	if ref != "" {
		branchExists := false
		for _, branch := range branches {
			if branch.Name == ref {
				branchExists = true
				break
			}
		}

		if !branchExists {
			refErr := fmt.Errorf("reference '%s' not found in repository", ref)
			apiErr := h.errorMapper.MapError(refErr, "reference_not_found")
			c.JSON(404, gin.H{"success": false, "error": apiErr.Message})
			return
		}
		h.logger.Info("ref validated successfully",
			zap.String("ref", ref))
	}

	// 5. commit history 가져오기 (빈 저장소 확인용)
	commits, err := gitRepo.GetCommits(ctx, ref, 9999999, 0)
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
			Readme:               "",
			LanguageDistribution: &git.LanguageDistribution{},
		}
		api.SendSuccess(c, response, "Empty repository - no commits found")
		return
	}

	// commits는 이미 위에서 가져왔으므로 여기서는 tree entries 가져오기
	latestCommit := commits[0]

	// 5. tree entries 가져오기 (커밋 정보 포함)
	treeEntries, err := gitRepo.GetTreeWithCommits(ctx, ref, path)
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
	readmeData, err := gitRepo.GetBlob(ctx, "README.md", ref)
	readmeContent := ""
	if err != nil {
		// README가 없는 경우 빈 문자열로 처리 (500 에러 방지)
		var pathNotFoundErr *git.PathNotFoundError
		if errors.As(err, &pathNotFoundErr) {
			// README 파일이 없는 경우 빈 문자열 사용
			readmeContent = ""
		} else {
			// 다른 에러인 경우에는 에러 반환
			apiErr := h.errorMapper.MapError(err, "get_readme")
			c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
			return
		}
	} else {
		readmeContent = string(readmeData)
	}

	// 10. 언어 분포도 계산
	languageDistribution, err := gitRepo.GetLanguageDistribution(ctx, ref)
	if err != nil {
		// 언어 분포도 계산 실패는 로그만 남기고 응답에는 포함하지 않음 (선택적 기능)
		h.logger.Warn("Failed to calculate language distribution",
			zap.String("owner", owner),
			zap.String("repo", repo),
			zap.String("ref", ref),
			zap.Error(err))
		// Continue without language distribution
	}

	// 11. response 생성
	response := responses.RepositoryDetailResponse{
		Repository:    fmt.Sprintf("%s/%s", owner, repo),
		Owner:         owner,
		Name:          repo,
		Path:          repoPath,
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
		Readme:               readmeContent,
		LanguageDistribution: languageDistribution,
	}

	api.SendSuccess(c, response, "Repository detail retrieved successfully")

}
