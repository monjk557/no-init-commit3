# Phase 2: CollabOps 성능 개선 구체적 아키텍처 설계

## 🎯 개선 목표

**현재 성능 격차를 Pack/Unpack 최적화 없이 해결**

| 영역 | 현재 격차 | 목표 성능 | 우선순위 |
|------|----------|----------|----------|
| **중형 저장소** | 2-3배 느림 | GitLab 수준 (200ms) | 🔴 최우선 |
| **동시 사용자** | 2-4배 느림 | GitLab 수준 (10명 동시) | 🔴 최우선 |
| **대형 저장소** | 6-10배 느림 | GitHub 수준 (300ms) | 🟡 중기 |

**제약사항**: Git Pack/Unpack 관련 최적화는 다른 팀에서 처리 중이므로 제외

---

## 🏗️ 핵심 개선 전략

### **전략 1: 스마트 페이지네이션 (즉시 효과)**

#### **문제 분석**
```
현재: 무제한 응답 → 대형 디렉토리에서 기하급수적 성능 저하
GitLab: 강제 페이지네이션 (20-100개) → 일관된 200ms 성능
```

#### **구체적 구현 설계**

```go
// 1. 응답 크기 기반 자동 페이지네이션
type SmartPagination struct {
    MaxItems     int           `default:"100"`
    MaxSizeBytes int64         `default:"1048576"` // 1MB
    ResponseTime time.Duration `default:"500ms"`   // 타임아웃 기반
}

// 2. 적응형 페이지 크기 결정
func (s *Service) GetDirectoryWithPagination(req *DirectoryRequest) (*DirectoryResponse, error) {
    // 저장소 크기 기반 초기 페이지 크기 예측
    repoSize := s.getRepositorySize(req.RepoID)

    var pageSize int
    switch {
    case repoSize < 100:
        pageSize = 100  // 소형: 전체 반환
    case repoSize < 1000:
        pageSize = 50   // 중형: 적절한 크기
    default:
        pageSize = 20   // 대형: GitLab 패턴
    }

    // 실시간 성능 기반 조정
    if s.monitor.GetAvgResponseTime() > 300*time.Millisecond {
        pageSize = pageSize / 2  // 성능 저하시 페이지 크기 감소
    }

    return s.getDirectoryPage(req.Path, req.Page, pageSize)
}
```

**예상 효과**: 중형 저장소 응답시간 567ms → 200ms (65% 개선)

---

### **전략 2: 다층 지능형 캐싱 (강력한 효과)**

#### **문제 분석**
```
현재: 기본 LRU 캐시 (90% 적중률)
경쟁사: 다층 캐시 + CDN 효과
기회: 캐시 미스 시에만 Git 직접 조회
```

#### **구체적 구현 설계**

```go
// 1. 3-Tier 캐시 아키텍처
type MultiTierCache struct {
    L1 *MemoryCache    // 초고속 인메모리 (최근 10분)
    L2 *RedisCache     // 중속 Redis (최근 1시간)
    L3 *DatabaseCache  // 저속 DB (커밋 메타데이터)
}

// 2. 지능형 캐시 키 설계
func (c *MultiTierCache) GetDirectoryListing(repoID, ref, path string) (*DirectoryListing, error) {
    cacheKey := fmt.Sprintf("dir:%s:%s:%s", repoID, ref[:8], path)

    // L1: 메모리 캐시 확인 (1-5ms)
    if result := c.L1.Get(cacheKey); result != nil {
        return result, nil
    }

    // L2: Redis 캐시 확인 (10-30ms)
    if result := c.L2.Get(cacheKey); result != nil {
        c.L1.Set(cacheKey, result, 10*time.Minute)
        return result, nil
    }

    // L3: 사전 인덱싱된 메타데이터 확인 (50-100ms)
    if result := c.L3.GetDirectoryMeta(repoID, ref, path); result != nil {
        c.L2.Set(cacheKey, result, 1*time.Hour)
        c.L1.Set(cacheKey, result, 10*time.Minute)
        return result, nil
    }

    // 캐시 미스: Git 직접 조회 (최후 수단)
    result := c.getFromGit(repoID, ref, path)
    c.storeInAllLevels(cacheKey, result)
    return result, nil
}

// 3. 선제적 캐싱 (AI 예측 기반)
type PredictiveCache struct {
    accessPatterns map[string]*AccessPattern
    prefetchQueue  chan PrefetchTask
}

func (p *PredictiveCache) LearnAccessPattern(userID, repoID, path string) {
    pattern := p.accessPatterns[userID]

    // 사용자별 접근 패턴 학습
    if pattern.PredictNextAccess(path) {
        // 다음에 접근할 가능성이 높은 경로 선제적 로딩
        go p.prefetchRelatedPaths(repoID, path)
    }
}
```

**예상 효과**: 캐시 적중률 90% → 98%, 평균 응답시간 40% 개선

---

### **전략 3: 커밋 히스토리 인덱싱 (근본적 해결)**

#### **문제 분석**
```
현재: O(C×N×F) 알고리즘 → 커밋 수 증가시 기하급수적 저하
해결: 사전 인덱싱으로 O(1) 조회 달성
```

#### **구체적 구현 설계**

```go
// 1. 커밋 메타데이터 인덱스 테이블
type CommitPathIndex struct {
    RepoID      string    `db:"repo_id"`
    FilePath    string    `db:"file_path"`
    LastCommit  string    `db:"last_commit_sha"`
    CommitTime  time.Time `db:"commit_time"`
    ChangeType  string    `db:"change_type"` // added, modified, deleted
    UpdatedAt   time.Time `db:"updated_at"`
}

// 2. 실시간 인덱스 업데이트 (Push 시)
func (s *IndexService) UpdateOnPush(repoID string, commits []Commit) error {
    tx := s.db.Begin()
    defer tx.Rollback()

    for _, commit := range commits {
        // 변경된 파일들의 인덱스 업데이트
        for _, change := range commit.Changes {
            index := &CommitPathIndex{
                RepoID:     repoID,
                FilePath:   change.Path,
                LastCommit: commit.SHA,
                CommitTime: commit.Time,
                ChangeType: change.Type,
                UpdatedAt:  time.Now(),
            }

            // Upsert: 기존 경로면 업데이트, 없으면 생성
            tx.Clauses(clause.OnConflict{
                Columns:   []clause.Column{{Name: "repo_id"}, {Name: "file_path"}},
                DoUpdates: clause.AssignmentColumns([]string{"last_commit", "commit_time", "updated_at"}),
            }).Create(index)
        }
    }

    return tx.Commit().Error
}

// 3. 고속 커밋 히스토리 조회
func (s *Service) GetLatestCommitsForPaths(repoID string, paths []string) (map[string]Commit, error) {
    results := make(map[string]Commit)

    // 인덱스에서 일괄 조회 (단일 SQL 쿼리)
    var indices []CommitPathIndex
    err := s.db.Where("repo_id = ? AND file_path IN ?", repoID, paths).Find(&indices).Error
    if err != nil {
        return nil, err
    }

    // 커밋 SHA 목록으로 커밋 정보 조회
    commitSHAs := extractUniqueSHAs(indices)
    commits := s.getCommitsBySHAs(repoID, commitSHAs)

    // 결과 매핑
    for _, index := range indices {
        results[index.FilePath] = commits[index.LastCommit]
    }

    return results, nil
}
```

**예상 효과**: 커밋 히스토리 조회 8,450ms → 200ms (97% 개선)

---

### **전략 4: 동시 요청 처리 최적화**

#### **문제 분석**
```
현재: 10명 동시 → 834ms (GitLab 대비 3배 느림)
원인: 커넥션 풀링 부족, 리소스 경합, 잠금 경쟁
```

#### **구체적 구현 설계**

```go
// 1. 지능형 Git 저장소 커넥션 풀
type RepositoryPool struct {
    pools     sync.Map            // repo별 커넥션 풀
    config    *PoolConfig
    monitor   *PerformanceMonitor
}

type PoolConfig struct {
    MaxConnections    int           `default:"10"`
    IdleTimeout      time.Duration `default:"5m"`
    MaxLifetime      time.Duration `default:"30m"`
    HealthCheckInterval time.Duration `default:"1m"`
}

func (p *RepositoryPool) GetRepository(repoID string) (*git.Repository, error) {
    pool, _ := p.pools.LoadOrStore(repoID, &sync.Pool{
        New: func() interface{} {
            repo, _ := git.PlainOpen(p.getRepoPath(repoID))
            return repo
        },
    })

    repo := pool.(*sync.Pool).Get().(*git.Repository)

    // 커넥션 유효성 검사
    if !p.isHealthy(repo) {
        repo, _ = git.PlainOpen(p.getRepoPath(repoID))
    }

    return repo, nil
}

// 2. 요청별 리소스 격리
type RequestIsolation struct {
    semaphore  *semaphore.Weighted // 동시 요청 수 제한
    cpuQuota   *ResourceQuota      // CPU 사용량 제한
    memQuota   *ResourceQuota      // 메모리 사용량 제한
}

func (r *RequestIsolation) ExecuteWithLimits(ctx context.Context, fn func() error) error {
    // 세마포어로 동시성 제한
    if err := r.semaphore.Acquire(ctx, 1); err != nil {
        return err
    }
    defer r.semaphore.Release(1)

    // 리소스 모니터링과 함께 실행
    return r.executeWithResourceMonitoring(ctx, fn)
}

// 3. 비동기 응답 처리
type AsyncResponseHandler struct {
    workers    chan struct{}       // 워커 풀
    responseDB *ResponseCache      // 응답 캐시
}

func (h *AsyncResponseHandler) HandleLargeRequest(req *LargeDirectoryRequest) (*AsyncResponse, error) {
    taskID := generateTaskID()

    // 백그라운드에서 처리
    go func() {
        result := h.processLargeDirectory(req)
        h.responseDB.Store(taskID, result, 10*time.Minute)
    }()

    // 즉시 응답 (비동기)
    return &AsyncResponse{
        TaskID: taskID,
        Status: "processing",
        EstimatedCompletion: time.Now().Add(5 * time.Second),
    }, nil
}
```

**예상 효과**: 10명 동시 834ms → 300ms (64% 개선)

---

## 📊 구현 로드맵

### **Phase 2.1: 빠른 승리 (2-3주)**

#### **Week 1: 스마트 페이지네이션**
```go
// 구현 범위
- DirectoryResponse 구조체에 페이지네이션 필드 추가
- 자동 페이지 크기 결정 로직 구현
- 기존 API 호환성 유지하며 점진적 적용

// 예상 효과
- 중형 저장소: 567ms → 200ms (65% 개선)
- 대형 저장소: 2,340ms → 800ms (66% 개선)
```

#### **Week 2: 다층 캐싱 구현**
```go
// 구현 범위
- Redis 연동 L2 캐시 구현
- 메모리 L1 캐시 고도화
- 캐시 키 설계 및 TTL 최적화

// 예상 효과
- 반복 요청: 90% → 98% 캐시 적중률
- 평균 응답시간: 40% 개선
```

#### **Week 3: 성능 측정 및 튜닝**
```go
// 구현 범위
- 실시간 성능 모니터링 구현
- A/B 테스트로 효과 검증
- 파라미터 튜닝 및 최적화
```

### **Phase 2.2: 구조적 개선 (4-6주)**

#### **Week 4-5: 커밋 인덱싱 시스템**
```go
// 구현 범위
- CommitPathIndex 테이블 설계 및 구현
- Push 훅으로 실시간 인덱스 업데이트
- 기존 저장소 배치 인덱싱

// 예상 효과
- 커밋 히스토리: 8,450ms → 200ms (97% 개선)
```

#### **Week 6: 동시성 최적화**
```go
// 구현 범위
- Git 저장소 커넥션 풀링
- 요청별 리소스 격리
- 비동기 응답 처리 구현

// 예상 효과
- 동시 사용자: 834ms → 300ms (64% 개선)
```

---

## 🎯 성능 목표 및 검증

### **단계별 성능 목표**

| 구현 단계 | 중형 저장소 | 대형 저장소 | 동시 사용자 (10명) |
|-----------|-------------|-------------|-------------------|
| **현재** | 567ms | 2,340ms | 834ms |
| **2.1 완료 후** | 200ms | 800ms | 600ms |
| **2.2 완료 후** | 150ms | 300ms | 300ms |
| **목표 (GitLab 수준)** | 187ms | 300ms | 267ms |

### **검증 방법**

#### **1. 자동화된 벤치마크**
```bash
# 기존 벤치마킹 시스템 활용
cd docs/benchmarking/infrastructure
./benchmark-runner/run-all-benchmarks.sh

# Phase 2 전후 성능 비교
python compare_performance.py baseline/ phase2-results/
```

#### **2. 실시간 모니터링**
```go
// Prometheus + Grafana 메트릭
type PerformanceMetrics struct {
    ResponseTime     histogram.Histogram
    CacheHitRatio   gauge.Gauge
    ConcurrentUsers counter.Counter
    ErrorRate       gauge.Gauge
}
```

#### **3. A/B 테스트**
```go
// 기능 플래그로 단계적 배포
if featureFlag.IsEnabled("phase2-pagination", userID) {
    return s.getDirectoryWithPagination(req)
} else {
    return s.getDirectoryLegacy(req)
}
```

---

## 🚀 구현 우선순위

### **우선순위 1: 페이지네이션 (즉시 효과)**
- ✅ **즉시 구현 가능**: 기존 코드 최소 변경
- ✅ **큰 효과**: 중형 저장소 65% 개선
- ✅ **위험도 낮음**: GitLab 검증된 패턴

### **우선순위 2: 다층 캐싱 (강력한 효과)**
- ✅ **높은 ROI**: 40% 전체 성능 개선
- ✅ **확장성**: 사용자 증가에도 성능 유지
- ⚠️ **Redis 의존성**: 추가 인프라 필요

### **우선순위 3: 커밋 인덱싱 (근본적 해결)**
- ✅ **근본 해결**: O(C×N×F) 문제 완전 해결
- ✅ **최고 효과**: 97% 성능 개선
- ⚠️ **복잡성**: 데이터베이스 설계 필요

### **우선순위 4: 동시성 최적화 (확장성)**
- ✅ **비즈니스 중요**: 사용자 증가 대응
- ✅ **안정성**: 시스템 안정성 향상
- ⚠️ **복잡성**: 리소스 관리 복잡

---

## 🏁 예상 최종 결과

### **Phase 2 완료 후 예상 성능**

```
🏆 CollabOps vs 경쟁사 (Phase 2 후)

소형 저장소 (100개):
✅ CollabOps: 234ms (유지)
✅ GitHub: 300ms
✅ GitLab: 142ms
→ 2위 유지 (강점 영역)

중형 저장소 (1K개):
✅ CollabOps: 150ms (567ms → 74% 개선)
✅ GitHub: 300ms
✅ GitLab: 187ms
→ 1위 달성! 🥇

대형 저장소 (10K+):
✅ CollabOps: 300ms (2,340ms → 87% 개선)
✅ GitHub: 300ms
✅ GitLab: 300ms (4페이지)
→ 동등 수준 달성! 🥇

동시 사용자 (10명):
✅ CollabOps: 300ms (834ms → 64% 개선)
✅ GitHub: 512ms
✅ GitLab: 267ms
→ 2위 달성! 🥈
```

### **전략적 포지셔닝 변화**

```
Phase 1 후: "소형 특화 1위"
Phase 2 후: "전 영역 경쟁력 + AI 차별화"

핵심 가치:
✅ 모든 규모에서 경쟁력 있는 성능
✅ AI 기반 지능형 최적화 (유일)
✅ 가장 효율적인 리소스 사용
✅ 개발자 친화적 빠른 응답
```

**Result**: Pack/Unpack 최적화 없이도 **모든 경쟁사와 동등하거나 우위** 달성! 🎯