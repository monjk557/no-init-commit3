# Commit Pipeline Optimization: git fast-import

## Overview

`POST /repos/{owner}/{repo}/commits` (`HandleMultiFileCommit`) 커밋 생성 파이프라인을
**다중 프로세스 순차 실행 → 단일 `git fast-import` 프로세스**로 전환하여 성능을 근본적으로 개선.

## Before: Multi-Process Pipeline

기존 파이프라인은 파일 N개, 디렉토리 D개 커밋 시 **7 + 3N + D개의 git 프로세스**를 순차 생성:

```
┌─ Phase 1: Branch Resolution ─────────────────────────┐
│  git symbolic-ref                              (1)    │
│  git rev-parse --verify refs/heads/<branch>    (1)    │
│  git rev-parse --verify <hash>^{tree}          (1)    │
└───────────────────────────────────────────────────────┘
         │
┌─ Phase 2: File Validation (PER-FILE) ────────────────┐
│  git ls-tree <tree> -- <path>  × N             (N)   │  ← service.go fileExists
│  git rev-parse --verify <hash>^{tree}          (1)   │  ← BeginTransaction (중복!)
│  git ls-tree <tree> -- <path>  × N             (N)   │  ← ValidateTransaction (중복!)
└───────────────────────────────────────────────────────┘
         │
┌─ Phase 3: Tree Construction (PER-FILE/DIR) ──────────┐
│  git ls-tree -r <tree>                         (1)   │  ← 전체 트리 로드
│  git hash-object -w --stdin  × N               (N)   │  ← 파일별 blob 생성
│  git mktree                  × D               (D)   │  ← 디렉토리별 tree 생성
│  git commit-tree                               (1)   │
└───────────────────────────────────────────────────────┘
         │
┌─ Phase 4: Finalization ──────────────────────────────┐
│  git update-ref                                (1)   │
│  git log (event publishing)                    (1)   │
└───────────────────────────────────────────────────────┘

Total: 7 + 3N + D git processes
```

### 실제 프로세스 수 예시 (Before)

| 시나리오 | 프로세스 수 | ~예상 소요시간 |
|----------|-----------|--------------|
| 파일 1개, 디렉토리 1개 | 11 | 200-500ms |
| 파일 5개, 디렉토리 3개 | 25 | 500ms-1s |
| 파일 20개, 디렉토리 8개 | 75 | 1.5-3s |
| 파일 50개, 디렉토리 15개 | 172 | 3-8s |

## After: Single fast-import Process

최적화된 파이프라인은 파일 수와 무관하게 **고정 ~5개의 git 프로세스**만 사용:

```
┌─ Phase 1: Branch Resolution ─────────────────────────┐
│  git symbolic-ref                              (1)    │
│  git rev-parse --verify refs/heads/<branch>    (1)    │
│  git rev-parse --verify <hash>^{tree}          (1)    │  ← 1회만 (중복 제거)
└───────────────────────────────────────────────────────┘
         │
┌─ Phase 2: Unified Validation ────────────────────────┐
│  git ls-tree -r <tree>                         (1)    │  ← 1회로 모든 파일 검증
└───────────────────────────────────────────────────────┘
         │
┌─ Phase 3: fast-import (SINGLE PROCESS) ──────────────┐
│  git fast-import --quiet --done                (1)    │
│  ├─ blob + mark (:1, :2, ...) × N                    │
│  ├─ commit refs/heads/<branch>                        │
│  │   ├─ author/committer                              │
│  │   ├─ data (commit message)                         │
│  │   └─ from <parent>                                 │
│  ├─ M <mode> :<mark> <path>  (creates/updates)       │
│  ├─ D <path>                 (deletes)                │
│  ├─ R <old> <new>            (moves)                  │
│  └─ done                                              │
└───────────────────────────────────────────────────────┘
         │
┌─ Phase 4: Post-commit ───────────────────────────────┐
│  git rev-parse (verify commit)                 (1)    │
│  git log (event publishing)                    (1)    │
└───────────────────────────────────────────────────────┘

Total: ~5-7 git processes (CONSTANT)
```

### 실제 프로세스 수 예시 (After)

| 시나리오 | 프로세스 수 | ~예상 소요시간 | 개선율 |
|----------|-----------|--------------|--------|
| 파일 1개 | 5-7 | 50-100ms | **2-5x** |
| 파일 5개 | 5-7 | 50-100ms | **5-10x** |
| 파일 20개 | 5-7 | 60-120ms | **15-25x** |
| 파일 50개 | 5-7 | 80-150ms | **30-50x** |

## Key Architectural Changes

### 1. Triple Validation → Single Validation

**Before**: 3단계 중복 검증
```
service.go:fileExists()     → nativeLsTreePath × N  (per-file subprocess)
transaction.go:Validate()   → nativeLsTreePath × N  (per-file subprocess, 중복)
transaction.go:createTree() → nativeLsTree -r        (전체 트리 로드, 또 중복)
```

**After**: 단일 검증
```
fast_import.go:PreValidateChanges() → nativeLsTree -r 1회 → in-memory map lookup
```

### 2. Per-file Process Spawning → Streaming Protocol

**Before**: 파일마다 별도 프로세스
```go
// N개의 프로세스
for _, change := range changes {
    hash, _ := nativeHashObject(ctx, repoPath, change.Content)  // git hash-object -w
}
// D개의 프로세스
treeHash, _ := buildTreeRecursive(ctx, repoPath, files, "")    // git mktree × D
// 1개 프로세스
commitHash, _ := nativeCommitTree(ctx, repoPath, treeHash, ...)  // git commit-tree
// 1개 프로세스
nativeUpdateRef(ctx, repoPath, refName, commitHash)               // git update-ref
```

**After**: 단일 프로세스 내 스트리밍
```go
// 1개의 프로세스가 모든 것을 처리
cmd := exec.CommandContext(ctx, "git", "fast-import", "--quiet", "--done")
stdin.Write("blob\nmark :1\ndata 42\n...")   // blob 생성
stdin.Write("commit refs/heads/main\n...")    // commit + tree + ref 동시 처리
stdin.Write("M 100644 :1 path/to/file\n")    // tree entry
stdin.Write("done\n")
```

### 3. TransactionManager Bypass

기존 `TransactionManager`는 다음과 같은 overhead를 추가:
- `BeginTransaction`: nativeGetTreeHash 중복 호출
- `AddOperation`: 단순 리스트 append (불필요한 추상화)
- `ValidateTransaction`: nativeLsTreePath × N 중복 호출
- `CommitTransaction`: createTreeFromChanges (ls-tree -r + hash-object × N + mktree × D)

fast-import 경로에서는 이 전체를 bypass하고, `PreValidateChanges` + `ExecuteFastImportCommit`으로 대체.

## git fast-import Protocol Reference

```
# Blob creation (repeated for each file)
blob
mark :<n>
data <byte-count>
<raw-content>

# Commit creation (once per commit)
commit refs/heads/<branch>
author <name> <<email>> <unix-timestamp> <timezone>
committer <name> <<email>> <unix-timestamp> <timezone>
data <byte-count>
<commit-message>
from <parent-hash>

# File operations (within commit)
M <mode> :<mark> <path>     # create/update file
D <path>                     # delete file
R <old-path> <new-path>      # rename/move file

# Stream termination
done
```

## Files Changed

| File | Change |
|------|--------|
| `internal/commits/fast_import.go` | **NEW** — fast-import 실행, 스트림 생성, 단일 검증 |
| `internal/commits/service.go` | `ExecuteMultiFileCommit` 최적화 — TransactionManager bypass, fast-import 사용 |
| `internal/commits/transaction.go` | 변경 없음 (하위 호환성 유지, dry-run 등에서 사용 가능) |
| `internal/commits/native_git.go` | 변경 없음 (개별 git 명령 래퍼 유지) |

## Backward Compatibility

- **API 응답 형식**: 완전 동일 (`MultiFileCommitResponse`)
- **Dry-run/validate-only 모드**: `PreValidateChanges`로 검증 후 조기 반환
- **TransactionManager**: 코드 유지 (다른 기능에서 사용 가능)
- **Event publishing**: 동일한 `addCommitEventToStream` 사용
- **Cache invalidation**: `repositories.GetSimpleCache().Invalidate()` 추가

## Monitoring

최적화 전후 비교를 위한 로그 필드:

```
commit: fast-import start   → fast-import 프로세스 시작
commit: fast-import done    → fast_import_dur: fast-import 전체 소요시간
commit: validated           → validate_dur: 검증 소요시간
commit: done                → total: 전체 소요시간
```

기존 로그 (`commit_txn_dur`, `txn_begin_dur`) 대신 `fast_import_dur`, `validate_dur`로 대체되어
실제 병목 지점을 더 정확하게 파악 가능.
