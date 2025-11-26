// Package protocol은 Git 커밋 로그 REST API 엔드포인트를 구현합니다.
// 페이지네이션이 적용된 커밋 데이터를 반환하는 커밋 히스토리 작업을 처리합니다.
package protocol

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/git"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/collabops/git-executor/internal/repository"
	"github.com/gin-gonic/gin"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// CommitsHandler는 Git 커밋 로그 REST API 엔드포인트를 처리합니다.
// 커밋 히스토리 조회 요청을 받아서 페이지네이션과 함께 응답합니다.
type CommitsHandler struct {
	pathConfig       *repository.PathConfig
	permissionClient permissions.PermissionClient
	errorMapper      *api.GitErrorMapper
}

// CommitsRequestParams는 커밋 로그 엔드포인트의 요청 파라미터를 나타냅니다.
type CommitsRequestParams struct {
	Owner string
	Repo  string
	Ref   string
	Limit int
	Skip  int
	Sort  string // 정렬 옵션: "date" (날짜별 그룹화) 또는 빈 문자열 (기본형)
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
func (h *CommitsHandler) HandleCommitsAPI(c *gin.Context) {
	ctx := c.Request.Context()

	// 파라미터 추출 및 검증
	params, err := h.validateAndExtractCommitsParams(c.Request)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "extract_parameters")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// optional parameters
	from_ref := c.Query("from_ref")
	to_ref := c.Query("to_ref")

	// 저장소 경로 조회 및 검증
	repoPath, err := repository.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		apiErr := h.errorMapper.MapError(err, "get_repository_path")
		c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
		return
	}

	// from과 to가 모두 제공된 경우, merge base부터 source branch까지의 커밋 조회
	var commits []git.Commit
	var totalCount int

	if from_ref != "" && to_ref != "" {
		commits, totalCount, err = h.getCommitsBetween(ctx, repoPath, params, from_ref, to_ref)
		if err != nil {
			apiErr := h.errorMapper.MapError(err, "get_commits_between")
			c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
			return
		}
	} else {
		// 커밋 히스토리 조회
		commits, totalCount, err = h.getCommitHistory(ctx, repoPath, params)
		if err != nil {
			apiErr := h.errorMapper.MapError(err, "get_commit_history")
			c.JSON(apiErr.HTTPStatusCode(), gin.H{"success": false, "error": apiErr.Message})
			return
		}
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
			Next:       buildPagination(totalCount, params.Skip/params.Limit+1, params.Limit, c.Request.URL.String()).Next,
			Previous:   buildPagination(totalCount, params.Skip/params.Limit+1, params.Limit, c.Request.URL.String()).Previous,
			TotalPages: buildPagination(totalCount, params.Skip/params.Limit+1, params.Limit, c.Request.URL.String()).TotalPages,
			PageSize:   params.Limit,
		},
	}

	// JSON 응답 전송
	c.JSON(http.StatusOK, response)
}

// HandleMergeBaseAPI는 GET /repos/:owner/:repo/commits/merge-base?from=:from&to=:to 요청을 처리합니다.
// 두 브랜치/커밋의 공통 조상(merge base)을 찾아 반환합니다.
func (h *CommitsHandler) HandleMergeBaseAPI(c *gin.Context) {
	ctx := c.Request.Context()

	// 경로 파라미터 추출
	owner := c.Param("owner")
	repo := c.Param("repo")

	// 쿼리 파라미터 추출
	from_ref := c.Query("from_ref")
	to_ref := c.Query("to_ref")

	// 필수 파라미터 검증
	if owner == "" || repo == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "missing required parameters: owner or repo"})
		return
	}

	if from_ref == "" || to_ref == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "missing required parameters: from and to"})
		return
	}

	// 저장소 경로 조회 및 검증
	repoPath, err := repository.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(404, gin.H{"success": false, "error": "Repository not found"})
		return
	}

	// Merge base 찾기
	mergeBase, err := h.findMergeBase(ctx, repoPath, from_ref, to_ref)
	if err != nil {
		c.JSON(404, gin.H{"success": false, "error": "Merge base not found"})
		return
	}

	// 응답 생성
	response := gin.H{
		"success":    true,
		"message":    "Merge base found successfully",
		"merge_base": mergeBase,
		"from_ref":   from_ref,
		"to_ref":     to_ref,
	}

	c.JSON(http.StatusOK, response)
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

	return &CommitsRequestParams{
		Owner: owner,
		Repo:  repo,
		Ref:   ref,
		Limit: limit,
		Skip:  skip,
		Sort:  sort,
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

// getCommitsBetween는 merge base부터 source branch의 latest commit까지의 커밋 히스토리를 반환합니다.
// from: target branch ref (또는 merge base를 직접 지정)
// to: source branch ref
// merge base를 찾고, merge base부터 source branch까지의 커밋들을 반환합니다.
func (h *CommitsHandler) getCommitsBetween(ctx context.Context, repoPath string, params *CommitsRequestParams, from, to string) ([]git.Commit, int, error) {
	// 컨텍스트 취소 확인
	select {
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	default:
	}

	// go-git 저장소 열기
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to open repository: %w", err)
	}

	// source와 target 커밋 해결
	sourceCommit, err := h.resolveCommit(repo, to)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to resolve source ref %s: %w", to, err)
	}

	targetCommit, err := h.resolveCommit(repo, from)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to resolve target ref %s: %w", from, err)
	}

	// merge base 찾기 (branch 분기점)
	mergeBases, err := sourceCommit.MergeBase(targetCommit)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to find merge base: %w", err)
	}
	if len(mergeBases) == 0 {
		return nil, 0, fmt.Errorf("no common ancestor found between %s and %s", to, from)
	}
	mergeBase := mergeBases[0]

	// merge base와 source commit이 같으면 빈 배열 반환
	if mergeBase.Hash == sourceCommit.Hash {
		return []git.Commit{}, 0, nil
	}

	// merge base부터 source commit까지의 커밋 가져오기
	commitIter, err := repo.Log(&gogit.LogOptions{
		From: sourceCommit.Hash,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get commit log: %w", err)
	}
	defer commitIter.Close()

	var objectCommits []*object.Commit
	mergeBaseHashStr := mergeBase.Hash.String()

	err = commitIter.ForEach(func(commit *object.Commit) error {
		// 컨텍스트 취소 확인
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// merge base에 도달하면 중지
		if commit.Hash.String() == mergeBaseHashStr {
			return fmt.Errorf("stop iteration")
		}

		objectCommits = append(objectCommits, commit)
		return nil
	})

	// merge base를 찾아서 중지했는지 확인
	if err != nil && err.Error() != "stop iteration" {
		return nil, 0, fmt.Errorf("failed to iterate commits: %w", err)
	}

	// 역순으로 정렬 (oldest first)
	for i, j := 0, len(objectCommits)-1; i < j; i, j = i+1, j-1 {
		objectCommits[i], objectCommits[j] = objectCommits[j], objectCommits[i]
	}

	// git.Commit으로 변환
	commits := make([]git.Commit, len(objectCommits))
	for i, c := range objectCommits {
		commits[i] = h.convertObjectCommitToGitCommit(c)
	}

	// 페이지네이션 적용
	totalCount := len(commits)
	start := params.Skip
	end := start + params.Limit
	if end > totalCount {
		end = totalCount
	}
	if start > totalCount {
		start = totalCount
	}

	if start < end {
		commits = commits[start:end]
	} else {
		commits = []git.Commit{}
	}

	return commits, totalCount, nil
}

// resolveCommit은 ref를 커밋 객체로 해석합니다.
func (h *CommitsHandler) resolveCommit(repo *gogit.Repository, ref string) (*object.Commit, error) {
	// 해시로 직접 파싱 시도
	if hash := plumbing.NewHash(ref); !hash.IsZero() && len(ref) >= 4 {
		if commit, err := repo.CommitObject(hash); err == nil {
			return commit, nil
		}
	}

	// 참조로 해석 시도
	reference, err := repo.Reference(plumbing.ReferenceName(ref), true)
	if err == nil {
		return repo.CommitObject(reference.Hash())
	}

	// refs/heads/ prefix로 브랜치 이름 시도
	if !strings.HasPrefix(ref, "refs/") {
		branchRef := "refs/heads/" + ref
		if reference, err := repo.Reference(plumbing.ReferenceName(branchRef), true); err == nil {
			return repo.CommitObject(reference.Hash())
		}

		// refs/tags/ prefix로 태그 이름 시도
		tagRef := "refs/tags/" + ref
		if reference, err := repo.Reference(plumbing.ReferenceName(tagRef), true); err == nil {
			return repo.CommitObject(reference.Hash())
		}
	}

	return nil, fmt.Errorf("unable to resolve reference: %s", ref)
}

// findMergeBase는 두 브랜치/커밋의 공통 조상(merge base)을 찾습니다.
func (h *CommitsHandler) findMergeBase(ctx context.Context, repoPath string, from, to string) (*git.Commit, error) {
	// 컨텍스트 취소 확인
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// go-git 저장소 열기
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	// from과 to 커밋 해결
	fromCommit, err := h.resolveCommit(repo, from)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve ref %s: %w", from, err)
	}

	toCommit, err := h.resolveCommit(repo, to)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve ref %s: %w", to, err)
	}

	// Merge base 찾기
	mergeBases, err := fromCommit.MergeBase(toCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to find merge base: %w", err)
	}

	if len(mergeBases) == 0 {
		return nil, fmt.Errorf("no common ancestor found between %s and %s", from, to)
	}

	// 첫 번째 merge base 반환 (가장 최근 공통 조상)
	mergeBase := mergeBases[0]

	// git.Commit으로 변환
	result := h.convertObjectCommitToGitCommit(mergeBase)
	return &result, nil
}

// convertObjectCommitToGitCommit는 go-git의 object.Commit을 내부 git.Commit으로 변환합니다.
func (h *CommitsHandler) convertObjectCommitToGitCommit(c *object.Commit) git.Commit {
	var parentHashes []string
	for _, parent := range c.ParentHashes {
		parentHashes = append(parentHashes, parent.String())
	}

	// 커밋 메시지 정리
	message := strings.TrimSpace(c.Message)
	message = strings.ReplaceAll(message, "\n", " ")
	message = strings.ReplaceAll(message, "\r", " ")

	return git.Commit{
		Sha:     c.Hash.String(),
		Message: message,
		Author: git.CommitAuthor{
			Name:  c.Author.Name,
			Email: c.Author.Email,
			Date:  c.Author.When,
		},
		Committer: git.CommitAuthor{
			Name:  c.Committer.Name,
			Email: c.Committer.Email,
			Date:  c.Committer.When,
		},
		ParentHashes: parentHashes,
		TreeHash:     c.TreeHash.String(),
	}
}
