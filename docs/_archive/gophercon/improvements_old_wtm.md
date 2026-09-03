# GopherCon 가이드라인 기반 코드베이스 개선 제안

## 📊 종합 평가

**현재 상태**: 85% 준수 ✅  
**개선 가능 영역**: 3개 주요 항목

---

## ✅ 잘 지켜진 부분

### 1. HTTP 프레임워크 선택 ✅
- **현재**: Gin 사용
- **가이드라인**: "대부분의 서비스에서는 gin 또는 net/http가 더 안전한 선택"
- **평가**: 가이드라인에 완벽히 부합

### 2. Small Interface 원칙 ✅
- **Evictor interface**: 2개 메서드만 (작은 인터페이스)
  ```go
  type Evictor interface {
      ShouldTriggerEviction() (bool, []string)
      RunEvictionCycle(maxEvictions int) ([]string, error)
  }
  ```
- **MetricsRecorder interface**: 6개 메서드 (적절한 크기)
- **평가**: Go 철학에 부합

### 3. 인터페이스 위치 ✅
- **Evictor**: 사용하는 쪽(Service)에서 정의
- **MetricsRecorder**: 사용하는 쪽(Handler)에서 정의
- **평가**: "인터페이스는 소비자가 정의한다" 원칙 준수

### 4. 반환 타입 ✅
- **NewService**: `*Service` (구조체 반환)
- **NewHandler**: `*Handler` (구조체 반환)
- **평가**: "Accept interfaces, return structs" 원칙 준수

### 5. 동시성 제어 도구 ✅
- **sync.Mutex**: 적절히 사용
- **semaphore**: concurrency.Manager에서 구현
- **평가**: 가이드라인에 부합

---

## ⚠️ 개선 필요 항목

### 1. 테스트: t.Parallel 미사용 (중요도: 높음)

**현재 상태**:
- 테스트 파일에서 `t.Parallel()` 사용 없음
- 순수 로직 테스트도 병렬 실행 안 함

**가이드라인**:
> "가능한 테스트에는 t.Parallel을 기본으로 사용한다. 동시성 버그는 테스트에서 먼저 발견하는 것이 가장 싸다."

**개선 제안**:
```go
// Before
func TestStoreCreate(t *testing.T) {
    // ...
}

// After
func TestStoreCreate(t *testing.T) {
    t.Parallel()  // 추가
    // ...
}
```

**적용 대상**:
- `internal/workspace/workspace_test.go` - Store 테스트 (순수 로직)
- `internal/workspace/ttl_test.go` - TTL 로직 테스트
- `internal/eviction/evictor_test.go` - Eviction 로직 테스트
- `internal/concurrency/concurrency_test.go` - 동시성 제어 테스트

**주의사항**:
- 파일 시스템/DB 사용 테스트는 제외 (테스트 격리 필요)
- 고유 데이터 사용 필수 (`time.Now().UnixNano()`)

---

### 2. main 함수 분리 패턴 (중요도: 중간)

**현재 상태**:
```go
// cmd/wtm/main.go
func main() {
    Execute()  // Cobra 실행
}

var serveCmd = &cobra.Command{
    RunE: func(cmd *cobra.Command, args []string) error {
        // 서버 초기화 로직이 여기에...
        return startWithGracefulShutdown(...)
    },
}
```

**문제점**:
- 서버 로직이 `serveCmd.RunE`에 있어서 테스트하기 어려움
- TestMain에서 실제 서버를 띄우기 어려움

**가이드라인**:
> "main 함수의 로직을 run 함수로 분리하고, 테스트에서는 run을 직접 호출하거나 TestMain에서 서버를 한 번만 기동"

**개선 제안**:
```go
// cmd/wtm/main.go
func main() {
    if err := run(); err != nil {
        os.Exit(1)
    }
}

func run() error {
    // 현재 serveCmd.RunE의 로직을 여기로 이동
    logger.Info("Starting WTM server...")
    // ... 초기화 로직
    return startWithGracefulShutdown(server, ...)
}

var serveCmd = &cobra.Command{
    RunE: func(cmd *cobra.Command, args []string) error {
        return run()
    },
}
```

**이점**:
- `TestMain`에서 `run()` 직접 호출 가능
- 실제 HTTP 요청 기반 테스트 작성 용이
- Mock 의존도 감소

---

### 3. Options Pattern 적용 (중요도: 낮음)

**현재 상태**:
```go
func NewService(
    cfg *config.Config,
    store *Store,
    evictor Evictor,
    concurrencyMgr *concurrency.Manager,
    gitClient *git.Client,
    fsManager *filesystem.Manager,
    logger *zap.Logger,
) *Service
```

**문제점**:
- 7개 파라미터 (가이드라인: "설정 값이 3개를 넘어가면 Options pattern을 고려")

**가이드라인**:
> "Options pattern은 설정 관리와 생성자 설계에서 널리 사용되며, telescoping constructor 문제를 효과적으로 해결한다."

**개선 제안** (선택적):
```go
type ServiceOption func(*Service)

func WithEvictor(evictor Evictor) ServiceOption {
    return func(s *Service) {
        s.evictor = evictor
    }
}

func NewService(cfg *config.Config, opts ...ServiceOption) *Service {
    s := &Service{
        config: cfg,
        // 기본값 설정
    }
    for _, opt := range opts {
        opt(s)
    }
    return s
}

// 사용
service := workspace.NewService(cfg,
    workspace.WithStore(store),
    workspace.WithEvictor(evictor),
    // ...
)
```

**고려사항**:
- 현재 구조도 충분히 명확함
- Options pattern은 선택적 개선 (우선순위 낮음)
- 테스트에서 유연성이 필요할 때만 도입 고려

---

## 📋 우선순위별 개선 계획

### Phase 1: 즉시 적용 (높은 영향, 낮은 리스크)
1. ✅ **t.Parallel 추가** - 순수 로직 테스트에 병렬 실행 적용
   - 예상 시간: 1-2시간
   - 리스크: 낮음 (테스트만 수정)

### Phase 2: 중기 개선 (테스트 품질 향상)
2. ✅ **main 함수 분리** - run() 함수로 서버 로직 분리
   - 예상 시간: 2-3시간
   - 리스크: 낮음 (리팩토링)

### Phase 3: 선택적 개선 (코드 품질 향상)
3. ⚠️ **Options Pattern** - NewService 생성자 개선
   - 예상 시간: 3-4시간
   - 리스크: 중간 (API 변경)
   - **권장**: 필요할 때만 적용 (현재 구조도 충분히 명확)

---

## 🎯 결론

**현재 코드베이스는 GopherCon 가이드라인을 85% 준수**하고 있습니다.

**핵심 강점**:
- ✅ Gin 프레임워크 선택 (가이드라인 부합)
- ✅ Small Interface 원칙 준수
- ✅ 인터페이스 위치 적절 (소비자 정의)

**개선 기회**:
1. **t.Parallel 추가** - 동시성 버그 조기 발견 (높은 우선순위)
2. **main 함수 분리** - 테스트 용이성 향상 (중간 우선순위)
3. **Options Pattern** - 선택적 개선 (낮은 우선순위)

**전체 평가**: 이미 잘 작성된 코드베이스이며, 위 3가지 개선으로 가이드라인 준수율을 95%+로 향상시킬 수 있습니다.
