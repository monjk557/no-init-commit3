# Claude Instructions for CollabOps Git Executor

## Development Philosophy

### Long-Term Solutions Only
When analyzing issues or proposing solutions for this codebase, **ALWAYS** focus exclusively on long-term, architectural solutions. Never suggest short-term improvements, quick fixes, or band-aid solutions.

**Rationale**: 
- Short-term fixes create technical debt and mask underlying architectural problems
- This codebase serves as critical infrastructure that must scale and evolve
- Band-aid solutions compound into maintenance nightmares
- Long-term thinking prevents recurring issues and builds robust systems

### Solution Characteristics

**What to ALWAYS provide:**
- ✅ Architectural redesigns that address root causes
- ✅ Scalable patterns that grow with system demands
- ✅ Self-healing and adaptive mechanisms
- ✅ Multi-phase implementation strategies (months/quarters)
- ✅ Comprehensive observability and metrics
- ✅ Resource coordination and intelligent management
- ✅ Future-proof designs that anticipate growth

**What to NEVER suggest:**
- ❌ Quick configuration changes
- ❌ Simple parameter adjustments
- ❌ Temporary workarounds
- ❌ "Try this for now" solutions
- ❌ Manual intervention procedures
- ❌ Point fixes without systemic thinking

### Analysis Approach

1. **Root Cause Analysis**: Always dig to fundamental architectural assumptions and design flaws
2. **System Thinking**: Consider how issues relate to overall system architecture
3. **Scalability Focus**: Ensure solutions work at 10x current scale
4. **Intelligence Integration**: Propose adaptive, learning systems where appropriate
5. **Resource Management**: Consider system-wide resource coordination

### Implementation Strategy

Every solution should include:
- Multi-phase timeline (typically 3-6 months)
- Dependencies and prerequisites
- Success metrics and observability
- Migration strategy from current state
- Risk mitigation approaches

## Code Quality Standards

### MANDATORY: Quality Checks After Every Code Change

**CRITICAL REQUIREMENT**: After ANY code modification (Edit, Write, or creation), you MUST immediately run:

```bash
# 1. Format code first
make format

# 2. Run go vet check
go vet ./...

# 3. Fix any errors before proceeding
```

**Workflow for EVERY code change:**

1. **Make code change** (Edit/Write file)
2. **IMMEDIATELY run `make format`** - fixes formatting issues
3. **IMMEDIATELY run `go vet ./...`** - catches compilation errors, unused variables, etc.
4. **If errors exist**: Fix them immediately, don't proceed to next change
5. **If clean**: Continue with next change or commit

**Before ANY commit:**
```bash
# Final verification
make format
go vet ./...
go test ./internal/merge
```

**Never ignore or defer go vet errors** - they indicate real issues that can cause:
- Compilation failures in CI
- Runtime bugs
- Unused variables masking real problems
- Import cycles
- Dead code

### Quality Gates

- ✅ **`go vet ./...` must pass** - zero errors allowed
- ✅ **`make format` must run clean** - consistent formatting
- ✅ **Core tests must pass** - `go test ./internal/merge`
- ✅ **No unused imports or variables** - clean code only

**This is non-negotiable for production-quality code.**

### Automated Quality Enforcement

**Integration with Development Workflow:**

Every response that includes code changes should follow this pattern:

1. **Make the code change**
2. **Auto-run quality checks:**
   ```bash
   make format && go vet ./...
   ```
3. **Report status**: "✅ Code quality checks passed" or "🔧 Fixed formatting/vet issues"
4. **If issues found**: Fix immediately and re-run checks
5. **Only then proceed** to next change or completion

**Error Response Template:**
When go vet fails, immediately:
```bash
# Fix the specific issues
# Re-run: make format && go vet ./...
# Verify: echo "✅ All quality checks now pass"
```

**Commit Readiness Checklist:**
```bash
make format              # ✅ Formatting standardized
go vet ./...            # ✅ No vet errors
go test ./internal/merge # ✅ Core functionality works
git status              # ✅ Ready to commit
```

## Examples

**Bad (Short-term):**
"Increase the timeout from 30s to 300s"

**Good (Long-term):**  
"Implement an adaptive timeout management system that scales based on repository size, network conditions, and system load, with predictive capabilities and self-healing mechanisms"

**Bad (Short-term):**
"Add retry logic for failed operations"

**Good (Long-term):**
"Design a distributed Git protocol optimization with chunked transfer, resume capability, and intelligent resource coordination across the entire system"

## Typed Handler Architecture (REST API)

Issue-2164 (Phase 0~2.14, 2026-05-03 완료) 에서 13 REST 도메인 / 45 endpoint
를 typed handler 로 일괄 마이그레이션했다. **신규 REST endpoint 는 반드시
이 패턴을 따라야 함**. 자세한 마이그레이션 가이드는
`docs/architecture/typed-handler-resume-guide.md`,
`docs/architecture/typed-handler-migration-plan.md` 참조.

LFS 도메인은 별도 트랙 — Git LFS 가 별도 프로토콜이라 OpenAPI 와 무관.

### 핵심 원칙

1. **handler 시그니처는 `func(*gin.Context) (RespT, error)`** — `c.JSON` 직접
   호출 금지. wrapper 가 결과/에러를 응답에 매핑.
2. **schema 는 runtime 응답과 정확히 일치해야 함** — orphan/aspirational
   schema 금지. 새 endpoint 는 named struct 응답을 schema.go 에 정의.
   ad-hoc `gin.H{...}` 응답 금지.
3. **모든 도메인은 `routes.go` 를 가짐** — `RegisterRoutes(g, h)` 가
   server.go (runtime) 와 specs/spec_routes.go (spec 생성) 양쪽에서 호출되어
   single source of truth 보장.

### 도메인 추가 레시피 (5 단계)

신규 endpoint 또는 새 도메인 추가 시:

```
internal/<domain>/
├── handler.go          # typed 시그니처 (Resp, error)
├── routes.go           # RegisterRoutes(g, h) — typed.RegisterX 호출
├── schema.go           # named Request/Response struct
└── handler_test.go     # adapt(handler.HandleX) 패턴
```

1. **handler.go** — 시그니처를 `func(*gin.Context) (RespT, error)` 로 정의.
   에러는 `typed.NewHTTPError(status, msg)` 또는 `&responseErr/Override{}` 로
   반환 (`c.JSON` 직접 호출 X).
2. **routes.go** — `RegisterRoutes(g gin.IRouter, h *Handler)` 함수에서
   `typed.RegisterX(g, path, tag, h.HandleY, opts...)` 호출. h 가 nil 이면
   spec-only 모드 (등록만, 호출 없음).
3. **server.go** — `<domain>.RegisterRoutes(group, handler)` 한 줄로 등록.
   `g.METHOD(path, h.HandleX)` 직접 호출 금지.
4. **specs/spec_routes.go** — `setupSpecRoutes` 의 마이그레이션된 도메인
   목록에 `<domain>.RegisterRoutes(apiGroup, nil)` 추가.
5. **테스트** — 같은 패키지에 `handler_typed_test.go` 추가하여
   `invokeTyped[T]` + `adapt[T]` 제네릭 헬퍼 정의 (이미 13 도메인에 있음 —
   복사하여 사용). router 등록은 `router.X(path, adapt(h.HandleY))` 패턴.

### typed.RegisterX 결정 트리

```
endpoint 의 method?
├── GET (body 없음)
│    ├── query string 무시 / handler 본문에서 c.Query 직접 → RegisterGet
│    └── 명시 query struct bind 원함 → RegisterGetWithQuery
├── POST/PUT/PATCH (body 있음)
│    ├── strict bind (gin validator 메시지 그대로 OK) → RegisterJSON
│    └── lenient bind (커스텀 친화 메시지 보존) → RegisterNoBody + WithRequestBody
├── POST/DELETE/PATCH (body 없음) → RegisterNoBody
└── binary/text streaming (raw bytes/text out) → RegisterRaw + contentType
```

### Option / archetype 선택

| 상황 | 도구 | 예시 도메인 |
|---|---|---|
| 정상 응답 status 200 외 (예: 201) | `WithStatus(http.StatusCreated)` | fork |
| sentinel 에러 → 다중 status 매핑 | `WithErrorMapper(func(err error)(int,any))` | fork, revert, tags |
| spec 에 다중 응답 status 명시 | `WithResponse(status, type, desc)` | fork |
| custom Content-Type | `WithContentType(ct)` | (LFS 향) |
| OpenAPI summary | `WithSummary("HandleX")` | **모든 typed endpoint 필수** |
| spec 에서 숨김 (런타임 유지) | `WithExcludeFromSpec()` | fork (deprecated 후보), LFS |
| OpenAPI deprecated 플래그 | `WithDeprecated()` | — |
| RegisterNoBody 인데 spec 에 body schema | `WithRequestBody(reflect.TypeOf(ReqT{}))` | repositories, context, revert, tags, commits, branches, merge, overview, diffs |

### 에러 처리 archetype

**단일 status (대부분의 케이스)** — `typed.NewHTTPError`:
```go
if err := h.pathValidator.ValidateFilePath(...); err != nil {
    return RespT{}, typed.NewHTTPError(http.StatusBadRequest,
        fmt.Sprintf("Invalid path: %v", err))
}
```
wrapper 가 자동으로 `gin.H{"success":false, "error": msg}` 응답.

**sentinel → 다중 status** — `WithErrorMapper`:
```go
// handler 본문
if err != nil { return RespT{}, err }  // raw

// routes.go 의 mapper
func myMapper(err error) (int, any) {
    var he *typed.HTTPError
    if errors.As(err, &he) {
        return he.Status, gin.H{"success": false, "error": he.Message}
    }
    switch {
    case errors.Is(err, ErrNotFound):  return 404, gin.H{...}
    case errors.Is(err, ErrConflict):  return 409, gin.H{...}
    }
    return 500, gin.H{"success": false, "error": err.Error()}
}
```
mapper 가 set 되어 있으면 wrapper 의 default `*HTTPError` 매핑은 적용 안 됨
→ mapper 안에서 `errors.As(&he)` 로 직접 처리해야 함.

**다중 필드 응답 (conflicts/code/reason 등) 또는 alternate success shape** —
`responseErr` (merge) / `responseOverride` (diffs) wrapper:
```go
type responseOverride struct {
    status int
    body   any
}
func (e *responseOverride) Error() string { return "response override" }

// handler
return RespT{}, &responseOverride{
    status: http.StatusConflict,
    body: gin.H{
        "success": false,
        "error": "...",
        "conflicts": [...],          // 다중 필드
        "resolution_options": {...}, // 다중 필드
    },
}

// mapper
func mapper(err error) (int, any) {
    var rich *responseOverride
    if errors.As(err, &rich) {
        return rich.status, rich.body  // 그대로
    }
    // ... typed.HTTPError 처리
}
```
**용도**: 단순 `{success, error}` 가 아닌 응답 (conflict 정보, error_code,
reason 필드 등) + 같은 200 OK 안에서 두 가지 다른 shape (예: paginated vs
flat).

### 테스트 헬퍼 (도메인 패키지마다 추가)

`internal/<domain>/handler_typed_test.go`:
```go
package <domain>

import (
    "errors"
    "net/http"
    "github.com/gin-gonic/gin"
    "github.com/collabops/git-executor/internal/api/typed"
)

// invokeTyped 는 typed handler 의 결과를 gin.Context 에 직접 쓴다 — wrapper
// + ErrorMapper (responseOverride 처리) mirror.
func invokeTyped[Resp any](c *gin.Context, fn func(*gin.Context) (Resp, error)) {
    resp, err := fn(c)
    if err != nil {
        // (responseOverride 처리는 도메인별로 추가)
        var he *typed.HTTPError
        if errors.As(err, &he) {
            c.JSON(he.Status, gin.H{"success": false, "error": he.Message})
            return
        }
        c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
        return
    }
    c.JSON(http.StatusOK, resp)
}

func adapt[Resp any](fn func(*gin.Context) (Resp, error)) gin.HandlerFunc {
    return func(c *gin.Context) { invokeTyped(c, fn) }
}
```

테스트에서 `router.X(path, h.HandleY)` → `router.X(path, adapt(h.HandleY))` 로
일괄 치환.

### 알려진 함정

1. **Summary 누락 → spec byte-equivalence 깨짐**: `WithSummary("HandleX")` 빠뜨리면
   spec 의 `summary` 가 빈 문자열. legacy AST 가 함수명을 summary 로 썼으므로
   동일 문자열 유지 필수.
2. **lenient JSON bind**: `RegisterJSON` 의 strict bind 대신 handler 본문
   에서 직접 `c.ShouldBindJSON(&req)` 후 친화 에러 메시지 (예:
   `"Invalid JSON: %v"`) 반환. `RegisterNoBody + WithRequestBody` 조합.
3. **gin.H ad-hoc 응답 → struct 승격**: 일부 legacy handler 가 named struct
   대신 `gin.H{...}` 로 ad-hoc 응답. typed 시그니처는 named struct 강제.
   `internal/<domain>/schema.go` 에 새 struct 정의 (예: tags
   `DeleteTagResponse`, branches `ResolveBranchResponse`/`DeleteBranchResponse`).
4. **schema-runtime 불일치 발견 시**: orphan/aspirational schema 가 런타임과
   다르면 **schema 수정** 이 정직한 fix (tags Phase 2.7,
   branches Phase 2.12 archetype). runtime 변경은 외부 client 영향 위험.
5. **legacy `getTagFromPath` substring 매칭 호환**: 일부 path 가 substring
   순서로 의도치 않은 tag 를 반환 (예: `/merges/diff` → "Diff" 아닌 substring
   매칭). byte-equivalence 보존하려면 per-endpoint tag 명시 (overview,
   merge, diffs).
6. **gin `*name` wildcard**: `c.Param("name")` 이 leading `/` 를 포함.
   handler 본문에서 `strings.TrimPrefix(name, "/")` 패턴 유지.
7. **RegisterRoutes 가 nil handler 받음**: spec-only 모드. type inference
   실패 방지 패턴:
   ```go
   var op func(*gin.Context) (Resp, error)
   if h != nil { op = h.HandleX }
   typed.RegisterGet(g, "/path", tag, op, ...)
   ```
8. **RegisterRaw 의 spec 처리**: text/binary 응답은 typed wrapper 우회.
   `RegisterRaw(g, method, path, tag, gin.HandlerFunc, contentType, opts...)`
   로 등록하면 spec 에는 `Content-Type` 만 노출되고 schema 없음 (예: diffs
   `HandlePatch` text/plain).

## CR 워크플로우 (Code Merge Methodology)

이 레포에 변경사항을 합칠 때는 **항상 아래 5단계를 따른다**. main에 직접 commit/push/merge 금지 — 작업은 worktree + topic 브랜치에서만.

**`co` CLI 는 사용하지 않는다.** 이슈 생성 / CR 생성 / bot attribution 모두 CLI 로 하지 않는다. 작업을 끝내면 **채팅으로 브랜치·제목·본문을 보고**하고, CR 은 사람이 직접 띄운다.

### 1. 최신 main 에서 워크트리 + 브랜치 동시 생성

작업은 항상 격리된 worktree 에서 — 메인 워킹 디렉터리는 그대로 두고 `.claude/worktrees/<name>` 하위에서 진행한다.

```bash
git fetch origin main
git worktree add -b <type>/<slug> \
  .claude/worktrees/<slug> origin/main
cd .claude/worktrees/<slug>
```

- `worktree add -b`로 새 브랜치를 같이 만들어 main에 커밋이 유입되지 않게 한다.
- 브랜치 네이밍: `<type>/<slug>`.
  - 타입: `feat | fix | refactor | docs | test | chore | perf | ci`
  - slug: 짧은 케밥-케이스 요약 (3~5단어)
  - 예: `feat/http-clone-resume`, `fix/merge-lock-release`, `docs/cr-workflow-claude-md`
  - 사용자가 이슈 번호를 준 경우에만 앞에 붙인다: `fix/6414-lazy-redis-events-client`. 이슈를 새로 만들지는 않는다.

### 2. 작업 + 커밋

작업 후 **반드시** 다음 quality gate 를 모두 통과시킨 뒤 커밋한다 ("Code Quality Standards" 섹션과 동일):

```bash
make format               # ✅ 포맷 정리
go vet ./...              # ✅ vet 0 에러
go test ./internal/merge  # ✅ 코어 테스트 통과
make specs                # ✅ 핸들러/스키마/라우트 변경 시 OpenAPI 스펙 동기화
```

핸들러 / 스키마 / 라우트가 바뀌었다면 `make specs` 결과물(OpenAPI YAML)도 같은 커밋에 함께 포함시킨다.

커밋 메시지 형식 (Conventional Commits):

```
<type>: <한 줄 요약>

<선택적 본문>
```

타입: `feat | fix | refactor | docs | test | chore | perf | ci`.

**커밋은 항상 새로 쌓는다 — `git commit --amend` 금지.** 수정이 필요하면 새 커밋을 추가한다. main에 직접 커밋도 금지.

### 3. 브랜치를 remote 에 push

**main 과 upstream tracking 은 건드리지 않고, topic 브랜치만 push 한다.**

```bash
git branch --unset-upstream          # worktree add 가 자동으로 박은 origin/main tracking 제거
git push origin <type>/<slug>        # -u 없이, 현재 브랜치만
```

- `git worktree add -b <topic> <path> origin/main` 은 새 브랜치를 만들면서 자동으로 `origin/main` 으로 tracking 을 박는다 → push 직전에 반드시 `git branch --unset-upstream` 으로 해제.
- `-u` (`--set-upstream`) 플래그 사용 금지 — push 가 다시 tracking 을 박지 않도록.
- `git push --all`, `git push origin main` 같은 명령 절대 금지.
- main 을 직접 push 하지 않는다 (CR 머지로만 main 업데이트).
- 이미 push 된 브랜치를 rebase 후 다시 올릴 때만 `--force-with-lease` 허용 (`--force` 금지). 다른 사람 커밋 덮어쓰기 방지.

### 4. 채팅으로 작업 완료 보고 (CR 은 사람이 직접 띄운다)

**`co cr create` 를 실행하지 않는다.** 변경요청에 필요한 작업을 모두 끝냈으면 채팅에 아래 형식으로 출력하고 끝낸다 — 사람이 그대로 복사해 CR 을 만든다.

```markdown
**작업 완료 위치**
- 브랜치: `<type>/<slug>`
- 워크트리: `.claude/worktrees/<slug>` (정리 완료 / 유지 중)
- push: `origin/<type>/<slug>` ✅

**제목**
<국문 한 줄 제목 — 아래 5단계 규칙 준수>

**본문**
## 문제 상황
- ...

## 원인
- ...

## 수정
- ...
```

- 제목 / 본문은 5단계의 Title / Description 규칙을 그대로 따른다.
- 본문은 사용자가 복사해서 붙여넣을 수 있게 **마크다운 원문 그대로** 출력한다.
- `🤖 [BETA] ` 접두사나 attribution footer 는 붙이지 않는다 — 사람이 CR 을 만들기 때문.
- 커밋 SHA / 변경 파일 요약을 덧붙이면 좋지만, 본문 안이 아니라 보고 블록 아래에 따로 적는다.

### 5. Title / Description 규칙

**Title (국문, 포괄적 — 한 줄에 변경의 핵심을 담는다)**:

```
HTTP clone 동시성 처리에서 dangling lock 회수 보강
```

- 영문 prefix(`feat:` 등) 는 커밋 메시지에서만 쓰고, CR 제목에는 붙이지 않는다.
- "수정", "추가", "정리", "보강" 같은 동작 동사로 끝맺는다.
- 50 자 내외, 너무 길지 않게.

**Description (불릿, 3 섹션)**:

```markdown
## 문제 상황

- HTTP clone 동시 요청에서 일부 워커가 stale lock 을 잡고 풀지 않음
- 같은 repository_key 로 다른 작업이 들어오면 timeout 후 실패

## 원인

- `internal/merge/lock.go` 의 lock release 가 panic 경로에서 누락
- 컨텍스트 취소 시 정리 hook 이 등록되지 않아 lock 이 GC 시점까지 유지

## 수정

- defer 기반 lock release 보강 + recover 핸들러 추가
- ctx.Done() 에 unlock 등록, lock 보유자 식별을 위한 owner ID 부여
```

- 모든 섹션은 불릿. 산문 금지.
- "왜 이렇게 했는지" (원인 + 트레이드오프) 를 코드보다 길지 않게.
- 스크린샷 / 로그가 있으면 그 아래 별도 섹션으로 첨부.
- 사용자가 이슈 번호를 준 경우에만 첫 줄에 `Closes Issue-<key>` 명시.

### 6. 보고 후 워크트리 정리

push + 채팅 보고까지 끝났으면 로컬 worktree 는 정리한다. **반드시 메인 레포 경로로 먼저 이동한 뒤** 실행 — worktree 디렉토리 안에서 정리하면 CWD 가 삭제된 경로를 가리켜 이후 모든 git / 파일 명령이 실패한다.

```bash
# 반드시 메인 레포로 이동한 뒤 실행
cd /Users/seungbaek/Desktop/code/collabops-git-executor
git worktree remove .claude/worktrees/<slug>
# 로컬 topic 브랜치는 삭제하지 않는다 — CR 머지 전까지 남겨둔다
```

머지 후에는:

```bash
git fetch origin --prune                 # remote 에서 사라진 머지된 브랜치 정리
git switch main && git pull --ff-only
```

**절대 하지 말 것**:

- 워크트리 디렉토리 안에서 worktree remove / merge 시도.
- 로컬에서 `git merge <topic>` 으로 main 에 직접 머지 (CR 로만 머지).

## Git ground rules

- main 에 직접 commit / push / merge 금지 (항상 worktree + topic 브랜치 + CR).
- `co` CLI 실행 금지 — 이슈/CR 생성은 사람이 한다. 에이전트는 채팅 보고까지만.
- `--no-verify`, `git push --force`, `git commit --amend` 금지. 재 push 가 필요하면 `--force-with-lease` 만 사용.
- 시크릿(.env, 토큰, GPG 키) 은 절대 커밋 안 함.
- 핸들러 / 스키마 / 라우트 변경 시 `make specs` 결과물(OpenAPI 스펙) 도 함께 커밋.

---

*Remember: We build systems that last, scale, and evolve. Every solution should make the system more intelligent, more adaptive, and more capable of handling future challenges.*