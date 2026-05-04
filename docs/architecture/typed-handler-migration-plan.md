# 타입 핸들러 아키텍처 마이그레이션 계획 (Issue #2164)

## 개요

`specs/generate.go` 의 `requestSchemaRef`/`responseSchemaRef` 두 hardcoded switch 가 path operation 의 request/response 매핑을 담당하면서 handler 본문 (`var req X` / `c.JSON(_, Y{})`) 와 drift 누적. response content schema 누락 24/55 (44%), requestBody 누락 13/25 (52%) 의 결함을 구조적으로 제거하기 위해 handler 시그니처가 곧 schema 선언이 되는 typed handler 아키텍처를 도입한다. registry 는 등록 시점에 `(method, path, ReqType, RespType, tag, status, contentType, security, errorMappings)` 를 capture, generator 는 registry walk 으로 spec 을 만든다.

## 요구사항

- 모든 REST endpoint (55개 + LFS 8개 = 약 63개) 의 schema 가 handler 시그니처에서 자동 도출
- 기존 외부 동작 (status code, response body, error mapping) 100% 보존
- generated spec 의 의미 동등성 (drift 가 있던 endpoint 들은 의도적으로 채워지므로 byte-equivalent 가 아닌 "기존 spec ⊆ 새 spec" 형태)
- 도메인 단위 incremental migration (한 PR 에 모두 묶지 않고 점진 머지 가능)
- `make format && go vet ./... && go test ./...` 모두 통과
- `CLAUDE.md` 의 "Long-Term Solutions Only" 정책 준수 — 임시 우회 없음

---

## 1. 현 코드 전수 조사 결과

### 1.1 Handler 분류 (12 도메인 + LFS)

| 도메인 | Handler 패키지 | 등록 위치 | 메서드 수 | receiver 타입 |
|--------|-------------|----------|---------|---------------|
| repository | `internal/repositories` | `setupRestAPIRoutes` | 1 | `*RepositoryCreationHandler` |
| state | `internal/state` | 동상 | 1 | `*StateHandler` |
| branches | `internal/branches` | 동상 | 9 | `*BranchOperationsHandler` |
| tree | `internal/tree` | 동상 | 2 | `*TreeHandler` |
| commits | `internal/commits` | 동상 | 4 | `*CommitsHandler` |
| tags | `internal/tags` | 동상 | 4 | `*TagsHandler` |
| blob | `internal/blob` | 동상 | 1 | `*BlobHandler` (raw bytes) |
| diff | `internal/diffs` | 동상 | 9 | `*DiffHandler` |
| merge | `internal/merge` | 동상 | 3 | `*MergePreviewHandler`, `*MergeOperationsHandler` |
| revert | `internal/revert` | 동상 | 1 | `*RevertHandler` |
| overview | `internal/overview` | 동상 | 6 | `*RepositoryOverviewHandler` |
| context | `internal/context` | 동상 | 1 | `*ContextHandler` |
| langstats | `internal/langstats` | 동상 | 1 | `*Handler` |
| fork | `internal/fork` | 동상 | 3 | `*ForkHandler` |
| LFS | `internal/lfs` | `registerLFSRoutes`, `registerLFSStorageRoutes`, workspaces/usage | 9 | `*Handler` (mixed: JSON / 201 Created / raw stream) |
| **합계** | | | **약 55 + 9 = 63** | |

### 1.2 Input 패턴 분류

| 패턴 | 사례 | 비율 |
|------|------|------|
| `c.ShouldBindJSON(&req)` | branches POST, commits POST, merge POST, lfs Batch/Verify/Lock 등 | 22 |
| `c.Query()` 만 사용 (query-param GET) | branches GET (limit/skip/search), diff GET, blob GET, overview GET 다수 | 30+ |
| `c.Param()` 만 사용 (path-param GET/DELETE) | tags DELETE/*tag, branches DELETE/*branch | 8 |
| `c.Param()` + `c.Query()` 혼합 | diff/commits/:base/:head + ?context= | 다수 |
| no-body POST | branches/bulk-delete (body 있음, 위에 포함), forks/sync (body 없음) | 2 |
| raw bytes upload (`c.Request.Body` 직접 read) | LFS HandleStorageUpload (PUT) | 1 |
| raw bytes download (`io.Copy(c.Writer, f)`) | LFS HandleStorageDownload (GET), blob 의 binary content path | 2 |
| multipart/wildcard `*name` `*ref` `*branch` `*tag` | branches/validate/*name, branches/resolve/*ref, branches/*branch DELETE/PATCH, tags/*tag DELETE | 5 |

### 1.3 Output 패턴 분류

| 패턴 | 사례 |
|------|------|
| `c.JSON(200, struct)` 표준 | 대부분 (~50개) |
| `c.JSON(201, struct)` Created | `HandleForkRepository`, `HandleCreateLock` |
| `c.JSON(다양한 status, struct)` 에러 분기 | blob `getErrorStatusCode`, diff `DiffTooLargeError` 413, fork `ErrSourceNotFound` 404 등 |
| `c.JSON(http.StatusOK, gin.H{...})` (struct 아닌 ad-hoc map) | blob `is_lfs_pointer` 응답, fork info 일부, ready check 등 — **schema 없음** |
| `c.Data(status, contentType, []byte)` raw bytes | blob HandleBlobAPI binary path (`application/octet-stream`, image/* 등) |
| `c.Status(200) + io.Copy(c.Writer, f)` raw stream | LFS HandleStorageDownload |
| `c.Header("Content-Type", lfsMediaType) + c.JSON(...)` 커스텀 미디어타입 | LFS Batch/Verify/Locks (`application/vnd.git-lfs+json`) |

### 1.4 Special cases (registry 가 모델링해야 할 것)

1. **gin wildcard `*param`**: `branches/*branch`, `branches/validate/*name`. OpenAPI 에서는 leading slash 가 포함된 path param 으로 표현 어려움 → 마이그레이션 시점에 `routeToSpecPath` 처럼 `{param}` 으로 변환하되, description 에 wildcard 임을 명시
2. **다중 응답 코드**: 200 외에 201, 400, 404, 409, 413 등 — 현재 generator 는 200 + 400 만. registry 가 `[]ResponseSpec{Status, RespType, Description}` 다중 entry 지원
3. **gin.H ad-hoc 응답**: typed migration 의 강제 효과로 모든 ad-hoc 응답을 named struct 로 승격 (drift 자리 제거의 핵심)
4. **LFS 커스텀 Content-Type**: `application/vnd.git-lfs+json` — registry 의 `ContentType` 필드로 표현
5. **raw bytes endpoint**: `RegisterRaw` 로 등록, registry 는 `application/octet-stream` 등 contentType 만 보유 (schema 없음)
6. **wildcard path 구조 — `/info/lfs/...` 와 `.git suffix` 중복 등록**: 두 경로에 같은 handler — registry 에 동일 ReqType 가 다중 path 로 등록되어도 무방. spec 출력은 path 별 개별 operation
7. **`/workspaces/:owner/lfs/usage`**: `/repos/...` prefix 가 아닌 별도 prefix — 등록 시 absolute path 지원 필요

### 1.5 다중 receiver/handler 패키지 분리

merge 패키지는 `*MergePreviewHandler` 와 `*MergeOperationsHandler` 두 receiver 가 공존. typed registration 도 동일 패키지 내 두 receiver 모두 지원해야 함. 이는 generic + method value 패턴으로 자연 해결 (아래 §3.2 참고).

---

## 2. Registry Data Model

### 2.1 핵심 필드

```go
// Pseudocode — file: internal/api/typed/registry.go
type Endpoint struct {
    Method       string                 // "GET", "POST", ...
    Path         string                 // "/repos/{owner}/{repo}/branches", absolute path
    Tag          string                 // "Branches"
    OperationID  string                 // 생성 시 derive: handlerName.lower 또는 명시
    Summary      string                 // doc comment 첫 줄
    Description  string                 // doc comment 본문

    ReqType      reflect.Type           // nil 이면 no body
    ReqLocation  RequestLocation        // BodyJSON | Query | None | Multipart | RawBytes
    QueryParams  []ParamSpec            // GET endpoint 의 query params (struct tag 기반)

    PathParams   []ParamSpec            // owner/repo 외 추가 path param

    Responses    []ResponseSpec         // 다중 status 지원
    ContentType  string                 // 기본 "application/json", LFS 는 lfsMediaType

    Security     []string               // 기본 ["ApiKeyAuth"]
    Deprecated   bool
}

type RequestLocation int
const (
    LocationNone RequestLocation = iota
    LocationBodyJSON
    LocationQuery       // RegisterGetWithQuery 전용
    LocationMultipart
    LocationRawBytes
)

type ResponseSpec struct {
    Status      int
    RespType    reflect.Type           // nil 이면 빈 응답 (예: 200 OK no body)
    ContentType string                 // 기본 "application/json", raw 는 "application/octet-stream"
    Description string
    SchemaRef   string                 // ref 우선 사용 시 (predefined ErrorResponse 등)
}

type ParamSpec struct {
    Name        string
    Required    bool
    GoType      reflect.Type
    Description string
}
```

### 2.2 Registry 인터페이스

```go
// file: internal/api/typed/registry.go
type Registry struct {
    mu        sync.RWMutex
    endpoints []Endpoint
}

func DefaultRegistry() *Registry            // package singleton

func (r *Registry) Register(e Endpoint)
func (r *Registry) Walk(fn func(Endpoint))  // ordered (method asc, path asc)
func (r *Registry) Snapshot() []Endpoint    // copy for spec generator
```

generator 는 `internal/api/typed` 를 import 해 `Walk` 호출만으로 spec 생성 가능.

### 2.3 현재 generate.go 가 만드는 spec 정보의 표현 가능성 검증

| 현재 spec 요소 | registry 표현 |
|---------------|--------------|
| `paths.<path>.<method>.summary` | `Endpoint.Summary` (doc comment 또는 handler name fallback) |
| `paths.<path>.<method>.operationId` | `Endpoint.OperationID` |
| `paths.<path>.<method>.tags` | `Endpoint.Tag` |
| `parameters: Owner/Repo $ref` | `/repos/{owner}/{repo}` prefix 감지 시 자동 주입 (registry 가 path 기반 판정) |
| 추가 path param | `Endpoint.PathParams` |
| `requestBody.content.application/json.schema.$ref` | `Endpoint.ReqType` reflection → component schema |
| `responses.200.content.application/json.schema.$ref` | `Endpoint.Responses[i]` |
| `responses.400 $ref` | 모든 endpoint 에 default 로 `Endpoint.Responses` 에 ErrorResponse 추가 (Phase 0 의 error mapper 가 자동 주입) |
| `components.schemas.*` | registry walk 에서 모든 ReqType/RespType 의 reflect.Type 을 모은 후 재귀 reflection (현재 `parseSchemaFile` 로직 대체) |

**결론**: 현재 generator 의 모든 spec 출력은 registry 로 표현 가능. 추가로 다중 status, custom contentType, security override 등 현 generator 에 없는 정보까지 자연스럽게 capture.

---

## 3. Typed Handler API 설계

### 3.1 Register 함수군

```go
// file: internal/api/typed/register.go
package typed

// JSON body POST/PUT/PATCH
func RegisterJSON[Req any, Resp any](
    r gin.IRouter,
    method, path, tag string,
    handler func(*gin.Context, Req) (Resp, error),
    opts ...EndpointOption,
)

// Query-only GET (req 는 query struct 또는 None)
func RegisterGet[Resp any](
    r gin.IRouter,
    path, tag string,
    handler func(*gin.Context) (Resp, error),
    opts ...EndpointOption,
)

// Query struct 를 자동 bind 하는 GET (advanced)
func RegisterGetWithQuery[Query any, Resp any](
    r gin.IRouter,
    path, tag string,
    handler func(*gin.Context, Query) (Resp, error),
    opts ...EndpointOption,
)

// no-body method (DELETE, no-body POST)
func RegisterNoBody[Resp any](
    r gin.IRouter,
    method, path, tag string,
    handler func(*gin.Context) (Resp, error),
    opts ...EndpointOption,
)

// raw bytes (binary streaming) — JSON wrapper 없음
func RegisterRaw(
    r gin.IRouter,
    method, path, tag string,
    handler gin.HandlerFunc,
    contentType string,    // "application/octet-stream"
    opts ...EndpointOption,
)

// status override, response status, error mapper 등
type EndpointOption func(*Endpoint)
func WithStatus(code int) EndpointOption
func WithResponse(status int, respType reflect.Type, desc string) EndpointOption
func WithErrorMapper(mapper func(error) (int, any)) EndpointOption
func WithContentType(ct string) EndpointOption
func WithSummary(s string) EndpointOption
func WithDescription(s string) EndpointOption
func WithDeprecated() EndpointOption
```

내부 구현 (RegisterJSON 예시):

```go
func RegisterJSON[Req, Resp any](r gin.IRouter, method, path, tag string,
    handler func(*gin.Context, Req) (Resp, error), opts ...EndpointOption) {

    var zeroReq Req
    var zeroResp Resp
    ep := Endpoint{
        Method:      method,
        Path:        absolutePath(r, path),  // gin RouterGroup BasePath 추적
        Tag:         tag,
        ReqType:     reflect.TypeOf(zeroReq),
        ReqLocation: LocationBodyJSON,
        Responses: []ResponseSpec{{
            Status:      200,
            RespType:    reflect.TypeOf(zeroResp),
            ContentType: "application/json",
            Description: "Success",
        }},
        ContentType: "application/json",
    }
    for _, opt := range opts { opt(&ep) }
    DefaultRegistry().Register(ep)

    r.Handle(method, path, func(c *gin.Context) {
        var req Req
        if err := c.ShouldBindJSON(&req); err != nil {
            writeError(c, http.StatusBadRequest, err)
            return
        }
        resp, err := handler(c, req)
        if err != nil {
            mapAndWriteError(c, err, ep)
            return
        }
        c.JSON(deriveStatus(ep), resp)
    })
}
```

### 3.2 Receiver method 와의 결합 (Go generics + method value)

기존 receiver 메서드:
```go
func (h *BranchOperationsHandler) HandleBranchStats(c *gin.Context) { ... }
```

Typed 화 후:
```go
// signature 변경
func (h *BranchOperationsHandler) HandleBranchStats(c *gin.Context, req BranchStatsRequest) (BranchStatsResponse, error) { ... }

// 등록 (server.go)
typed.RegisterJSON(group, "POST", "/branches/stats", "Branches",
    branchesHandler.HandleBranchStats)
```

**핵심**: `branchesHandler.HandleBranchStats` 는 method value (closure) 로 `func(*gin.Context, BranchStatsRequest) (BranchStatsResponse, error)` 와 호환. Go generics 가 두 매개변수의 type 을 inferring → registry 에 자동 capture. 추가 wrapper 코드 불필요.

### 3.3 Error mapper

기존 코드의 error 분기 (`getErrorStatusCode` in blob, `errors.Is(err, ErrSourceNotFound)` in fork) 는 `WithErrorMapper` 로 옮긴다:

```go
typed.RegisterJSON(group, "POST", "/forks", "Repository Management",
    forkHandler.HandleForkRepository,
    typed.WithStatus(201),
    typed.WithErrorMapper(func(err error) (int, any) {
        switch {
        case errors.Is(err, fork.ErrSourceNotFound):
            return 404, ErrorResponse{Success: false, Error: err.Error()}
        case errors.Is(err, fork.ErrForkAlreadyExists):
            return 409, ErrorResponse{Success: false, Error: err.Error()}
        }
        return 500, ErrorResponse{Success: false, Error: err.Error()}
    }),
    typed.WithResponse(404, nil, "Source repo not found"),
    typed.WithResponse(409, nil, "Fork already exists"),
)
```

이 방식이 (1) handler 본문에서 status 분기 코드가 사라지고 (2) registry 에는 모든 가능 status 가 등록되어 spec 의 `responses` 가 풍부해지는 두 효과 동시 달성.

---

## 4. 아키텍처 변경 요약

| 변경 위치 | 변경 내용 |
|-----------|---------|
| `internal/api/typed/` (신규 디렉토리) | `register.go`, `registry.go`, `wrapper.go`, `errors.go`, `options.go`, `*_test.go` |
| `specs/generate.go` | `requestSchemaRef`/`responseSchemaRef` 삭제. `parseServerRoutes` AST 파싱 → `typed.DefaultRegistry().Walk` 로 교체. `parseAllSchemas` → registry 의 ReqType/RespType 들의 reflect.Type 재귀 walk 로 교체. `getTagFromPath` 삭제 |
| `internal/server/server.go` | `setupRestAPIRoutes` 의 모든 `ownerRepoGroup.METHOD(path, handler)` 호출을 `typed.RegisterJSON/Get/...` 로 교체 |
| 각 도메인 `internal/<domain>/handler.go` | handler 메서드 시그니처 typed 화. 본문은 가능한 그대로 유지하되 (1) `c.JSON(_, resp)` 제거 → `return resp, nil` (2) status 분기 → `WithErrorMapper` 로 추출 |
| 각 도메인 `internal/<domain>/schema.go` | 새로 named struct 가 필요한 ad-hoc 응답들 추가 (예: `BlobLFSPointerResponse`, `ForkInfoResponse` 등 — 이미 있는 것은 재사용) |

---

## 5. 구현 단계 (Phase 0 + 1 + 2)

### Phase 0 — Infrastructure (단일 PR, ~2-3일)

**목표**: typed wrapper 자체를 완성하고 unit test 만 통과. 어떤 handler 도 아직 마이그레이션 안 됨. 기존 코드는 100% 그대로 동작.

| Step | 파일 | 액션 | 위험 |
|------|------|------|------|
| 0.1 | `internal/api/typed/registry.go` | `Endpoint`, `ResponseSpec`, `ParamSpec`, `Registry`, `DefaultRegistry` 정의 | Low |
| 0.2 | `internal/api/typed/options.go` | `EndpointOption` + 모든 `With*` helper | Low |
| 0.3 | `internal/api/typed/wrapper.go` | `writeError`, `mapAndWriteError`, `deriveStatus`, `absolutePath` | Medium — gin RouterGroup BasePath 추적 |
| 0.4 | `internal/api/typed/register.go` | `RegisterJSON`, `RegisterGet`, `RegisterGetWithQuery`, `RegisterNoBody`, `RegisterRaw` | Medium — generic 시그니처 매칭 |
| 0.5 | `internal/api/typed/registry_test.go` | unit test: register → walk 으로 capture 검증, reflect.Type 동등성, ordering | High — TDD 핵심 |
| 0.6 | `internal/api/typed/register_test.go` | 각 Register* 에 대해 가짜 handler 등록 → httptest 로 라우트 동작 + registry capture 동시 검증 | High |
| 0.7 | `internal/api/typed/wrapper_test.go` | error mapper 동작, status override, JSON binding 실패 시 400 등 | High |

**완료 조건**: `go test ./internal/api/typed/...` 모두 green. `make format && go vet ./...` clean. 기존 `go test ./...` 전부 그대로 통과 (typed 패키지는 아직 import 되지 않음).

### Phase 1 — generate.go rewrite (단일 PR, ~2일)

**목표**: spec generator 가 registry 를 사용하도록 전환. 단, 이 시점에는 어떤 handler 도 typed 화 안 됨 → registry 가 비어있어 spec 도 비어있게 생성됨. 이를 막기 위해 **hybrid 모드** 운영.

#### Hybrid 전략

```go
// generate.go 의 새 로직 (pseudocode)
func generatePathsFromRoutes(routes []RouteInfo) string {
    registered := loadRegistryEndpoints()  // 마이그레이션된 endpoint 들
    registeredKey := func(method, path string) string { return method + " " + path }

    seen := make(map[string]bool)
    for _, ep := range registered {
        renderFromRegistry(ep)
        seen[registeredKey(ep.Method, ep.Path)] = true
    }
    for _, route := range routes {
        specPath, skip := routeToSpecPath(route.Path)
        if skip || seen[registeredKey(route.Method, specPath)] { continue }
        renderFromLegacySwitch(route)  // 기존 requestSchemaRef/responseSchemaRef 활용
    }
}
```

`registry` 를 로드하기 위해 generator 가 main 바이너리를 import 하기는 어려움 → `cmd/specgen` 또는 `specs/generate.go` 가 `internal/api/typed` 와 모든 도메인 패키지를 import 하고, handler 들은 `init()` 가 아닌 explicit 등록 함수로 구조화. 즉:

- 각 도메인 패키지에 `func RegisterRoutes(r gin.IRouter, h *Handler)` 추가 (server.go 가 호출)
- `specs/generate.go` 도 같은 `RegisterRoutes` 를 호출하지만 dummy handler instance + `gin.New()` 로. 등록만 하고 실제 server 는 시작 안 함
- 이렇게 하면 generator 와 server 가 **하나의 source of truth** (RegisterRoutes 함수) 를 공유

| Step | 파일 | 액션 | 위험 |
|------|------|------|------|
| 1.1 | `specs/generate.go` | `parseServerRoutes` 유지 (legacy 보충용), 새 함수 `loadRegistryEndpoints()` 추가 | Low |
| 1.2 | `specs/generate.go` | `requestSchemaRef`/`responseSchemaRef` 는 **legacy 모드용으로 유지** (Phase 2 끝에 삭제) | Medium |
| 1.3 | `specs/generate.go` | `generatePathsFromRoutes` 를 hybrid 로직으로 변경 | High — 결과 yaml 의 sort order/들여쓰기 byte-equivalent 유지 필수 |
| 1.4 | `specs/generate.go` | `generateSchemasFromStructs` 는 registry 의 reflect.Type 들을 union 한 set 기반으로 수정 (legacy schemaFiles 는 fallback) | High |
| 1.5 | `specs/generate.go` | `getTagFromPath` 는 hybrid 모드에서만 사용. registered endpoint 는 `Endpoint.Tag` 사용 | Low |
| 1.6 | `specs/generate_test.go` | golden test: 현재 yaml 과 비교 (Phase 2 이전이므로 byte-equivalent 여야 함) | High |
| 1.7 | `specs/generate.go` | dummy `gin.Engine` 으로 dummy handler 등록 시 nil pointer panic 방지 (handler instance 가 필요) — `New*Handler` 들이 cfg nil 에서 fail 하는 케이스 처리 | Medium |

**완료 조건**: `make specs generate` 결과가 현재 generated yaml 과 byte-equivalent (registry 비어있고 모두 legacy fallback 으로 처리). `go test ./specs/...` golden test green.

### Phase 2 — Handler migration (도메인별 PR, ~1.5-2주)

12개 도메인 + LFS = 13개 PR. 각 PR 은:
1. 도메인 schema 에 부족한 named struct 추가 (예: ad-hoc gin.H 응답을 struct 로 승격)
2. handler 메서드 시그니처 typed 화 + 본문 정리 (status 분기는 WithErrorMapper 로)
3. 각 도메인에 `RegisterRoutes(group, handler)` 함수 추가
4. `server.go` 의 해당 도메인 등록 부분을 `RegisterRoutes` 호출로 교체
5. `specs/generate.go` 도 같은 `RegisterRoutes` 호출 (specs 빌드는 server 와 다른 binary 임)
6. legacy switch 에서 해당 case 삭제 (점진적으로 switch 가 비어감)
7. golden spec test 갱신 — 이 PR 에서는 spec 이 의도적으로 더 풍부해짐 (drift 채워짐)

**도메인 우선순위 (의존성 적음 → 많음, drift 가 큰 것 우선)**:

| 순서 | 도메인 | 이유 |
|------|------|------|
| P2.1 | repository | endpoint 1개, 구조 단순. 마이그레이션 파일럿 |
| P2.2 | state | endpoint 1개. 두 번째 파일럿 |
| P2.3 | langstats | endpoint 1개 |
| P2.4 | context | endpoint 1개, POST body 있음 |
| P2.5 | revert | endpoint 1개, POST body 있음 |
| P2.6 | tags | endpoint 4개, wildcard `*tag` 첫 사례 |
| P2.7 | tree | endpoint 2개 |
| P2.8 | commits | endpoint 4개 |
| P2.9 | branches | endpoint 9개, wildcard 다수 (`*name`, `*ref`, `*branch`) — 가장 복잡 |
| P2.10 | merge | endpoint 3개, 두 receiver |
| P2.11 | overview | endpoint 6개 |
| P2.12 | diff | endpoint 9개, query/path 혼합, pagination |
| P2.13 | fork | endpoint 3개, 다중 status (201, 404, 409) — error mapper 검증 |
| P2.14 | LFS | endpoint 9개, custom Content-Type, raw streaming, 중복 path (.git suffix) — `RegisterRaw` 검증 |

**Phase 2 종료 직후 (P2.14 머지 후)**: legacy switch 두 개 삭제 + `parseServerRoutes` 의 AST 파싱 로직 삭제 + `getTagFromPath` 삭제. 마지막 cleanup PR.

---

## 6. PR 구조 결정

**결정**: **Phase 0+1+2 를 단일 CR 에서 관리** (영조님 결정).

브랜치: `feat/typed-handler-architecture`

이유:
- Phase 0 단독 머지 = dead code. Phase 1 단독 = registry 비어 있어 spec 깨짐. 셋이 entangled
- 외부 동작 보존이 PR 단위로 검증되므로 (`go test ./...` + golden spec), 단일 CR 로 한 번에 검증하는 게 review 단순
- hybrid 모드 (legacy switch + registry 공존) 도 불필요 — 단일 CR 안에서 한 번에 갈아끼움

**커밋 단위 (squash 자동 X — phase 별 보존)**:

```
docs(architecture): typed handler migration plan (Issue-2164)
feat(api/typed): Phase 0 — registry + options + wrapper + Register* infrastructure
feat(specs): Phase 1 — generate.go switches → registry walk
feat(<domain>): Phase 2.1 — repository typed migration
feat(<domain>): Phase 2.2 — state typed migration
... (각 도메인별 commit)
chore(specs): Phase 2 cleanup — legacy switch + AST parsing 제거
```

머지 시 영조님이 squash 여부 직접 결정. Phase 별 commit 가 보존되면 대형 CR 안에서도 review 의 시간순 흐름 유지 가능.

---

## 7. 회귀 검증 전략

### 7.1 spec 의미 동등성

**Phase 1 PR**: byte-equivalent (registry 비어있음 → legacy fallback 만 작동).

**Phase 2 도메인별 PR**: byte-equivalent 가 아니다. drift 가 채워짐. review 친화적으로 보이려면:

- 각 도메인 PR 에 `specs/generated/collabops-git-executor.yaml` diff 를 첨부
- 추가로 `specs/migration_diff.txt` 같은 보조 파일에 "이 PR 에서 채워지는 drift endpoint 목록" 을 자동 생성 (예: `POST /branches/stats: requestBody added (BranchStatsRequest), response 200 added (BranchStatsResponse)`)
- 이 보조 파일은 Phase 2 가 끝날 때 issue 본문의 24+13 endpoint 와 정확히 일치해야 함 — 일치 검증은 Phase 2 마지막 PR 의 acceptance criteria

### 7.2 외부 동작 보존

도메인별 PR 마다:

1. **기존 통합 테스트 (`internal/<domain>/*_test.go`) 를 손대지 않은 채 통과** — 외부 동작 변경 없음의 가장 강한 신호
2. **`internal/api/error_handling_test.go`, `internal/api/performance_load_test.go`** 통과 — typed wrapper 가 기존 error 형태 (`{"success": false, "error": "..."}`) 를 그대로 유지
3. **새 wrapper test**: `internal/api/typed/wrapper_test.go` 가 typed wrapper 의 status code, error body 형태가 기존과 byte-equivalent 임을 검증
4. **spec golden test**: `specs/generate_test.go` 가 마이그레이션된 도메인의 spec 출력이 expected snapshot 과 일치

### 7.3 CI 게이트

- `make format` clean
- `go vet ./...` zero error
- `go test -race ./...` all pass (`-race` 는 typed registry 의 sync.RWMutex 검증)
- `go test -cover ./internal/api/typed/...` 80%+ coverage
- `make specs generate` 결과 gitclean (yaml 이 자동 갱신되어 PR 에 포함되어야 함)

---

## 8. 위험 + 완화

| 위험 | 영향도 | 완화책 |
|------|------|------|
| **gin wildcard `*branch` 의 OpenAPI 변환** — leading `/` 포함되어 path param value 에 들어감 | High | `routeToSpecPath` 의 기존 `*([a-zA-Z0-9_]+)` → `{$1}` 변환 유지. registry 등록 시 path 는 이미 OpenAPI 형식. handler 측에서는 `c.Param("branch")` 가 leading `/` 포함 — 이는 기존 동작 그대로 (`strings.TrimPrefix(name, "/")` 패턴 유지). description 에 wildcard 임을 명시 |
| **generic 함수의 reflect.TypeOf 한계** — 빈 인터페이스 `any` 의 zero value 가 nil 이면 reflect.TypeOf 가 nil 반환 | Medium | Register 함수 내부에서 `reflect.TypeOf((*Req)(nil)).Elem()` 패턴으로 type 추출. `var zeroReq Req` 후 `reflect.TypeOf(zeroReq)` 는 zero value 가 interface 라도 reflect.Type 정상 반환 (struct 타입 한정) |
| **init() 등록 순서 문제** | Low | `init()` 사용 안 함. 명시적 `RegisterRoutes(group, handler)` 함수가 server.go 와 specs/generate.go 양쪽에서 호출 |
| **raw streaming endpoint 의 spec 표현** | Low | `RegisterRaw` 는 `RespType=nil` + `ContentType="application/octet-stream"` 으로 등록. spec 에는 `responses.200.content.application/octet-stream.schema.type: string, format: binary` 출력 |
| **다중 응답 코드 (4xx)** — error mapper 가 등록한 status 들이 spec 에 빠짐 | Medium | `WithResponse(404, ...)`, `WithResponse(409, ...)` 옵션으로 명시 등록. error mapper 와 별도 registration. acceptance: 모든 도메인 PR 에서 error mapper 의 가능 status 가 모두 `WithResponse` 로도 등록됨 |
| **specs/generate.go 가 handler instance 를 만들 수 없음** — `NewBranchOperationsHandler(cfg)` 가 cfg 의 RepoRoot 디렉토리 검증 등 부수효과 | High | 두 가지 접근 (1) test config (`/tmp/dummy-repo`) 로 dummy 인스턴스 생성 (2) `RegisterRoutes` 가 nil handler 도 받아 등록만 수행하는 spec-only 모드 추가. **선택: (2)** — `RegisterRoutes(group gin.IRouter, h HandlerInterface)` 의 spec 모드는 handler 호출이 일어나지 않으므로 nil 가능 |
| **method value (`branchesHandler.HandleBranchStats`) 의 reflection 한계** — Go 의 method value 는 receiver 를 closure 로 capture, reflect.TypeOf 시 일반 func type | Low | typed.RegisterJSON 의 generic param `Req`, `Resp` 가 type inference 로 도출. method value 의 receiver 는 reflect 로 안 봄 — generic params 만 봄. 검증: Phase 0 의 `register_test.go` 에서 method value 등록 케이스 추가 |
| **gin.H ad-hoc 응답을 struct 로 승격할 때 외부 호환성** — orchestrator 측이 특정 JSON 키 의존 | Medium | 도메인 PR 마다 승격된 struct 의 json tag 가 기존 ad-hoc 키와 정확히 일치하는지 unit test. `httptest` 로 응답 byte 비교 |
| **spec golden test 의 unstable order** — map iteration order, registry endpoints 정렬 | Low | `Walk` 가 method asc, path asc 로 정렬된 snapshot 반환. 모든 reflect 기반 schema rendering 도 sorted |
| **CLAUDE.md 정책 ("Long-Term Solutions Only") 와의 hybrid 모드 충돌** | Low | hybrid 는 Phase 1-2 사이의 **마이그레이션 트랙** 일 뿐. Phase 2 마지막 PR 에서 legacy switch 가 완전히 제거됨. 임시 우회가 아닌 단계적 마이그레이션의 일부 |

---

## 9. 테스트 전략

### 9.1 typed wrapper 자체 (Phase 0)

- `internal/api/typed/registry_test.go`: register, walk, snapshot, concurrent registration (race detector)
- `internal/api/typed/register_test.go`: RegisterJSON 의 정상 흐름, JSON bind 실패 → 400, handler error → mapper 적용
- `internal/api/typed/wrapper_test.go`: status override, error body 형태 (`{"success": false, "error": "..."}`)
- `internal/api/typed/options_test.go`: 각 EndpointOption 의 누적 적용

### 9.2 generator (Phase 1)

- `specs/generate_test.go`:
  - golden test (현재 yaml 과 비교)
  - registry 비어있을 때 legacy fallback 동작
  - registry 에 dummy endpoint 추가 후 spec 출력에 반영되는지

### 9.3 도메인별 회귀 (Phase 2)

각 도메인 PR 마다:

- 기존 `internal/<domain>/*_test.go` 손대지 않고 통과 (외부 동작 보존)
- 도메인 schema 에 추가된 새 struct 의 json marshaling test
- `specs/generate_test.go` 의 golden snapshot 갱신 + 갱신 사항 review 친화 출력

### 9.4 영향받는 기존 테스트

| 테스트 | 영향 |
|------|------|
| `internal/api/error_handling_test.go` | 영향 없음 — handler signature 변경되어도 raw `*gin.Context` 받는 외부 entry 는 typed wrapper 가 동일하게 wrapping. 단 setupRouterForErrorTests 의 `r.GET(...)` 호출은 직접 handler 등록 → typed.RegisterGet 으로 교체할지 그대로 둘지 도메인 PR 에서 결정 (테스트 격리를 위해 그대로 두되, typed wrapper 통과 확인 위한 별도 test 추가) |
| `internal/api/performance_load_test.go` | 영향 없음 — 외부 동작 보존 |
| `internal/merge/*_test.go` (CLAUDE.md 핵심) | 영향 없음 — `go test ./internal/merge` 통과 필수 |
| 각 도메인 unit test | handler 시그니처 변경되었으므로 unit test 도 typed signature 호출로 변경 필요 (도메인 PR 의 일부) |

### 9.5 Coverage

- typed package: 80%+ (TDD 로 작성하므로 자연스럽게 달성)
- 각 도메인 handler: 기존 coverage 유지 또는 증가 (시그니처 단순해져 더 쉽게 unit test 가능)

---

## 10. 산출물 검증 체크리스트 (전체 마이그레이션 완료 시)

- [ ] `internal/api/typed/` 패키지 unit test 80%+ coverage
- [ ] `specs/generate.go` 의 `requestSchemaRef`, `responseSchemaRef`, `getTagFromPath`, `parseServerRoutes` (AST) 완전 삭제
- [ ] 13개 도메인 모두 typed handler 로 마이그레이션 완료
- [ ] `specs/generated/collabops-git-executor.yaml` 의 모든 path operation 이 `responses.<status>.content` 보유 (44% 미만 누락 → 0%)
- [ ] 모든 POST/PUT/PATCH 가 적절한 `requestBody.content` 보유 (52% 미만 → 0%; raw upload 는 binary content type)
- [ ] `make format && go vet ./... && go test -race ./...` clean
- [ ] `go test ./internal/merge` 통과 (CLAUDE.md 게이트)
- [ ] orchestrator 측에서 새 yaml 로 `make specs sync-git-executor` 실행 시 schema 재생성 정상 (Phase 3, 본 plan out of scope)
- [ ] issue #2164 본문의 drift 사례 (`POST /branches/stats` 등) 가 spec 에서 정확히 채워짐을 grep 으로 확인

---

## 부록: 도메인별 endpoint 인벤토리 (server.go 기반 전수 조사)

```
[repository] POST /
[state]      GET  /state
[branches]   GET  /branches/validate/*name      → wildcard
             GET  /branches/resolve/*ref        → wildcard
             POST /branches/validate-reopen
             GET  /branches
             POST /branches
             PATCH /branches/*branch            → wildcard, body
             POST /branches/stats               → drift (44%)
             POST /branches/bulk-delete
             DELETE /branches/*branch           → wildcard
[tree]       GET  /tree
             GET  /tree/search
[commits]    GET  /commits
             GET  /commits/compare/summary
             GET  /commits/:sha
             POST /commits
[tags]       GET  /tags
             POST /tags
             POST /tags/bulk-delete
             DELETE /tags/*tag                  → wildcard
[blob]       GET  /blob                         → raw bytes 가능 (binary path)
[diff]       GET  /diff/commits/:base/:head
             GET  /diff/branches/:base/:head
             GET  /diff/branches                → query
             GET  /diff/file
             GET  /diff/commit/:sha
             GET  /diff/expand-context
             GET  /diff/patch/:base/:head       → raw text/diff?
             GET  /diff/search/files
             POST /diff/classify-anchors
[merge]      GET  /merges/preview
             GET  /merges/diff
             POST /merges
[revert]     POST /revert
[overview]   GET  /overview
             GET  /overview/tree
             GET  /overview/tree/commits
             GET  /overview/blob
             GET  /overview/latest-commit
             POST /overview/latest-commits
[context]    POST /context
[langstats]  GET  /languages
[fork]       POST /forks                        → status 201, error mapper
             GET  /fork
             POST /fork/sync
[LFS]        POST /info/lfs/objects/batch       → custom content-type
             POST /info/lfs/objects/verify
             POST /info/lfs/locks               → status 201
             GET  /info/lfs/locks
             POST /info/lfs/locks/verify
             POST /info/lfs/locks/:id/unlock
             PUT  /info/lfs/objects/storage/:oid → raw upload
             GET  /info/lfs/objects/storage/:oid → raw download
             GET  /workspaces/:owner/lfs/usage  → 다른 prefix
             (위 endpoints 는 .git suffix 경로로도 중복 등록)
```

`/diff/patch/:base/:head` 는 응답 형식 확인 필요 (raw patch text 가능 → `RegisterRaw` 후보). Phase 2 의 P2.12 (diff) PR 에서 코드 inspect 후 결정.

---

## 참고

- 이슈 본문: GitHub Issue #2164 (Issue-2020 의 sub)
- 정책: `/Users/jang-yeongjo/Desktop/code/collabops/collabops-git-executor/CLAUDE.md` 의 "Long-Term Solutions Only" + "Multi-phase implementation strategies"
- Phase 3 (orchestrator 측 sync) 는 본 plan out of scope — 별도 collabops-api-orchestrator 트랙
- 작업 동반 워크플로 (`feedback_collabops_cli_workflow.md`): step-by-step 진입 시 question → discussion → issue 등록 (이미 #2164) → /plan (본 문서) → /tdd → /code-review → CR 작성
