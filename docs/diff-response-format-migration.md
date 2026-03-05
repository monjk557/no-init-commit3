# Diff Response Format Migration

## Overview

이 문서는 diff 관련 API의 응답 포맷을 프론트엔드 요구사항에 맞게 변경한 내용을 정리합니다.

## 영향 받는 API 엔드포인트

| Endpoint | Handler | 변경 여부 |
|----------|---------|-----------|
| `GET /repos/:owner/:repo/diff/commits/:base/:head` | `HandleCommitDiff` | **변경됨** - FileDiff/LineChange 포맷 변경 |
| `GET /repos/:owner/:repo/diff/branches/:base/:head` | `HandleBranchDiff` | **변경됨** - FileDiff/LineChange 포맷 변경 |
| `GET /repos/:owner/:repo/diff/file` | `HandleFileDiff` | **변경됨** - FileDiff/LineChange 포맷 변경 |
| `GET /repos/:owner/:repo/diff/commit/:sha` | `HandleSingleCommitDiff` | **변경됨** - FileDiff/LineChange 포맷 변경 |
| `GET /repos/:owner/:repo/patch/:base/:head` | `HandlePatch` | 변경 없음 (text/plain 반환) |

Merge Preview API (`MergePreviewResponse.FilesChanged`)도 `diffs.FileDiff`를 embed하므로 자동으로 영향 받음.

## 변경 상세

### 1. FileDiff 구조체 (JSON 필드명 변경)

| 기존 JSON 필드 | 변경 후 JSON 필드 | 설명 |
|---------------|------------------|------|
| `changes` | `diffLines` | 라인별 diff 배열 |
| `load_status` | `loadStatus` | 파일 diff 로드 상태 |

**Go 필드명은 그대로 유지** (`Changes`, `LoadStatus`)하고, JSON 태그만 변경.

### 2. LineChange 구조체 (필드 전면 변경)

| 기존 필드 | 기존 JSON | 변경 후 필드 | 변경 후 JSON | 타입 변경 |
|-----------|----------|-------------|-------------|-----------|
| `LeftLineNum` (int) | `left_line` | `Left` (*int) | `left` | int → *int (nullable) |
| `RightLineNum` (int) | `right_line` | `Right` (*int) | `right` | int → *int (nullable) |
| `Content` (string) | `content` | `Text` (string) | `text` | 동일 |
| `HunkIndex` (int) | `hunk_index` | `HunkIndex` (int) | (제거) | JSON에서 제외 (`json:"-"`) |
| - | - | `BlobLineNumber` (*int) | `blobLineNumber` | **신규 추가** |
| - | - | `DisplayNoNewLineWarning` (bool) | `displayNoNewLineWarning` | **신규 추가** |
| `Meta` | `meta` | (제거) | - | JSON에서 제외 |
| `Comment` | `comment` | (제거) | - | JSON에서 제외 |

### 3. LineChangeType 상수 값 변경 (대문자)

| 기존 값 | 변경 후 값 | 설명 |
|---------|-----------|------|
| `"add"` | `"ADD"` | 라인 추가 |
| `"del"` | `"DELETE"` | 라인 삭제 |
| `"context"` | `"CONTEXT"` | 컨텍스트 라인 |
| `"hunk"` | `"HUNK"` | Hunk 헤더 |

### 4. 새 필드 로직

#### `blobLineNumber`
- **HUNK**: hunk 시작 old 라인 번호 (= left)
- **CONTEXT**: old 라인 번호 (= left)
- **DELETE**: old 라인 번호 (= left)
- **ADD**: `null` (old 파일에 대응하는 라인 없음)

#### `displayNoNewLineWarning`
- `\ No newline at end of file` 감지 시 해당 직전 라인에 `true` 설정
- 그 외에는 항상 `false`

#### `left` / `right` nullable 로직
- **CONTEXT**: left = oldLine, right = newLine (둘 다 non-null)
- **DELETE**: left = oldLine, right = `null`
- **ADD**: left = `null`, right = newLine
- **HUNK**: left = startOld, right = startNew (둘 다 non-null)

## 변경 후 응답 예시

```json
{
  "path": "packages/react-reconciler/src/ReactFiberBeginWork.js",
  "loadStatus": "",
  "diffLines": [
    {
      "type": "HUNK",
      "blobLineNumber": 25,
      "displayNoNewLineWarning": false,
      "text": "@@ -26,43 +26,41 @@ func ...",
      "left": 25,
      "right": 25
    },
    {
      "type": "CONTEXT",
      "blobLineNumber": 26,
      "displayNoNewLineWarning": false,
      "text": " \t\treturn nil, fmt.Errorf(\"failed to get target commit: %w\", err)",
      "left": 26,
      "right": 26
    },
    {
      "type": "DELETE",
      "blobLineNumber": 49,
      "displayNoNewLineWarning": false,
      "text": "-\t\toldCode here",
      "left": 49,
      "right": null
    },
    {
      "type": "ADD",
      "blobLineNumber": null,
      "displayNoNewLineWarning": false,
      "text": "+\t\tnewCode here",
      "left": null,
      "right": 52
    }
  ]
}
```

## 변경된 파일 목록

| 파일 | 변경 내용 |
|------|----------|
| `internal/diffs/schema.go` | 구조체 정의, JSON 태그, 상수값 변경 |
| `internal/diffs/service.go` | LineChange 생성 로직 업데이트 |
| `internal/diffs/native_git.go` | LineChange/FileDiff 참조 코드 업데이트 |
| `internal/diffs/myers_diff_adapter.go` | LineChange 생성 로직 업데이트 |
| `internal/diffs/service_test.go` | JSON 필드명 검증 테스트 업데이트 |
| `internal/diffs/handler_test.go` | 필드 접근 테스트 업데이트 |
| `internal/merge/preview.go` | diffs.LineChange 생성 코드 업데이트 |
| `internal/merge/preview_test.go` | 테스트 업데이트 |
| `internal/merge/merge_preview_ref_resolution_test.go` | 테스트 업데이트 |
