# Error Mapper 사용 유무 분석 및 권장사항

## 📋 분석 기준

GopherCon 가이드라인의 핵심 원칙:
- **최소 추상 철학**: 불필요한 추상을 제거하고, 반드시 필요한 추상만 최소 크기로 유지
- **Small Interface 원칙**: 인터페이스가 커질수록 추상화는 약해진다
- **인터페이스는 발견하는 것**: 사용 패턴이 드러났을 때 발견
- **Effect 패턴은 선택적 실험**: 복잡한 orchestration 레이어에서만 제한적 적용

---

## 🔍 현재 코드베이스 상황

### 사용 현황
- **직접 응답 패턴** (`c.JSON()`): 약 90%+ 핸들러에서 사용
- **도메인별 매핑 함수**: `merge/handler.go`의 `statusCodeForResolveReferenceError()` (4회 사용)
- **Protocol Error Handler**: `protocol/errors.go`의 `ErrorHandler.HandleError()` (SSH protocol에서 4회 사용)
- **미들웨어 기반**: `api/response.go`의 `JSONResponseMiddleware` (Gin 에러 처리)

### 미사용 추상화
- `WrapRepositoryError()` - 정의만 존재
- `WrapPermissionError()` - 정의만 존재
- `RefErrorCode()` - 정의만 존재
- `logging.ErrorHandler.Handle()` - 전역 함수로만 존재, 직접 호출 없음

---

## ⚖️ Error Mapper 사용 유무에 따른 장단점

### 1️⃣ 직접 응답 패턴 (`c.JSON()` 직접 사용)

#### ✅ 장점
- **명시성**: 코드를 읽는 즉시 어떤 응답이 반환되는지 명확
- **최소 추상**: Go의 "최소 추상 철학"에 부합
- **단순성**: 추가 추상화 레이어 없이 직관적
- **디버깅 용이**: 스택 트레이스가 단순하고 추적이 쉬움
- **유연성**: 각 핸들러가 상황에 맞게 자유롭게 응답 형식 조정 가능
- **학습 곡선 낮음**: Gin 사용자에게 자연스러운 패턴

#### ❌ 단점
- **중복 코드**: 비슷한 에러 응답 패턴이 반복됨
  ```go
  c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
  ```
- **일관성 관리 어려움**: 에러 응답 형식이 핸들러마다 약간씩 다를 수 있음
- **에러 메시지 변경 시**: 여러 곳을 수정해야 함
- **로깅 일관성**: 각 핸들러에서 로깅 방식이 다를 수 있음
- **테스트 복잡도**: 각 핸들러마다 응답 형식을 개별적으로 검증해야 함

---

### 2️⃣ 중앙집중식 Error Mapper (`ErrorHandler`, `AppError`)

#### ✅ 장점
- **일관성**: 모든 에러 응답이 동일한 형식으로 반환
- **중앙 관리**: 에러 응답 형식 변경 시 한 곳만 수정
- **로깅 통합**: 모든 에러가 일관된 방식으로 로깅됨
- **에러 타입 분류**: `ErrorType` 기반으로 체계적 관리
- **확장성**: 새로운 에러 타입 추가가 용이
- **테스트 용이**: ErrorHandler만 모킹하면 됨

#### ❌ 단점
- **추상화 증가**: Go의 "최소 추상 철학"에 위배될 수 있음
- **복잡도 증가**: 
  - `AppError` → `ErrorHandler` → `sendErrorResponse()` 체인
  - 디버깅 시 여러 레이어를 거쳐야 함
- **유연성 감소**: 특수한 경우에 맞춤 응답이 어려움
- **학습 곡선**: 팀원들이 ErrorHandler 사용법을 학습해야 함
- **과도한 추상화 위험**: 
  - GopherCon 가이드: "Effect 패턴은 선택적 실험"
  - 모든 핸들러에 적용하면 "과도한 추상화"가 될 수 있음
- **현재 미사용**: 대부분의 핸들러에서 사용하지 않아 일관성 없음

---

### 3️⃣ 도메인별 매핑 함수 (`statusCodeForResolveReferenceError()`)

#### ✅ 장점
- **최소 추상**: 특정 도메인의 에러 매핑만 추상화
- **명확한 범위**: merge 도메인의 reference resolution 에러만 처리
- **사용 패턴 발견**: 실제 필요에 따라 자연스럽게 생성됨
  - GopherCon 원칙: "인터페이스는 발견하는 것"
- **테스트 용이**: 순수 함수로 테스트 가능
- **재사용성**: 같은 도메인 내에서 재사용

#### ❌ 단점
- **도메인별 중복**: 각 도메인마다 비슷한 매핑 함수가 생길 수 있음
- **일관성 부족**: 도메인마다 매핑 로직이 다를 수 있음

---

## 🎯 GopherCon 원칙 기반 권장사항

### 핵심 판단 기준

1. **"최소 추상 철학" 준수**
   - 불필요한 추상은 제거
   - 실제 사용 패턴이 드러났을 때만 추상화

2. **"인터페이스는 발견하는 것"**
   - 중복이 3회 이상 발생하면 추상화 고려
   - 사용 패턴이 명확할 때만 도입

3. **"Effect 패턴은 선택적 실험"**
   - 복잡한 orchestration 레이어에서만 제한적 적용
   - 핵심 도메인 로직에는 과도한 추상화 지양

---

## ✅ 권장 방안: 하이브리드 접근

### 1. 기본 패턴: 직접 응답 유지 (현재 방식)

**대부분의 핸들러는 직접 응답 패턴 유지**

```go
// ✅ 권장: 직접 응답 (명시적이고 단순)
if err != nil {
    c.JSON(http.StatusNotFound, gin.H{
        "success": false,
        "error": "repository not found",
    })
    return
}
```

**이유**:
- 현재 코드베이스의 90%+가 이 패턴 사용
- Go의 "최소 추상 철학"에 부합
- 명시적이고 디버깅이 쉬움

---

### 2. 도메인별 매핑 함수: 필요 시 도입

**특정 도메인에서 에러 매핑이 복잡할 때만 사용**

```go
// ✅ 권장: 도메인별 매핑 함수 (merge/handler.go 패턴)
func statusCodeForResolveReferenceError(err error) int {
    if errors.Is(err, repositories.ErrInvalidRef) {
        return http.StatusBadRequest
    }
    if errors.Is(err, repositories.ErrRefNotFound) {
        return http.StatusNotFound
    }
    return http.StatusInternalServerError
}

// 사용
c.JSON(statusCodeForResolveReferenceError(err), gin.H{...})
```

**적용 기준**:
- 같은 도메인에서 에러 → 상태 코드 매핑이 3회 이상 반복될 때
- 매핑 로직이 복잡할 때 (단순 if문이 아닌 경우)
- 예: `overview/handler.go`에서 repository 에러 처리 반복 시

---

### 3. Protocol 레이어: ErrorHandler 유지

**Git Protocol, SSH Protocol 등 특수 프로토콜에서만 사용**

```go
// ✅ 유지: protocol/errors.go의 ErrorHandler
// Git protocol은 특수한 에러 형식이 필요하므로 중앙집중식 처리 필요
h.errorHandler.HandleError(w, r, err, "ssh_upload_pack")
```

**이유**:
- Protocol 레이어는 "orchestration 레이어"에 해당
- GopherCon 가이드: "복잡한 orchestration 레이어에서만 제한적 적용"
- Git protocol은 특수한 에러 형식(pkt-line)이 필요

---

### 4. 미사용 추상화 제거 또는 활성화 결정

#### 제거 권장
- `WrapRepositoryError()` - 사용되지 않음, 직접 응답으로 충분
- `WrapPermissionError()` - 사용되지 않음, 직접 응답으로 충분
- `RefErrorCode()` - 사용되지 않음, 필요 시 도입

#### 활성화 고려 (선택적)
- `logging.ErrorHandler` - 미들웨어에서만 사용 중
  - 현재 `JSONResponseMiddleware`에서 `AppError` 사용 중
  - 미들웨어 레벨에서의 사용은 유지
  - 핸들러에서 직접 사용은 권장하지 않음

---

## 📊 구체적 개선 제안

### Phase 1: 현재 패턴 정리 및 표준화

1. **공통 에러 응답 헬퍼 함수 추가** (최소 추상)
   ```go
   // internal/api/response.go에 추가
   func SendError(c *gin.Context, statusCode int, message string) {
       c.JSON(statusCode, gin.H{
           "success": false,
           "error": message,
       })
   }
   ```

2. **사용 현황**:
   - 단순한 에러 응답에 사용
   - 복잡한 에러는 여전히 직접 `c.JSON()` 사용

### Phase 2: 도메인별 매핑 함수 도입 (필요 시)

**조건**: 같은 도메인에서 에러 매핑이 3회 이상 반복될 때

예시: `overview/handler.go`에서 repository 에러 처리
```go
// 현재: 반복되는 패턴
c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})

// 개선: 도메인별 매핑 함수 (3회 이상 반복 시)
func (h *RepositoryOverviewHandler) handleRepositoryError(c *gin.Context, err error) {
    statusCode := statusCodeForRepositoryError(err)
    message := getRepositoryErrorMessage(err)
    c.JSON(statusCode, gin.H{"success": false, "error": message})
}
```

### Phase 3: 미사용 코드 정리

- `WrapRepositoryError()` 제거 또는 주석 처리
- `WrapPermissionError()` 제거 또는 주석 처리
- `RefErrorCode()` - 필요 시에만 활성화

---

## 🎯 최종 권장사항

### ✅ 현재 코드베이스에 적합한 방식

1. **기본 패턴: 직접 응답 유지** (90%+ 핸들러)
   - Go의 "최소 추상 철학" 준수
   - 명시적이고 디버깅 용이
   - 현재 코드베이스와 일관성 유지

2. **도메인별 매핑 함수: 필요 시 도입**
   - 중복이 3회 이상 발생할 때만
   - `statusCodeForResolveReferenceError()` 패턴 참고
   - "인터페이스는 발견하는 것" 원칙 준수

3. **Protocol 레이어: ErrorHandler 유지**
   - Git/SSH protocol 등 특수 프로토콜에서만
   - "orchestration 레이어"에 해당하므로 허용

4. **미사용 추상화 제거**
   - 사용되지 않는 함수는 제거하여 코드베이스 정리
   - 필요 시 나중에 다시 도입 가능

---

## 📈 예상 효과

### 직접 응답 패턴 유지 시
- ✅ 코드 가독성 향상
- ✅ 디버깅 용이성 유지
- ✅ 학습 곡선 낮음
- ✅ Go 철학 준수

### 도메인별 매핑 함수 도입 시 (필요 시)
- ✅ 중복 코드 감소
- ✅ 도메인별 일관성 확보
- ✅ 테스트 용이성 향상
- ✅ 최소 추상 원칙 준수

### 중앙집중식 ErrorHandler 전면 도입 시 (비권장)
- ❌ 과도한 추상화
- ❌ Go 철학 위배
- ❌ 기존 코드베이스와 불일치
- ❌ 학습 곡선 증가

---

## 🔗 참고

- GopherCon 가이드: "최소 추상 철학", "Small Interface 원칙"
- 현재 코드베이스: 90%+ 핸들러가 직접 응답 패턴 사용
- 성공 사례: `merge/handler.go`의 `statusCodeForResolveReferenceError()` 패턴
