# Loose Object → Packfile 전환 정리

## 배경

기존 API 기반 commit/merge 작업은 `repo.Storer.SetEncodedObject()`를 직접 호출하여  
blob·tree·commit 오브젝트를 **loose object** (`.git/objects/xx/yy...`) 형태로 개별 저장했다.  
SSH push는 이미 packfile로 저장하도록 변경된 상태였으며, API 경로만 loose 상태로 남아 있었다.

---

## 핵심 구현: `objectCollector`

| 파일 | 역할 |
|------|------|
| `internal/commits/object_collector.go` | commits 패키지용 collector |
| `internal/merge/object_collector.go` | merge 패키지용 collector |

```
objectCollector
  ├── addBlob(content)    → MemoryObject 생성 후 버퍼에 누적
  ├── encodeTree(tree)    → MemoryObject 생성 후 버퍼에 누적
  ├── encodeCommit(commit)→ MemoryObject 생성 후 버퍼에 누적
  └── flush(storer)
        ├── PackfileWriter 지원 시 → memory.Storage → packfile.Encoder → pack+idx 1개 생성
        └── 미지원(in-memory storer 등) → SetEncodedObject fallback (loose)
```

중복 오브젝트는 `seen map[Hash]struct{}`로 자동 dedup.

---

## 전환된 영역

### 1. API Commit (`commits/transaction.go`)

| 함수 | 변경 전 | 변경 후 |
|------|--------|--------|
| `createBlob` | `Storer.SetEncodedObject` → loose | `c.addBlob` → 버퍼 |
| `buildTreeRecursive` | `Storer.SetEncodedObject` → loose | `c.encodeTree` → 버퍼 |
| `createTreeFromChanges` | — | 내부 collector 생성 + `c.flush` (blob+tree 일괄 pack) |
| `createCommit` | `Storer.SetEncodedObject` → loose | `c.encodeCommit` + `c.flush` (commit pack) |

**결과**: `HandleMultiFileCommit` 1회 호출 시 loose object 0개, packfile 2개 (tree pack + commit pack)

---

### 2. Merge Tree 생성 (`merge/apply_tree.go`)

| 함수 | 변경 전 | 변경 후 |
|------|--------|--------|
| `createAndStoreTree` | `Storer.SetEncodedObject` → loose | `c.encodeTree` → 버퍼 |
| `createEmptyTree` | loose 저장 후 read-back | `emptyTree()` - in-memory `*object.Tree` 직접 반환 (저장 없음) |
| type conflict 분기 (`mergeDirectoryEntry`) | `createAndStoreTree` → loose 저장 후 `repo.TreeObject` read-back | `&object.Tree{Entries: ...}` 직접 생성 (저장 없음) |
| `createMergedTreeWithBase` | — | 내부 collector 생성 + `c.flush` (모든 subtree 일괄 pack) |

`mergeTreeEntries` / `resolveTreeEntry` / `mergeDirectoryEntry` / `resolveTypeConflict` 모두  
`c *objectCollector` 파라미터 추가로 재귀 전체 체인에 threading.

---

### 3. Merge Commit 저장 (strategy 파일 3종)

| 파일 | 변경 전 | 변경 후 |
|------|--------|--------|
| `strategy_merge.go` (`performThreeWayMerge`) | `Storer.SetEncodedObject` → loose | `newObjectCollector().encodeCommit` + `flush` |
| `strategy_squash.go` (`performSquashMerge`) | `Storer.SetEncodedObject` → loose | 동일 |
| `strategy_rebase.go` (`performRebase` 루프) | `Storer.SetEncodedObject` → loose | 커밋마다 collector + flush (reload 후 다음 iteration) |

---

## 주의 사항

- **cross-instance 가시성**: packfile은 동일 파일시스템을 공유하는 다른 `*gogit.Repository` 인스턴스에서 즉시 조회되지 않는다 (pack index 캐시 미갱신). 운영 환경에서는 각 API 요청이 repo를 새로 열기 때문에 문제 없음. 테스트에서는 merge 후 `gogit.PlainOpen(repoPath)`로 repo를 재열기해야 한다.
- **fallback**: `PackfileWriter` 미지원 storer (e.g. `memory.Storage`) 에서는 `SetEncodedObject` fallback 자동 적용.
