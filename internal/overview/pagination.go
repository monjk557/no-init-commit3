package overview

import (
	"context"
	"fmt"
	"time"

	"github.com/collabops/git-executor/internal/logging"
	"go.uber.org/zap"
)

// SmartPagination은 저장소 크기와 성능에 따른 적응형 페이지네이션을 제공합니다.
type SmartPagination struct {
	logger  *logging.Logger
	config  *PaginationConfig
	monitor *PerformanceMonitor
}

// PaginationConfig는 페이지네이션 설정을 정의합니다.
type PaginationConfig struct {
	// 저장소 크기별 임계값
	SmallRepoThreshold  int `default:"100"`  // 100개 이하면 소형
	MediumRepoThreshold int `default:"1000"` // 1000개 이하면 중형

	// 성능 기준
	MaxResponseTime time.Duration `default:"300ms"` // 목표 응답시간

	// 저장소 크기별 기본 페이지 크기
	SmallRepoPageSize  int `default:"100"` // 소형: 전체 반환
	MediumRepoPageSize int `default:"50"`  // 중형: 적당한 크기
	LargeRepoPageSize  int `default:"20"`  // 대형: GitLab 패턴

	// 제한값
	MaxPageSize int `default:"100"` // 최대 페이지 크기
	MinPageSize int `default:"10"`  // 최소 페이지 크기
}

// PerformanceMonitor는 성능 모니터링을 위한 인터페이스입니다.
type PerformanceMonitor struct {
	responseTimeCache map[string]time.Duration
}

// NewSmartPagination은 새로운 SmartPagination을 생성합니다.
func NewSmartPagination(logger *logging.Logger) *SmartPagination {
	config := &PaginationConfig{
		SmallRepoThreshold:  100,
		MediumRepoThreshold: 1000,
		MaxResponseTime:     300 * time.Millisecond,
		SmallRepoPageSize:   100,
		MediumRepoPageSize:  50,
		LargeRepoPageSize:   20,
		MaxPageSize:         100,
		MinPageSize:         10,
	}

	monitor := &PerformanceMonitor{
		responseTimeCache: make(map[string]time.Duration),
	}

	return &SmartPagination{
		logger:  logger,
		config:  config,
		monitor: monitor,
	}
}

// DeterminePageSize는 저장소와 성능 상황에 따라 최적의 페이지 크기를 결정합니다.
func (p *SmartPagination) DeterminePageSize(ctx context.Context, repoID, path string, requestedSize int) int {
	logger := p.logger.With(
		zap.String("repo_id", repoID),
		zap.String("path", path),
		zap.Int("requested_size", requestedSize),
	)

	// 1. 사용자가 명시적으로 요청한 크기가 있으면 우선 적용 (제한값 내에서)
	if requestedSize > 0 {
		if requestedSize > p.config.MaxPageSize {
			logger.Info("Requested page size exceeds maximum, capping",
				zap.Int("requested", requestedSize),
				zap.Int("max", p.config.MaxPageSize))
			return p.config.MaxPageSize
		}
		if requestedSize < p.config.MinPageSize {
			logger.Info("Requested page size below minimum, adjusting",
				zap.Int("requested", requestedSize),
				zap.Int("min", p.config.MinPageSize))
			return p.config.MinPageSize
		}
		return requestedSize
	}

	// 2. 저장소 크기 추정 (빠른 추정 방법 사용)
	estimatedSize := p.estimateDirectorySize(ctx, repoID, path)
	logger.Debug("Estimated directory size", zap.Int("estimated_size", estimatedSize))

	// 3. 기본 페이지 크기 결정
	var basePageSize int
	switch {
	case estimatedSize <= p.config.SmallRepoThreshold:
		basePageSize = p.config.SmallRepoPageSize
		logger.Debug("Using small repo page size", zap.Int("page_size", basePageSize))
	case estimatedSize <= p.config.MediumRepoThreshold:
		basePageSize = p.config.MediumRepoPageSize
		logger.Debug("Using medium repo page size", zap.Int("page_size", basePageSize))
	default:
		basePageSize = p.config.LargeRepoPageSize
		logger.Debug("Using large repo page size", zap.Int("page_size", basePageSize))
	}

	// 4. 실시간 성능 기반 조정
	avgResponseTime := p.monitor.GetAverageResponseTime(repoID)
	if avgResponseTime > p.config.MaxResponseTime {
		// 성능이 저하되고 있으면 페이지 크기 감소
		adjustedSize := basePageSize * 2 / 3
		logger.Info("Adjusting page size due to performance degradation",
			zap.Duration("avg_response_time", avgResponseTime),
			zap.Duration("max_response_time", p.config.MaxResponseTime),
			zap.Int("original_size", basePageSize),
			zap.Int("adjusted_size", adjustedSize))
		basePageSize = adjustedSize
	}

	// 5. 최종 제한값 적용
	if basePageSize > p.config.MaxPageSize {
		basePageSize = p.config.MaxPageSize
	}
	if basePageSize < p.config.MinPageSize {
		basePageSize = p.config.MinPageSize
	}

	logger.Info("Determined optimal page size", zap.Int("final_page_size", basePageSize))
	return basePageSize
}

// estimateDirectorySize는 디렉토리의 대략적인 파일 수를 빠르게 추정합니다.
func (p *SmartPagination) estimateDirectorySize(ctx context.Context, repoID, path string) int {
	// TODO: 실제 구현에서는 다음 방법들을 활용:
	// 1. 캐시된 값 조회
	// 2. Git ls-tree를 통한 빠른 카운트
	// 3. 이전 조회 결과 기반 추정

	// TODO: 실제 구현에서는 다음 방법들을 활용:
	// 1. 캐시된 값 조회
	// 2. Git ls-tree를 통한 빠른 카운트
	// 3. 이전 조회 결과 기반 추정

	// 간단한 캐시 조회 시뮬레이션
	if p.monitor.responseTimeCache != nil {
		// cacheKey := fmt.Sprintf("dirsize:%s:%s", repoID, path)
		// 실제로는 더 정교한 캐시 로직 필요
	}

	// 기본값: 중형 저장소로 가정
	return 50
}

// ApplyPagination은 전체 파일 목록에 페이지네이션을 적용합니다.
func (p *SmartPagination) ApplyPagination(
	allFiles []GitTreeEntryWithCommit,
	page, perPage int,
	baseURL string,
) (*TreeWithCommitOverviewResponse, *PaginationInfo) {

	totalFiles := len(allFiles)

	// 페이지 검증
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = p.config.MediumRepoPageSize // 기본값
	}

	// 총 페이지 수 계산
	totalPages := (totalFiles + perPage - 1) / perPage
	if totalPages < 1 {
		totalPages = 1
	}

	// 페이지 범위 계산
	start := (page - 1) * perPage
	end := start + perPage

	// 범위 검증
	if start >= totalFiles {
		start = 0
		end = 0
	} else if end > totalFiles {
		end = totalFiles
	}

	// 해당 페이지의 파일들만 추출
	var pageFiles []GitTreeEntryWithCommit
	if start < totalFiles && end > start {
		pageFiles = allFiles[start:end]
	}

	// 페이지네이션 정보 생성
	pagination := &PaginationInfo{
		Page:        page,
		PerPage:     perPage,
		TotalFiles:  totalFiles,
		TotalPages:  totalPages,
		HasNextPage: page < totalPages,
		HasPrevPage: page > 1,
	}

	// URL 생성 (있는 경우)
	if baseURL != "" {
		if pagination.HasNextPage {
			pagination.NextPageURL = fmt.Sprintf("%s?page=%d&per_page=%d", baseURL, page+1, perPage)
		}
		if pagination.HasPrevPage {
			pagination.PrevPageURL = fmt.Sprintf("%s?page=%d&per_page=%d", baseURL, page-1, perPage)
		}
	}

	// 응답 생성 (나머지 필드는 호출자가 설정)
	response := &TreeWithCommitOverviewResponse{
		Tree:       pageFiles,
		Pagination: pagination,
	}

	p.logger.Info("Applied pagination",
		zap.Int("total_files", totalFiles),
		zap.Int("page", page),
		zap.Int("per_page", perPage),
		zap.Int("total_pages", totalPages),
		zap.Int("returned_files", len(pageFiles)),
	)

	return response, pagination
}

// GetAverageResponseTime은 저장소의 평균 응답 시간을 반환합니다.
func (pm *PerformanceMonitor) GetAverageResponseTime(repoID string) time.Duration {
	if pm.responseTimeCache == nil {
		return 0
	}

	if avgTime, exists := pm.responseTimeCache[repoID]; exists {
		return avgTime
	}

	// 기본값: 성능 저하가 없는 것으로 가정
	return 100 * time.Millisecond
}

// RecordResponseTime은 응답 시간을 기록합니다.
func (pm *PerformanceMonitor) RecordResponseTime(repoID string, duration time.Duration) {
	if pm.responseTimeCache == nil {
		pm.responseTimeCache = make(map[string]time.Duration)
	}

	// 간단한 이동 평균 (실제로는 더 정교한 통계 필요)
	if existing, exists := pm.responseTimeCache[repoID]; exists {
		pm.responseTimeCache[repoID] = (existing + duration) / 2
	} else {
		pm.responseTimeCache[repoID] = duration
	}
}
