## Phase 2 Plan — "완료" 조건 충족을 위한 남은 작업

> Phase 1~3.5에서 방향은 맞았고 설계는 "정답 계열".
> 하지만 지금 만든 건 **빠른 시스템이 아니라 빠르게 보이게 만드는 시스템**이다.
> 완료 조건은 벤치 숫자가 아니라 **운영 안정성**으로 판정한다.

---

## 완료 조건 체크리스트

| # | 조건 | 현재 상태 | 목표 |
|---|---|---|---|
| 1 | uncapped 코드 경로가 완전히 사라졌음이 테스트로 보장 | ✅ 완료 | — |
| 2 | incomplete rate < X% (repo 규모별) | ⚠️ 메트릭 있으나 운영 관측 전 | 대시보드 확정 |
| 3 | cache hit + index hit 합산 60~90% | ⚠️ 메트릭 있으나 운영 관측 전 | 대시보드 확정 |
| 4 | background populate 큐/worker 제한 + 드롭 정책 | ❌ 미구현 | worker pool + rate limit |
| 5 | 메모리 상한, LRU 폭발 안 함 | ⚠️ LRU maxEntries 있으나 cost-aware 아님 | cost-aware eviction |
| 6 | S1/S2/S3 + 실제 big repo e2e p99 목표 충족 | ⚠️ synthetic만 | 운영 24h 관측 |

---

## 완료 게이트 (합격/불합격 기준)

> Action 6(24h 관측)은 "해보자"가 아니라 **합격선**으로 판정.
> 이걸 통과하면 "진짜 끝".

### Gate A: 안정성

| 항목 | 합격 기준 |
|---|---|
| background queue length p99 | < 32 |
| drop rate | < 1% |
| RSS 메모리 | 상한 내 유지 (1.5GB 미만) |
| FD | 안정 (증가 추세 없음) |
| goroutine | 누수 없음 (증가 추세 없음) |

### Gate B: UX/성능

| 항목 | 합격 기준 |
|---|---|
| `/overview/tree/commits` p99 | < 1.5s |
| incomplete rate (small repo, N≤200) | < 1% |
| incomplete rate (medium repo, N≤1000) | < 5% |
| incomplete rate (huge repo, N>1000) | < 15% |
| cache + index hit 합산 (huge repo) | ≥ 60% |
| cache + index hit 합산 (기타) | ≥ 40% |

---

## 핵심 리스크 10개 + 대응 방안

### 1) partial 캐시 안 함 → 트래픽 패턴 따라 tail 재발

- **문제**: complete만 캐시하면, N이 큰 디렉터리에서 time budget이 짧아 **영원히 partial**로 남는다.
  인기 repo에서 동일 요청이 매번 full computation을 유발.
- **대응**:
  - partial cache는 **TTL이 아니라 singleflight-first 정책**으로 구현
  - 핵심은 **중복 계산 방지**(compute fan-out 차단)
  - TTL(1~3s)은 보조: singleflight 결과가 끝난 후 짧은 재사용 구간
  - singleflight를 **partial/complete 둘 다에 강제 적용** (이거 없으면 thundering herd)
  - 구현: 기존 `OverviewCache.Do(sfKey, fn)` + partial 결과도 짧은 TTL로 set

### 2) background populate 폭주 → 서버 자살

- **문제**: 여러 디렉터리/여러 repo에서 동시에 miss+incomplete → 5s/100k background job이 CPU/I/O/FD 다 빨아먹음
- **대응**:
  - **전역 worker pool 제한**: `sem chan struct{}` (max 4~8 concurrent)
  - **repo별 rate limit**: 동일 repo에서 동시 populate 1개만
  - **큐 길이 상한**: 대기 큐 > 32 → drop (tryAcquire 패턴)
  - **취소 가능**: ctx 전파 + graceful shutdown 연동
  - **hotness 기반 warmup 대상 선별**: 뜬금없는 dir이 아니라 실제 hot dir만 populate
    - (repo, dirPath) 기준 최근 5분 요청수 카운터
    - threshold 넘는 것만 warmup 큐에 진입
  - 구현: `CommitIndex`에 `workerSem`, `repoInflight sync.Map`, `hotCounter` 추가

### 3) CommitIndex LRU → 운영 메모리 폭탄

- **문제**: key-space = (repoPath × refHash × dirPath). refHash는 브랜치 움직일 때마다, dirPath는 탐색할수록 폭발.
- **대응**:
  - **refHash 정책**: 최근 1~2개 refHash만 유지 (stale 자동 evict)
  - **dirPath 상한**: hot dir top-N만 인덱싱 (동일 (repo, refHash)에서 최대 50 dir)
  - **cost-aware eviction**: `cost = len(paths)*w1 + uniqueCommits*w2` (w2=0.2~0.5)
  - **max bytes budget**: 전체 인덱스 메모리 예산 제한 (256MB / 512MB)
  - **모니터링**: `commit_index_entries` gauge + memory RSS 연동 alert

### 4) 50ms budget은 운영에서 안 지켜질 수 있음

- **문제**: 디스크/스토리지 성능 + 동시 요청 + 다른 endpoint contention → 50ms 초과 빈번
- **대응**:
  - **정적값이 아닌 adaptive budget 검토** (load-aware; 서버 부하 높으면 budget 줄여서 빨리 partial 반환)
  - **최소한**: repo-size-aware N-bucket은 이미 있음. 운영 p99 관측 후 bucket 수치 튜닝
  - **fail-safe**: budget 초과 시 commits 비우고 tree는 무조건 반환

### 5) /overview 병렬화 → 동시 I/O 폭발

- **문제**: wall-time은 줄지만 peak I/O가 늘음. 동시 사용자 많으면 I/O 큐 포화
- **대응**:
  - 병렬 tasks에도 **bounded concurrency** (전역 sem으로 overview 동시 처리 제한)
  - blob read / readme / tags는 **우선순위 낮게** (tree+commits 먼저 완료)
  - 운영: open FD 수 / goroutine 수 모니터링

### 6) CommitCount cap=100k → UX/신뢰 리스크

- **문제**: "100,000+" 표시 → "왜 GitHub이랑 숫자가 다르지?"
- **대응**:
  - **정책 확정**: `CommitsApproximate=true`일 때 UI에 "~100,000+" 또는 "100k+" 표시
  - 정확한 수치가 필요하면 별도 API로 (비동기/캐시)

### 7) DiffTree가 지배 → go-git 자체가 병목

- **문제**: 지금 최적화는 "덜 돌게 만들기". 궁극적으로는 go-git I/O 모델 한계.
- **대응** (장기):
  - `libgit2` 호출
  - `git cat-file --batch` 방식
  - packfile 캐싱 전략
  - → 당장 갈아엎을 건 아님. caps/cache/index로 충분히 커버되는 동안은 유지

### 8) 정확도 요구사항 상승 시 caps와 충돌

- **문제**: rename 추적, merge 의미, directory last commit 정확도를 GitHub급으로 맞추면 budget/caps 충돌
- **대응**:
  - **정책 문서로 못 박기**: first-parent only, rename 미추적, partial 허용
  - 정확도 상승 요구 시 인덱스 레이어에서 해결 (API 레이어 caps는 유지)

### 9) API contract 복잡도 → 클라 지옥

- **문제**: `meta.incomplete` / `cut_reason` / placeholder / 재시도 → 클라 구현 복잡 → 버그로 UX 악화
- **대응**: **클라 재시도 규칙 5줄로 고정** (아래 Action 5 참조)

### 10) 벤치 좋은데 운영에서 느림

- **문제**: synthetic + 단일 머신. 운영은 동시성/스토리지/CPU contention이 다름.
- **대응**: **운영 대시보드에서 p95/p99 확정** (24h 관측 → Gate A/B로 합격 판정)

---

## 다음 액션 6개

### Action 1: partial 캐시 + singleflight 강제 적용

- **핵심**: partial cache는 "값" 캐시가 아니라 **singleflight-first** (compute fan-out 차단)
- 동일 key에 대해 singleflight로 1개만 계산, 나머지는 결과(complete/partial) 공유
- TTL(1~3s)은 보조: singleflight 결과 끝난 후 짧은 재사용 구간
- singleflight를 **partial/complete 둘 다**에 강제 적용 (이거 없으면 thundering herd)
- **파일**: `internal/overview/overview_cache.go`, `internal/overview/handler.go`

### Action 2: background populate worker pool + hotness 기반 warmup

- `CommitIndex`에 전역 worker sem (max 4~8 concurrent)
- repo별 동시 populate 1개 제한 (`repoInflight sync.Map`)
- 큐 길이 > 32 → drop (tryAcquire 패턴)
- **hotness 기반 warmup 대상 선별**:
  - (repo, dirPath) 기준 최근 5분 요청수 cheap counter
  - threshold 넘는 것만 warmup 큐 진입 ("complete로 만들 가치가 있는 것"만)
  - incomplete가 동일 key로 3회 연속 발생 시에도 warmup 트리거
- (기존 Action 4: warmup rule을 여기에 흡수)
- **파일**: `internal/overview/commit_index.go`

### Action 3: CommitIndex cost-aware eviction + bytes budget

- cost = `len(paths) * w1 + uniqueCommits * w2` (w1=1.0, w2=0.3으로 시작)
- **max bytes budget**: 전체 인덱스 메모리 예산 (256MB default, env override)
- dirPath 상한: 동일 (repo, refHash)에서 최대 50 dir
- stale refHash 자동 evict: 최근 2개 refHash만 유지
- **파일**: `internal/overview/commit_index.go`

### Action 4: 클라 재시도 규칙 고정

```
1. meta.incomplete == false → 완료. 재시도 불필요.
2. meta.incomplete == true  → visible rows의 missing paths만 재요청.
3. 재시도는 최대 2회까지.
4. 2회 후에도 incomplete → placeholder 표시. user action(스크롤/클릭) 시에만 재요청.
5. cut_reason == "index_hit" → 자동 재시도 금지(쿨다운).
   서버가 background refresh 큐에 low-priority로 넣음.
   다음 user action 시에만 재요청.
```

### Action 5: 실 repo 24h 관측 → Gate A/B 합격 판정

- 대상: 10만+ 커밋 모노레포, N=1000+ 디렉터리
- 관측:
  - `incomplete rate` by repo/dir → Gate B 판정
  - `cache hit rate` + `index hit rate` → Gate B 판정
  - `p50/p95/p99 latency` by endpoint → Gate B 판정
  - `FD count`, `memory RSS`, `goroutine count` → Gate A 판정
  - `background populate queue length` / `drop count` → Gate A 판정
- **합격/불합격**: 위 Gate A/B 기준으로 판정
- 결과를 `docs/performance/result.md`에 운영 섹션으로 추가

### Action 6: fail-closed 정책 확정

- **budget 초과 시**: commits 비우고 tree는 무조건 반환 (`meta.incomplete=true`)
- **서버 메모리 상한 초과 시**: LRU eviction 강제 + 인덱스 일시 중단
- **background worker 큐 포화 시**: 새 요청 drop + partial 결과로 서빙
- **원칙**: 서버가 죽는 것보다 partial이 낫다

---

## pprof 해석 주의 포인트

> timeBudget=50ms에서 DiffTree 절대시간이 uncapped보다 커 보이는 현상:
> - budget 때문에 "짧은 루프를 더 많이 반복"하면서 profile 샘플이 DiffTree에 더 찍히는 효과
> - 또는 벤치 반복 횟수(iterations)가 달라서 생긴 샘플링 편향
> - 이미 "절대시간(cum seconds)로 본다"라는 원칙이 있으므로 OK
> - **운영에서는 "request당 CPU seconds"로도 관측 필수**

---

## 우선순위

### P0 — 운영 자살 방지

| 액션 | 예상 공수 | 리스크 커버 |
|---|---|---|
| Action 2: worker pool + repo inflight + hotness 기반 warmup | 1d | #2 서버 자살, warmup 대상 선별 |
| Action 6: fail-closed 정책 | 0.5d | #4,#5 안전장치 |
| (포함) singleflight를 partial/complete 둘 다 강제 적용 | Action 1에 포함 | thundering herd 방지 |

### P1 — 메모리 폭탄 방지 + tail 재발 방지

| 액션 | 예상 공수 | 리스크 커버 |
|---|---|---|
| Action 1: partial cache + singleflight 강제 | 0.5d | #1 tail 재발, thundering herd |
| Action 3: cost-aware eviction + bytes budget | 1d | #3 메모리 폭탄 |
| Action 4: 클라 재시도 규칙 | 0.5d | #9 클라 지옥 |

### P2 — 운영 검증

| 액션 | 예상 공수 | 리스크 커버 |
|---|---|---|
| Action 5: 24h 관측 → Gate A/B 합격 판정 | 1d (대기) | #10 운영 검증 |
