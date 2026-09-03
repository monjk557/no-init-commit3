# Frontend Handoff: Compare 페이지 개선사항

## 일시

2026-03-09

## 현상

Compare URL에서 `/`가 포함된 브랜치명이 잘못 파싱됨.

**URL:**
```
/collabops-one/repositories/2/compare/main...fix/empty-repo-overview-404
```

**기대 동작:**
- target: `main`
- source: `fix/empty-repo-overview-404`

**실제 동작:**
- target: `main`
- source: `fix` (슬래시 이후가 잘림)

**에러 메시지:**
> 'fix와 main' 사이에 변경 요청이 없습니다

## 원인

`main...fix/empty-repo-overview-404`에서 `...`로 split한 후, source 부분(`fix/empty-repo-overview-404`)이 URL path의 일부로 해석되어 첫 번째 segment(`fix`)만 브랜치명으로 인식하는 것으로 추정.

## 해결 방향

`...` 구분자 기준으로 split할 때:

```
[target] ... [source]
  main   ... fix/empty-repo-overview-404
```

- `...` 앞: target (첫 번째 segment)
- `...` 뒤: source (**나머지 전체**, 슬래시 포함)

### 참고: GitHub 동작

GitHub도 동일한 URL 포맷을 사용하며 슬래시가 포함된 브랜치명을 정상 처리함:

```
github.com/org/repo/compare/main...fix/some-branch
```

### 주의사항

- 브랜치명에 슬래시가 여러 개 들어갈 수 있음: `feature/team/task-123`
- `...` 구분자 자체가 URL path에 포함되므로, URL router 레벨이 아닌 파싱 로직에서 처리해야 할 수 있음
- 프론트엔드 라우터가 `/compare/:refs` 형태로 받아서 내부에서 `...` split하는 방식이 안전

## 재현 경로

1. `https://stage.collabops.ai/collabops-one/repositories/2/compare/main...fix/empty-repo-overview-404` 접근
2. "'fix와 main' 사이에 변경 요청이 없습니다" 에러 확인

---

## 이슈 2: Compare API 호출 최적화

### 현상

Compare 페이지에서 현재 **2개의 API를 별도 호출**하고 있음:

1. `GET /repos/:owner/:repo/merges/preview?source_ref=X&target_ref=Y` — conflict 분석, 파일 변경사항
2. `GET /repos/:owner/:repo/commits?source_ref=X&target_ref=Y` — 커밋 목록

### 문제

merge preview 응답에 이미 커밋 관련 데이터가 **모두 포함**되어 있음:

```json
{
  "can_merge": true,
  "has_conflicts": false,
  "changes_summary": { ... },
  "files_changed": [ ... ],
  "commits": { "ahead": 3, "behind": 1 },
  "contributors": ["user@email.com"],
  "commits_by_date": [
    {
      "date": "2026-03-09",
      "commits": [
        { "sha": "abc123", "author": "...", "message": "..." }
      ]
    }
  ],
  "count_summary": { "commits": 3, "files": 5, "contributors": 1 }
}
```

별도 commits API 호출은 **중복 요청**이며, 서버에 불필요한 merge-base 계산을 두 번 시키고 있음.

### 해결

commits API 호출을 제거하고, **merge preview 1회 호출**로 통합.

| 데이터 | 현재 | 변경 후 |
|--------|------|---------|
| conflict 분석 | merge preview | merge preview |
| 파일 변경사항 | merge preview | merge preview |
| 커밋 목록 | commits API (별도 호출) | merge preview의 `commits_by_date` |
| ahead/behind | merge preview | merge preview의 `commits` |
| 기여자 목록 | 없음 | merge preview의 `contributors` |

**API 호출: 2회 → 1회로 감소.**

---

## 이슈 3: Diff API 브랜치명 파싱 동일 버그

### 현상

이슈 1과 동일한 슬래시 파싱 문제가 diff API 호출에서도 발생.

**현재 호출:**
```
GET /api/v1/executor/{ws_slug}/{uuid}/branches/fix/empty-repo-overview-404/base
```

**프론트엔드가 의도한 것:**
- branch: `fix/empty-repo-overview-404`

**실제 서버가 받는 것:**
- branch: `fix` (슬래시에서 잘림)
- 나머지 path segment가 URL 경로로 해석됨

### 원인

이슈 1과 동일. 브랜치명에 포함된 `/`가 URL path separator로 해석됨.

### 해결: query parameter 방식 엔드포인트 추가 완료 (백엔드)

git executor에 query parameter 방식의 branch diff 엔드포인트를 추가했음:

**기존 (path parameter — 슬래시 문제 있음):**
```
GET /repos/:owner/:repo/diff/branches/:base/:head
```

**신규 (query parameter — 슬래시 안전):**
```
GET /repos/:owner/:repo/diff/branches?base=fix/empty-repo-overview-404&head=main
```

- 응답 스키마는 기존 `HandleBranchDiff`와 **100% 동일**
- 페이지네이션(`page`, `page_size`), `context` 파라미터 동일하게 지원
- 기존 path parameter 엔드포인트도 하위호환을 위해 유지

### 수정 필요한 레이어 (3단 호출 체인)

```
Frontend → Orchestrator → Git Executor
```

| 레이어 | 현재 | 변경 | 상태 |
|--------|------|------|------|
| Git Executor | `/diff/branches/:base/:head` | `/diff/branches?base=X&head=Y` 추가 | **완료** |
| Orchestrator | `api/v2/executor/{repo_key}/diffs/branches/{base}/{head}` | `api/v2/executor/{repo_key}/diffs/branches?base=X&head=Y` | **TODO** |
| Frontend | path에 브랜치명 삽입 | query parameter로 전달 | **TODO** |

**Orchestrator 작업**: git executor의 새 엔드포인트(`/diff/branches?base=X&head=Y`)로 프록시하도록 변경. 프론트엔드에서 받은 query parameter를 그대로 전달.

**Frontend 작업**: diff API 호출 방식 변경:

```diff
- GET /api/v2/executor/{repo_key}/diffs/branches/{base}/{head}
+ GET /api/v2/executor/{repo_key}/diffs/branches?base={base}&head={head}
```

### 영향 범위

이 버그는 `/`가 포함된 브랜치명을 사용하는 **모든 API 호출**에 해당됨. compare 페이지에서 호출하는 API 전체를 점검할 것:

- merge preview API — `source_ref`, `target_ref` query parameter → 영향 없음
- commits API — `source_ref`, `target_ref` query parameter → 영향 없음
- **diff API — path parameter로 브랜치명 전달 → 영향 있음 → query parameter 엔드포인트 추가 완료**
