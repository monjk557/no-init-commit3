# executor 죽은 코드 삭제 리스트

검증 방법: `go list -deps ./cmd/git-executor`(바이너리 의존성 그래프) +
`deadcode -test ./cmd/git-executor`(도달성 분석) + import/호출 역추적.
**grep 단독 판정 금지** — 이 세션에서 여러 번 틀렸다. 삭제 전 각 항목을 아래 명령으로 재확인.

## 요약

| 티어 | 대상 | 프로덕션 | 테스트 | 확신 |
|---|---|---:|---:|:---:|
| A | 패키지 통째 (import 0) | ~3,700줄 | ~1,786줄 | **확정** |
| B | 패키지 통째 (일부 도달, 표면 좁음) | ~9,500줄 | ~3,660줄 | 높음 |
| C | 패키지 내 죽은 함수 | ~640줄 | ~334줄 | 중간 |

---

## Tier A — 프로덕션 import 0건 (즉시 삭제 가능)

`grep -rn 'internal/<pkg>' --include='*.go' internal/ cmd/ | grep -v _test | grep '"github'` → **0건 확인됨**.

### A1. `internal/security/` — 전량 미사용  ✅ 삭제 완료 (3,274줄)
- 파일: `rate_limiter.go` · `tls_config.go` · `validation.go` (+테스트 5)
- 프로덕션 1,627줄 / 테스트 1,647줄 (**테스트가 더 많다**)
- 실제 보안: `middleware`(JWT) + `ruleguard`(정책). 이건 안 불림
- **삭제**: `rm -rf internal/security/`
- 재확인: `go build ./... && go list -deps ./cmd/git-executor | grep security` (0이어야)

### A2. `internal/streaming/` — 전량 미사용  ✅ 삭제 완료 (445줄)
- 파일: `git_stream.go` (+테스트 1)
- 프로덕션 306줄 / 테스트 139줄
- **삭제**: `rm -rf internal/streaming/`
- 재확인: `grep -rn 'internal/streaming' internal/ cmd/ | grep '"github'` (0)

---

### A3. `tests/security.disabled/` — security 삭제로 컴파일 불가  ✅ 삭제 완료 (1,507줄)
- QUEUE 에 없던 발견. `.disabled` 접미(빌드 제외) + 삭제된 security 참조. 확정 죽은 코드.

## Tier B — 일부만 도달, 죽은 표면 큼 (표면 확정 후 삭제)

`go list -deps` 에는 뜨지만(=일부 심볼 살아있음), 죽은 파일이 대부분.

### B1. `internal/performance/` — 통째 죽은 7파일 + cmd/test-* 삭제  ✅ 부분 완료

> **삭제 완료(파일단위 안전분)**: deadcode 로 "살아있는 심볼 0" 확정된 7파일 +
> throwaway cmd/test-* 9개.
> - performance: resource_manager, zero_copy_json, access_pattern_tracker,
>   predictive_prefetch, streaming_json_optimizer, optimized_response_middleware,
>   multi_tier_cache (~3,600줄)
> - cmd/test-* 9개 (2,554줄) — Makefile 이 "throwaway" 명시, 실 바이너리와 독립
> **잔여 보류(함수단위 정밀수술 필요)**: binary_protocol/commitgraph/config/
> file_descriptor_pool/myers_diff/object_cache/parallel_tree/smart_language_detector
> 8파일. deadcode 상 대부분 죽었으나, **살아있는 진입점(GetLatestCommitsForPathsWithMeta
> → OptimizeLogTraversal)과 죽은 진입점(GetLanguageDistribution → NewSmartLanguageDetector)이
> 같은 struct 를 공유**해 파일 단위로 못 자른다. overview 죽은 메서드부터 함수단위로
> 제거해야 이 8파일이 죽는다. → B3 정밀수술과 한 묶음.

### B1-원본. `internal/performance/` — 57% 죽음 (T14 확정)

> **보류 사유 (deadcode 정밀 분석)**: `deadcode -test ./cmd/git-executor` 기준
> **performance 170함수 중 152(89%)가 unreachable**. T14 의 "57%"는 grep 역추적이라 과소평가였다.
> - grep 은 `overview/service.go:251` 의 `NewSmartLanguageDetector` 호출을 "살아있음"으로 봤으나,
>   그 호출부는 **`GetLanguageDistribution`(deadcode 죽음) 안**이다. 죽은 함수가 죽은 함수를 부르는 사슬.
> - `myers_diff` 도 마찬가지 — `diffs/myers_diff_adapter.go` 가 쓰지만 그 adapter 자체가 deadcode 죽음.
> - 즉 **performance + indexing + overview-gogit + myers-adapter 가 하나의 죽은 클러스터**다.
> - 파일 단위로 지우면 얽힌 참조로 컴파일이 깨진다. **클러스터 통째 제거**가 필요하고 자동 루프 범위 밖.
>
> **살아있는 최소 표면(진짜)**: deadcode 에 없는 함수만 = 18/170. 이것만 남기고 나머지 삭제하려면
> overview 죽은 계층(B3)·indexing(B2)과 **한 PR 로 묶어** 정밀 수술해야 한다. 수동 리뷰 대상.

### B1-원본. `internal/performance/` — 57% 죽음 (T14 확정)
바이너리에서 **실제 사용되는 심볼 20종만 살아있음**:
```
CommitGraphOptimizer NewCommitGraphOptimizer  (overview)
FileDescriptorPool   NewFileDescriptorPool    (overview)
ObjectCache          NewObjectCache           (overview)
MyersDiffOptimizer   NewMyersDiffOptimizer    Edit{Delete,Equal,Insert}  (diffs)
ParallelTreeProcessor NewParallelTreeProcessor (overview)
SmartLanguageDetector NewSmartLanguageDetector LanguageStatsMaxFileSize (langstats)
OptimizedPktLineWriter NewOptimizedPktLineWriter OptimizedSidebandWriter NewOptimizedSidebandWriter (protocol)
PerformanceConfig    NewPerformanceConfig
```
**죽은 파일 (위 심볼을 정의하지 않는 것 = 삭제 대상)**:
- `access_pattern_tracker.go` (cmd/test-* 전용)
- `predictive_prefetch.go` · `zero_copy_json.go` · `streaming_json_optimizer.go`
- `optimized_response_middleware.go` · `resource_manager.go`
- `multi_tier_cache.go`(performance 판, cache 것과 다름) · `binary_protocol.go` 중 미사용분
- **삭제 전 필수**: 파일별로 `deadcode` 로 재확인 — 살아있는 심볼이 죽은 파일에 섞여 있을 수 있음(P17/P18 에서 실제로 그랬음)
- 재확인: `deadcode -test ./cmd/git-executor | grep internal/performance`

### B2. `internal/indexing/` — 프로덕션 db=nil 로 영구 비활성  ⏸ 보류 (B3 와 동일 — 런타임 죽음)

> **보류 사유**: `handler.go:55` 이 `NewRepositoryOverviewService(..., nil)` 로 db=nil 을
> 넘겨 indexing 경로 전체가 **런타임 미실행**이나, `service.go:107` 등에 `db != nil` 분기로
> **컴파일 참조가 남아** deadcode(정적 도달성)로는 안 잡힌다. `NewQueryService`/`NewIndexBuilder`
> 가 그 분기 안에서 참조된다. indexing 을 지우려면 overview 의 db-분기(service.go +
> service_indexing_integration.go 441줄 + commit_index.go)까지 함께 제거하는 정밀 수술이
> 필요하다. B3 와 한 묶음으로 **수동 리뷰** 대상. 규모: indexing 2,358줄 + overview 통합 ~1,000줄.

### B2-원본. `internal/indexing/`
- 파일: `builder.go` · `query.go` · `webhook_handler.go` (+테스트 4)
- 프로덕션 2,358줄 / 테스트 978줄
- **주의**: `builder.go` 는 `overview/service_indexing_integration.go` 가 참조. 그 통합 경로도 죽었는지 함께 확인
- 연동 삭제: `overview/service_indexing_integration.go` + `overview/commit_index.go`
- 재확인: `grep -rn 'indexing\.' internal/overview/*.go | grep -v _test` 의 호출이 죽은 핸들러에서만 오는지

### B3. `overview/service.go` 의 죽은 gogit 조회 함수  ⏸ 보류 (자동 삭제 위험)

> **보류 사유 (deadcode 정밀 분석 결과)**: 단순 함수 삭제가 아니라 계층 혼재다.
> - `deadcode -test ./cmd/git-executor` 가 overview 에서 **49개 죽은 함수**를 보고
>   (`RepositoryOverviewService` 의 gogit 메서드 대부분 + `service_indexing_integration` +
>   `commit_index` + `SmartPagination`).
> - 그러나 **살아있는 함수가 같은 파일·같은 struct 에 섞여 있다**:
>   - `service.go`: `GetLatestCommitsForPathsWithMeta`(살아있음, `HandlePostLatestCommitsBatch`
>     :763 → :889 이 호출) vs `GetLatestCommitForPath`(단수, 죽음). **이름이 비슷해 혼동 위험.**
>   - `service_indexing_integration.go`: `GetAllCommits`/`GetCommitsBySHAs`/`OpenRepository` 살아있음
>   - `pagination.go`: `NewSmartPagination` 살아있음
> - **`overview` 의 go-git 은 죽지 않았다** — 배치 조회 API 가 여전히 `repoWrapper.GetRepo()`
>   (go-git)를 통해 `GetLatestCommitsForPathsWithMeta` 를 쓴다. 원래 리스트의 전제("호출 0")는
>   단수 함수엔 맞으나 복수 함수엔 틀렸다.
>
> **필요 조치**: struct 메서드를 하나씩 지우며 매번 컴파일 확인하는 정밀 수술이 필요하다.
> 자동 루프로는 위험하므로 **수동 리뷰 대상**으로 남긴다. 49개 목록은 `/tmp/ov_dead.txt` 참조.
> (원래 항목 아래 유지)

### B3-원본. `overview/service.go` 의 죽은 gogit 조회 함수 (신규 발견)
호출 0 확정된 함수 (핸들러는 `Native*` 만 씀):
- `GetLatestCommitForPath` (:476) · `GetRemotes` (:176) · `GetLanguageDistribution` (:196)
- `GetTreeOnly` (:896) · `GetTreeWithCommitPaginated` · `GetTreeWithCommitIndexed`
- `convertCommit` · `matchInTree` · `loadGitmodulesFromTree` 등 이들의 헬퍼
- **이게 `overview` 의 go-git 의존 대부분** → 삭제 시 `service.go` 의 gogit import 크게 줄어듦
- 재확인: 각 함수 `grep -rn '\.<fn>(' internal/ | grep -v _test | grep -v 'func\|service.go'` = 0

---

## Tier C — 패키지 내부 죽은 단위

### C1. `merge/transaction.go`  ✅ 해당 없음 — 최신 main 에 파일 없음(이미 처리됨)
- `NewTransactionManager` 외부호출 2건 — **완전히 죽지 않음**. 호출처가 살아있는지 먼저 확인
- 640줄 + 테스트 334줄
- 재확인: 호출 2건이 죽은 코드에서 오는지 추적 후 결정

### C2. `internal/cache/lru.go` 구세대 캐시  ✅ 삭제 완료 — lru+factory+interface+config (~1,250줄)
- `deadcode` 가 이 파일에서 **33개 죽은 함수** 발견 (최다)
- `NewLRUCache`·`NewLRUCacheForTesting`·`GetRepository` 등 구세대 캐시
- **주의**: 같은 패키지 `memory_cache.go`·`multi_tier_cache.go` 는 살아있음(overview 사용)
- 파일 통째 삭제 아님 — 죽은 함수만
- 재확인: `deadcode -test ./cmd/git-executor | grep internal/cache/lru`

### C3. `internal/git/operations.go`  ✅ 삭제 완료 — 패키지 통째(95줄, import 0)
- 프로덕션 43줄 / 테스트 52줄 (테스트가 더 많음)
- 재확인: `grep -rn 'git\.' internal/ | grep -v _test | grep -iE '\bgit\.[A-Z]'` — config/server 의 `git.repo_root` 는 무관(config 필드)

---

## 삭제 순서 (안전순)

1. **Tier A** (security, streaming) — import 0, 위험 0. 삭제 → `go build ./...` → `make test-internal`
2. **B3** (overview gogit 함수) — go-git 축소 효과 큼, 호출 0 확정
3. **B2** (indexing + overview 통합) — 연동 주의
4. **B1** (performance 죽은 파일) — **파일별 deadcode 재확인 필수**
5. **C** (transaction, lru, git) — 부분 삭제, 호출처 추적

각 단계 후: `go build ./... && make test-internal && golangci-lint run -c .golangci-interfaces.yml`

## go-git 제거 최종 목표
Tier B3 삭제 후 `overview` 의 go-git 은 대부분 사라진다. 남는 것:
- `repositories/repositories_manager.go`: `PlainOpen` + `RepositoryWrapper`(ResolveReference/CommitObject 얇은 층)
- `diffs/gogit_compat.go`: merge/preview 가 아직 참조
- `testutil/gitrepo/`: 픽스처 (정당, 유지)
이 얇은 층까지 native 화하면 `go.mod` 에서 go-git 직접 의존성 삭제 가능.

---

## 최종 요약 (루프 종료)

### 삭제 완료 (프로덕션 6,639줄 + 테스트)
| 티어 | 대상 | 규모 | 검증 |
|---|---|---:|---|
| A | `internal/security/` | 1,627줄 | import 0 + 그래프 부재 |
| A | `internal/streaming/` | 306줄 | import 0 |
| A | `tests/security.disabled/` | 1,507줄 | .disabled + security 참조 |
| C3 | `internal/git/` | 95줄 | import 0, PerformOperation |
| C2 | `cache/lru+factory+interface+config` | ~1,250줄 | deadcode 전체 unreachable |

각 단계 `go build ./...` + `go vet` + 영향 패키지 테스트 통과. 잔존 참조 0.

### 보류 (자동 삭제 불가 — 수동 정밀수술 필요)
**B1/B2/B3 는 하나의 죽은 클러스터**로 얽혀 있다:
- `performance`(170함수 중 152=89% 죽음) + `indexing`(db=nil 런타임 죽음) +
  `overview` gogit 조회 계층(GetLatestCommitForPath 등) + `diffs/myers_diff_adapter`
- **grep 이 "살아있다"고 오판한 이유**: 죽은 함수가 죽은 함수를 부르는 사슬.
  `overview/service.go:251` 의 `NewSmartLanguageDetector` 호출은 `GetLanguageDistribution`
  (deadcode 죽음) 안이다. 호출부가 있으니 grep 은 산 것으로 봤으나 실질 죽음.
- **살아있는 반례**: `GetLatestCommitsForPathsWithMeta`(복수형)는 배치 API 가 여전히 사용.
  단수형 `GetLatestCommitForPath` 와 이름이 비슷해 혼동 위험 → deadcode 로만 구분 가능.
- 파일 단위로 지우면 얽힌 참조로 컴파일이 깨진다. **한 PR 로 묶어 정밀 수술** 필요.

### go-git 직접 의존성 제거: **아직 불가**
남은 go-git import (비테스트, 비gogit):
- **보류 클러스터**: performance(6파일) · indexing · overview(service/integration/commit_index)
- **살아있는 얇은 층**: `repositories`(PlainOpen + RepositoryWrapper) · `diffs/gogit_compat`(merge/preview 참조)
- **정당**: `testutil/gitrepo`(픽스처)
→ Tier B 클러스터 정밀수술 + 저장소 열기 native 화가 끝나야 `go.mod` 에서 go-git 제거 가능.

### 방법론 교훈
이 세션 내내 반복된 **"grep 0건/미사용 판정 오류"의 결정판**. `deadcode`(정적 도달성) +
`go list -deps`(의존성 그래프) 교차검증이 없었다면 클러스터를 무턱대고 지워 컴파일이
깨졌을 것. **"없음/죽음"은 grep 으로 증명할 수 없다** — 도구로 교차확인해야 한다.
