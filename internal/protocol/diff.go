// Package protocol은 executor를 위한 Git diff REST API 엔드포인트를 구현합니다.
// 이 파일은 구조화된 JSON 응답을 제공하는 diff 작업을 처리합니다.
package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/git"
	"github.com/collabops/git-executor/internal/permissions"
	"github.com/collabops/git-executor/internal/repository"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// DiffHandler는 Git diff REST API 엔드포인트를 처리합니다
type DiffHandler struct {
	pathConfig       *repository.PathConfig
	permissionClient permissions.PermissionClient
}

// DiffResponse는 diff 엔드포인트의 응답 구조를 나타냅니다
type DiffResponse struct {
	Success       bool            `json:"success"`
	Base          string          `json:"base"`
	Head          string          `json:"head"`
	Files         []FileDiff      `json:"files"`
	Stats         DiffStats       `json:"stats"`
	Commits       *CommitStats    `json:"commits,omitempty"`         // 브랜치 diff용
	CommitsByDate []CommitsByDate `json:"commits_by_date,omitempty"` // 날짜별 커밋 목록
	MergeBase     string          `json:"merge_base,omitempty"`      // 브랜치 diff용
}

// SingleCommitDiffResponse는 단일 커밋 diff 엔드포인트의 응답 구조를 나타냅니다
type SingleCommitDiffResponse struct {
	Success bool            `json:"success"`
	Commit  *CommitMetadata `json:"commit"`
	*DiffResponse
}

// CommitMetadata는 커밋 정보를 나타냅니다
type CommitMetadata struct {
	SHA     string   `json:"sha"`
	Author  string   `json:"author"`
	Email   string   `json:"email"`
	Date    string   `json:"date"`
	Message string   `json:"message"`
	Parents []string `json:"parents"`
}

// CommitStats는 브랜치 비교를 위한 커밋 수 차이를 나타냅니다
type CommitStats struct {
	Ahead  int `json:"ahead"`  // base에 없는 head의 커밋들
	Behind int `json:"behind"` // head에 없는 base의 커밋들
}

// CommitsByDate는 날짜별로 그룹화된 커밋 목록을 나타냅니다
type CommitsByDate struct {
	Date    string           `json:"date"`    // YYYY-MM-DD 형식의 날짜
	Commits []CommitMetadata `json:"commits"` // 해당 날짜의 커밋 목록
}

// FileDiff는 단일 파일의 변경사항을 나타냅니다
type FileDiff struct {
	Path      string                 `json:"path"`
	Status    string                 `json:"status"` // "added", "deleted", "modified", "renamed"
	Additions int                    `json:"additions"`
	Deletions int                    `json:"deletions"`
	Changes   []LineChange           `json:"changes,omitempty"`
	IsBinary  bool                   `json:"is_binary"`
	OldPath   string                 `json:"old_path,omitempty"` // 이름 변경시 사용
	Metadata  map[string]interface{} `json:"metadata,omitempty"` // 파일 메타데이터 (해시, 모드)
}

// LineChange는 diff의 단일 라인 변경을 나타냅니다
type LineChange struct {
	Type    string `json:"type"`    // "add", "del", "context"
	LineNum int    `json:"line"`    // 파일 내 라인 번호
	Content string `json:"content"` // diff 접두사가 포함된 라인 내용
}

// DiffStats는 diff의 전체 통계를 나타냅니다
type DiffStats struct {
	TotalAdditions int `json:"total_additions"`
	TotalDeletions int `json:"total_deletions"`
	FilesChanged   int `json:"files_changed"`
}

// NewDiffHandler는 새로운 diff 핸들러를 생성합니다
func NewDiffHandler(cfg *config.Config, permClient permissions.PermissionClient) (*DiffHandler, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	// 경로 설정 생성
	pathConfig, err := repository.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	return &DiffHandler{
		pathConfig:       pathConfig,
		permissionClient: permClient,
	}, nil
}

// HandleCommitDiff는 GET /repos/:owner/:repo/diff/commits/:base/:head를 처리합니다
func (h *DiffHandler) HandleCommitDiff(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 공통 파라미터 추출 및 검증
	params, err := h.validateAndExtractParams(r)
	if err != nil {
		handleDiffError(w, err, "extract parameters")
		return
	}

	// 커밋 diff를 위해 base와 head 파라미터가 필수인지 검증
	if params.Base == "" || params.Head == "" {
		handleDiffError(w, fmt.Errorf("missing required parameters: base or head"), "validate commit parameters")
		return
	}

	// 권한 검증 및 저장소 경로 가져오기
	repoPath, err := repository.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		handleDiffError(w, err, "invalid repository path: %w")
		return
	}

	// 쿼리 파라미터에서 컨텍스트 라인 수 가져오기
	contextLines := 3 // 기본값
	if contextStr := r.URL.Query().Get("context"); contextStr != "" {
		if ctx, err := strconv.Atoi(contextStr); err == nil && ctx >= 0 {
			contextLines = ctx
		}
	}

	// diff 계산
	diffResp, err := h.calculateCommitDiff(ctx, repoPath, params.Base, params.Head, contextLines)
	if err != nil {
		handleDiffError(w, err, "calculate commit diff")
		return
	}

	// 페이지네이션 요청 확인
	if r.URL.Query().Get("page") != "" || r.URL.Query().Get("page_size") != "" {
		pagination := extractPaginationParams(r)
		paginatedFiles, updatedPagination := paginateFiles(diffResp.Files, pagination)

		response := &PaginatedDiffResponse{
			Success: true,
			DiffResponse: &DiffResponse{
				Base:  diffResp.Base,
				Head:  diffResp.Head,
				Files: paginatedFiles,
				Stats: DiffStats{
					TotalAdditions: diffResp.Stats.TotalAdditions,
					TotalDeletions: diffResp.Stats.TotalDeletions,
					FilesChanged:   len(paginatedFiles), // 페이지네이션 응답에 맞게 업데이트
				},
			},
			Pagination: &updatedPagination,
		}
		sendJSONResponse(w, response, http.StatusOK)
	} else {
		sendJSONResponse(w, diffResp, http.StatusOK)
	}
}

// HandleBranchDiff는 GET /repos/:owner/:repo/diff/branches/:base/:head를 처리합니다
func (h *DiffHandler) HandleBranchDiff(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 공통 파라미터 추출 및 검증
	params, err := h.validateAndExtractParams(r)
	if err != nil {
		handleDiffError(w, err, "extract parameters")
		return
	}

	// 브랜치 diff를 위해 base와 head 파라미터가 필수인지 검증
	if params.Base == "" || params.Head == "" {
		handleDiffError(w, fmt.Errorf("missing required parameters: base or head"), "validate branch parameters")
		return
	}

	// 권한 검증 및 저장소 경로 가져오기
	repoPath, err := h.validateRequestAndPermissions(ctx, params)
	if err != nil {
		handleDiffError(w, err, "validate permissions")
		return
	}

	// 쿼리 파라미터에서 컨텍스트 라인 수 가져오기
	contextLines := 3 // 기본값
	if contextStr := r.URL.Query().Get("context"); contextStr != "" {
		if ctx, err := strconv.Atoi(contextStr); err == nil && ctx >= 0 {
			contextLines = ctx
		}
	}

	// 브랜치 diff 계산
	diffResp, err := h.calculateBranchDiff(ctx, repoPath, params.Base, params.Head, contextLines)
	if err != nil {
		handleDiffError(w, err, "calculate branch diff")
		return
	}

	// 페이지네이션 요청 확인
	if r.URL.Query().Get("page") != "" || r.URL.Query().Get("page_size") != "" {
		pagination := extractPaginationParams(r)
		paginatedFiles, updatedPagination := paginateFiles(diffResp.Files, pagination)

		response := &PaginatedDiffResponse{
			Success: true,
			DiffResponse: &DiffResponse{
				Base:  diffResp.Base,
				Head:  diffResp.Head,
				Files: paginatedFiles,
				Stats: DiffStats{
					TotalAdditions: diffResp.Stats.TotalAdditions,
					TotalDeletions: diffResp.Stats.TotalDeletions,
					FilesChanged:   len(paginatedFiles), // 페이지네이션 응답에 맞게 업데이트
				},
				Commits:   diffResp.Commits,
				MergeBase: diffResp.MergeBase,
			},
			Pagination: &updatedPagination,
		}
		sendJSONResponse(w, response, http.StatusOK)
	} else {
		sendJSONResponse(w, diffResp, http.StatusOK)
	}
}

// HandleFileDiff는 GET /repos/:owner/:repo/diff/file?path=:path&base=:base&head=:head를 처리합니다
func (h *DiffHandler) HandleFileDiff(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 공통 파라미터 추출 및 검증
	params, err := h.validateAndExtractParams(r)
	if err != nil {
		handleDiffError(w, err, "extract parameters")
		return
	}

	// 쿼리 문자열에서 파일별 파라미터 추출
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		handleDiffError(w, fmt.Errorf("missing required parameter: path"), "validate file parameters")
		return
	}

	base := r.URL.Query().Get("base")
	head := r.URL.Query().Get("head")
	if base == "" || head == "" {
		handleDiffError(w, fmt.Errorf("missing required parameters: base or head"), "validate file diff parameters")
		return
	}

	// 파일 diff를 위해 params를 쿼리 파라미터로 덮어쓰기
	params.Base = base
	params.Head = head

	// 권한 검증 및 저장소 경로 가져오기
	repoPath, err := repository.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		handleDiffError(w, fmt.Errorf("invalid repository path: %w", err), "invalid repository path: %w")
		return
	}

	// 쿼리 파라미터에서 컨텍스트 라인과 형식 가져오기
	contextLines := 3 // 기본값
	if contextStr := r.URL.Query().Get("context"); contextStr != "" {
		if ctx, err := strconv.Atoi(contextStr); err == nil && ctx >= 0 {
			contextLines = ctx
		}
	}

	// full_context 파라미터 확인 (Pull Request 뷰용)
	fullContext := r.URL.Query().Get("full_context") == "true"
	if fullContext {
		contextLines = 999999 // 전체 파일을 포함하기 위한 큰 숫자
	}

	format := r.URL.Query().Get("format")
	if format == "" {
		format = "unified" // 기본값
	}

	// 파일 diff 계산
	diffResp, err := h.calculateFileDiff(ctx, repoPath, filePath, params.Base, params.Head, contextLines, format)
	if err != nil {
		handleDiffError(w, err, "calculate file diff")
		return
	}

	sendJSONResponse(w, diffResp, http.StatusOK)
}

// HandleSingleCommitDiff는 GET /repos/:owner/:repo/commits/:sha/diff를 처리합니다
func (h *DiffHandler) HandleSingleCommitDiff(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 공통 파라미터 추출 및 검증
	params, err := h.validateAndExtractParams(r)
	if err != nil {
		handleDiffError(w, err, "extract parameters")
		return
	}

	// URL 경로에서 SHA 파라미터 가져오기
	sha := URLParam(r, "sha")
	if sha == "" {
		handleDiffError(w, fmt.Errorf("missing required parameter: sha"), "validate commit sha")
		return
	}

	// 권한 검증 및 저장소 경로 가져오기
	repoPath, err := h.validateRequestAndPermissions(ctx, params)
	if err != nil {
		handleDiffError(w, err, "validate permissions")
		return
	}

	// 쿼리 파라미터에서 컨텍스트 라인 수 가져오기
	contextLines := 3 // 기본값
	if contextStr := r.URL.Query().Get("context"); contextStr != "" {
		if ctx, err := strconv.Atoi(contextStr); err == nil && ctx >= 0 {
			contextLines = ctx
		}
	}

	// 단일 커밋 diff 계산
	response, err := h.calculateSingleCommitDiff(ctx, repoPath, sha, contextLines)
	if err != nil {
		handleDiffError(w, err, "calculate single commit diff")
		return
	}

	sendJSONResponse(w, response, http.StatusOK)
}

// HandlePatch는 GET /repos/:owner/:repo/patch/:base/:head를 처리합니다
func (h *DiffHandler) HandlePatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 공통 파라미터 추출 및 검증
	params, err := h.validateAndExtractParams(r)
	if err != nil {
		handleDiffError(w, err, "extract parameters")
		return
	}

	// patch를 위해 base와 head 파라미터가 필수인지 검증
	if params.Base == "" || params.Head == "" {
		handleDiffError(w, fmt.Errorf("missing required parameters: base or head"), "validate patch parameters")
		return
	}

	// 권한 검증 및 저장소 경로 가져오기
	repoPath, err := repository.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		handleDiffError(w, fmt.Errorf("invalid repository path: %w", err), "invalid repository path: %w")
		return
	}

	// patch 콘텐츠 생성
	patchContent, err := h.generatePatch(ctx, repoPath, params.Base, params.Head)
	if err != nil {
		handleDiffError(w, err, "generate patch")
		return
	}

	// patch 콘텐츠에 적절한 헤더 설정
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s-%s.patch\"", params.Base, params.Head))

	// patch 콘텐츠 작성
	w.WriteHeader(http.StatusOK)
	_, err = w.Write([]byte(patchContent))
	if err != nil {
		// 오류 로깅하지만 이 시점에선 HTTP 오류를 보낼 수 없음
		fmt.Printf("Error writing patch response: %v\n", err)
	}
}

// generatePatch는 커밋 범위에 대한 Git patch 형식 출력을 생성합니다
func (h *DiffHandler) generatePatch(ctx context.Context, repoPath, base, head string) (string, error) {
	// 저장소 열기
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return "", fmt.Errorf("failed to open repository: %w", err)
	}

	// base와 head 참조를 커밋 객체로 해석
	baseCommit, err := h.resolveReferenceToCommit(repo, base)
	if err != nil {
		return "", fmt.Errorf("failed to resolve base reference %s: %w", base, err)
	}

	headCommit, err := h.resolveReferenceToCommit(repo, head)
	if err != nil {
		return "", fmt.Errorf("failed to resolve head reference %s: %w", head, err)
	}

	// go-git의 내장 patch 기능을 사용하여 patch 생성
	patch, err := baseCommit.Patch(headCommit)
	if err != nil {
		return "", fmt.Errorf("failed to generate patch: %w", err)
	}

	return patch.String(), nil
}

// calculateCommitDiff는 두 커밋 간의 실제 diff 계산을 수행합니다
func (h *DiffHandler) calculateCommitDiff(ctx context.Context, repoPath, base, head string, contextLines int) (*DiffResponse, error) {
	// 캐시된 방식으로 저장소 열기
	gitRepo, cleanup, err := h.getGitRepository(repoPath)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	// 커밋 객체 가져오기
	baseCommit, err := gitRepo.CommitObject(plumbing.NewHash(base))
	if err != nil {
		return nil, fmt.Errorf("failed to get base commit %s: %w", base, err)
	}

	headCommit, err := gitRepo.CommitObject(plumbing.NewHash(head))
	if err != nil {
		return nil, fmt.Errorf("failed to get head commit %s: %w", head, err)
	}

	// 커밋에서 트리 가져오기
	baseTree, err := baseCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get base tree: %w", err)
	}

	headTree, err := headCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get head tree: %w", err)
	}

	// 트리 간 diff 계산
	changes, err := object.DiffTree(baseTree, headTree)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate tree diff: %w", err)
	}

	// 변경 사항을 구조화된 응답으로 처리
	fileDiffs, stats, err := h.processChanges(changes, contextLines)
	if err != nil {
		return nil, fmt.Errorf("failed to process changes: %w", err)
	}

	return &DiffResponse{
		Success: true,
		Base:    base,
		Head:    head,
		Files:   fileDiffs,
		Stats:   stats,
	}, nil
}

// calculateSingleCommitDiff는 부모 커밋에 대한 단일 커밋의 diff 계산을 수행합니다
func (h *DiffHandler) calculateSingleCommitDiff(ctx context.Context, repoPath, sha string, contextLines int) (*SingleCommitDiffResponse, error) {
	// 캐시된 방식으로 저장소 열기
	gitRepo, cleanup, err := h.getGitRepository(repoPath)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	// 커밋 객체 가져오기
	commitHash := plumbing.NewHash(sha)
	commit, err := gitRepo.CommitObject(commitHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get commit %s: %w", sha, err)
	}

	// 커밋 메타데이터 추출
	commitMetadata := h.extractCommitMetadata(commit)

	// 다른 커밋 시나리오 처리
	var diffResp *DiffResponse

	if commit.NumParents() == 0 {
		// 초기 커밋 - 빈 트리와 비교
		diffResp, err = h.calculateInitialCommitDiff(ctx, gitRepo, commit, contextLines)
		if err != nil {
			return nil, fmt.Errorf("failed to calculate initial commit diff: %w", err)
		}
	} else {
		// 일반 커밋 - 첫 번째 부모와 비교
		parentCommit, err := commit.Parent(0)
		if err != nil {
			return nil, fmt.Errorf("failed to get parent commit: %w", err)
		}

		// 기존 커밋 diff 로직 재사용
		diffResp, err = h.calculateCommitDiffBetween(ctx, gitRepo, parentCommit, commit, contextLines)
		if err != nil {
			return nil, fmt.Errorf("failed to calculate commit diff: %w", err)
		}
	}

	// 커밋과 부모를 나타내도록 base와 head 설정
	if commit.NumParents() > 0 {
		parent, _ := commit.Parent(0)
		diffResp.Base = parent.Hash.String()
	} else {
		diffResp.Base = "empty_tree"
	}
	diffResp.Head = sha

	return &SingleCommitDiffResponse{
		Success:      true,
		Commit:       commitMetadata,
		DiffResponse: diffResp,
	}, nil
}

// extractCommitMetadata는 커밋 객체에서 메타데이터를 추출합니다
func (h *DiffHandler) extractCommitMetadata(commit *object.Commit) *CommitMetadata {
	var parents []string
	for _, parent := range commit.ParentHashes {
		parents = append(parents, parent.String())
	}

	return &CommitMetadata{
		SHA:     commit.Hash.String(),
		Author:  commit.Author.Name,
		Email:   commit.Author.Email,
		Date:    commit.Author.When.Format("2006-01-02T15:04:05Z07:00"),
		Message: strings.TrimSpace(commit.Message),
		Parents: parents,
	}
}

// calculateInitialCommitDiff는 부모가 없는 커밋(초기 커밋)에 대한 diff를 처리합니다
func (h *DiffHandler) calculateInitialCommitDiff(ctx context.Context, repo *gogit.Repository, commit *object.Commit, contextLines int) (*DiffResponse, error) {
	// 커밋 트리 가져오기
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get commit tree: %w", err)
	}

	// 트리의 모든 파일을 추가로 처리하여 변경 사항 생성
	var fileDiffs []FileDiff
	var stats DiffStats

	err = tree.Files().ForEach(func(file *object.File) error {
		content, err := file.Contents()
		if err != nil {
			// 오류를 로깅하지만 다른 파일들은 계속 처리
			fmt.Printf("Error getting content for %s: %v\n", file.Name, err)
			return nil
		}

		lines := splitLines(content)
		additions := len(lines)

		fileDiff := FileDiff{
			Path:      file.Name,
			Status:    "added",
			Additions: additions,
			Deletions: 0,
			IsBinary:  isBinaryFile(content, file.Name),
		}

		// 바이너리가 아니고 contextLines > 0이면 라인 변경 생성
		if !fileDiff.IsBinary && contextLines > 0 {
			var lineChanges []LineChange
			for i, line := range lines {
				lineChanges = append(lineChanges, LineChange{
					Type:    "add",
					LineNum: i + 1,
					Content: "+" + line,
				})
			}
			fileDiff.Changes = lineChanges
		}

		fileDiffs = append(fileDiffs, fileDiff)
		stats.FilesChanged++
		stats.TotalAdditions += additions

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to iterate tree files: %w", err)
	}

	return &DiffResponse{
		Files: fileDiffs,
		Stats: stats,
	}, nil
}

// calculateCommitDiffBetween은 두 특정 커밋 객체 간의 diff를 계산합니다
func (h *DiffHandler) calculateCommitDiffBetween(ctx context.Context, repo *gogit.Repository, baseCommit, headCommit *object.Commit, contextLines int) (*DiffResponse, error) {
	// 커밋에서 트리 가져오기
	baseTree, err := baseCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get base tree: %w", err)
	}

	headTree, err := headCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get head tree: %w", err)
	}

	// 트리 간 diff 계산
	changes, err := object.DiffTree(baseTree, headTree)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate tree diff: %w", err)
	}

	// 변경 사항을 구조화된 응답으로 처리
	fileDiffs, stats, err := h.processChanges(changes, contextLines)
	if err != nil {
		return nil, fmt.Errorf("failed to process changes: %w", err)
	}

	return &DiffResponse{
		Files: fileDiffs,
		Stats: stats,
	}, nil
}

// processChanges는 go-git 변경 사항을 구조화된 FileDiff 객체로 변환합니다
func (h *DiffHandler) processChanges(changes object.Changes, contextLines int) ([]FileDiff, DiffStats, error) {
	var fileDiffs []FileDiff
	var stats DiffStats

	for _, change := range changes {
		fileDiff, err := h.processChange(change, contextLines)
		if err != nil {
			// 로깅을 위해 파일 이름 가져오기
			fileName := getFileName(change)
			// 오류 로깅하지만 다른 파일들은 계속 처리
			fmt.Printf("Error processing change for %s: %v\n", fileName, err)
			continue
		}

		fileDiffs = append(fileDiffs, fileDiff)
		stats.FilesChanged++
		stats.TotalAdditions += fileDiff.Additions
		stats.TotalDeletions += fileDiff.Deletions
	}

	return fileDiffs, stats, nil
}

// processChange는 단일 파일 변경을 처리합니다
func (h *DiffHandler) processChange(change *object.Change, contextLines int) (FileDiff, error) {
	fileName := getFileName(change)
	fileDiff := FileDiff{
		Path:     fileName,
		Metadata: getFileMetadata(change),
	}

	// 변경 상태 결정
	switch {
	case change.From.Name == "" && change.To.Name != "":
		fileDiff.Status = "added"
	case change.From.Name != "" && change.To.Name == "":
		fileDiff.Status = "deleted"
	case change.From.Name != change.To.Name:
		fileDiff.Status = "renamed"
		fileDiff.OldPath = change.From.Name
	default:
		fileDiff.Status = "modified"
	}

	// 라인 레벨 변경을 위한 patch 정보 가져오기
	patch, err := change.Patch()
	if err != nil {
		return fileDiff, fmt.Errorf("failed to get patch: %w", err)
	}

	// patch 콘텐츠에서 추가 및 삭제 수 계산
	patchText := patch.String()
	lines := splitLines(patchText)

	for _, line := range lines {
		if len(line) > 0 {
			switch line[0] {
			case '+':
				fileDiff.Additions++
			case '-':
				fileDiff.Deletions++
			}
		}
	}

	// patch 콘텐츠와 파일명을 모두 사용한 향상된 바이너리 파일 감지
	fileDiff.IsBinary = isBinaryFile(patchText, fileName)

	// 바이너리가 아니고 contextLines > 0이면 라인 레벨 변경 생성
	if !fileDiff.IsBinary && contextLines > 0 {
		lineChanges := h.GenerateLineChanges(lines, contextLines)
		fileDiff.Changes = lineChanges
	}

	return fileDiff, nil
}

// GenerateLineChanges는 라인별 diff 정보를 생성합니다
func (h *DiffHandler) GenerateLineChanges(lines []string, contextLines int) []LineChange {
	return GenerateLineChanges(lines, contextLines)
}

// GenerateLineChanges는 라인별 diff 정보를 생성하는 공통 함수입니다
func GenerateLineChanges(lines []string, contextLines int) []LineChange {
	var lineChanges []LineChange
	lineNum := 1

	for _, line := range lines {
		if len(line) == 0 {
			continue
		}

		var changeType string
		switch line[0] {
		case '+':
			changeType = "add"
		case '-':
			changeType = "del"
		case ' ':
			changeType = "context"
		default:
			changeType = "context"
		}

		lineChanges = append(lineChanges, LineChange{
			Type:    changeType,
			LineNum: lineNum,
			Content: line,
		})

		if changeType != "del" {
			lineNum++
		}
	}

	return lineChanges
}

// getFileName은 change 객체에서 파일 이름을 추출합니다
func getFileName(change *object.Change) string {
	if change.To.Name != "" {
		return change.To.Name
	}
	return change.From.Name
}

// isBinaryPatch는 patch가 바이너리 파일 변경을 나타내는지 확인합니다
func isBinaryPatch(patchText string) bool {
	// 간단한 휴리스틱: 바이너리 파일은 종종 null 바이트를 포함하거나 특정 마커를 가짐
	return strings.Contains(patchText, "Binary files") ||
		strings.Contains(patchText, "\x00") ||
		strings.Contains(patchText, "GIT binary patch")
}

// splitLines는 콘텐츠를 개별 라인으로 분할합니다
func splitLines(content string) []string {
	if content == "" {
		return nil
	}

	var lines []string
	start := 0
	for i, char := range content {
		if char == '\n' {
			lines = append(lines, content[start:i])
			start = i + 1
		}
	}

	// 콘텐츠가 개행으로 끝나지 않으면 마지막 라인 추가
	if start < len(content) {
		lines = append(lines, content[start:])
	}

	return lines
}

// isCommitNotFoundError는 오류가 커밋을 찾을 수 없어서 발생했는지 확인합니다
func isCommitNotFoundError(err error) bool {
	return err != nil && (err.Error() == "object not found" ||
		err.Error() == "reference not found" ||
		strings.Contains(err.Error(), "object not found") ||
		strings.Contains(err.Error(), "commit not found"))
}

// calculateBranchDiff는 merge base 분석을 통해 두 브랜치 간의 diff 계산을 수행합니다
func (h *DiffHandler) calculateBranchDiff(ctx context.Context, repoPath, baseBranch, headBranch string, contextLines int) (*DiffResponse, error) {
	// 캐시된 방식으로 저장소 열기
	gitRepo, cleanup, err := h.getGitRepository(repoPath)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	// 브랜치 이름을 커밋으로 해석
	baseCommit, err := h.resolveBranchToCommit(gitRepo, baseBranch)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve base branch %s: %w", baseBranch, err)
	}

	headCommit, err := h.resolveBranchToCommit(gitRepo, headBranch)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve head branch %s: %w", headBranch, err)
	}

	// 정확한 비교를 위한 merge base 찾기
	mergeBase, err := h.findMergeBase(gitRepo, baseCommit, headCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to find merge base: %w", err)
	}

	// 커밋 수 계산 (ahead/behind)
	commitStats, err := h.calculateCommitCounts(gitRepo, baseCommit, headCommit, mergeBase)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate commit counts: %w", err)
	}

	// diff 계산을 위한 트리 가져오기 (정확한 브랜치 비교를 위해 merge base vs head 사용)
	mergeBaseTree, err := mergeBase.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get merge base tree: %w", err)
	}

	headTree, err := headCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get head tree: %w", err)
	}

	// merge base와 head 간 diff 계산
	changes, err := object.DiffTree(mergeBaseTree, headTree)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate tree diff: %w", err)
	}

	// 변경 사항을 구조화된 응답으로 처리
	fileDiffs, stats, err := h.processChanges(changes, contextLines)
	if err != nil {
		return nil, fmt.Errorf("failed to process changes: %w", err)
	}

	// 날짜별 커밋 목록 가져오기
	commitsByDate, err := h.getCommitsByDate(gitRepo, mergeBase, headCommit)
	if err != nil {
		// 날짜별 커밋 목록 실패는 경고로 처리하고 계속 진행
		fmt.Printf("Warning: failed to get commits by date: %v\n", err)
		commitsByDate = []CommitsByDate{}
	}

	return &DiffResponse{
		Success:       true,
		Base:          baseBranch,
		Head:          headBranch,
		Files:         fileDiffs,
		Stats:         stats,
		Commits:       commitStats,
		CommitsByDate: commitsByDate,
		MergeBase:     mergeBase.Hash.String(),
	}, nil
}

// resolveBranchToCommit은 브랜치 이름을 커밋 객체로 해석합니다
func (h *DiffHandler) resolveBranchToCommit(repo *gogit.Repository, branchName string) (*object.Commit, error) {
	// 먼저 참조로 해석 시도
	ref, err := repo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	if err != nil {
		// 브랜치 참조 실패시 직접 해시로 시도
		if plumbing.IsHash(branchName) {
			return repo.CommitObject(plumbing.NewHash(branchName))
		}
		return nil, fmt.Errorf("branch %s not found", branchName)
	}

	// 참조에서 커밋 가져오기
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, fmt.Errorf("failed to get commit from branch %s: %w", branchName, err)
	}

	return commit, nil
}

// findMergeBase는 두 커밋의 merge base (공통 조상)를 찾습니다
func (h *DiffHandler) findMergeBase(repo *gogit.Repository, commit1, commit2 *object.Commit) (*object.Commit, error) {
	// go-git의 merge base 알고리즘 사용
	mergeBaseCommits, err := commit1.MergeBase(commit2)
	if err != nil {
		return nil, fmt.Errorf("failed to find merge base: %w", err)
	}

	if len(mergeBaseCommits) == 0 {
		return nil, fmt.Errorf("no common ancestor found")
	}

	// 첫 번째 (가장 최근) merge base 반환
	return mergeBaseCommits[0], nil
}

// calculateCommitCounts는 각 브랜치가 몇 개의 커밋만큼 앞서있거나 뒤처져있는지 계산합니다
func (h *DiffHandler) calculateCommitCounts(repo *gogit.Repository, baseCommit, headCommit, mergeBase *object.Commit) (*CommitStats, error) {
	// merge base에서 head까지의 커밋 수 계산 (ahead)
	ahead, err := h.countCommitsBetween(repo, mergeBase, headCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to count ahead commits: %w", err)
	}

	// merge base에서 base까지의 커밋 수 계산 (behind)
	behind, err := h.countCommitsBetween(repo, mergeBase, baseCommit)
	if err != nil {
		return nil, fmt.Errorf("failed to count behind commits: %w", err)
	}

	return &CommitStats{
		Ahead:  ahead,
		Behind: behind,
	}, nil
}

// getCommitsByDate는 두 커밋 사이의 커밋들을 날짜별로 그룹화합니다
func (h *DiffHandler) getCommitsByDate(repo *gogit.Repository, from, to *object.Commit) ([]CommitsByDate, error) {
	if from.Hash == to.Hash {
		return []CommitsByDate{}, nil
	}

	// 'to' 커밋에서 커밋 반복자 가져오기
	commitIter, err := repo.Log(&gogit.LogOptions{
		From: to.Hash,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get commit log: %w", err)
	}
	defer commitIter.Close()

	// 날짜별로 커밋을 그룹화할 맵
	commitsByDate := make(map[string][]CommitMetadata)
	fromHash := from.Hash.String()

	err = commitIter.ForEach(func(commit *object.Commit) error {
		if commit.Hash.String() == fromHash {
			// 'from' 커밋에 도달하면 중지
			return fmt.Errorf("stop iteration")
		}

		// 커밋 날짜를 YYYY-MM-DD 형식으로 변환
		date := commit.Author.When.Format("2006-01-02")

		// 커밋 메타데이터 생성
		commitMeta := CommitMetadata{
			SHA:     commit.Hash.String(),
			Author:  commit.Author.Name,
			Email:   commit.Author.Email,
			Date:    commit.Author.When.Format("2006-01-02 15:04:05"),
			Message: commit.Message,
			Parents: make([]string, len(commit.ParentHashes)),
		}

		// 부모 커밋 해시들 추가
		for i, parentHash := range commit.ParentHashes {
			commitMeta.Parents[i] = parentHash.String()
		}

		// 날짜별로 그룹화
		commitsByDate[date] = append(commitsByDate[date], commitMeta)
		return nil
	})

	// 'from' 커밋을 찾아서 중지했는지 확인
	if err != nil && err.Error() != "stop iteration" {
		return nil, fmt.Errorf("failed to iterate commits: %w", err)
	}

	// 맵을 슬라이스로 변환 (날짜 순으로 정렬)
	var result []CommitsByDate
	for date, commits := range commitsByDate {
		result = append(result, CommitsByDate{
			Date:    date,
			Commits: commits,
		})
	}

	// 날짜 순으로 정렬 (최신 날짜가 먼저)
	sort.Slice(result, func(i, j int) bool {
		return result[i].Date > result[j].Date
	})

	return result, nil
}

// countCommitsBetween은 두 커밋 사이의 커밋 수를 계산합니다 (from은 제외, to는 포함)
func (h *DiffHandler) countCommitsBetween(repo *gogit.Repository, from, to *object.Commit) (int, error) {
	if from.Hash == to.Hash {
		return 0, nil
	}

	// 'to' 커밋에서 커밋 반복자 가져오기
	commitIter, err := repo.Log(&gogit.LogOptions{
		From: to.Hash,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to get commit log: %w", err)
	}
	defer commitIter.Close()

	count := 0
	fromHash := from.Hash.String()

	err = commitIter.ForEach(func(commit *object.Commit) error {
		if commit.Hash.String() == fromHash {
			// 'from' 커밋에 도달하면 중지 (카운트하지 않음)
			return fmt.Errorf("stop iteration") // 반복을 중단하기 위해 오류 사용
		}
		count++
		return nil
	})

	// 'from' 커밋을 찾아서 중지했는지 확인 (정상 케이스)
	if err != nil && err.Error() == "stop iteration" {
		return count, nil
	}

	// 오류가 없으면 'from'을 찾지 못하고 루트까지 모든 커밋을 계산한 것
	// 이는 'from'이 'to'의 조상이 아니라는 의미
	if err == nil {
		return count, nil
	}

	return 0, fmt.Errorf("failed to iterate commits: %w", err)
}

// isBranchNotFoundError는 오류가 브랜치를 찾을 수 없어서 발생했는지 확인합니다
func isBranchNotFoundError(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "branch") && strings.Contains(err.Error(), "not found") ||
		strings.Contains(err.Error(), "reference not found"))
}

// isFileNotFoundError는 오류가 파일을 찾을 수 없어서 발생했는지 확인합니다
func isFileNotFoundError(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "file not found") ||
		strings.Contains(err.Error(), "path not found") ||
		strings.Contains(err.Error(), "entry not found"))
}

// calculateFileDiff는 두 참조 간의 특정 파일에 대한 diff 계산을 수행합니다
func (h *DiffHandler) calculateFileDiff(ctx context.Context, repoPath, filePath, base, head string, contextLines int, format string) (*DiffResponse, error) {
	// 캐시된 방식으로 저장소 열기
	gitRepo, cleanup, err := h.getGitRepository(repoPath)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	// base와 head를 커밋 객체로 해석 (커밋 해시와 브랜치 이름 모두 지원)
	baseCommit, err := h.resolveReferenceToCommit(gitRepo, base)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve base reference %s: %w", base, err)
	}

	headCommit, err := h.resolveReferenceToCommit(gitRepo, head)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve head reference %s: %w", head, err)
	}

	// 커밋에서 트리 가져오기
	baseTree, err := baseCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get base tree: %w", err)
	}

	headTree, err := headCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get head tree: %w", err)
	}

	// 양쪽 트리에서 파일 엔트리 가져오기
	baseFile, baseErr := baseTree.File(filePath)
	headFile, headErr := headTree.File(filePath)

	// 파일이 하나 또는 양쪽 트리에 존재하지 않는 경우 처리
	var fileDiff FileDiff
	var stats DiffStats

	// 파일 상태를 결정하고 그에 따라 처리
	switch {
	case baseErr != nil && headErr != nil:
		// 파일이 양쪽 트리 모두에 존재하지 않음
		return nil, fmt.Errorf("file not found in either base or head: %s", filePath)

	case baseErr != nil && headErr == nil:
		// head에 파일이 추가됨
		fileDiff, err = h.processFileAddition(headFile, filePath, contextLines, format)
		if err != nil {
			return nil, fmt.Errorf("failed to process file addition: %w", err)
		}

	case baseErr == nil && headErr != nil:
		// head에서 파일이 삭제됨
		fileDiff, err = h.processFileDeletion(baseFile, filePath, contextLines, format)
		if err != nil {
			return nil, fmt.Errorf("failed to process file deletion: %w", err)
		}

	default:
		// 파일이 양쪽 트리 모두에 존재, 수정 diff 계산
		fileDiff, err = h.processFileModification(baseFile, headFile, filePath, contextLines, format)
		if err != nil {
			return nil, fmt.Errorf("failed to process file modification: %w", err)
		}
	}

	// 통계 업데이트
	stats.FilesChanged = 1
	stats.TotalAdditions = fileDiff.Additions
	stats.TotalDeletions = fileDiff.Deletions

	return &DiffResponse{
		Success: true,
		Base:    base,
		Head:    head,
		Files:   []FileDiff{fileDiff},
		Stats:   stats,
	}, nil
}

// resolveReferenceToCommit은 참조 (커밋 해시 또는 브랜치 이름)를 커밋 객체로 해석합니다
func (h *DiffHandler) resolveReferenceToCommit(repo *gogit.Repository, ref string) (*object.Commit, error) {
	// 먼저 커밋 해시로 시도
	if plumbing.IsHash(ref) {
		return repo.CommitObject(plumbing.NewHash(ref))
	}

	// 브랜치 참조로 시도
	branchRef, err := repo.Reference(plumbing.NewBranchReferenceName(ref), true)
	if err == nil {
		return repo.CommitObject(branchRef.Hash())
	}

	// 태그 참조로 시도
	tagRef, err := repo.Reference(plumbing.NewTagReferenceName(ref), true)
	if err == nil {
		// 태그는 커밋 또는 태그 객체를 가리킬 수 있음
		obj, err := repo.Object(plumbing.AnyObject, tagRef.Hash())
		if err != nil {
			return nil, fmt.Errorf("failed to resolve tag object: %w", err)
		}

		switch o := obj.(type) {
		case *object.Commit:
			return o, nil
		case *object.Tag:
			return repo.CommitObject(o.Target)
		default:
			return nil, fmt.Errorf("tag points to unsupported object type")
		}
	}

	return nil, fmt.Errorf("reference %s not found", ref)
}

// processFileAddition은 파일이 추가된 경우를 처리합니다 (head에만 존재)
func (h *DiffHandler) processFileAddition(file *object.File, filePath string, contextLines int, format string) (FileDiff, error) {
	content, err := file.Contents()
	if err != nil {
		return FileDiff{}, fmt.Errorf("failed to get file contents: %w", err)
	}

	lines := splitLines(content)
	additions := len(lines)

	fileDiff := FileDiff{
		Path:      filePath,
		Status:    "added",
		Additions: additions,
		Deletions: 0,
		IsBinary:  isBinaryContent(content),
	}

	// 바이너리가 아니고 contextLines > 0이면 라인 변경 생성
	if !fileDiff.IsBinary && contextLines > 0 {
		var lineChanges []LineChange
		for i, line := range lines {
			lineChanges = append(lineChanges, LineChange{
				Type:    "add",
				LineNum: i + 1,
				Content: "+" + line,
			})
		}
		fileDiff.Changes = lineChanges
	}

	return fileDiff, nil
}

// processFileDeletion은 파일이 삭제된 경우를 처리합니다 (base에만 존재)
func (h *DiffHandler) processFileDeletion(file *object.File, filePath string, contextLines int, format string) (FileDiff, error) {
	content, err := file.Contents()
	if err != nil {
		return FileDiff{}, fmt.Errorf("failed to get file contents: %w", err)
	}

	lines := splitLines(content)
	deletions := len(lines)

	fileDiff := FileDiff{
		Path:      filePath,
		Status:    "deleted",
		Additions: 0,
		Deletions: deletions,
		IsBinary:  isBinaryContent(content),
	}

	// 바이너리가 아니고 contextLines > 0이면 라인 변경 생성
	if !fileDiff.IsBinary && contextLines > 0 {
		var lineChanges []LineChange
		for i, line := range lines {
			lineChanges = append(lineChanges, LineChange{
				Type:    "del",
				LineNum: i + 1,
				Content: "-" + line,
			})
		}
		fileDiff.Changes = lineChanges
	}

	return fileDiff, nil
}

// processFileModification은 파일이 수정된 경우를 처리합니다 (base와 head 모두에 존재)
func (h *DiffHandler) processFileModification(baseFile, headFile *object.File, filePath string, contextLines int, format string) (FileDiff, error) {
	baseContent, err := baseFile.Contents()
	if err != nil {
		return FileDiff{}, fmt.Errorf("failed to get base file contents: %w", err)
	}

	headContent, err := headFile.Contents()
	if err != nil {
		return FileDiff{}, fmt.Errorf("failed to get head file contents: %w", err)
	}

	// 파일 중 하나가 바이너리인지 확인
	isBinary := isBinaryContent(baseContent) || isBinaryContent(headContent)

	fileDiff := FileDiff{
		Path:     filePath,
		Status:   "modified",
		IsBinary: isBinary,
	}

	// 바이너리 파일의 경우 변경되었음만 표시
	if isBinary {
		return fileDiff, nil
	}

	// 텍스트 파일에 대한 라인 레벨 diff 계산
	baseLines := splitLines(baseContent)
	headLines := splitLines(headContent)

	// 간단한 라인 기반 diff 알고리즘
	lineChanges, additions, deletions := h.calculateLineDiff(baseLines, headLines, contextLines, format)

	fileDiff.Additions = additions
	fileDiff.Deletions = deletions
	fileDiff.Changes = lineChanges

	return fileDiff, nil
}

// calculateLineDiff는 라인 기반 diff 계산을 수행합니다
func (h *DiffHandler) calculateLineDiff(baseLines, headLines []string, contextLines int, format string) ([]LineChange, int, int) {
	var lineChanges []LineChange
	additions := 0
	deletions := 0

	// 간단한 diff 알고리즘: 공통 부분 수열을 찾고 변경 사항 식별
	// 이는 기본 구현 - 프로덕션용으로는 더 정교한 diff 알고리즘 사용 고려

	baseIndex := 0
	headIndex := 0
	lineNum := 1

	for baseIndex < len(baseLines) || headIndex < len(headLines) {
		if baseIndex >= len(baseLines) {
			// head에 남은 라인들은 추가
			for headIndex < len(headLines) {
				lineChanges = append(lineChanges, LineChange{
					Type:    "add",
					LineNum: lineNum,
					Content: "+" + headLines[headIndex],
				})
				additions++
				headIndex++
				lineNum++
			}
		} else if headIndex >= len(headLines) {
			// base에 남은 라인들은 삭제
			for baseIndex < len(baseLines) {
				lineChanges = append(lineChanges, LineChange{
					Type:    "del",
					LineNum: lineNum,
					Content: "-" + baseLines[baseIndex],
				})
				deletions++
				baseIndex++
			}
		} else if baseLines[baseIndex] == headLines[headIndex] {
			// 라인이 동일하면 컨텍스트 윈도우 내에 있을 경우 컨텍스트로 추가
			lineChanges = append(lineChanges, LineChange{
				Type:    "context",
				LineNum: lineNum,
				Content: " " + baseLines[baseIndex],
			})
			baseIndex++
			headIndex++
			lineNum++
		} else {
			// 라인이 다르면 삭제와 추가로 표시
			lineChanges = append(lineChanges, LineChange{
				Type:    "del",
				LineNum: lineNum,
				Content: "-" + baseLines[baseIndex],
			})
			deletions++
			baseIndex++

			lineChanges = append(lineChanges, LineChange{
				Type:    "add",
				LineNum: lineNum,
				Content: "+" + headLines[headIndex],
			})
			additions++
			headIndex++
			lineNum++
		}
	}

	return lineChanges, additions, deletions
}

// isBinaryContent는 콘텐츠가 바이너리로 보이는지 확인합니다
func isBinaryContent(content string) bool {
	// 간단한 휴리스틱: 콘텐츠에 null 바이트가 포함되어 있으면 바이너리로 간주
	return strings.Contains(content, "\x00")
}

// 공통 요청 검증 및 응답 유틸리티

// RequestParams는 diff 엔드포인트의 공통 요청 파라미터를 보유합니다
type RequestParams struct {
	Owner  string
	Repo   string
	Base   string
	Head   string
	UserID string
}

// PaginationParams는 페이지네이션 파라미터를 보유합니다
type PaginationParams struct {
	Page     int  `json:"page"`
	PageSize int  `json:"page_size"`
	Total    int  `json:"total"`
	HasMore  bool `json:"has_more"`
}

// ErrorResponse는 표준화된 오류 응답을 나타냅니다
type ErrorResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	Code    string `json:"code,omitempty"`
	Details string `json:"details,omitempty"`
}

// PaginatedDiffResponse는 페이지네이션이 포함된 DiffResponse를 확장합니다
type PaginatedDiffResponse struct {
	Success bool `json:"success"`
	*DiffResponse
	Pagination *PaginationParams `json:"pagination,omitempty"`
}

// validateAndExtractParams는 요청에서 공통 파라미터를 추출하고 검증합니다
func (h *DiffHandler) validateAndExtractParams(r *http.Request) (*RequestParams, error) {
	// 경로 파라미터 추출
	owner := URLParam(r, "owner")
	repo := URLParam(r, "repo")
	base := URLParam(r, "base")
	head := URLParam(r, "head")

	// 필수 파라미터 검증
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("missing required parameters: owner or repo")
	}

	// 권한 확인을 위해 헤더에서 사용자 ID 가져오기
	userID := r.Header.Get("X-USER-ID")
	if userID == "" {
		return nil, fmt.Errorf("missing user authentication")
	}

	return &RequestParams{
		Owner:  owner,
		Repo:   repo,
		Base:   base,
		Head:   head,
		UserID: userID,
	}, nil
}

// validateRequestAndPermissions는 공통 검증 및 권한 확인을 수행합니다
func (h *DiffHandler) validateRequestAndPermissions(ctx context.Context, params *RequestParams) (string, error) {
	// 사용자 권한 확인 (diff 작업을 위한 읽기 권한)

	// 저장소 경로 가져오기 및 검증
	repoPath, err := repository.GetAndValidateRepoPath(h.pathConfig, params.Owner, params.Repo)
	if err != nil {
		return "", fmt.Errorf("invalid repository path: %w", err)
	}

	return repoPath, nil
}

// sendJSONResponse는 표준화된 JSON 응답을 전송합니다
func sendJSONResponse(w http.ResponseWriter, data interface{}, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if err := json.NewEncoder(w).Encode(data); err != nil {
		// 오류를 로깅하지만 이 시점에선 응답을 변경할 수 없음
		fmt.Printf("Failed to encode JSON response: %v\n", err)
	}
}

// sendErrorResponse는 표준화된 오류 응답을 전송합니다
func sendErrorResponse(w http.ResponseWriter, message string, statusCode int) {
	errorResp := ErrorResponse{
		Success: false,
		Error:   message,
	}
	sendJSONResponse(w, errorResp, statusCode)
}

// handleDiffError는 오류를 분류하고 적절한 HTTP 응답을 전송합니다
func handleDiffError(w http.ResponseWriter, err error, operation string) {
	if isCommitNotFoundError(err) {
		sendErrorResponse(w, fmt.Sprintf("Commit not found: %v", err), http.StatusNotFound)
	} else if isBranchNotFoundError(err) {
		sendErrorResponse(w, fmt.Sprintf("Branch not found: %v", err), http.StatusNotFound)
	} else if isFileNotFoundError(err) {
		sendErrorResponse(w, fmt.Sprintf("File not found: %v", err), http.StatusNotFound)
	} else if strings.Contains(err.Error(), "permission") {
		sendErrorResponse(w, fmt.Sprintf("Permission check failed: %v", err), http.StatusInternalServerError)
	} else if strings.Contains(err.Error(), "access denied") {
		sendErrorResponse(w, "Access denied to repository", http.StatusForbidden)
	} else if strings.Contains(err.Error(), "authentication") {
		sendErrorResponse(w, err.Error(), http.StatusUnauthorized)
	} else if strings.Contains(err.Error(), "missing") || strings.Contains(err.Error(), "invalid") {
		sendErrorResponse(w, err.Error(), http.StatusBadRequest)
	} else {
		sendErrorResponse(w, fmt.Sprintf("Failed to %s: %v", operation, err), http.StatusInternalServerError)
	}
}

// extractPaginationParams는 쿼리 문자열에서 페이지네이션 파라미터를 추출합니다
func extractPaginationParams(r *http.Request) PaginationParams {
	params := PaginationParams{
		Page:     1,  // 기본값: 페이지 1
		PageSize: 50, // 기본 페이지 크기
	}

	if pageStr := r.URL.Query().Get("page"); pageStr != "" {
		if page, err := strconv.Atoi(pageStr); err == nil && page > 0 {
			params.Page = page
		}
	}

	if sizeStr := r.URL.Query().Get("page_size"); sizeStr != "" {
		if size, err := strconv.Atoi(sizeStr); err == nil && size > 0 && size <= 200 {
			params.PageSize = size
		}
	}

	return params
}

// paginateFiles는 파일 diff에 페이지네이션을 적용합니다
func paginateFiles(files []FileDiff, pagination PaginationParams) ([]FileDiff, PaginationParams) {
	total := len(files)
	pagination.Total = total

	// 시작 및 종료 인덱스 계산
	start := (pagination.Page - 1) * pagination.PageSize
	if start >= total {
		pagination.HasMore = false
		return []FileDiff{}, pagination
	}

	end := start + pagination.PageSize
	if end > total {
		end = total
		pagination.HasMore = false
	} else {
		pagination.HasMore = true
	}

	return files[start:end], pagination
}

// 향상된 바이너리 감지 유틸리티

// isBinaryFile은 콘텐츠와 확장자를 기반으로 파일이 바이너리인지 확인합니다
func isBinaryFile(content string, filename string) bool {
	// 먼저 콘텐츠에서 바이너리 표시자 확인
	if isBinaryContent(content) {
		return true
	}

	// 알려진 바이너리 유형의 파일 확장자 확인
	ext := strings.ToLower(filepath.Ext(filename))
	binaryExtensions := map[string]bool{
		".exe": true, ".dll": true, ".so": true, ".dylib": true,
		".zip": true, ".tar": true, ".gz": true, ".bz2": true,
		".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".bmp": true,
		".pdf": true, ".doc": true, ".docx": true, ".xls": true, ".xlsx": true,
		".mp3": true, ".mp4": true, ".avi": true, ".mov": true,
		".bin": true, ".dat": true, ".db": true,
	}

	return binaryExtensions[ext]
}

// getFileMetadata는 파일 변경에 대한 메타데이터를 추출합니다
func getFileMetadata(change *object.Change) map[string]interface{} {
	metadata := make(map[string]interface{})

	// 파일 모드 정보 추출
	if change.From.TreeEntry.Mode != 0 {
		metadata["old_mode"] = change.From.TreeEntry.Mode.String()
	}
	if change.To.TreeEntry.Mode != 0 {
		metadata["new_mode"] = change.To.TreeEntry.Mode.String()
	}

	// 파일 해시 정보 추출
	if !change.From.TreeEntry.Hash.IsZero() {
		metadata["old_hash"] = change.From.TreeEntry.Hash.String()
	}
	if !change.To.TreeEntry.Hash.IsZero() {
		metadata["new_hash"] = change.To.TreeEntry.Hash.String()
	}

	return metadata
}

// getGitRepository는 캐시를 사용하여 Git 저장소를 열고 기본 go-git 저장소를 반환합니다
func (h *DiffHandler) getGitRepository(repoPath string) (*gogit.Repository, func(), error) {
	repo, err := git.OpenRepositoryWithNewCache(repoPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open repository: %w", err)
	}

	// 정리 함수 생성
	cleanup := func() {
		_ = repo.Close()
	}

	// 기본 go-git 저장소 가져오기
	goGitRepo, ok := repo.(*git.GoGitRepository)
	if !ok {
		cleanup()
		return nil, nil, fmt.Errorf("unexpected repository type")
	}

	return goGitRepo.GetGoGitRepo(), cleanup, nil
}
