# 002. API 응답 규약

모든 REST API JSON 응답이 지켜야 하는 규칙.

---

## 1. 적용 범위

- JSON 응답을 반환하는 REST API 엔드포인트 (`/repos/{owner}/{repo}/...` 등)
- 바이너리/스트리밍 응답(예: blob 본문, packfile)은 본문 구조 제외, **에러 시에만** 아래 규약 적용

---

## 2. 필수 규칙 (MUST)

### 2.1 에러 응답

**모든 JSON 에러 응답은 다음을 만족해야 한다.**

| 필드 | 타입 | 필수 | 설명 |
|------|------|------|------|
| `success` | boolean | O | 반드시 `false` |
| `error` | string | O | 사용자/로그에 노출할 에러 메시지 |

- **형식**: `{ "success": false, "error": "메시지" }`
- 추가 필드(`error_code`, `request_id` 등)는 선택. 이 두 필드는 항상 포함.
- **예외**: Git Smart HTTP 등 비-JSON 프로토콜 에러는 해당 프로토콜 규격을 따름.

### 2.2 성공 응답

**모든 JSON 성공 응답은 다음을 만족해야 한다.**

| 필드 | 타입 | 필수 | 설명 |
|------|------|------|------|
| `success` | boolean | O | 반드시 `true` |

- 최상위에 `"success": true` 를 포함한다.
- **`data` 래퍼를 사용하지 않는다.** 페이로드는 `success`와 동일한 최상위 레벨에 flat 하게 둔다. (예: `{ "success": true, "branch_name": "...", "commit_sha": "..." }` 또는 `{ "success": true, "results": [...], "pagination": {...} }`)

---

## 3. 권장 규칙 (SHOULD)

- **에러 응답**: 다른 메타데이터가 필요하면 `success`, `error` 뒤에 추가 (예: `request_id`, `error_code`).
- **성공 응답**: `message`, `meta`, `pagination` 등 메타는 최상위에 두고, 리소스/결과 필드도 모두 최상위에 둔다. HTTP status code + `success`로 성공 여부 판단.

---

## 4. 참조

- 에러 처리 패턴: `003_error-handling.md`
