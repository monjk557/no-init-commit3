# CollabOps Git Executor Runtime Policies

> **목적**: source_ref/target_ref 해석 정책을 명확히 정의하여 API 동작의 일관성 보장
> **관련 문서**: `docs/test_guides/test_cases.md` - 0.1 Ref Input Contract

---

## 1. Overview

CollabOps Git Executor는 REST API를 통해 Git 작업을 수행합니다.
`source_ref`, `target_ref` 같은 ref 입력이 들어왔을 때, 이를 어떻게 해석하고 어떤 에러를 반환할지 명확한 정책이 필요합니다.

---

## 2. Core Principles

1. **source_ref는 branch, tag, commit SHA를 모두 허용한다.**
2. **CollabOps는 ambiguity를 에러로 취급하지 않는다.**
3. **해석 가능한 경우, 가장 보수적인(ref 우선) 방식으로 resolve한다.**
4. **동일 입력은 환경과 시점에 관계없이 동일한 결과를 낸다.**

---

## 3. Ref Resolution Order (⭐ 핵심)

```
1. Branch / Tag name resolution
2. Full commit SHA (40 hex)
3. Short commit SHA (>=7 hex)
```

- 첫 단계에서 성공하면 다음 단계는 시도하지 않는다.
- Branch와 Tag가 같은 이름일 경우 **Branch 우선** (Branch-First Policy)

---

## 4. Branch-First Policy

### 적용 상황
- 입력이 문자열이고
- branch/tag 이름으로 해석 가능성이 있는 경우

### 정책
- **SHA처럼 보이더라도 branch/tag를 우선한다.**
- resolve 실패 시 → `404 NOT FOUND`

### 예시

| Input | Reasoning | Result |
|-------|-----------|--------|
| `xyz123` | Valid branch name candidate | 404 (ref not found) |
| `deadbeef` | Valid branch name candidate | 404 (ref not found) |
| `feature-1` | Explicit branch name | 404 (ref not found) |
| `deadbee` | sha-like branch가 실제로 존재하면 | ✅ 해당 branch로 resolve |

---

## 5. Commit SHA Handling

### Full SHA (40자리)
- 정확히 40자리 hex
- branch/tag로 먼저 resolve 시도
- 실패 시 commit lookup
- commit도 없으면 → `404 OBJECT_NOT_FOUND`

### Short SHA (7-39자리)
- 최소 7자리 hex
- branch/tag 실패 후 commit lookup
- 여러 commit과 매칭되면 → `400 AMBIGUOUS_REF`
- 실패 시 → `404 OBJECT_NOT_FOUND`

---

## 6. Malformed Input Definition (400의 기준)

> ❗ **중요**: malformed는 "해석 불가"가 아니라 "형식 위반"이다

### 400을 반환하는 경우 (명확한 형식 오류)

| 케이스 | 예시 | error.code |
|--------|------|------------|
| 공백/개행 포함 | `"main branch"`, `"main\n"` | `INVALID_REF` |
| 금지 문자 포함 | `: .. ~ ^ @ { }` | `INVALID_REF` 또는 `UNSUPPORTED_REF` |
| null byte / control character | `"main\x00"` | `INVALID_REF` |
| rev-parse 표현식 | `main^`, `main~1`, `@{-1}` | `UNSUPPORTED_REF` |
| tree-ish:path 형식 | `main:README.md` | `UNSUPPORTED_REF` |
| 로컬 state ref | `FETCH_HEAD`, `ORIG_HEAD` | `UNSUPPORTED_REF` |

### 400을 반환하지 않는 경우 (해석 시도 → 404)

| 케이스 | 이유 |
|--------|------|
| `xyz123` | branch 이름으로 유효 |
| `deadbeef` | branch 이름으로 유효 |
| 짧은 hex 문자열 | branch로 해석 시도 가능 |

---

## 7. Error Code Mapping

| 상황 | HTTP Status | error.code |
|------|-------------|------------|
| 형식 위반 (공백, 금지 문자 등) | **400** | `INVALID_REF` |
| 지원하지 않는 형식 (rev-parse 등) | **400** | `UNSUPPORTED_REF` |
| 여러 commit 매칭 (short SHA) | **400** | `AMBIGUOUS_REF` |
| Branch/Tag 없음 | **404** | `REF_NOT_FOUND` |
| Commit SHA 없음 | **404** | `OBJECT_NOT_FOUND` |
| Repository 없음 | **404** | `REPO_NOT_FOUND` |
| 권한 없음 | **403** | `FORBIDDEN` |
| Git backend failure | **502/503** | `INTERNAL_ERROR` |

---

## 8. Implementation Details

### ResolveReference 함수 흐름

```
validateRefFormat(ref)
    ↓ (형식 오류면 400)
isHeadRef(ref)?
    ↓ yes → resolve HEAD
looksLikeSHA(ref)?
    ↓ no → try branch/tag → 실패 시 404
    ↓ yes → try branch/tag first (branch-first policy)
              ↓ 실패 → try SHA resolve
                        ↓ 실패 → 404
```

### Branch와 Tag 이름 충돌 시

```
입력: "release" (branch와 tag 둘 다 존재)
정책: Branch 우선
결과: refs/heads/release로 resolve
```

---

## 9. Test Cases Reference

이 정책을 검증하는 테스트 케이스는 다음 파일에 정의:

- `internal/merge/ref_resolution_test.go`
- `docs/test_guides/test_cases.md` - Section 1.1

### 핵심 테스트

| 테스트 | 기대 결과 |
|--------|----------|
| `Test_MergePreview_DeletedSourceBranch_Returns404` | 404 |
| `Test_MergePreview_NonExistentSHA_Returns404_OBJECT_NOT_FOUND` | 404 |
| `Test_MergePreview_ShortSHA_ReturnsSuccess` | 200 |
| `Test_MergePreview_RefContainsWhitespace_Returns400` | 400 |
| `Test_MergePreview_RevParseExpression_Returns400` | 400 |

---

## 10. Changelog

| 날짜 | 변경 내용 |
|------|----------|
| 2026-01-27 | 초기 정책 문서 작성. Branch-First Policy 명시. |
