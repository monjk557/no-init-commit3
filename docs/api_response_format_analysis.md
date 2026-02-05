# API 응답 형식 분석 보고서

## 1. 개요

- **목적**: 전체 API의 성공/에러 응답 형식 일관성 검증
- **범위**: `internal/*/handler*.go`, `internal/*/handler_helpers*.go` 등에서 JSON 응답을 반환하는 엔드포인트

---

## 2. 성공 응답 형식

### 2.1 표준 패턴 (대다수)

| 패턴 | 예시 | 사용처 예 |
|------|------|------------|
| `{ "success": true, "data": {...} }` | 단일 리소스/객체 | branches, tags, merge, commits 등 |
| `{ "success": true, "results": [...], "pagination": {...} }` | 목록 + 페이지네이션 | branches 목록, tags 목록 등 |

### 2.2 비표준(최상위 필드 직접 노출)

- **`GET /branches/validate/*name`**  
  - `{ "success": true, "is_available": bool }`  
  - `data` 래퍼 없음
- **`GET /branches/resolve/*ref`**  
  - `{ "success": true, "name", "commit_sha", "ref_path" }`  
  - `data` 래퍼 없음
- **`DELETE /tags/*tag`**  
  - `{ "success": true, "tag": "..." }`  
  - `data` 래퍼 없음

**권장**: 나머지 API와 통일하려면 위 응답도 `data` 안에 넣는 형태로 정리. (연동 중인 클라이언트와 협의 후 적용.)

### 2.3 data 래핑 근거 (참고용, 현재 미적용)

- **클라이언트 파싱 일관성**  
  모든 성공 응답이 `success` + `data` 한 구조이면, 클라이언트는 `if (res.success) payload = res.data` 로만 처리하면 되어 분기 최소화.

- **메타 필드와 페이로드 분리**  
  `success`, `error`(에러 시) 같은 메타데이터는 최상위에 두고, 실제 리소스/결과는 `data` 안에만 두면 스키마가 명확해짐. 나중에 `request_id`, `meta` 등을 추가해도 `data`는 그대로 유지 가능.

- **목록 API와의 통일**  
  이미 `results` + `pagination` 을 쓰는 목록 API는 “결과”가 별도 키에 담겨 있음. 단일 리소스/스칼라 응답도 `data` 에 담으면 “결과가 항상 특정 키 아래에 있다”는 규칙으로 통일됨.

- **타입/스키마 생성 용이**  
  OpenAPI/코드생성 시 `SuccessResponse<T> = { success: true, data: T }` 로 한 번만 정의하면 되고, API별로 `T`만 바꾸면 됨.

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
| 성공 응답 | **부분 일치** | 대부분 `success` + `data`/`results`+`pagination` 이나, validate/resolve/delete tag 등은 최상위 필드 직접 노출 |
| 에러 응답 | **불일치** | blob 에러는 `success` 없이 `error` 만 사용; 일부는 `error_code` 등 추가 필드 사용 |

### 4.2 권장 조치

1. **에러 응답 통일**
   - 모든 JSON 에러 응답: `{ "success": false, "error": "메시지" }` 로 통일
   - `internal/blob/handler.go` 의 `sendError()` 에 `"success": false` 추가

2. **성공 응답 통일 (선택, 연동 협의 후)**
   - validate, resolve, delete tag 등도 `{ "success": true, "data": { ... } }` 형태로 감싸면 클라이언트 파싱 일관성 확보에 유리

3. **문서화**
   - API 스펙(OpenAPI/Swagger 등)에 성공/에러 응답 스키마를 명시하고, 위 규칙을 기준으로 정리

---

*작성일: 2025-02-04*
