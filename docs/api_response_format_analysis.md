# API 응답 형식 분석 보고서

## 1. 개요

- **목적**: 전체 API의 성공/에러 응답 형식 일관성 검증
- **범위**: `internal/*/handler*.go`, `internal/*/handler_helpers*.go` 등에서 JSON 응답을 반환하는 엔드포인트

---

## 2. 성공 응답 형식

### 2.1 규약: flat 응답 (data 래퍼 미사용)

- **성공 여부**: HTTP status code + 최상위 `success: true` 로 판단.
- **페이로드**: `data` 필드를 두지 않고, 모든 필드를 최상위에 flat 하게 둔다.
- **예**: `{ "success": true, "branch_name": "...", "commit_sha": "..." }`, `{ "success": true, "results": [...], "pagination": {...} }`

### 2.2 표준 패턴 (규약 반영)

| 패턴 | 예시 |
|------|------|
| 단일 리소스 | `{ "success": true, "branch_name": "...", "commit_sha": "...", ... }` (최상위에 필드 직접) |
| 목록 + 페이지네이션 | `{ "success": true, "results": [...], "pagination": {...} }` |

### 2.3 마이그레이션 (기존 data 사용처)

- **branches** Create/Update/Delete, **tree** 등 기존에 `{ "success": true, "data": {...} }` 형태를 쓰던 API는 `data` 를 제거하고 payload 필드를 최상위로 올려 flat 응답으로 전환한다.

---


## 3. 에러 응답 형식

### 3.1 표준 패턴 (대다수)

- **형식**: `{ "success": false, "error": "메시지" }`
- **사용처**: merge, branches, tags, commits 등 대부분 핸들러

### 3.2 비표준 / 불일치

| 위치 | 형식 | 비고 |
|------|------|------|
| **`internal/blob/handler.go`** `sendError()` | `{ "error": err.Error() }` | **`success: false` 없음** |
| 일부 에러 응답 | `{ "error_code": "...", "error": "..." }` 등 | `success` 필드 유무/이름이 API별로 상이할 수 있음 |

- **blob**  
  - 성공: 바이너리/텍스트 본문 직접 반환 (JSON 아님)  
  - 실패: JSON `{"error": "..."}` 만 사용 → 다른 API와 맞추려면 `"success": false` 추가 권장

---

## 4. 요약 및 권장사항

### 4.1 일관성 요약

| 구분 | 일관 여부 | 설명 |
|------|-----------|------|
| 성공 응답 | **규약: flat** | `data` 래퍼 없이 `success` + payload 필드를 최상위에 두는 flat 응답으로 통일 (규약: `api_response_contract.md`) |
| 에러 응답 | **불일치** | blob 에러는 `success` 없이 `error` 만 사용; 일부는 `error_code` 등 추가 필드 사용 |

### 4.2 권장 조치

1. **에러 응답 통일**
   - 모든 JSON 에러 응답: `{ "success": false, "error": "메시지" }` 로 통일
   - `internal/blob/handler.go` 의 `sendError()` 에 `"success": false` 추가

2. **성공 응답: flat 통일**
   - `data` 필드를 제거하고, 모든 성공 응답을 최상위 flat 구조로 통일 (HTTP status + `success` 로 성공 여부 판단)

3. **문서화**
   - API 스펙(OpenAPI/Swagger 등)에 성공/에러 응답 스키마를 명시하고, 위 규칙을 기준으로 정리

---

*작성일: 2025-02-04*
