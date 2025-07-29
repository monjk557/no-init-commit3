// Package protocol implements Git commit log REST API endpoints for the executor.
// This file handles commit history operations that return paginated commit data.
package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/git"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/collabops/git-executor/internal/repository"
)

// CommitsHandler handles Git commit log REST API endpoints
type CommitsHandler struct {
	pathConfig       *repository.PathConfig
	permissionClient permissions.PermissionClient
}

// CommitsRequestParams holds request parameters for commit log endpoints
type CommitsRequestParams struct {
	Owner  string
	Repo   string
	Ref    string
	Limit  int
	Skip   int
	UserID string
}

// CommitsResponse represents the response structure for commit log endpoints
type CommitsResponse struct {
	Commits    []git.Commit   `json:"commits"`
	Pagination PaginationInfo `json:"pagination"`
}

// PaginationInfo represents pagination information in commit responses
type PaginationInfo struct {
	Limit   int  `json:"limit"`
	Skip    int  `json:"skip"`
	Count   int  `json:"count"`    // Number of commits returned
	HasMore bool `json:"has_more"` // Whether there are more commits available
}

// CommitsErrorResponse represents a standardized error response for commit endpoints
type CommitsErrorResponse struct {
	Error   string `json:"error"`
	Code    string `json:"code,omitempty"`
	Details string `json:"details,omitempty"`
}

// Default pagination values
const (
	DefaultCommitsLimit = 30
	MaxCommitsLimit     = 100
	MinCommitsLimit     = 1
)

// NewCommitsHandler creates a new commits handler
func NewCommitsHandler(cfg *config.Config, permClient permissions.PermissionClient) (*CommitsHandler, error) {
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

	return &CommitsHandler{
		pathConfig:       pathConfig,
		permissionClient: permClient,
	}, nil
}

// HandleCommitsAPI handles GET /repos/:owner/:repo/commits?ref=:ref&limit=:limit&skip=:skip
func (h *CommitsHandler) HandleCommitsAPI(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract and validate parameters
	params, err := h.validateAndExtractCommitsParams(r)
	if err != nil {
		h.handleCommitsError(w, err, "extract parameters")
		return
	}

	// Validate permissions and get repository path
	repoPath, err := h.validateCommitsRequestAndPermissions(ctx, params)
	if err != nil {
		h.handleCommitsError(w, err, "validate permissions")
		return
	}

	// Get commit history
	commits, hasMore, err := h.getCommitHistory(ctx, repoPath, params)
	if err != nil {
		h.handleCommitsError(w, err, "get commit history")
		return
	}

	// Create response with pagination info
	response := CommitsResponse{
		Commits: commits,
		Pagination: PaginationInfo{
			Limit:   params.Limit,
			Skip:    params.Skip,
			Count:   len(commits),
			HasMore: hasMore,
		},
	}

	// Send JSON response
	h.sendCommitsJSONResponse(w, response, http.StatusOK)
}

// validateAndExtractCommitsParams extracts and validates parameters from commit request
func (h *CommitsHandler) validateAndExtractCommitsParams(r *http.Request) (*CommitsRequestParams, error) {
	// Extract path parameters
	owner := URLParam(r, "owner")
	repo := URLParam(r, "repo")

	// Extract query parameters
	ref := r.URL.Query().Get("ref")
	limitStr := r.URL.Query().Get("limit")
	skipStr := r.URL.Query().Get("skip")

	// Validate required parameters
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("missing required parameters: owner or repo")
	}

	// Default ref to "HEAD" if not provided
	if ref == "" {
		ref = "HEAD"
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

	// Parse and validate limit parameter
	limit := DefaultCommitsLimit
	if limitStr != "" {
		parsedLimit, err := strconv.Atoi(limitStr)
		if err != nil {
			return nil, fmt.Errorf("invalid limit parameter: must be a number")
		}
		if parsedLimit < MinCommitsLimit || parsedLimit > MaxCommitsLimit {
			return nil, fmt.Errorf("invalid limit parameter: must be between %d and %d", MinCommitsLimit, MaxCommitsLimit)
		}
		limit = parsedLimit
	}

	// Parse and validate skip parameter
	skip := 0
	if skipStr != "" {
		parsedSkip, err := strconv.Atoi(skipStr)
		if err != nil {
			return nil, fmt.Errorf("invalid skip parameter: must be a number")
		}
		if parsedSkip < 0 {
			return nil, fmt.Errorf("invalid skip parameter: must be non-negative")
		}
		skip = parsedSkip
	}

	// Get user ID from header for permission checking
	userID := r.Header.Get("X-USER-ID")
	if userID == "" {
		return nil, fmt.Errorf("missing user authentication")
	}

	return &CommitsRequestParams{
		Owner:  owner,
		Repo:   repo,
		Ref:    ref,
		Limit:  limit,
		Skip:   skip,
		UserID: userID,
	}, nil
}

// validateCommitsRequestAndPermissions performs validation and permission checking for commit requests
func (h *CommitsHandler) validateCommitsRequestAndPermissions(ctx context.Context, params *CommitsRequestParams) (string, error) {
	// Check user permissions (read access for commit operations)
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

// getCommitHistory retrieves commit history using the Git repository interface
func (h *CommitsHandler) getCommitHistory(ctx context.Context, repoPath string, params *CommitsRequestParams) ([]git.Commit, bool, error) {
	// Open repository using the simple cache
	gitRepo, err := git.OpenRepositorySimple(repoPath)
	if err != nil {
		return nil, false, fmt.Errorf("failed to open repository: %w", err)
	}
	defer func() { _ = gitRepo.Close() }()

	// Get commits from Git repository interface
	// We request one extra commit to determine if there are more pages
	requestLimit := params.Limit + 1
	commits, err := gitRepo.GetCommits(ctx, params.Ref, requestLimit, params.Skip)
	if err != nil {
		return nil, false, fmt.Errorf("failed to get commits: %w", err)
	}

	// Determine if there are more commits (pagination)
	hasMore := len(commits) > params.Limit
	if hasMore {
		// Remove the extra commit we requested
		commits = commits[:params.Limit]
	}

	return commits, hasMore, nil
}

// sendCommitsJSONResponse sends a standardized JSON response for commit endpoints
func (h *CommitsHandler) sendCommitsJSONResponse(w http.ResponseWriter, data interface{}, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if err := json.NewEncoder(w).Encode(data); err != nil {
		// Log error but can't change response at this point
		fmt.Printf("Failed to encode JSON response: %v\n", err)
	}
}

// sendCommitsErrorResponse sends a standardized error response for commit endpoints
func (h *CommitsHandler) sendCommitsErrorResponse(w http.ResponseWriter, message string, statusCode int) {
	errorResp := CommitsErrorResponse{
		Error: message,
	}
	h.sendCommitsJSONResponse(w, errorResp, statusCode)
}

// handleCommitsError categorizes errors and sends appropriate HTTP responses for commit endpoints
func (h *CommitsHandler) handleCommitsError(w http.ResponseWriter, err error, operation string) {
	if isCommitsReferenceNotFoundError(err) {
		h.sendCommitsErrorResponse(w, fmt.Sprintf("Reference not found: %v", err), http.StatusNotFound)
	} else if isCommitsRepositoryNotFoundError(err) {
		h.sendCommitsErrorResponse(w, fmt.Sprintf("Repository not found: %v", err), http.StatusNotFound)
	} else if strings.Contains(err.Error(), "permission") {
		h.sendCommitsErrorResponse(w, fmt.Sprintf("Permission check failed: %v", err), http.StatusInternalServerError)
	} else if strings.Contains(err.Error(), "access denied") {
		h.sendCommitsErrorResponse(w, "Access denied to repository", http.StatusForbidden)
	} else if strings.Contains(err.Error(), "authentication") {
		h.sendCommitsErrorResponse(w, err.Error(), http.StatusUnauthorized)
	} else if strings.Contains(err.Error(), "missing") || strings.Contains(err.Error(), "invalid") {
		h.sendCommitsErrorResponse(w, err.Error(), http.StatusBadRequest)
	} else {
		h.sendCommitsErrorResponse(w, fmt.Sprintf("Failed to %s: %v", operation, err), http.StatusInternalServerError)
	}
}

// Error type checking functions for commit operations

// isCommitsReferenceNotFoundError checks if error is a reference not found error
func isCommitsReferenceNotFoundError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	return strings.Contains(errStr, "reference not found") ||
		strings.Contains(errStr, "revision not found") ||
		strings.Contains(errStr, "ref not found") ||
		strings.Contains(errStr, "ReferenceNotFoundError")
}

// isCommitsRepositoryNotFoundError checks if error is a repository not found error
func isCommitsRepositoryNotFoundError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	return strings.Contains(errStr, "repository not found") ||
		strings.Contains(errStr, "no such file or directory") ||
		strings.Contains(errStr, "not a git repository") ||
		strings.Contains(errStr, "RepositoryNotFoundError")
}
