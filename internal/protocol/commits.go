// Package protocol은 Git 커밋 로그 REST API 엔드포인트를 구현합니다.
// 페이지네이션이 적용된 커밋 데이터를 반환하는 커밋 히스토리 작업을 처리합니다.
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

// CommitsHandler는 Git 커밋 로그 REST API 엔드포인트를 처리합니다.
// 커밋 히스토리 조회 요청을 받아서 페이지네이션과 함께 응답합니다.
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

// CommitsResponse는 커밋 로그 엔드포인트의 응답 구조를 나타냅니다.
// 커밋 목록과 함께 페이지네이션 정보를 포함합니다.
type CommitsResponse struct {
	Success    bool           `json:"success"`
	Message    string         `json:"message"`
	Results    []git.Commit   `json:"results"`
	Pagination PaginationInfo `json:"pagination"`
}

// PaginationInfo는 커밋 응답의 페이지네이션 정보를 나타냅니다.
// Python PaginatedResponse와 호환되는 필드들을 포함합니다.
type PaginationInfo struct {
	// Python PaginatedResponse compatible fields
	Count       int     `json:"count"`        // Total number of items
	Next        *string `json:"next"`         // Next page URL
	Previous    *string `json:"previous"`     // Previous page URL
	CurrentPage int     `json:"current_page"` // Current page number
	TotalPages  int     `json:"total_pages"`  // Total number of pages
	PageSize    int     `json:"page_size"`    // Number of items per page
}

// CommitsErrorResponse represents a standardized error response for commit endpoints
type CommitsErrorResponse struct {
	Success bool   `json:"success"`
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

func buildPagination(totalCount, currentPage, pageSize int, baseURL string) PaginationInfo {
	totalPages := (totalCount + pageSize - 1) / pageSize

	var next, previous *string

	if currentPage < totalPages {
		nextURL := fmt.Sprintf("%s?page=%d&page_size=%d", baseURL, currentPage+1, pageSize)
		next = &nextURL
	}

	if currentPage > 1 {
		prevURL := fmt.Sprintf("%s?page=%d&page_size=%d", baseURL, currentPage-1, pageSize)
		previous = &prevURL
	}

	return PaginationInfo{
		Count:      totalCount,
		Next:       next,
		Previous:   previous,
		TotalPages: totalPages,
		PageSize:   pageSize,
	}
}

// NewCommitsHandler는 새로운 commits 핸들러를 생성합니다.
// 설정과 권한 클라이언트를 사용하여 초기화합니다.
func NewCommitsHandler(cfg *config.Config, permClient permissions.PermissionClient) (*CommitsHandler, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
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

	// Get repository path and validate
	repoPath, err := repository.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		h.handleCommitsError(w, err, "get repository path")
		return
	}

	// Get commit history
	commits, totalCount, err := h.getCommitHistory(ctx, repoPath, params)
	if err != nil {
		h.handleCommitsError(w, err, "get commit history")
		return
	}

	// Create response with pagination info
	response := CommitsResponse{
		Success: true,
		Results: commits,
		Message: "Commits fetched successfully",
		Pagination: PaginationInfo{
			Count:      totalCount,
			Next:       buildPagination(totalCount, params.Skip/params.Limit+1, params.Limit, r.URL.String()).Next,
			Previous:   buildPagination(totalCount, params.Skip/params.Limit+1, params.Limit, r.URL.String()).Previous,
			TotalPages: buildPagination(totalCount, params.Skip/params.Limit+1, params.Limit, r.URL.String()).TotalPages,
			PageSize:   params.Limit,
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

	// TODO: Re-enable user authentication when permission service is available
	// userID := r.Header.Get("X-USER-ID")
	// if userID == "" {
	// 	return nil, fmt.Errorf("missing user authentication")
	// }
	userID := "dev-user" // Development placeholder

	return &CommitsRequestParams{
		Owner:  owner,
		Repo:   repo,
		Ref:    ref,
		Limit:  limit,
		Skip:   skip,
		UserID: userID,
	}, nil
}

// getCommitHistory retrieves commit history using the Git repository interface
func (h *CommitsHandler) getCommitHistory(ctx context.Context, repoPath string, params *CommitsRequestParams) ([]git.Commit, int, error) {
	// Open repository using the simple cache
	gitRepo, err := git.OpenRepositorySimple(repoPath)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to open repository: %w", err)
	}
	defer func() { _ = gitRepo.Close() }()

	// Get commits from Git repository interface
	// We request one extra commit to determine if there are more pages
	requestLimit := params.Limit + 1
	commits, err := gitRepo.GetCommits(ctx, params.Ref, requestLimit, params.Skip)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get commits: %w", err)
	}

	// Determine if there are more commits (pagination)
	totalCount := len(commits)

	return commits, totalCount, nil
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
func (h *CommitsHandler) sendCommitsErrorResponse(w http.ResponseWriter, success bool, message string, statusCode int) {
	errorResp := CommitsErrorResponse{
		Success: success,
		Error:   message,
	}
	h.sendCommitsJSONResponse(w, errorResp, statusCode)
}

// handleCommitsError categorizes errors and sends appropriate HTTP responses for commit endpoints
func (h *CommitsHandler) handleCommitsError(w http.ResponseWriter, err error, operation string) {
	if isCommitsReferenceNotFoundError(err) {
		h.sendCommitsErrorResponse(w, true, fmt.Sprintf("Reference not found: %v", err), http.StatusNotFound)
	} else if isCommitsRepositoryNotFoundError(err) {
		h.sendCommitsErrorResponse(w, true, fmt.Sprintf("Repository not found: %v", err), http.StatusNotFound)
	} else if strings.Contains(err.Error(), "permission") {
		h.sendCommitsErrorResponse(w, true, fmt.Sprintf("Permission check failed: %v", err), http.StatusInternalServerError)
	} else if strings.Contains(err.Error(), "access denied") {
		h.sendCommitsErrorResponse(w, true, "Access denied to repository", http.StatusForbidden)
	} else if strings.Contains(err.Error(), "authentication") {
		h.sendCommitsErrorResponse(w, true, err.Error(), http.StatusUnauthorized)
	} else if strings.Contains(err.Error(), "missing") || strings.Contains(err.Error(), "invalid") {
		h.sendCommitsErrorResponse(w, true, err.Error(), http.StatusBadRequest)
	} else {
		h.sendCommitsErrorResponse(w, true, fmt.Sprintf("Failed to %s: %v", operation, err), http.StatusInternalServerError)
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
