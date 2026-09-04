# 004. Git Ref 규칙

> Git ref(브랜치/태그) 이름을 다루는 두 가지 국면을 하나로 묶은 문서다.
> - **1부 네이밍**: 새 브랜치/태그를 **만들 때** 어떤 이름을 허용할지
> - **2부 해석**: `source_ref`/`target_ref` 같은 입력을 REST API가 **조회할 때** 어떻게 매칭할지
>
> 원래 `collabops-git__GIT_CHECK_REF_FORMAT.md`와 `collabops-git__COLLABOPS_RUNTIME_POLICIES.md`로 나뉘어 있었는데, 둘 다 "ref 문자열을 어떻게 다루는가"라는 같은 도메인이라 합쳤다. `test_cases.md`의 "0.1 Ref Input Contract" 절도 2부에 흡수 통합돼 있다.

---

## 1부. Ref 네이밍 (생성 시 검증)

> 출처: `git check-ref-format` (git documentation)
> 적용 위치: `internal/branches/validator.go`, `internal/branches/handler.go`

### 1.1 금지 문자

| 문자 / 범위 | 설명 |
|---|---|
| `\x00`–`\x1f` | ASCII 제어 문자 (Control characters) |
| `\x7f` | DEL 문자 |
| ` ` (space) | 공백 |
| `~` | 틸드 |
| `^` | 캐럿 |
| `:` | 콜론 |
| `?` | 물음표 |
| `*` | 별표 |
| `[` | 여는 대괄호 |
| `\` | 백슬래시 |

정규식: `^[^\x00-\x20\x7f~^:?*\[\\]+$`

### 1.2 금지 패턴

| 패턴 | 설명 |
|---|---|
| `.`으로 시작 | path component가 `.`으로 시작 불가 |
| `.`으로 끝남 | trailing dot 불가 |
| `/`으로 시작 | leading slash 불가 |
| `/`으로 끝남 | trailing slash 불가 |
| `..` 포함 | 연속된 dot 불가 |
| `//` 포함 | 연속된 slash 불가 |
| `@{` 포함 | reflog 문법과 충돌 |
| 정확히 `@` | 단독 `@` 불가 |
| `.lock`으로 끝남 | lock 파일과 충돌 |

### 1.3 예약어 (대소문자 무관)

`HEAD`, `ORIG_HEAD`, `FETCH_HEAD`, `MERGE_HEAD`

### 1.4 허용/거부 예시

```
main                    ✅
feature/login           ✅
hotfix/v3.0.1           ✅ dot 포함 허용
release/2024-01-15      ✅ hyphen 포함 허용
feat/JIRA-123/fix       ✅ 다중 slash 허용
v1.0.0                  ✅

.hidden                 ❌ dot으로 시작
branch.                 ❌ dot으로 끝남
branch..name            ❌ 연속 dot
branch//name            ❌ 연속 slash
branch name             ❌ 공백 포함
branch~1                ❌ ~ 포함
HEAD                    ❌ 예약어
feat@{yesterday}        ❌ @{ 포함
my-branch.lock          ❌ .lock suffix
a:b                     ❌ 콜론 포함
```

### 1.5 참고

- [git-check-ref-format documentation](https://git-scm.com/docs/git-check-ref-format)
- Git은 유니코드 및 기타 프린터블 문자를 브랜치명에 허용하지만, 호환성을 위해 `[a-zA-Z0-9._/-]` 범위 내 사용을 권장한다.

---

## 2부. Ref 해석 (조회 시 매칭)

> **목적**: `source_ref`/`target_ref` 해석 정책을 명확히 정의해 API 동작의 일관성을 보장한다.

### 2.1 Core Principles

1. **source_ref는 branch, tag, commit SHA를 모두 허용한다.**
2. **CollabOps는 ambiguity를 에러로 취급하지 않는다.**
3. **해석 가능한 경우, 가장 보수적인(ref 우선) 방식으로 resolve한다.**
4. **동일 입력은 환경과 시점에 관계없이 동일한 결과를 낸다.**

### 2.2 Ref Resolution Order

```
1. Branch / Tag name resolution
2. Full commit SHA (40 hex)
3. Short commit SHA (>=7 hex)
```

첫 단계에서 성공하면 다음 단계는 시도하지 않는다. Branch와 Tag가 같은 이름일 경우 **Branch 우선**(Branch-First Policy)을 적용한다.

### 2.3 Branch-First Policy

SHA처럼 보이더라도 branch/tag를 우선한다. resolve 실패 시 → `404 NOT FOUND`.

| Input | Reasoning | Result |
|-------|-----------|--------|
| `xyz123` | Valid branch name candidate | 404 (ref not found) |
| `deadbeef` | Valid branch name candidate | 404 (ref not found) |
| `feature-1` | Explicit branch name | 404 (ref not found) |
| `deadbee` | sha-like branch가 실제로 존재하면 | 해당 branch로 resolve |

### 2.4 Commit SHA Handling

- **Full SHA(40자리)**: branch/tag로 먼저 resolve 시도 → 실패 시 commit lookup → 없으면 `404 OBJECT_NOT_FOUND`.
- **Short SHA(7-39자리)**: branch/tag 실패 후 commit lookup → 여러 commit과 매칭되면 `400 AMBIGUOUS_REF` → 실패 시 `404 OBJECT_NOT_FOUND`.

### 2.5 Ref Input Contract (입력별 지원 여부 고정)

> **원칙**: "지원 안 하는 ref는 400(`UNSUPPORTED_REF`)으로 명확히 거절한다", "존재하지 않는 ref는 404(`REF_NOT_FOUND`)로 거절한다". malformed는 "해석 불가"가 아니라 "형식 위반"을 뜻한다.

| 입력 | 지원 | 실패 코드 | 실패 reason (error.code) |
|------|------|----------|-------------------------|
| `main` (branch) | 지원 | 404 | `REF_NOT_FOUND` |
| `v1.0.0` (tag) | 지원 | 404 | `REF_NOT_FOUND` |
| `a1b2c3d4e5f6...` (full sha) | 지원 | 404 | `OBJECT_NOT_FOUND` |
| `a1b2c3d` (short sha, 7+) | 지원(권장) | 404 | `OBJECT_NOT_FOUND` |
| `xyz` (malformed sha) | 미지원 | 400 | `INVALID_REF` (길이/hex 아님) |
| `deadbee` (sha-like branch) | 지원 | - | **branch 우선**(tag 충돌 시만 `AMBIGUOUS_REF`) |
| `refs/heads/main` (full ref) | 미지원 | 400 | `UNSUPPORTED_REF` (내부 ref 직접 입력 금지) |
| `refs/tags/v1.0.0` (full tag ref) | 미지원 | 400 | `UNSUPPORTED_REF` (내부 ref 일관성) |
| `HEAD` | 정책상 미지원 | 400 | `UNSUPPORTED_REF` |
| `origin/main` | 미지원 | 400 | `UNSUPPORTED_REF` |
| `refs/pull/1/head` | GitHub 전용 | 400 | `UNSUPPORTED_REF` (타 호스팅에서는 거절) |
| `refs/merge-requests/1/head` | GitLab 전용 | 400 | `UNSUPPORTED_REF` (타 호스팅에서는 거절) |
| `@{-1}` | 미지원 | 400 | `UNSUPPORTED_REF` |
| `main^`, `main~1` | 미지원 | 400 | `UNSUPPORTED_REF` (rev-parse 표현식 금지) |
| `main:README.md` | 미지원 | 400 | `UNSUPPORTED_REF` (tree-ish:path 금지) |
| `FETCH_HEAD`, `ORIG_HEAD` | 미지원 | 400 | `UNSUPPORTED_REF` (로컬 state ref 금지) |
| `feature ` (공백/개행 포함) | 미지원 | 400 | `INVALID_REF` |
| `` (빈 문자열), `.`, `..` | 미지원 | 400 | `INVALID_REF` |
| null byte / control character (`"main\x00"`) | 미지원 | 400 | `INVALID_REF` |

**Ref Resolution 규칙**

1. **branch 우선**: sha-like 이름이 branch로 존재하면 branch로 해석한다 (sha 모양일 뿐, ambiguous가 아니다).
2. **tag dereference**: annotated tag는 commit으로 역참조하고, lightweight tag는 commit을 직접 가리킨다.
3. **명시적 거절**: 지원하지 않는 ref 문법은 400 + `UNSUPPORTED_REF`로 거절한다.
4. **404 vs 400**: 존재하지 않으면 404, 문법 오류면 400으로 구분한다.
5. **ambiguous는 branch vs tag 충돌에만 쓴다**: 같은 이름의 branch와 tag가 동시에 존재할 때만 400 + `AMBIGUOUS_REF`를 반환한다.

### 2.6 Error Code Mapping

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

### 2.7 Implementation Details

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

Branch와 Tag 이름이 충돌하면(`"release"`가 branch/tag 둘 다 존재) Branch를 우선해 `refs/heads/release`로 resolve한다.

### 2.8 Test Cases Reference

- `internal/merge/ref_resolution_test.go`
- `_archive/test_guides/test_cases.md` - Section 1.1 (도메인별 상세 케이스, 커버리지 추적용)

| 테스트 | 기대 결과 |
|--------|----------|
| `Test_MergePreview_DeletedSourceBranch_Returns404` | 404 |
| `Test_MergePreview_NonExistentSHA_Returns404_OBJECT_NOT_FOUND` | 404 |
| `Test_MergePreview_ShortSHA_ReturnsSuccess` | 200 |
| `Test_MergePreview_RefContainsWhitespace_Returns400` | 400 |
| `Test_MergePreview_RevParseExpression_Returns400` | 400 |

---

## Changelog

| 날짜 | 변경 내용 |
|------|----------|
| 2026-01-27 | 네이밍/해석 정책 각각 초기 작성. |
| 2026-09-03 | `test_guides__test_cases.md`의 "0.1 Ref Input Contract"를 해석 정책에 흡수 통합. |
| 2026-09-04 | 네이밍 규칙과 해석 정책을 "Git Ref 규칙" 하나로 병합. |
