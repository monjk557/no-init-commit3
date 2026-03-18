package lfs

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/lfs/metadata"
	"github.com/collabops/git-executor/internal/lfs/metering"
	"github.com/collabops/git-executor/internal/lfs/storage"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	// lfsMediaType은 Git LFS API의 Content-Type입니다.
	lfsMediaType = "application/vnd.git-lfs+json"
)

// Handler는 Git LFS HTTP API 엔드포인트를 처리합니다.
type Handler struct {
	config  *config.Config
	logger  *logging.Logger
	service *LFSService
}

// NewHandler는 새로운 LFS 핸들러를 생성합니다.
// GCS 백엔드, 메타데이터 DB, 미터링을 초기화합니다.
func NewHandler(cfg *config.Config) (*Handler, error) {
	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	// GCS 스토리지 백엔드 초기화 (ADC/Workload Identity 인증)
	gcsBackend, err := storage.NewGCSBackend(
		context.Background(),
		cfg.LFS.GCSBucket,
		cfg.LFS.URLExpiry,
		logger.Logger,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCS backend: %w", err)
	}

	// 메타데이터 저장소 초기화
	metadataRepo, err := metadata.NewRepository(cfg.LFS.MetadataDBPath, logger.Logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create metadata repository: %w", err)
	}

	// 미터링 컬렉터 초기화
	meteringCollector, err := metering.NewCollector(cfg.LFS.MetadataDBPath, logger.Logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create metering collector: %w", err)
	}

	service := NewLFSService(ServiceConfig{
		Storage:                gcsBackend,
		Metadata:               metadataRepo,
		Metering:               meteringCollector,
		Logger:                 logger.Logger,
		MaxObjectSize:          cfg.LFS.MaxObjectSize,
		StorageLimitPerOwner:   cfg.LFS.StorageLimitPerOwner,
		BandwidthLimitPerMonth: cfg.LFS.BandwidthLimitPerMonth,
	})

	return &Handler{
		config:  cfg,
		logger:  logger,
		service: service,
	}, nil
}

// cleanRepoParam는 repo 파라미터에서 .git suffix를 제거합니다.
// Git LFS 클라이언트가 .git suffix를 포함한 URL을 사용할 수 있습니다.
func cleanRepoParam(repo string) string {
	return strings.TrimSuffix(repo, ".git")
}

// HandleBatch는 POST /{owner}/{repo}/info/lfs/objects/batch 요청을 처리합니다.
// Git LFS 클라이언트의 upload/download 요청에 대해 GCS Signed URL을 반환합니다.
func (h *Handler) HandleBatch(c *gin.Context) {
	ctx := c.Request.Context()
	owner := c.Param("owner")
	repo := cleanRepoParam(c.Param("repo"))

	var req BatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "invalid request body"})
		return
	}

	// 요청 유효성 검증
	if req.Operation != "upload" && req.Operation != "download" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "operation must be 'upload' or 'download'"})
		return
	}
	if len(req.Objects) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "objects list is empty"})
		return
	}

	h.logger.Info("LFS batch request",
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.String("operation", req.Operation),
		zap.Int("object_count", len(req.Objects)))

	resp, err := h.service.ProcessBatch(ctx, owner, repo, &req)
	if err != nil {
		h.logger.Error("failed to process LFS batch", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"message": "internal server error"})
		return
	}

	c.Header("Content-Type", lfsMediaType)
	c.JSON(http.StatusOK, resp)
}

// HandleVerify는 POST /{owner}/{repo}/info/lfs/objects/verify 요청을 처리합니다.
// 클라이언트가 GCS에 업로드 완료 후 호출하여 메타데이터를 확정합니다.
func (h *Handler) HandleVerify(c *gin.Context) {
	ctx := c.Request.Context()
	owner := c.Param("owner")
	repo := cleanRepoParam(c.Param("repo"))

	var req VerifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "invalid request body"})
		return
	}

	if err := h.service.VerifyObject(ctx, owner, repo, req.OID, req.Size); err != nil {
		h.logger.Error("LFS verify failed",
			zap.String("oid", req.OID),
			zap.Error(err))
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}

	c.Status(http.StatusOK)
}

// HandleCreateLock은 POST /{owner}/{repo}/info/lfs/locks 요청을 처리합니다.
func (h *Handler) HandleCreateLock(c *gin.Context) {
	owner := c.Param("owner")
	repo := cleanRepoParam(c.Param("repo"))
	user := c.GetHeader("X-LFS-User")
	if user == "" {
		user = "unknown"
	}

	var req LockRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "invalid request body"})
		return
	}

	lock, err := h.service.metadata.CreateLock(c.Request.Context(), owner, repo, req.Path, user)
	if err != nil {
		// 이미 잠긴 경우 409 Conflict
		c.Header("Content-Type", lfsMediaType)
		c.JSON(http.StatusConflict, LockResponse{
			Lock:    toLockInfo(lock),
			Message: err.Error(),
		})
		return
	}

	c.Header("Content-Type", lfsMediaType)
	c.JSON(http.StatusCreated, LockResponse{
		Lock: toLockInfo(lock),
	})
}

// HandleListLocks는 GET /{owner}/{repo}/info/lfs/locks 요청을 처리합니다.
func (h *Handler) HandleListLocks(c *gin.Context) {
	owner := c.Param("owner")
	repo := cleanRepoParam(c.Param("repo"))
	path := c.Query("path")
	cursor := c.Query("cursor")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))

	locks, err := h.service.metadata.ListLocks(c.Request.Context(), owner, repo, path, cursor, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to list locks"})
		return
	}

	lockInfos := make([]LockInfo, 0, len(locks))
	for _, l := range locks {
		lockInfos = append(lockInfos, toLockInfo(&l))
	}

	var nextCursor string
	if len(locks) == limit {
		nextCursor = strconv.FormatUint(uint64(locks[len(locks)-1].ID), 10)
	}

	c.Header("Content-Type", lfsMediaType)
	c.JSON(http.StatusOK, LockListResponse{
		Locks:      lockInfos,
		NextCursor: nextCursor,
	})
}

// HandleVerifyLocks는 POST /{owner}/{repo}/info/lfs/locks/verify 요청을 처리합니다.
func (h *Handler) HandleVerifyLocks(c *gin.Context) {
	owner := c.Param("owner")
	repo := cleanRepoParam(c.Param("repo"))
	user := c.GetHeader("X-LFS-User")

	var req LockVerifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "invalid request body"})
		return
	}

	ours, theirs, err := h.service.metadata.VerifyLocks(c.Request.Context(), owner, repo, user, req.Cursor, req.Limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to verify locks"})
		return
	}

	oursInfo := make([]LockInfo, 0, len(ours))
	for _, l := range ours {
		oursInfo = append(oursInfo, toLockInfo(&l))
	}
	theirsInfo := make([]LockInfo, 0, len(theirs))
	for _, l := range theirs {
		theirsInfo = append(theirsInfo, toLockInfo(&l))
	}

	c.Header("Content-Type", lfsMediaType)
	c.JSON(http.StatusOK, LockVerifyResponse{
		Ours:   oursInfo,
		Theirs: theirsInfo,
	})
}

// HandleDeleteLock은 POST /{owner}/{repo}/info/lfs/locks/:id/unlock 요청을 처리합니다.
func (h *Handler) HandleDeleteLock(c *gin.Context) {
	user := c.GetHeader("X-LFS-User")

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "invalid lock id"})
		return
	}

	var req UnlockRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "invalid request body"})
		return
	}

	lock, err := h.service.metadata.DeleteLock(c.Request.Context(), uint(id), user, req.Force)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"message": err.Error()})
		return
	}

	c.Header("Content-Type", lfsMediaType)
	c.JSON(http.StatusOK, UnlockResponse{
		Lock: toLockInfo(lock),
	})
}

// HandleGetUsage는 GET /workspaces/:owner/lfs/usage 요청을 처리합니다.
func (h *Handler) HandleGetUsage(c *gin.Context) {
	owner := c.Param("owner")

	usage, err := h.service.GetUsage(c.Request.Context(), owner)
	if err != nil {
		h.logger.Error("failed to get LFS usage", zap.String("owner", owner), zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"message": "failed to get usage"})
		return
	}

	c.JSON(http.StatusOK, usage)
}

// toLockInfo는 DB 모델을 API 응답 구조체로 변환합니다.
func toLockInfo(lock *metadata.LFSLock) LockInfo {
	if lock == nil {
		return LockInfo{}
	}
	return LockInfo{
		ID:       strconv.FormatUint(uint64(lock.ID), 10),
		Path:     lock.Path,
		LockedAt: lock.LockedAt,
		Owner:    &Owner{Name: lock.LockedBy},
	}
}
