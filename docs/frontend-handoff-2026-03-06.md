# Frontend Handoff - Commit Diff Page 개선

> 대상 페이지: **Commit Diff Page** (Single Commit Diff / Commit Range Diff)

---

## 1. Changed Files 메타데이터 전체 렌더링

### 현상
- 33 files changed인데 파일 메타데이터가 10개만 렌더링됨

### 원인
- 백엔드는 **모든 파일의 메타데이터를 응답에 포함**함
- `diffLines`(라인별 diff)만 상위 10개 파일로 제한 (백엔드 상수 `MaxFilesWithChanges = 10`)
- 11번째 파일부터: `loadStatus: "collapsed"`, `diffLines: null`
- 메타데이터(`path`, `status`, `additions`, `deletions`)는 **전체 파일에 항상 포함**

### 수정 사항
- `files` 배열의 **모든 항목**에 대해 메타데이터를 렌더링할 것
- `loadStatus` 값에 따라 diff 본문 영역만 분기 처리

### loadStatus별 렌더링

| loadStatus | diffLines | diff 본문 영역 |
|------------|-----------|---------------|
| `"loaded"` | `[...]` | diff 전체 렌더링 |
| `"collapsed"` | `null` | "Load diff" 버튼 표시 → 클릭 시 개별 조회 |
| `"too_large"` | `null` | "Large diffs are not rendered by default" 안내 |
| `"binary"` | `null` | "Binary file not shown" 안내 |

### collapsed 파일 개별 로드 API
```
GET /repos/:owner/:repo/diff/file?path={파일경로}&base={base}&head={head}&context=3
```
- 응답 형식은 동일한 `DiffResponse` (`files` 배열에 1개 파일)
- 로드 후 해당 파일의 `loadStatus`를 `"loaded"`로 교체하여 diff 렌더링

---

## 2. Changed Files Tree 정렬

### 현상
- 프론트에서 `files[].path`를 조합하여 file tree를 구성하는데, 디렉토리/파일 구분 없이 평탄하게 나열됨

### 수정 사항
- 프론트에서 file tree를 빌드할 때 **디렉토리(tree) 먼저, 파일(blob) 다음** 순서로 정렬
- 각 그룹 내에서는 **ASCII 정렬** (대문자 < 소문자)

### 정렬 규칙
```
1. 디렉토리 노드 (자식이 있는 경로)  → 위
2. 파일 노드 (leaf)                  → 아래
3. 같은 타입 내에서 name 기준 ASCII 오름차순
```

### 예시
응답 `files[].path`:
```
src/utils/helper.go
src/main.go
README.md
docs/api.md
.gitignore
src/utils/format.go
```

렌더링 tree (정렬 후):
```
docs/
  api.md
src/
  utils/
    format.go
    helper.go
  main.go
.gitignore
README.md
```

### 정렬 참고 (pseudo-code)
```ts
nodes.sort((a, b) => {
  // 디렉토리 먼저
  if (a.isDir && !b.isDir) return -1;
  if (!a.isDir && b.isDir) return 1;
  // 같은 타입이면 ASCII 정렬
  return a.name < b.name ? -1 : a.name > b.name ? 1 : 0;
});
```

---

## 3. File Tree 클릭 시 해당 Diff로 스크롤

### 요구 사항
- File tree에서 파일을 클릭하면 해당 파일의 diff 섹션으로 **자동 스크롤**

### 구현 방식 (권장)
1. 각 diff 섹션에 파일 경로 기반 `id` 부여:
   ```html
   <div id="diff-src/main.go">...</div>
   ```
2. tree 항목 클릭 시 해당 `id`로 스크롤:
   ```ts
   const el = document.getElementById(`diff-${filePath}`);
   el?.scrollIntoView({ behavior: 'smooth', block: 'start' });
   ```
3. `collapsed` 파일 클릭 시:
   - 먼저 diff 개별 로드 API 호출
   - 로드 완료 후 해당 섹션으로 스크롤

---

## 참고: 백엔드 응답 구조

### FileDiff 객체 (files 배열 내 각 항목)
```json
{
  "path": "src/main.go",
  "status": "modified",
  "additions": 10,
  "deletions": 3,
  "diffLines": [ ... ] | null,
  "hunks": [ ... ],
  "is_binary": false,
  "loadStatus": "loaded" | "collapsed" | "too_large" | "binary",
  "old_path": "src/old_main.go",
  "metadata": { "old_mode": "100644", "new_mode": "100644" }
}
```

### status 값

| status | 의미 | old_path |
|--------|------|----------|
| `"added"` | 새 파일 | 없음 |
| `"deleted"` | 삭제된 파일 | 없음 |
| `"modified"` | 수정된 파일 | 없음 |
| `"renamed"` | 이름 변경 | 이전 경로 |

### stats 객체
```json
{
  "total_additions": 50,
  "total_deletions": 20,
  "files_changed": 33
}
```
- `files_changed`: 전체 변경 파일 수 (collapsed 포함)
