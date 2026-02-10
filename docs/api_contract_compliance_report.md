# API 규약 적합도 보고서

**기준 규약**: `docs/api_response_contract.md`  
**작성일**: 2026-02-10  
**범위**: REST API JSON 응답 (성공/에러)

---

## 1. 규약 요약

| 구분 | MUST | 내용 |
|------|------|------|
| **에러 응답** | `success` | boolean, 반드시 `false` |
| | `error` | string, 필수 |
| **성공 응답** | `success` | boolean, 반드시 `true` |
| | 페이로드 | `data` 래퍼 미사용, 최상위 flat |

---

## 2. 에러 응답 적합도

모든 JSON 에러 응답은 `{ "success": false, "error": "..." }` 형식 준수 여부.

| 패키지 | 파일 | 적합 | 비고 |
|--------|------|:----:|------|
| api | response.go (미들웨어) | ✅ | ErrorHandler 응답에 success, error 포함 |
| blob | handler.go | ✅ | sendError() 및 직접 c.JSON 모두 `success: false, error` |
| branches | handler.go | ✅ | gin.H 에러에 success: false 포함 (미들웨어 경로 포함) |
| commits | handler.go | ✅ | 모든 에러 응답 success: false, error |
| diffs | handler.go | ✅ | 동일 |
| merge | handler.go | ✅ | 동일 |
| overview | handler.go | ✅ | 동일 |
| repositories | handler.go | ✅ | 400/409/500 모두 success, error |
| state | handler.go | ✅ | 400/500 success, error |
| tags | handler.go | ✅ | 동일 |

**결과**: 에러 응답 **전 구간 적합**.

---

## 3. 성공 응답 적합도 (flat, data 래퍼 없음)

| 패키지 | 엔드포인트/기능 | 응답 타입 | success | data 래퍼 | 적합 |
|--------|------------------|-----------|:-------:|:--------:|:----:|
| **repositories** | POST /repos/{owner}/{repo} | CreateRepositoryResponse | ✅ | 없음 | ✅ |
| **state** | GET .../state | RepositoryStateData | ✅ | 없음 | ✅ |
| **branches** | GET .../branches | GetBranchesResponse | ✅ | 없음 (branches, pagination) | ✅ |
| | POST .../branches | CreateBranchResponse (flat) | ✅ | 없음 | ✅ |
| | PATCH .../branches/{branch} | gin.H flat | ✅ | 없음 | ✅ |
| | DELETE .../branches/{branch} | gin.H flat | ✅ | 없음 | ✅ |
| | GET .../branches/validate/*name | gin.H (success, is_available) | ✅ | 없음 | ✅ |
| **tree** | GET .../tree (recursive/non-recursive) | TreeEntriesRecursiveResponse / TreeEntriesResponse | ✅ | 없음 (tree / entries) | ✅ |
| **commits** | GET .../commits | CommitsResponse | ✅ | 없음 (results, pagination) | ✅ |
| | POST .../commits | MultiFileCommitResponse | ✅ | 없음 | ✅ |
| **tags** | GET .../tags | TagsResponse | ✅ | 없음 (results, pagination) | ✅ |
| | POST .../tags | CreateTagResponse (success, tag) | ✅ | 없음 | ✅ |
| | DELETE .../tags/{tag} | gin.H (success, tag) | ✅ | 없음 | ✅ |
| **blob** | GET .../blob (성공 시) | 바이너리/텍스트 본문 | — | N/A | ✅ |
| **diffs** | GET .../diff/commits, branches, file, commit, patch | DiffResponse 등 | ✅ | 없음 | ✅ |
| **merge** | POST .../merges | MergeResponse | ✅ | 없음 | ✅ |
| | GET .../merges/preview | MergePreviewResponse | ✅ | 없음 (can_merge 등) | ✅ |
| **overview** | GET .../overview, .../tree, .../blob, .../branches | RepositoryOverviewResponse 등 | ✅ | 없음 | ✅ |

**결과**: 성공 응답 **전 구간 flat, data 래퍼 없음, 적합**.

---

## 4. 스키마 적합도

| 스키마 파일 | 응답 구조체 | success 필드 | json "data" 래퍼 | 비고 |
|-------------|-------------|:------------:|:----------------:|------|
| branches/schema.go | CreateBranchResponse, GetBranchesResponse, UpdateBranchResponse | ✅ | 없음 | CreateBranchData는 내부용 |
| tree/schema.go | TreeEntriesResponse (entries), TreeEntriesRecursiveResponse (tree) | ✅ | 없음 | |
| overview/schema.go | RepositoryOverviewResponse, TreeOverviewResponse, BlobOverviewResponse, BranchesOverviewResponse, TreeWithCommitOverviewResponse | ✅ | 없음 | |
| merge/schema.go | MergeResponse, MergePreviewResponse | ✅ | 없음 | |
| commits/schema.go | CommitsResponse, MultiFileCommitResponse | ✅ | 없음 | |
| tags/schema.go | TagsResponse, CreateTagResponse, DeleteTagResponse | ✅ | 없음 | |
| repositories/schema.go | CreateRepositoryResponse | ✅ | 없음 | |
| diffs/schema.go | DiffResponse, SingleCommitDiffResponse, PaginatedDiffResponse | ✅ | 없음 | |
| state/schema.go | RepositoryStateData | ✅ | 없음 (타입명만 Data) | |
| blob/schema.go | BlobErrorResponse | ✅ | 없음 | success 필드 추가 완료 |

**결과**: 스키마 **전 구간 규약 적합**.

---

## 5. OpenAPI 스펙 적합도

| 항목 | 상태 | 비고 |
|------|:----:|------|
| 성공 응답 스키마에 `data` 래퍼 | ✅ 없음 | CreateBranchResponse, UpdateBranch, DeleteBranch, Tree 등 flat으로 정리됨 |
| 에러 응답 스키마 (ErrorResponse) | ✅ | success: false, error 필수 |
| BlobErrorResponse | ✅ | success, error 필수 반영됨 |

---

## 6. 종합

| 항목 | 적합 | 비고 |
|------|:----:|------|
| 에러 응답 (success: false, error) | ✅ | 전 핸들러 준수 |
| 성공 응답 (success: true, flat) | ✅ | data 래퍼 제거 완료 |
| 스키마 정의 | ✅ | BlobErrorResponse에 success 추가 반영 |
| OpenAPI 스펙 | ✅ | flat 응답 및 에러 형식 반영 |

**최종**: **전체 API 규약 적합.**

---

## 7. 참조

- 규약 정의: `docs/api_response_contract.md`
- 분석 근거: `docs/api_response_format_analysis.md`
