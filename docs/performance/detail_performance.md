
## 1) 시스템/호출 경로 파악 (Fact finding)

- (a) **발견한 사실**
  - **HTTP endpoint → executor call graph**
    - `internal/server/server.go`
      - `ownerRepoGroup.GET("/overview/tree/commits", overviewHandler.HandleGetTreeWithCommitOverview)`
      - (동일 서비스 재사용) `GET /overview`, `GET /overview/tree`, `GET /overview/blob`도 내부에서 `GetTreeWithCommit` 호출
    - `internal/overview/handler.go`
      - `(*RepositoryOverviewHandler) HandleGetTreeWithCommitOverview`가 `h.service.GetTreeWithCommit(ctx, gitRepo, repoCtx.ref, repoCtx.path)` 호출
      - 공통 오픈/검증은 `validateAndOpenRepository(ctx, c, owner, repo, ref)`에서 수행
    - `internal/overview/service.go`
      - `(*RepositoryOverviewService) GetTreeWithCommit(ctx, gitRepo, ref, path)`에서
        - `ref` → commit 해석 → tree 로딩 → `path`의 subtree로 이동
        - subtree entry 목록(N)을 만든 뒤
        - `GetLatestCommitsForPaths(ctx, repo, ref, pathInfos)`로 “경로별 최신 커밋”을 계산해 합침
  - **요청 파라미터 (tree+commits)**
    - `internal/overview/handler.go`의 `HandleGetTreeWithCommitOverview`
      - path param: `:owner`, `:repo`
      - query: `ref`, `path`
      - depth/page/limit/recursive 파라미터 **없음**
    - `ref` 기본값: `validateAndOpenRepository`에서 `ref==""`이면 `"HEAD"`로 설정 (`internal/overview/handler.go`)
  - **Tree 데이터 구조 + latest commit 결합**
    - 응답: `internal/overview/schema.go`의 `TreeWithCommitOverviewResponse`
      - `Tree []GitTreeEntryWithCommit`
    - 엔트리: `GitTreeEntryWithCommit` (`internal/overview/schema.go`)
      - `Type` (`"tree"|"blob"|"commit"(submodule)`), `Name`, `Path`
      - `Mode`, `Hash`, `Size` (blob에 대해 `BlobObject`로 조회), `LatestCommit *GitCommit`
    - 결합 방식: `GetTreeWithCommit`에서 `entries[]`를 만든 뒤 `commitMap[path] = *GitCommit`을 `pathToIndex`로 역매핑하여 `LatestCommit` 필드에 주입 (`internal/overview/service.go`)
  - **A. GetTreeWithCommit 입력/출력 스키마**
    - 입력: `(ctx, *gogit.Repository, ref string, path string)` (`internal/overview/service.go`)
    - 출력: `([]GitTreeEntryWithCommit, error)`
  - **B. “경로별 최신 커밋” 계산 핵심 루프(중첩 구조)**
    - `GetLatestCommitsForPaths` 내부:
      - 커밋 로그 순회: `iter := repo.Log(&gogit.LogOptions{From: *hash})`
      - 커밋마다:
        - `currentTree := c.Tree()`
        - `parentTree := parent.Tree()` (첫 부모만 사용)
        - `changes := object.DiffTree(parentTree, currentTree)`
        - `for path := range pending { if diffMatchesPath(changes, path, isTree) { ... } }`
      - 조기 종료: `len(pending)==0`이면 `io.EOF`로 커밋 순회 종료
  - **C. 커밋 순회(C) 범위 결정**
    - 시작점: `ref`가 가리키는 commit hash (`ResolveRevision`/`ResolveReference`)
    - 종료조건:
      - `pending`이 0이 되면 즉시 종료 (`GetLatestCommitsForPaths`)
      - 그렇지 않으면 `repo.Log`가 제공하는 커밋을 끝까지 순회 (사실상 HEAD→초기까지)
      - 추가 하드리밋(최대 커밋 수, depth, pagination) **없음**
      - 컨텍스트 취소 시 `io.EOF`로 종료 (`GetLatestCommitsForPaths`)
  - **D. F(변경 크기) 계산에 사용하는 데이터**
    - per-commit diff: `changes, err := object.DiffTree(parentTree, currentTree)`의 결과 `object.Changes`
    - `diffMatchesPath(changes, path, isTree)`는 `for _, ch := range changes { ... }`로 전체 change 리스트를 선형 스캔
    - 즉, F는 코드상 `len(changes)` (단, `DiffTree` 자체의 비용은 별도이며, 여기서는 호출 사실만 확정)
  - **E. N(현재 트리 레벨 경로 개수) 산정 방식**
    - `targetTree.Entries` 길이로 결정 (`entries := make([]entryInfo, len(targetTree.Entries))`)
    - `pathInfos`는 `entryType == "blob" || "tree"`만 포함 (submodule `"commit"` 타입은 제외)
  - **추가 사실 (관련 API)**
    - 별도 tree API: `internal/tree/handler.go`의 `GET /repos/:owner/:repo/tree?ref=&path=&recursive=`는 pagination 없이 tree만 제공 (latest commit 계산 없음)
  - **추가 사실 (캐시)**
    - `internal/overview/overview_cache.go`에 TTL+LRU+singleflight 캐시 구현 존재
    - 현재는 `POST /overview/latest-commits`에서 latest-commits batch 캐시로 사용됨(complete 결과만, singleflight 포함)
    - 기존 `/overview/tree/commits` 등은 캐시를 아직 직접 연결하지 않음(treeEntries 캐시는 존재)
  - **추가 사실 (OpenAPI 스펙)**
    - `specs/generated/collabops-git-executor.yaml`에 `/overview/tree/commits`의 query `ref`, `path` 및 `/overview/latest-commits`가 반영됨(정합화 완료)

- (b) **해석**
  - `GetTreeWithCommit`의 “트리 로딩(N)”과 “latest commit 매핑(O(C×N×F))”이 단일 요청에 묶여 있으며, **파라미터로 N/C를 제한할 수단이 없음**(depth/limit 없음).
  - `GetLatestCommitsForPaths`는 구조적으로
    - 커밋마다 `DiffTree`를 수행하고(F 생성),
    - pending path(N) 각각에 대해 changes(F)를 선형 스캔하기 때문에,
    - 최악에 가까운 형태로 **O(C × N × F)**가 코드 구조로 고정됨.

- (c) **다음 행동**
  - `GetLatestCommitsForPaths`를 기준으로 C/N/F 상한을 줄이는 옵션(섹션 5)을 설계하고,
  - 큰 입력(C/N/F)에서 실제로 병목이 어디인지 pprof로 확정(섹션 3/4)하기 위해 “재현 가능한 large-repo 생성 + 실행경로 타이밍 분해”를 준비.

### Call graph (텍스트 다이어그램)

- **`GET /repos/:owner/:repo/overview/tree/commits?ref=&path=`**
  - `internal/server/server.go`
    - → `internal/overview/handler.go` `(*RepositoryOverviewHandler) HandleGetTreeWithCommitOverview`
      - → `validateAndOpenRepository` (repo open, branches, ref validate)
      - → `internal/overview/service.go` `(*RepositoryOverviewService) GetTreeWithCommit(ctx, gitRepo, ref, path)`
        - → `go-git` `ResolveRevision`/`CommitObject`/`Tree()`/`FindEntry(path)`/`TreeObject(hash)`
        - → build `entries` from `targetTree.Entries` (N)
          - (blob만) `gitRepo.BlobObject(entry.Hash)`로 `Size` 조회 (N회)
        - → `GetLatestCommitsForPaths(ctx, repo, ref, pathInfos[N])`
          - → `repo.Log(From=refHash)` (commit iterator; C)
            - per commit:
              - `c.Tree()` + (first parent) `parent.Tree()`
              - `object.DiffTree(parentTree, currentTree)` (F 생성)
              - for each pending path (N): `diffMatchesPath(changes, path, isTree)` (F 선형 스캔)
              - 조기 종료: pending==0이면 stop
        - → `commitMap`을 `entries[i].LatestCommit`에 매핑 후 정렬/응답

### 핵심 루프 의사코드(실제 코드 기반)

- `internal/overview/service.go`의 구조를 그대로 요약:

```text
GetTreeWithCommit(ref, path):
  commit := CommitObject(ResolveRevision(ref))
  targetTree := commit.Tree(); if path != "" then targetTree = subtree(path)

  entries := targetTree.Entries                      // N
  pathInfos := []
  for each entry in entries:                         // N
    if entry is blob or tree:
      pathInfos.append({Path: fullPath(entry), IsTree: entry is tree})
    if entry is blob:
      BlobObject(entry.Hash) -> size                 // N (blob only)

  commitMap := GetLatestCommitsForPaths(ref, pathInfos)
  for each (path -> commit) in commitMap:
    entries[pathToIndex[path]].LatestCommit = commit
  return entries (sorted)

GetLatestCommitsForPaths(ref, pathInfos):
  pending := set(pathInfos)                          // size N
  for each commit c in Log(From=refHash):            // C
    if pending empty: break
    currentTree := c.Tree()
    parentTree := first-parent tree (if exists)

    if parentTree == nil:
      for each path in pending:                      // N
        if matchInTree(currentTree, path):           // may scan all files for tree path
          result[path] = c; remove pending[path]
      continue

    changes := DiffTree(parentTree, currentTree)     // F changes
    for each path in pending:                        // N
      if diffMatchesPath(changes, path):             // scans F
        result[path] = c; remove pending[path]
  return result
```

### 루프 상한을 좌우하는 조건(파라미터/설정/하드리밋)

- **C (commit iteration count)**
  - **상한**: `repo.Log(From=refHash)`가 반환하는 커밋 전부 (사실상 히스토리 전체)
  - **조기 종료**: `pending`이 0이 되는 시점 (`GetLatestCommitsForPaths`)
  - **하드리밋**: 없음 (max commits / pagination 없음)
  - **요청 파라미터 영향**: `ref`가 커밋 범위 시작점을 바꿈
- **N (paths at current level)**
  - **정의**: `targetTree.Entries` 길이 (현재 `path` 디렉터리 1 depth)
  - **요청 파라미터 영향**: `path`가 subtree를 바꿔 N을 바꿈
  - **하드리밋**: 없음 (limit 없음)
- **F (changes per commit)**
  - **정의**: `object.DiffTree(parentTree, currentTree)` 결과 `changes`의 길이 + 그 생성 비용
  - **하드리밋**: 없음

---

## 2) 베이스라인 정의 (성능이 “느리다”를 수치화)

- (a) **발견한 사실**
  - 레포 내 “대규모 입력 생성” 관련 코드가 이미 존재:
    - `internal/overview/handler_perf_test.go`: “100MB repo” 생성 후 overview API 호출을 목표로 함
      - `entries in tree are not sorted` 이슈는 정렬 추가로 해결됨(large baseline 실행 가능)
    - `internal/api/performance_load_test.go`: bare repo 생성 유틸 존재
      - `createBareRepoWithCommits(n)` (기본값 `perfCommitCount=500`) → 커밋 수(C) 스트레스
      - `createBareRepoWithManyFiles(fileCount)` (기본값 `perfManyFilesCount=1000`) → 트리 엔트리 수(N) 스트레스
      - (단, 이 파일은 `/tree`/`/blob`에 대한 로드 테스트이며, `GetTreeWithCommit` 경로는 아님)
  - `GetTreeWithCommit`은 pagination/limit이 없어 “큰 N”을 직접적으로 제한할 수 없음 (`internal/overview/handler.go`, `internal/overview/service.go`)

- (b) **해석**
  - “느림 재현”의 최소 시나리오는 C/N/F를 독립적으로 키우는 조합이 필요:
    - **N만 큰 경우**: 한 커밋에 파일이 매우 많고(대형 디렉토리), latest commit을 각 파일에 붙이기 위해 N개의 pathInfos 생성
    - **C가 큰 경우**: 히스토리가 길고, 많은 파일이 “오래전 커밋에서 마지막으로 변경”되어 pending이 늦게 비워짐
    - **F가 큰 경우**: 커밋당 변경 파일이 많아 `DiffTree` 결과 changes가 커짐

- (c) **다음 행동**
  - baseline 시나리오를 아래 2~3개로 고정하고, 동일 입력으로 반복 측정(p50/p95/p99) 가능하게 준비:

### Baseline 시나리오 (small / medium / large)

- **Small (기능 확인)**
  - **구성**: C≈10, N≈50, F≈5
  - **목적**: 정확도(파일/디렉터리 latest commit) + 기본 성능 계측 파이프라인 검증
- **Medium (현실적 monorepo 일부)**
  - **구성**: C≈2,000, N≈2,000 (한 디렉터리에 2k 엔트리), F≈50
  - **목적**: “pending이 늦게 비워질 때” 커밋 순회/매칭 비용 확인
- **Large (병목 재현 목표)**
  - **구성**: C≈50,000+, N≈10,000, F≈500+
  - **목적**: O(C×N×F) 성질이 체감 latency로 폭발하는 구간 재현

### 각 시나리오의 C/N/F 추정치(코드 기준 정의)

- **C**: `repo.Log(From=refHash)`가 실제로 순회하는 커밋 수(early stop 전까지)
- **N**: `targetTree.Entries` 길이(현재 `path` 수준)
- **F**: `len(object.DiffTree(parentTree, currentTree))` (changes 개수)

---

## 3) 측정 도구 세팅 (프로파일링/벤치/트레이싱)

- (a) **발견한 사실**
  - 계측 포인트가 되는 경계는 명확:
    - `internal/overview/handler.go` → `GetTreeWithCommit` 호출 전/후
    - `internal/overview/service.go`의 `GetTreeWithCommit`
      - subtree 탐색 (`FindEntry`, `TreeObject`)
      - entry 스캔 + (blob size 조회)
      - `GetLatestCommitsForPaths`
    - `GetLatestCommitsForPaths`
      - `repo.Log` 커밋 순회
      - `object.DiffTree`
      - `diffMatchesPath` (changes 선형 스캔)
  - 레포 내 perf test(`internal/overview/handler_perf_test.go`)로 large-ish 입력 기반 실행/프로파일링이 가능

- (b) **해석**
  - 의미 있는 벤치마크/프로파일을 만들려면:
    - 입력(repo, ref, path)을 **고정**하고
    - 외부 I/O(디스크/압축 해제)를 **분리**하거나 “포함할지”를 명확히 해야 함
    - 특히 `BlobObject` size 조회는 N에 비례해 object 로딩을 유발하므로, “latest commit 계산”과 분리 측정이 필요

- (c) **다음 행동**
  - (코드 변경 없이 문서 제안) 아래 3가지를 바로 적용 가능한 형태로 준비:
    - 단위 benchmark(Go `testing.B`) 스켈레톤 1개
    - pprof 실행 커맨드
    - 단계별 타이머/structured logging 삽입 포인트 목록

### A. Go benchmark 스켈레톤 (단위: `GetTreeWithCommit`)

> 아래는 “형태” 제공(문서용). 실제 적용 시 `internal/overview/`에 `*_test.go`로 추가하여 패키지 스코프 helper 재사용 권장.

```go
package overview

import (
  "context"
  "testing"
)

func BenchmarkRepositoryOverviewService_GetTreeWithCommit(b *testing.B) {
  // 1) 고정 입력 준비: repo/ref/path
  // - C/N/F를 컨트롤 가능한 synthetic bare repo 생성 helper 필요
  // - 목표: (C,N,F) 매트릭스를 바꿔가며 반복 측정

  ctx := context.Background()

  service, tempDir, cleanup := setupTestServiceForBench(b) // 문서: helper 필요
  defer cleanup()

  gitRepo := createSyntheticRepoForTreeWithCommits(b, tempDir, /*C*/ 2000, /*N*/ 2000, /*F*/ 50)

  b.ResetTimer()
  for i := 0; i < b.N; i++ {
    _, _ = service.GetTreeWithCommit(ctx, gitRepo, "HEAD", "")
  }
}
```

### B. CPU/mem/block/mutex profile 적용 위치/방법

- **프로파일 대상 함수**
  - `internal/overview/service.go`
    - `GetTreeWithCommit`
    - `GetLatestCommitsForPaths`
    - `diffMatchesPath`, `matchInTree`
  - (go-git 내부) `object.DiffTree`, `Tree.Files` 등이 hot spot으로 나타날 가능성이 있음 → pprof로 확인
- **실행 커맨드 예시**
  - 테스트 기반:
    - `go test ./internal/overview -run <TestName> -count=1 -cpuprofile cpu.out -memprofile mem.out`
    - `go tool pprof -top cpu.out`
  - 벤치 기반(권장):
    - `go test ./internal/overview -bench BenchmarkRepositoryOverviewService_GetTreeWithCommit -benchmem -cpuprofile cpu.out -memprofile mem.out`
    - (lock 확인) `-mutexprofile mutex.out -blockprofile block.out`

### C. structured logging/trace(단계별 타이머) 추가 포인트(파일/함수)

- `internal/overview/handler.go`
  - `HandleGetTreeWithCommitOverview`에서 request-scoped elapsed:
    - repo open/resolve(ref)
    - `GetTreeWithCommit` total
- `internal/overview/service.go`
  - `GetTreeWithCommit` 내부 분해:
    - resolve ref/commit/tree
    - path subtree 탐색
    - entry scan + blob size 조회
    - `GetLatestCommitsForPaths` 호출
  - `GetLatestCommitsForPaths` 내부 분해:
    - `repo.Log` 시작
    - per-commit: `Tree()` / `parent.Tree()` / `DiffTree`
    - pending loop 수행 횟수, pending 감소 속도(조기 종료 효과)

### “벤치마크가 의미 없게 되는 함정” 방지 체크리스트

- **캐시/워밍업**: 동일 repo/ref/path 반복 시 OS page cache 영향 큼 → cold/warm 분리
- **GC 영향**: `DiffTree`/changes 생성으로 alloc이 커질 수 있음 → `-benchmem`, `GODEBUG=gctrace=1` 등으로 확인
- **디스크 I/O 섞임**: blob size 조회(`BlobObject`)가 object decode I/O를 유발 → 옵션으로 분리 측정 권장
- **락 경합**: 현재 코드에서 명시적 락은 없음(단, go-git 내부/스토리지 내부는 별도) → mutex/block profile로 확인

---

## 4) 병목 원인 확정 (O(C×F×N) 증명)

- (a) **발견한 사실**
  - O(C×N×F) 형태가 “코드 구조”로 확정:
    - `internal/overview/service.go` `GetLatestCommitsForPaths`
      - **C 루프**: `repo.Log(...).ForEach(func(c *object.Commit) ...)`
      - **F 루프**: `changes := object.DiffTree(...)` + `diffMatchesPath` 내부 `for _, ch := range changes`
      - **N 루프**: `for path := range pending { ... diffMatchesPath(changes, path, ...) ... }`
  - 조기 종료(가지치기)가 존재:
    - `if len(pending) == 0 { return io.EOF }`
  - `internal/overview/handler_perf_test.go`로 large-ish 입력에서 endpoint 실행 및 pprof 수집 가능
  - **S1(N-heavy) 실측 pprof(벤치 기반) 확보**
    - 벤치: `internal/overview/performance_bench_test.go`의 `Benchmark_GetTreeWithCommit_Full_S1_NHeavy`
    - (참고) loop inversion은 기본(always-on)으로 고정됨(배포 안정성)
    - 실행 예시:
      - `go test ./internal/overview -run '^$' -bench Benchmark_GetTreeWithCommit_Full_S1_NHeavy -benchtime=10s -count=1 -cpuprofile .pprof_s1_cpu_s1.out`
    - 관측 요약:
      - `filesystem.(*ObjectStorage).EncodedObject` **cum ~35–39%**
      - `os.OpenFile` **cum ~33–37%**
      - `plumbing/object.DiffTree` **cum ~18–21%**
      - `(*Repository).BlobObject` **cum ~8%**
      - `diffMatchesPath`/`strings.HasPrefix`는 `pprof -top -cum -nodecount=200`에서 **미등장(컷 이하)** → **<~0.5%** 수준으로 취급 가능

### S1 pprof Top20 (flat%, cum%) — OFF vs ON

- `go tool pprof -top -nodecount=20 .pprof_s1_cpu_off.out`
- `go tool pprof -top -nodecount=20 .pprof_s1_cpu_on.out`

| 함수 | OFF flat% | OFF cum% | ON flat% | ON cum% |
|---|---:|---:|---:|---:|
| `syscall.syscall` | 45.33 | 45.33 | 40.07 | 40.07 |
| `runtime.pthread_kill` | 5.99 | 6.05 | 6.09 | 6.09 |
| `runtime.mallocgcSmallScanNoHeader` | 4.61 | 8.92 | 4.56 | 9.88 |
| `runtime.scanobject` | 3.89 | 9.04 | 3.73 | 9.28 |
| `runtime.pthread_cond_wait` | 4.19 | 4.19 | 3.66 | 3.66 |
| `runtime.usleep` | 2.81 | 2.81 | 2.83 | 2.83 |
| `runtime.pthread_cond_signal` | 1.62 | 1.62 | 0.70 | 0.70 |
| `runtime.mallocgcSmallNoscan` | 1.44 | 2.87 | 1.96 | 3.66 |
| `internal/bytealg.IndexByteString` | 1.38 | 1.38 | 2.23 | 2.23 |
| `runtime.mallocgc` | 1.20 | 14.13 | 1.10 | 15.81 |
| `runtime.nextFreeFast (inline)` | 1.20 | 1.20 | 1.93 | 1.93 |
| `runtime.findObject` | 1.14 | 1.62 | 1.56 | 2.23 |
| `runtime.greyobject` | 1.14 | 2.10 | 0.97 | 2.13 |
| `runtime.kevent` | 0.96 | 0.96 | 0.73 | 0.73 |
| `runtime.(*mspan).writeHeapBitsSmall` | 0.90 | 1.20 | 1.10 | 1.60 |
| `runtime.typePointers.next*` | 0.84 | 1.14 | 0.97 | 0.97 |
| `merkletrie.(*Iter).current` | 0.66 | 5.33 | 0.70 | 6.12 |
| (기타 runtime 항목) | - | - | - | - |

### 후보 항목별 누적 비중(OFF/ON) — “왜 S1에서 루프 역전 효과가 거의 없는가”

- OFF(`.pprof_s1_cpu_off.out`)의 `pprof -top -cum -nodecount=200` 기준:
  - `filesystem.(*ObjectStorage).EncodedObject` **cum 39.10%**
  - `os.OpenFile` **cum 36.95%**
  - `plumbing/object.DiffTree` **cum 18.08%**
  - `(*Repository).BlobObject` **cum 8.32%**
  - `diffMatchesPath`, `strings.HasPrefix` **미등장(컷 이하)** → **<~0.5%**
- ON(`.pprof_s1_cpu_on.out`)의 `pprof -top -cum -nodecount=200` 기준:
  - `filesystem.(*ObjectStorage).EncodedObject` **cum 34.78%**
  - `os.OpenFile` **cum 33.71%**
  - `plumbing/object.DiffTree` **cum 21.06%**
  - `(*Repository).BlobObject` **cum 8.42%**
  - `diffMatchesPath`, `strings.HasPrefix` **미등장(컷 이하)** → **<~0.5%**

### S1 결론(분류)

- **C) BlobObject 또는 I/O가 지배적이다**
  - 근거: `EncodedObject`/`OpenFile` 누적이 최상위, `DiffTree`가 그 다음, `BlobObject`도 유의미.
  - 반대로 루프 역전이 줄이려는 `diffMatchesPath`/`strings.HasPrefix`는 컷 이하로 관측되어, S1에서 OFF/ON 차이가 거의 없는 것이 pprof로 설명됨.

- (b) **해석**
  - “증명” 관점에서:
    - 코드상 per-commit마다 pending(N)을 전부 순회하며,
    - 각 pending 검사에서 changes(F)를 전부 선형 스캔하므로,
    - 최악(또는 pending이 늦게 비워지는 repo)에서 요청 비용이 커밋 수(C)에 선형으로 비례해 증가할 수밖에 없음.
  - 조기 종료는 “pending이 빨리 비워지는” 분포에서는 효과가 있으나,
    - “오래 안정적인 파일/디렉터리(마지막 변경이 과거)”가 많을수록 pending이 늦게 비워져 C가 커짐.

- (c) **다음 행동**
  - Top 5 hotspots를 “실측 기반”으로 확정하기 위한 최소 작업:
    - `internal/overview/handler_perf_test.go`의 tree entry 정렬 문제를 해결해 테스트를 통과시키고(프로파일 대상 시나리오 확보)
    - 또는 `GetTreeWithCommit` 경로를 직접 호출하는 synthetic large-repo benchmark를 추가
  - 그 후 pprof로 다음을 수집:
    - CPU top (DiffTree vs pending loop vs blob size)
    - alloc top (changes/strings/maps)
    - mutex/block (go-git 내부 경합 여부)

---

## 5) 해결책 옵션 설계 (최소 6개 이상, 트레이드오프 포함)

> 아래 옵션들은 모두 `internal/overview/service.go`의 `GetLatestCommitsForPaths` / `GetTreeWithCommit`의 구조를 기준으로 제안.

### 옵션 1) pending 중심(N)에서 “changes 중심(F)”으로 루프 역전 + prefix 인덱스

- **아이디어 요약**: 커밋마다 `changes`를 만든 뒤, `pending`을 전부 훑지 말고 `changes`의 path(from/to)만 보고 “영향받는 pending”만 갱신.
- **기대 복잡도 변화**: 대략 \(O(C \times (F + \text{matchCost}))\)로 전환 (현재 \(O(C \times N \times F)\)에서 N 제거 목표)
- **구현 난이도/범위**: 중 (자료구조 변경 + 경로 매칭 인덱스)
  - 변경 지점: `internal/overview/service.go` `GetLatestCommitsForPaths`, `diffMatchesPath` 대체
- **정확도 영향**
  - rename: `from/to` 모두 처리하면 현재 코드(`diffMatchesPath`)와 동일 수준 가능
  - merge: 현재도 first-parent만 보므로(정확도 제한 존재) 개선 여지 별도(옵션 6)
  - symlink/submodule: submodule은 현재 pending에서 제외됨(현행 유지/정책 결정 필요)
- **캐싱 전략**
  - key: `(repoPath, refHash, path)` 단위로 결과 캐시 가능(현재 latest-commits batch에는 연결됨)
  - TTL: 짧게(예: 5~30s) + refHash 기반이면 무효화 간단
- **실패/예외**
  - changes가 비정상적으로 큰 커밋(F 매우 큼)에서는 여전히 비쌈 → depth/limit 옵션 병행 필요

### 옵션 2) `object.DiffTree` 제거: “변경 파일 목록”을 더 싸게 얻는 경로로 전환

- **아이디어 요약**: per-commit마다 `DiffTree`로 전체 changes를 만들지 말고, 변경 파일 path만 얻는(혹은 go-git이 제공하는 더 경량 루틴) 방식으로 전환.
- **기대 복잡도 변화**: \(DiffTree\) 생성 비용 감소 + F 감소(또는 F 산출을 더 싸게)
- **구현 난이도/범위**: 중~높 (go-git API/구현 제약 조사 필요)
- **정확도 영향**: rename/merge 처리 품질은 선택한 변경 목록 추출 로직에 의존
- **캐싱 전략**: commitHash → changedPaths 캐시 (LRU)
- **실패/예외**: binary/large trees, merge commit 처리

### 옵션 3) 디렉터리 latest commit 계산 정책 변경(디렉터리는 미노출 or 별도 API)

- **아이디어 요약**: `entryType=="tree"`에 대한 latest commit 계산 비용이 커질 수 있으므로(현재 prefix match), 디렉터리에는 latest commit을 붙이지 않거나 별도 API로 분리.
- **기대 복잡도 변화**: N 감소(대상 pathInfos에서 tree 제외) + match 비용 감소
- **구현 난이도/범위**: 낮~중 (API 모델/클라이언트 영향)
- **정확도 영향**: 디렉터리 latest commit 미제공(스펙 변경)
- **캐싱 전략**: file(blob)만 캐시하면 hit율 상승
- **실패/예외**: UI/클라이언트 요구사항 조정 필요

### 옵션 4) 응답 모델 분리 + progressive disclosure (Tree 먼저, commits 비동기)

- **아이디어 요약**: `/overview/tree/commits`를
  - (A) tree만 빠르게 반환하는 API
  - (B) latest commits를 별도 endpoint에서 path 리스트로 조회하는 API
  - 로 분리하여 UI에서 점진적으로 렌더링.
- **기대 복잡도 변화**: tail latency를 분리/완화(최악 케이스에서도 tree UI는 빠르게)
- **구현 난이도/범위**: 중~높 (API 추가 + 클라이언트 변경)
- **정확도 영향**: eventual consistency(비동기) 허용
- **캐싱 전략**: (repo, refHash, pathListHash) 키로 결과 캐시
- **실패/예외**: pathList가 매우 큰 경우(한 디렉터리에 N이 큰 경우) batch 처리 필요

---

## 부록) Tree/Commits 분리(Progressive API) 구현 — latest-commits batch

### (A) 신규 endpoint 스펙(구현 반영)

- **Route**: `POST /repos/:owner/:repo/overview/latest-commits`
- **Request(JSON)**: `LatestCommitsBatchRequest`
  - `ref`: string (required; server는 내부에서 `resolved_ref_hash`로 고정)
  - `dir_path`: string (optional; `""`=root)
  - `paths`: []string (required; `dir_path` 하위의 file paths)
- **Response(JSON)**: `LatestCommitsBatchResponse`
  - `commits_by_path`: `map[path]GitCommit`
  - `meta`:
    - `resolved_ref_hash`
    - `incomplete` (partial 허용)
    - `scanned_commits`
    - `time_budget_ms`
    - `cut_reason` (`complete|time_budget|max_commits|context_cancelled|end_of_history|cache_hit|error`)
  - `missing_paths`: incomplete 시 UI placeholder 용

### (A.1) OpenAPI 스펙 정합화(구현 반영)

- 파일: `specs/generated/collabops-git-executor.yaml`
  - `GET /repos/{owner}/{repo}/overview/tree/commits`에 누락된 query 파라미터 `ref`, `path`를 추가
  - `POST /repos/{owner}/{repo}/overview/latest-commits` 신규 path + request/response schema 추가

### (B) “9초 tail 제거” 강제 상한 2개(구현 반영)

- **환경변수**
  - `OVERVIEW_LATEST_COMMITS_TIME_BUDGET_MS=<ms>` (기본 300; batch endpoint에 항상 적용)
  - `OVERVIEW_LATEST_COMMITS_MAX_COMMITS=<n>` (기본 10000; batch endpoint에 항상 적용)
- **적용 위치**
  - `internal/overview/service.go:GetLatestCommitsForPathsWithMeta` (신규 batch endpoint용 meta 포함; partial 허용)
  - `/overview/tree/commits` 등 기존 결합 endpoint는 **partial 금지(A안)** → `GetLatestCommitsForPaths`는 상한 미적용(complete only)

### (C) 캐시(정상 속도; 구현 반영)

- **근거(pprof)**: S1에서 `os.OpenFile`/`filesystem.(*ObjectStorage).EncodedObject`가 지배적 → 동일 입력 반복 시 캐시 hit이면 p95/p99 하락 기대
- **구현**
  - `internal/overview/overview_cache.go`: `latest_commits_batch` 캐시 + singleflight
  - key: `(repoPath, resolvedRefHash, dirPath|pathsHash)`
  - complete 결과만 캐시(부분 결과는 cache miss로 유지)

### (D) S1 벤치 검증(상한/캐시 전후)

- **벤치**
  - `internal/overview/performance_bench_test.go`
    - `Benchmark_LatestCommitsCaps_S1_NHeavy_Uncapped`
    - `Benchmark_LatestCommitsCaps_S1_NHeavy_TimeBudget50ms`
    - `Benchmark_LatestCommitsCache_S1_NHeavy_WarmHit`
- **실행 예시**
  - `go test ./internal/overview -run '^$' -bench 'Benchmark_LatestCommitsCaps_S1_NHeavy_.*|Benchmark_LatestCommitsCache_S1_NHeavy_WarmHit' -benchtime=3s -count=1 -benchmem`
- **결과(실측; M2 Pro)**
  - Uncapped: `1.245s/op`, `1.76GB/op`, `32.2M allocs/op`
  - TimeBudget(50ms): `53.4ms/op`, `90.0MB/op`, `1.64M allocs/op` (partial)
  - Cache warm-hit: `264.7ns/op`, `400 B/op`, `6 allocs/op`

> 해석: pprof에서 지배적인 `OpenFile/EncodedObject/DiffTree`를 “덜 하게” 만드는 방법은 (a) 상한으로 tail을 강제 절단하거나 (b) 동일 입력 반복을 캐시로 흡수하는 것뿐이다(매칭 루프 최적화는 S1에서 컷 이하로 관측).

### 옵션 5) 서버 강제 제어: depth/pagination/limit 도입으로 N을 상한 관리

- **아이디어 요약**: 현재 `GetTreeWithCommit`은 N이 무제한이므로, API에 `limit/offset`(또는 page/size)을 도입하여 한 요청에서 처리하는 N을 제한.
- **기대 복잡도 변화**: 요청당 \(N\) 상한으로 tail latency 관리
- **구현 난이도/범위**: 중 (API/스키마/클라이언트 변경)
- **정확도 영향**: page 경계에서 commit 계산도 page 범위로만 제공(정책 명확화 필요)
- **캐싱 전략**: (repo, refHash, path, page, limit) 캐시
- **실패/예외**: 클라이언트가 무한 스크롤 등으로 N 전체를 결국 요청하면 총비용은 유지(단, UX 개선)

### 옵션 6) merge 정확도 개선(현재 first-parent만 사용) + 비용/정확도 트레이드오프 선택

- **아이디어 요약**: 현재 `GetLatestCommitsForPaths`는 `ParentHashes[0]`만 사용하여 merge commit에서 변경 판정이 실제와 달라질 수 있음. 정책을 명시:
  - (A) first-parent 기준으로 정의(현행 유지) → 빠름, 단 정확도 제한
  - (B) merge commit에서 모든 parent를 비교 → 정확도↑, 비용↑
  - (C) merge는 skip하고 non-merge만 기준 → 정책 단순
- **기대 복잡도 변화**: (B)는 per-commit 비용 증가(부모 수만큼 diff)
- **구현 난이도/범위**: 중
- **캐싱 전략**: merge commit diff 결과 캐시
- **실패/예외**: 복잡 merge history에서 비용 폭증 가능

### 옵션 7) repo 규모 기반 degraded mode (정책 기반)

- **아이디어 요약**: large repo(커밋 수/트리 엔트리 수/변경 수 기준)에서는 latest commit 계산을 축소(파일만/상위 K개만/비동기)하는 모드로 전환.
- **기대 복잡도 변화**: 최악 케이스를 정책적으로 차단
- **구현 난이도/범위**: 중 (메트릭 + 플래그 + 정책)
- **정확도 영향**: 일부 기능 저하(정의 필요)
- **캐싱 전략**: degraded 결과도 캐시
- **실패/예외**: 임계치 튜닝/관측 필요

### 옵션 8) 결과 캐시 도입(이미 구현된 `OverviewCache` 활용)

- **아이디어 요약**: `OverviewCache`(TTL+LRU+singleflight)를 실제 경로에 연결하여 동일 `(repo, refHash, path)` 요청 반복 시 계산을 회피.
- **기대 복잡도 변화**: cold miss는 동일, warm hit는 O(1) 수준
- **구현 난이도/범위**: 중 (핫 경로에 캐시 주입, 무효화 정책 명확화)
- **정확도 영향**: TTL 동안 stale 허용(문서에 명시 필요)
- **캐싱 전략**:
  - key: `(repoPath, resolvedRefHash, path)`
  - TTL: 짧게(예: 5~30s) + refHash 기반이면 사실상 immutable에 가까움
- **실패/예외**: 메모리 압박(LRU), ref가 HEAD일 때 refHash resolve 후 key 구성 필요

---

## 6) 선택/검증 계획 (실제로 빨라졌는지 증명)

- (a) **발견한 사실**
  - 현재 `GetTreeWithCommit` 경로는 요청 파라미터로 N/C를 제한할 수단이 없음(섹션 1).
  - `GetLatestCommitsForPaths`는 조기 종료는 있으나, 최악에선 히스토리 전체(C)를 순회할 수 있음.
  - large-ish 입력 기반 perf test/벤치/pprof로 실측 기반 목표치 산정 가능.

- (b) **해석**
  - 목표치/검증은 “정확도 테스트”와 “성능 테스트”를 분리해야 함:
    - 정확도는 rename/merge/submodule/symlink 등 edge 케이스 중심
    - 성능은 C/N/F를 독립적으로 확장하는 매트릭스 테스트로 구성

- (c) **다음 행동**
  - (1) baseline 시나리오(small/medium/large)에서 p50/p95/p99, alloc/op, bytes/op, goroutine 수를 기록
  - (2) 옵션 적용 후 동일 시나리오로 비교
  - (3) 정책 기반 degraded mode + fallback

### (1) baseline 대비 목표치(제안; 실측 후 조정)

- **목표(large 시나리오 기준)**: p95를 “수초” → “수백 ms”대로 낮추는 것을 1차 목표로 설정
  - 단, 현재 레포에는 large baseline 실측치가 없으므로(overview perf test 실패), 목표치는 large baseline을 먼저 측정한 후 확정

### (2) 정확도 테스트 vs 성능 테스트

- **정확도 테스트(케이스 제안)**
  - rename: `from/to`가 바뀌는 커밋에서 path 최신 커밋 판정
  - merge: merge commit에서 first-parent 정책 여부에 따른 차이
  - empty commits: tree hash 변화 없음
  - submodule entry: `Type=="commit"` (현재 latest commit 계산 대상에서 제외됨) 정책 확인
  - symlink: go-git entry mode 처리 확인(필요 시 정책 명시)
- **성능 테스트(매트릭스)**
  - C만 증가: 커밋 1k/10k/50k
  - N만 증가: 한 디렉터리 1k/5k/10k 엔트리
  - F만 증가: 커밋당 변경 10/100/500 paths
  - 3차원 조합: (C,N,F) 교차로 p99 폭발 구간 확인

### (3) 롤아웃 계획

- **정책 기반 적용**
  - large-repo 감지(커밋 수/트리 엔트리 수) 기반으로 degraded mode를 적용(토글이 아니라 정책)
- **canary(선택)**
  - 특정 owner/repo 또는 특정 크기 구간만 우선 적용(운영 라우팅/설정으로 제한)
- **fallback**
  - 에러/타임아웃 시 tree만 반환하고 commits는 빈 값 또는 별도 요청 유도
- **observability**
  - endpoint별 p50/p95/p99
  - `GetTreeWithCommit` 단계별 타이머(섹션 3)
  - 요청당 C/N/F 관측치(pending 종료 시점, changes 길이 분포)

---

## 결론: 가장 ROI 높은 2개 옵션(추천)

- **추천 1: 옵션 1 (루프 역전: changes 중심 + pending 인덱스)**
  - **이유(코드 근거)**: 현재 병목 구조가 `GetLatestCommitsForPaths`의 `C × N × F` 중첩 루프로 고정되어 있음(`internal/overview/service.go`).
  - **기대 효과**: N을 루프에서 제거하여 worst-case를 구조적으로 낮춤.
  - **프로파일 근거**: 현재 large baseline 프로파일은 `internal/overview/handler_perf_test.go` 실패로 미확정 → 우선 large baseline을 통과/측정한 뒤 pprof로 “pending loop vs DiffTree” 비중을 확인해 효과를 수치로 확정.

- **추천 2: 옵션 5 (pagination/limit 도입으로 N 상한 관리)**
  - **이유(코드 근거)**: 현재 `/overview/tree/commits`는 N을 제한하는 파라미터가 없어 한 요청에 “디렉터리 전체 엔트리(N)”가 그대로 들어감(`internal/overview/handler.go`).
  - **기대 효과**: 알고리즘 개선 전에도 tail latency를 정책적으로 제한 가능(요청당 N 상한).
  - **프로파일 근거**: large baseline이 준비되면 “N 증가에 따른 alloc/op 및 CPU”를 먼저 측정하고, limit 도입 전/후를 비교하여 효과를 계량화.

