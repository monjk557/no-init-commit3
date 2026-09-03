# Phase 2 구현 계획 - 실행 가능한 단계별 가이드

## 🎯 3주 만에 2-3배 성능 개선 달성

**제약사항**: Pack/Unpack 최적화 제외 (다른 팀 담당)
**목표**: 우리가 할 수 있는 부분만으로 경쟁사 수준 달성

---

## 📅 Week 1: 스마트 페이지네이션 (즉시 효과)

### **Day 1-2: API 구조 개선**

#### **1. 기존 응답 구조 확인**
```bash
# 현재 API 응답 구조 파악
curl "http://localhost:8090/repos/test-org/medium-repo/overview/tree/commits" | jq '.'
```

#### **2. 페이지네이션 응답 구조 설계**
```go
// internal/overview/types.go
type DirectoryResponse struct {
    Files      []FileInfo `json:"files"`

    // 새로 추가: 페이지네이션 메타데이터
    Pagination *PaginationInfo `json:"pagination,omitempty"`

    // 기존 필드들
    Repository string     `json:"repository"`
    Path       string     `json:"path"`
    Ref        string     `json:"ref"`
}

type PaginationInfo struct {
    Page         int  `json:"page"`
    PerPage      int  `json:"per_page"`
    TotalFiles   int  `json:"total_files"`
    TotalPages   int  `json:"total_pages"`
    HasNextPage  bool `json:"has_next_page"`
    NextPageURL  string `json:"next_page_url,omitempty"`
}

type DirectoryRequest struct {
    // 기존 필드들
    RepoID string `json:"repo_id"`
    Path   string `json:"path"`
    Ref    string `json:"ref"`

    // 새로 추가: 페이지네이션 파라미터
    Page    int `json:"page,omitempty" default:"1"`
    PerPage int `json:"per_page,omitempty" default:"0"` // 0이면 자동 결정
}
```

#### **3. 자동 페이지 크기 결정 로직**
```go
// internal/overview/pagination.go
package overview

import (
    "context"
    "fmt"
    "time"
)

type SmartPagination struct {
    monitor *PerformanceMonitor
    config  *PaginationConfig
}

type PaginationConfig struct {
    SmallRepoThreshold  int           `default:"100"`   // 100개 이하면 소형
    MediumRepoThreshold int           `default:"1000"`  // 1000개 이하면 중형
    MaxResponseTime     time.Duration `default:"300ms"` // 목표 응답시간

    // 저장소 크기별 기본 페이지 크기
    SmallRepoPageSize   int `default:"100"` // 소형: 전체
    MediumRepoPageSize  int `default:"50"`  // 중형: 적당히
    LargeRepoPageSize   int `default:"20"`  // 대형: GitLab 패턴
}

func (p *SmartPagination) DeterminePageSize(ctx context.Context, repoID, path string, requestedSize int) int {
    // 사용자가 명시적으로 요청한 크기가 있으면 우선 적용
    if requestedSize > 0 && requestedSize <= 100 {
        return requestedSize
    }

    // 저장소 크기 추정 (캐시된 값 사용)
    estimatedSize := p.estimateDirectorySize(repoID, path)

    // 기본 페이지 크기 결정
    var basePageSize int
    switch {
    case estimatedSize <= p.config.SmallRepoThreshold:
        basePageSize = p.config.SmallRepoPageSize
    case estimatedSize <= p.config.MediumRepoThreshold:
        basePageSize = p.config.MediumRepoPageSize
    default:
        basePageSize = p.config.LargeRepoPageSize
    }

    // 실시간 성능 기반 조정
    avgResponseTime := p.monitor.GetAverageResponseTime(repoID)
    if avgResponseTime > p.config.MaxResponseTime {
        // 성능이 저하되고 있으면 페이지 크기 감소
        basePageSize = basePageSize * 2 / 3
        if basePageSize < 10 {
            basePageSize = 10 // 최소값 보장
        }
    }

    return basePageSize
}

func (p *SmartPagination) estimateDirectorySize(repoID, path string) int {
    // 캐시에서 저장소 메타데이터 조회
    cacheKey := fmt.Sprintf("dirsize:%s:%s", repoID, path)
    if cached := p.monitor.cache.Get(cacheKey); cached != nil {
        return cached.(int)
    }

    // 빠른 추정: git ls-tree로 파일 개수만 세기
    // (실제 구현에서는 기존 Git 조회 최적화)
    return 50 // 기본값
}
```

### **Day 3-4: 서비스 레이어 구현**

#### **4. 기존 서비스 수정**
```go
// internal/overview/service.go

func (s *Service) GetDirectoryTreeWithCommits(ctx context.Context, req *DirectoryRequest) (*DirectoryResponse, error) {
    // 페이지네이션 설정
    pagination := s.smartPagination.DeterminePageSize(ctx, req.RepoID, req.Path, req.PerPage)

    // 성능 모니터링 시작
    timer := s.monitor.StartTimer(req.RepoID)
    defer timer.Stop()

    // 전체 파일 목록 조회 (기존 로직 활용)
    allFiles, err := s.getAllFiles(ctx, req.RepoID, req.Path, req.Ref)
    if err != nil {
        return nil, err
    }

    // 페이지네이션 적용
    response := s.applyPagination(allFiles, req.Page, pagination)
    response.Repository = req.RepoID
    response.Path = req.Path
    response.Ref = req.Ref

    return response, nil
}

func (s *Service) applyPagination(allFiles []FileInfo, page, perPage int) *DirectoryResponse {
    totalFiles := len(allFiles)
    totalPages := (totalFiles + perPage - 1) / perPage

    // 페이지 범위 계산
    start := (page - 1) * perPage
    end := start + perPage
    if end > totalFiles {
        end = totalFiles
    }

    // 해당 페이지 파일들만 추출
    var pageFiles []FileInfo
    if start < totalFiles {
        pageFiles = allFiles[start:end]
    }

    return &DirectoryResponse{
        Files: pageFiles,
        Pagination: &PaginationInfo{
            Page:        page,
            PerPage:     perPage,
            TotalFiles:  totalFiles,
            TotalPages:  totalPages,
            HasNextPage: page < totalPages,
            NextPageURL: s.buildNextPageURL(page, perPage, totalPages),
        },
    }
}
```

### **Day 5: API 엔드포인트 업데이트**

#### **5. HTTP 핸들러 수정**
```go
// internal/handlers/overview.go

func (h *OverviewHandler) GetDirectoryTree(w http.ResponseWriter, r *http.Request) {
    // 쿼리 파라미터 파싱
    req := &DirectoryRequest{
        RepoID:  chi.URLParam(r, "repo"),
        Path:    r.URL.Query().Get("path"),
        Ref:     r.URL.Query().Get("ref"),
    }

    // 페이지네이션 파라미터 파싱
    if pageStr := r.URL.Query().Get("page"); pageStr != "" {
        if page, err := strconv.Atoi(pageStr); err == nil && page > 0 {
            req.Page = page
        }
    }
    if req.Page == 0 {
        req.Page = 1 // 기본값
    }

    if perPageStr := r.URL.Query().Get("per_page"); perPageStr != "" {
        if perPage, err := strconv.Atoi(perPageStr); err == nil && perPage > 0 && perPage <= 100 {
            req.PerPage = perPage
        }
    }

    // 서비스 호출
    response, err := h.overviewService.GetDirectoryTreeWithCommits(r.Context(), req)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }

    // 응답 헤더 설정 (GitLab 패턴)
    if response.Pagination != nil {
        w.Header().Set("X-Total", strconv.Itoa(response.Pagination.TotalFiles))
        w.Header().Set("X-Total-Pages", strconv.Itoa(response.Pagination.TotalPages))
        w.Header().Set("X-Page", strconv.Itoa(response.Pagination.Page))
        w.Header().Set("X-Per-Page", strconv.Itoa(response.Pagination.PerPage))

        if response.Pagination.HasNextPage {
            w.Header().Set("X-Next-Page", strconv.Itoa(response.Pagination.Page+1))
        }
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(response)
}
```

### **Week 1 목표 달성 확인**
```bash
# 테스트 스크립트
#!/bin/bash
echo "=== Week 1 페이지네이션 테스트 ==="

# 소형 저장소 (전체 반환 확인)
curl "localhost:8090/repos/small-repo/overview/tree/commits" | jq '.pagination'

# 중형 저장소 (페이지네이션 적용 확인)
curl "localhost:8090/repos/medium-repo/overview/tree/commits?page=1&per_page=50" | jq '.pagination'

# 응답 시간 측정
time curl -s "localhost:8090/repos/medium-repo/overview/tree/commits" > /dev/null

echo "목표: 567ms → 200ms 달성 확인"
```

---

## 📅 Week 2: 다층 캐싱 구현 (강력한 효과)

### **Day 6-7: Redis 캐시 레이어**

#### **1. Redis 설정 및 연동**
```go
// internal/cache/redis.go
package cache

import (
    "context"
    "encoding/json"
    "time"

    "github.com/go-redis/redis/v8"
)

type RedisCache struct {
    client *redis.Client
    prefix string
}

func NewRedisCache(addr, password string, db int) *RedisCache {
    rdb := redis.NewClient(&redis.Options{
        Addr:     addr,
        Password: password,
        DB:       db,
    })

    return &RedisCache{
        client: rdb,
        prefix: "collabops:",
    }
}

func (r *RedisCache) Get(ctx context.Context, key string) (interface{}, error) {
    val, err := r.client.Get(ctx, r.prefix+key).Result()
    if err == redis.Nil {
        return nil, nil // 캐시 미스
    }
    if err != nil {
        return nil, err
    }

    // JSON 디코딩
    var result interface{}
    err = json.Unmarshal([]byte(val), &result)
    return result, err
}

func (r *RedisCache) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
    data, err := json.Marshal(value)
    if err != nil {
        return err
    }

    return r.client.Set(ctx, r.prefix+key, data, ttl).Err()
}
```

#### **2. 메모리 캐시 고도화**
```go
// internal/cache/memory.go
package cache

import (
    "container/list"
    "sync"
    "time"
)

type MemoryCache struct {
    mu       sync.RWMutex
    items    map[string]*cacheItem
    evictList *list.List
    maxSize  int
}

type cacheItem struct {
    key       string
    value     interface{}
    expiresAt time.Time
    element   *list.Element
}

func NewMemoryCache(maxSize int) *MemoryCache {
    return &MemoryCache{
        items:     make(map[string]*cacheItem),
        evictList: list.New(),
        maxSize:   maxSize,
    }
}

func (m *MemoryCache) Get(key string) interface{} {
    m.mu.Lock()
    defer m.mu.Unlock()

    item, ok := m.items[key]
    if !ok {
        return nil
    }

    // 만료 확인
    if time.Now().After(item.expiresAt) {
        m.removeItem(item)
        return nil
    }

    // LRU: 최근 사용 항목을 앞으로 이동
    m.evictList.MoveToFront(item.element)

    return item.value
}

func (m *MemoryCache) Set(key string, value interface{}, ttl time.Duration) {
    m.mu.Lock()
    defer m.mu.Unlock()

    // 기존 항목이 있으면 업데이트
    if item, ok := m.items[key]; ok {
        item.value = value
        item.expiresAt = time.Now().Add(ttl)
        m.evictList.MoveToFront(item.element)
        return
    }

    // 새 항목 추가
    item := &cacheItem{
        key:       key,
        value:     value,
        expiresAt: time.Now().Add(ttl),
    }

    item.element = m.evictList.PushFront(item)
    m.items[key] = item

    // 크기 제한 확인 및 LRU 제거
    if len(m.items) > m.maxSize {
        oldest := m.evictList.Back()
        if oldest != nil {
            m.removeItem(oldest.Value.(*cacheItem))
        }
    }
}
```

### **Day 8-9: 다층 캐시 통합**

#### **3. 통합 캐시 매니저**
```go
// internal/cache/multi_tier.go
package cache

import (
    "context"
    "fmt"
    "time"
)

type MultiTierCache struct {
    L1 *MemoryCache  // 초고속 (10분)
    L2 *RedisCache   // 중속 (1시간)

    // 성능 메트릭
    metrics *CacheMetrics
}

type CacheMetrics struct {
    L1Hits, L1Misses int64
    L2Hits, L2Misses int64
    TotalRequests    int64
}

func NewMultiTierCache(memSize int, redisAddr string) *MultiTierCache {
    return &MultiTierCache{
        L1:      NewMemoryCache(memSize),
        L2:      NewRedisCache(redisAddr, "", 0),
        metrics: &CacheMetrics{},
    }
}

func (m *MultiTierCache) GetDirectoryListing(ctx context.Context, repoID, ref, path string) (*DirectoryResponse, error) {
    m.metrics.TotalRequests++

    cacheKey := m.buildCacheKey("dir", repoID, ref, path)

    // L1: 메모리 캐시 확인 (1-5ms)
    if result := m.L1.Get(cacheKey); result != nil {
        m.metrics.L1Hits++
        return result.(*DirectoryResponse), nil
    }
    m.metrics.L1Misses++

    // L2: Redis 캐시 확인 (10-30ms)
    if result, err := m.L2.Get(ctx, cacheKey); err == nil && result != nil {
        m.metrics.L2Hits++
        response := result.(*DirectoryResponse)

        // L1에도 저장
        m.L1.Set(cacheKey, response, 10*time.Minute)
        return response, nil
    }
    m.metrics.L2Misses++

    return nil, nil // 캐시 미스
}

func (m *MultiTierCache) SetDirectoryListing(ctx context.Context, repoID, ref, path string, response *DirectoryResponse) error {
    cacheKey := m.buildCacheKey("dir", repoID, ref, path)

    // L1, L2에 모두 저장
    m.L1.Set(cacheKey, response, 10*time.Minute)
    return m.L2.Set(ctx, cacheKey, response, 1*time.Hour)
}

func (m *MultiTierCache) buildCacheKey(prefix, repoID, ref, path string) string {
    return fmt.Sprintf("%s:%s:%s:%s", prefix, repoID, ref[:8], path)
}

func (m *MultiTierCache) GetHitRatio() (float64, float64) {
    total := m.metrics.TotalRequests
    if total == 0 {
        return 0, 0
    }

    l1Ratio := float64(m.metrics.L1Hits) / float64(total)
    l2Ratio := float64(m.metrics.L2Hits) / float64(total)

    return l1Ratio, l2Ratio
}
```

### **Day 10: 서비스 레이어 캐시 통합**

#### **4. 캐시 적용된 서비스**
```go
// internal/overview/service.go 수정

func (s *Service) GetDirectoryTreeWithCommits(ctx context.Context, req *DirectoryRequest) (*DirectoryResponse, error) {
    // 캐시에서 조회 시도
    if cached, err := s.cache.GetDirectoryListing(ctx, req.RepoID, req.Ref, req.Path); err == nil && cached != nil {
        // 페이지네이션 적용해서 반환
        return s.applyPagination(cached.Files, req.Page, req.PerPage), nil
    }

    // 캐시 미스: 원본 데이터 조회
    timer := s.monitor.StartTimer(req.RepoID)
    defer timer.Stop()

    response, err := s.getDirectoryFromGit(ctx, req)
    if err != nil {
        return nil, err
    }

    // 캐시에 저장 (페이지네이션 전 전체 데이터)
    fullResponse := &DirectoryResponse{
        Files:      response.Files,
        Repository: req.RepoID,
        Path:       req.Path,
        Ref:        req.Ref,
    }
    s.cache.SetDirectoryListing(ctx, req.RepoID, req.Ref, req.Path, fullResponse)

    // 페이지네이션 적용해서 반환
    return s.applyPagination(response.Files, req.Page, req.PerPage), nil
}
```

### **Week 2 목표 달성 확인**
```bash
# 캐시 효과 테스트
echo "=== Week 2 캐싱 효과 테스트 ==="

# 첫 번째 요청 (캐시 미스)
time curl -s "localhost:8090/repos/medium-repo/overview/tree/commits" > /dev/null

# 두 번째 요청 (캐시 히트)
time curl -s "localhost:8090/repos/medium-repo/overview/tree/commits" > /dev/null

# 캐시 적중률 확인
curl "localhost:8090/metrics" | grep cache_hit_ratio

echo "목표: 캐시 히트 시 40% 성능 향상 확인"
```

---

## 📅 Week 3: 커밋 인덱싱 (근본적 해결)

### **Day 11-12: 데이터베이스 설계**

#### **1. 커밋 인덱스 테이블**
```sql
-- migrations/001_create_commit_path_index.sql
CREATE TABLE IF NOT EXISTS commit_path_index (
    id           BIGSERIAL PRIMARY KEY,
    repo_id      VARCHAR(255) NOT NULL,
    file_path    TEXT NOT NULL,
    last_commit_sha  VARCHAR(64) NOT NULL,
    commit_time      TIMESTAMP NOT NULL,
    change_type      VARCHAR(20) NOT NULL, -- 'added', 'modified', 'deleted'
    file_size        BIGINT DEFAULT 0,
    created_at       TIMESTAMP DEFAULT NOW(),
    updated_at       TIMESTAMP DEFAULT NOW(),

    UNIQUE(repo_id, file_path)
);

-- 인덱스
CREATE INDEX idx_commit_path_repo_path ON commit_path_index(repo_id, file_path);
CREATE INDEX idx_commit_path_repo_time ON commit_path_index(repo_id, commit_time DESC);
CREATE INDEX idx_commit_path_commit_sha ON commit_path_index(last_commit_sha);
```

#### **2. Go 모델 정의**
```go
// internal/models/commit_index.go
package models

import (
    "time"
    "gorm.io/gorm"
)

type CommitPathIndex struct {
    ID            uint64    `gorm:"primaryKey;autoIncrement"`
    RepoID        string    `gorm:"type:varchar(255);not null;index:idx_repo_path"`
    FilePath      string    `gorm:"type:text;not null;index:idx_repo_path"`
    LastCommitSHA string    `gorm:"type:varchar(64);not null;index"`
    CommitTime    time.Time `gorm:"not null;index:idx_repo_time"`
    ChangeType    string    `gorm:"type:varchar(20);not null"` // added, modified, deleted
    FileSize      int64     `gorm:"default:0"`
    CreatedAt     time.Time
    UpdatedAt     time.Time
}

func (CommitPathIndex) TableName() string {
    return "commit_path_index"
}
```

### **Day 13-14: 인덱스 생성 서비스**

#### **3. 인덱스 빌더**
```go
// internal/indexing/builder.go
package indexing

import (
    "context"
    "fmt"
    "log"
    "time"

    "gorm.io/gorm"
    "gorm.io/gorm/clause"
)

type IndexBuilder struct {
    db     *gorm.DB
    gitSvc GitService // Git 저장소 접근 인터페이스
}

func NewIndexBuilder(db *gorm.DB, gitSvc GitService) *IndexBuilder {
    return &IndexBuilder{
        db:     db,
        gitSvc: gitSvc,
    }
}

// 전체 저장소 인덱스 구축 (초기 구축용)
func (b *IndexBuilder) BuildFullIndex(ctx context.Context, repoID string) error {
    log.Printf("Starting full index build for repo: %s", repoID)

    // Git 히스토리 전체 조회
    commits, err := b.gitSvc.GetAllCommits(repoID)
    if err != nil {
        return fmt.Errorf("failed to get commits: %w", err)
    }

    // 배치 크기
    batchSize := 100

    for i := 0; i < len(commits); i += batchSize {
        end := i + batchSize
        if end > len(commits) {
            end = len(commits)
        }

        batch := commits[i:end]
        if err := b.processBatch(ctx, repoID, batch); err != nil {
            return fmt.Errorf("failed to process batch %d-%d: %w", i, end-1, err)
        }

        log.Printf("Processed commits %d-%d of %d", i, end-1, len(commits))
    }

    log.Printf("Completed full index build for repo: %s", repoID)
    return nil
}

func (b *IndexBuilder) processBatch(ctx context.Context, repoID string, commits []Commit) error {
    tx := b.db.Begin()
    defer func() {
        if r := recover(); r != nil {
            tx.Rollback()
        }
    }()

    for _, commit := range commits {
        for _, change := range commit.Changes {
            index := &models.CommitPathIndex{
                RepoID:        repoID,
                FilePath:      change.Path,
                LastCommitSHA: commit.SHA,
                CommitTime:    commit.Time,
                ChangeType:    string(change.Type),
                FileSize:      change.Size,
                UpdatedAt:     time.Now(),
            }

            // Upsert: 기존 경로가 있으면 업데이트, 없으면 생성
            result := tx.Clauses(clause.OnConflict{
                Columns: []clause.Column{
                    {Name: "repo_id"},
                    {Name: "file_path"},
                },
                DoUpdates: clause.AssignmentColumns([]string{
                    "last_commit_sha",
                    "commit_time",
                    "change_type",
                    "file_size",
                    "updated_at",
                }),
            }).Create(index)

            if result.Error != nil {
                tx.Rollback()
                return result.Error
            }
        }
    }

    return tx.Commit().Error
}

// 증분 인덱스 업데이트 (Push 시)
func (b *IndexBuilder) UpdateIndex(ctx context.Context, repoID string, newCommits []Commit) error {
    return b.processBatch(ctx, repoID, newCommits)
}
```

### **Day 15: 고속 조회 서비스**

#### **4. 인덱스 기반 조회 서비스**
```go
// internal/indexing/query.go
package indexing

import (
    "context"
    "fmt"

    "gorm.io/gorm"
)

type QueryService struct {
    db *gorm.DB
}

func NewQueryService(db *gorm.DB) *QueryService {
    return &QueryService{db: db}
}

// 고속 커밋 히스토리 조회
func (q *QueryService) GetLatestCommitsForPaths(ctx context.Context, repoID string, paths []string) (map[string]*CommitInfo, error) {
    if len(paths) == 0 {
        return make(map[string]*CommitInfo), nil
    }

    var indices []models.CommitPathIndex

    // 단일 쿼리로 모든 경로의 최신 커밋 조회
    err := q.db.Where("repo_id = ? AND file_path IN ?", repoID, paths).
        Find(&indices).Error
    if err != nil {
        return nil, fmt.Errorf("failed to query commit index: %w", err)
    }

    // 커밋 SHA 목록 추출
    commitSHAs := make([]string, 0, len(indices))
    shaToIndex := make(map[string]*models.CommitPathIndex)

    for i := range indices {
        commitSHAs = append(commitSHAs, indices[i].LastCommitSHA)
        shaToIndex[indices[i].LastCommitSHA] = &indices[i]
    }

    // 커밋 상세 정보 조회 (기존 Git 서비스 활용)
    commits, err := q.getCommitsBySHAs(repoID, commitSHAs)
    if err != nil {
        return nil, fmt.Errorf("failed to get commit details: %w", err)
    }

    // 결과 매핑
    result := make(map[string]*CommitInfo)
    for _, index := range indices {
        if commit, exists := commits[index.LastCommitSHA]; exists {
            result[index.FilePath] = &CommitInfo{
                SHA:         commit.SHA,
                Message:     commit.Message,
                Author:      commit.Author,
                Time:        commit.Time,
                ChangeType:  index.ChangeType,
                FileSize:    index.FileSize,
            }
        }
    }

    return result, nil
}

// 디렉토리별 파일 목록과 최신 커밋 통합 조회
func (q *QueryService) GetDirectoryWithCommits(ctx context.Context, repoID, ref, path string) ([]FileWithCommit, error) {
    // 패턴 매칭으로 디렉토리 하위 파일들 조회
    pattern := path + "%"
    if path == "" || path == "/" {
        pattern = "%"
    }

    var indices []models.CommitPathIndex
    err := q.db.Where("repo_id = ? AND file_path LIKE ?", repoID, pattern).
        Order("file_path ASC").
        Find(&indices).Error
    if err != nil {
        return nil, err
    }

    // 결과 변환
    files := make([]FileWithCommit, 0, len(indices))
    for _, index := range indices {
        files = append(files, FileWithCommit{
            Path:          index.FilePath,
            Size:          index.FileSize,
            LastCommitSHA: index.LastCommitSHA,
            CommitTime:    index.CommitTime,
            ChangeType:    index.ChangeType,
        })
    }

    return files, nil
}
```

### **Week 3 목표 달성 확인**
```bash
# 인덱싱 효과 테스트
echo "=== Week 3 인덱싱 효과 테스트 ==="

# 인덱스 구축
curl -X POST "localhost:8090/admin/rebuild-index/medium-repo"

# 커밋 히스토리 조회 성능 측정
time curl -s "localhost:8090/repos/medium-repo/overview/latest-commits" > /dev/null

echo "목표: 8,450ms → 200ms 달성 확인"
```

---

## 🎯 3주 후 예상 결과

### **성능 개선 목표 달성 확인**

```bash
#!/bin/bash
echo "=== Phase 2 완료 후 성능 검증 ==="

# 중형 저장소 (목표: 567ms → 150ms)
echo "중형 저장소 성능 테스트:"
time curl -s "localhost:8090/repos/medium-repo/overview/tree/commits?per_page=50" > /dev/null

# 대형 저장소 (목표: 2,340ms → 300ms)
echo "대형 저장소 성능 테스트:"
time curl -s "localhost:8090/repos/large-repo/overview/tree/commits?per_page=20" > /dev/null

# 동시 사용자 (목표: 834ms → 300ms)
echo "동시 사용자 테스트:"
seq 1 10 | xargs -I {} -P 10 bash -c 'time curl -s "localhost:8090/repos/medium-repo/overview/tree/commits" > /dev/null'

# 캐시 효과 확인
echo "캐시 적중률 확인:"
curl -s "localhost:8090/metrics" | grep cache_hit_ratio
```

### **최종 경쟁사 대비 성능**

| 시나리오 | CollabOps (현재) | CollabOps (Phase 2 후) | GitHub | GitLab | 순위 |
|----------|------------------|-------------------------|---------|---------|------|
| **중형 저장소** | 567ms | 150ms | 300ms | 187ms | 🥇 1위 |
| **대형 저장소** | 2,340ms | 300ms | 300ms | 300ms | 🥇 공동 1위 |
| **동시 사용자** | 834ms | 300ms | 512ms | 267ms | 🥈 2위 |

**Result**: Pack/Unpack 최적화 없이도 **모든 영역에서 경쟁사 수준 이상** 달성! 🏆