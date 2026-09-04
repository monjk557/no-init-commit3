# Diff Expand Context API - Frontend Handoff

## 개요

Diff 뷰에서 hunk 사이 숨겨진 context 라인을 확장하기 위한 경량 API입니다.
기존 `/diff/file?context=N` 재호출 방식은 파일 전체에 대해 `git diff`를 다시 실행하지만,
이 API는 **blob 단일 읽기**만 수행하므로 파일 크기에 관계없이 빠릅니다.

---

## 신규 API 목록

### 1. `GET /repos/:owner/:repo/diff/expand-context`

Hunk 사이 숨겨진 context 라인을 blob에서 읽어 반환합니다.

#### Query Parameters

| 파라미터   | 필수 | 설명 |
|-----------|------|------|
| `path`     | O | 파일 경로 |
| `base`     | O | Base ref (커밋 SHA 또는 브랜치명) |
| `head`     | O | Head ref (커밋 SHA 또는 브랜치명) |
| `from_old` | O | 가져올 첫 번째 old 라인 번호 (1-based) |
| `to_old`   | O | 가져올 마지막 old 라인 번호 (1-based) |
| `from_new` | O | 대응되는 첫 번째 new 라인 번호 (1-based) |
| `side`     | X | `"old"` 지정 시 base ref에서 blob 읽기 (기본: head ref). deleted 파일 확장 시 사용 |

#### 라인 범위 제한

- 최대 **500 라인** (`to_old - from_old + 1 <= 500`)
- `from_old >= 1`, `to_old >= from_old`, `from_new >= 1`

#### Response

```json
{
  "success": true,
  "lines": [
    {
      "type": "CONTEXT",
      "blobLineNumber": 45,
      "text": " unchanged line content",
      "left": 45,
      "right": 50,
      "displayNoNewLineWarning": false
    }
  ],
  "path": "src/main.go",
  "start_old": 45,
  "start_new": 50,
  "count": 10
}
```

- `lines[]`의 각 항목은 기존 `diffLines`와 동일한 `LineChange` 구조
- `type`은 항상 `"CONTEXT"`
- `text`는 space prefix (` `) 포함 — 기존 CONTEXT 라인과 동일 포맷
- `left`/`right`에 old/new 라인 번호 포함

---

### 2. `GET /repos/:owner/:repo/commits/:sha`

단일 커밋의 메타데이터를 반환합니다. Commit diff 페이지 상단에 표시할 정보용.

#### Response

```json
{
  "success": true,
  "sha": "abc123...",
  "message": "Full commit message\n\nincluding body",
  "author": {
    "name": "Author Name",
    "email": "author@example.com",
    "date": "2026-03-17T12:00:00Z"
  },
  "committer": {
    "name": "Committer Name",
    "email": "committer@example.com",
    "date": "2026-03-17T12:00:00Z"
  },
  "parents": ["parent_sha1"],
  "tree_hash": "tree_sha"
}
```

- `message`는 `%B` 포맷 (subject + body 전체)

---

### 3. `GET /repos/:owner/:repo/commits/compare/summary`

두 ref 간 merge base 기준 commit count와 changed file count를 반환하는 경량 API.

#### Query Parameters

| 파라미터 | 필수 | 설명 |
|---------|------|------|
| `source` | O | 소스 ref (커밋 SHA, 브랜치명 등) |
| `target` | O | 타겟 ref |

#### Response

```json
{
  "success": true,
  "merge_base": "merge_base_sha",
  "source_sha": "resolved_source_sha",
  "target_sha": "resolved_target_sha",
  "commit_count": 3,
  "changed_file_count": 7
}
```

---

## Frontend 구현 가이드: Hunk Expand

### 기존 Diff 응답 구조

```
diffLines: [
  { type: "HUNK", left: 10, right: 10, text: "@@ -10,5 +10,5 @@" },
  { type: "CONTEXT", left: 10, right: 10, text: " line 10" },
  { type: "DELETE",  left: 11, right: null, text: "-old line" },
  { type: "ADD",     left: null, right: 11, text: "+new line" },
  { type: "CONTEXT", left: 12, right: 12, text: " line 12" },
  { type: "CONTEXT", left: 13, right: 13, text: " line 13" },
  { type: "CONTEXT", left: 14, right: 14, text: " line 14" },
  // ← 여기에 숨겨진 라인 15~24 (gap)
  { type: "HUNK", left: 25, right: 25, text: "@@ -25,3 +25,3 @@" },
  { type: "CONTEXT", left: 25, right: 25, text: " line 25" },
  ...
]
```

### Gap 계산 방법

두 HUNK 사이의 gap:

```typescript
// hunkA: 이전 hunk의 마지막 CONTEXT/ADD/DELETE 라인
// hunkB: 다음 HUNK 라인

const lastLineBeforeGap = diffLines[hunkBIndex - 1];
const hunkBLine = diffLines[hunkBIndex];

const fromOld = lastLineBeforeGap.left + 1;   // 이전 hunk 끝 + 1
const toOld = hunkBLine.left - 1;             // 다음 hunk 시작 - 1
const fromNew = lastLineBeforeGap.right + 1;  // new 쪽도 동일

// gap이 존재하는지 확인
if (fromOld <= toOld) {
  // expand 가능
}
```

### API 호출 & Splice

```typescript
async function expandHunkGap(
  owner: string, repo: string,
  path: string, base: string, head: string,
  fromOld: number, toOld: number, fromNew: number
) {
  const params = new URLSearchParams({
    path, base, head,
    from_old: String(fromOld),
    to_old: String(toOld),
    from_new: String(fromNew),
  });

  const res = await fetch(
    `/repos/${owner}/${repo}/diff/expand-context?${params}`
  );
  const data = await res.json();

  if (data.success) {
    // data.lines를 diffLines 배열의 hunkB 위치에 splice
    // hunkB의 HUNK 헤더 라인은 제거하거나 유지 (UI 정책에 따라)
    diffLines.splice(hunkBIndex, 0, ...data.lines);
  }
}
```

### 파일 상단/하단 확장

- **파일 상단** (첫 번째 HUNK 위): `from_old=1`, `to_old=firstHunk.start_old-1`, `from_new=1`
- **파일 하단** (마지막 HUNK 아래): `from_old=lastLine.left+1`, `to_old=9999` (서버가 자동 클램핑), `from_new=lastLine.right+1`

### Deleted 파일 처리

파일이 삭제된 경우 head ref에 파일이 없으므로 `side=old` 파라미터를 추가:

```typescript
params.append('side', 'old');
```

### UX 권장사항

1. **Expand 버튼**: HUNK 헤더 라인 위에 "Show N hidden lines" 버튼 표시
   - `N = toOld - fromOld + 1`
2. **부분 확장**: gap이 클 때 (예: 100줄 이상) "Show 20 lines above/below" 부분 확장 옵션
   - `from_old`/`to_old` 범위를 20줄 단위로 조절
3. **Loading 상태**: 버튼 클릭 시 spinner 표시 (응답은 보통 50ms 이내)
4. **HUNK 헤더 제거**: expand 후 해당 HUNK 헤더 라인은 제거 (gap이 완전히 채워졌으므로)
   - 부분 확장 시에는 HUNK 헤더 유지하고 라인 번호만 업데이트

---

## 성능 비교

| 방식 | 동작 | git 프로세스 | 파일 크기 영향 |
|------|------|-------------|---------------|
| 기존: `/diff/file?context=N` | `git diff -UN` 전체 재실행 → 파싱 | 3~4개 | O(파일크기) |
| 신규: `/diff/expand-context` | `git show ref:path` blob 읽기만 | 2개 (resolve + show) | O(blob 크기), 라인 추출은 O(N) |

blob 읽기는 OS 페이지 캐시에 의해 반복 요청 시 더 빨라집니다.
