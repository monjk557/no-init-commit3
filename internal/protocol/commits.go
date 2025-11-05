// Package protocol은 Git 커밋 로그 REST API 엔드포인트를 구현합니다.
// 페이지네이션이 적용된 커밋 데이터를 반환하는 커밋 히스토리 작업을 처리합니다.
package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
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

// CommitsRequestParams는 커밋 로그 엔드포인트의 요청 파라미터를 나타냅니다.
type CommitsRequestParams struct {
	Owner  string
	Repo   string
	Ref    string
	Limit  int
	Skip   int
	Sort   string // 정렬 옵션: "date" (날짜별 그룹화) 또는 빈 문자열 (기본형)
	UserID string
}

// CommitsResponse는 커밋 로그 엔드포인트의 응답 구조를 나타냅니다.
// sort=date일 때는 날짜별로 그룹화된 커밋 목록, 그렇지 않으면 기본 커밋 목록을 반환합니다.
// CommitsByDate 타입은 diff.go에서 정의된 타입을 재사용합니다.
type CommitsResponse struct {
	Success    bool           `json:"success"`
	Message    string         `json:"message"`
	Results    interface{}    `json:"results"` // sort=date일 때는 []CommitsByDate, 그렇지 않으면 []git.Commit
	Pagination PaginationInfo `json:"pagination"`
}

// PaginationInfo는 커밋 응답의 페이지네이션 정보를 나타냅니다.
// Python PaginatedResponse와 호환되는 필드들을 포함합니다.
type PaginationInfo struct {
	Count       int     `json:"count"`        // 전체 항목 수
	Next        *string `json:"next"`         // 다음 페이지 URL
	Previous    *string `json:"previous"`     // 이전 페이지 URL
	CurrentPage int     `json:"current_page"` // 현재 페이지 번호
	TotalPages  int     `json:"total_pages"`  // 전체 페이지 수
	PageSize    int     `json:"page_size"`    // 페이지당 항목 수
}

// CommitsErrorResponse는 커밋 엔드포인트의 표준화된 에러 응답을 나타냅니다.
type CommitsErrorResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	Code    string `json:"code,omitempty"`
	Details string `json:"details,omitempty"`
}

// 기본 페이지네이션 값들
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

	// 경로 설정 생성
	pathConfig, err := repository.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	return &CommitsHandler{
		pathConfig:       pathConfig,
		permissionClient: permClient,
	}, nil
}

// HandleCommitsAPI는 GET /repos/:owner/:repo/commits?ref=:ref&limit=:limit&skip=:skip 요청을 처리합니다.
func (h *CommitsHandler) HandleCommitsAPI(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 파라미터 추출 및 검증
	params, err := h.validateAndExtractCommitsParams(r)
	if err != nil {
		h.handleCommitsError(w, err, "extract parameters")
		return
	}

	// 저장소 경로 조회 및 검증
	repoPath, err := repository.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		h.handleCommitsError(w, err, "get repository path")
		return
	}

	// 커밋 히스토리 조회
	commits, totalCount, err := h.getCommitHistory(ctx, repoPath, params)
	if err != nil {
		h.handleCommitsError(w, err, "get commit history")
		return
	}

	// sort=date 파라미터에 따라 응답 형식 결정
	var results interface{}
	if params.Sort == "date" {
		// 날짜별로 커밋 그룹화
		results = h.groupCommitsByDate(commits)
	} else {
		// 기본형: 커밋 목록을 그대로 반환
		results = commits
	}

	// 페이지네이션 정보와 함께 응답 생성
	response := CommitsResponse{
		Success: true,
		Results: results,
		Message: "Commits fetched successfully",
		Pagination: PaginationInfo{
			Count:      totalCount,
			Next:       buildPagination(totalCount, params.Skip/params.Limit+1, params.Limit, r.URL.String()).Next,
			Previous:   buildPagination(totalCount, params.Skip/params.Limit+1, params.Limit, r.URL.String()).Previous,
			TotalPages: buildPagination(totalCount, params.Skip/params.Limit+1, params.Limit, r.URL.String()).TotalPages,
			PageSize:   params.Limit,
		},
	}

	// JSON 응답 전송
	h.sendCommitsJSONResponse(w, response, http.StatusOK)
}

// validateAndExtractCommitsParams는 커밋 요청에서 파라미터를 추출하고 검증합니다.
func (h *CommitsHandler) validateAndExtractCommitsParams(r *http.Request) (*CommitsRequestParams, error) {
	// 경로 파라미터 추출
	owner := URLParam(r, "owner")
	repo := URLParam(r, "repo")

	// 쿼리 파라미터 추출
	ref := r.URL.Query().Get("ref")
	limitStr := r.URL.Query().Get("limit")
	skipStr := r.URL.Query().Get("skip")
	sort := r.URL.Query().Get("sort")

	// 필수 파라미터 검증
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("missing required parameters: owner or repo")
	}

	// ref가 제공되지 않은 경우 기본값으로 "HEAD" 설정
	if ref == "" {
		ref = "HEAD"
	}

	// 파라미터 형식 기본 검증
	if !isValidRepoIdentifier(owner) {
		return nil, fmt.Errorf("invalid owner format")
	}

	if !isValidRepoIdentifier(repo) {
		return nil, fmt.Errorf("invalid repo format")
	}

	if !isValidGitRef(ref) {
		return nil, fmt.Errorf("invalid ref format")
	}

	// limit 파라미터 파싱 및 검증
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

	// skip 파라미터 파싱 및 검증
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

	// TODO: 권한 서비스가 사용 가능할 때 사용자 인증 다시 활성화
	// userID := r.Header.Get("X-USER-ID")
	// if userID == "" {
	// 	return nil, fmt.Errorf("missing user authentication")
	// }
	userID := "dev-user" // 개발용 플레이스홀더

	return &CommitsRequestParams{
		Owner:  owner,
		Repo:   repo,
		Ref:    ref,
		Limit:  limit,
		Skip:   skip,
		Sort:   sort,
		UserID: userID,
	}, nil
}

// getCommitHistory는 Git 저장소 인터페이스를 사용하여 커밋 히스토리를 조회합니다.
func (h *CommitsHandler) getCommitHistory(ctx context.Context, repoPath string, params *CommitsRequestParams) ([]git.Commit, int, error) {
	// 간단한 캐시를 사용하여 저장소 열기
	gitRepo, err := git.OpenRepositorySimple(repoPath)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to open repository: %w", err)
	}
	defer func() { _ = gitRepo.Close() }()

	// Git 저장소 인터페이스에서 커밋 조회
	// 더 많은 페이지가 있는지 확인하기 위해 하나 더 요청
	requestLimit := params.Limit + 1
	commits, err := gitRepo.GetCommits(ctx, params.Ref, requestLimit, params.Skip)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get commits: %w", err)
	}

	// 더 많은 커밋이 있는지 확인 (페이지네이션)
	totalCount := len(commits)

	return commits, totalCount, nil
}

// sendCommitsJSONResponse는 커밋 엔드포인트의 표준화된 JSON 응답을 전송합니다.
func (h *CommitsHandler) sendCommitsJSONResponse(w http.ResponseWriter, data interface{}, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if err := json.NewEncoder(w).Encode(data); err != nil {
		// 에러 로깅 (이 시점에서는 응답을 변경할 수 없음)
		fmt.Printf("Failed to encode JSON response: %v\n", err)
	}
}

// sendCommitsErrorResponse는 커밋 엔드포인트의 표준화된 에러 응답을 전송합니다.
func (h *CommitsHandler) sendCommitsErrorResponse(w http.ResponseWriter, success bool, message string, statusCode int) {
	errorResp := CommitsErrorResponse{
		Success: success,
		Error:   message,
	}
	h.sendCommitsJSONResponse(w, errorResp, statusCode)
}

// handleCommitsError는 에러를 분류하고 커밋 엔드포인트에 적절한 HTTP 응답을 전송합니다.
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

// 커밋 작업을 위한 에러 타입 확인 함수들

// isCommitsReferenceNotFoundError는 에러가 참조를 찾을 수 없는 에러인지 확인합니다.
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

// isCommitsRepositoryNotFoundError는 에러가 저장소를 찾을 수 없는 에러인지 확인합니다.
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

// convertGitCommitToMetadata는 git.Commit을 CommitMetadata로 변환합니다.
func (h *CommitsHandler) convertGitCommitToMetadata(commit git.Commit) CommitMetadata {
	return CommitMetadata{
		SHA:     commit.Sha,
		Author:  commit.Author.Name,
		Email:   commit.Author.Email,
		Date:    commit.Author.Date.Format("2006-01-02 15:04:05"),
		Message: commit.Message,
		Parents: commit.ParentHashes,
	}
}

// groupCommitsByDate는 커밋을 날짜별로 그룹화합니다 (YYYY-MM-DD 형식).
// 일관성을 위해 diff.go의 CommitsByDate 타입을 재사용합니다.
// 커밋은 작성자 날짜를 기준으로 그룹화되며, 날짜 내림차순으로 정렬됩니다.
func (h *CommitsHandler) groupCommitsByDate(commits []git.Commit) []CommitsByDate {
	if len(commits) == 0 {
		return []CommitsByDate{}
	}

	// 날짜 문자열별로 커밋을 저장할 맵
	dateMap := make(map[string][]CommitMetadata)

	// 날짜별로 커밋 그룹화
	for _, commit := range commits {
		// 그룹화를 위해 작성자 날짜 사용
		dateStr := commit.Author.Date.Format("2006-01-02")
		commitMeta := h.convertGitCommitToMetadata(commit)
		dateMap[dateStr] = append(dateMap[dateStr], commitMeta)
	}

	// 맵을 정렬된 슬라이스로 변환
	var result []CommitsByDate
	for dateStr, commits := range dateMap {
		result = append(result, CommitsByDate{
			Date:    dateStr,
			Commits: commits,
		})
	}

	// 날짜 내림차순으로 정렬 (가장 최근 날짜가 먼저)
	// 날짜가 YYYY-MM-DD 형식이므로 효율성을 위해 문자열 비교 사용
	sort.Slice(result, func(i, j int) bool {
		return result[i].Date > result[j].Date
	})

	return result
}
