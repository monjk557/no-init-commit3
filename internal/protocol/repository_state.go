package protocol

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"os"
	"path/filepath"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/filesystem"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/git"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/collabops/git-executor/internal/repository"
	"github.com/collabops/git-executor/internal/schemas/common"
)

// RepositoryStateHandler handles REST API operations for repository state queries
type RepositoryStateHandler struct {
	config             *config.Config
	logger             *logging.Logger
	permClient         permissions.PermissionClient
	pathConfig         *repository.PathConfig
	pathValidator      *repository.GitAPIPathValidator
	objectManager      *git.GitObjectManager
	responseMiddleware *api.JSONResponseMiddleware
	errorMapper        *api.GitErrorMapper
}

// NewRepositoryStateHandler creates a new repository state handler
func NewRepositoryStateHandler(cfg *config.Config, permClient permissions.PermissionClient) (*RepositoryStateHandler, error) {
	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	pathConfig, err := repository.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	pathValidator := repository.NewGitAPIPathValidator(pathConfig)

	objectManager, err := git.NewGitObjectManager(pathConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create object manager: %w", err)
	}

	responseMiddleware := api.NewJSONResponseMiddleware(logger, "v1")
	errorMapper := api.NewGitErrorMapper()

	return &RepositoryStateHandler{
		config:             cfg,
		logger:             logger,
		permClient:         permClient,
		pathConfig:         pathConfig,
		pathValidator:      pathValidator,
		objectManager:      objectManager,
		responseMiddleware: responseMiddleware,
		errorMapper:        errorMapper,
	}, nil
}

// RepositoryStateResponse represents the complete repository state information
type RepositoryStateResponse struct {
	Success        bool                   `json:"success"`
	RepositoryName string                 `json:"repository_name"`
	Owner          string                 `json:"owner"`
	Head           *HeadReferenceInfo     `json:"head"`
	References     *RepositoryReferences  `json:"references"`
	WorkingTree    *WorkingTreeStatus     `json:"working_tree,omitempty"`
	RepositoryInfo *RepositoryMetadata    `json:"repository_info"`
	Timestamp      time.Time              `json:"timestamp"`
	Details        map[string]interface{} `json:"details,omitempty"`
}

// HeadReferenceInfo contains information about the current HEAD
type HeadReferenceInfo struct {
	Type       string             `json:"type"`        // "symbolic" or "hash"
	Name       string             `json:"name"`        // branch name or "HEAD"
	Target     string             `json:"target"`      // target reference or commit hash
	CommitSHA  string             `json:"commit_sha"`  // actual commit hash
	CommitInfo *common.CommitInfo `json:"commit_info"` // detailed commit information
	IsBranch   bool               `json:"is_branch"`   // true if pointing to a branch
	IsDetached bool               `json:"is_detached"` // true if in detached HEAD state
}

// RepositoryReferences contains all repository references
type RepositoryReferences struct {
	Branches []BranchReference `json:"branches"`
	Tags     []TagReference    `json:"tags"`
	Remotes  []RemoteReference `json:"remotes"`
	Count    ReferenceCounts   `json:"count"`
}

// BranchReference represents a branch reference
type BranchReference struct {
	Name         string             `json:"name"`
	FullName     string             `json:"full_name"` // refs/heads/branch-name
	CommitSHA    string             `json:"commit_sha"`
	CommitInfo   *common.CommitInfo `json:"commit_info"`
	IsHead       bool               `json:"is_head"`      // true if this is the current HEAD branch
	IsProtected  bool               `json:"is_protected"` // true if this is a protected branch
	LastModified time.Time          `json:"last_modified"`
}

// TagReference represents a tag reference
type TagReference struct {
	Name         string             `json:"name"`
	FullName     string             `json:"full_name"` // refs/tags/tag-name
	CommitSHA    string             `json:"commit_sha"`
	CommitInfo   *common.CommitInfo `json:"commit_info"`
	IsAnnotated  bool               `json:"is_annotated"`       // true if this is an annotated tag
	TagInfo      *TagInfo           `json:"tag_info,omitempty"` // additional tag information
	LastModified time.Time          `json:"last_modified"`
}

// RemoteReference represents a remote reference
type RemoteReference struct {
	Name         string             `json:"name"`
	FullName     string             `json:"full_name"` // refs/remotes/origin/branch-name
	CommitSHA    string             `json:"commit_sha"`
	CommitInfo   *common.CommitInfo `json:"commit_info"`
	RemoteName   string             `json:"remote_name"` // e.g., "origin"
	BranchName   string             `json:"branch_name"` // e.g., "main"
	LastModified time.Time          `json:"last_modified"`
}

// TagInfo contains additional information for annotated tags
type TagInfo struct {
	Message    string             `json:"message"`
	Tagger     *common.AuthorInfo `json:"tagger"`
	TargetType string             `json:"target_type"` // "commit", "tree", "blob", "tag"
	TargetSHA  string             `json:"target_sha"`
}

// ReferenceCounts provides counts of different reference types
type ReferenceCounts struct {
	Branches int `json:"branches"`
	Tags     int `json:"tags"`
	Remotes  int `json:"remotes"`
	Total    int `json:"total"`
}

// WorkingTreeStatus represents the status of the working tree (for non-bare repos)
type WorkingTreeStatus struct {
	IsClean        bool                      `json:"is_clean"`
	HasChanges     bool                      `json:"has_changes"`
	ModifiedFiles  []string                  `json:"modified_files,omitempty"`
	AddedFiles     []string                  `json:"added_files,omitempty"`
	DeletedFiles   []string                  `json:"deleted_files,omitempty"`
	UntrackedFiles []string                  `json:"untracked_files,omitempty"`
	FileStatus     map[string]FileStatusInfo `json:"file_status,omitempty"`
}

// FileStatusInfo provides detailed status for individual files
type FileStatusInfo struct {
	Staging  string `json:"staging"`  // staging area status
	Worktree string `json:"worktree"` // working tree status
}

// RepositoryMetadata contains general repository information
type RepositoryMetadata struct {
	IsBare        bool   `json:"is_bare"`
	HasWorktree   bool   `json:"has_worktree"`
	GitDirectory  string `json:"git_directory"`
	WorktreeRoot  string `json:"worktree_root,omitempty"`
	DefaultBranch string `json:"default_branch,omitempty"`
}

// HandleGetRepositoryState handles GET /repos/{owner}/{repo}/state for complete repository state
func (h *RepositoryStateHandler) HandleGetRepositoryState(c *gin.Context) {
	ctx := c.Request.Context()

	// Extract URL parameters
	owner := c.Param("owner")
	repo := c.Param("repo")

	h.logger.Info("Processing repository state request",
		zap.String("owner", owner),
		zap.String("repo", repo))

	// Validate path components
	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo)); err != nil {
		apiErr := h.errorMapper.MapError(err, "validate_path")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// Validate permissions
	if err := h.validatePermissions(c, owner, repo, "read"); err != nil {
		if gitAPIErr, ok := err.(*api.GitAPIError); ok {
			c.JSON(gitAPIErr.HTTPStatusCode(), gin.H{"success": false, "error": gitAPIErr.Message})
		} else {
			c.JSON(http.StatusForbidden, gin.H{"success": false, "error": err.Error()})
		}
		return
	}

	// Open repository
	repository, err := h.objectManager.OpenRepository(ctx, owner, repo)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "open_repository")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// Get HEAD information
	headInfo, err := h.getHeadInfo(ctx, repository)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "get_head_info")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// Get all references
	references, err := h.getAllReferences(ctx, repository, headInfo)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "get_references")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// Get repository metadata
	repoInfo, err := h.getRepositoryMetadata(ctx, repository)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "get_repository_metadata")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// Get working tree status (only for non-bare repositories)
	var workingTree *WorkingTreeStatus
	if !repoInfo.IsBare {
		workingTree, err = h.getWorkingTreeStatus(ctx, repository)
		if err != nil {
			// Log warning but don't fail the request
			h.logger.Warn("Failed to get working tree status",
				zap.Error(err))
		}
	}

	// Prepare response
	response := RepositoryStateResponse{
		Success:        true,
		RepositoryName: repo,
		Owner:          owner,
		Head:           headInfo,
		References:     references,
		WorkingTree:    workingTree,
		RepositoryInfo: repoInfo,
		Timestamp:      time.Now().UTC(),
	}

	// Add additional details
	details := map[string]interface{}{
		"request_type": "repository_state",
		"includes": map[string]bool{
			"head_info":       headInfo != nil,
			"references":      references != nil,
			"working_tree":    workingTree != nil,
			"repository_info": repoInfo != nil,
		},
	}

	response.Details = details

	// Log success
	h.logger.Info("Repository state retrieved successfully",
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.String("head_type", headInfo.Type),
		zap.String("head_target", headInfo.Target),
		zap.Int("branches_count", references.Count.Branches),
		zap.Int("tags_count", references.Count.Tags),
		zap.Bool("is_bare", repoInfo.IsBare))

	// Send success response
	api.SendSuccess(c, response, "Repository state retrieved successfully")
}

// HandleGetRepositoryHead handles GET /repos/{owner}/{repo}/head for HEAD information only
func (h *RepositoryStateHandler) HandleGetRepositoryHead(c *gin.Context) {
	ctx := c.Request.Context()

	// Extract URL parameters
	owner := c.Param("owner")
	repo := c.Param("repo")

	h.logger.Info("Processing repository HEAD request",
		zap.String("owner", owner),
		zap.String("repo", repo))

	// Validate path components
	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo)); err != nil {
		apiErr := h.errorMapper.MapError(err, "validate_path")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// Validate permissions
	if err := h.validatePermissions(c, owner, repo, "read"); err != nil {
		if gitAPIErr, ok := err.(*api.GitAPIError); ok {
			c.JSON(gitAPIErr.HTTPStatusCode(), gin.H{"success": false, "error": gitAPIErr.Message})
		} else {
			c.JSON(http.StatusForbidden, gin.H{"success": false, "error": err.Error()})
		}
		return
	}

	// Open repository
	repository, err := h.objectManager.OpenRepository(ctx, owner, repo)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "open_repository")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// Get HEAD information
	headInfo, err := h.getHeadInfo(ctx, repository)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "get_head_info")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// Prepare response
	response := map[string]interface{}{
		"success":    true,
		"repository": fmt.Sprintf("%s/%s", owner, repo),
		"head":       headInfo,
		"timestamp":  time.Now().UTC(),
	}

	// Log success
	h.logger.Info("Repository HEAD retrieved successfully",
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.String("head_type", headInfo.Type),
		zap.String("head_target", headInfo.Target),
		zap.String("commit_sha", headInfo.CommitSHA))

	// Send success response
	api.SendSuccess(c, response, "Repository HEAD information retrieved successfully")
}

// HandleGetRepositoryReferences handles GET /repos/{owner}/{repo}/references for all references
func (h *RepositoryStateHandler) HandleGetRepositoryReferences(c *gin.Context) {
	ctx := c.Request.Context()

	// Extract URL parameters
	owner := c.Param("owner")
	repo := c.Param("repo")

	// Optional query parameters
	refType := c.Query("type") // "branches", "tags", "remotes", or empty for all
	includeCommitInfo := c.Query("include_commit_info") == "true"

	h.logger.Info("Processing repository references request",
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.String("type", refType),
		zap.Bool("include_commit_info", includeCommitInfo))

	// Validate path components
	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo)); err != nil {
		apiErr := h.errorMapper.MapError(err, "validate_path")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// Validate permissions
	if err := h.validatePermissions(c, owner, repo, "read"); err != nil {
		if gitAPIErr, ok := err.(*api.GitAPIError); ok {
			c.JSON(gitAPIErr.HTTPStatusCode(), gin.H{"success": false, "error": gitAPIErr.Message})
		} else {
			c.JSON(http.StatusForbidden, gin.H{"success": false, "error": err.Error()})
		}
		return
	}

	// Open repository
	repository, err := h.objectManager.OpenRepository(ctx, owner, repo)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "open_repository")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// Get HEAD information (needed for identifying current branch)
	headInfo, err := h.getHeadInfo(ctx, repository)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "get_head_info")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// Get references based on type filter
	references, err := h.getFilteredReferences(ctx, repository, headInfo, refType, includeCommitInfo)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "get_references")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// Prepare response
	response := map[string]interface{}{
		"success":    true,
		"repository": fmt.Sprintf("%s/%s", owner, repo),
		"references": references,
		"filters": map[string]interface{}{
			"type":                refType,
			"include_commit_info": includeCommitInfo,
		},
		"timestamp": time.Now().UTC(),
	}

	// Log success
	h.logger.Info("Repository references retrieved successfully",
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.String("filter_type", refType),
		zap.Int("total_references", references.Count.Total))

	// Send success response
	api.SendSuccess(c, response, "Repository references retrieved successfully")
}

// validatePermissions validates user permissions for repository access - DISABLED FOR DEVELOPMENT
func (h *RepositoryStateHandler) validatePermissions(c *gin.Context, owner, repo, operation string) error {
	// Permission validation disabled for development
	return nil
}

// getHeadInfo retrieves HEAD reference information
func (h *RepositoryStateHandler) getHeadInfo(ctx context.Context, repo *gogit.Repository) (*HeadReferenceInfo, error) {
	headRef, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("failed to get HEAD reference: %w", err)
	}

	// For bare repositories, check if HEAD is actually a symbolic reference
	// by looking at the raw HEAD file content
	repoPath := repo.Storer.(*filesystem.Storage).Filesystem().Root()
	headFilePath := filepath.Join(repoPath, "HEAD")
	headContent, err := os.ReadFile(headFilePath)

	isSymbolic := false
	targetBranch := ""

	if err == nil {
		headStr := strings.TrimSpace(string(headContent))
		if strings.HasPrefix(headStr, "ref: ") {
			isSymbolic = true
			targetBranch = strings.TrimPrefix(headStr, "ref: ")
		}
	}

	headInfo := &HeadReferenceInfo{
		Type:       headRef.Type().String(),
		CommitSHA:  headRef.Hash().String(),
		IsDetached: !isSymbolic, // Use the file content to determine if detached
	}

	if isSymbolic {
		// HEAD points to a branch (based on file content)
		headInfo.Target = targetBranch
		headInfo.Name = strings.TrimPrefix(targetBranch, "refs/heads/")
		headInfo.IsBranch = strings.HasPrefix(targetBranch, "refs/heads/")
	} else {
		// Detached HEAD
		headInfo.Target = headRef.Hash().String()
		headInfo.Name = "HEAD"
		headInfo.IsBranch = false
	}

	// Get commit information
	commit, err := repo.CommitObject(headRef.Hash())
	if err == nil {
		headInfo.CommitInfo = &common.CommitInfo{
			SHA:       commit.Hash.String(),
			Message:   commit.Message,
			CreatedAt: commit.Author.When,
			Author: common.AuthorInfo{
				Name:  commit.Author.Name,
				Email: commit.Author.Email,
				Date:  commit.Author.When,
			},
			Committer: common.AuthorInfo{
				Name:  commit.Committer.Name,
				Email: commit.Committer.Email,
				Date:  commit.Committer.When,
			},
		}
	}

	return headInfo, nil
}

// getAllReferences retrieves all repository references
func (h *RepositoryStateHandler) getAllReferences(ctx context.Context, repo *gogit.Repository, headInfo *HeadReferenceInfo) (*RepositoryReferences, error) {
	return h.getFilteredReferences(ctx, repo, headInfo, "", true)
}

// getFilteredReferences retrieves filtered repository references
func (h *RepositoryStateHandler) getFilteredReferences(ctx context.Context, repo *gogit.Repository, headInfo *HeadReferenceInfo, refType string, includeCommitInfo bool) (*RepositoryReferences, error) {
	refs := &RepositoryReferences{
		Branches: []BranchReference{},
		Tags:     []TagReference{},
		Remotes:  []RemoteReference{},
		Count:    ReferenceCounts{},
	}

	// Get all references
	refIter, err := repo.References()
	if err != nil {
		return nil, fmt.Errorf("failed to get references: %w", err)
	}

	err = refIter.ForEach(func(ref *plumbing.Reference) error {
		refName := ref.Name().String()

		switch {
		case strings.HasPrefix(refName, "refs/heads/"):
			if refType == "" || refType == "branches" {
				branch := h.createBranchReference(ref, headInfo, includeCommitInfo, repo)
				refs.Branches = append(refs.Branches, branch)
				refs.Count.Branches++
			}
		case strings.HasPrefix(refName, "refs/tags/"):
			if refType == "" || refType == "tags" {
				tag := h.createTagReference(ref, includeCommitInfo, repo)
				refs.Tags = append(refs.Tags, tag)
				refs.Count.Tags++
			}
		case strings.HasPrefix(refName, "refs/remotes/"):
			if refType == "" || refType == "remotes" {
				remote := h.createRemoteReference(ref, includeCommitInfo, repo)
				refs.Remotes = append(refs.Remotes, remote)
				refs.Count.Remotes++
			}
		}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to iterate references: %w", err)
	}

	// Sort references by name
	sort.Slice(refs.Branches, func(i, j int) bool {
		return refs.Branches[i].Name < refs.Branches[j].Name
	})
	sort.Slice(refs.Tags, func(i, j int) bool {
		return refs.Tags[i].Name < refs.Tags[j].Name
	})
	sort.Slice(refs.Remotes, func(i, j int) bool {
		return refs.Remotes[i].Name < refs.Remotes[j].Name
	})

	refs.Count.Total = refs.Count.Branches + refs.Count.Tags + refs.Count.Remotes

	return refs, nil
}

// createBranchReference creates a BranchReference from a plumbing.Reference
func (h *RepositoryStateHandler) createBranchReference(ref *plumbing.Reference, headInfo *HeadReferenceInfo, includeCommitInfo bool, repo *gogit.Repository) BranchReference {
	branchName := strings.TrimPrefix(ref.Name().String(), "refs/heads/")

	branch := BranchReference{
		Name:         branchName,
		FullName:     ref.Name().String(),
		CommitSHA:    ref.Hash().String(),
		IsHead:       headInfo != nil && headInfo.Target == ref.Name().String(),
		IsProtected:  h.isProtectedBranch(branchName),
		LastModified: time.Now(), // TODO: Get actual last modified time
	}

	if includeCommitInfo {
		if commit, err := repo.CommitObject(ref.Hash()); err == nil {
			branch.CommitInfo = &common.CommitInfo{
				SHA:       commit.Hash.String(),
				Message:   commit.Message,
				CreatedAt: commit.Author.When,
				Author: common.AuthorInfo{
					Name:  commit.Author.Name,
					Email: commit.Author.Email,
					Date:  commit.Author.When,
				},
				Committer: common.AuthorInfo{
					Name:  commit.Committer.Name,
					Email: commit.Committer.Email,
					Date:  commit.Committer.When,
				},
			}
		}
	}

	return branch
}

// createTagReference creates a TagReference from a plumbing.Reference
func (h *RepositoryStateHandler) createTagReference(ref *plumbing.Reference, includeCommitInfo bool, repo *gogit.Repository) TagReference {
	tagName := strings.TrimPrefix(ref.Name().String(), "refs/tags/")

	tag := TagReference{
		Name:         tagName,
		FullName:     ref.Name().String(),
		CommitSHA:    ref.Hash().String(),
		IsAnnotated:  false,      // Will be determined below
		LastModified: time.Now(), // TODO: Get actual last modified time
	}

	// Check if it's an annotated tag
	if tagObj, err := repo.TagObject(ref.Hash()); err == nil {
		tag.IsAnnotated = true
		tag.TagInfo = &TagInfo{
			Message:    tagObj.Message,
			TargetType: tagObj.TargetType.String(),
			TargetSHA:  tagObj.Target.String(),
		}

		if tagObj.Tagger.Name != "" {
			tag.TagInfo.Tagger = &common.AuthorInfo{
				Name:  tagObj.Tagger.Name,
				Email: tagObj.Tagger.Email,
				Date:  tagObj.Tagger.When,
			}
		}

		// Get the actual commit the tag points to
		if commit, err := tagObj.Commit(); err == nil {
			tag.CommitSHA = commit.Hash.String()

			if includeCommitInfo {
				tag.CommitInfo = &common.CommitInfo{
					SHA:       commit.Hash.String(),
					Message:   commit.Message,
					CreatedAt: commit.Author.When,
					Author: common.AuthorInfo{
						Name:  commit.Author.Name,
						Email: commit.Author.Email,
						Date:  commit.Author.When,
					},
					Committer: common.AuthorInfo{
						Name:  commit.Committer.Name,
						Email: commit.Committer.Email,
						Date:  commit.Committer.When,
					},
				}
			}
		}
	} else {
		// Lightweight tag - points directly to a commit
		if includeCommitInfo {
			if commit, err := repo.CommitObject(ref.Hash()); err == nil {
				tag.CommitInfo = &common.CommitInfo{
					SHA:       commit.Hash.String(),
					Message:   commit.Message,
					CreatedAt: commit.Author.When,
					Author: common.AuthorInfo{
						Name:  commit.Author.Name,
						Email: commit.Author.Email,
						Date:  commit.Author.When,
					},
					Committer: common.AuthorInfo{
						Name:  commit.Committer.Name,
						Email: commit.Committer.Email,
						Date:  commit.Committer.When,
					},
				}
			}
		}
	}

	return tag
}

// createRemoteReference creates a RemoteReference from a plumbing.Reference
func (h *RepositoryStateHandler) createRemoteReference(ref *plumbing.Reference, includeCommitInfo bool, repo *gogit.Repository) RemoteReference {
	fullName := ref.Name().String()
	parts := strings.Split(strings.TrimPrefix(fullName, "refs/remotes/"), "/")

	var remoteName, branchName string
	if len(parts) >= 2 {
		remoteName = parts[0]
		branchName = strings.Join(parts[1:], "/")
	}

	remote := RemoteReference{
		Name:         strings.TrimPrefix(fullName, "refs/remotes/"),
		FullName:     fullName,
		CommitSHA:    ref.Hash().String(),
		RemoteName:   remoteName,
		BranchName:   branchName,
		LastModified: time.Now(), // TODO: Get actual last modified time
	}

	if includeCommitInfo {
		if commit, err := repo.CommitObject(ref.Hash()); err == nil {
			remote.CommitInfo = &common.CommitInfo{
				SHA:       commit.Hash.String(),
				Message:   commit.Message,
				CreatedAt: commit.Author.When,
				Author: common.AuthorInfo{
					Name:  commit.Author.Name,
					Email: commit.Author.Email,
					Date:  commit.Author.When,
				},
				Committer: common.AuthorInfo{
					Name:  commit.Committer.Name,
					Email: commit.Committer.Email,
					Date:  commit.Committer.When,
				},
			}
		}
	}

	return remote
}

// getRepositoryMetadata retrieves repository metadata
func (h *RepositoryStateHandler) getRepositoryMetadata(ctx context.Context, repo *gogit.Repository) (*RepositoryMetadata, error) {
	worktree, err := repo.Worktree()
	isBare := err != nil

	metadata := &RepositoryMetadata{
		IsBare:      isBare,
		HasWorktree: !isBare,
	}

	if !isBare && worktree != nil {
		metadata.WorktreeRoot = worktree.Filesystem.Root()
	}

	// Try to determine default branch
	if headRef, err := repo.Head(); err == nil && headRef.Type() == plumbing.SymbolicReference {
		defaultBranch := strings.TrimPrefix(headRef.Target().String(), "refs/heads/")
		metadata.DefaultBranch = defaultBranch
	}

	return metadata, nil
}

// getWorkingTreeStatus retrieves working tree status for non-bare repositories
func (h *RepositoryStateHandler) getWorkingTreeStatus(ctx context.Context, repo *gogit.Repository) (*WorkingTreeStatus, error) {
	worktree, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("failed to get worktree: %w", err)
	}

	status, err := worktree.Status()
	if err != nil {
		return nil, fmt.Errorf("failed to get worktree status: %w", err)
	}

	workingTreeStatus := &WorkingTreeStatus{
		IsClean:        status.IsClean(),
		HasChanges:     !status.IsClean(),
		ModifiedFiles:  []string{},
		AddedFiles:     []string{},
		DeletedFiles:   []string{},
		UntrackedFiles: []string{},
		FileStatus:     make(map[string]FileStatusInfo),
	}

	for file, fileStatus := range status {
		workingTreeStatus.FileStatus[file] = FileStatusInfo{
			Staging:  string(fileStatus.Staging),
			Worktree: string(fileStatus.Worktree),
		}

		// Categorize files
		switch fileStatus.Worktree {
		case gogit.Modified:
			workingTreeStatus.ModifiedFiles = append(workingTreeStatus.ModifiedFiles, file)
		case gogit.Deleted:
			workingTreeStatus.DeletedFiles = append(workingTreeStatus.DeletedFiles, file)
		case gogit.Untracked:
			workingTreeStatus.UntrackedFiles = append(workingTreeStatus.UntrackedFiles, file)
		}

		if fileStatus.Staging == gogit.Added {
			workingTreeStatus.AddedFiles = append(workingTreeStatus.AddedFiles, file)
		}
	}

	// Sort file lists
	sort.Strings(workingTreeStatus.ModifiedFiles)
	sort.Strings(workingTreeStatus.AddedFiles)
	sort.Strings(workingTreeStatus.DeletedFiles)
	sort.Strings(workingTreeStatus.UntrackedFiles)

	return workingTreeStatus, nil
}

// isProtectedBranch checks if a branch is considered protected (reused from branch_operations.go)
func (h *RepositoryStateHandler) isProtectedBranch(branchName string) bool {
	protectedBranches := []string{"main", "master", "develop", "production"}
	for _, protected := range protectedBranches {
		if strings.EqualFold(branchName, protected) {
			return true
		}
	}
	return false
}
