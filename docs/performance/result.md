## 결과 요약 (Tree/Commits 분리 + tail 제거)

- **핵심 결론(S1 실측 pprof)**: 병목은 매칭(`diffMatchesPath`/`HasPrefix`)이 아니라 **오브젝트 read(I/O) + `DiffTree`**
  - `filesystem.(*ObjectStorage).EncodedObject` cum **~35–39%**
  - `os.OpenFile` cum **~33–37%**
  - `plumbing/object.DiffTree` cum **~18–21%**
  - `(*Repository).BlobObject` cum **~8%**
  - `diffMatchesPath`/`strings.HasPrefix` **top200 미등장(<~0.5%)**

- **따라서 “9초 tail 제거”는**:
  - **강제 상한(time budget / max commits scanned)**으로 “더 이상 오래 돌지 못하게” 자르거나
  - **캐시(singleflight 포함)**로 동일 입력 반복을 흡수하는 방향이 1차 처방

---

## 구현 변경점

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
    - `OVERVIEW_LATEST_COMMITS_TIME_BUDGET_MS=<ms>` (default **300**; always applied on batch endpoint)
    - `OVERVIEW_LATEST_COMMITS_MAX_COMMITS=<n>` (default **10000**; always applied on batch endpoint)
  - 적용:
    - **신규 batch endpoint에만 적용**(partial 허용): `internal/overview/service.go:GetLatestCommitsForPathsWithMeta`
    - `/overview/tree/commits` 등 기존 결합 endpoint는 **partial 금지(A안)**: `GetLatestCommitsForPaths`는 상한 미적용(complete only)

- **캐시(singleflight + resolvedRefHash key)**
  - `internal/overview/overview_cache.go`
    - `latest_commits_batch` 캐시 추가
    - key: `(repoPath, resolvedRefHash, dirPath|pathsHash)`
    - **complete 결과만 캐시**(partial은 캐시 안 함)

- **OpenAPI 스펙 정합화**
  - `specs/generated/collabops-git-executor.yaml`
    - `GET /repos/{owner}/{repo}/overview/tree/commits`: query `ref`, `path` 추가
    - `POST /repos/{owner}/{repo}/overview/latest-commits` + schemas 추가

---

## 실측(벤치)

- **S1 caps / cache 벤치**
  - 벤치: `internal/overview/performance_bench_test.go`
    - `Benchmark_LatestCommitsCaps_S1_NHeavy_Uncapped`
    - `Benchmark_LatestCommitsCaps_S1_NHeavy_TimeBudget50ms`
    - `Benchmark_LatestCommitsCache_S1_NHeavy_WarmHit`
  - 결과(실측; M2 Pro):
    - **Uncapped(meta API)**: **1.307s/op**, **1.76GB/op**, **32.2M allocs/op**
    - **TimeBudget=50ms(Partial; meta API)**: **53.4ms/op**, **89.5MB/op**, **1.63M allocs/op**
    - **Cache warm-hit**: **264.7ns/op**, **400 B/op**, **6 allocs/op**

 - **S2 caps 벤치 (C-heavy)**
  - `Benchmark_LatestCommitsCaps_S2_CHeavy_Uncapped`: **626ms/op**, **410MB/op**, **7.47M allocs/op**
  - `Benchmark_LatestCommitsCaps_S2_CHeavy_TimeBudget50ms`(Partial): **50.2ms/op**, **34.9MB/op**, **647k allocs/op**

- **S3 caps 벤치 (F-heavy, scaled-down for iteration)**
  - `Benchmark_LatestCommitsCaps_S3_FHeavy_Small_Uncapped`: **81.5ms/op**, **75.2MB/op**, **1.30M allocs/op**
  - `Benchmark_LatestCommitsCaps_S3_FHeavy_Small_TimeBudget50ms`(Partial): **50.3ms/op**, **47.2MB/op**, **813k allocs/op**

---

## pprof 확인(상한 ON/OFF)

- **원칙(추천1 반영)**: pprof는 **%가 아니라 “절대시간(cum seconds) + Total samples”**로 비교한다. (%는 전체 work가 달라지면 의미가 쉽게 왜곡됨)
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

> 해석: caps는 “핫스팟의 종류를 바꾸는” 게 아니라, **해당 루프를 더 이상 오래 못 돌게 잘라서 tail을 제거**한다. (정상 속도 p95/p99는 cache로 흡수하는 방향)

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

> 해석: “동시성 부족” 레버는 blob size fill 구간에서 **wall-time 개선**이 관측되지만, pprof 지배항(`OpenFile/EncodedObject` + `DiffTree`)을 근본적으로 제거하지는 못하므로 **단독 처방은 아님**(caps/cache와 병행).

---

## 실행 방법(재현)

- **latest-commits 상한 ON**
  - `OVERVIEW_LATEST_COMMITS_TIME_BUDGET_MS=300` (예시)
  - `OVERVIEW_LATEST_COMMITS_MAX_COMMITS=10000` (예시)

- **벤치 실행**
  - `go test ./internal/overview -run '^$' -bench 'Benchmark_LatestCommitsCaps_S1_NHeavy_.*|Benchmark_LatestCommitsCache_S1_NHeavy_WarmHit' -benchtime=3s -count=1 -benchmem`

---

## 남은 작업(다음)

- **정책/제품**
  - incomplete/partial 시 UI placeholder 처리 및 재요청 전략 확정
  - maxCommits bucket 기준(레포 규모/트래픽) 확정
- **성능**
  - S2/S3에서 동일 방식으로 caps/caching 효과를 표로 고정(p95/p99 목표와 연결)
  - cache hit/miss 관측(메트릭) 추가 후 운영 데이터로 TTL/키 튜닝

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