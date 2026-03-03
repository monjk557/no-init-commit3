package commits

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
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

// getCommitHistory는 커밋 히스토리를 조회합니다.
// compare mode일 경우 mergeBase SHA를 함께 반환합니다.
func (h *CommitsHandler) getCommitHistory(ctx context.Context, repoPath string, params *CommitsRequestParams) ([]Commit, int, string, error) {
	requestLimit := params.Limit + 1

	// Compare mode: source_ref/target_ref가 모두 제공된 경우
	if params.SourceRef != "" || params.TargetRef != "" {
		if params.SourceRef == "" || params.TargetRef == "" {
			return nil, 0, "", fmt.Errorf("both source_ref and target_ref must be provided together")
		}

		commits, mergeBase, err := GetCommitsFromMergeBase(ctx, repoPath, params.SourceRef, params.TargetRef, requestLimit, params.Skip)
		if err != nil {
			return nil, 0, "", fmt.Errorf("failed to get commits (merge-base): %w", err)
		}

		totalCount := len(commits)
		return commits, totalCount, mergeBase, nil
	}

	commits, err := GetCommits(ctx, repoPath, params.Ref, requestLimit, params.Skip)
	if err != nil {
		return nil, 0, "", fmt.Errorf("failed to get commits: %w", err)
	}

	totalCount := len(commits)
	return commits, totalCount, "", nil
}

// convertGitCommitToMetadata는 Commit을 CommitMetadata로 변환합니다.
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
	owner := c.Param("owner")
	repo := c.Param("repo")

	ref := c.Query("ref")
	sourceRef := c.Query("source_ref")
	targetRef := c.Query("target_ref")
	limitStr := c.Query("limit")
	skipStr := c.Query("skip")

	if owner == "" || repo == "" {
		return nil, fmt.Errorf("missing required parameters: owner or repo")
	}

	if ref == "" {
		ref = "HEAD"
	}

	if (sourceRef != "" && targetRef == "") || (sourceRef == "" && targetRef != "") {
		return nil, fmt.Errorf("both source_ref and target_ref must be provided together")
	}

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
		Owner:     owner,
		Repo:      repo,
		Ref:       ref,
		SourceRef: sourceRef,
		TargetRef: targetRef,
		Limit:     limit,
		Skip:      skip,
	}, nil
}

// GetCommitsFromMergeBase returns commits after merge-base up to source_ref (merge base exclusive).
func GetCommitsFromMergeBase(
	ctx context.Context,
	repoPath string,
	sourceRef string,
	targetRef string,
	limit int,
	skip int,
) ([]Commit, string, error) {
	select {
	case <-ctx.Done():
		return nil, "", ctx.Err()
	default:
	}

	if sourceRef == "" || targetRef == "" {
		return nil, "", fmt.Errorf("source_ref and target_ref are required")
	}

	// Resolve refs to hashes
	sourceHash, err := nativeResolveRef(ctx, repoPath, sourceRef)
	if err != nil {
		return nil, "", fmt.Errorf("failed to resolve source_ref '%s': %w", sourceRef, err)
	}
	targetHash, err := nativeResolveRef(ctx, repoPath, targetRef)
	if err != nil {
		return nil, "", fmt.Errorf("failed to resolve target_ref '%s': %w", targetRef, err)
	}

	// Find merge base
	mergeBase, err := nativeMergeBase(ctx, repoPath, sourceHash, targetHash)
	if err != nil {
		return nil, "", fmt.Errorf("failed to find merge base: %w", err)
	}

	// Get commits from source excluding ancestors of merge-base
	// git log source ^mergeBase with limit and skip
	commits, err := nativeGetCommitLog(ctx, repoPath, sourceHash, limit, skip, mergeBase)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get commit log: %w", err)
	}

	return commits, mergeBase, nil
}

// groupCommitsByDate는 커밋을 날짜별로 그룹화합니다.
func (h *CommitsHandler) groupCommitsByDate(commits []Commit) []CommitsByDate {
	if len(commits) == 0 {
		return []CommitsByDate{}
	}

	dateMap := make(map[string][]CommitMetadata)

	for _, commit := range commits {
		dateStr := commit.Author.Date.Format("2006-01-02")
		commitMeta := h.convertGitCommitToMetadata(commit)
		dateMap[dateStr] = append(dateMap[dateStr], commitMeta)
	}

	var result []CommitsByDate
	for dateStr, commits := range dateMap {
		result = append(result, CommitsByDate{
			Date:    dateStr,
			Commits: commits,
		})
	}

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

// GetCommits는 페이지네이션을 사용하여 커밋 히스토리를 조회합니다.
func GetCommits(ctx context.Context, repoPath string, ref string, limit, skip int) ([]Commit, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	commits, err := nativeGetCommitLog(ctx, repoPath, ref, limit, skip, "")
	if err != nil {
		return nil, err
	}

	return commits, nil
}

// CommitCountResult holds the result of a commit count operation.
type CommitCountResult struct {
	Count       int
	Approximate bool
}

// CountCommits는 특정 ref의 전체 커밋 개수를 세는 함수입니다.
func CountCommits(ctx context.Context, repoPath string, ref string) (int, error) {
	r, err := CountCommitsWithCap(ctx, repoPath, ref, 0)
	if err != nil {
		return 0, err
	}
	return r.Count, nil
}

// CountCommitsWithCap counts commits up to maxCount. If maxCount <= 0, no cap is applied.
func CountCommitsWithCap(ctx context.Context, repoPath string, ref string, maxCount int) (CommitCountResult, error) {
	select {
	case <-ctx.Done():
		return CommitCountResult{}, ctx.Err()
	default:
	}

	count, err := nativeRevListCount(ctx, repoPath, ref)
	if err != nil {
		return CommitCountResult{}, err
	}

	if maxCount > 0 && count >= maxCount {
		return CommitCountResult{Count: maxCount, Approximate: true}, nil
	}

	return CommitCountResult{Count: count, Approximate: false}, nil
}

// convertToFileChange는 MultiFileOperation을 FileChange로 변환합니다
func convertToFileChange(op *MultiFileOperation) (*FileChange, error) {
	fileChange := &FileChange{
		Path:         op.Path,
		Operation:    FileOperation(op.Operation),
		OriginalPath: op.OriginalPath,
	}

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

	if op.Mode != "" {
		switch op.Mode {
		case "100644", "644":
			mode := "100644"
			fileChange.Mode = &mode
		case "100755", "755":
			mode := "100755"
			fileChange.Mode = &mode
		default:
			return nil, fmt.Errorf("unsupported file mode: %s", op.Mode)
		}
	}

	return fileChange, nil
}

// ExecuteMultiFileCommit creates a multi-file commit using a single git fast-import process.
//
// Optimized pipeline (replaces the previous 7+3N+D process approach):
//  1. Lock acquisition + branch/ref resolution (2-3 git processes)
//  2. Single nativeLsTree call for all file validations (1 process, replaces N per-file calls)
//  3. Single git fast-import process for blob+tree+commit+ref-update (1 process, replaces N+D+2)
//  4. Event publishing (1 process)
//
// Total: ~5 git processes regardless of file count (was 7+3N+D).
func (s *CommitsService) ExecuteMultiFileCommit(ctx context.Context, repoPath string, req *MultiFileCommitRequest, branch string, owner, repo, userID string) (*MultiFileCommitResponse, error) {
	start := time.Now()
	s.logger.Info("commit: start",
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.String("branch", branch),
		zap.Int("operations", len(req.Operations)),
		zap.String("message", req.Message),
	)

	// Serialize pack writes per repo to prevent concurrent write panics.
	repoLock := repositories.GetRepoRWLock(repoPath)
	lockWaitStart := time.Now()
	repoLock.Lock()
	s.logger.Info("commit: lock acquired",
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.Duration("lock_wait", time.Since(lockWaitStart)),
	)
	defer repoLock.Unlock()

	// Phase 1: Resolve branch
	if branch == "" {
		symRef, _ := nativeSymbolicRef(ctx, repoPath)
		if symRef != "" && strings.HasPrefix(symRef, "refs/heads/") {
			branch = strings.TrimPrefix(symRef, "refs/heads/")
		}
		if branch == "" {
			branch = "main"
		}
	}

	// Phase 2: Resolve base commit hash
	baseHash, err := nativeResolveRef(ctx, repoPath, "refs/heads/"+branch)
	if err != nil {
		baseHash, err = nativeResolveRef(ctx, repoPath, "HEAD")
		if err != nil {
			hasHeads, _ := nativeHasRefs(ctx, repoPath, "refs/heads/")
			if hasHeads {
				return nil, fmt.Errorf("branch '%s' not found", branch)
			}

			defaultBranch := "main"
			symRef, _ := nativeSymbolicRef(ctx, repoPath)
			if symRef != "" && strings.HasPrefix(symRef, "refs/heads/") {
				defaultBranch = strings.TrimPrefix(symRef, "refs/heads/")
			}
			if branch != "" && branch != defaultBranch {
				return nil, fmt.Errorf("first commit must use default branch '%s'", defaultBranch)
			}
			if branch == "" {
				branch = defaultBranch
			}
			baseHash = ""
		}
	}

	s.logger.Info("commit: base resolved",
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.String("branch", branch),
		zap.String("base_hash", baseHash),
		zap.Duration("elapsed", time.Since(start)),
	)

	// Phase 3: Get base tree hash (single call — not duplicated)
	var baseTreeHash string
	if baseHash != "" {
		baseTreeHash, err = nativeGetTreeHash(ctx, repoPath, baseHash)
		if err != nil {
			return nil, fmt.Errorf("commit not found")
		}
	}

	// Phase 4: Convert operations to FileChange list
	operationResults := make([]OperationResult, len(req.Operations))
	var fileChanges []FileChange
	for i, op := range req.Operations {
		fileChange, err := convertToFileChange(&op)
		if err != nil {
			operationResults[i] = OperationResult{
				Path:      op.Path,
				Operation: Operation(op.Operation),
				Success:   false,
				Error:     err.Error(),
			}
			return nil, fmt.Errorf("operation %d failed: %w", i, err)
		}
		fileChanges = append(fileChanges, *fileChange)
		operationResults[i] = OperationResult{
			Path:      op.Path,
			Operation: Operation(op.Operation),
			Success:   true,
		}
	}

	// Phase 5: Validate all operations in a single ls-tree call
	// Replaces: per-file nativeLsTreePath × N + ValidateTransaction nativeLsTreePath × N
	// Total: 1 git process instead of 2N
	validateStart := time.Now()
	if err := PreValidateChanges(ctx, repoPath, baseTreeHash, fileChanges); err != nil {
		return nil, err
	}
	s.logger.Info("commit: validated",
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.Int("operations", len(fileChanges)),
		zap.Duration("validate_dur", time.Since(validateStart)),
		zap.Duration("elapsed", time.Since(start)),
	)

	// Phase 6: Prepare commit metadata
	if req.Author == nil {
		req.Author = &CommitAuthor{
			Name:  "Git API",
			Email: "api@git-executor.local",
		}
	}
	if req.Committer == nil {
		req.Committer = req.Author
	}

	var parents []string
	if baseHash != "" {
		parents = []string{baseHash}
	}

	now := time.Now()
	commitInfo := CommitInfo{
		Message: req.Message,
		Author: NativeSignature{
			Name:  req.Author.Name,
			Email: req.Author.Email,
			When:  now,
		},
		Committer: &NativeSignature{
			Name:  req.Committer.Name,
			Email: req.Committer.Email,
			When:  now,
		},
		Parents: parents,
	}

	// Check for validate-only / dry-run mode
	validateOnly := false
	if req.Options != nil && (req.Options.ValidateOnly || req.Options.DryRun) {
		validateOnly = true
	}

	if validateOnly {
		s.logger.Info("commit: dry-run complete",
			zap.String("owner", owner),
			zap.String("repo", repo),
			zap.Duration("elapsed", time.Since(start)),
		)
		return &MultiFileCommitResponse{
			Success:          true,
			Message:          req.Message,
			TotalOperations:  len(req.Operations),
			SuccessfulOps:    len(req.Operations),
			OperationResults: operationResults,
			ValidationOnly:   true,
			Duration:         time.Since(start),
		}, nil
	}

	// Phase 7: Execute fast-import — single process creates blobs + tree + commit + ref update
	// Replaces: hash-object × N + mktree × D + commit-tree + update-ref = N+D+2 processes → 1 process
	fastImportStart := time.Now()
	s.logger.Info("commit: fast-import start",
		zap.String("owner", owner),
		zap.String("repo", repo),
	)

	fiResult, err := ExecuteFastImportCommit(ctx, &FastImportCommitRequest{
		RepoPath:     repoPath,
		Branch:       branch,
		BaseTreeHash: baseTreeHash,
		Changes:      fileChanges,
		CommitInfo:   commitInfo,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create commit: %w", err)
	}

	s.logger.Info("commit: fast-import done",
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.String("commit_hash", fiResult.CommitHash),
		zap.Duration("fast_import_dur", time.Since(fastImportStart)),
		zap.Duration("elapsed", time.Since(start)),
	)

	// Phase 8: Post-commit — event publishing
	if s.eventsClient != nil && owner != "" && repo != "" {
		s.addCommitEventToStream(ctx, repoPath, baseHash, fiResult.CommitHash, branch, owner, repo, userID)
	}

	// Phase 9: Invalidate cache
	repositories.GetSimpleCache().Invalidate(repoPath)

	// Phase 10: Build response
	response := MultiFileCommitResponse{
		Success:          true,
		CommitSHA:        fiResult.CommitHash,
		Message:          req.Message,
		TotalOperations:  len(req.Operations),
		SuccessfulOps:    len(req.Operations),
		OperationResults: operationResults,
		Duration:         time.Since(start),
	}

	s.logger.Info("commit: done",
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.String("branch", branch),
		zap.String("commit_sha", response.CommitSHA),
		zap.Bool("success", true),
		zap.Int("operations", len(req.Operations)),
		zap.Duration("total", time.Since(start)),
	)

	// Loose object 누적 방지: git gc --auto 비동기 실행
	go func() {
		gcCtx, gcCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer gcCancel()
		cmd := exec.CommandContext(gcCtx, "git", "--git-dir", repoPath, "gc", "--auto")
		if err := cmd.Run(); err != nil {
			s.logger.Debug("git gc --auto failed (non-critical)", zap.String("repo", repoPath), zap.Error(err))
		}
	}()

	return &response, nil
}

// ExecuteMultiFileCommitOrCreateBranch는 create_branch_name이 있으면 브랜치를 만든 후 커밋을 생성합니다.
func (s *CommitsService) ExecuteMultiFileCommitOrCreateBranch(
	ctx context.Context,
	repoPath string,
	req *MultiFileCommitRequest,
	owner, repo, userID string,
) (*MultiFileCommitResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("request is required")
	}

	targetBranch := req.Branch
	if targetBranch == "" {
		targetBranch = "main"
	}

	// create_branch_name이 없으면 기존 branch에 커밋 생성
	if req.CreateBranchName == "" {
		return s.ExecuteMultiFileCommit(ctx, repoPath, req, targetBranch, owner, repo, userID)
	}

	// create_branch_name이 있으면 source를 결정하고 브랜치 생성 후 그 브랜치로 커밋
	sourceRef := req.SourceBranchName
	if sourceRef == "" {
		if req.Branch != "" {
			sourceRef = req.Branch
		} else {
			sourceRef = "HEAD"
		}
	}

	// Check if branch already exists
	branchRefName := fmt.Sprintf("refs/heads/%s", req.CreateBranchName)
	exists, err := nativeCheckRefExists(ctx, repoPath, branchRefName)
	if err != nil {
		return nil, fmt.Errorf("failed to check branch existence: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("branch '%s' already exists", req.CreateBranchName)
	}

	// Resolve source and create branch
	if err := createBranchFromSource(ctx, repoPath, req.CreateBranchName, sourceRef); err != nil {
		return nil, err
	}

	commitReq := *req
	commitReq.Branch = req.CreateBranchName
	resp, err := s.ExecuteMultiFileCommit(ctx, repoPath, &commitReq, commitReq.Branch, owner, repo, userID)
	if err != nil {
		// Rollback: delete the branch we just created
		_ = nativeDeleteRef(ctx, repoPath, branchRefName)
		return nil, err
	}

	return resp, nil
}

// resolveSourceReference resolves a source reference using native git.
func resolveSourceReference(ctx context.Context, repoPath, source string) (string, error) {
	// Try direct resolve (handles commit hashes, branch names, tags, HEAD)
	hash, err := nativeResolveRef(ctx, repoPath, source)
	if err == nil {
		return hash, nil
	}

	// Try as branch reference
	hash, err = nativeResolveRef(ctx, repoPath, "refs/heads/"+source)
	if err == nil {
		return hash, nil
	}

	// Try as tag reference (dereference annotated tags)
	hash, err = nativeResolveRef(ctx, repoPath, "refs/tags/"+source+"^{commit}")
	if err == nil {
		return hash, nil
	}

	// Try plain tag
	hash, err = nativeResolveRef(ctx, repoPath, "refs/tags/"+source)
	if err == nil {
		return hash, nil
	}

	return "", fmt.Errorf("unable to resolve reference '%s'", source)
}

func createBranchFromSource(ctx context.Context, repoPath, branchName, source string) error {
	if branchName == "" {
		return fmt.Errorf("branch name is required")
	}
	if source == "" {
		source = "HEAD"
	}

	sourceCommit, err := resolveSourceReference(ctx, repoPath, source)
	if err != nil {
		return fmt.Errorf("failed to resolve source reference '%s': %w", source, err)
	}

	refPath := fmt.Sprintf("refs/heads/%s", branchName)
	if err := nativeUpdateRef(ctx, repoPath, refPath, sourceCommit); err != nil {
		return fmt.Errorf("failed to create branch: %w", err)
	}

	return nil
}

// addCommitEventToStream adds a commit event to Redis Stream
func (s *CommitsService) addCommitEventToStream(ctx context.Context, repoPath, oldHash, commitHash, refName, owner, repoName, userID string) {
	if s.eventsClient == nil {
		return
	}

	// Get commit info to verify it exists
	commit, err := nativeGetCommitInfo(ctx, repoPath, commitHash)
	if err != nil {
		return
	}

	// Convert to relative path
	defaultRepoRoot := "/var/lib/git/repos"
	relativePath := events.GetRelativeRepoPath(repoPath, defaultRepoRoot)

	// Normalize ref name
	if !strings.HasPrefix(refName, "refs/") {
		refName = "refs/heads/" + refName
	}

	event := &events.CommitEvent{
		RepoPath:      relativePath,
		EventType:     events.EventTypeCommit,
		CommitHash:    commitHash,
		OldRev:        oldHash,
		RefName:       refName,
		Pusher:        "git",
		UserID:        userID,
		CommitMessage: commit.Message,
		Timestamp:     commit.Committer.Date,
	}

	s.logger.Debug("Creating commit event with user context",
		zap.String("user_id", userID),
		zap.String("commit_hash", commitHash),
		zap.String("ref_name", refName),
		zap.String("owner", owner),
		zap.String("repo", repoName))

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := s.eventsClient.AddCommitEventToStream(ctx, event); err != nil {
			s.logger.Error("Failed to publish commit event",
				zap.String("repo_path", repoPath),
				zap.String("commit_hash", commitHash),
				zap.String("event_type", string(event.EventType)),
				zap.Error(err))
		}
	}()
}
