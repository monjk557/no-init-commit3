// Package protocol은 Git tree REST API 엔드포인트를 구현합니다.
// 구조화된 JSON 응답을 제공하는 트리 작업을 처리합니다.
package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/git"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/collabops/git-executor/internal/repository"
	"github.com/collabops/git-executor/internal/schemas/responses"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// TreeHandler는 Git tree REST API 엔드포인트를 처리합니다.
// 디렉토리 구조 조회 요청을 받아서 JSON 형식으로 응답합니다.
type TreeHandler struct {
	pathConfig       *repository.PathConfig
	permissionClient permissions.PermissionClient
	repoManager      *git.DefaultRepositoryManager
}

// TreeRequestParams holds request parameters for tree endpoints
type TreeRequestParams struct {
	Owner string
	Repo  string
	Ref   string
	Path  string // Optional: subdirectory path to query
}

// TreeErrorResponse represents a standardized error response for tree endpoints
type TreeErrorResponse struct {
	Error   string `json:"error"`
	Code    string `json:"code,omitempty"`
	Details string `json:"details,omitempty"`
}

// NewTreeHandler는 새로운 tree 핸들러를 생성합니다.
// 설정과 권한 클라이언트를 사용하여 초기화합니다.
func NewTreeHandler(cfg *config.Config, permClient permissions.PermissionClient) (*TreeHandler, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	// Create path configuration
	pathConfig, err := repository.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	// Create logger
	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	// Create repository manager
	repoManager, err := git.NewRepositoryManager(pathConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create repository manager: %w", err)
	}

	return &TreeHandler{
		pathConfig:       pathConfig,
		permissionClient: permClient,
		repoManager:      repoManager,
	}, nil
}

// HandleTreeAPI handles GET /repos/:owner/:repo/tree?ref=:ref
func (h *TreeHandler) HandleTreeAPI(c *gin.Context) {
	ctx := c.Request.Context()
	owner := c.Param("owner")
	repo := c.Param("repo")
	refs := c.Query("refs")
	path := c.Query("path")
	recursiveStr := c.Query("recursive")
	recursive := false
	if recursiveStr != "" {
		recursive, _ = strconv.ParseBool(recursiveStr)
	}

	// 1. parameter 조합 검증
	// Validate permissions and get repository path
	repoPath, err := repository.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(404, gin.H{"success": false, "error": "Repository not found"})
		return
	}

	// 2. repository 존재 확인
	exists, err := h.repoManager.RepositoryExists(ctx, repoPath)
	if err != nil {
		c.JSON(404, gin.H{"success": false, "error": "Repository not found"})
		return
	}
	if !exists {
		c.JSON(404, gin.H{"success": false, "error": "Repository not found"})
		return
	}

	// Get tree structure
	if recursive {
		treeMap, err := h.getTreeStructureRecursive(ctx, repoPath, refs, path)
		if err != nil {
			h.handleTreeError(c.Writer, err, "get tree structure")
			return
		}
		// Wrap in response structure
		response := &responses.TreeEntriesRecursiveResponse{
			Success: true,
			Data:    treeMap,
		}
		// Send JSON response with map structure
		h.sendTreeJSONResponse(c.Writer, response, http.StatusOK)
	} else {
		treeEntries, err := h.getTreeStructure(ctx, repoPath, refs, path)
		if err != nil {
			h.handleTreeError(c.Writer, err, "get tree structure")
			return
		}
		// Send JSON response
		h.sendTreeJSONResponse(c.Writer, treeEntries, http.StatusOK)
	}
}

// getTreeStructure retrieves the tree structure using the Git repository interface
func (h *TreeHandler) getTreeStructure(ctx context.Context, repoPath, ref, path string) (*responses.TreeEntriesResponse, error) {
	// Open repository using the simple cache
	gitRepo, err := git.OpenRepositorySimple(repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}
	defer func() { _ = gitRepo.Close() }()

	// Get tree entries from Git repository interface
	entries, err := gitRepo.GetTree(ctx, ref, path)
	if err != nil {
		return nil, fmt.Errorf("failed to get tree: %w", err)
	}

	// Convert to response format
	var responseEntries []responses.TreeEntry
	// Normalize base path (remove leading/trailing slashes)
	basePath := strings.Trim(path, "/")

	for _, entry := range entries {
		// Convert type from git.EntryType to string expected by API
		entryType := ""
		switch entry.Type {
		case git.EntryTypeTree:
			entryType = "tree"
		case git.EntryTypeBlob:
			entryType = "blob"
		case git.EntryTypeCommit:
			// For submodules, we'll treat them as trees for the API response
			entryType = "tree"
		default:
			// Default to blob for unknown types
			entryType = "blob"
		}

		// Name is just the entry name (file or directory name)
		name := entry.Path

		// Path is the full path from repository root
		fullPath := entry.Path
		if basePath != "" {
			fullPath = basePath + "/" + entry.Path
		}

		responseEntries = append(responseEntries, responses.TreeEntry{
			Type: entryType,
			Name: name,
			Path: fullPath,
		})
	}

	return &responses.TreeEntriesResponse{
		Success: true,
		Data:    responseEntries,
	}, nil
}

// getTreeStructureRecursive retrieves tree structure recursively from root to the specified path
// Returns a map where keys are directory paths and values are tree entries in that directory
func (h *TreeHandler) getTreeStructureRecursive(ctx context.Context, repoPath, ref, targetPath string) (map[string][]responses.TreeEntry, error) {
	// Open repository using the simple cache
	gitRepo, err := git.OpenRepositorySimple(repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}
	defer func() { _ = gitRepo.Close() }()

	// Normalize target path
	targetPath = strings.Trim(targetPath, "/")

	// Build list of all parent directories from root to target path
	var pathsToExplore []string
	if targetPath == "" {
		// If target is root, only explore root
		pathsToExplore = []string{""}
	} else {
		// Start with root
		pathsToExplore = []string{""}

		// Split path into components and build all parent paths
		parts := strings.Split(targetPath, "/")
		currentPath := ""
		for _, part := range parts {
			if currentPath == "" {
				currentPath = part
			} else {
				currentPath = currentPath + "/" + part
			}
			pathsToExplore = append(pathsToExplore, currentPath)
		}
	}

	// Map to store results: path -> []TreeEntry
	result := make(map[string][]responses.TreeEntry)

	// Explore each path from root to target
	for _, path := range pathsToExplore {
		// Get tree entries for this path
		entries, err := gitRepo.GetTree(ctx, ref, path)
		if err != nil {
			// If path doesn't exist, skip it
			continue
		}

		// Convert to responses.TreeEntry format (same as getTreeStructure)
		basePath := strings.Trim(path, "/")
		var responseEntries []responses.TreeEntry

		for _, entry := range entries {
			// Convert type from git.EntryType to string expected by API
			entryType := ""
			switch entry.Type {
			case git.EntryTypeTree:
				entryType = "tree"
			case git.EntryTypeBlob:
				entryType = "blob"
			case git.EntryTypeCommit:
				// For submodules, we'll treat them as trees for the API response
				entryType = "tree"
			default:
				// Default to blob for unknown types
				entryType = "blob"
			}

			// Name is just the entry name (file or directory name)
			name := entry.Path

			// Path is the full path from repository root
			fullPath := entry.Path
			if basePath != "" {
				fullPath = basePath + "/" + entry.Path
			}

			responseEntries = append(responseEntries, responses.TreeEntry{
				Type: entryType,
				Name: name,
				Path: fullPath,
			})
		}

		result[path] = responseEntries
	}

	return result, nil
}

// sendTreeJSONResponse sends a standardized JSON response for tree endpoints
func (h *TreeHandler) sendTreeJSONResponse(w http.ResponseWriter, data interface{}, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if err := json.NewEncoder(w).Encode(data); err != nil {
		// Log error but can't change response at this point
		fmt.Printf("Failed to encode JSON response: %v\n", err)
	}
}

// sendTreeErrorResponse sends a standardized error response for tree endpoints
func (h *TreeHandler) sendTreeErrorResponse(w http.ResponseWriter, message string, statusCode int) {
	errorResp := TreeErrorResponse{
		Error: message,
	}
	h.sendTreeJSONResponse(w, errorResp, statusCode)
}

// handleTreeError categorizes errors and sends appropriate HTTP responses for tree endpoints
func (h *TreeHandler) handleTreeError(w http.ResponseWriter, err error, operation string) {
	if isTreeReferenceNotFoundError(err) {
		h.sendTreeErrorResponse(w, fmt.Sprintf("Reference not found: %v", err), http.StatusNotFound)
	} else if isTreeRepositoryNotFoundError(err) {
		h.sendTreeErrorResponse(w, fmt.Sprintf("Repository not found: %v", err), http.StatusNotFound)
	} else if isTreePathNotFoundError(err) {
		h.sendTreeErrorResponse(w, fmt.Sprintf("Path not found: %v", err), http.StatusNotFound)
	} else if strings.Contains(err.Error(), "permission") {
		h.sendTreeErrorResponse(w, fmt.Sprintf("Permission check failed: %v", err), http.StatusInternalServerError)
	} else if strings.Contains(err.Error(), "access denied") {
		h.sendTreeErrorResponse(w, "Access denied to repository", http.StatusForbidden)
	} else if strings.Contains(err.Error(), "authentication") {
		h.sendTreeErrorResponse(w, err.Error(), http.StatusUnauthorized)
	} else if strings.Contains(err.Error(), "missing") || strings.Contains(err.Error(), "invalid") {
		h.sendTreeErrorResponse(w, err.Error(), http.StatusBadRequest)
	} else {
		h.sendTreeErrorResponse(w, fmt.Sprintf("Failed to %s: %v", operation, err), http.StatusInternalServerError)
	}
}

// Validation helper functions

// isValidRepoIdentifier는 저장소 식별자(소유자/저장소)가 유효한지 확인합니다.
// 영숫자, 하이픈, 언더스코어, 닷만 허용합니다.
func isValidRepoIdentifier(identifier string) bool {
	if identifier == "" {
		return false
	}

	// Basic validation for repository owner/name
	// Allow alphanumeric, hyphens, underscores, dots
	for _, char := range identifier {
		if (char < 'a' || char > 'z') &&
			(char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') &&
			char != '-' && char != '_' && char != '.' {
			return false
		}
	}

	return true
}

// isValidGitRef는 Git 참조가 올바른 형식인지 확인합니다.
// 브랜치명, 태그명, 커밋 해시 등을 검증합니다.
func isValidGitRef(ref string) bool {
	if ref == "" {
		return false
	}

	// Basic validation for Git references
	// Allow alphanumeric, hyphens, underscores, dots, slashes (for branches like feature/xyz)
	for _, char := range ref {
		if (char < 'a' || char > 'z') &&
			(char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') &&
			char != '-' && char != '_' && char != '.' && char != '/' {
			return false
		}
	}

	return true
}

// Error type checking functions

// isTreeReferenceNotFoundError checks if error is a reference not found error
func isTreeReferenceNotFoundError(err error) bool {
	// Check for git.ReferenceNotFoundError or similar patterns
	if err == nil {
		return false
	}

	errStr := err.Error()
	return strings.Contains(errStr, "reference not found") ||
		strings.Contains(errStr, "revision not found") ||
		strings.Contains(errStr, "ref not found")
}

// isTreeRepositoryNotFoundError checks if error is a repository not found error
func isTreeRepositoryNotFoundError(err error) bool {
	// Check for git.RepositoryNotFoundError or similar patterns
	if err == nil {
		return false
	}

	errStr := err.Error()
	return strings.Contains(errStr, "repository not found") ||
		strings.Contains(errStr, "no such file or directory") ||
		strings.Contains(errStr, "not a git repository")
}

// isTreePathNotFoundError checks if error is a path not found error
func isTreePathNotFoundError(err error) bool {
	// Check for git.PathNotFoundError or similar patterns
	if err == nil {
		return false
	}

	// Check for the specific PathNotFoundError type
	var pathErr *git.PathNotFoundError
	if errors.As(err, &pathErr) {
		return true
	}

	errStr := err.Error()
	return strings.Contains(errStr, "path") && strings.Contains(errStr, "not found")
}

// HandleGetTreeDetail handles GET /api/v1/repository/{owner}/{repo}/tree for tree detail
func (h *RepositoryDetailHandler) HandleGetTreeDetail(c *gin.Context) {
	ctx := c.Request.Context()
	owner := c.Param("owner")
	repo := c.Param("repo")
	refs := c.Query("refs")
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
		apiErr := h.errorMapper.MapError(err, "repository_not_found")
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

	// 8. path가 제공된 경우, 해당 브랜치에 path가 존재하는지 확인
	if path != "" && path != "/" {
		_, err := gitRepo.GetTree(ctx, refs, path)
		if err != nil {
			// Check if it's a PathNotFoundError
			var pathErr *git.PathNotFoundError
			if errors.As(err, &pathErr) {
				apiErr := h.errorMapper.MapError(err, "path_not_found")
				c.JSON(404, gin.H{"success": false, "error": apiErr.Message})
				return
			}
			// Other errors might be temporary, but we should still return error
			apiErr := h.errorMapper.MapError(err, "get_tree")
			c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
			return
		}
	}

	// 5. commit history 가져오기 (빈 저장소 확인용)
	commits, err := gitRepo.GetCommits(ctx, refs, 10, 0)
	emptyRepository := false
	if err != nil || len(commits) == 0 {
		emptyRepository = true
	}

	// commit이 없는 빈 저장소인 경우 early return
	if emptyRepository {
		response := responses.RepositoryTreeResponse{
			Repository:    fmt.Sprintf("%s/%s", owner, repo),
			Owner:         owner,
			Name:          repo,
			Path:          repoPath,
			LatestCommits: git.Commit{},
			Tree:          []git.TreeEntry{},
			Branches:      []git.Branch{},
			Readme:        "",
		}
		api.SendSuccess(c, response, "Empty repository - no commits found")
		return
	}

	// Path의 Tree (Commit info)
	// 5. tree entries 가져오기 (커밋 정보 포함)
	treeEntries, err := gitRepo.GetTreeWithCommits(ctx, refs, path)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "get_tree")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// Normalize base path (remove leading/trailing slashes)
	basePath := strings.Trim(path, "/")

	// Convert tree entries: Path should be full path, Name should be just the file/dir name
	convertedTreeEntries := make([]git.TreeEntry, len(treeEntries))
	for i, entry := range treeEntries {
		// Name is just the entry name (file or directory name)
		name := entry.Path

		// Path is the full path from repository root
		fullPath := entry.Path
		if basePath != "" {
			fullPath = basePath + "/" + entry.Path
		}

		convertedTreeEntries[i] = git.TreeEntry{
			Path:         fullPath,
			Name:         name,
			Mode:         entry.Mode,
			Type:         entry.Type,
			Hash:         entry.Hash,
			Size:         entry.Size,
			LatestCommit: entry.LatestCommit,
		}
	}

	treeEntries = convertedTreeEntries

	// tree entries의 LatestCommit 중에서 가장 최근 커밋 찾기
	var latestCommitValue git.Commit
	if len(treeEntries) > 0 {
		var latestCommit *git.Commit
		for _, entry := range treeEntries {
			if entry.LatestCommit != nil {
				if latestCommit == nil || entry.LatestCommit.Committer.Date.After(latestCommit.Committer.Date) {
					latestCommit = entry.LatestCommit
				}
			}
		}
		if latestCommit != nil {
			latestCommitValue = *latestCommit
		}
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

	// 9. readme 가져오기 (현재 경로의 README.md)
	readmePath := "README.md"
	if path != "/" && path != "" {
		readmePath = fmt.Sprintf("%s/README.md", path)
	}

	readmeData, err := gitRepo.GetBlob(ctx, readmePath, refs)
	if err != nil {
		// README가 없을 수도 있으므로 빈 문자열로 처리
		readmeData = []byte("")
	}

	// 10. response 생성
	// latestCommit이 nil이 아닌 경우에만 역참조

	response := responses.RepositoryTreeResponse{
		Repository:    fmt.Sprintf("%s/%s", owner, repo),
		Owner:         owner,
		Name:          repo,
		Path:          repoPath,
		TreePath:      path,
		LatestCommits: latestCommitValue,
		Tree:          treeEntries,
		Branches:      branches,
		Readme:        string(readmeData),
	}

	api.SendSuccess(c, response, "Repository tree detail retrieved successfully")

}
