// Package typed 는 typed handler 아키텍처의 핵심 인프라를 제공한다.
//
// handler 시그니처가 곧 OpenAPI schema 선언이 되도록 하여 spec drift 의
// 메커니즘 자체를 제거한다 (Issue-2164). 등록 시점에 (method, path, ReqType,
// RespType, tag, ...) 가 Registry 에 자동 capture 되고, spec generator 는
// Registry 를 walk 하여 OpenAPI YAML 을 생성한다.
//
// 자세한 설계: docs/architecture/typed-handler-migration-plan.md
package typed

import (
	"reflect"
	"sort"
	"sync"
)

// Endpoint 는 단일 REST endpoint 의 schema 선언이다. typed.Register* 함수가
// 등록 시점에 채우고 spec generator 가 walk 하여 OpenAPI 출력에 반영한다.
type Endpoint struct {
	Method      string // "GET", "POST", ...
	Path        string // OpenAPI 형식의 absolute path (예: "/repos/{owner}/{repo}/branches")
	Tag         string // OpenAPI tag (예: "Branches")
	OperationID string // 미지정 시 generator 가 derive
	Summary     string
	Description string

	ReqType     reflect.Type    // requestBody 의 Go 타입. nil 이면 body 없음
	ReqLocation RequestLocation // body / query / multipart / raw / none
	QueryParams []ParamSpec     // RegisterGetWithQuery 등에서 활용
	PathParams  []ParamSpec     // owner/repo 외 추가 path param

	Responses []ResponseSpec // 200 외 4xx 등 다중 status 지원

	// SuccessStatus 는 handler 가 정상 반환했을 때 wrapper 가 사용할 status code.
	// 0 이면 기본 200 사용 (POST 라도 200 — Created 의도면 WithStatus(201) 명시).
	SuccessStatus int

	// ErrorMapper 는 handler 가 반환한 error 를 (HTTP status, response body) 로
	// 변환한다. nil 이면 wrapper 가 default 500 + 공통 ErrorResponse 사용.
	// spec generator 는 이 함수를 직접 보지 않고, 대응하는 status 들이
	// Endpoint.Responses 에 명시되어 있어야 한다 (WithResponse 로).
	ErrorMapper func(error) (int, any)

	ContentType string // 기본 "application/json"; LFS 의 vnd.git-lfs+json 등 override

	Security   []string // 기본 ["ApiKeyAuth"]
	Deprecated bool

	// ExcludeFromSpec 가 true 면 spec generator 가 이 endpoint 를 출력에서
	// 제외한다. gin 런타임 라우트 등록은 그대로 유지되므로 기존 caller 는
	// 계속 호출 가능. 기능에서 빠진 (deprecated) endpoint 를 외부 spec 에서만
	// 숨길 때 사용.
	ExcludeFromSpec bool
}

// RequestLocation 은 요청 페이로드가 어디서 오는지를 분류한다.
type RequestLocation int

const (
	LocationNone     RequestLocation = iota // body / query 모두 없음 (DELETE 등)
	LocationBodyJSON                        // application/json body
	LocationQuery                           // query struct (RegisterGetWithQuery 전용)
	LocationMultipart
	LocationRawBytes // 바이너리 업로드 (LFS storage PUT 등)
)

// ResponseSpec 는 OpenAPI responses 의 한 status 항목에 대응한다.
type ResponseSpec struct {
	Status      int
	RespType    reflect.Type // nil 이면 빈 응답 또는 SchemaRef 사용
	ContentType string       // 기본 "application/json"
	Description string
	SchemaRef   string // predefined component 참조 시 (예: "#/components/responses/BadRequest")
}

// ParamSpec 는 path/query parameter 한 개의 메타데이터.
type ParamSpec struct {
	Name        string
	Required    bool
	GoType      reflect.Type
	Description string
}

// Registry 는 등록된 Endpoint 들의 thread-safe 컬렉션이다.
type Registry struct {
	mu        sync.RWMutex
	endpoints []Endpoint
}

// NewRegistry 는 빈 Registry 를 만든다 (주로 테스트용 — 운영 코드는
// DefaultRegistry 를 공유한다).
func NewRegistry() *Registry {
	return &Registry{}
}

// Register 는 Endpoint 를 추가한다. 동일 (method, path) 중복 등록도 허용한다
// (예: gin 의 .git suffix 중복 등록과 같이 의도된 alias 는 spec 출력 단계에서
// dedupe 처리).
func (r *Registry) Register(e Endpoint) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.endpoints = append(r.endpoints, e)
}

// Walk 는 등록된 endpoint 들을 method asc, path asc 정렬 순서로 방문한다.
// fn 이 호출되는 동안 Registry 는 read-locked 가 아니므로 fn 안에서 추가
// Register 호출은 허용된다 (snapshot 기반 walk 이므로 race 없음).
func (r *Registry) Walk(fn func(Endpoint)) {
	for _, e := range r.Snapshot() {
		fn(e)
	}
}

// Snapshot 은 정렬된 endpoint slice 의 독립 copy 를 반환한다. 호출자가
// 반환된 slice 를 변형해도 Registry 내부 상태에는 영향이 없다.
func (r *Registry) Snapshot() []Endpoint {
	r.mu.RLock()
	out := make([]Endpoint, len(r.endpoints))
	copy(out, r.endpoints)
	r.mu.RUnlock()

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Method != out[j].Method {
			return out[i].Method < out[j].Method
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// defaultRegistry 는 프로세스-와이드 singleton — server.go 와
// specs/generate.go 가 같은 인스턴스를 공유한다.
var defaultRegistry = NewRegistry()

// DefaultRegistry 는 프로세스 singleton Registry 를 반환한다.
func DefaultRegistry() *Registry {
	return defaultRegistry
}

// SetDefaultRegistry 는 프로세스 singleton 을 임시로 교체한다. 반환된
// restore 함수를 호출하면 원래 인스턴스로 복원된다.
//
// 의도된 사용처:
//   - spec generator (specs/generate.go) — 도메인의 RegisterRoutes 가 채울
//     별도 Registry 를 임시 default 로 swap.
//   - 테스트 격리.
//
// 동시성 안전하지 않음 — server runtime 에서 호출 금지 (concurrent goroutine
// 이 mid-swap 시점에 default 를 보면 의도하지 않은 인스턴스에 등록될 수 있음).
func SetDefaultRegistry(r *Registry) (restore func()) {
	orig := defaultRegistry
	defaultRegistry = r
	return func() { defaultRegistry = orig }
}
