// Package protocol은 Git blob REST API 엔드포인트를 구현합니다.
// 적절한 Content-Type 헤더와 함께 파일 내용을 반환하는 blob 작업을 처리합니다.
package protocol

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/git"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/collabops/git-executor/internal/repository"
	"github.com/collabops/git-executor/internal/schemas/responses"
	"github.com/gin-gonic/gin"
)

// BlobHandler는 Git blob REST API 엔드포인트를 처리합니다.
// 파일 내용 조회 요청을 받아서 적절한 형식으로 응답합니다.
type BlobHandler struct {
	pathConfig       *repository.PathConfig
	permissionClient permissions.PermissionClient
}

// BlobRequestParams는 blob 엔드포인트의 요청 매개변수들을 포함합니다.
// 소유자, 저장소, 파일 경로, 참조 및 사용자 ID를 포함합니다.
type BlobRequestParams struct {
	Owner  string
	Repo   string
	Path   string
	Ref    string
	UserID string
}

// BlobErrorResponse represents a standardized error response for blob endpoints
type BlobErrorResponse struct {
	Error   string `json:"error"`
	Code    string `json:"code,omitempty"`
	Details string `json:"details,omitempty"`
}

// NewBlobHandler는 새로운 blob 핸들러를 생성합니다.
// 경로 설정과 권한 클라이언트를 사용하여 초기화합니다.
func NewBlobHandler(pathConfig *repository.PathConfig, permClient permissions.PermissionClient) (*BlobHandler, error) {
	if pathConfig == nil {
		var err error
		pathConfig, err = repository.NewPathConfig("/tmp/repos") // Default for tests
		if err != nil {
			return nil, fmt.Errorf("failed to create path config: %w", err)
		}
	}

	return &BlobHandler{
		pathConfig:       pathConfig,
		permissionClient: permClient,
	}, nil
}

// HandleBlobAPI handles GET /repos/{owner}/{repo}/blob?path=<file_path>&ref=<git_ref> (for HTTP handler)
func (h *BlobHandler) HandleBlobAPI(w http.ResponseWriter, r *http.Request) {
	// Extract parameters using chi for backward compatibility (tests)
	owner := extractURLParam(r, "owner")
	repo := extractURLParam(r, "repo")

	h.processBlobRequest(w, r, owner, repo)
}

// HandleBlobAPIGin handles GET /repos/{owner}/{repo}/blob?path=<file_path>&ref=<git_ref> (for Gin)
func (h *BlobHandler) HandleBlobAPIGin(c *gin.Context) {
	owner := c.Param("owner")
	repo := c.Param("repo")

	ctx := c.Request.Context()

	// Extract and validate parameters
	params, err := h.validateAndExtractBlobParams(c.Request, owner, repo)
	if err != nil {
		c.JSON(404, gin.H{"error": "Invalid request parameters"})
		return
	}

	// Validate permissions and get repository path
	repoPath, err := h.validateBlobRequestAndPermissions(ctx, params)
	if err != nil {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}

	// Get blob content
	content, err := h.getBlobContent(ctx, repoPath, params.Path, params.Ref)
	if err != nil {
		// Hardcoded 404 response
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}

	// Detect content type
	contentType := h.detectContentType(content, params.Path)

	// Set headers
	c.Header("Content-Type", contentType)
	c.Header("Content-Length", fmt.Sprintf("%d", len(content)))
	c.Header("Cache-Control", "public, max-age=3600")

	// For binary content
	if h.isBinaryContent(content, contentType) && h.shouldBase64Encode(contentType) {
		c.Header("X-Content-Encoding", "binary")
	}

	// Write content
	c.Data(http.StatusOK, contentType, content)
}

// processBlobRequest processes the actual blob request
func (h *BlobHandler) processBlobRequest(w http.ResponseWriter, r *http.Request, owner, repo string) {
	ctx := r.Context()

	// Extract and validate parameters
	params, err := h.validateAndExtractBlobParams(r, owner, repo)
	if err != nil {
		h.handleBlobError(w, err, "extract parameters")
		return
	}

	// Validate permissions and get repository path
	repoPath, err := h.validateBlobRequestAndPermissions(ctx, params)
	if err != nil {
		h.handleBlobError(w, err, "validate permissions")
		return
	}

	// Get blob content
	content, err := h.getBlobContent(ctx, repoPath, params.Path, params.Ref)
	if err != nil {
		h.sendBlobErrorResponse(w, fmt.Sprintf("File not found: %v", err), http.StatusNotFound)
		return
	}

	// Detect content type and format response
	h.sendBlobResponse(w, content, params.Path)
}

// extractURLParam extracts URL parameter for backward compatibility with chi
func extractURLParam(r *http.Request, key string) string {
	return URLParam(r, key)
}

// validateAndExtractBlobParams extracts and validates parameters from blob request
func (h *BlobHandler) validateAndExtractBlobParams(r *http.Request, owner, repo string) (*BlobRequestParams, error) {
	// Extract path parameters
	// owner := chi.URLParam(r, "owner") // This line is now handled by processBlobRequest
	// repo := chi.URLParam(r, "repo")   // This line is now handled by processBlobRequest

	// Extract query parameters
	path := r.URL.Query().Get("path")
	ref := r.URL.Query().Get("ref")

	// Validate required parameters
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("missing required parameters: owner or repo")
	}

	if path == "" {
		return nil, fmt.Errorf("missing required parameter: path")
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

	// Validate and sanitize path to prevent directory traversal
	if err := h.validateFilePath(path); err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}

	// Get user ID from header for permission checking
	userID := r.Header.Get("X-USER-ID")
	if userID == "" {
		return nil, fmt.Errorf("missing user authentication")
	}

	return &BlobRequestParams{
		Owner:  owner,
		Repo:   repo,
		Path:   path,
		Ref:    ref,
		UserID: userID,
	}, nil
}

// validateFilePath validates file path and prevents directory traversal attacks
func (h *BlobHandler) validateFilePath(path string) error {
	if path == "" {
		return fmt.Errorf("path cannot be empty")
	}

	// Check for directory traversal attempts
	if strings.Contains(path, "..") {
		return fmt.Errorf("path cannot contain '..' (directory traversal)")
	}

	// Check for absolute paths
	if filepath.IsAbs(path) {
		return fmt.Errorf("path cannot be absolute")
	}

	// Check for other suspicious patterns
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, "\\") {
		return fmt.Errorf("path cannot start with separator")
	}

	// Normalize and validate the path
	normalized := filepath.Clean(path)
	if normalized != path && normalized != strings.ReplaceAll(path, "\\", "/") {
		return fmt.Errorf("path contains invalid characters or patterns")
	}

	return nil
}

// validateBlobRequestAndPermissions performs validation and permission checking for blob requests
func (h *BlobHandler) validateBlobRequestAndPermissions(ctx context.Context, params *BlobRequestParams) (string, error) {

	// Get repository path and validate
	repoPath, err := repository.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		return "", fmt.Errorf("invalid repository path: %w", err)
	}

	return repoPath, nil
}

// getBlobContent retrieves the blob content using the Git repository interface
func (h *BlobHandler) getBlobContent(ctx context.Context, repoPath, path, ref string) ([]byte, error) {
	// Open repository using the simple cache
	gitRepo, err := git.OpenRepositorySimple(repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}
	defer func() { _ = gitRepo.Close() }()

	// Get blob content from Git repository interface
	content, err := gitRepo.GetBlob(ctx, path, ref)

	if err != nil {
		return nil, fmt.Errorf("failed to get blob: %w", err)
	}

	return content, nil
}

// sendBlobResponse sends the blob content with appropriate Content-Type header
func (h *BlobHandler) sendBlobResponse(w http.ResponseWriter, content []byte, filename string) {
	// Detect content type
	contentType := h.detectContentType(content, filename)

	// Set Content-Type header
	w.Header().Set("Content-Type", contentType)

	// Set Content-Length header
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))

	// Set cache headers for static content
	w.Header().Set("Cache-Control", "public, max-age=3600")

	// For binary content, check if we should base64 encode
	if h.isBinaryContent(content, contentType) && h.shouldBase64Encode(contentType) {
		// For API responses that need JSON formatting, we might want to base64 encode
		// However, for blob endpoints, it's typical to return binary content directly
		// We'll return binary content directly but add a header to indicate encoding
		w.Header().Set("X-Content-Encoding", "binary")
	}

	// Write status code
	w.WriteHeader(http.StatusOK)

	// Write content
	if _, err := w.Write(content); err != nil {
		// Can't change response at this point, but log the error
		fmt.Printf("Failed to write blob response: %v\n", err)
	}
}

// detectContentType determines the appropriate Content-Type header for the content
func (h *BlobHandler) detectContentType(content []byte, filename string) string {
	// First try to detect by file extension
	ext := filepath.Ext(filename)
	if contentType := mime.TypeByExtension(ext); contentType != "" {
		return contentType
	}

	// Fall back to content detection
	contentType := http.DetectContentType(content)

	// Special handling for common cases where detection might be imprecise
	if contentType == "text/plain; charset=utf-8" {
		// Check for specific file types that should have specific MIME types
		switch ext {
		case ".json":
			return "application/json"
		case ".yaml", ".yml":
			return "application/x-yaml"
		case ".xml":
			return "application/xml"
		case ".css":
			return "text/css"
		case ".js":
			return "application/javascript"
		case ".html", ".htm":
			return "text/html"
		case ".md", ".markdown":
			return "text/markdown"
		case ".go":
			return "text/x-go"
		case ".py":
			return "text/x-python"
		case ".sh":
			return "application/x-sh"
		}
	}

	return contentType
}

// isBinaryContent checks if the content is binary
func (h *BlobHandler) isBinaryContent(content []byte, contentType string) bool {
	// Check Content-Type first
	if strings.HasPrefix(contentType, "text/") ||
		strings.HasPrefix(contentType, "application/json") ||
		strings.HasPrefix(contentType, "application/xml") ||
		strings.HasPrefix(contentType, "application/javascript") {
		return false
	}

	// Check if content is valid UTF-8 text
	return !utf8.Valid(content)
}

// shouldBase64Encode determines if binary content should be base64 encoded
func (h *BlobHandler) shouldBase64Encode(contentType string) bool {
	// For direct blob access, we typically don't base64 encode
	// Base64 encoding is more useful for JSON API responses
	// This could be controlled by a query parameter or Accept header in the future
	return false
}

// sendBlobErrorResponse sends a standardized error response for blob endpoints
func (h *BlobHandler) sendBlobErrorResponse(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	// Create simple JSON error response
	errorJSON := fmt.Sprintf(`{"error": "%s"}`, strings.ReplaceAll(message, `"`, `\"`))
	if _, err := w.Write([]byte(errorJSON)); err != nil {
		fmt.Printf("Failed to write error response: %v\n", err)
	}
}

// handleBlobError categorizes errors and sends appropriate HTTP responses for blob endpoints
func (h *BlobHandler) handleBlobError(w http.ResponseWriter, err error, operation string) {
	if isBlobPathNotFoundError(err) {
		h.sendBlobErrorResponse(w, fmt.Sprintf("File not found: %v", err), http.StatusNotFound)
	} else if isBlobReferenceNotFoundError(err) {
		h.sendBlobErrorResponse(w, fmt.Sprintf("Reference not found: %v", err), http.StatusNotFound)
	} else if isBlobRepositoryNotFoundError(err) {
		h.sendBlobErrorResponse(w, fmt.Sprintf("Repository not found: %v", err), http.StatusNotFound)
	} else if strings.Contains(err.Error(), "permission") {
		h.sendBlobErrorResponse(w, fmt.Sprintf("Permission check failed: %v", err), http.StatusInternalServerError)
	} else if strings.Contains(err.Error(), "access denied") {
		h.sendBlobErrorResponse(w, "Access denied to repository", http.StatusForbidden)
	} else if strings.Contains(err.Error(), "authentication") {
		h.sendBlobErrorResponse(w, err.Error(), http.StatusUnauthorized)
	} else if strings.Contains(err.Error(), "missing") || strings.Contains(err.Error(), "invalid") {
		h.sendBlobErrorResponse(w, err.Error(), http.StatusBadRequest)
	} else {
		h.sendBlobErrorResponse(w, fmt.Sprintf("Failed to %s: %v", operation, err), http.StatusInternalServerError)
	}
}

// Error type checking functions for blob operations

// isBlobPathNotFoundError checks if error is a path not found error
func isBlobPathNotFoundError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	return strings.Contains(errStr, "path not found") ||
		strings.Contains(errStr, "file not found") ||
		strings.Contains(errStr, "PathNotFoundError")
}

// isBlobReferenceNotFoundError checks if error is a reference not found error
func isBlobReferenceNotFoundError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	return strings.Contains(errStr, "reference not found") ||
		strings.Contains(errStr, "revision not found") ||
		strings.Contains(errStr, "ref not found")
}

// isBlobRepositoryNotFoundError checks if error is a repository not found error
func isBlobRepositoryNotFoundError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	return strings.Contains(errStr, "repository not found") ||
		strings.Contains(errStr, "no such file or directory") ||
		strings.Contains(errStr, "not a git repository")
}

func (h *RepositoryDetailHandler) HandleGetBlobDetail(c *gin.Context) {
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

	// 1. parameter 조합 검증
	repoPath, err := repository.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "get_repository_path")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// 2. repository 존재 확인
	exists, err := h.repoManager.RepositoryExists(ctx, repoPath)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "check_repository_exists")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}
	if !exists {
		c.JSON(404, gin.H{"success": false, "error": "Repository not found"})
		return
	}

	// 3. repository 열기
	gitRepo, err := git.OpenRepositorySimple(repoPath)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "open_repository")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// 6. branches 가져오기
	branches, err := gitRepo.GetBranches(ctx)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "repository_corrupted")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
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
	}

	// 4. blob 가져오기
	// Lines, Bytes
	blob, err := gitRepo.GetBlob(ctx, path, ref)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "get_blob")
		c.JSON(404, gin.H{"success": false, "error": apiErr.Message})
		return
	}

	lines := strings.Count(strings.ReplaceAll(string(blob), "\r\n", "\n"), "\n") + 1

	size := int64(len(blob))

	// 5. tree entries 가져오기
	treeEntries, err := gitRepo.GetTree(ctx, ref, "")
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "get_tree")
		c.JSON(404, gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// 6. 파일의 커밋 히스토리 가져오기
	commits, err := gitRepo.GetCommitsForFile(ctx, path, ref, 9999999, 0)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "get_commits")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// 7. 최신 히스토리
	latestCommit := commits[0]

	// 6. response 생성
	response := responses.BlobDetailResponse{
		Success:      true,
		Content:      string(blob),
		Lines:        lines,
		Size:         size,
		Tree:         treeEntries,
		Branches:     branches,
		Commits:      commits,
		LatestCommit: latestCommit,
	}

	api.SendSuccess(c, response, "Blob retrieved successfully")

}
