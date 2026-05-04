# Typed Handler Migration — 세션 이어가기 가이드 (Issue-2164)

이 문서는 typed handler architecture 마이그레이션이 도중에 멈췄을 때 새 세션
(혹은 다른 작업자) 이 컨텍스트 없이 이어가기 위한 단일 source.

전체 설계: [`typed-handler-migration-plan.md`](./typed-handler-migration-plan.md)
이슈: collabops Issue-2164 (Issue-2020 의 sub)

---

## 1. 현재 상태 (2026-05-03)

**브랜치**: `feat/typed-handler-architecture` (main 기준)

**commit log (시간순)**:

```
82e0164 docs(architecture): typed handler migration plan (Issue-2164)
ec27cf5 docs(architecture): Phase 0+1+2 단일 CR 로 scope 확정
cf17b05 feat(api/typed): Phase 0.1 — Registry + Endpoint data model
13268ad feat(api/typed): Phase 0.2 — EndpointOption + With* helpers
b4c0789 feat(api/typed): Phase 0.3 — wrapper helpers + path resolution
464fd7d feat(api/typed): Phase 0.4 — Register* generic functions
723c28f feat(specs): Phase 1 — generate.go registry-first hybrid mode
f63bfd2 feat(api/typed,specs): Phase 2 prep — registry swap helper + spec route shell
d361718 feat(api/typed): HTTPError type + default mapAndWriteError 매핑
ceaedc6 feat(state): Phase 2.1 — typed handler migration (state, 1 endpoint)
79c65d3 feat(repositories): Phase 2.2 — typed handler migration (repository, lenient JSON bind archetype)
150bc77 feat(fork): Phase 2.3 — typed migration + WithExcludeFromSpec
5b1c78a feat(langstats): Phase 2.4 — typed handler migration
605bf93 feat(context): Phase 2.5 — typed handler migration
4140dea feat(revert): Phase 2.6 — typed handler migration
41f2222 feat(tags): Phase 2.7 — typed handler migration (4 endpoints)
78b1bbf feat(tree): Phase 2.8 — typed handler migration (2 endpoints)
4d79737 feat(commits): Phase 2.9 — typed handler migration (4 endpoints)
4322148 feat(merge): Phase 2.10 — typed handler migration (3 endpoints, 2 receivers)
6cb67e6 feat(overview): Phase 2.11 — typed handler migration (6 endpoints)
65fc518 feat(branches): Phase 2.12 — typed handler migration (9 endpoints)
127b105 feat(diffs): Phase 2.13 — typed handler migration (9 endpoints)
```

**완료** (REST API 도메인 13/13, 45 endpoints):
- Phase 0 — `internal/api/typed/` 패키지 (4 파일 + 테스트, 90.3% coverage)
- Phase 1 — `specs/generate.go` registry-first hybrid (legacy switch 공존, byte-equivalent)
- Phase 2 prep — `SetDefaultRegistry` swap, `setupSpecRoutes` 스캐폴딩, `HTTPError`, `WithRequestBody`, `WithExcludeFromSpec`
- Phase 2.1-2.13 도메인 — state(1), repositories(1), fork(3), langstats(1), context(1), revert(1), tags(4), tree(2), commits(4), merge(3), overview(6), branches(9), diffs(9)

**별도 트랙 — LFS (9 endpoints)**:

LFS 도메인은 별도 PR/효과로 보류. 이유:
- Git LFS 는 별도 프로토콜 (Git LFS Specification) — OpenAPI REST API 와 무관
- error response shape 다름: `{message: ...}` vs 다른 도메인의 `{success, error}`
- 커스텀 Content-Type (`application/vnd.git-lfs+json`) 처리 필요
- storage upload/download (`PUT/GET /info/lfs/objects/storage/:oid`) 는 RAW binary streaming
- 현재 spec 에 거의 등장 안 함 — `/workspaces/{owner}/lfs/usage` 만 (AST scan 부정확)
- LFS 라우트가 dual-registered 됨 (`.git` suffix 대응) — 17 route 등록을 9 logical endpoint 로 정리해야

LFS 마이그레이션 시 권장 패턴:
- `RegisterRaw` for storage upload/download (이미 diffs 의 HandlePatch 에서 패턴 확립)
- `responseOverride` for `{message}` 응답 형태 보존 (diffs 에서 도입)
- 모든 endpoint 에 `WithExcludeFromSpec()` (LFS 는 OpenAPI 가 아니라 별도 spec)
- `.git` dual registration 은 routes.go 의 RegisterRoutes 를 `lfsGroup` / `dotGitLfsGroup` 양쪽에서 호출하면 됨

---

## (참고용 — 이력) 남은 도메인 (9)

| 순서 | 도메인 | endpoints | 노트 |
|---|---|---|---|
| 5 | context | 1 POST | quick |
| 6 | revert | 1 POST | quick |
| 7 | tags | 4 (GET/POST/POST bulk-delete/DELETE *tag) | wildcard |
| 8 | tree | 2 GET | query |
| 9 | commits | 4 (GET list/POST/GET compare/GET :sha) | |
| 10 | merge | 3 GET preview/GET diff/POST | 두 receiver (`*MergePreviewHandler`, `*MergeOperationsHandler`) |
| 11 | overview | 6 GET (+ 1 POST latest-commits) | 다수 |
| 12 | branches | 9 (다수 wildcards `*name`/`*ref`/`*branch`) | 가장 복잡 |
| 13 | diff | 9 (query/path 혼합) | |
| 14 | LFS | 9 (custom Content-Type, raw streaming) | RegisterRaw |

**남은 작업 (위 도메인 외)**:
- legacy switch (`requestSchemaRef`/`responseSchemaRef`/`getTagFromPath`) + AST `parseServerRoutes` 의 dead path 제거 cleanup commit
- 모든 도메인의 `internal/<domain>/handler_test.go` (또는 `internal/api/error_handling_test.go`/`performance_load_test.go`) 가 직접 `router.METHOD(handler)` 등록 시 update

---

## 2. 마이그레이션 레시피 (도메인 1개 기준)

각 도메인 마이그레이션은 다음 5단계를 따른다.

### Step 1 — handler 시그니처 typed 화

`internal/<domain>/handler.go` 의 각 `Handle*(c *gin.Context)` 함수를 다음 패턴 중 하나로 변경:

| 입력/출력 패턴 | typed 시그니처 | 어떤 Register* 와 짝 |
|---|---|---|
| body 없음, JSON resp | `func(*gin.Context) (RespT, error)` | `RegisterGet` / `RegisterNoBody` |
| body 있음 (strict bind), JSON resp | `func(*gin.Context, ReqT) (RespT, error)` | `RegisterJSON` |
| body 있음 (lenient bind: invalid 무시) | `func(*gin.Context) (RespT, error)` + 본문에서 `c.ShouldBindJSON` | `RegisterNoBody` + `WithRequestBody(reflect.TypeOf(ReqT{}))` |
| query struct bind | `func(*gin.Context, QueryT) (RespT, error)` | `RegisterGetWithQuery` |
| binary body/response | `gin.HandlerFunc` 그대로 | `RegisterRaw(..., contentType)` |

본문에서 `c.JSON(status, gin.H{...})` 호출은 **모두 제거**:
- 정상 경로: `return resp, nil`
- 단일 status 에러 (예: 400 path invalid): `return zero, typed.NewHTTPError(http.StatusBadRequest, "Invalid path: ...")`
- sentinel 에 따른 다중 status: `return zero, err` (raw error 반환) → `WithErrorMapper` 가 분류

`import` 추가:
```go
"github.com/collabops/git-executor/internal/api/typed"
```

### Step 2 — `internal/<domain>/routes.go` 신설

```go
package <domain>

import (
    "github.com/gin-gonic/gin"
    "github.com/collabops/git-executor/internal/api/typed"
)

const tag = "Repository State"  // OpenAPI tag (handler 그룹별)

// RegisterRoutes 는 <domain> 의 모든 endpoint 를 등록한다. h 가 nil 이면
// spec-only 모드 (등록만 수행, 호출 없음 — spec 생성 시).
func RegisterRoutes(g gin.IRouter, h *<DomainHandler>) {
    var (
        op1 func(*gin.Context) (Resp1, error)
        // ...
    )
    if h != nil {
        op1 = h.HandleOp1
        // ...
    }

    typed.RegisterGet(g, "/path", tag,
        op1,
        typed.WithSummary("HandleOp1"),  // legacy AST 의 HandlerName 과 일치
    )
    // ...
}
```

**중요 — Summary**: `WithSummary("HandleOriginalHandlerName")` 을 명시. 빠뜨리면 spec 의 `summary` 가 빈 문자열이 되어 byte-equivalence 가 어긋남. legacy AST 가 `HandleX` 함수명을 그대로 summary 로 썼으므로 동일 문자열을 유지.

### Step 3 — `internal/server/server.go` 라우트 등록 교체

```go
// Before
ownerRepoGroup.GET("/state", stateHandler.HandleGetRepositoryState)

// After
state.RegisterRoutes(ownerRepoGroup, stateHandler)
```

여러 라우트가 한 도메인이면 한 줄로 압축됨.

### Step 4 — `specs/spec_routes.go` 의 `setupSpecRoutes` 에 한 줄 추가

```go
import (
    // ...
    "github.com/collabops/git-executor/internal/<domain>"
)

func setupSpecRoutes(reg *typed.Registry) {
    // ...
    state.RegisterRoutes(apiGroup, nil)
    repositories.RegisterRoutes(apiGroup, nil)
    fork.RegisterRoutes(apiGroup, nil)
    <domain>.RegisterRoutes(apiGroup, nil)  // <-- 추가
    // ...
}
```

### Step 5 — `specs/generate.go` 의 legacy switch 에서 해당 도메인 case 삭제

`requestSchemaRef` / `responseSchemaRef` 의 case 중 마이그레이션된 path 들을 모두 제거. registry 가 이제 그 path 의 spec 을 만든다.

`getTagFromPath` 의 substring 매칭은 그대로 둬도 됨 (legacy fallback 만 사용).

### Step 6 — 테스트 업데이트

도메인 패키지 내부 테스트 (`internal/<domain>/handler_test.go`) 가 직접 `router.METHOD("/path", handler.HandleX)` 로 등록하면, 시그니처가 typed 로 바뀌어 컴파일 안 됨. 패턴:

```go
// Before
router := gin.New()
router.POST("/repos/:owner/:repo", handler.HandleCreateRepository)

// After
router := gin.New()
g := router.Group("/repos/:owner/:repo")
RegisterRoutes(g, handler)
```

`internal/api/error_handling_test.go` 와 `internal/api/performance_load_test.go` 도 핸들러 직접 등록 시 같은 패턴으로 수정.

### Step 7 — 검증 (단일 명령)

```bash
go build ./... \
  && go test -count=1 ./internal/<domain>/... ./internal/api/... ./internal/api/typed/... ./specs/... \
  && go vet ./... \
  && go run ./specs \
  && echo "✓ all gates green"
```

`gofmt -l ./internal/<domain> ./internal/api/typed ./specs` 가 빈 출력이면 format clean.

---

## 3. 패턴 카탈로그 (when-to-use)

### 3.1 Register* 결정 트리

```
endpoint 의 method?
├── GET (body 없음)
│    ├── query string 무시 / handler 본문에서 c.Query 직접 → RegisterGet
│    └── 명시 query struct bind 원함 → RegisterGetWithQuery
├── POST/PUT/PATCH (body 있음)
│    ├── strict bind (invalid → 400) → RegisterJSON
│    └── lenient bind (invalid → fallback Req) → RegisterNoBody + WithRequestBody
├── POST/DELETE/PATCH (body 없음) → RegisterNoBody
└── binary streaming (raw bytes in/out) → RegisterRaw + contentType
```

### 3.2 Option 결정

| 상황 | option |
|---|---|
| 정상 응답 status 200 외 (예: 201 Created) | `WithStatus(http.StatusCreated)` |
| sentinel 에러 → 다중 status 매핑 | `WithErrorMapper(func(err error)(int,any))` |
| spec 에 다중 응답 status 명시 | `WithResponse(status, respType, desc)` (반복 호출) |
| custom Content-Type (예: `application/vnd.git-lfs+json`) | `WithContentType(ct)` |
| OpenAPI summary | `WithSummary("HandleX")` (마이그레이션 시 byte-equivalent 위해 필수) |
| OpenAPI description | `WithDescription("...")` |
| deprecated/feature 빠진 endpoint 를 spec 에서만 숨김 (런타임 유지) | `WithExcludeFromSpec()` |
| OpenAPI deprecated 플래그 | `WithDeprecated()` (런타임은 그대로) |
| RegisterNoBody 인데 spec 에 body schema 노출 | `WithRequestBody(reflect.TypeOf(ReqT{}))` |

### 3.3 Error 패턴

**단일 status (대부분의 케이스)** — `typed.NewHTTPError`:
```go
if err := h.pathValidator.ValidateFilePath(...); err != nil {
    return RespT{}, typed.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Invalid path: %v", err))
}
```
wrapper 가 자동으로 `gin.H{"success":false, "error": msg}` 로 응답.

**sentinel → 다중 status** — `WithErrorMapper`:
```go
// handler 본문
if err != nil {
    return RespT{}, err  // raw 그대로 반환
}

// routes.go 의 mapper
func myMapper(err error) (int, any) {
    var he *typed.HTTPError
    if errors.As(err, &he) {
        return he.Status, gin.H{"success": false, "error": he.Message}
    }
    switch {
    case errors.Is(err, ErrNotFound):  return http.StatusNotFound, gin.H{"success": false, "error": err.Error()}
    case errors.Is(err, ErrConflict):  return http.StatusConflict, gin.H{"success": false, "error": err.Error()}
    }
    return http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()}
}
```

mapper 가 set 되어 있으면 wrapper 의 default HTTPError 매핑은 적용 안 됨 → mapper 안에서 `errors.As(&he)` 로 직접 처리해야 함.

---

## 4. byte-equivalence vs 의도된 drift fill

**registry 가 비어있을 때 (Phase 1)**: 출력은 baseline 과 byte-equivalent.

**도메인이 마이그레이션될 때 (Phase 2)**: 두 가지 변화 가능:

1. **byte-equivalent** — legacy switch 에 이미 있던 path. registry 가 동일한 spec 을 만들면 변화 없음. (state, repository 케이스).
2. **의도된 drift fill** — legacy switch 에 누락되어 있던 endpoint (issue #2164 의 24 response + 13 requestBody 누락). registry 가 채우면 spec 에 새 schema reference 가 등장. (fork 케이스 — drift 가 정확히 채워짐).
3. **의도된 spec 제거** — `WithExcludeFromSpec()` 로 hidden. (fork 케이스 — 4 endpoints 를 외부 spec 에서 숨김).

**스펙이 의도대로 변하는지 확인 명령**:
```bash
# baseline 비교 (Phase 1 시점 snapshot)
diff /tmp/baseline-spec.yaml specs/generated/collabops-git-executor.yaml | head -60
```

`/tmp/baseline-spec.yaml` 이 없으면 첫 commit 시점 baseline 으로 되돌려서 만들 것:
```bash
git stash
git checkout 723c28f  # Phase 1 마지막 commit
go run ./specs
cp specs/generated/collabops-git-executor.yaml /tmp/baseline-spec.yaml
git checkout feat/typed-handler-architecture
git stash pop
go run ./specs
```

---

## 5. 알려진 함정

1. **gofmt vs editor 자동 reformat**: `internal/api/typed/wrapper_test.go` / `options_test.go` / `registry_test.go` 는 doc comment 들여쓰기가 한 번 자동 변경됐었음. 의도된 변경. 다시 reformat 해도 무방.

2. **internal/diffs/classify.go 가 항상 `gofmt -l` 에 잡힘**: 본 마이그레이션과 무관한 pre-existing fmt drift. main 브랜치에도 있음. (commit 93cb8ea 가 한 번 정정해줌)

3. **타 패키지 테스트 실패 (pre-existing)**:
   - `internal/branches/handler_test.go` 의 10+ 테스트 — 환경 의존
   - `internal/blob/Test_Blob_LargeFile_Truncates` — 환경 의존
   - `internal/events/Test*RedisStream*` — Redis 미실행
   - `internal/tree/Test_Tree_Symlink*` — 환경 의존

   migration 검증 시 해당 패키지를 `./...` 전체로 돌리지 말고 마이그레이션한 도메인 + `./internal/api/...` + `./specs/...` 만 돌리는 게 빠름.

4. **`POST("")` (empty path)**: gin RouterGroup 의 base path 자체에 등록. `absolutePath` 가 빈 path 를 base 만 반환하도록 처리되어 있음 (`/repos/{owner}/{repo}/` trailing slash 회피).

5. **gin `*name` wildcard**: `c.Param("name")` 이 leading `/` 를 포함한다. handler 본문에서 `strings.TrimPrefix(name, "/")` 패턴 유지 (legacy 와 동일).

6. **lenient JSON bind** (RegisterJSON 의 strict 모드와 다름): handler 본문에서 직접 `c.ShouldBindJSON(&req)` 후 fallback. `RegisterNoBody` + `WithRequestBody` 조합 사용. 예시: `internal/repositories/handler.go:HandleCreateRepository`.

7. **gin.H ad-hoc 응답 → struct 승격**: 일부 handler 가 named struct 대신 `gin.H{...}` 로 ad-hoc 응답. typed 시그니처는 named struct 를 강제하므로 schema 에 named struct 추가 필요. 발생 시 `internal/<domain>/schema.go` 에 새 struct 정의.

8. **RegisterRoutes 가 nil handler 받음**: spec-only 모드에서 nil handler 를 generic 함수에 넘기면 type inference 실패 가능. 패턴:
   ```go
   var op func(*gin.Context) (Resp, error)
   if h != nil { op = h.HandleX }
   typed.RegisterGet(g, "/path", tag, op, ...)
   ```
   `op` 의 명시 declared type 으로 generic Resp 가 추론됨.

---

## 6. 다음 — LFS 도메인 (별도 트랙)

위 §1 의 'LFS 마이그레이션 시 권장 패턴' 참조. LFS 는 별도 PR 로 처리.

---

## 7. 새 세션 시작 시 첫 명령

```bash
# 작업 디렉토리 + 브랜치 확인
cd /Users/jang-yeongjo/Desktop/code/collabops/collabops-git-executor
git branch --show-current  # → feat/typed-handler-architecture
git log --oneline main..HEAD  # 위 §1 의 commit log 와 일치 확인

# 도구 sanity
go version  # go1.26+
which co  # collabops CLI

# 진행상황 파악
cat docs/architecture/typed-handler-resume-guide.md  # 이 문서
cat docs/architecture/typed-handler-migration-plan.md  # 전체 plan

# 테스트 baseline
go test -count=1 ./internal/api/typed/... ./specs/... ./internal/state/... ./internal/repositories/... ./internal/api/...
go run ./specs

# 다음 도메인부터 §2 레시피 적용
```

---

## 8. 미결 결정 사항 (영조님 확인 필요)

- **fork 의 런타임 유지 여부**: 현재 `WithExcludeFromSpec` 으로 spec 만 숨김. 런타임은 그대로 서빙 중. 만약 fork 기능을 완전 제거하려면 server.go 의 `fork.RegisterRoutes(...)` 도 삭제 → handler / service / schema 도 dead code 정리. 별도 chore 트랙.

- **deprecated 후보 추가 식별**: 다른 도메인 중 spec 에서 빠져야 하는 endpoint 가 있는지 도메인별 마이그레이션 시 영조님 확인 필요.

- **CR 시점**: 모든 13 도메인 + cleanup 완료 후 `co cr create --bot --issue 2164` 로 CR 생성. 완료 전엔 push 해도 PR 안 만듦.
