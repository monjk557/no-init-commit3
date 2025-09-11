# 📋 테스트 가이드

이 디렉토리는 CollabOps Git Executor의 모든 테스트를 체계적으로 관리합니다.

## 🏗️ 디렉토리 구조

```
tests/
├── README.md                      # 📋 테스트 가이드 및 실행 방법
├── go.mod                         # 🔧 테스트 모듈 설정
│
├── unit/                          # 🧪 단위 테스트 (개별 컴포넌트)
│   ├── core/                     # 핵심 비즈니스 로직
│   │   ├── cache_*.go            # 캐시 관련 테스트
│   │   ├── git_*.go              # Git 저장소 관련 테스트
│   │   └── repository_*.go       # 저장소 경로/관리 테스트
│   ├── api/                      # HTTP API 핸들러
│   │   ├── *_test.go            # API 핸들러 테스트
│   │   └── git_stream_*.go       # 스트리밍 API 테스트
│   ├── config/                   # 설정 관리
│   │   └── config_test.go        # 설정 테스트
│   ├── util/                     # 유틸리티 컴포넌트
│   │   ├── security_*.go         # 보안 관련 테스트
│   │   ├── logging_*.go          # 로깅 관련 테스트
│   │   ├── permissions_*.go      # 권한 관련 테스트
│   │   └── middleware_*.go       # 미들웨어 테스트
│   └── perf/                     # 성능 테스트
│       └── resource_manager_test.go
│
├── integration/                   # 🔗 통합 테스트 (컴포넌트 간 연동)
│   ├── *_integration_test.go     # 기존 통합 테스트
│   └── protocol_*.go             # Git 프로토콜 통합 테스트
│
├── e2e/                          # 🚀 E2E 테스트 (전체 사용자 여정)
│   └── (future smoke tests)     # 향후 추가 예정
│
├── fixtures/                     # 📦 테스트 데이터 및 설정
│   └── configs/
│       └── test.yaml            # 테스트용 설정
│
└── utils/                        # 🛠️ 테스트 유틸리티
    └── integration_harness.go   # 통합 테스트 헬퍼 함수들
```

## 🚀 테스트 실행 방법

### 전체 테스트 실행
```bash
# 프로젝트 루트에서
make test
```

### 카테고리별 테스트 실행
```bash
# 단위 테스트만 실행
go test ./tests/unit/...

# 통합 테스트만 실행  
go test ./tests/integration/...

# 특정 모듈 테스트
go test ./tests/unit/core/...
go test ./tests/unit/api/...
go test ./tests/unit/util/...
```

### 커버리지 포함 테스트
```bash
# 전체 커버리지
go test -coverprofile=coverage.out ./tests/...
go tool cover -html=coverage.out -o coverage.html

# 단위 테스트 커버리지만
go test -coverprofile=unit_coverage.out ./tests/unit/...
```

### 특정 테스트 패턴 실행
```bash
# 특정 테스트 함수만 실행
go test -run TestBranchOperations ./tests/integration/...

# 캐시 관련 테스트만 실행
go test -run Cache ./tests/unit/core/...
```

## 🧪 테스트 작성 가이드라인

### 단위 테스트 (Unit Tests)
- **위치**: `tests/unit/`
- **목적**: 개별 함수나 메서드의 동작 검증
- **특징**: 
  - 외부 의존성 없이 실행 가능
  - 빠른 실행 속도
  - Mock/Stub 사용

### 통합 테스트 (Integration Tests)  
- **위치**: `tests/integration/`
- **목적**: 여러 컴포넌트 간의 상호작용 검증
- **특징**:
  - 실제 Git 저장소 사용
  - 네트워크 통신 포함 가능
  - 더 긴 실행 시간

### E2E 테스트 (End-to-End Tests)
- **위치**: `tests/e2e/`
- **목적**: 전체 사용자 워크플로우 검증
- **특징**:
  - 실제 환경과 유사한 조건
  - 전체 시스템 동작 검증

## 🔧 테스트 헬퍼 사용법

### Integration Harness
```go
// tests/utils/integration_harness.go 사용
import "github.com/collabops/git-executor/tests/utils"

func TestSomething(t *testing.T) {
    harness := utils.NewIntegrationHarness(t)
    defer harness.Cleanup()
    
    // 테스트 로직...
}
```

## 📊 테스트 메트릭

테스트 실행 시 다음 메트릭들을 모니터링합니다:
- **커버리지**: 최소 80% 목표
- **실행 시간**: 단위 테스트 < 10초, 통합 테스트 < 2분
- **안정성**: 플래키 테스트 0% 목표

## 🐛 테스트 문제 해결

### 일반적인 이슈
1. **Import 경로 문제**: 모든 테스트는 `github.com/collabops/git-executor/` 기준으로 import
2. **Permission 문제**: 테스트 실행 시 적절한 파일 권한 확인
3. **Git 저장소 상태**: 테스트 간 저장소 상태 초기화 확인

### 디버깅 옵션
```bash
# 자세한 테스트 로그
go test -v ./tests/...

# 테스트 중 출력 보기
go test -v -count=1 ./tests/unit/core/ 

# 특정 테스트만 반복 실행
go test -run TestSpecific -count=10 ./tests/...
```

## 📝 마이그레이션 노트

기존 `internal/` 디렉토리에 있던 테스트들이 다음과 같이 재구성되었습니다:

- `internal/*/` → `tests/unit/`로 단위 테스트 이동
- `internal/protocol/` → `tests/integration/`로 통합 테스트 이동  
- `test/integration/` → `tests/integration/`로 통합
- `internal/testutils/` → `tests/utils/`로 헬퍼 함수 이동

모든 테스트는 기존 동작을 유지하면서 더 나은 구조로 정리되었습니다.