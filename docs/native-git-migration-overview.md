# Native Git Migration: `/overview`, `/overview/tree`, `/overview/blob` API

## Summary

`/overview`, `/overview/tree`, `/overview/blob` 3개 핸들러를 go-git에서 native git CLI로 전환.
`/overview` **4.0초 → 0.6초 (6.5배)**, `/overview/blob` **4.0초 → 0.5초 (8배)**, `/overview/tree` **0.75초 → 0.45초 (1.7배)**.

## Problem

### go-git `OpenRepositoryNoCache` 반복 호출 병목

`runOverviewTasks`에서 6개 goroutine이 **각각 독립적으로** `repositories.OpenRepositoryNoCache(repoPath)`를 호출:

```
goroutine 1: OpenRepositoryNoCache → commits.GetCommits        (latest commit)
goroutine 2: OpenRepositoryNoCache → commits.CountCommitsWithCap (commit count)
goroutine 3: OpenRepositoryNoCache → service.GetTreeOnly         (tree entries)
goroutine 4: OpenRepositoryNoCache → tags.GetTags                (tags)
goroutine 5: (shared instance)     → service.GetRemotes          (remotes)
goroutine 6: OpenRepositoryNoCache → blobService.GetBlobContent  (README)
```

go-git의 `ObjectStorage`가 goroutine-safe하지 않기 때문에 shared instance를 쓸 수 없고, goroutine마다 별도 repo를 열어야 했음.

| 항목 | 값 |
|---|---|
| 테스트 레포 | 2,273 files |
| OpenRepositoryNoCache 호출 수 | 요청당 **5회** |
| 호출당 오버헤드 | **~500-800ms** (packfile index 로드 + object store 초기화) |
| 총 오버헤드 | **~3-4초** (실제 작업 시간은 수 ms) |

### 왜 느린가

1. **Packfile index 반복 로드**: 매번 `.idx` 파일을 파싱하여 Go 메모리에 인덱스 구축
2. **Object store 초기화**: go-git의 `filesystem.ObjectStorage`가 매번 파일 핸들 + 내부 캐시 재구축
3. **Go 역직렬화 비용**: Git object를 Go struct로 변환하는 오버헤드
4. **캐시 비활성화**: 동시 접근 panic 방지를 위해 `NoCache`를 사용, 매 호출이 cold start

## Solution: Native Git CLI

### 핵심 아이디어

Native git exec은 **repo open 자체가 불필요**. `--git-dir` 플래그만 전달하면 git이 내부적으로 packfile을 mmap하고 최적화된 C 코드로 처리.

### Before vs After

```
[Before: go-git]
요청 → 6 goroutines → 각각 OpenRepositoryNoCache (~600ms) → 실제 작업 (~5ms) → 완료
       wall time ≈ max(600ms + 5ms) × packfile contention ≈ 3-5초

[After: native git]
요청 → 6 goroutines → 각각 git exec (~5-50ms, repo open 불필요) → 완료
       wall time ≈ max(5-50ms) ≈ 50-600ms
```

## Architecture

### 신규 Native Git 함수 (`native_git.go`)

| 함수 | Git 명령어 | 대체 대상 |
|---|---|---|
| `NativeGetLatestCommit` | `git log -1 --format=<fmt> <ref>` | `commits.GetCommits(rw, ctx, ref, 1, 0)` |
| `NativeCountCommits` | `git rev-list --count <ref>` | `commits.CountCommitsWithCap(rw, ctx, ref, cap)` |
| `NativeGetTags` | `git tag -l --sort=-creatordate --format=<fmt>` | `tags.GetTags(rw, 100, 0)` |
| `NativeGetRemotes` | `git remote -v` | `service.GetRemotes(ctx, gitRepo)` |
| `NativeGetBlobContent` | `git show <ref>:<path>` | `blobService.GetBlobContent(...)` |
| `NativeGetLatestCommitForPath` | `git log -1 --format=<fmt> <ref> -- <path>` | `service.GetLatestCommitForPath(...)` |
| `NativeGetCommits` | `git log -N --format=<fmt> <ref>` | `commits.GetCommits(rw, ctx, ref, N, 0)` |

기존 함수 (`NativeGetTreeEntries`, `GetLatestCommitsNative`)와 동일한 패턴:
- `--git-dir` bare repo 경로 직접 전달
- `context.WithTimeout`으로 개별 명령어 타임아웃
- null-byte 구분자 포맷으로 안전한 파싱

### 핸들러 변경

#### `/overview` — `HandleGetRepositoryOverview`

```go
// Before: 6 goroutines, 각각 OpenRepositoryNoCache
func runOverviewTasks(ctx, repoCtx, owner, repo, path, refHash, repoPath) overviewResult

// After: 6 goroutines, native git exec (repo open 불필요)
func runOverviewTasksNative(ctx, repoPath, ref, path, refHash) overviewResult
```

- `overviewResult.latestCommit` (`commits.Commit`) → `overviewResult.nativeLatestCommit` (`*GitCommit`)
- `overviewResult.tagsList` (`[]tags.Tag`) → `overviewResult.nativeTagsList` (`[]GitTag`)
- `convertCommitToGitCommit`, `convertTagsToGitTags` 변환 레이어 제거
- `nativeResolveRef`로 refHash resolve (go-git `ResolveReference` 대체)

#### `/overview/tree` — `HandleGetTreeOverview`

- `service.GetTreeOnly` → `NativeGetTreeEntries`
- `service.GetLatestCommitForPath` → `NativeGetLatestCommitForPath`
- `commits.GetCommits` → `NativeGetCommits`
- `[]commits.Commit` → `[]GitCommit` 직접 사용

#### `/overview/blob` — `HandleGetBlobOverview`

- `blobService.GetBlobContent` → `NativeGetBlobContent`
- `service.GetTreeOnly` → `NativeGetTreeEntries`
- `service.GetLatestCommitForPath` → `NativeGetLatestCommitForPath`
- `commits.GetCommits` → `NativeGetCommits`

### 유지된 것

- `validateAndOpenRepository` — RLock 관리 + 브랜치 목록 조회 (다른 핸들러와 공유)
- `overviewCache` — 커밋 카운트 캐싱 (native에서도 동일하게 활용)
- retry 로직 — push 직후 pack visibility 이슈 대응
- `HandleGetBranchesOverview` — go-git 유지 (ahead/behind 계산에 필요)

### 제거된 코드

| 파일 | 제거 항목 | 이유 |
|---|---|---|
| `schema.go` | `convertCommitToGitCommit` | native가 `*GitCommit` 직접 반환 |
| `schema.go` | `convertAuthorToGitAuthor` | 위 함수에서만 사용 |
| `schema.go` | `convertTagsToGitTags` | native가 `[]GitTag` 직접 반환 |
| `schema.go` | `convertCommitsToGitCommits` | native가 `[]GitCommit` 직접 반환 |
| `schema.go` | `commits`, `tags` import | 위 함수들 제거로 불필요 |
| `handler.go` | `commits` import | native 전환으로 불필요 |

## Benchmark Results

### 환경

- **서버**: Docker container (hot reload 빌드)
- **테스트 레포**: `my2/a731a4b2-...` (2,273 files)
- **방법**: 브랜치 전환 → hot reload 대기 → health check 확인 → 5회 반복 측정
- **날짜**: 2026-02-26

### `/overview`

| Run | main (go-git) | native git |
|-----|---------------|------------|
| 1 | 4.518s | 0.837s |
| 2 | 3.151s | 0.549s |
| 3 | 3.158s | 0.520s |
| 4 | 4.168s | 0.529s |
| 5 | 5.028s | 0.642s |
| **평균** | **4.005s** | **0.615s** |
| **개선** | | **6.5x** |

### `/overview/tree`

| Run | main (go-git) | native git |
|-----|---------------|------------|
| 1 | 0.862s | 0.512s |
| 2 | 0.890s | 0.703s |
| 3 | 0.681s | 0.356s |
| 4 | 0.657s | 0.358s |
| 5 | 0.645s | 0.333s |
| **평균** | **0.747s** | **0.452s** |
| **개선** | | **1.7x** |

### `/overview/blob?path=README.md`

| Run | main (go-git) | native git |
|-----|---------------|------------|
| 1 | 3.462s | 0.531s |
| 2 | 3.685s | 0.489s |
| 3 | 3.463s | 0.467s |
| 4 | 3.419s | 0.458s |
| 5 | 6.066s | 0.525s |
| **평균** | **4.019s** | **0.494s** |
| **개선** | | **8.1x** |

### 성능 개선 요약

| 엔드포인트 | Before (go-git) | After (native) | 개선 |
|---|---|---|---|
| `/overview` | **4.0초** | **0.6초** | **6.5x** |
| `/overview/blob` | **4.0초** | **0.5초** | **8.1x** |
| `/overview/tree` | **0.75초** | **0.45초** | **1.7x** |

## 수정 파일

| 파일 | 변경 | +/- |
|---|---|---|
| `internal/overview/native_git.go` | 7개 native git 함수 추가 | +217 |
| `internal/overview/handler.go` | 3개 핸들러 native 전환, `runOverviewTasks` 교체 | +111 / -215 |
| `internal/overview/schema.go` | 불필요한 변환 함수 4개 + import 제거 | +3 / -46 |

## Verification

```bash
make format              # ✅ 포맷 정상
go vet ./internal/overview/...  # ✅ vet 에러 없음
go build ./internal/... ./cmd/... # ✅ 빌드 성공
go test ./internal/overview/... -run "Test_.*Overview" # ✅ 전체 통과 (6.9s)
```

## 전체 Native Git 전환 현황

| 엔드포인트 | 상태 | 이전 | 이후 | 개선 |
|---|---|---|---|---|
| `/overview/tree/commits` | ✅ 완료 (이전 PR) | 9,000ms | 6ms | 1,500x |
| `/overview` | ✅ 완료 (이번 PR) | 4,000ms | 600ms | 6.5x |
| `/overview/blob` | ✅ 완료 (이번 PR) | 4,000ms | 500ms | 8.1x |
| `/overview/tree` | ✅ 완료 (이번 PR) | 750ms | 450ms | 1.7x |
| `/overview/branches` | 미전환 | go-git | go-git | — |
| `/overview/latest-commits` (batch) | 미전환 | go-git | go-git | — |
