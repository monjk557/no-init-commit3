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

## Worktree Workflow

워크트리에서 작업 후 정리할 때 **반드시 아래 순서를 지킬 것**:

1. 워크트리 안에서 커밋 완료
2. **메인 레포 경로로 먼저 이동** (`cd /Users/seungbaek/Desktop/CODE/collabops-git-executor`)
3. 그 다음 머지 + 워크트리 제거 + 브랜치 삭제

```bash
# 반드시 메인 레포로 이동한 뒤 실행
cd /Users/seungbaek/Desktop/CODE/collabops-git-executor
git merge <worktree-branch> --no-edit
git worktree remove .claude/worktrees/<name>
git branch -d <worktree-branch>
```

**절대 하지 말 것**: 워크트리 디렉토리 안에서 워크트리 제거/머지 시도 — CWD가 삭제된 경로를 가리키게 되어 이후 모든 git/파일 명령이 실패함.

---

*Remember: We build systems that last, scale, and evolve. Every solution should make the system more intelligent, more adaptive, and more capable of handling future challenges.*