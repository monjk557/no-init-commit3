package commits

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"go.uber.org/zap"
)

// CommitsService는 커밋 관련 비즈니스 로직을 처리하는 서비스입니다.
type CommitsService struct {
	logger             *logging.Logger
	transactionManager *TransactionManager
	eventsClient       events.RedisStreamClient
}

// NewCommitsService는 새로운 CommitsService를 생성합니다.
func NewCommitsService(logger *logging.Logger, transactionManager *TransactionManager, eventsClient events.RedisStreamClient) *CommitsService {
	return &CommitsService{
		logger:             logger,
		transactionManager: transactionManager,
		eventsClient:       eventsClient,
	}
}

// getCommitHistory는 Git 저장소 인터페이스를 사용하여 커밋 히스토리를 조회합니다.
func (h *CommitsHandler) getCommitHistory(ctx context.Context, repoWrapper *repositories.RepositoryWrapper, params *CommitsRequestParams) ([]Commit, int, error) {
	// Git 저장소 인터페이스에서 커밋 조회
	// 더 많은 페이지가 있는지 확인하기 위해 하나 더 요청
	requestLimit := params.Limit + 1
	commits, err := GetCommits(repoWrapper, ctx, params.Ref, requestLimit, params.Skip)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get commits: %w", err)
	}

	// 더 많은 커밋이 있는지 확인 (페이지네이션)
	totalCount := len(commits)

	return commits, totalCount, nil
}

// convertGitCommitToMetadata는 git.Commit을 CommitMetadata로 변환합니다.
func (h *CommitsHandler) convertGitCommitToMetadata(commit Commit) CommitMetadata {
	return CommitMetadata{
		SHA:     commit.Sha,
		Author:  commit.Author.Name,
		Email:   commit.Author.Email,
		Date:    commit.Author.Date.Format("2006-01-02 15:04:05"),
		Message: commit.Message,
		Parents: commit.ParentHashes,
	}
}

// validateAndExtractCommitsParams는 커밋 요청에서 파라미터를 추출하고 검증합니다.
func (h *CommitsHandler) validateAndExtractCommitsParams(c *gin.Context) (*CommitsRequestParams, error) {
	// 경로 파라미터 추출
	owner := c.Param("owner")
	repo := c.Param("repo")

	// 쿼리 파라미터 추출
	ref := c.Query("ref")
	limitStr := c.Query("limit")
	skipStr := c.Query("skip")
	sort := c.Query("sort")

	// 필수 파라미터 검증
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("missing required parameters: owner or repo")
	}

	// ref가 제공되지 않은 경우 기본값으로 "HEAD" 설정
	if ref == "" {
		ref = "HEAD"
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

// groupCommitsByDate는 커밋을 날짜별로 그룹화합니다 (YYYY-MM-DD 형식).
// 일관성을 위해 diff.go의 CommitsByDate 타입을 재사용합니다.
// 커밋은 작성자 날짜를 기준으로 그룹화되며, 날짜 내림차순으로 정렬됩니다.
func (h *CommitsHandler) groupCommitsByDate(commits []Commit) []CommitsByDate {
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

// GetCommits는 저장소 래퍼로부터 페이지네이션을 사용하여 커밋 히스토리를 조회합니다.
func GetCommits(rw *repositories.RepositoryWrapper, ctx context.Context, ref string, limit, skip int) ([]Commit, error) {
	// 컨텍스트 취소 확인
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// 참조를 커밋 해시로 변환
	hash, err := rw.ResolveReference(ref)
	if err != nil {
		return nil, err
	}

	// 내부 저장소 가져오기
	gitRepo := rw.GetRepo()
	if gitRepo == nil {
		return nil, fmt.Errorf("repository is closed")
	}

	// 변환된 해시부터 시작하는 커밋 반복자 가져오기
	commitIter, err := gitRepo.Log(&git.LogOptions{
		From: hash,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get commit log: %w", err)
	}
	defer commitIter.Close()

	var commits []Commit
	current := 0

	err = commitIter.ForEach(func(c *object.Commit) error {
		// 반복 중 컨텍스트 취소 확인
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// 필요한 경우 커밋 건너뛰기
		if current < skip {
			current++
			return nil
		}

		// 제한에 도달했는지 확인
		if limit > 0 && len(commits) >= limit {
			return io.EOF
		}

		commits = append(commits, convertCommit(c))
		current++

		return nil
	})

	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("failed to iterate commits: %w", err)
	}

	return commits, nil
}

// CountCommits는 특정 ref의 전체 커밋 개수를 효율적으로 세는 함수입니다.
// 커밋 객체를 메모리에 로드하지 않고 iterator를 순회하면서 카운트만 합니다.
func CountCommits(rw *repositories.RepositoryWrapper, ctx context.Context, ref string) (int, error) {
	// 컨텍스트 취소 확인
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	// 참조를 커밋 해시로 변환
	hash, err := rw.ResolveReference(ref)
	if err != nil {
		return 0, err
	}

	// 내부 저장소 가져오기
	gitRepo := rw.GetRepo()
	if gitRepo == nil {
		return 0, fmt.Errorf("repository is closed")
	}

	// 커밋 반복자 가져오기
	commitIter, err := gitRepo.Log(&git.LogOptions{
		From: hash,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to get commit log: %w", err)
	}
	defer commitIter.Close()

	count := 0
	err = commitIter.ForEach(func(c *object.Commit) error {
		// 컨텍스트 취소 확인
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		count++
		return nil
	})

	if err != nil && err != io.EOF {
		return 0, fmt.Errorf("failed to iterate commits: %w", err)
	}

	return count, nil
}

// convertCommit은 go-git 커밋을 내부 Commit 형식으로 변환합니다.
// 커밋 메시지를 정리하고 작성자/커밋터 정보를 설정합니다.
func convertCommit(c *object.Commit) Commit {
	var parentHashes []string
	for _, parent := range c.ParentHashes {
		parentHashes = append(parentHashes, parent.String())
	}

	// 커밋 메시지 정리: 끝의 개행 제거 및 내부 개행을 공백으로 대체
	message := strings.TrimSpace(c.Message)
	message = strings.ReplaceAll(message, "\n", " ")
	message = strings.ReplaceAll(message, "\r", " ")

	return Commit{
		Sha:     c.Hash.String(),
		Message: message,
		Author: CommitAuthor{
			Name:  c.Author.Name,
			Email: c.Author.Email,
			Date:  c.Author.When,
		},
		Committer: CommitAuthor{
			Name:  c.Committer.Name,
			Email: c.Committer.Email,
			Date:  c.Committer.When,
		},
		ParentHashes: parentHashes,
		TreeHash:     c.TreeHash.String(),
	}
}

// convertToFileChange는 MultiFileOperation을 FileChange로 변환합니다
func convertToFileChange(op *MultiFileOperation) (*FileChange, error) {
	fileChange := &FileChange{
		Path:         op.Path,
		Operation:    FileOperation(op.Operation),
		OriginalPath: op.OriginalPath,
	}

	// 콘텐츠 인코딩 처리
	if op.Content != "" {
		encoding := op.Encoding
		if encoding == "" {
			encoding = "utf-8"
		}

		switch encoding {
		case "utf-8":
			fileChange.Content = []byte(op.Content)
		case "base64":
			decoded, err := base64.StdEncoding.DecodeString(op.Content)
			if err != nil {
				return nil, fmt.Errorf("invalid base64 content: %w", err)
			}
			fileChange.Content = decoded
		default:
			return nil, fmt.Errorf("unsupported encoding: %s", encoding)
		}
	}

	// 파일 모드 처리
	if op.Mode != "" {
		switch op.Mode {
		case "100644", "644":
			mode := filemode.FileMode(0644)
			fileChange.Mode = &mode
		case "100755", "755":
			mode := filemode.FileMode(0755)
			fileChange.Mode = &mode
		default:
			return nil, fmt.Errorf("unsupported file mode: %s", op.Mode)
		}
	}

	return fileChange, nil
}

// ExecuteMultiFileCommit는 다중 파일 커밋 작업을 실행합니다.
// 이 메서드는 비즈니스 로직의 핵심을 담당하며, 트랜잭션을 통해 원자적으로 처리합니다.
func (s *CommitsService) ExecuteMultiFileCommit(ctx context.Context, repository *repositories.RepositoryWrapper, req *MultiFileCommitRequest, branch string, owner, repo string) (*MultiFileCommitResponse, error) {
	// 기본 커밋 해시 가져오기
	baseHash, err := repository.ResolveReference(branch)
	if err != nil {
		// 브랜치가 없으면 HEAD 시도
		baseHash, err = repository.ResolveReference("HEAD")
		if err != nil {
			return nil, fmt.Errorf("branch '%s' not found", branch)
		}
	}

	// 커밋 및 트리 가져오기 (파일 존재 확인용)
	gitRepo := repository.GetRepo()
	if gitRepo == nil {
		return nil, fmt.Errorf("repository is closed")
	}

	commit, err := gitRepo.CommitObject(baseHash)
	if err != nil {
		return nil, fmt.Errorf("commit not found")
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get commit tree")
	}

	// create 작업의 파일 존재 충돌 확인 및 delete 작업 검증
	// baseTree.Files()를 사용하여 모든 파일을 확인 (서브디렉토리 포함)
	existingFiles := make(map[string]bool)
	tree.Files().ForEach(func(f *object.File) error {
		existingFiles[f.Name] = true
		return nil
	})

	for _, op := range req.Operations {
		switch op.Operation {
		case "create":
			// 파일이 이미 존재하는지 확인
			if existingFiles[op.Path] {
				return nil, fmt.Errorf("file already exists: %s", op.Path)
			}
			existingFiles[op.Path] = true

		case "delete":
			// 파일이 존재하는지 확인 - delete 작업에는 파일이 존재해야 함
			if !existingFiles[op.Path] {
				return nil, fmt.Errorf("file '%s' not found in repository", op.Path)
			}
			delete(existingFiles, op.Path)

		case "move":
			// 원본 파일 존재 확인 + 대상 경로 충돌 확인
			if op.OriginalPath == "" {
				return nil, fmt.Errorf("original_path is required for move operations")
			}
			if !existingFiles[op.OriginalPath] {
				return nil, fmt.Errorf("file '%s' not found in repository", op.OriginalPath)
			}
			if existingFiles[op.Path] {
				return nil, fmt.Errorf("file already exists: %s", op.Path)
			}
			delete(existingFiles, op.OriginalPath)
			existingFiles[op.Path] = true
		}
	}

	// 작성자 및 커밋터 기본값 설정
	if req.Author == nil {
		req.Author = &CommitAuthor{
			Name:  "Git API",
			Email: "api@git-executor.local",
		}
	}
	if req.Committer == nil {
		req.Committer = req.Author
	}

	// 트랜잭션 옵션 준비
	options := TransactionOptions{
		Timeout:            30 * time.Minute, // 기본 타임아웃
		ValidateOnly:       false,
		AllowPartialCommit: false,
	}

	if req.Options != nil {
		if req.Options.Timeout > 0 {
			options.Timeout = req.Options.Timeout
		}
		if req.Options.ValidateOnly || req.Options.DryRun {
			options.ValidateOnly = true
		}
		if req.Options.AllowPartialCommit {
			options.AllowPartialCommit = true
		}
	}

	// 커밋 정보 생성
	commitInfo := CommitInfo{
		Message: req.Message,
		Author: object.Signature{
			Name:  req.Author.Name,
			Email: req.Author.Email,
			When:  time.Now(),
		},
		Committer: &object.Signature{
			Name:  req.Committer.Name,
			Email: req.Committer.Email,
			When:  time.Now(),
		},
		Parents: []plumbing.Hash{baseHash},
	}

	// 트랜잭션 시작
	transaction, err := s.transactionManager.BeginTransaction(ctx, repository, baseHash, commitInfo, options)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}

	// 트랜잭션 정리 보장
	defer func() {
		if transaction.State != TransactionStateCommitted {
			if rollbackErr := s.transactionManager.RollbackTransaction(ctx, transaction); rollbackErr != nil {
				s.logger.Warn("트랜잭션 정리 중 롤백 실패",
					zap.String("transaction_id", transaction.ID),
					zap.Error(rollbackErr))
			}
		}
		s.transactionManager.CleanupTransaction(transaction.ID)
	}()

	// 작업 변환 및 트랜잭션에 추가
	operationResults := make([]OperationResult, len(req.Operations))
	for i, op := range req.Operations {
		fileChange, err := convertToFileChange(&op)
		if err != nil {
			operationResults[i] = OperationResult{
				Path:      op.Path,
				Operation: Operation(op.Operation),
				Success:   false,
				Error:     err.Error(),
			}
			if !options.AllowPartialCommit {
				return nil, fmt.Errorf("operation %d failed: %w", i, err)
			}
			continue
		}

		txnOp := TransactionOperation{
			Type:       FileOperation(op.Operation),
			FileChange: *fileChange,
		}

		if err := s.transactionManager.AddOperation(ctx, transaction, txnOp); err != nil {
			operationResults[i] = OperationResult{
				Path:      op.Path,
				Operation: Operation(op.Operation),
				Success:   false,
				Error:     err.Error(),
			}
			if !options.AllowPartialCommit {
				return nil, fmt.Errorf("failed to add operation: %w", err)
			}
		} else {
			operationResults[i] = OperationResult{
				Path:      op.Path,
				Operation: Operation(op.Operation),
				Success:   true,
			}
		}
	}

	// 트랜잭션 검증
	if err := s.transactionManager.ValidateTransaction(ctx, transaction); err != nil {
		// 부분 커밋의 경우 일부 작업이 검증에 실패해도 계속 진행
		if !options.AllowPartialCommit {
			return nil, fmt.Errorf("transaction validation failed: %w", err)
		}
		s.logger.Warn("트랜잭션 검증에 문제가 있었지만 부분 커밋으로 계속 진행",
			zap.String("transaction_id", transaction.ID),
			zap.Error(err))
	}

	// 트랜잭션 커밋
	result, err := s.transactionManager.CommitTransaction(ctx, transaction)
	if err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	// 트랜잭션이 성공한 경우 브랜치 참조 업데이트
	if result.State == TransactionStateCommitted && result.CommitResult != nil {
		refName := plumbing.ReferenceName(fmt.Sprintf("refs/heads/%s", branch))
		newRef := plumbing.NewHashReference(refName, result.CommitResult.Hash)
		if err := repository.SetReference(newRef); err != nil {
			s.logger.Error("커밋 성공 후 브랜치 참조 업데이트 실패",
				zap.String("branch", branch),
				zap.String("commit_hash", result.CommitResult.Hash.String()),
				zap.Error(err))
			// 전체 작업을 실패시키지 않지만 에러를 로깅
		}

		// Publish commit event if enabled
		if s.eventsClient != nil && owner != "" && repo != "" {
			oldHashStr := baseHash.String()
			s.addCommitEventToStream(ctx, repository, oldHashStr, result.CommitResult.Hash, branch, owner, repo)
		}
	}

	// 응답 준비
	response := MultiFileCommitResponse{
		Success:          result.State == TransactionStateCommitted,
		TransactionID:    result.TransactionID,
		Message:          req.Message,
		TotalOperations:  len(req.Operations),
		SuccessfulOps:    result.GetSuccessfulOperationCount(),
		FailedOps:        result.GetFailedOperationCount(),
		OperationResults: operationResults,
		ValidationOnly:   options.ValidateOnly,
		Duration:         result.Duration,
	}

	if result.CommitResult != nil {
		response.CommitSHA = result.CommitResult.Hash.String()
	}

	// 문제가 있었던 경우 세부 정보 추가
	if result.GetFailedOperationCount() > 0 {
		response.Details = map[string]interface{}{
			"partial_success":   options.AllowPartialCommit,
			"failed_operations": result.FailedOperations,
		}
	}

	return &response, nil
}

// ExecuteMultiFileCommitOrCreateBranch는 create_branch_name이 있으면 (source_branch_name/branch/HEAD 기준) 브랜치를 만든 후 커밋을 생성합니다.
// 커밋 생성이 실패하면 브랜치 참조 및 HEAD를 best-effort로 롤백합니다.
func (s *CommitsService) ExecuteMultiFileCommitOrCreateBranch(
	ctx context.Context,
	repository *repositories.RepositoryWrapper,
	req *MultiFileCommitRequest,
	owner, repo string,
) (*MultiFileCommitResponse, error) {
	if repository == nil {
		return nil, fmt.Errorf("repository is closed")
	}
	if req == nil {
		return nil, fmt.Errorf("request is required")
	}

	// 기존 동작 유지: branch 미지정이면 main
	targetBranch := req.Branch
	if targetBranch == "" {
		targetBranch = "main"
	}

	// create_branch_name이 없으면 기존 branch에 커밋 생성
	if req.CreateBranchName == "" {
		return s.ExecuteMultiFileCommit(ctx, repository, req, targetBranch, owner, repo)
	}

	// create_branch_name이 있으면 source를 결정하고 브랜치 생성 후 그 브랜치로 커밋
	sourceRef := req.SourceBranchName
	if sourceRef == "" {
		// source_branch_name 미지정 시: branch가 있으면 그 branch를 source로, 아니면 HEAD
		if req.Branch != "" {
			sourceRef = req.Branch
		} else {
			sourceRef = "HEAD"
		}
	}

	gitRepo := repository.GetRepo()
	if gitRepo == nil {
		return nil, fmt.Errorf("repository is closed")
	}

	// Capture rollback info (branch ref + HEAD) before mutating refs.
	branchRefName := plumbing.ReferenceName(fmt.Sprintf("refs/heads/%s", req.CreateBranchName))

	var existedBefore bool
	var previousBranchHash plumbing.Hash
	ref, refErr := gitRepo.Reference(branchRefName, false)
	if refErr == nil {
		existedBefore = true
		previousBranchHash = ref.Hash()
	} else if refErr == plumbing.ErrReferenceNotFound {
		existedBefore = false
	} else {
		return nil, fmt.Errorf("failed to read branch reference: %w", refErr)
	}

	// match BranchService.CreateBranch default behavior: existing branch without force -> conflict
	if existedBefore {
		return nil, fmt.Errorf("branch '%s' already exists.", req.CreateBranchName)
	}

	// HEAD rollback info (best-effort)
	// IMPORTANT: repo.Head() resolves symbolic refs, so use Reference(HEAD, false) to capture the actual HEAD ref state.
	var previousHEADType plumbing.ReferenceType
	var previousHEADTarget plumbing.ReferenceName
	var previousHEADHash plumbing.Hash
	if headRef, err := gitRepo.Reference(plumbing.HEAD, false); err == nil && headRef != nil {
		previousHEADType = headRef.Type()
		if previousHEADType == plumbing.SymbolicReference {
			previousHEADTarget = headRef.Target()
		} else {
			previousHEADHash = headRef.Hash()
		}
	}

	err := createBranchFromSource(gitRepo, req.CreateBranchName, sourceRef)
	if err != nil {
		return nil, err
	}

	commitReq := *req
	commitReq.Branch = req.CreateBranchName
	resp, err := s.ExecuteMultiFileCommit(ctx, repository, &commitReq, commitReq.Branch, owner, repo)
	if err != nil {
		// Rollback branch + HEAD if commit creation fails.
		rbErr := s.rollbackBranchAndHEAD(gitRepo, branchRefName, existedBefore, previousBranchHash, previousHEADType, previousHEADTarget, previousHEADHash)
		if rbErr != nil {
			s.logger.Warn("Rollback failed after commit error",
				zap.String("branch", req.CreateBranchName),
				zap.Bool("branch_existed_before", existedBefore),
				zap.Error(rbErr),
			)
			return nil, fmt.Errorf("commit failed: %w; rollback failed: %v", err, rbErr)
		}
		return nil, err
	}

	return resp, nil
}

func resolveSourceReference(repo *git.Repository, source string) (plumbing.Hash, error) {
	// Try as commit hash first
	if len(source) >= 7 && len(source) <= 40 {
		if hash := plumbing.NewHash(source); hash != plumbing.ZeroHash {
			if _, err := repo.CommitObject(hash); err == nil {
				return hash, nil
			}
		}
	}

	// Try as branch reference
	branchRef := plumbing.ReferenceName(fmt.Sprintf("refs/heads/%s", source))
	if ref, err := repo.Reference(branchRef, false); err == nil {
		return ref.Hash(), nil
	}

	// Try as tag reference
	tagRef := plumbing.ReferenceName(fmt.Sprintf("refs/tags/%s", source))
	if ref, err := repo.Reference(tagRef, false); err == nil {
		tagHash := ref.Hash()
		tagObj, err := repo.TagObject(tagHash)
		if err == nil {
			// Annotated tag - return the target commit hash
			return tagObj.Target, nil
		}
		// Lightweight tag - return the hash directly
		return tagHash, nil
	}

	// Try HEAD
	if source == "HEAD" {
		if headRef, err := repo.Head(); err == nil {
			return headRef.Hash(), nil
		}
	}

	return plumbing.ZeroHash, fmt.Errorf("unable to resolve reference '%s'", source)
}

func createBranchFromSource(repo *git.Repository, branchName, source string) error {
	if branchName == "" {
		return fmt.Errorf("branch name is required")
	}
	if source == "" {
		source = "HEAD"
	}

	sourceCommit, err := resolveSourceReference(repo, source)
	if err != nil {
		return fmt.Errorf("failed to resolve source reference '%s': %w", source, err)
	}

	refPath := fmt.Sprintf("refs/heads/%s", branchName)
	ref := plumbing.NewHashReference(plumbing.ReferenceName(refPath), sourceCommit)
	if err := repo.Storer.SetReference(ref); err != nil {
		return fmt.Errorf("failed to create branch: %w", err)
	}

	return nil
}

func (s *CommitsService) rollbackBranchAndHEAD(
	repo *git.Repository,
	branchRefName plumbing.ReferenceName,
	existedBefore bool,
	previousBranchHash plumbing.Hash,
	previousHEADType plumbing.ReferenceType,
	previousHEADTarget plumbing.ReferenceName,
	previousHEADHash plumbing.Hash,
) error {
	// Branch rollback
	if existedBefore {
		if previousBranchHash == plumbing.ZeroHash {
			return fmt.Errorf("previous branch hash is zero")
		}
		if err := repo.Storer.SetReference(plumbing.NewHashReference(branchRefName, previousBranchHash)); err != nil {
			return fmt.Errorf("failed to restore branch ref: %w", err)
		}
	} else {
		if err := repo.Storer.RemoveReference(branchRefName); err != nil {
			return fmt.Errorf("failed to remove branch ref: %w", err)
		}
	}

	// HEAD rollback (best-effort: only if we captured a previous value)
	switch previousHEADType {
	case plumbing.SymbolicReference:
		if previousHEADTarget != "" {
			if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, previousHEADTarget)); err != nil {
				return fmt.Errorf("failed to restore HEAD symbolic ref: %w", err)
			}
		}
	case plumbing.HashReference:
		if previousHEADHash != plumbing.ZeroHash {
			if err := repo.Storer.SetReference(plumbing.NewHashReference(plumbing.HEAD, previousHEADHash)); err != nil {
				return fmt.Errorf("failed to restore HEAD hash ref: %w", err)
			}
		}
	default:
		// Unknown/zero: skip
	}

	return nil
}

// addCommitEventToStream adds a commit event to Redis Stream
func (s *CommitsService) addCommitEventToStream(ctx context.Context, repo *repositories.RepositoryWrapper, oldHash string, commitHash plumbing.Hash, refName, owner, repoName string) {
	if s.eventsClient == nil {
		return
	}

	// Get commit object to verify it exists
	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		// Not a commit, skip
		return
	}

	// Get repository path from the wrapper
	repoPath := repo.Path()
	if repoPath == "" {
		// Skip if we can't get the path
		return
	}

	// Convert to relative path (work_space_name/uuid.git format)
	// Use default RepoRoot if not available
	defaultRepoRoot := "/var/lib/git/repos"
	relativePath := events.GetRelativeRepoPath(repoPath, defaultRepoRoot)

	// Normalize ref name
	if !strings.HasPrefix(refName, "refs/") {
		refName = "refs/heads/" + refName
	}

	// Create event
	event := &events.CommitEvent{
		RepoPath:   relativePath,
		EventType:  events.EventTypeCommit,
		CommitHash: commitHash.String(),
		OldRev:     oldHash,
		RefName:    refName,
		Pusher:     "git", // Default pusher identifier
		Timestamp:  commit.Committer.When,
	}

	// Publish asynchronously to avoid blocking
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := s.eventsClient.AddCommitEventToStream(ctx, event); err != nil {
			s.logger.Error("Failed to publish commit event",
				zap.String("repo_path", repoPath),
				zap.String("commit_hash", commitHash.String()),
				zap.String("event_type", string(event.EventType)),
				zap.Error(err))
		}
	}()
}
