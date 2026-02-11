
## 목표

- **목표**: `GET /repos/:owner/:repo/overview/tree/commits` (및 `GetTreeWithCommit`을 호출하는 `/overview`, `/overview/tree`, `/overview/blob`)에서 large repo의 tail latency(p95/p99)를 “정책적 상한 + 구조적 최적화”로 낮춘다.
- **핵심 병목 후보(코드 기준)**:
  - `internal/overview/service.go:GetLatestCommitsForPaths`의 **O(C×N×F)** 중첩 루프
  - `internal/overview/service.go:GetTreeWithCommit`의 **N회 `BlobObject` size 조회**
- **성공 기준(측정으로 확정)**:
  - p50/p95/p99 latency + alloc/op + bytes/op + goroutine 수 + (가능하면) CPU profile Top10 비교
- **1차 목표치(초기 잠금; large baseline 확보 후 조정 가능)**:
  - (S1/S2/S3 중 “가장 현실적인” 1개를 대표로 선택) **p95 ≤ 800ms**, **p99 ≤ 1.5s**
  - **alloc/op 또는 bytes/op 30% 감소**(둘 중 하나라도 달성)
  - degraded mode(정책 축소)일 때는 목표치를 별도로 둔다(예: p95 ≤ 300ms, p99 ≤ 800ms 등)

---

## 0) 현상 정의(측정/재현) — 먼저 답해야 하는 질문(제품/운영)

- **요청 식별**
  - **어떤 endpoint가 느린가**: `/overview/tree/commits` vs `/overview` vs `/overview/tree` vs `/overview/blob`
  - 느린 요청의 query: `ref`, `path` 값(특히 `path==""(root)`인지, 특정 하위 디렉터리인지)
- **분포**
  - 느림이 **p95/p99만 터지는지** vs **대부분 요청이 느린지**
  - 동일 `(repo, ref, path)` 반복 호출에서 **2번째부터 빨라지는지**(OS page cache 영향)
- **운영 상한**
  - 서버(app/nginx/ELB) 타임아웃, 클라이언트 타임아웃, “장애급” SLA(예: p95 1s)
- **실제 production에서의 C/N/F 근사치 확보(없으면 “없음”으로 확정)**
  - **C**: `repo.Log(From=refHash)`에서 pending이 0이 될 때까지 평균/상위 몇 커밋을 내려가는지
  - **N**: 느린 디렉터리의 `targetTree.Entries` 대략 크기
  - **F**: 커밋당 변경 파일 수 분포(평균/상위)

---

## 0.5) baseline 입력 세트(고정) — “축별로 3개” (필수)

- **원칙**: 개선 전/후 비교는 아래 3개 시나리오(S1/S2/S3)로 “항상” 동일 입력을 사용한다.
- **S1: N-heavy (디렉터리 엔트리 폭발)**
  - 예: **N=10k, C=200, F=20**
  - 목적: pagination/limit, blob size 분리(또는 지연 로딩) 효과 확인
- **S2: C-heavy (히스토리 폭발, pending 늦게 비워짐)**
  - 예: **C=50k, N=1k, F=10**
  - 목적: 조기 종료, 커밋 컷오프(정책), 캐시 효과 확인
- **S3: F-heavy (커밋당 변경 폭발)**
  - 예: **F=2k, C=2k, N=1k**
  - 목적: `DiffTree`/changes 처리 비용 지배 여부 확인

### baseline 생성기(generator) 규칙(고정) — “C/N/F를 실제로 제어 가능”하게

- **공통 원칙**
  - generator는 각 시나리오별로 “의도한 pending 제거 패턴”이 나오도록 commit당 변경 경로를 구성한다.
  - 목표는 “관측되는 C/N/F”가 시나리오 의도와 어긋나지 않게 만드는 것(예: S2인데 pending이 빨리 비워져 C가 작아지는 상황 방지).
- **S1(N-heavy) 생성 규칙**
  - 한 커밋에서 **N개 파일을 생성**(targetTree.Entries 폭발)
  - 이후 커밋에서는 **pending 대상 변경은 최소화**하여 C/F 영향이 지배하지 않게 유지
- **S2(C-heavy) 생성 규칙**
  - N개 path 중 일부는 **아주 오래전 커밋에서 마지막 변경**되도록 설계하여 pending이 늦게 비워지게 만든다
  - 이후 커밋들은 pending 대상 경로를 “거의 건드리지 않게” 하여, 실제로 많은 커밋을 스캔하도록 유도한다
- **S3(F-heavy) 생성 규칙**
  - 매 커밋마다 **F개 파일을 변경**
  - pending 대상이 매번 걸리도록 경로를 분산/구성하여, `DiffTree`/changes 처리 비용이 지배하도록 만든다

---

## 0.6) 측정 프로토콜(고정) — Cold vs Warm (필수)

- **원칙**: 모든 비교는 아래 2트랙으로 고정한다.
- **Cold run**
  - 정의: 가능한 한 “새 프로세스/새 입력/캐시 영향 최소”로 실행
  - 반복: **Cold 5회** 수행 → p50/p95/p99 기록
- **Warm run**
  - 정의: 동일 `(repo, ref, path[, pagination])`를 반복 호출하여 steady-state 관측
  - 반복: **Warm 30회** 수행 → p50/p95/p99 기록

### ref=HEAD 흔들림 제거(필수에 가까움)

- **원칙**: benchmark/성능 비교는 `ref=HEAD`가 아니라 **`ref=<fixed commit hash>`**로 수행한다.
- `ref=HEAD`는 운영 관측/대시보드에서만 추적한다(비교 입력으로 사용하지 않음).

---

## 1) large baseline(재현) 확보 — “무조건 1개는 돌아가게”

- **목표**: large-ish 입력에서 `GetTreeWithCommit` + `GetLatestCommitsForPaths`를 실제로 태우고 pprof를 뽑을 수 있는 상태 만들기
- **액션**
  - **A안(최단)**: `internal/overview/handler_perf_test.go:setupLargeRepository`에서 `object.TreeEntry`를 `Name` 기준으로 정렬 후 `tree.Encode` 호출
    - 근거: `internal/api/performance_load_test.go:createBareRepoWithManyFiles`는 `sort.Slice(entries, ...)`로 tree encode 에러를 회피하고 있음
  - **B안(정밀)**: large repo generator를 `internal/overview` 패키지 테스트 헬퍼로 분리하여, (C/N/F)를 명시적으로 제어 가능한 형태로 제공

---

## 2) 계측/관측(C/N/F) — “실제 숫자”를 남기기

- **목표**: 옵션 1/2/5의 우선순위를 “추정”이 아니라 데이터로 결정
- **추가 포인트(코드 위치)**
  - `internal/overview/handler.go:HandleGetTreeWithCommitOverview`
    - repo open/resolve(ref), `GetTreeWithCommit` total duration
  - `internal/overview/service.go:GetTreeWithCommit`
    - `len(targetTree.Entries)`(=N), blob count, `BlobObject` size 조회에 걸린 시간(총/평균)
  - `internal/overview/service.go:GetLatestCommitsForPaths`
    - 실제 순회 커밋 수(=C), pending 감소 곡선(예: 특정 interval마다 pending size)
    - commit마다 `len(changes)` 분포 샘플(=F) (전부 로그 X, 샘플링/상한 필요)
- **주의**
  - 로그 폭발 방지: large repo에서 per-commit/per-change 로그는 금지(요약/샘플링만)
  - 비교 목적 계측은 “오버헤드”가 섞일 수 있으므로, 기본은 메트릭(카운터/히스토그램)로 누적하고 로그는 요청당 1줄 요약만 남긴다.

### 요청당 1줄 요약(고정 포맷; 로그/메트릭 공통)

- 예시(필수 필드):
  - `refHash=... path=... N=... blobCount=... C=... F_p95=... latestCommitMs=... blobSizeMs=... totalMs=...`

---

## 3) 프로파일링/벤치마크 “실행 가능한” 형태로 고정

- **pprof(최소 1회, large baseline 기준)**
  - `go test ./internal/overview -run <LargeTestName> -count=1 -cpuprofile cpu.out -memprofile mem.out`
  - 목표: Top10에서 `object.DiffTree` vs `diffMatchesPath` vs `BlobObject` 비중 확정
- **pprof 저장 규칙(필수)**
  - S1 대표 시나리오에서 **Before 1회 + After 1회**를 반드시 저장한다.
  - Before/After의 **Top10을 표로 비교**한다(함수명/flat/cum).
- **벤치(반복 측정 가능)**
  - **벤치 분해(필수; BlobObject 혼입 제거)**
    - `Benchmark_GetTreeOnly`: tree 로딩 + 엔트리 enumerate (size fill 제외)
    - `Benchmark_GetLatestCommitsOnly`: `GetLatestCommitsForPaths`만 측정(입력 pathInfos 고정)
    - `Benchmark_BlobSizeFillOnly`: blob 엔트리 N개에 대해 `BlobObject` size 채우기만 측정
    - `Benchmark_GetTreeWithCommit_Full`: 현재 전체(`GetTreeWithCommit`) 측정
  - 입력 고정: S1/S2/S3별 synthetic repo + 동일 ref/path로 반복 (Cold/Warm 프로토콜은 위 0.6 준수)

---

## 4) 제품 정책(정확도) 3문장으로 확정(필수)

- **디렉터리(Type=="tree") latest commit**
  - “필수”인지 “nice-to-have”인지 결정 (nice-to-have면 비용 큰 구간부터 제거 가능)
- **merge commit 의미**
  - 현재는 first-parent만 사용(`internal/overview/service.go:GetLatestCommitsForPaths`) → 이를 “정의”로 수용할지 결정
- **rename 의미**
  - rename 추적을 엄밀히 할지(동일 파일로 간주) vs rename을 새 파일 취급해도 되는지 결정

---

## 5) 구현 순서(권장) — 리스크 낮은 것부터, 효과 큰 것 우선

- **Phase 0: baseline/관측 먼저**
  - 1) large baseline test 통과
  - 2) C/N/F 관측 로그/메트릭 추가
  - 3) pprof Top10 확보
- **Phase 0.9: Tree/Commits 분리(Progressive API) + tail 제거(강제 상한)**
  - Tree는 먼저 빠르게 렌더(`/overview/tree` 또는 tree-only 응답)
  - Latest commits는 batch endpoint로 별도 요청:
    - `POST /repos/:owner/:repo/overview/latest-commits`
    - request: `{ref, dir_path, paths[]}`
    - response: `{commits_by_path, meta{resolved_ref_hash, incomplete, scanned_commits, time_budget_ms, cut_reason}, missing_paths[]}`
  - “9초 tail 제거”를 위한 강제 상한 2개(Partial 허용):
    - `OVERVIEW_LATEST_COMMITS_TIME_BUDGET_MS=200~400` (기본 300)
    - `OVERVIEW_LATEST_COMMITS_MAX_COMMITS=<bucket>` (기본 10000)
- **Phase 1: 정책적 상한(옵션 5)**
  - `/overview/tree/commits`에 `limit/offset`(또는 page/size) 추가
  - latest commit은 “page 범위만 계산”을 허용할지 정책 결정(허용 시 서버 비용 요청당 상한 보장)
  - OpenAPI 스펙(`specs/generated/...yaml`)에 `ref`, `path`, pagination 파라미터 반영
- **S2(C-heavy) 컷오프 정책(권장; 비교 흔들림 방지)**
  - S2는 구조적으로 “히스토리 전체 스캔”이 발생할 수 있으므로, 구조 개선 전에도 정책 상한이 필요할 수 있다.
  - 컷오프를 도입하는 경우 아래 3가지를 “고정”한다:
    - (a) **컷오프 값**: 예) `maxCommitsScanned=10_000` (수치는 시나리오/환경에 맞게 1회 결정 후 고정)
    - (b) **컷오프 시 결과 표현**: 예) `LatestCommit=nil` 허용 또는 `"unknown"` 같은 명시적 상태(둘 중 하나로 고정)
    - (c) **정확도 테스트 기대 결과**: 컷오프 케이스에서의 기대 출력(파일/디렉터리별)을 테스트로 잠금
- **Phase 1.1: pagination 성능 비교 관점(필수로 분리)**
  - **UX 관점(첫 페이지 tail latency)**: “첫 페이지 p95/p99”가 얼마나 줄었는지
  - **총비용 관점(동일 결과를 만들기 위한 비용)**:
    - 예: 기존 N=10k 1회 호출 vs 신규 limit=1k 10회 호출의 총합(시간/CPU/alloc) 비교
- **Phase 2: 구조적 최적화(옵션 1)**
  - `GetLatestCommitsForPaths`에서 “pending(N) 전체 스캔”을 제거하는 방향으로 변경
  - dir(tree)는 현재 문자열 prefix 매칭(`strings.HasPrefix`)이므로, 구현 시 “prefix 인덱스/트라이/디렉터리 경계”를 고려(그대로면 병목 재발 가능)
- **Phase 2.5(유연): 캐시 레버(옵션 8) — 트래픽 패턴/pprof에 따라 당길 수 있음**
  - 동일 `(repo, refHash, path)` 반복이 많거나 동시성 붕괴가 보이면 Phase 1.5로 “당긴다”
  - `internal/overview/overview_cache.go`를 실제 경로에 연결
  - key는 `(repoPath, resolvedRefHash, path[, pagination])` (ref가 HEAD여도 resolvedRefHash면 사실상 immutable에 가까움)
  - TTL은 짧게(초~수십초) + singleflight로 동시 요청 붕괴 방지
  - latest-commits batch 캐시도 동일 원칙:
    - key: `(repoPath, resolvedRefHash, dirPath, pathsHash)`
    - singleflight 필수(동시성 붕괴 방지)
- **Phase 4: 비용 큰 부가 기능 분리**
  - blob size 조회(`BlobObject`)가 hot이면: size를 optional/지연 로딩/별도 endpoint로 분리
  - dir latest commit이 hot이면: dir commit 계산 비활성화(정책 기반) 또는 비동기 API로 분리(옵션 4)

---

## 6) 테스트/검증(정확도 vs 성능 분리)

- **정확도 테스트(케이스)**
  - rename(from/to) 포함 여부 및 기대 결과
  - merge(first-parent vs all-parent) 정책에 따른 기대 결과
  - empty commit / 큰 diff / submodule(Type=="commit") / symlink 모드
- **성능 테스트(매트릭스)**
  - C만 증가 / N만 증가 / F만 증가 + 교차 조합
  - p50/p95/p99 + alloc/op + bytes/op + goroutine + pprof 비교(Top10)

---

## 7) 롤아웃

- **정책 기반 degraded mode**
  - repo 규모 기반 degraded mode(예: N 상한 초과 시 dir latest commit 제외, commits 비동기화 등)
- **canary**
  - 특정 owner/repo 또는 size bucket만 우선 적용
- **fallback**
  - 타임아웃/에러 시 tree만 반환하고 commits는 비움(또는 별도 요청 유도)
- **observability**
  - endpoint별 p50/p95/p99
  - 요청당 관측 C/N/F 요약치(샘플링)

