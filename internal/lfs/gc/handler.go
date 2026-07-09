package gc

import (
	"fmt"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
)

// Handler 는 LFS GC reachability scan endpoint 의 typed handler.
type Handler struct {
	logger        *logging.Logger
	pathConfig    *repositories.PathConfig
	pathValidator *repositories.GitAPIPathValidator
}

// NewHandler 는 LFS GC handler 를 생성한다.
func NewHandler(cfg *config.Config) (*Handler, error) {
	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}
	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}
	return &Handler{
		logger:        logger,
		pathConfig:    pathConfig,
		pathValidator: repositories.NewGitAPIPathValidator(pathConfig),
	}, nil
}

// HandleScanReachableOIDs 는 GET /repos/{owner}/{repo}/lfs/reachable-oids 요청을
// 처리한다.
//
// orchestrator 의 GC cron 이 호출 → ref 에서 도달 가능한 LFS pointer OID 의
// 집합을 응답. orchestrator 는 그 집합과 MinIO 의 object 목록을 차집합 해서
// orphan 을 식별 / 삭제한다.
func (h *Handler) HandleScanReachableOIDs(c *gin.Context) (ScanReachableOIDsResponse, error) {
	ctx := c.Request.Context()
	owner := c.Param("owner")
	repo := c.Param("repo")

	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo)); err != nil {
		return ScanReachableOIDsResponse{}, api.NewCoded(api.ErrCodeRepositoryNotFound)
	}

	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return ScanReachableOIDsResponse{}, api.NewCoded(api.ErrCodeRepositoryNotFound)
	}

	// RLock — git refs / objects 를 읽는 동안 다른 write 가 pack 을 재구성하지 않도록.
	repoLock := repositories.GetRepoRWLock(repoPath)
	repoLock.RLock()
	defer repoLock.RUnlock()

	resp, err := ScanReachableOIDs(ctx, repoPath)
	if err != nil {
		h.logger.Error("LFS GC scan failed",
			zap.String("owner", owner),
			zap.String("repo", repo),
			zap.Error(err),
		)
		return ScanReachableOIDsResponse{}, api.NewCoded(api.ErrCodeInternalError, fmt.Sprintf("scan failed: %v", err))
	}

	h.logger.Info("LFS GC scan completed",
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.Int("reachable_oids", len(resp.ReachableOIDs)),
		zap.Int("scanned_blobs", resp.ScannedBlobs),
		zap.Int("scanned_refs", resp.ScannedRefs),
		zap.Bool("truncated", resp.Truncated),
		zap.Int64("duration_ms", resp.DurationMs),
	)
	return *resp, nil
}
