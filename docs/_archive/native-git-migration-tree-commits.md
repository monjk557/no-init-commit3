# Native Git Migration: `/overview/tree/commits` API

## Summary

`/overview/tree/commits` API의 latestCommit 조회를 go-git에서 native git CLI로 전환.
9,000ms → 6ms (1,500배 성능 개선).

## Problem

### go-git의 `object.DiffTree` 병목

`GetLatestCommitsForPathsWithMeta` (service.go:648)에서 매 커밋마다 `object.DiffTree(parentTree, currentTree)`를 호출:

```
for each commit in history:
    parentTree = parent.Tree()       // 전체 트리 오브젝트 로드 (Go 역직렬화)
    currentTree = commit.Tree()      // 전체 트리 오브젝트 로드 (Go 역직렬화)
    changes = DiffTree(parent, cur)  // Go로 재귀 비교 (O(N) where N = total files)
    for each change:
        if change matches pending path → record
```

| 항목 | 값 |
|---|---|
| 테스트 레포 | 973 files, 340 commits |
| go-git 소요시간 | **~9,000ms** |
| 5초 time budget 내 스캔 가능 커밋 | **3개** (커밋 당 ~1.7초) |
| 전체 커밋의 1% 미만 스캔 → 23개 경로 중 5개만 resolve |

### 왜 느린가

1. **Pure Go 구현**: C로 작성된 native git 대비 트리 비교 연산이 수십 배 느림
2. **전체 트리 비교**: 경로 필터링 없이 매 커밋마다 전체 트리를 비교 (973개 파일 전부)
3. **Go 오브젝트 역직렬화**: 매 커밋마다 2개의 트리 오브젝트를 Go 메모리로 로드
4. **최적화 부재**: Native git의 bloom filter, commit-graph, path-limited traversal 미사용

## Solution: Native Git CLI

### Benchmark

```bash
# Native git: 23개 경로 전부 조회
$ cd /var/lib/git/repos/my2/67677cc5-8021-4c7b-90ea-b30167d3fc3d.git
$ time (git ls-tree --name-only HEAD | while read entry; do
    git log -1 --format="%H|%aI|%aN|%aE|%s" -- "$entry"
  done > /dev/null)

Total: 6ms    # go-git: 9,000ms → native: 6ms (1,500x)
```

### 왜 빠른가

1. **C 최적화**: 트리 비교가 C로 구현, SIMD 가속 가능
2. **Path-limited traversal**: `git log -- <path>`는 해당 경로의 서브트리만 비교
3. **Bloom filter (commit-graph v2)**: 커밋이 특정 경로를 수정했는지 O(1)로 판별
4. **mmap packfile**: 파일 I/O 최적화
5. **경로별 독립 조회**: 23개 경로를 8개 goroutine으로 병렬 실행 가능

## Architecture

### Before (go-git)
```
Handler
  → commitIndex.Lookup() (캐시 히트 시도)
  → service.GetTreeWithCommit()
    → getTreeEntries(withCommits=true)
      → repo.Log() + DiffTree per commit  ← 병목 (9초)
  → commitIndex.MergeStore()
  → commitIndex.ForceScheduleBackgroundPopulation()
  → Response
```

### After (native git)
```
Handler
  → NativeGetTreeEntries()              ← git ls-tree -l (< 1ms)
  → GetLatestCommitsNative()            ← git log -1 per path × 8 goroutines (< 10ms)
  → Response
```

commitIndex, 백그라운드 population, hotness tracking 등 모두 불필요 — native git이 충분히 빠르기 때문.

## Changed Files

| File | Change |
|---|---|
| `internal/overview/native_git.go` | **신규**. `GetLatestCommitsNative()`, `NativeGetTreeEntries()` 구현 |
| `internal/overview/handler.go` | `HandleGetTreeWithCommitOverview` 전면 재작성: go-git → native git |
| `internal/overview/commit_index.go` | 유지 (다른 API에서 사용 가능) |
| `internal/overview/service.go` | 유지 (다른 API에서 사용 가능) |

## API Response

변경 전후 동일한 스키마:

```json
{
  "success": true,
  "repository": "owner/repo",
  "tree": [
    {
      "type": "tree",
      "name": "app",
      "path": "app",
      "mode": "040000",
      "hash": "abc123...",
      "latest_commit": {
        "sha": "f2e9f155...",
        "message": "feat: add new feature",
        "author": { "name": "...", "email": "...", "date": "..." },
        "committer": { "name": "...", "email": "...", "date": "..." },
        "parent_hashes": ["..."],
        "tree_hash": "..."
      }
    }
  ],
  "meta": {
    "resolved_ref_hash": "4be641bf...",
    "incomplete": false,
    "scanned_commits": 23,
    "time_budget_ms": 6,
    "cut_reason": "complete"
  }
}
```

### Meta 필드 변경

| 필드 | Before (go-git) | After (native) |
|---|---|---|
| `incomplete` | `true` (5/23 resolve) | `false` (23/23 resolve) |
| `scanned_commits` | 3 (time budget 초과) | 23 (전체 resolve) |
| `time_budget_ms` | 5000 (설정값) | ~6 (실제 소요) |
| `cut_reason` | `"index_hit_merged"` | `"complete"` |

## Implementation Details

### `GetLatestCommitsNative`

```go
func GetLatestCommitsNative(ctx, repoPath, ref, paths) (map[string]*GitCommit, LatestCommitsMeta)
```

- 각 경로에 대해 `git --git-dir=<repoPath> log -1 --format=<format> <ref> -- <path>` 실행
- 최대 8개 goroutine 병렬 실행 (`nativeGitConcurrency = 8`)
- 개별 호출 타임아웃: 5초 (`nativeGitTimeout`)
- null byte (`%x00`) 구분자로 파싱

### `NativeGetTreeEntries`

```go
func NativeGetTreeEntries(ctx, repoPath, ref, path) ([]GitTreeEntryWithCommit, error)
```

- `git ls-tree -l <ref>:<path>` 실행
- mode, type, hash, size, name 파싱
- go-git의 `GetTreeOnly`를 대체

### git log format

```
%H%x00%T%x00%P%x00%an%x00%ae%x00%aI%x00%cn%x00%ce%x00%cI%x00%s
```

| Specifier | Field |
|---|---|
| `%H` | Commit SHA |
| `%T` | Tree hash |
| `%P` | Parent hashes (space-separated) |
| `%an/%ae/%aI` | Author name/email/date |
| `%cn/%ce/%cI` | Committer name/email/date |
| `%s` | Subject (first line of message) |

## Risk Assessment

| Risk | Mitigation |
|---|---|
| `git` binary 없음 | Docker 이미지에 git 포함 (이미 있음) |
| 프로세스 spawn 오버헤드 | 경로당 < 1ms, 23개 병렬 → 총 ~6ms |
| 동시 요청 시 프로세스 폭증 | goroutine 수 제한 (8), context timeout |
| Large repo (10만+ 파일) | native git은 여전히 빠름 (bloom filter + commit-graph) |

## Verification

```bash
make format
go vet ./internal/...
go build ./internal/...
go test ./internal/overview/...

# API 테스트
curl -s 'http://localhost:5001/repos/<owner>/<repo>/overview/tree/commits' | python3 -c "
import json, sys; d=json.load(sys.stdin)
print(f'entries: {len(d[\"tree\"])}')
print(f'with commit: {sum(1 for t in d[\"tree\"] if t.get(\"latest_commit\"))}')
print(f'meta: {json.dumps(d[\"meta\"], indent=2)}')
"
```
