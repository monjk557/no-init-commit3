package lfs

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/lfs/metadata"
	"github.com/collabops/git-executor/internal/lfs/metering"
	"github.com/collabops/git-executor/internal/lfs/storage"
	"go.uber.org/zap"
)

// LFSService는 LFS 비즈니스 로직을 처리합니다.
type LFSService struct {
	storage  storage.Backend
	metadata *metadata.Repository
	metering *metering.Collector
	logger   *zap.Logger

	maxObjectSize          int64
	storageLimitPerOwner   int64
	bandwidthLimitPerMonth int64
}

// ServiceConfig는 LFSService 생성에 필요한 설정입니다.
type ServiceConfig struct {
	Storage                storage.Backend
	Metadata               *metadata.Repository
	Metering               *metering.Collector
	Logger                 *zap.Logger
	MaxObjectSize          int64
	StorageLimitPerOwner   int64
	BandwidthLimitPerMonth int64
}

// NewLFSService는 새로운 LFS 서비스를 생성합니다.
func NewLFSService(cfg ServiceConfig) *LFSService {
	return &LFSService{
		storage:                cfg.Storage,
		metadata:               cfg.Metadata,
		metering:               cfg.Metering,
		logger:                 cfg.Logger,
		maxObjectSize:          cfg.MaxObjectSize,
		storageLimitPerOwner:   cfg.StorageLimitPerOwner,
		bandwidthLimitPerMonth: cfg.BandwidthLimitPerMonth,
	}
}

// ProcessBatch는 LFS Batch API 요청을 처리합니다.
func (s *LFSService) ProcessBatch(ctx context.Context, owner, repo string, req *BatchRequest) (*BatchResponse, error) {
	resp := &BatchResponse{
		Transfer: "basic",
		Objects:  make([]ObjectResult, 0, len(req.Objects)),
	}

	for _, obj := range req.Objects {
		result, err := s.processObject(ctx, owner, repo, req.Operation, obj)
		if err != nil {
			s.logger.Error("failed to process LFS object",
				zap.String("oid", obj.OID),
				zap.String("operation", req.Operation),
				zap.Error(err))
			result = ObjectResult{
				OID:  obj.OID,
				Size: obj.Size,
				Error: &ObjectError{
					Code:    500,
					Message: "internal server error",
				},
			}
		}
		resp.Objects = append(resp.Objects, result)
	}

	return resp, nil
}

// processObject는 개별 LFS 오브젝트를 처리합니다.
func (s *LFSService) processObject(ctx context.Context, owner, repo, operation string, obj ObjectSpec) (ObjectResult, error) {
	result := ObjectResult{
		OID:           obj.OID,
		Size:          obj.Size,
		Authenticated: true,
	}

	switch operation {
	case "upload":
		return s.processUpload(ctx, owner, repo, obj, result)
	case "download":
		return s.processDownload(ctx, owner, repo, obj, result)
	default:
		return result, fmt.Errorf("unsupported operation: %s", operation)
	}
}

// processUpload는 업로드 요청을 처리합니다.
func (s *LFSService) processUpload(ctx context.Context, owner, repo string, obj ObjectSpec, result ObjectResult) (ObjectResult, error) {
	// 단일 오브젝트 크기 제한 확인
	if s.maxObjectSize > 0 && obj.Size > s.maxObjectSize {
		result.Error = &ObjectError{
			Code:    422,
			Message: fmt.Sprintf("object size %d exceeds maximum %d bytes", obj.Size, s.maxObjectSize),
		}
		return result, nil
	}

	// 워크스페이스 스토리지 한도 확인
	if s.storageLimitPerOwner > 0 {
		totalBytes, _, err := s.metadata.GetStorageUsage(ctx, owner)
		if err != nil {
			return result, fmt.Errorf("failed to check storage usage: %w", err)
		}
		if totalBytes+obj.Size > s.storageLimitPerOwner {
			result.Error = &ObjectError{
				Code:    507,
				Message: "workspace storage limit exceeded",
			}
			return result, nil
		}
	}

	// 이미 존재하는 오브젝트인지 확인 (중복 업로드 방지)
	exists, err := s.metadata.ObjectExists(ctx, owner, repo, obj.OID)
	if err != nil {
		return result, fmt.Errorf("failed to check object existence: %w", err)
	}
	if exists {
		// 이미 존재하면 actions 없이 반환 (클라이언트는 업로드 건너뜀)
		return result, nil
	}

	// GCS Signed Upload URL 생성
	uploadURL, err := s.storage.GenerateUploadURL(ctx, owner, repo, obj.OID, obj.Size)
	if err != nil {
		return result, fmt.Errorf("failed to generate upload URL: %w", err)
	}

	result.Actions = map[string]Action{
		"upload": {
			Href:      uploadURL.URL,
			Header:    uploadURL.Headers,
			ExpiresIn: int(time.Until(uploadURL.ExpiresAt).Seconds()),
			ExpiresAt: uploadURL.ExpiresAt,
		},
		"verify": {
			Href:      fmt.Sprintf("/repos/%s/%s/info/lfs/objects/verify", owner, repo),
			ExpiresIn: 3600,
		},
	}

	return result, nil
}

// processDownload는 다운로드 요청을 처리합니다.
func (s *LFSService) processDownload(ctx context.Context, owner, repo string, obj ObjectSpec, result ObjectResult) (ObjectResult, error) {
	// 메타데이터에서 오브젝트 존재 확인
	exists, err := s.metadata.ObjectExists(ctx, owner, repo, obj.OID)
	if err != nil {
		return result, fmt.Errorf("failed to check object existence: %w", err)
	}
	if !exists {
		result.Error = &ObjectError{
			Code:    404,
			Message: "object not found",
		}
		return result, nil
	}

	// 월간 대역폭 한도 확인
	if s.bandwidthLimitPerMonth > 0 {
		currentMonth := time.Now().Format("2006-01")
		used, err := s.metering.GetMonthlyBandwidth(ctx, owner, currentMonth)
		if err != nil {
			s.logger.Warn("failed to check bandwidth usage, allowing download",
				zap.String("owner", owner),
				zap.Error(err))
		} else if used+obj.Size > s.bandwidthLimitPerMonth {
			result.Error = &ObjectError{
				Code:    507,
				Message: "monthly bandwidth limit exceeded",
			}
			return result, nil
		}
	}

	// GCS Signed Download URL 생성
	downloadURL, err := s.storage.GenerateDownloadURL(ctx, owner, repo, obj.OID)
	if err != nil {
		return result, fmt.Errorf("failed to generate download URL: %w", err)
	}

	// 대역폭 사용량 기록 (URL 발급 시점에 기록)
	if err := s.metering.RecordDownload(ctx, owner, repo, obj.OID, obj.Size); err != nil {
		s.logger.Warn("failed to record bandwidth usage",
			zap.String("oid", obj.OID),
			zap.Error(err))
	}

	result.Actions = map[string]Action{
		"download": {
			Href:      downloadURL.URL,
			ExpiresIn: int(time.Until(downloadURL.ExpiresAt).Seconds()),
			ExpiresAt: downloadURL.ExpiresAt,
		},
	}

	return result, nil
}

// VerifyObject는 업로드 완료 후 GCS에 실제 존재하는지 확인하고 메타데이터를 기록합니다.
func (s *LFSService) VerifyObject(ctx context.Context, owner, repo, oid string, expectedSize int64) error {
	// GCS에서 실제 오브젝트 확인
	exists, err := s.storage.Exists(ctx, owner, repo, oid)
	if err != nil {
		return fmt.Errorf("failed to verify object in storage: %w", err)
	}
	if !exists {
		return fmt.Errorf("object not found in storage: %s", oid)
	}

	// 크기 검증
	actualSize, err := s.storage.ObjectSize(ctx, owner, repo, oid)
	if err != nil {
		return fmt.Errorf("failed to get object size: %w", err)
	}
	if actualSize != expectedSize {
		return fmt.Errorf("size mismatch: expected %d, got %d", expectedSize, actualSize)
	}

	// 메타데이터 DB에 기록
	if err := s.metadata.PutObject(ctx, owner, repo, oid, actualSize); err != nil {
		return fmt.Errorf("failed to store object metadata: %w", err)
	}

	// 스토리지 사용량 이벤트 기록
	if err := s.metering.RecordUpload(ctx, owner, repo, oid, actualSize); err != nil {
		s.logger.Warn("failed to record storage event",
			zap.String("oid", oid),
			zap.Error(err))
	}

	return nil
}

// GetUsage는 워크스페이스의 LFS 사용량을 조회합니다.
func (s *LFSService) GetUsage(ctx context.Context, owner string) (*UsageResponse, error) {
	totalBytes, objectCount, err := s.metadata.GetStorageUsage(ctx, owner)
	if err != nil {
		return nil, fmt.Errorf("failed to get storage usage: %w", err)
	}

	currentMonth := time.Now().Format("2006-01")
	bandwidthUsed, err := s.metering.GetMonthlyBandwidth(ctx, owner, currentMonth)
	if err != nil {
		return nil, fmt.Errorf("failed to get bandwidth usage: %w", err)
	}

	repoUsages, err := s.metadata.GetRepoStorageUsage(ctx, owner)
	if err != nil {
		return nil, fmt.Errorf("failed to get repo usage: %w", err)
	}

	repos := make([]RepoUsage, 0, len(repoUsages))
	for _, ru := range repoUsages {
		repos = append(repos, RepoUsage{
			Repo:         ru.Repo,
			StorageBytes: ru.TotalBytes,
			ObjectCount:  ru.ObjectCount,
		})
	}

	return &UsageResponse{
		Owner: owner,
		Storage: StorageUsage{
			UsedBytes:   totalBytes,
			LimitBytes:  s.storageLimitPerOwner,
			ObjectCount: objectCount,
		},
		Bandwidth: BandwidthUsage{
			Month:      currentMonth,
			UsedBytes:  bandwidthUsed,
			LimitBytes: s.bandwidthLimitPerMonth,
		},
		Repos: repos,
	}, nil
}

// --- LFS 포인터 유틸리티 ---

// lfsPointerPattern은 LFS 포인터 파일의 정규식 패턴입니다.
var lfsPointerPattern = regexp.MustCompile(`^version https://git-lfs\.github\.com/spec/v1\noid sha256:([a-f0-9]{64})\nsize (\d+)\n$`)

// IsLFSPointer는 콘텐츠가 LFS 포인터 파일인지 확인합니다.
func IsLFSPointer(content []byte) bool {
	return lfsPointerPattern.Match(content)
}

// ParseLFSPointer는 LFS 포인터에서 OID와 크기를 추출합니다.
func ParseLFSPointer(content []byte) (oid string, size int64, ok bool) {
	matches := lfsPointerPattern.FindSubmatch(content)
	if matches == nil {
		return "", 0, false
	}

	oid = string(matches[1])
	size, err := strconv.ParseInt(string(matches[2]), 10, 64)
	if err != nil {
		return "", 0, false
	}

	return oid, size, true
}

// FormatLFSPointer는 LFS 포인터 파일 콘텐츠를 생성합니다.
func FormatLFSPointer(oid string, size int64) string {
	var b strings.Builder
	b.WriteString("version https://git-lfs.github.com/spec/v1\n")
	b.WriteString("oid sha256:")
	b.WriteString(oid)
	b.WriteString("\n")
	b.WriteString("size ")
	b.WriteString(strconv.FormatInt(size, 10))
	b.WriteString("\n")
	return b.String()
}
