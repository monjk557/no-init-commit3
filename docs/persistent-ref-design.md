# Persistent Ref 설계 문서

## 배경

### 현재 문제

Change Request(CR)에서 source commit을 DB에 `source_commit_sha`로 저장하고 있으나, 다음 상황에서 **object not found** 에러가 발생한다:

- **force-push**: source branch의 history가 rewrite되면 기존 commit이 unreachable → `git gc`가 삭제
- **브랜치 삭제 후 reopen**: branch ref 삭제 → commit unreachable → GC 삭제 → reopen 불가
- **DB-Git 동기화 불일치**: DB에 저장된 sha가 git object store에 없는 상태 발생

### 근본 원인

DB에 sha를 복제 저장하는 구조. Git 상태와 DB 상태가 분리되어 있어 force-push, branch 삭제, GC 등의 이벤트에서 둘이 어긋남.

## Git Ref 개념 정리

### Ref란

커밋 hash를 가리키는 포인터. `.git/refs/` 밑에 sha가 적힌 파일로 존재한다.

```
.git/refs/
├── heads/              ← 브랜치
│   ├── main            ← 내용: a1b2c3d
│   └── feat            ← 내용: d4e5f6a
├── tags/               ← 태그
│   └── v1.0            ← 내용: f7g8h9i
└── changerequests/     ← CR용 persistent ref (신규)
    └── 20/
        └── head        ← 내용: abc1234
```

브랜치, 태그, persistent ref 전부 ref다. **구조는 동일하고 namespace만 다르다.**

### Ref 종류별 차이

| ref 종류 | 가리키는 대상 | 움직이는 방식 | 관리 주체 |
|----------|-------------|-------------|----------|
| tag | 특정 커밋 하나 | 고정 (안 움직임) | 사용자 |
| branch | 해당 브랜치의 최신 커밋 | push 시 자동 이동 | 사용자 (push/delete/force-push) |
| CR ref | CR의 source 커밋 | CR 이벤트 시 시스템이 이동 | 시스템 전용 |

### Persistent Ref가 GC로부터 커밋을 보호하는 원리

`git gc`는 어떤 ref에서도 도달할 수 없는(unreachable) 객체만 삭제한다. persistent ref가 커밋을 가리키고 있으면 해당 커밋은 reachable이므로 GC 대상에서 제외된다.

```
force-push 전:  refs/changerequests/1/head → abc123
                refs/heads/feat            → abc123

force-push 후:  refs/changerequests/1/head → abc123  (유지 → GC 보호)
                refs/heads/feat            → def456  (변경됨)
```

## 설계: Persistent Ref로 통합

### 현재 구조 vs Persistent Ref 구조

| | 현재 구조 | persistent ref |
|---|---|---|
| source commit 저장 | DB `source_commit_sha` 필드 | `refs/changerequests/<id>/head` |
| push 시 업데이트 | DB UPDATE 쿼리 | `git update-ref` |
| diff/commits 계산 | DB에서 sha 읽어서 전달 | ref에서 직접 resolve |
| GC 보호 | 없음 | ref가 가리키므로 자동 보호 |
| force-push 후 | object not found | old commit 보호 + ref 업데이트 |
| 브랜치 삭제 후 reopen | 실패 | ref로 커밋 접근 가능 |
| 브랜치 복원 | 불가능 | ref에서 브랜치 재생성 가능 |
| 의존성 | DB + git 두 곳 동기화 필요 | git 단일 source of truth |

### Ref 라이프사이클

```
CR 생성     → git update-ref refs/changerequests/<id>/head <source_commit>
push (open) → git update-ref refs/changerequests/<id>/head <new_commit>
push (closed) → 업데이트 안 함 (CR이 닫혀있으므로)
reopen      → refs/changerequests/<id>/head를 source branch 최신 커밋으로 업데이트
CR 삭제     → git update-ref -d refs/changerequests/<id>/head → 자연스럽게 GC 대상
```

### Push Event 처리 흐름

```
push event 수신
→ push된 branch를 source_branch로 하는 open 상태 CR 조회
→ 해당 CR들에 대해 git update-ref refs/changerequests/<id>/head <new_commit>
```

기존 "push 시 DB source_commit_sha 업데이트" 로직을 `git update-ref`로 대체한다.

### Diff/Commits 계산 시

```
기존: DB에서 source_commit_sha 읽기 → git executor에 sha 전달
변경: git rev-parse refs/changerequests/<id>/head → 바로 사용
```

DB 조회 없이 git ref를 직접 source of truth로 사용.

## 업계 사례

GitHub는 `refs/pull/<id>/head`를 PR 생성 시 자동 생성하여 동일한 효과를 얻는다:

- 브랜치 삭제 후에도 PR 커밋 접근 가능
- force-push 후에도 old commit 보호
- "Restore branch" 버튼 = persistent ref에서 브랜치 재생성

## 해결되는 문제 목록

1. **force-push 후 object not found** — old commit이 ref로 보호되어 GC 대상에서 제외
2. **브랜치 삭제 후 reopen 실패** — 브랜치가 없어도 persistent ref로 커밋 접근 가능
3. **브랜치 복원(restore branch)** — persistent ref가 가리키는 커밋으로 브랜치 재생성 가능
4. **DB-Git 동기화 불일치** — git이 단일 source of truth, DB 복제 불필요
5. **고아 커밋 발생** — persistent ref가 커밋을 붙잡고 있어 unreachable 상태 방지
