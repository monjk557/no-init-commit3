## 결과 요약 (Tree/Commits 분리 + tail 제거)

- **핵심 결론(S1 실측 pprof)**: 병목은 매칭(`diffMatchesPath`/`HasPrefix`)이 아니라 **오브젝트 read(I/O) + `DiffTree`**
  - `filesystem.(*ObjectStorage).EncodedObject` cum **~35–39%**
  - `os.OpenFile` cum **~33–37%**
  - `plumbing/object.DiffTree` cum **~18–21%**
  - `(*Repository).BlobObject` cum **~8%**
  - `diffMatchesPath`/`strings.HasPrefix` **top200 미등장(<~0.5%)**

- **따라서 "9초 tail 제거"는**:
  - **강제 상한(time budget / max commits scanned)**으로 "더 이상 오래 돌지 못하게" 자르거나
  - **캐시(singleflight 포함)**로 동일 입력 반복을 흡수하는 방향이 1차 처방

---

## 구현 변경점

### Phase 1: Tree/Commits 분리 + batch endpoint

- **신규 endpoint (commits 분리)**
  - **Route**: `POST /repos/:owner/:repo/overview/latest-commits`
  - **Request**: `{ ref, dir_path, paths[] }`
  - **Response**: `{ commits_by_path, meta{resolved_ref_hash, incomplete, scanned_commits, time_budget_ms, cut_reason}, missing_paths[] }`
  - **코드**
    - handler: `internal/overview/handler.go:HandlePostLatestCommitsBatch`
    - schema: `internal/overview/schema.go`
    - route: `internal/server/server.go`

- **tail 제거용 강제 상한(Partial 허용; tuning)**
  - env:
    - `OVERVIEW_LATEST_COMMITS_TIME_BUDGET_MS=<ms>` (default **300**; batch endpoint용)
    - `OVERVIEW_LATEST_COMMITS_MAX_COMMITS=<n>` (default **10000**; batch endpoint용)
  - 적용:
    - **batch endpoint**: `internal/overview/service.go:GetLatestCommitsForPathsWithMeta`
    - **결합 endpoint(`/overview/tree/commits`)**: N-bucket caps 적용 (아래 Phase 2 참조)

- **캐시(singleflight + resolvedRefHash key)**
  - `internal/overview/overview_cache.go`
    - `latest_commits_batch` 캐시 추가
    - key: `(repoPath, resolvedRefHash, dirPath|pathsHash)`
    - **complete 결과만 캐시**(partial은 캐시 안 함)

- **OpenAPI 스펙 정합화**
  - `specs/generated/collabops-git-executor.yaml`
    - `GET /repos/{owner}/{repo}/overview/tree/commits`: query `ref`, `path` 추가
    - `POST /repos/{owner}/{repo}/overview/latest-commits` + schemas 추가

### Phase 2: Uncapped 완전 금지 + N-bucket caps + 인덱스 레이어

- **Uncapped path 완전 제거**
  - `GetTreeWithCommit`이 이제 항상 `GetLatestCommitsForPathsWithMeta`(capped)를 사용
  - 기존 uncapped `GetLatestCommitsForPaths`는 핸들러에서 호출되지 않음 (테스트/벤치 전용)
  - 반환값: `([]GitTreeEntryWithCommit, LatestCommitsMeta, error)` — Meta로 partial 상태 전파

- **N-bucket caps (디렉터리 크기 기반 time budget)**
  - `internal/overview/perf_config.go`: `timeBudgetForEntryCount(n)` / `maxCommitsForEntryCount(n)`
  - env override: `OVERVIEW_TREE_COMMITS_TIME_BUDGET_MS`, `OVERVIEW_TREE_COMMITS_MAX_COMMITS`
  - 버킷 규칙:

  | N (entries) | Time Budget | Max Commits |
  |---:|---:|---:|
  | ≤ 50 | 500ms | 20,000 |
  | ≤ 200 | 400ms | 15,000 |
  | ≤ 500 | 300ms | 10,000 |
  | ≤ 1,000 | 200ms | 5,000 |
  | > 1,000 | 150ms | 5,000 |

- **CommitIndex (인덱스 레이어)**
  - `internal/overview/commit_index.go`
  - Key: `(repoPath, refHash, dirPath)` → Value: `map[path]*GitCommit`
  - LRU in-memory store
  - **Lookup hit**: "조회"로 즉시 반환 (계산 없음; `cut_reason=index_hit`)
  - **Miss + incomplete**: 백그라운드로 generous budget(5s/100k commits)으로 완전 계산 후 저장
  - **Miss + complete**: 즉시 인덱스에 저장 → 다음 요청부터 instant hit
  - Invalidation: refHash 변경 시 자동 miss

- **Meta 전파**
  - `TreeWithCommitOverviewResponse.Meta *LatestCommitsMeta` 필드 추가
  - 클라이언트가 `meta.incomplete`, `meta.cut_reason`으로 UI placeholder/재시도 결정

### Phase 3: `/overview` 핸들러 i/o timeout 해결 (병렬화 + CountCommits cap)

- **원인**: `GET /overview`에서 `CountCommits`(상한 없이 전체 히스토리 순회) + `GetTreeWithCommit` + Tags + README를 **순차 실행** → WriteTimeout(30s) 초과 → `write tcp i/o timeout`

- **CountCommitsWithCap 추가** (`internal/commits/service.go`)
  - `CountCommitsWithCap(rw, ctx, ref, maxCount)` → `CommitCountResult{Count, Approximate}`
  - `maxCount=100,000`: 10만 커밋 이상이면 즉시 중단, `Approximate=true`
  - 기존 `CountCommits`는 내부에서 cap=0 호출 (하위 호환)

- **`/overview` 핸들러 완전 병렬화** (`internal/overview/handler.go`)
  - **Before**: GetCommits → CountCommits → GetTreeWithCommit → GetTags → GetRemotes → README (순차)
  - **After**: 6개 작업을 `sync.WaitGroup`으로 병렬 실행

  ```
  ┌ GetCommits(1,0)                        ~10ms
  ├ CountCommitsWithCap(100k) + cache      ~200ms (max; 2회째부터 cache hit)
  ├ GetTreeWithCommit (N-bucket caps)      ~50-500ms
  ├ GetTags                                ~10ms
  ├ GetRemotes                             ~1ms
  └ GetBlobContent(README)                 ~10ms
  Total: max(개별) = ~200-500ms (이전: sum = 5-30s+)
  ```

- **Request-level timeout**: `context.WithTimeout(ctx, 15s)` — 어떤 sub-task도 15s 초과 불가

- **CommitCount cache**: refHash 기반 `OverviewCache.SetCommitCount` / `GetCommitCount` 연결

- **`CommitsApproximate` flag**: `RepositoryCount.CommitsApproximate bool` — UI에서 "100,000+" 표시

### Phase 3.5: Micro-optimizations (alloc 절감)

- **Double ResolveRevision 제거**: `GetTreeWithCommit`에서 `ResolveRevision` 2번→1번 (I/O 1회 절약)
- **`fmt.Sprintf("%o")` → `strconv.FormatUint`**: per-entry alloc 제거 (N=10k → 10k개 fmt 버퍼 절약)
- **Sort key pre-compute**: `entryTypeSortKey` int 필드로 O(N log N) 비교마다 switch 제거
- **`blobSizeParallelism()` 호이스트**: env parse를 entry 루프 밖으로 (N=10k → 10k회→1회)

---

## 실측(벤치) — M2 Pro 기준

### S1 N-Heavy (N=10,000 entries, C=200 commits)

| 벤치마크 | Latency/op | Alloc/op | Allocs/op | 개선 |
|---|---:|---:|---:|---|
| **Uncapped** | **1,220ms** | **1.76GB** | **32.2M** | baseline |
| **TimeBudget=50ms (caps)** | **52ms** | **94MB** | **1.71M** | **23x↓ latency, 18x↓ memory** |
| **Cache warm-hit** | **0.37µs** | **400B** | **6** | **3,300,000x↓ latency** |

### S2 C-Heavy (pending=200, C=2,000 commits, F=10)

| 벤치마크 | Latency/op | Alloc/op | 개선 |
|---|---:|---:|---|
| Uncapped | 843ms | 452MB | baseline |
| TimeBudget=50ms | **50ms** | **27MB** | **17x↓ latency** |

### S3 F-Heavy (pending=50, C=200, F=200 changes/commit)

| 벤치마크 | Latency/op | Alloc/op | 개선 |
|---|---:|---:|---|
| Uncapped | 102ms | 81MB | baseline |
| TimeBudget=50ms | **50ms** | **39MB** | **2x↓ latency** |

### GetTreeWithCommit Full (N=2,000, C=200)

| 벤치마크 | Latency/op | Alloc/op | Allocs/op |
|---|---:|---:|---:|
| Full (N-bucket caps) | **186ms** | **215MB** | **4.57M** |
| Tree only (no commits) | 0.28ms | 0.36MB | 6.2K |
| BlobSize only (sequential) | 32ms | 2MB | 28.5K |
| BlobSize only (parallel=8) | 19ms | 2.3MB | 32.3K |

### `/overview` handler (end-to-end wall time)

| Before (순차, WriteTimeout=30s) | After (병렬, ctx timeout=15s) |
|---|---|
| `CountCommits(uncapped)` 5-30s+ | `CountCommitsWithCap(100k)` ≤200ms |
| + `GetTreeWithCommit(uncapped)` 200ms-1s+ | + `GetTreeWithCommit(N-bucket caps)` 50-500ms |
| + Tags + README + Remotes 순차 합산 | 모든 작업 병렬 실행 |
| **Total: sum = 5-30s+ → i/o timeout** | **Total: max(개별) = 200-500ms** |

---

## pprof 확인(상한 ON/OFF)

- **원칙(추천1 반영)**: pprof는 **%가 아니라 "절대시간(cum seconds) + Total samples"**로 비교한다. (%는 전체 work가 달라지면 의미가 쉽게 왜곡됨)
- S1 latest-commits(meta API) `-benchtime=10s -cpuprofile`:
  - uncapped: `Duration 17.03s`, `Total samples 22.37s`
  - timeBudget=50ms: `Duration 25.80s`, `Total samples 32.81s`
- 핵심 지배항(절대시간; cum seconds)
  - uncapped:
    - `plumbing/object.DiffTree` **4.06s**
    - `os.OpenFile` **4.00s**
    - `filesystem.(*ObjectStorage).EncodedObject` **3.25s**
  - timeBudget=50ms:
    - `plumbing/object.DiffTree` **7.62s**
    - `os.OpenFile` **4.39s**
    - `filesystem.(*ObjectStorage).EncodedObject` **2.61s**

> 해석: caps는 "핫스팟의 종류를 바꾸는" 게 아니라, **해당 루프를 더 이상 오래 못 돌게 잘라서 tail을 제거**한다. (정상 속도 p95/p99는 cache로 흡수하는 방향)

---

## 동시성 레버(A): Blob size fill 병렬화(결과)

- **변경(코드)**: `internal/overview/service.go:GetTreeWithCommit`에서 `BlobObject` size fill을 bounded parallelism로 수행(설정값)
  - env:
    - `OVERVIEW_BLOB_SIZE_PARALLELISM=1`이면 순차(기본)
    - `OVERVIEW_BLOB_SIZE_PARALLELISM=<n>` (예: 4~8)이면 병렬
- **S1 BlobObject-only 벤치(동일 작업 기준; 2000 blobs)**
  - `Benchmark_BlobSizeFillOnly_S1_NHeavy`(sequential): **34.49ms/op**, **2.10MB/op**, **28,549 allocs/op**
  - `Benchmark_BlobSizeFillOnly_S1_NHeavy_Parallel`(parallel=8): **19.01ms/op**, **2.35MB/op**, **32,294 allocs/op**
- **S1 Full 벤치(End-to-end)**
  - `Benchmark_GetTreeWithCommit_Full_S1_NHeavy` OFF: **287.80ms/op**
  - `Benchmark_GetTreeWithCommit_Full_S1_NHeavy` ON(parallel=8): **274.68ms/op** (약 **-4.6%**)

> 해석: "동시성 부족" 레버는 blob size fill 구간에서 **wall-time 개선**이 관측되지만, pprof 지배항(`OpenFile/EncodedObject` + `DiffTree`)을 근본적으로 제거하지는 못하므로 **단독 처방은 아님**(caps/cache와 병행).

---

## Prometheus 메트릭 (운영 관측)

### 기존 메트릭

| 메트릭 | 라벨 | 용도 |
|---|---|---|
| `git_executor_overview_cache_ops_total` | `kind`, `op` (hit/miss/set) | 캐시 hit/miss rate |
| `git_executor_overview_cache_entries` | `kind` | 캐시 크기 |
| `git_executor_latest_commits_batch_requests_total` | `outcome` | batch endpoint 결과 분포 |
| `git_executor_latest_commits_batch_duration_seconds` | `outcome` | batch endpoint latency |
| `git_executor_latest_commits_batch_cut_reason_total` | `reason` | incomplete 사유 분포 |

### 신규 메트릭

| 메트릭 | 라벨 | 용도 |
|---|---|---|
| `git_executor_tree_with_commit_requests_total` | `outcome` (complete/incomplete/error/index_hit) | tree-with-commit endpoint 결과 분포 |
| `git_executor_tree_with_commit_duration_seconds` | `outcome` | tree-with-commit latency 히스토그램 |
| `git_executor_tree_with_commit_entries` | `outcome` | N (entry count) 분포 |
| `git_executor_commit_index_ops_total` | `op` (hit/miss/populate/evict) | 인덱스 hit rate |
| `git_executor_commit_index_entries` | — | 인덱스 크기 |

### PromQL 예시

```promql
# Cache hit rate (latest-commits batch)
rate(git_executor_overview_cache_ops_total{kind="latest_commits_batch",op="hit"}[5m])
/ rate(git_executor_overview_cache_ops_total{kind="latest_commits_batch",op=~"hit|miss"}[5m])

# Incomplete rate (tree-with-commit)
rate(git_executor_tree_with_commit_requests_total{outcome="incomplete"}[5m])
/ rate(git_executor_tree_with_commit_requests_total[5m])

# Index hit rate
rate(git_executor_commit_index_ops_total{op="hit"}[5m])
/ rate(git_executor_commit_index_ops_total{op=~"hit|miss"}[5m])
```

---

## 실행 방법(재현)

- **latest-commits 상한 ON**
  - `OVERVIEW_LATEST_COMMITS_TIME_BUDGET_MS=300` (예시)
  - `OVERVIEW_LATEST_COMMITS_MAX_COMMITS=10000` (예시)

- **tree-commits N-bucket caps override**
  - `OVERVIEW_TREE_COMMITS_TIME_BUDGET_MS=500` (전역 override; 설정 안 하면 N-bucket 자동)
  - `OVERVIEW_TREE_COMMITS_MAX_COMMITS=20000` (전역 override)

- **벤치 실행**
  - `go test ./internal/overview -run '^$' -bench 'Benchmark_LatestCommitsCaps_S1_NHeavy_.*|Benchmark_LatestCommitsCache_S1_NHeavy_WarmHit' -benchtime=3s -count=1 -benchmem`
  - `go test ./internal/overview -run '^$' -bench 'Benchmark_GetTreeWithCommit_Full_S1' -benchtime=3s -count=3 -benchmem`

---

## p95/p99 샘플링 테스트(latest-commits)

- 테스트: `internal/overview/handler_perf_test.go`
  - `Test_LatestCommitsBatch_Performance_TimeBudget50ms_P99Under1500ms`
- 조건:
  - repo: C-heavy synthetic (pending=200, commits=2000, noiseFiles=10)
  - `timeBudget=50ms` (partial 허용)
  - warm 30회 반복
- 결과(실측):
  - p50 **50.63ms**
  - p95 **50.95ms**
  - p99 **51.26ms**

---

## 남은 작업(다음)

- **정책/제품**
  - incomplete/partial 시 UI placeholder 처리 및 재요청 전략 확정
  - maxCommits bucket 기준(레포 규모/트래픽) 운영 데이터로 튜닝
- **성능**
  - 인덱스 background population의 운영 효과 관측 (index_hit rate)
  - hit-rate 낮으면 → background warmup 주기 추가 또는 인덱스 TTL 조정
  - incomplete-rate 높으면 → caps/bucket/paths batch 크기 조정
