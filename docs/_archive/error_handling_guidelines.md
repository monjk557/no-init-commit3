# Error Handling 가이드라인

## 핵심 원칙

**Error Mapper 추상화 메서드를 사용하지 않는다.**

핸들러에서는 에러를 직접 응답으로 처리하며, 에러 매핑 함수나 추상화 레이어를 만들지 않는다.

---

## ✅ 올바른 패턴: 직접 응답

### 기본 패턴

```go
// ✅ 권장: 직접 응답 (명시적이고 단순)
if err != nil {
    c.JSON(http.StatusNotFound, gin.H{
        "success": false,
        "error":   err.Error(),
    })
    return
}
```

### 에러 타입별 분기 처리

```go
// ✅ 권장: errors.Is()로 직접 체크
sourceCommit, err := h.objectManager.ResolveReference(repository, req.SourceRef)
if err != nil {
    if errors.Is(err, repositories.ErrRefNotFound) ||
        errors.Is(err, repositories.ErrObjectNotFound) ||
        errors.Is(err, plumbing.ErrReferenceNotFound) {
        c.JSON(http.StatusNotFound, gin.H{
            "success": false,
            "error":   err.Error(),
        })
    } else {
        c.JSON(http.StatusBadRequest, gin.H{
            "success": false,
            "error":   err.Error(),
        })
    }
    return
}
```

---

## ❌ 금지 패턴: Error Mapper 추상화

### 1. Status Code 매핑 함수 금지

```go
// ❌ 금지: status code 매핑 함수
func statusCodeForResolveReferenceError(err error) int {
    if errors.Is(err, repositories.ErrInvalidRef) {
        return http.StatusBadRequest
    }
    // ...
}

// 사용
c.JSON(statusCodeForResolveReferenceError(err), gin.H{...})
```

**이유:**
- 불필요한 추상화
- Go의 "최소 추상 철학" 위배
- 코드 가독성 저하

### 2. Error Wrapping 함수 금지

```go
// ❌ 금지: 에러 래핑 함수
func wrapRefError(err error) error {
    statusCode := http.StatusBadRequest
    if errors.Is(err, ErrRefNotFound) {
        statusCode = http.StatusNotFound
    }
    return &RefResolutionError{
        Err:        err,
        StatusCode: statusCode,
    }
}
```

**이유:**
- 에러 객체에 HTTP status code를 포함시키는 것은 레이어 분리 원칙 위배
- 도메인 로직과 HTTP 레이어의 혼재

### 3. Error Handler 추상화 금지 (REST API 핸들러)

```go
// ❌ 금지: 중앙집중식 Error Handler (REST API 핸들러에서)
type ErrorHandler struct {
    logger *Logger
}

func (h *ErrorHandler) Handle(ctx context.Context, w http.ResponseWriter, err error) {
    // 에러를 AppError로 변환하고 응답
}

// 핸들러에서 사용
errorHandler.Handle(ctx, w, err)
```

**이유:**
- Gin 핸들러에서는 `*gin.Context`를 사용하므로 `http.ResponseWriter` 기반 추상화는 불필요
- 대부분의 핸들러에서 사용하지 않아 일관성 없음

---

## 예외: Protocol 레이어

**Git Protocol, SSH Protocol 등 특수 프로토콜에서는 허용**

```go
// ✅ 허용: Protocol 레이어 (orchestration 레이어)
// internal/protocol/errors.go
func (eh *ErrorHandler) HandleError(w http.ResponseWriter, r *http.Request, err error, operation string) {
    // Git protocol은 특수한 에러 형식(pkt-line)이 필요하므로 중앙집중식 처리 필요
}
```

**이유:**
- Protocol 레이어는 "orchestration 레이어"에 해당
- Git protocol은 특수한 에러 형식이 필요
- GopherCon 가이드: "복잡한 orchestration 레이어에서만 제한적 적용"

---

## HTTP Status Code 매핑 규칙

### 기본 원칙

- **기본값: 400 Bad Request**
- **404 Not Found**: 리소스가 존재하지 않을 때만
- **500 Internal Server Error**: 사용하지 않음 (알 수 없는 에러도 400으로 처리)

### 에러 타입별 매핑

```go
// 400 Bad Request (기본값)
- ErrInvalidRef
- ErrUnsupportedRef
- ErrAmbiguousRef
- 기타 모든 에러

// 404 Not Found
- ErrRefNotFound
- ErrObjectNotFound
- plumbing.ErrReferenceNotFound
```

---

## 코드 예시

### ✅ 올바른 예시

```go
func (h *MergeOperationsHandler) HandleMerge(c *gin.Context) {
    // ...
    
    sourceCommit, err := h.objectManager.ResolveReference(repository, req.SourceRef)
    if err != nil {
        if errors.Is(err, repositories.ErrRefNotFound) ||
            errors.Is(err, repositories.ErrObjectNotFound) ||
            errors.Is(err, plumbing.ErrReferenceNotFound) {
            c.JSON(http.StatusNotFound, gin.H{
                "success": false,
                "error":   err.Error(),
            })
        } else {
            c.JSON(http.StatusBadRequest, gin.H{
                "success": false,
                "error":   err.Error(),
            })
        }
        return
    }
    
    // ...
}
```

### ❌ 잘못된 예시

```go
// ❌ 금지: 매핑 함수 사용
func (h *MergeOperationsHandler) HandleMerge(c *gin.Context) {
    sourceCommit, err := h.objectManager.ResolveReference(repository, req.SourceRef)
    if err != nil {
        c.JSON(statusCodeForResolveReferenceError(err), gin.H{
            "success": false,
            "error":   err.Error(),
        })
        return
    }
}

// ❌ 금지: 에러 래핑 함수 사용
func (h *MergeOperationsHandler) HandleMerge(c *gin.Context) {
    sourceCommit, err := h.objectManager.ResolveReference(repository, req.SourceRef)
    if err != nil {
        wrappedErr := wrapRefError(err)
        c.JSON(wrappedErr.StatusCode, gin.H{
            "success": false,
            "error":   wrappedErr.Error(),
        })
        return
    }
}
```

---

## 참고 문서

- [GopherCon 가이드라인: Error Mapper 분석](./gophercon/error_mapper_analysis.md)
- [Go의 최소 추상 철학](./gophercon/guides.json)

---

## 요약

- ✅ **직접 응답**: `c.JSON()`으로 직접 처리
- ✅ **errors.Is()**: 에러 타입 체크는 `errors.Is()` 사용
- ❌ **Mapper 함수**: status code 매핑 함수 금지
- ❌ **Wrapping 함수**: 에러 래핑 함수 금지
- ❌ **Error Handler**: REST API 핸들러에서 중앙집중식 Error Handler 금지
- ⚠️ **예외**: Protocol 레이어에서만 제한적 허용

**핵심**: Go의 "최소 추상 철학"을 준수하여 불필요한 추상화를 만들지 않는다.
