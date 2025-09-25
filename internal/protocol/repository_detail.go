package protocol

import (
	"fmt"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/git"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repository"
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

	return &RepositoryDetailHandler{
		config:             cfg,
		logger:             logger,
		objectManager:      objectManager,
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

	if ref == "" {
		ref = "main"
	}

	repoPath, err := repository.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "get_repository_path")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	gitRepo, err := git.OpenRepositorySimple(repoPath)
	if err != nil {
		return
	}
	defer func() { _ = gitRepo.Close() }()

	// Get tree entries from Git repository interface
	treeEntries, err := gitRepo.GetTree(ctx, ref)
	if err != nil {
		return
	}
	h.logger.Info("treeEntries", zap.Any("treeEntries", treeEntries))
	// //default branch
	// headRef, err := refs[0]
	// if err != nil {
	// 	apiErr := h.errorMapper.MapError(err, "get_head_ref")
	// 	c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
	// 	return
	// }

	// 브랜치 로그 가져오기

	// defer logIter.Close()
	// // 순회
	// err = logIter.ForEach(func(c *object.Commit) error {
	// 	fmt.Printf("[%s] %s\n", c.Hash, c.Message)
	// 	return nil
	// })
	// if err != nil {
	// 	apiErr := h.errorMapper.MapError(err, "iterate_commits")
	// 	c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
	// 	return
	// }

	// //tree
	// tree, err := h.getTreeStructure(ctx, repository.Path(), headRef.Name().String())
	// if err != nil {
	// 	apiErr := h.errorMapper.MapError(err, "get_tree_structure")
	// 	c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
	// 	return
	// }

	//tags
	// tagIter, err := repository.Tags()
	// if err != nil {
	// 	apiErr := h.errorMapper.MapError(err, "get_tag_iter")
	// 	c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
	// 	return nil, err
	// }

	//remotes
	// remoteIter, err := repository.Remotes()
	// if err != nil {
	// 	apiErr := h.errorMapper.MapError(err, "get_remote_iter")
	// 	c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
	// 	return nil, err
	// }

	// //hash count
	// hashCount, err := repository.HashCount()
	// if err != nil {
	// 	apiErr := h.errorMapper.MapError(err, "get_hash_count")
	// 	c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
	// 	return
	// }

	// //readme
	// readme, err := tree.File("README.md")
	// if err != nil {
	// 	apiErr := h.errorMapper.MapError(err, "get_readme")
	// 	c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
	// 	return
	// }

	// readmeContent, err := readme.Contents()
	// if err != nil {
	// 	apiErr := h.errorMapper.MapError(err, "get_readme_content")
	// 	c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
	// 	return
	// }

	// response := responses.RepositoryDetailResponse{
	// 	Description: "description",
	// 	// Tree:        treeEntries,
	// 	Tags: []string{},
	// 	// DefaultBranch: headRef.Name().String(),
	// 	// Branches: "",
	// 	Count: responses.RepositoryCount{
	// 		Coomits:  1,
	// 		Branches: 1,
	// 		Tags:     1,
	// 		Remotes:  1,
	// 		Total:    1,
	// 	},
	// 	Readme: "readme",
	// }

	api.SendSuccess(c, "response", "Repository detail retrieved successfully")

}
