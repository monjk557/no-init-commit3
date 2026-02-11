### 배포 판단(결론)

- **가능**: `latest-commits` 분리 endpoint에서 **time budget + max commits 상한**으로 tail(9초) 방지되는 것이 **테스트로 실측 확인됨**
- **주의**: 이는 **partial(incomplete) 허용 계약** 전제이며, 기존 결합 endpoint(`/overview/tree/commits`)는 **partial 금지(A안)**로 유지됨

---

### 이번 변경의 목표/효과

- **목표**: tree/commits 분리 + latest-commits의 tail(최대 9초) 제거
- **pprof 근거(S1)**: 병목은 매칭이 아니라 **I/O(`os.OpenFile`/`EncodedObject`) + `DiffTree`**
- **tail 제거 레버**:
  - **caps**: timeBudget / maxCommits로 “더 이상 오래 못 돌게” 절단(Partial)
  - **cache**: resolvedRefHash 기반 + singleflight로 “동일 요청 반복/동시성 붕괴” 방지

---

### 배포 대상 변경점(코드)

- **신규 endpoint (commits 분리)**
  - `POST /repos/:owner/:repo/overview/latest-commits`
  - request: `{ ref, dir_path, paths[] }`
  - response: `{ commits_by_path, meta{resolved_ref_hash, incomplete, scanned_commits, time_budget_ms, cut_reason}, missing_paths[] }`
  - 구현:
    - `internal/overview/handler.go:HandlePostLatestCommitsBatch`
    - `internal/overview/service.go:GetLatestCommitsForPathsWithMeta`
    - `internal/server/server.go` route 추가

- **기존 endpoint 정책**
  - `/overview/tree/commits` 등 결합 endpoint는 **partial 금지(A안)**
  - `internal/overview/service.go:GetLatestCommitsForPaths`는 **caps 미적용(complete only)**

- **캐시(singleflight 포함)**
  - `internal/overview/overview_cache.go`
  - latest-commits batch 캐시 추가(complete 결과만 캐시)

- **OpenAPI 정합화**
  - `specs/generated/collabops-git-executor.yaml`
    - `/overview/tree/commits`의 query `ref/path` 추가
    - `/overview/latest-commits` + schema 추가

---

### 배포 시 설정(Feature flags / Env)

- **latest-commits caps (항상 적용; tuning only)**
  - `OVERVIEW_LATEST_COMMITS_TIME_BUDGET_MS=200~400` (기본 300)
  - `OVERVIEW_LATEST_COMMITS_MAX_COMMITS=10000` (repo size bucket으로 추후 튜닝)

- **Blob size fill 병렬화(보조 레버; optional)**
  - `OVERVIEW_BLOB_SIZE_PARALLELISM=1`이면 순차(기본)
  - `OVERVIEW_BLOB_SIZE_PARALLELISM=4~8`이면 병렬
  - 주의: 효과는 Full 기준 ~몇 % 수준(보조). 파일/스토리지 환경에 따라 득/실이 갈릴 수 있음

---

### 롤백 플랜(즉시)

- **caps 완화/강화**: `OVERVIEW_LATEST_COMMITS_TIME_BUDGET_MS`, `OVERVIEW_LATEST_COMMITS_MAX_COMMITS` 조정
- **blob 병렬화 끄기**: `OVERVIEW_BLOB_SIZE_PARALLELISM=1`
- **클라이언트 롤백(가능하면)**: `latest-commits` 호출을 끄고 기존 결합 endpoint만 사용(단, 결합 endpoint는 partial 금지 유지)

---

### 운영 모니터링 체크리스트(필수)

- **endpoint latency**
  - `POST /overview/latest-commits` p50/p95/p99
  - `GET /overview/tree/commits` p50/p95/p99 (변동 여부)
- **partial 비율**
  - `meta.incomplete == true` 비율(시간대/레포별)
  - `cut_reason` 분포: `time_budget` vs `max_commits`
- **캐시 효율**
  - cache hit/miss (추가 계측 필요; result.md에 남겨둠)
- **리소스**
  - CPU, RSS, open files(핸들), goroutines(병렬화 ON 시)

---

### 검증 근거(테스트/실측)

- **p95/p99 샘플링 테스트**
  - `internal/overview/handler_perf_test.go`
    - `Test_LatestCommitsBatch_Performance_TimeBudget50ms_P99Under1500ms`
  - 조건: C-heavy synthetic (pending=200, commits=2000, noiseFiles=10), warm 30회, timeBudget=50ms
  - 결과(실측): p50 **50.63ms**, p95 **50.95ms**, p99 **51.26ms**

---

### 남은 의사결정/리스크(배포 전 확인)

- **제품 정책**
  - partial(incomplete)일 때 UI placeholder/재시도 전략 확정
  - `timeBudgetMs` 기본값(200~400ms) 확정 및 SLA와 정렬
- **정확도**
  - merge/rename 의미는 기존 정책을 따른다(필요 시 별도 문서화)