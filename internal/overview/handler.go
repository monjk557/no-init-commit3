package overview

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/blob"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/lfs"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/observability"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type RepositoryOverviewHandler struct {
	config        *config.Config
	logger        *logging.Logger
	pathConfig    *repositories.PathConfig
	pathValidator *repositories.GitAPIPathValidator
	repoManager   *repositories.DefaultRepositoryManager
	blobService   *blob.BlobService
	service       *RepositoryOverviewService
	overviewCache *OverviewCache
	commitIndex   *CommitIndex
}

func NewRepositoryOverviewHandler(cfg *config.Config) (*RepositoryOverviewHandler, error) {
	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	pathValidator := repositories.NewGitAPIPathValidator(pathConfig)

	repoManager, err := repositories.NewRepositoryManager(pathConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create repository manager: %w", err)
	}

	blobService := blob.NewBlobService()
	service, err := NewRepositoryOverviewService(logger, pathConfig, repoManager, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create overview service: %w", err)
	}

	overviewCache := NewOverviewCache(cfg.Git.MaxCacheSize, cfg.Git.CacheTTL)
	commitIndex := NewCommitIndex(cfg.Git.MaxCacheSize, service, logger)

	return &RepositoryOverviewHandler{
		config:        cfg,
		logger:        logger,
		pathConfig:    pathConfig,
		pathValidator: pathValidator,
		repoManager:   repoManager,
		blobService:   blobService,
		service:       service,
		overviewCache: overviewCache,
		commitIndex:   commitIndex,
	}, nil
}

// repositoryContext는 저장소 관련 공통 컨텍스트를 담는 구조체입니다.
type repositoryContext struct {
	owner       string
	repo        string
	ref         string
	path        string
	repoPath    string
	repoWrapper *repositories.RepositoryWrapper
}

// validateAndOpenRepository는 저장소 검증 및 열기를 수행한다. 성공 시 RLock 을
// 보유한 채 컨텍스트를 반환 — 호출자는 RUnlock + repoWrapper.Close 책임. 실패
// 시 *typed.HTTPError 반환 (lock/repo open 모두 정리됨).
func (h *RepositoryOverviewHandler) validateAndOpenRepository(_ context.Context, owner, repo, ref string) (*repositoryContext, error) {
	if ref == "" {
		ref = "HEAD"
	}

	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo)); err != nil {
		return nil, api.NewCoded(api.ErrCodeRepositoryNotFound)
	}

	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return nil, api.NewCoded(api.ErrCodeRepositoryNotFound)
	}

	repoLock := repositories.GetRepoRWLock(repoPath)
	repoLock.RLock()

	repoWrapper, err := repositories.OpenRepositoryNoCache(repoPath)
	if err != nil {
		repoLock.RUnlock()
		return nil, api.NewCoded(api.ErrCodeRepositoryNotFound)
	}

	if ref != "" && ref != "HEAD" {
		_, err := repoWrapper.ResolveReference(ref)
		if err != nil {
			repoLock.RUnlock()
			_ = repoWrapper.Close()
			return nil, api.NewCoded(api.ErrCodeRefNotFound)
		}
	}

	return &repositoryContext{
		owner:       owner,
		repo:        repo,
		ref:         ref,
		repoPath:    repoPath,
		repoWrapper: repoWrapper,
	}, nil
}

// validateRepositoryNative는 native git만 사용하여 저장소 검증을 수행한다.
// 성공 시 RLock 을 보유한 채 컨텍스트를 반환 — 호출자는 RUnlock 책임. 실패
// 시 *typed.HTTPError 반환 (lock 정리됨).
func (h *RepositoryOverviewHandler) validateRepositoryNative(ctx context.Context, owner, repo, ref string) (*repositoryContext, error) {
	if ref == "" {
		ref = "HEAD"
	}

	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo)); err != nil {
		return nil, api.NewCoded(api.ErrCodeRepositoryNotFound)
	}

	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return nil, api.NewCoded(api.ErrCodeRepositoryNotFound)
	}

	repoLock := repositories.GetRepoRWLock(repoPath)
	repoLock.RLock()

	if err := nativeCheckRepoExists(ctx, repoPath); err != nil {
		repoLock.RUnlock()
		return nil, api.NewCoded(api.ErrCodeRepositoryNotFound)
	}

	if ref != "" && ref != "HEAD" {
		if _, err := nativeResolveRef(ctx, repoPath, ref); err != nil {
			repoLock.RUnlock()
			return nil, api.NewCoded(api.ErrCodeRefNotFound)
		}
	}

	return &repositoryContext{
		owner:    owner,
		repo:     repo,
		ref:      ref,
		repoPath: repoPath,
	}, nil
}

// overviewTimeout is the hard request-level timeout for the overview handler.
const overviewTimeout = 15 * time.Second

// overviewCommitCountCap is the maximum number of commits to iterate when counting.
const overviewCommitCountCap = 100_000

// overviewResult holds the result of running overview parallel tasks (for retry).
type overviewResult struct {
	commitsCount          int
	commitsApproximate    bool
	treeEntriesWithCommit []GitTreeEntryWithCommit
	treeErr               error
	nativeTagsList        []GitTag
	branchesCount         int
	readmeContent         string
}

// runOverviewTasks는 native git CLI를 사용하여 overview 병렬 태스크를 실행합니다.
func (h *RepositoryOverviewHandler) runOverviewTasks(ctx context.Context, repoPath, ref, path, refHash string) overviewResult {
	return h.runOverviewTasksNative(ctx, repoPath, ref, path, refHash)
}

// runOverviewTasksNative runs parallel overview sub-tasks using native git CLI.
func (h *RepositoryOverviewHandler) runOverviewTasksNative(
	ctx context.Context, repoPath, ref, path, refHash string,
) overviewResult {
	var (
		mu  sync.Mutex
		res overviewResult
		wg  sync.WaitGroup
	)

	wg.Add(1)
	go func() {
		defer wg.Done()
		if refHash != "" {
			if cached, ok := h.overviewCache.GetCommitCount(repoPath, refHash); ok {
				mu.Lock()
				res.commitsCount = cached
				mu.Unlock()
				return
			}
		}
		count, approx, err := NativeCountCommits(ctx, repoPath, ref)
		mu.Lock()
		if err != nil {
			h.logger.Warn("Native count commits failed, using fallback", zap.Error(err))
			res.commitsCount = 1
		} else {
			res.commitsCount = count
			res.commitsApproximate = approx
			if refHash != "" && !approx {
				h.overviewCache.SetCommitCount(repoPath, refHash, count)
			}
		}
		mu.Unlock()
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		entries, err := NativeGetTreeEntries(ctx, repoPath, ref, path)
		mu.Lock()
		res.treeEntriesWithCommit = entries
		res.treeErr = err
		mu.Unlock()
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		tl, err := NativeGetTags(ctx, repoPath, 100)
		mu.Lock()
		if err != nil {
			res.nativeTagsList = []GitTag{}
		} else {
			res.nativeTagsList = tl
		}
		mu.Unlock()
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		n, err := NativeCountBranches(ctx, repoPath)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			h.logger.Debug("branch count unavailable, defaulting to 0", zap.Error(err))
			return
		}
		res.branchesCount = n
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		readmeData, err := NativeGetBlobContent(ctx, repoPath, ref, "README.md")
		mu.Lock()
		if err == nil {
			const maxReadmeSize = 100 * 1024
			if len(readmeData) > maxReadmeSize {
				res.readmeContent = string(readmeData[:maxReadmeSize])
			} else {
				res.readmeContent = string(readmeData)
			}
		}
		mu.Unlock()
	}()

	wg.Wait()
	return res
}

// isObjectNotFoundForPack returns true if the result suggests object-not-found due to pack not yet visible (e.g. after push).
func isObjectNotFoundForPack(res overviewResult) bool {
	if res.treeErr != nil && strings.Contains(strings.ToLower(res.treeErr.Error()), "object not found") {
		return true
	}
	return false
}

// HandleGetRepositoryOverview는 저장소 개요를 위한 GET /overview 요청을 처리한다.
//
// pack 미반영 시 object not found 가 나올 수 있으므로 캐시 무효화 후 한 번
// 재시도. validateRepositoryNative 가 RLock 획득을 책임지고, 실패 case 는
// *typed.HTTPError 로 즉시 반환.
func (h *RepositoryOverviewHandler) HandleGetRepositoryOverview(c *gin.Context) (RepositoryOverviewResponse, error) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), overviewTimeout)
	defer cancel()

	owner := c.Param("owner")
	repo := c.Param("repo")
	ref := c.Query("ref")
	path := c.Query("path")

	var repoCtx *repositoryContext
	var res overviewResult
	refHash := ""
	repoPath := ""

	for attempt := 0; attempt < 2; attempt++ {
		if attempt == 1 {
			repositories.GetSimpleCache().Invalidate(repoPath)
			h.logger.Debug("Overview retry after cache invalidate (pack visibility)", zap.String("repoPath", repoPath))
		}

		var verr error
		repoCtx, verr = h.validateRepositoryNative(ctx, owner, repo, ref)
		if verr != nil {
			return RepositoryOverviewResponse{}, verr
		}
		repoPath = repoCtx.repoPath

		refHash = ""
		if resolved, resolveErr := nativeResolveRef(ctx, repoPath, repoCtx.ref); resolveErr == nil {
			refHash = resolved
		}

		res = h.runOverviewTasks(ctx, repoPath, repoCtx.ref, path, refHash)

		if !isObjectNotFoundForPack(res) {
			break
		}

		repositories.GetRepoRWLock(repoCtx.repoPath).RUnlock()
		repoCtx = nil
	}

	if repoCtx != nil {
		defer repositories.GetRepoRWLock(repoCtx.repoPath).RUnlock()
	}

	if refHash == "" {
		return RepositoryOverviewResponse{
			Success:    true,
			Message:    "Repository overview fetched successfully",
			IsEmpty:    true,
			Repository: fmt.Sprintf("%s/%s", owner, repo),
			Owner:      owner,
			Name:       repo,
			Path:       repoPath,
			Tree:       []GitTreeEntryWithCommit{},
			Branches:   []GitBranch{},
			Tags:       []GitTag{},
			Count:      RepositoryCount{},
			Readme:     "",
		}, nil
	}

	if res.treeErr != nil {
		errMsg := strings.ToLower(res.treeErr.Error())
		h.logger.Error("overview: tree failed",
			zap.String("owner", owner),
			zap.String("repo", repo),
			zap.String("ref", ref),
			zap.String("path", path),
			zap.Error(res.treeErr),
		)
		if strings.Contains(errMsg, "not a tree object") ||
			strings.Contains(errMsg, "does not exist") ||
			strings.Contains(errMsg, "path not found") {
			return RepositoryOverviewResponse{}, api.NewCoded(api.ErrCodeFileNotFound, "path not found")
		}
		return RepositoryOverviewResponse{}, api.NewCoded(api.ErrCodeRepositoryCorrupted)
	}

	return RepositoryOverviewResponse{
		Success:    true,
		Message:    "Repository overview fetched successfully",
		Repository: fmt.Sprintf("%s/%s", owner, repo),
		Owner:      owner,
		Name:       repo,
		Path:       repoPath,
		Tree:       res.treeEntriesWithCommit,
		Branches:   []GitBranch{},
		Tags:       res.nativeTagsList,
		Count: RepositoryCount{
			Commits:            res.commitsCount,
			Branches:           res.branchesCount,
			Tags:               len(res.nativeTagsList),
			Total:              res.commitsCount + res.branchesCount + len(res.nativeTagsList),
			CommitsApproximate: res.commitsApproximate,
		},
		Readme: res.readmeContent,
	}, nil
}

// HandleGetTreeOverview는 트리 개요를 위한 GET /overview/tree 요청을 처리합니다.
func (h *RepositoryOverviewHandler) HandleGetTreeOverview(c *gin.Context) (TreeOverviewResponse, error) {
	ctx := c.Request.Context()
	owner := c.Param("owner")
	repo := c.Param("repo")
	ref := c.Query("ref")
	path := c.Query("path")

	repoCtx, err := h.validateRepositoryNative(ctx, owner, repo, ref)
	if err != nil {
		return TreeOverviewResponse{}, err
	}
	defer repositories.GetRepoRWLock(repoCtx.repoPath).RUnlock()

	treeEntries, err := NativeGetTreeEntries(ctx, repoCtx.repoPath, repoCtx.ref, path)
	if err != nil {
		return TreeOverviewResponse{}, api.NewCoded(api.ErrCodeRepositoryCorrupted)
	}

	var latestCommit *GitCommit
	if path != "" {
		latestCommit, _ = NativeGetLatestCommitForPath(ctx, repoCtx.repoPath, repoCtx.ref, path)
	} else {
		latestCommit, _ = NativeGetLatestCommit(ctx, repoCtx.repoPath, repoCtx.ref)
	}

	var commitsList []GitCommit
	if c.Query("include_all_commits") == "true" {
		const maxCommitsForList = 1000
		commitsList, err = NativeGetCommits(ctx, repoCtx.repoPath, repoCtx.ref, maxCommitsForList)
		if err != nil {
			h.logger.Warn("Failed to get all commits for tree overview", zap.Error(err))
			commitsList = []GitCommit{}
		}
	} else if latestCommit != nil {
		commitsList = []GitCommit{*latestCommit}
	}

	if commitsList == nil {
		commitsList = []GitCommit{}
	}

	return TreeOverviewResponse{
		Success:      true,
		Repository:   fmt.Sprintf("%s/%s", owner, repo),
		Owner:        owner,
		Name:         repo,
		Path:         path,
		Tree:         treeEntries,
		Branches:     []GitBranch{},
		LatestCommit: latestCommit,
		Commits:      commitsList,
	}, nil
}

// HandleGetBlobOverview는 GET /overview/blob 요청을 처리합니다.
func (h *RepositoryOverviewHandler) HandleGetBlobOverview(c *gin.Context) (BlobOverviewResponse, error) {
	ctx := c.Request.Context()
	owner := c.Param("owner")
	repo := c.Param("repo")
	path := c.Query("path")
	ref := c.Query("ref")

	if path == "" {
		return BlobOverviewResponse{}, api.NewCoded(api.ErrCodeInvalidInput, "Missing required parameter: path")
	}

	repoCtx, err := h.validateRepositoryNative(ctx, owner, repo, ref)
	if err != nil {
		return BlobOverviewResponse{}, err
	}
	defer repositories.GetRepoRWLock(repoCtx.repoPath).RUnlock()

	blobContent, err := NativeGetBlobContent(ctx, repoCtx.repoPath, repoCtx.ref, path)
	if err != nil {
		return BlobOverviewResponse{}, api.NewCoded(api.ErrCodeFileNotFound)
	}

	contentType := blob.DetectContentType(blobContent, path)
	isBinary := blob.IsBinaryContent(blobContent, contentType)

	// LFS pointer 감지 — pointer 일 때 Content/Lines 비우고 LFS 메타데이터를
	// 응답에 첨부하여 client 가 raw /blob 응답의 LFS JSON 분기에 의존하지
	// 않도록 한다 (Issue-2327).
	var (
		isLFSPointer bool
		lfsInfo      *LFSPointerInfo
	)
	if lfs.IsLFSPointer(blobContent) {
		if oid, size, ok := lfs.ParseLFSPointer(blobContent); ok {
			isLFSPointer = true
			lfsInfo = &LFSPointerInfo{Oid: oid, Size: size}
		}
	}

	parentPath := ""
	if idx := strings.LastIndex(path, "/"); idx != -1 {
		parentPath = path[:idx]
	}
	treeEntries, err := NativeGetTreeEntries(ctx, repoCtx.repoPath, repoCtx.ref, parentPath)
	if err != nil {
		return BlobOverviewResponse{}, api.NewCoded(api.ErrCodeRepositoryCorrupted)
	}

	latestCommit, _ := NativeGetLatestCommitForPath(ctx, repoCtx.repoPath, repoCtx.ref, path)

	var commitsList []GitCommit
	if c.Query("include_all_commits") == "true" {
		const maxCommitsForList = 1000
		commitsList, err = NativeGetCommits(ctx, repoCtx.repoPath, repoCtx.ref, maxCommitsForList)
		if err != nil {
			h.logger.Warn("Failed to get all commits for blob overview", zap.Error(err))
			commitsList = []GitCommit{}
		}
	} else if latestCommit != nil {
		commitsList = []GitCommit{*latestCommit}
	}

	if commitsList == nil {
		commitsList = []GitCommit{}
	}

	var content string
	var lines int
	// LFS pointer 면 raw pointer 텍스트를 노출하지 않는다 — client 는
	// IsLFSPointer + LFS 필드만 보고 처리.
	if !isBinary && !isLFSPointer {
		content = string(blobContent)
		lines = strings.Count(strings.ReplaceAll(content, "\r\n", "\n"), "\n") + 1
	}

	return BlobOverviewResponse{
		Success:      true,
		IsBinary:     isBinary,
		IsLFSPointer: isLFSPointer,
		LFS:          lfsInfo,
		Content:      content,
		ContentType:  contentType,
		Lines:        lines,
		Size:         int64(len(blobContent)),
		Tree:         treeEntries,
		Branches:     []GitBranch{},
		LatestCommit: latestCommit,
		Commits:      commitsList,
	}, nil
}

// HandleGetTreeWithCommitOverview handles GET /overview/tree/commits.
//
// query 는 typed.RegisterGetWithQuery wrapper 가 TreeWithCommitOverviewRequest
// 로 bind 한 뒤 본 함수에 전달한다 — wrapper 가 spec generator 에 ref/path 를
// in: query 로 노출 (Issue-2317 후속).
func (h *RepositoryOverviewHandler) HandleGetTreeWithCommitOverview(c *gin.Context, req TreeWithCommitOverviewRequest) (TreeWithCommitOverviewResponse, error) {
	ctx := c.Request.Context()
	start := time.Now()
	m := observability.GetApplicationMetrics()

	owner := c.Param("owner")
	repo := c.Param("repo")
	ref := req.Ref
	path := req.Path

	repoCtx, err := h.validateRepositoryNative(ctx, owner, repo, ref)
	if err != nil {
		return TreeWithCommitOverviewResponse{}, err
	}
	defer repositories.GetRepoRWLock(repoCtx.repoPath).RUnlock()

	repoCtx.path = path

	treeEntries, err := NativeGetTreeEntries(ctx, repoCtx.repoPath, repoCtx.ref, repoCtx.path)
	if err != nil {
		m.RecordTreeWithCommit("error", time.Since(start), 0)
		return TreeWithCommitOverviewResponse{}, api.NewCoded(api.ErrCodeFileNotFound, "path not found")
	}

	paths := make([]string, 0, len(treeEntries))
	for _, e := range treeEntries {
		if e.Type == "blob" || e.Type == "tree" {
			paths = append(paths, e.Path)
		}
	}

	commitMap, meta := GetLatestCommitsNative(ctx, repoCtx.repoPath, repoCtx.ref, paths)

	for i := range treeEntries {
		if commit, ok := commitMap[treeEntries[i].Path]; ok {
			treeEntries[i].LatestCommit = commit
		}
	}

	m.RecordTreeWithCommit("native_complete", time.Since(start), len(treeEntries))

	if h.logger != nil {
		h.logger.Debug("tree/commits native git",
			zap.String("repo", fmt.Sprintf("%s/%s", owner, repo)),
			zap.String("path", path),
			zap.Int("entries", len(treeEntries)),
			zap.Int("resolved", len(commitMap)),
			zap.Duration("elapsed", time.Since(start)),
		)
	}

	return TreeWithCommitOverviewResponse{
		Success:    true,
		Repository: fmt.Sprintf("%s/%s", owner, repo),
		Owner:      owner,
		Name:       repo,
		Path:       path,
		Tree:       treeEntries,
		Meta:       &meta,
	}, nil
}

// HandleGetLatestCommitOverview handles GET /overview/latest-commit.
//
// query 는 typed.RegisterGetWithQuery wrapper 가 LatestCommitOverviewRequest
// 로 bind 한 뒤 본 함수에 전달한다 — wrapper 가 spec generator 에 ref/path 를
// in: query 로 노출 (Issue-2317 후속).
func (h *RepositoryOverviewHandler) HandleGetLatestCommitOverview(c *gin.Context, req LatestCommitOverviewRequest) (LatestCommitOverviewResponse, error) {
	ctx := c.Request.Context()
	start := time.Now()

	owner := c.Param("owner")
	repo := c.Param("repo")
	ref := req.Ref
	path := strings.Trim(req.Path, "/")

	repoCtx, err := h.validateRepositoryNative(ctx, owner, repo, ref)
	if err != nil {
		return LatestCommitOverviewResponse{}, err
	}
	defer repositories.GetRepoRWLock(repoCtx.repoPath).RUnlock()

	if path != "" {
		if err := h.pathValidator.ValidateFilePath(path); err != nil {
			return LatestCommitOverviewResponse{}, api.NewCoded(api.ErrCodeInvalidInput, err.Error())
		}
	}

	// An empty repository (created but never pushed → unborn HEAD, zero commits)
	// is a valid state, not an error: there is simply no latest commit. Resolve
	// the ref first and, if it does not point at a commit, return 200 with a null
	// commit — mirroring the main overview's empty-repo path (IsEmpty). Otherwise
	// `git log HEAD` fails exit 128 and was mislabelled 422 "failed to resolve
	// latest commit" (Issue-5560). The LatestCommitsBatch sibling already avoids
	// this by resolving the ref up front.
	if resolved, rerr := nativeResolveRef(ctx, repoCtx.repoPath, repoCtx.ref); rerr != nil || resolved == "" {
		return LatestCommitOverviewResponse{
			Success:    true,
			Repository: fmt.Sprintf("%s/%s", owner, repo),
			Owner:      owner,
			Name:       repo,
			Ref:        repoCtx.ref,
			Path:       path,
			Commit:     nil,
		}, nil
	}

	var (
		commit  *GitCommit
		nateErr error
	)
	if path == "" {
		commit, nateErr = NativeGetLatestCommit(ctx, repoCtx.repoPath, repoCtx.ref)
	} else {
		commit, nateErr = NativeGetLatestCommitForPath(ctx, repoCtx.repoPath, repoCtx.ref, path)
	}
	if nateErr != nil {
		h.logger.Warn("latest-commit native git failed",
			zap.String("owner", owner),
			zap.String("repo", repo),
			zap.String("ref", repoCtx.ref),
			zap.String("path", path),
			zap.Error(nateErr),
		)
		return LatestCommitOverviewResponse{}, api.NewCoded(api.ErrCodeRepositoryCorrupted, "failed to resolve latest commit")
	}

	// Best-effort commit count for the header ("N commits" affordance).
	// path-scoped (`git rev-list --count <ref> -- <path>`) or ref-total when path=="".
	// 절대 치명적이지 않다 — 타임아웃/에러면 count를 생략(nil)하고 소비측은 History 라벨로
	// 폴백한다. (repoPath, refHash, path)로 캐시하며, path=="" 키는 overview 엔드포인트가
	// 채운 ref-total 캐시와 공유된다.
	commitCount, commitCountApprox := h.resolveLatestCommitCount(ctx, repoCtx, path, commit != nil)

	if h.logger != nil {
		h.logger.Debug("latest-commit native git",
			zap.String("repo", fmt.Sprintf("%s/%s", owner, repo)),
			zap.String("ref", repoCtx.ref),
			zap.String("path", path),
			zap.Bool("found", commit != nil),
			zap.Duration("elapsed", time.Since(start)),
		)
	}

	return LatestCommitOverviewResponse{
		Success:                true,
		Repository:             fmt.Sprintf("%s/%s", owner, repo),
		Owner:                  owner,
		Name:                   repo,
		Ref:                    repoCtx.ref,
		Path:                   path,
		Commit:                 commit,
		CommitCount:            commitCount,
		CommitCountApproximate: commitCountApprox,
	}, nil
}

// resolveLatestCommitCount는 latest-commit 헤더용 커밋 수를 best-effort로 계산한다.
// hasCommit이 false면(빈 repo/경로) 계산을 건너뛰고 (nil, false)를 반환한다.
// 캐시 우선 조회 → miss 시 native rev-list → 성공하고 non-approximate면 캐시에 저장.
// 어떤 실패도 nil을 반환할 뿐 에러를 전파하지 않는다(응답 본체 보존).
func (h *RepositoryOverviewHandler) resolveLatestCommitCount(
	ctx context.Context, repoCtx *repositoryContext, path string, hasCommit bool,
) (*int, bool) {
	if !hasCommit {
		return nil, false
	}

	refHash := ""
	if resolved, resolveErr := nativeResolveRef(ctx, repoCtx.repoPath, repoCtx.ref); resolveErr == nil {
		refHash = resolved
	}

	if refHash != "" {
		if cached, ok := h.overviewCache.GetCommitCountForPath(repoCtx.repoPath, refHash, path); ok {
			// 캐시에는 non-approximate 값만 저장하므로 approximate=false.
			return &cached, false
		}
	}

	count, approx, err := NativeCountCommitsForPath(ctx, repoCtx.repoPath, repoCtx.ref, path)
	if err != nil {
		if h.logger != nil {
			h.logger.Warn("latest-commit count failed (omitting)",
				zap.String("ref", repoCtx.ref),
				zap.String("path", path),
				zap.Error(err),
			)
		}
		return nil, false
	}

	if refHash != "" && !approx {
		h.overviewCache.SetCommitCountForPath(repoCtx.repoPath, refHash, path, count)
	}
	return &count, approx
}

// HandlePostLatestCommitsBatch handles POST /overview/latest-commits.
func (h *RepositoryOverviewHandler) HandlePostLatestCommitsBatch(c *gin.Context) (LatestCommitsBatchResponse, error) {
	ctx := c.Request.Context()
	start := time.Now()
	m := observability.GetApplicationMetrics()

	owner := c.Param("owner")
	repo := c.Param("repo")

	var req LatestCommitsBatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return LatestCommitsBatchResponse{}, api.NewCoded(api.ErrCodeInvalidInput, "invalid request body")
	}
	if strings.TrimSpace(req.Ref) == "" {
		return LatestCommitsBatchResponse{}, api.NewCoded(api.ErrCodeInvalidInput, "Missing required field: ref")
	}
	if len(req.Paths) == 0 {
		return LatestCommitsBatchResponse{}, api.NewCoded(api.ErrCodeInvalidInput, "Missing required field: paths")
	}

	dirPath := strings.Trim(req.DirPath, "/")
	if dirPath != "" {
		if err := h.pathValidator.ValidateFilePath(dirPath); err != nil {
			return LatestCommitsBatchResponse{}, api.NewCoded(api.ErrCodeInvalidInput, err.Error())
		}
	}

	// Open with an empty ref (→ HEAD, no ref rejection) so an empty repo is not
	// pre-rejected 404 by validateAndOpenRepository before we can classify it;
	// req.Ref is resolved explicitly below.
	repoCtx, err := h.validateAndOpenRepository(ctx, owner, repo, "")
	if err != nil {
		return LatestCommitsBatchResponse{}, err
	}
	defer repositories.GetRepoRWLock(repoCtx.repoPath).RUnlock()
	defer func() { _ = repoCtx.repoWrapper.Close() }()
	repoCtx.ref = req.Ref

	resolvedHash, err := repoCtx.repoWrapper.ResolveReference(repoCtx.ref)
	if err != nil {
		// Distinguish an empty repository (unborn HEAD, zero commits) from a
		// genuinely bad ref. An empty repo has no latest commit for any path —
		// a valid state, not an error — so return 200 with empty results,
		// unifying with the main overview / latest-commit empty handling
		// (Issue-5577), instead of the 404 the batch endpoint used to emit.
		if _, headErr := nativeResolveRef(ctx, repoCtx.repoPath, "HEAD"); headErr != nil {
			m.RecordLatestCommitsBatch("empty_repo", time.Since(start), len(req.Paths), 0)
			return LatestCommitsBatchResponse{
				Success:       true,
				Repository:    fmt.Sprintf("%s/%s", owner, repo),
				Owner:         owner,
				Name:          repo,
				DirPath:       dirPath,
				CommitsByPath: map[string]*GitCommit{},
				Meta:          LatestCommitsMeta{},
			}, nil
		}
		return LatestCommitsBatchResponse{}, api.NewCoded(api.ErrCodeRefNotFound)
	}
	resolvedRefHash := resolvedHash.String()

	seen := make(map[string]struct{}, len(req.Paths))
	pathInfos := make([]CommitPathInfo, 0, len(req.Paths))
	for _, p := range req.Paths {
		p = strings.Trim(p, "/")
		if p == "" {
			continue
		}

		full := p
		if dirPath != "" {
			full = dirPath + "/" + p
		}

		if err := h.pathValidator.ValidateFilePath(full); err != nil {
			return LatestCommitsBatchResponse{}, api.NewCoded(api.ErrCodeInvalidInput, err.Error())
		}

		if _, ok := seen[full]; ok {
			continue
		}
		seen[full] = struct{}{}

		pathInfos = append(pathInfos, CommitPathInfo{Path: full, IsTree: false})
	}
	if len(pathInfos) == 0 {
		return LatestCommitsBatchResponse{}, api.NewCoded(api.ErrCodeInvalidInput, "paths must not be empty")
	}

	sortedFull := make([]string, 0, len(seen))
	for full := range seen {
		sortedFull = append(sortedFull, full)
	}
	sort.Strings(sortedFull)
	hh := sha1.Sum([]byte(strings.Join(sortedFull, "\n")))
	pathsHash := hex.EncodeToString(hh[:])
	cachePathKey := dirPath + "|" + pathsHash

	if cachedCommits, isComplete, found := h.overviewCache.GetLatestCommitsBatchAny(repoCtx.repoPath, resolvedRefHash, cachePathKey); found {
		cutReason := "cache_hit"
		incomplete := !isComplete
		if !isComplete {
			cutReason = "cache_hit_partial"
		}
		m.RecordLatestCommitsBatch(cutReason, time.Since(start), len(pathInfos), 0)
		return LatestCommitsBatchResponse{
			Success:       true,
			Repository:    fmt.Sprintf("%s/%s", owner, repo),
			Owner:         owner,
			Name:          repo,
			DirPath:       dirPath,
			CommitsByPath: cachedCommits,
			Meta: LatestCommitsMeta{
				ResolvedRefHash: resolvedRefHash,
				Incomplete:      incomplete,
				ScannedCommits:  0,
				TimeBudgetMs:    0,
				CutReason:       cutReason,
			},
		}, nil
	}

	sfKey := fmt.Sprintf("latest_commits_batch|%s|%s|%s", repoCtx.repoPath, resolvedRefHash, cachePathKey)
	v, sfErr, shared := h.overviewCache.Do(sfKey, func() (any, error) {
		timeBudget := latestCommitsTimeBudget()
		maxCommits := latestCommitsMaxCommits()

		commits, meta := h.service.GetLatestCommitsForPathsWithMeta(ctx, repoCtx.repoWrapper.GetRepo(), resolvedHash, pathInfos, timeBudget, maxCommits)
		if !meta.Incomplete {
			h.overviewCache.SetLatestCommitsBatch(repoCtx.repoPath, resolvedRefHash, cachePathKey, commits)
		} else {
			h.overviewCache.SetLatestCommitsBatchPartial(repoCtx.repoPath, resolvedRefHash, cachePathKey, commits)
		}
		return struct {
			commits map[string]*GitCommit
			meta    LatestCommitsMeta
		}{commits: commits, meta: meta}, nil
	})
	if sfErr != nil {
		m.RecordLatestCommitsBatch("error", time.Since(start), len(pathInfos), 0)
		return LatestCommitsBatchResponse{}, api.NewCoded(api.ErrCodeRepositoryCorrupted)
	}
	if shared {
		m.RecordOverviewSingleflightShared("latest_commits_batch")
	}

	out := v.(struct {
		commits map[string]*GitCommit
		meta    LatestCommitsMeta
	})
	out.meta.ResolvedRefHash = resolvedRefHash

	missing := make([]string, 0)
	for _, p := range req.Paths {
		p = strings.Trim(p, "/")
		if p == "" {
			continue
		}
		full := p
		if dirPath != "" {
			full = dirPath + "/" + p
		}
		if _, ok := out.commits[full]; !ok {
			missing = append(missing, p)
		}
	}
	sort.Strings(missing)

	outcome := "complete"
	if out.meta.Incomplete {
		outcome = "incomplete"
		m.RecordLatestCommitsBatchCutReason(out.meta.CutReason)
	}
	m.RecordLatestCommitsBatch(outcome, time.Since(start), len(pathInfos), out.meta.ScannedCommits)

	return LatestCommitsBatchResponse{
		Success:       true,
		Repository:    fmt.Sprintf("%s/%s", owner, repo),
		Owner:         owner,
		Name:          repo,
		DirPath:       dirPath,
		CommitsByPath: out.commits,
		Meta:          out.meta,
		MissingPaths:  missing,
	}, nil
}
