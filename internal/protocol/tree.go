// Package protocol은 Git tree REST API 엔드포인트를 구현합니다.
// 구조화된 JSON 응답을 제공하는 트리 작업을 처리합니다.
package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/git"
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
}

// TreeRequestParams holds request parameters for tree endpoints
type TreeRequestParams struct {
	Owner  string
	Repo   string
	Ref    string
	Path   string // Optional: subdirectory path to query
	UserID string
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

	return &TreeHandler{
		pathConfig:       pathConfig,
		permissionClient: permClient,
	}, nil
}

// HandleTreeAPI handles GET /repos/:owner/:repo/tree?ref=:ref
func (h *TreeHandler) HandleTreeAPI(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract and validate parameters
	params, err := h.validateAndExtractTreeParams(r)
	if err != nil {
		h.handleTreeError(w, err, "extract parameters")
		return
	}

	// Validate permissions and get repository path
	repoPath, err := h.validateTreeRequestAndPermissions(ctx, params)
	if err != nil {
		h.handleTreeError(w, err, "validate permissions")
		return
	}

	// Get tree structure
	treeEntries, err := h.getTreeStructure(ctx, repoPath, params.Ref, params.Path)
	if err != nil {
		h.handleTreeError(w, err, "get tree structure")
		return
	}

	// Send JSON response
	h.sendTreeJSONResponse(w, treeEntries, http.StatusOK)
}

// validateAndExtractTreeParams extracts and validates parameters from tree request
func (h *TreeHandler) validateAndExtractTreeParams(r *http.Request) (*TreeRequestParams, error) {
	// Extract path parameters
	owner := URLParam(r, "owner")
	repo := URLParam(r, "repo")

	// Extract query parameters
	ref := r.URL.Query().Get("ref")
	path := r.URL.Query().Get("path") // Optional: subdirectory path

	// Validate required parameters
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("missing required parameters: owner or repo")
	}

	if ref == "" {
		return nil, fmt.Errorf("missing required parameter: ref")
	}

	// Basic validation of parameter format
	if !isValidRepoIdentifier(owner) {
		return nil, fmt.Errorf("invalid owner format")
	}

	if !isValidRepoIdentifier(repo) {
		return nil, fmt.Errorf("invalid repo format")
	}

	if !isValidGitRef(ref) {
		return nil, fmt.Errorf("invalid ref format")
	}

	// Get user ID from header for permission checking
	userID := r.Header.Get("X-USER-ID")
	if userID == "" {
		return nil, fmt.Errorf("missing user authentication")
	}

	return &TreeRequestParams{
		Owner:  owner,
		Repo:   repo,
		Ref:    ref,
		Path:   path,
		UserID: userID,
	}, nil
}

// validateTreeRequestAndPermissions performs validation and permission checking for tree requests
func (h *TreeHandler) validateTreeRequestAndPermissions(ctx context.Context, params *TreeRequestParams) (string, error) {
	// If permission client is nil, skip permission checking (for development/testing)
	if h.permissionClient != nil {
		// Check user permissions (read access for tree operations)
		hasAccess, err := h.permissionClient.CheckReadAccess(ctx, params.UserID, params.Owner, params.Repo)
		if err != nil {
			return "", fmt.Errorf("permission check failed: %w", err)
		}
		if !hasAccess {
			return "", fmt.Errorf("access denied to repository")
		}
	}

	// Get repository path and validate
	repoPath, err := repository.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		return "", fmt.Errorf("invalid repository path: %w", err)
	}

	return repoPath, nil
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

		responseEntries = append(responseEntries, responses.TreeEntry{
			Type: entryType,
			Name: entry.Path, // In GetTree, Path contains just the name
			Path: entry.Path, // For root level entries, name and path are the same
		})
	}

	return &responses.TreeEntriesResponse{
		Success: true,
		Data:    responseEntries,
	}, nil
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

	// 5. commit history 가져오기 (빈 저장소 확인용)
	commits, err := gitRepo.GetCommits(ctx, refs, 10, 0)
	emptyRepository := false
	if err != nil || len(commits) == 0 {
		emptyRepository = true
	}

	// commit이 없는 빈 저장소인 경우 early return
	if emptyRepository {
		response := responses.RepositoryTreeResponse{
			Repository: fmt.Sprintf("%s/%s", owner, repo),
			Owner:      owner,
			Name:       repo,
			Path:       repoPath,
			Tree:       make(map[string][]git.TreeEntry),
		}
		api.SendSuccess(c, response, "Empty repository - no commits found")
		return
	}

	// recursive tree 가져오기
	// path가 중첩된 경우 부모 경로들도 모두 가져오기 (커밋 정보 없이)
	// 경로별로 그룹화하여 map으로 반환
	tree := make(map[string][]git.TreeEntry)

	if path == "" || path == "/" {
		// 루트 경로인 경우 그냥 가져오기
		entries, err := gitRepo.GetTree(ctx, refs, "/")
		if err != nil {
			apiErr := h.errorMapper.MapError(err, "get_sidebar_tree")
			c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
			return
		}
		tree["/"] = entries
	} else {
		// 중첩된 경로인 경우 부모 경로들도 재귀적으로 가져오기
		pathParts := strings.Split(strings.Trim(path, "/"), "/")
		currentPath := ""

		// 루트부터 시작
		entries, err := gitRepo.GetTree(ctx, refs, "/")
		if err != nil {
			apiErr := h.errorMapper.MapError(err, "get_sidebar_tree")
			c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
			return
		}
		tree["/"] = entries

		// 각 부모 경로들 가져오기
		for _, part := range pathParts {
			currentPath = currentPath + "/" + part
			entries, err := gitRepo.GetTree(ctx, refs, currentPath)
			if err != nil {
				apiErr := h.errorMapper.MapError(err, "get_sidebar_tree")
				c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
				return
			}
			tree[currentPath] = entries
		}
	}
	// 10. response 생성
	response := responses.RepositoryTreeResponse{
		Repository: fmt.Sprintf("%s/%s", owner, repo),
		Owner:      owner,
		Name:       repo,
		Path:       repoPath,
		Tree:       tree,
	}

	api.SendSuccess(c, response, "Repository tree detail retrieved successfully")

}
