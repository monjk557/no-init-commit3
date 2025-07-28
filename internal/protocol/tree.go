// Package protocol implements Git tree REST API endpoints for the executor.
// This file handles tree operations that provide structured JSON responses.
package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/git"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/collabops/git-executor/internal/repository"
	"github.com/go-chi/chi/v5"
)

// TreeHandler handles Git tree REST API endpoints
type TreeHandler struct {
	pathConfig       *repository.PathConfig
	permissionClient permissions.PermissionClient
}

// TreeEntry represents a tree entry in the JSON response
type TreeEntryResponse struct {
	Type string `json:"type"` // "tree" or "blob"
	Name string `json:"name"` // Entry name
	Path string `json:"path"` // Full path
}

// TreeRequestParams holds request parameters for tree endpoints
type TreeRequestParams struct {
	Owner  string
	Repo   string
	Ref    string
	UserID string
}

// TreeErrorResponse represents a standardized error response for tree endpoints
type TreeErrorResponse struct {
	Error   string `json:"error"`
	Code    string `json:"code,omitempty"`
	Details string `json:"details,omitempty"`
}

// NewTreeHandler creates a new tree handler
func NewTreeHandler(cfg *config.Config, permClient permissions.PermissionClient) (*TreeHandler, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	if permClient == nil {
		return nil, fmt.Errorf("permission client cannot be nil")
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
	treeEntries, err := h.getTreeStructure(ctx, repoPath, params.Ref)
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
	owner := chi.URLParam(r, "owner")
	repo := chi.URLParam(r, "repo")

	// Extract ref query parameter
	ref := r.URL.Query().Get("ref")

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
		UserID: userID,
	}, nil
}

// validateTreeRequestAndPermissions performs validation and permission checking for tree requests
func (h *TreeHandler) validateTreeRequestAndPermissions(ctx context.Context, params *TreeRequestParams) (string, error) {
	// Check user permissions (read access for tree operations)
	hasAccess, err := h.permissionClient.CheckReadAccess(ctx, params.UserID, params.Owner, params.Repo)
	if err != nil {
		return "", fmt.Errorf("permission check failed: %w", err)
	}
	if !hasAccess {
		return "", fmt.Errorf("access denied to repository")
	}

	// Get repository path and validate
	repoPath, err := repository.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		return "", fmt.Errorf("invalid repository path: %w", err)
	}

	return repoPath, nil
}

// getTreeStructure retrieves the tree structure using the Git repository interface
func (h *TreeHandler) getTreeStructure(ctx context.Context, repoPath, ref string) ([]TreeEntryResponse, error) {
	// Open repository using the simple cache
	gitRepo, err := git.OpenRepositorySimple(repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}
	defer func() { _ = gitRepo.Close() }()

	// Get tree entries from Git repository interface
	entries, err := gitRepo.GetTree(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("failed to get tree: %w", err)
	}

	// Convert to response format
	var responseEntries []TreeEntryResponse
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

		responseEntries = append(responseEntries, TreeEntryResponse{
			Type: entryType,
			Name: entry.Path, // In GetTree, Path contains just the name
			Path: entry.Path, // For root level entries, name and path are the same
		})
	}

	return responseEntries, nil
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

// isValidRepoIdentifier checks if a repository identifier (owner/repo) is valid
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

// isValidGitRef checks if a Git reference is valid format
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
