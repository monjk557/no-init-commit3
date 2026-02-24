# .env 파일 삭제 원인 분석 보고서

> 분석 대상 브랜치: `main`
> 분석 날짜: 2026-02-24
> 분석 파일: `internal/merge/` 전체

---

## 1. .gitignore 파싱 문제 여부: **해당 없음**

merge 코드는 `.gitignore`를 전혀 파싱하지 않는다.
`apply_tree.go`의 `mergeTreeEntries` → `resolveFileForMerge` 전 경로가 순수하게 git tree object (blob/tree hash) 만 비교한다.
처음 의심했던 gitignore 파싱 문제는 원인이 아니다.

---

## 2. 발견된 버그 목록 (가장 유력한 원인 순)

### Bug #1: Conflict Detector가 "정상 변경"을 전부 Conflict로 과잉 탐지

**파일**: `internal/merge/conflict_detector.go` → `analyzeFileConflict` (line 284)

```go
// 세 버전이 완전히 동일할 때만 nil 반환 (no conflict)
if baseExists && sourceExists && targetExists &&
    base.Hash == source.Hash && base.Hash == target.Hash {
    return nil, nil  // ← 이 조건만 nil
}
// 그 외 모든 경우(한 쪽만 변경 포함) → conflict로 처리
```

"target에서만 변경" / "source에서만 변경" 같은 순수한 clean merge 케이스도
conflict로 분류된다. 결과적으로 일상적인 merge에서도 `HasConflicts=true`가 되어
`force_conflict=true` 없이는 409로 실패한다.

**실무 영향**: 사용자는 거의 모든 merge에서 `force_conflict=true`를 써야 하며,
이것이 Bug #2를 활성화한다.

---

### Bug #2: Recursive strategy가 충돌 시 항상 SOURCE(feature branch)를 우선

**파일**: `internal/merge/apply_tree.go` → `resolveFileForMerge` (line 202~214)

```go
// Case 6: 두 브랜치 모두 변경 (진짜 conflict)
switch strategy {
case MergeStrategyRecursive, MergeStrategyResolve, MergeStrategyORT:
    // SOURCE(feature branch) 버전 사용 ← 문제
    return entry.sourceHash, entry.sourceMode, nil
```

**파일**: `internal/merge/pure_tree_plan.go` → `resolveConflictSource`

```go
case MergeStrategyRecursive, MergeStrategyResolve, MergeStrategyORT:
    return EntrySourceSource  // ← 항상 SOURCE 선택
```

**시나리오**: staging(target)과 feature branch(source) 양쪽에서 `.env`가 수정된 경우,
`force_conflict=true`로 merge하면 feature branch의 `.env`가 staging의 `.env`를 덮어쓴다.
Feature branch에 `.env`가 없거나 빈 값이면 staging `.env` 내용이 사라진다.

---

### Bug #3: determineConflictType의 delete 분류 누락

**파일**: `internal/merge/conflict_detector.go` → `determineConflictType` (line 364~407)

```go
if baseExists && !sourceExists && targetExists {
    if base.Hash != target.Hash {
        return ConflictTypeDelete  // 변경+삭제 → 올바름
    }
    // base.Hash == target.Hash (target 미변경) → 아무것도 return 안 함!
    // → 아래로 fall-through해서 ConflictTypeContent로 분류됨 ← BUG
}
```

"source에서 삭제됐지만 target은 미변경" 케이스가
`ConflictTypeDelete` 대신 `ConflictTypeContent`로 잘못 분류된다.

**영향**:
- Severity: High 대신 Medium으로 잘못 판단
- AutoResolvable: false 대신 true로 잘못 판단
- 실제로는 수동 확인이 필요한 케이스인데 자동 해소로 처리됨

---

### Bug #4: Squash merge + conflicts + !force_conflict → SOURCE tree 전체 반환

**파일**: `internal/merge/strategy_merge.go` → `resolveMergeTree` (line 134~138)

```go
if analysis.HasConflicts && !forceConflict {
    if isSquash {
        return sourceCommit.TreeHash, nil, nil  // ← source 전체 tree 반환!
    }
    return targetCommit.TreeHash, nil, nil
}
```

handler에서 이미 `force_conflict=false`면 409로 차단하지만,
Bug #1의 과잉 탐지와 결합 시 의도치 않은 경로로
source tree 전체가 target에 덮어씌워질 수 있다.

---

### Bug #5: resolveFileForMerge의 Case 7, 8 Dead Code

**파일**: `internal/merge/apply_tree.go` (line 217~238)

```go
// Case 7: base && source && !target (← Case 5와 완전히 동일 조건, 도달 불가)
if baseExists && sourceExists && !targetExists { ... }

// Case 8: base && !source && target (← Case 4와 완전히 동일 조건, 도달 불가)
if baseExists && !sourceExists && targetExists { ... }
```

Case 4, 5가 먼저 동일 조건을 처리하므로 7, 8은 영원히 실행되지 않는다.
코드 진화 과정에서 생긴 불일치이며 논리적 혼란을 유발한다.

---

## 3. .env 삭제 발생 경로 요약

```
시나리오 A (가장 유력):
  staging(.env 있음) + feature(.env도 수정됨) → force_conflict=true → recursive strategy
  → 두 브랜치 모두 .env 변경 → Bug #2 → feature branch .env로 덮어씌움
  → staging의 .env 내용 손실

시나리오 B:
  Bug #1 → HasConflicts 과잉 탐지 → 사용자가 force_conflict=true 강제 사용
  → 실제로는 충돌 없는 clean merge인데 충돌 해소 경로로 진입
  → Bug #2와 결합 시 source 버전으로 대체

시나리오 C:
  .env가 git에 untracked (gitignored) 상태
  → merge가 Redis event 발행 → 배포 파이프라인 trigger
  → git clean -fd 또는 git reset --hard 실행
  → gitignore된 .env 파일 working directory에서 물리 삭제
```

---

## 4. 즉시 확인이 필요한 사항

```bash
# 1. feature branch의 git tree에 .env가 포함되어 있는가?
git ls-tree <feature-branch-commit> .env

# 2. merge된 commit의 tree에 .env가 있는가?
git show <merge-commit>:.env

# 3. merge base(공통 조상)에 .env가 있는가?
git merge-base <source-branch> <target-branch> | xargs git ls-tree -- .env
```

**API 호출 파라미터 확인**:
- `force_conflict` 값이 무엇인가?
- `strategy` 값이 무엇인가? (`recursive`가 default)
- `method` 값이 무엇인가? (`merge` / `squash` / `rebase`)

**배포 파이프라인 확인**:
- Redis event 수신 후 `git clean`, `git reset --hard` 실행 여부

---

## 5. 결론

가장 유력한 원인은 **Bug #2** (Recursive strategy가 content conflict에서 항상 source 우선)와
**Bug #1** (정상 변경도 conflict로 탐지하여 `force_conflict=true` 강제)의 결합이다.

.gitignore 파싱은 원인이 아니며, merge 로직은 git tree object 레벨에서만 동작한다.

staging 환경의 `.env`가 보존되려면:
1. target(staging) 브랜치에 고유한 파일은 merge strategy에서 항상 target을 우선해야 한다.
2. 또는 `.env`를 git에서 완전히 untrack하고 배포 파이프라인에서 별도 관리해야 한다.
