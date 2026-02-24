# Git check-ref-format 규칙 정리

> 출처: `git check-ref-format` (git documentation)  
> 적용 위치: `internal/branches/validator.go`, `internal/branches/handler.go`

---

## 금지 규칙 (Forbidden Rules)

### 1. 금지 문자

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

→ 정규식: `^[^\x00-\x20\x7f~^:?*\[\\]+$`

---

### 2. 금지 패턴

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

---

### 3. 예약어 (Reserved Names)

| 이름 | 설명 |
|---|---|
| `HEAD` | 현재 브랜치 포인터 |
| `ORIG_HEAD` | 이전 HEAD (merge/reset 이전) |
| `FETCH_HEAD` | 마지막 fetch 결과 |
| `MERGE_HEAD` | 진행 중인 merge의 대상 commit |

→ 대소문자 무관 (case-insensitive)

---

## 허용 예시

```
main
feature/login
hotfix/v3.0.1          ✅ dot 포함 허용
release/2024-01-15     ✅ hyphen 포함 허용
feat/JIRA-123/fix      ✅ 다중 slash 허용
v1.0.0                 ✅
```
## 거부 예시

```
.hidden                ❌ dot으로 시작
branch.               ❌ dot으로 끝남
branch..name           ❌ 연속 dot
branch//name           ❌ 연속 slash
branch name            ❌ 공백 포함
branch~1               ❌ ~ 포함
HEAD                   ❌ 예약어
feat@{yesterday}       ❌ @{ 포함
my-branch.lock         ❌ .lock suffix
a:b                    ❌ 콜론 포함
```

---

## 구현 검증 체크리스트

현재 `internal/branches/validator.go` 구현 기준:

- [x] 빈 이름 거부
- [x] 예약어 거부 (HEAD, ORIG_HEAD, FETCH_HEAD, MERGE_HEAD)
- [x] leading/trailing dot 거부
- [x] leading/trailing slash 거부
- [x] 연속 slash (`//`) 거부
- [x] 연속 dot (`..`) 거부
- [x] 공백 거부
- [x] `@{` 시퀀스 거부
- [x] 단독 `@` 거부
- [x] `.lock` suffix 거부
- [x] 금지 문자 blocklist 정규식 (`^[^\x00-\x20\x7f~^:?*\[\\]+$`)

---

## 참고

- [git-check-ref-format documentation](https://git-scm.com/docs/git-check-ref-format)
- Git은 유니코드 및 기타 프린터블 문자를 브랜치명에 허용하지만,  
  호환성을 위해 `[a-zA-Z0-9._/-]` 범위 내 사용을 권장
