# operation.go 리팩토링 설계 문서

## 1. 현재 상태 진단

### 문제점

| 항목 | 현재 상태 | 문제 |
|------|----------|------|
| 파일 크기 | 1617줄 | Go 권장(200-500줄)의 3-4배 초과 |
| 책임 | 분석, 실행, Tree 병합, Conflict 해결 혼합 | SRP 위반 |
| 함수 길이 | performRebase ~200줄, resolveFileForMerge ~150줄 | 50줄 권장 초과 |
| 중복 코드 | author/committer 설정 패턴 4회 반복 | DRY 위반 |
| 테스트 용이성 | 낮음 | mocking 어려움 |

### 현재 operation.go의 책임

```
operation.go (1617줄)
├── 분석 로직
│   ├── analyzeMerge()
│   └── analyzeRebaseDryRun()
├── 실행 로직
│   ├── performMerge()
│   ├── performSquashMerge()
│   ├── performRebase()
│   ├── performThreeWayMerge()
│   └── performFastForwardMerge()
├── Tree 병합 로직
│   ├── createMergedTree()
│   ├── createMergedTreeWithBase()
│   ├── mergeTreeEntries()
│   ├── resolveTreeEntry()
│   └── mergeDirectoryEntry()
├── Conflict 해결 로직
│   ├── resolveMergeTree()
│   ├── resolveFileForMerge()
│   ├── resolveTypeConflict()
│   └── hasTypeConflict()
└── Helper 함수
    ├── generateMergeCommitMessage()
    ├── generateSquashCommitMessage()
    ├── getMergeType()
    ├── commitToInfo()
    └── 기타 유틸리티
```

---

## 2. 설계 원칙

### 핵심 원칙

1. **순수 레이어는 진짜 순수하게**
   - `context.Context` 모름
   - `go-git` 타입 직접 사용 안함 (자체 DTO 사용)
   - 부작용 0

2. **프레임워크 격리**
   - `errors.go`는 `gin.H` 모름
   - HTTP 변환은 `handler_helpers.go`에서만

3. **공통화보다 명확성**
   - Strategy 인터페이스 공통화 안함
   - executor에서 switch 분기 (명확함)

4. **Plan과 Result 분리**
   - `ResolutionPlan`: 결정만 (ResultHash 없음)
   - `ApplyResult`: 실제 결과

5. **점진적 치환**
   - 한번에 구조 바꾸지 않음
   - 죽은 코드 만들지 않음

---

## 3. 최종 파일 구조

```
internal/merge/
├── # Layer 0: Pure Types (의존성 0, go-git 모름)
├── errors.go            # sentinel + typed error
├── snapshot.go          # TreeSnapshot, FileSnapshot (자체 DTO)
├── plan_types.go        # ResolutionPlan (결정만, 결과 없음)
├── signature.go         # 순수: author/committer 생성
├── message.go           # 순수: commit/response 메시지
│
├── # Layer 1: Pure Logic (ctx 모름, go-git 모름)
├── tree_plan.go         # TreeSnapshot → ResolutionPlan
├── conflict_plan.go     # 충돌 해결 계획
│
├── # Layer 2: Adapters (go-git ↔ DTO 변환)
├── snapshot_adapter.go  # object.Tree → TreeSnapshot
├── env.go               # MergeEnv
│
├── # Layer 3: Apply (부작용)
├── tree_apply.go        # plan → tree 저장 → ApplyResult
├── commit_apply.go      # commit 생성 + ref 업데이트
│
├── # Layer 4: Analysis
├── analyzer.go          # handler에서 분리
│
├── # Layer 5: Strategies (전략별 분리, 공통 인터페이스 없음)
├── merge_strategy.go    # ThreeWayMerge 전용
├── squash_strategy.go   # Squash 전용
├── rebase_strategy.go   # Rebase 전용
│
├── # Layer 6: Orchestration
├── executor.go          # switch 분기 (공통화 안함)
│
├── # Layer 7: HTTP
├── handler.go           # 기존 유지
├── handler_helpers.go   # writeError, writeSuccess
│
├── # 기존 유지
├── schema.go
├── conflict_detector.go
├── preview.go
├── validator.go
```

---

## 4. 의존성 그래프

```
Layer 7 (HTTP)
    │
    ▼
Layer 6 (Orchestration)
    │
    ├──────────────────┐
    ▼                  ▼
Layer 5 (Strategies)  Layer 4 (Analysis)
    │
    ├──────────────────┐
    ▼                  ▼
Layer 3 (Apply)       Layer 2 (Adapters)
    │                  │
    ▼                  ▼
Layer 1 (Pure Logic)  Layer 0 (Pure Types)
    │
    ▼
Layer 0 (Pure Types)
```

**규칙**: 화살표는 위에서 아래로만. 순환 의존 불가.

---

## 5. 핵심 타입 설계

### snapshot.go - go-git 격리

```go
// TreeSnapshot: go-git과 완전히 분리된 자체 DTO
type TreeSnapshot struct {
    Entries map[string]FileSnapshot
}

type FileSnapshot struct {
    Hash   string  // plumbing.Hash.String()
    Mode   uint32
    IsDir  bool
    Size   int64
}
```

### plan_types.go - 결정만, 결과 없음

```go
// ResolutionPlan: 순수 결정 (ResultHash 없음!)
type ResolutionPlan struct {
    Entries  []PlannedEntry
    Strategy MergeStrategy
}

type PlannedEntry struct {
    Path       string
    Action     EntryAction
    SourceHash string
    TargetHash string
    Mode       uint32
    // ResultHash 없음! (결과는 ApplyResult에)
}

// ApplyResult: 적용 결과 (별도 타입)
type ApplyResult struct {
    TreeHash      string
    AppliedHashes map[string]string
}
```

### errors.go - sentinel + typed error

```go
// Sentinel (카테고리)
var (
    ErrConflict  = errors.New("merge conflict")
    ErrNotFound  = errors.New("not found")
    ErrLocked    = errors.New("resource locked")
)

// Typed error (디테일)
type NotFoundError struct {
    Kind string  // "ref", "commit", "repo"
    Name string
}

func (e NotFoundError) Unwrap() error { return ErrNotFound }

// API Error (프레임워크 무관)
type APIError struct {
    Code    string         `json:"code"`
    Status  int            `json:"-"`
    Message string         `json:"message"`  // 고정 템플릿
    Details map[string]any `json:"details,omitempty"`
}

// 매핑: errors.As로 디테일 추출
func ToAPIError(err error) APIError {
    var notFound NotFoundError
    if errors.As(err, &notFound) {
        return APIError{
            Code:    "NOT_FOUND",
            Status:  404,
            Message: "The requested resource was not found",
            Details: map[string]any{"kind": notFound.Kind, "name": notFound.Name},
        }
    }
    // ...
}
```

---

## 6. PR 전략 (5단계)

### PR-1: 파일 이동만 (행동 변화 0)

- operation.go에서 함수들을 새 파일로 이동
- 기존 코드는 그대로 호출됨
- **검증**: `make test` 통과

### PR-2: errors + handler writeError

- `errors.go` 생성 (typed error 포함)
- `handler_helpers.go` 생성
- handler.go에서 writeError 사용
- **검증**: 에러 응답 포맷 테스트

### PR-3: Analyzer 분리 + Executor 도입

- `analyzer.go` 생성
- `executor.go` 생성
- handler.go가 executor 사용
- **검증**: E2E 테스트

### PR-4: Snapshot + 순수 레이어 (MergeMethodMerge만)

- `snapshot.go`, `snapshot_adapter.go` 생성
- `plan_types.go`, `tree_plan.go` 생성
- MergeMethodMerge만 먼저 치환
- **검증**: 순수 레이어 테스트 + merge E2E

### PR-5: 나머지 전략 + operation.go 삭제

- `squash_strategy.go`, `rebase_strategy.go` 생성
- operation.go 삭제
- **검증**: 전체 E2E + atomicity/concurrency 테스트

---

## 7. 테스트 전략

### Layer 1 테스트 (순수)

```go
func TestPlanTreeMerge_Determinism(t *testing.T) {
    // TreeSnapshot 직접 생성 (go-git 없음!)
    base := TreeSnapshot{...}
    source := TreeSnapshot{...}
    target := TreeSnapshot{...}
    
    plan1, _ := PlanTreeMerge(base, source, target, nil, strategy)
    plan2, _ := PlanTreeMerge(base, source, target, nil, strategy)
    
    assert.Equal(t, plan1, plan2)
}
```

### Error 테스트

```go
func TestToAPIError_TypedError(t *testing.T) {
    err := NotFoundError{Kind: "ref", Name: "feature/test"}
    apiErr := ToAPIError(err)
    
    assert.Equal(t, 404, apiErr.Status)
    assert.Equal(t, "ref", apiErr.Details["kind"])
}

func TestToAPIError_WrappedError(t *testing.T) {
    inner := NotFoundError{Kind: "commit", Name: "abc123"}
    wrapped := fmt.Errorf("rebase: %w", inner)
    
    apiErr := ToAPIError(wrapped)
    assert.Equal(t, 404, apiErr.Status)  // 래핑 후에도 동작!
}
```

### Atomicity 테스트

```go
func TestFinalize_AtomicityOnFailure(t *testing.T) {
    // ref 업데이트 실패 시 repo 상태 불변 확인
}
```

### Concurrency 테스트

```go
func TestExecute_ConcurrentMerge_OneLocked(t *testing.T) {
    // 동시 merge 2개 → 1개 성공, 1개 423
}
```

---

## 8. 예상 결과

| Layer | 파일 | 줄 수 | ctx | go-git | 부작용 |
|-------|------|-------|-----|--------|--------|
| 0 | errors.go | ~120 | X | X | X |
| 0 | snapshot.go | ~60 | X | X | X |
| 0 | plan_types.go | ~80 | X | X | X |
| 0 | signature.go | ~60 | X | X | X |
| 0 | message.go | ~50 | X | X | X |
| 1 | tree_plan.go | ~180 | **X** | **X** | **X** |
| 1 | conflict_plan.go | ~100 | **X** | **X** | **X** |
| 2 | snapshot_adapter.go | ~80 | X | O | X |
| 2 | env.go | ~40 | O | O | X |
| 3 | tree_apply.go | ~150 | O | O | O |
| 3 | commit_apply.go | ~120 | O | O | O |
| 4 | analyzer.go | ~150 | O | O | 읽기 |
| 5 | merge_strategy.go | ~200 | O | O | O |
| 5 | squash_strategy.go | ~180 | O | O | O |
| 5 | rebase_strategy.go | ~250 | O | O | O |
| 6 | executor.go | ~120 | O | O | O |
| 7 | handler_helpers.go | ~50 | gin | X | X |

**핵심 성과**:
- Layer 0-1: **650줄 완전 순수** (go-git 모름, ctx 모름)
- ResolutionPlan에 결과 없음 (테스트 깔끔)
- typed error로 API 메시지 안정성 확보
- 점진적 PR로 회귀 최소화

---

## 9. 회귀 방지 체크리스트

### 에러 코드 매핑

| 상황 | Status | Code |
|------|--------|------|
| Conflict 발생 | 409 | CONFLICT |
| Ref 없음 | 404 | NOT_FOUND |
| Validation 실패 | 400 | INVALID |
| 동시 접근 | 423 | LOCKED |

### 커밋 메타데이터

- author nil → default author 사용
- committer nil → author 복사
- date zero → now 사용

### Tree 병합 결정론

- 같은 입력 → 같은 ResolutionPlan
- 같은 plan → 같은 tree hash
- Now 고정 → 같은 commit hash

---

## 10. 리팩토링 완료 현황 (2026-02-04 최종)

### 최종 파일 구조 (prefix 규칙 적용)

```
internal/merge/
├── # Layer 0: Pure Types (types_*)
├── types_errors.go        # sentinel + typed error + APIError + ConcurrentUpdateError
├── types_snapshot.go      # TreeSnapshot, FileSnapshot (자체 DTO)
├── types_plan.go          # ResolutionPlan, PlannedEntry, ApplyResult
│
├── # Layer 0-1: Pure Functions (pure_*)
├── pure_signature.go      # CreateSignature, CreateAuthorSignature 등
├── pure_message.go        # GenerateMergeCommitMsg, GetMergeTypeString 등
├── pure_tree_plan.go      # PlanTreeMerge (순수 함수)
│
├── # Layer 2: Adapters (adapter_*)
├── adapter_snapshot.go    # TreeToSnapshot, CommitToSnapshots
├── adapter_env.go         # MergeEnv, NewMergeEnvWithTime
│
├── # Layer 3: Apply (apply_*)
├── apply_tree.go          # createMergedTree, resolveFileForMerge 등
│
├── # Layer 4: Analysis
├── analysis.go            # analyzeMerge, analyzeRebaseDryRun
├── conflict_detector.go   # 기존 유지
├── preview.go             # 기존 유지
│
├── # Layer 5: Strategies (strategy_*)
├── strategy_merge.go      # performThreeWayMerge + resolveMergeTree
├── strategy_squash.go     # performSquashMerge
├── strategy_rebase.go     # performRebase
│
├── # Layer 6: Orchestration
├── executor.go            # performMerge (switch 분기)
│
├── # Layer 7: HTTP
├── handler.go             # 기존 유지
├── handler_helpers.go     # writeError (gin 변환 유일 지점)
│
├── # 기존 유지
├── schema.go
└── validator.go
```

### 새로 작성된 테스트 파일

| 테스트 파일 | 테스트 수 | 커버리지 대상 |
|-------------|----------|---------------|
| `types_errors_test.go` | 20개 | 모든 typed error, ToAPIError, WrapMergeError |
| `types_snapshot_test.go` | 8개 | TreeSnapshot, FileSnapshot 메서드 |
| `types_plan_test.go` | 14개 | ResolutionPlan, PlannedEntry, ApplyResult |
| `pure_message_test.go` | 13개 | 메시지 생성, ref 정규화 |
| `pure_signature_test.go` | 11개 | author/committer 생성, fallback 로직 |
| `pure_tree_plan_test.go` | 17개 | PlanTreeMerge, determinism, 전략별 분기 |
| `adapter_snapshot_test.go` | 5개 | TreeToSnapshot, CommitToSnapshots |
| `adapter_env_test.go` | 6개 | MergeEnv, IsCancelled |
| **총계** | **94개** | - |

### 달성 목표

| 목표 | 상태 | 비고 |
|------|------|------|
| operation.go 삭제 | ✅ 완료 | 1617줄 → 0줄 |
| SRP 준수 | ✅ 완료 | 책임별 파일 분리 |
| 순수 레이어 분리 | ✅ 완료 | `types_*`, `pure_*` prefix |
| go-git 격리 | ✅ 완료 | `types_snapshot.go` (ctx/go-git 모름) |
| 프레임워크 격리 | ✅ 완료 | `types_errors.go`에 gin.H 없음 |
| typed error 시스템 | ✅ 완료 | 6종 + ConcurrentUpdateError 추가 |
| 테스트 작성 | ✅ 완료 | 94개 신규 테스트 |
| make test 통과 | ✅ 완료 | - |

---

## 11. Technical Debt (해결됨)

### ✅ author/committer 중복 (DRY 위반 해결)

원래 `operation.go`에 4군데 있던 하드코딩:
```go
author := &object.Signature{
    Name:  "Git Executor",
    Email: "git@executor.com",
    When:  now,
}
```

**해결 후 상태**:

| 파일 | Before | After |
|------|--------|-------|
| `pure_signature.go` | ✅ 중앙화 | ✅ **유일한 fallback 위치** |
| `strategy_merge.go` | ❌ 하드코딩 | ✅ `CreateAuthorSignature()` 사용 |
| `strategy_squash.go` | ❌ 하드코딩 | ✅ `CreateAuthorSignature()` 사용 |

**변경 내용**:
```go
// Before (strategy_merge.go, strategy_squash.go 각각 30줄)
now := time.Now()
author := &object.Signature{
    Name:  "Git Executor",
    Email: "git@executor.com",
    When:  now,
}
committer := author
if req.Author != nil { ... }
if req.Committer != nil { ... }

// After (각각 3줄)
now := time.Now()
author := CreateAuthorSignature(req, nil, now)  // or &sourceCommit.Author for squash
committer := CreateCommitterSignature(req, &author, now)
```

**효과**:
- 코드 중복: 4곳 → **1곳**
- 총 삭제 줄 수: ~60줄
- fallback 로직 변경 시 수정 지점: **1곳만**

### 🟡 conflict_plan.go 미구현

- 설계 문서에는 Layer 1에 `conflict_plan.go` 계획됨
- 현재 conflict 로직은 `strategy_merge.go`의 `resolveMergeTree`에 혼재
- 순수 레이어로 분리하면 테스트 용이성 향상

### 🟡 commit_apply.go 미구현

- commit 생성 + ref 업데이트 로직이 `strategy_*.go`에 분산됨
- 별도 파일로 분리하면 atomicity 테스트 용이

---

## 12. 파일별 의존성 매트릭스 (최종)

| 파일 | ctx | go-git | 부작용 | 테스트 |
|------|-----|--------|--------|--------|
| `types_errors.go` | ❌ | ❌ | ❌ | ✅ 20개 |
| `types_snapshot.go` | ❌ | ❌ | ❌ | ✅ 8개 |
| `types_plan.go` | ❌ | ❌ | ❌ | ✅ 14개 |
| `pure_signature.go` | ❌ | ⚠️ object.Signature | ❌ | ✅ 11개 |
| `pure_message.go` | ❌ | ❌ | ❌ | ✅ 13개 |
| `pure_tree_plan.go` | ❌ | ❌ | ❌ | ✅ 17개 |
| `adapter_snapshot.go` | ❌ | ✅ | ❌ | ✅ 5개 |
| `adapter_env.go` | ✅ | ✅ | ❌ | ✅ 6개 |
| `apply_tree.go` | ✅ | ✅ | ✅ | 기존 |
| `strategy_*.go` | ✅ | ✅ | ✅ | 기존 |

**범례**:
- `⚠️ object.Signature`: go-git 타입 사용하지만 순수 함수 (부작용 없음)
- 순수 레이어: `types_*`, `pure_*` (ctx 모름, 부작용 없음)

---

## 13. 순수 레이어 검증

### Layer 0 (types_*)

```
types_errors.go      → import: errors, fmt, net/http (표준 라이브러리만)
types_snapshot.go    → import: 없음
types_plan.go        → import: 없음 (merge 패키지 내부 타입만)
```

### Layer 0-1 (pure_*)

```
pure_message.go      → import: fmt, strings (표준만)
pure_signature.go    → import: time, go-git/object (Signature 타입만, 메서드 호출 없음)
pure_tree_plan.go    → import: 없음 (merge 패키지 내부 타입만)
```

**핵심 검증**:
- `context.Context` 사용 없음 ✅
- `*gogit.Repository` 접근 없음 ✅
- 부작용(저장, 네트워크, 파일) 없음 ✅
- 같은 입력 → 같은 출력 (deterministic) ✅

---

## 14. 에러 시스템 특징

### Sentinel Errors (카테고리)

```go
ErrConflict      = errors.New("merge conflict")
ErrNotFound      = errors.New("not found")
ErrLocked        = errors.New("resource locked")
ErrInvalidInput  = errors.New("invalid input")
ErrInternalError = errors.New("internal error")
```

### Typed Errors (디테일)

| 타입 | Unwrap | HTTP Status |
|------|--------|-------------|
| `NotFoundError` | `ErrNotFound` | 404 |
| `ConflictError` | `ErrConflict` | 409 |
| `ValidationError` | `ErrInvalidInput` | 400 |
| `LockedError` | `ErrLocked` | 423 |
| `InternalError` | wrappedErr 또는 `ErrInternalError` | 500 |
| `ConcurrentUpdateError` | `ErrConflict` | 409 |

### 프레임워크 격리

- `ToAPIError()`: `errors.As`/`Is` 기반 매핑 → `APIError` 반환
- `APIError`: `gin.H` 없음, 순수 struct
- gin 변환: `handler_helpers.go`의 `writeError()`에서만 수행
