# Go 테스트 작성 가이드

> **핵심 원칙**: 테스트는 "실행 가능한 스펙"이다. 읽는 사람이 3초 안에 "무엇을, 어떤 상황에서, 어떻게 검증하는지" 파악할 수 있어야 한다.

---

## 0. 10-Line Standard (PR에서 바로 참고)

```
1. 이름은 정답 스펙           Test_<Unit>_<Scenario>_<Expect>
2. Given/When/Then           3섹션 고정
3. When은 1개                 단일 행위만
4. 정답 강제                  requireStatus(t, resp, 404)
5. 에러 무시 금지             _, _ := 금지
6. t.TempDir()               전역/상대경로 금지
7. 시간 고정                  time.Date(...) 사용
8. helper는 boilerplate만    setup* 금지, t.Helper() 필수
9. 실패 메시지에 body         status=%d want=%d body=%s
10. L0/L1/L2 표시            테스트 레벨 명확히
```

**추가 금지**:
- `if got == 409` → 부분 조건 금지
- `requireNotStatus` 단독 사용 → 반드시 `requireStatus`와 함께

---

## 1. 테스트 이름 규칙 (가독성 80%는 이름에서 끝남)

### 포맷 (권장)

```
Test_<Unit>_<When/Scenario>_<Expect>
```

### 핵심 원칙: 이름은 "정답 스펙"이어야 한다

> **버그가 고쳐져도 테스트 이름은 그대로여야 한다.**

```go
// ✅ 좋음 - "정답"을 말함
Test_MergePreview_DeletedSourceRef_Returns404
Test_ResolveReference_BranchDeleted_ReturnsError
Test_CreateBranch_DuplicateName_Returns409Conflict

// ❌ 나쁨 - "현재 버그"에 종속됨
Test_MergePreview_DeletedSourceRef_Returns404_Not409   // 버그 고치면 이름이 구림
Test_DeletedBranch_Gets409_ShouldBe404                 // 버그 기준 이름

// ❌ 나쁨 - 의미 없음
TestSomething
TestHandler        // 무엇을? 어떤 상황을?
TestMergePreview   // 뭘 검증?
```

### 부정 기대(Not)는 이름에 넣지 말고 assert로

```go
// ✅ 좋음 - 이름은 정답, 부정은 보조 assert
func Test_MergePreview_DeletedSourceRef_Returns404(t *testing.T) {
    // ...
    requireStatus(t, resp, http.StatusNotFound)           // 정답 강제
    requireNotStatus(t, resp, http.StatusConflict)        // 보조: 옛날 버그 가드
}

// ❌ 나쁨 - 이름에 부정 기대
func Test_MergePreview_DeletedSourceRef_Returns404_Not409(t *testing.T) { ... }
```

---

## 2. Given/When/Then 섹션 규칙

### 규칙

- 섹션은 **반드시 3개만**: `Given` / `When` / `Then`
- 복잡하면 `Given` 안에 `And:` 추가
- 섹션 간에는 **빈 줄 1줄**로 시각적 구분
- **When은 1개**: 단일 행위만 (아래 예외 참고)

### When 1개 규칙의 예외

| 허용 | 설명 | 예시 |
|------|------|------|
| **인프라 준비** | 테스트 행위 자체가 아닌 환경 준비 | `startServer(); doRequest()` |
| **단일 행위의 구성 요소** | 논리적으로 1개 행위를 이루는 경우 | `login(); callProtectedAPI()` |

```go
// ✅ 허용 - startServer는 인프라 준비
// When: call API
startServer(t)           // 인프라 준비 (Given에 가까움)
resp := callAPI(t, "/")  // 실제 행위

// ❌ 금지 - 별개의 행위가 2개
// When: create and delete
createBranch(t, "foo")   // 행위 1
deleteBranch(t, "foo")   // 행위 2  ← 테스트를 분리해야 함
```

### 템플릿

```go
func Test_<Unit>_<Scenario>_<Expect>(t *testing.T) {
    t.Parallel()

    // Given: 테스트 전제 조건
    // And: 추가 전제 조건 (필요시)

    // When: 테스트 대상 행동 (1개만)

    // Then: 검증
}
```

### 실제 예시 (표준형)

```go
func Test_MergePreview_DeletedSourceRef_Returns404(t *testing.T) {
    t.Parallel()

    // Given: feature 브랜치가 있는 repo
    repo := newRepo(t)
    mainHash := commitFile(t, repo, "init.txt", "init", "initial commit")
    createBranchAt(t, repo, "feature/foo", mainHash)
    featureHash := commitFile(t, repo, "feat.txt", "feat", "feature commit")
    updateBranch(t, repo, "feature/foo", featureHash)

    // And: 브랜치가 삭제됨
    removeBranch(t, repo, "feature/foo")

    // When: 삭제된 브랜치로 MergePreview 호출
    resp := callMergePreview(t, repo, "feature/foo", "main")

    // Then: 404 반환 (ref not found)
    requireStatus(t, resp, http.StatusNotFound)           // 핵심: 정답 강제
    requireNotStatus(t, resp, http.StatusConflict)        // 보조: 옛날 버그 가드
}
```

---

## 3. Helper(추상화) 정책

> **디폴트는 inline, 예외적으로만 helper**

### 3.1 Helper 허용 기준 (1개라도 충족해야 함)

| 기준 | 예시 |
|------|------|
| 의도가 아닌 **순수 boilerplate** | `httptest.NewRecorder()`, `gin.New()`, `t.TempDir()` |
| **재사용 5회 이상** 확실하고, 이름이 스펙처럼 읽힘 | `newRepo(t)`, `commitFile(t, ...)` |
| 테스트를 **더 읽기 쉽게** 만듦 (핵심 상태 변화가 숨지 않음) | - |

### 3.2 Helper 네이밍 규칙

`setup*` **금지**. "무엇을 만든다/무슨 상태로 만든다"를 말로 써.

```go
// ✅ 좋음 - 의미가 명확
newRepo(t)
commitFile(t, repo, "a.txt", "content", "commit message")
createBranchAt(t, repo, "feature/foo", hash)
callMergePreview(t, router, source, target)
removeBranch(t, repo, "feature/foo")

// ❌ 나쁨 - 무슨 의미인지 모름
setupTestRepository(t)
setupTestHandler(t)
createCommit(t, ...)        // 무슨 파일? 무슨 의미의 커밋?
setup(t)
```

### 3.3 Helper 필수 규칙

| 규칙 | 설명 |
|------|------|
| **`t.Helper()` 필수** | 모든 helper 함수는 **첫 줄에 `t.Helper()`** |
| **파일 분리 권장** | `testutil/git.go` (arrange), `testutil/assert.go` (assert) |

```go
// ✅ 모든 helper는 t.Helper() 필수
func newRepo(t *testing.T) *gogit.Repository {
    t.Helper()  // ← 첫 줄
    // ...
}

func requireStatus(t *testing.T, resp *httptest.ResponseRecorder, want int) {
    t.Helper()  // ← 첫 줄
    // ...
}
```

### 3.4 Helper가 절대 하면 안 되는 것

| 금지 | 이유 |
|------|------|
| **When(행동)을 helper 안에 숨기기** | 테스트의 핵심 변화는 inline으로 보여야 함 |
| **Assert를 helper 안에 숨기기** | assert는 테스트 본문에서 "스펙"으로 보여야 함 |
| **`t.Helper()` 누락** | 실패 시 스택트레이스가 helper 내부를 가리킴 |

---

## 4. "상태 변화"는 한 줄로 보여야 한다

### 핵심 원칙

테스트의 **transition(상태 변화) 지점**을 줄 단위로 고정.  
transition 라인은 **반드시 동사로 시작**.

```go
// Given
repo := newRepo(t)
mainHash := commitFile(t, repo, "init.txt", "init", "initial")
createBranchAt(t, repo, "feature/foo", mainHash)

// When
removeBranch(t, repo, "feature/foo")  // ← 상태 변화 지점

// Then
resp := callMergePreview(t, router, "feature/foo", "main")
requireStatus(t, resp, 404)
requireNotStatus(t, resp, 409)
```

### 흐름이 "눈으로" 보여야 함

```
브랜치가 있다 → 삭제한다 → preview 호출 → 404여야 한다
```

이 흐름이 코드에서 **한 줄씩** 읽혀야 함.

---

## 5. Assertion 규칙

### 5.1 핵심 원칙: "정답을 강제"한다

> **절대 금지**: "특정 코드면 버그"처럼 **부분 조건만 체크**하는 어설션

```go
// ❌ 금지 - "특정 값만 나쁘다" (409만 나쁘다)
if got == 409 {
    t.Error("...")
}

// ❌ 금지 - "범위/카테고리로 뭉뚱그림" (5xx만 막고 200/404 섞이면 통과)
if status >= 500 {
    t.Error("server error")
}

// ❌ 금지 - 애매한 범위 통과
if status >= 200 && status < 300 {
    // 2xx면 다 OK? → 204 vs 200 구분 못함
}

// ✅ 좋음 - 정답을 고정
requireStatus(t, resp, http.StatusNotFound)           // 핵심: "404여야 함"
requireNotStatus(t, resp, http.StatusConflict)        // 보조: 옛날 버그 가드 (optional)
```

### 5.2 requireNotStatus는 "보조 가드 전용"

> **단독 사용 금지**: `requireNotStatus`는 반드시 `requireStatus`와 함께 쓴다.

```go
// ❌ 금지 - requireNotStatus 단독 사용
requireNotStatus(t, resp, http.StatusConflict)  // 409 아니면 뭐든 통과

// ✅ 좋음 - requireStatus가 핵심, requireNotStatus는 보조
requireStatus(t, resp, http.StatusNotFound)           // 핵심: 정답 강제
requireNotStatus(t, resp, http.StatusConflict)        // 보조: 과거 회귀 방지
```

> `requireNotStatus`는 `t.Errorf`(non-fatal)이므로, `requireStatus`가 이미 fail이면 의미가 없다. 메시지 강화/문서화 용도로만 사용.

### 5.3 한 테스트는 "핵심 assertion 1개 + 보조 assertion 몇 개"

```go
// 핵심: 정답을 강제
requireStatus(t, resp, http.StatusNotFound)

// 보조: 옛날 버그 가드 (optional)
requireNotStatus(t, resp, http.StatusConflict)

// 보조: error body shape
requireBodyContains(t, resp, "unable to resolve reference")
```

### 5.3 requireStatus 구현 (팀 표준)

```go
func requireStatus(t *testing.T, resp *httptest.ResponseRecorder, want int) {
    t.Helper()
    if resp.Code != want {
        t.Fatalf("status=%d want=%d body=%s", resp.Code, want, resp.Body.String())
    }
}

func requireNotStatus(t *testing.T, resp *httptest.ResponseRecorder, notWant int) {
    t.Helper()
    if resp.Code == notWant {
        t.Errorf("status=%d (should not be %d) body=%s", resp.Code, notWant, resp.Body.String())
    }
}
```

### 5.4 require vs assert

| 사용 | 조건 |
|------|------|
| `require.*` / `t.Fatalf` | 조건이 깨지면 테스트 진행 의미 없음 |
| `assert.*` / `t.Errorf` | 계속 진행해도 됨 |

### 5.5 에러 메시지 템플릿

```go
// ✅ 좋음 - 디버깅에 필요한 정보 포함
t.Fatalf("status=%d want=%d body=%s", got, want, body)

// ❌ 나쁨 - 무슨 상황인지 모름
t.Fatal("wrong status")
```

**항상 body를 넣어라.** 디버깅 속도가 달라진다.

---

## 6. Real Test 우선 원칙

> **"mock 금지"가 아니라 mock의 사용 위치를 고정**

### 6.1 테스트 레벨 정의 (팀 공통 언어)

| 레벨 | 설명 | mock 사용 |
|------|------|-----------|
| **L0** | Pure function/unit | 거의 없음 |
| **L1** | Component test (실제 repo/handler, 외부 네트워크 없음) | 최소 |
| **L2** | End-to-end (실제 서버, 별도 suite) | 외부만 |

### 6.2 mock 허용 구역

| 허용 | 예시 |
|------|------|
| 시간/랜덤/UUID 같은 **비결정성** | `time.Now()`, `uuid.New()` |
| **외부 네트워크/외부 SaaS** | HTTP client to external API |
| **느린 것** 대신 in-memory 대체 | 진짜 DB → in-memory |

**그 외는 기본 real.**

---

## 7. 결정성(Determinism) 규칙

> **flaky 테스트를 구조적으로 막기**

### 7.1 시간 고정

```go
// ✅ 좋음 - 테스트 상단에서 시간 고정
now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// 커밋 Author.When에 고정된 시간 사용
commitFile(t, repo, "a.txt", "x", "msg", now)

// ❌ 나쁨 - 비결정적
time.Now()
```

### 7.2 파일/경로

```go
// ✅ 좋음 - 격리된 temp dir
tempDir := t.TempDir()

// ❌ 나쁨 - 전역/상대 경로
os.TempDir()
"./testdata"
"/tmp/test"
```

### 7.3 goroutine/비동기

```go
// goroutine 사용 시 반드시 cleanup
t.Cleanup(func() {
    // 종료 보장
})
```

### 7.4 전역 상태/환경 변수

> **flaky 만드는 1순위**: 전역 상태가 병렬 테스트에서 공유됨

| 위험 요소 | 해결책 |
|-----------|--------|
| 환경변수/전역 설정 (gin mode, timezone) | `t.Cleanup`으로 원복 |
| HTTP 서버의 전역 라우터/미들웨어 | 테스트별 router 인스턴스 생성 |
| 병렬 테스트에서 공유되는 글로벌 상태 | `t.Parallel()` 사용 제한 |

```go
// ✅ 전역 상태 변경 시 반드시 원복
func Test_Something(t *testing.T) {
    original := os.Getenv("MY_VAR")
    os.Setenv("MY_VAR", "test_value")
    t.Cleanup(func() {
        os.Setenv("MY_VAR", original)
    })
    // ...
}

// ✅ gin mode는 테스트 시작 시 고정
func init() {
    gin.SetMode(gin.TestMode)
}
```

### 7.5 t.Parallel() 허용 조건

> **"공유 전역이 없는 테스트"에서만 `t.Parallel()` 허용**

```go
// ✅ 허용 - 공유 상태 없음
func Test_PureLogic(t *testing.T) {
    t.Parallel()
    // 전역 상태 접근 없음
}

// ❌ 금지 - 전역 상태 변경
func Test_GlobalState(t *testing.T) {
    // t.Parallel()  ← 금지
    os.Setenv("KEY", "value")
    // ...
}
```

---

## 8. 파일/패키지 구조 규칙

### 8.1 권장 구조

| 테스트 유형 | 패키지 | 설명 |
|-------------|--------|------|
| **Black-box** (외부 API만 사용) | `package xxx_test` | 진짜 사용자처럼 테스트 |
| **White-box** (내부 상태까지 검증) | `package xxx` | 내부 구현 테스트 |

### 8.2 파일 위치

- `*_test.go`는 대상 코드와 같은 디렉토리에 위치
- 핸들러/라우터 중심이면 `package handler_test` 형태로 "진짜 사용자처럼" 요청

---

## 9. Golden 템플릿 (복붙용)

### 표준형 (권장)

```go
func Test_<Unit>_<Scenario>_<Expect>(t *testing.T) {
    t.Parallel()

    // Given: ...

    // When: ...

    // Then: ...
    requireStatus(t, resp, http.StatusNotFound)
}
```

### 극단 단순형 (허용 조건 충족 시)

```go
func Test_MergePreview_DeletedSourceRef_Returns404(t *testing.T) {
    t.Parallel()

    // Given: repo with feature branch
    repo := makeRepoWithBranch(t, "feature/foo")

    // When: delete source branch
    removeBranch(t, repo, "feature/foo")

    // Then: merge preview should be 404 (ref not found)
    resp := callMergePreview(t, repo, "feature/foo", "main")

    requireStatus(t, resp, http.StatusNotFound)
    requireNotStatus(t, resp, http.StatusConflict) // optional: guards old bug
}
```

---

## 9.5. 극단 단순 스타일 허용 조건

> **"짧게 쓰는 건 OK, 약하게 쓰는 건 NO"**

### 허용 조건 (모두 충족해야 함)

| 조건 | 설명 |
|------|------|
| L1 테스트 | Component test (실제 repo/handler, 외부 네트워크 없음) |
| 상태 변화 3개 이하 | 단순한 시나리오만 |
| 테스트 이름이 "정답 스펙" | `_Not409` 같은 부정 기대 금지 |
| 어설션이 "정답 강제" | `requireStatus(...)` 필수, `if got == 409` 금지 |
| 실패 메시지에 body 포함 | 디버깅 가능해야 함 |

### 허용되는 극단형

```go
func Test_MergePreview_DeletedSourceRef_Returns404(t *testing.T) {
    repo := makeRepoWithBranch(t, "feature/foo")
    removeBranch(t, repo, "feature/foo")
    resp := callMergePreview(t, repo, "feature/foo", "main")

    requireStatus(t, resp, http.StatusNotFound)    // ✅ 정답 강제
}
```

### 금지되는 극단형

```go
// ❌ 이름이 버그 기준
func Test_DeletedBranch_Gets409_ShouldBe404(t *testing.T) { ... }

// ❌ 부분 조건만 체크 (약한 어설션)
func Test_MergePreview_DeletedSourceRef_Returns404(t *testing.T) {
    // ...
    if resp.Code == 409 {   // ← 409가 아니면 뭐든 통과
        t.Error("BUG")
    }
}
```

---

## 10. 에러 핸들링 규칙

### 절대 금지: 에러 무시

```go
// ❌ 나쁨 - 에러 무시
repo, _ := gogit.PlainInit(tempDir, false)
wt, _ := repo.Worktree()

// ✅ 좋음 - 에러 체크
repo, err := gogit.PlainInit(tempDir, false)
if err != nil {
    t.Fatalf("repo init failed: %v", err)
}
```

### 에러 체크 패턴

```go
// testify 사용 시
require.NoError(t, err, "repo init failed")

// 표준 라이브러리만 사용 시
if err != nil {
    t.Fatalf("repo init failed: %v", err)
}
```

---

## 11. 실제 예시: 완전한 테스트

```go
package merge_test

import (
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "strings"
    "testing"
    "time"

    "github.com/gin-gonic/gin"
    gogit "github.com/go-git/go-git/v5"
    "github.com/go-git/go-git/v5/plumbing"
    "github.com/go-git/go-git/v5/plumbing/object"
)

func Test_MergePreview_DeletedSourceRef_Returns404(t *testing.T) {
    t.Parallel()

    // Given: feature 브랜치가 있는 repo
    tempDir := t.TempDir()
    repo, err := gogit.PlainInit(tempDir, false)
    if err != nil {
        t.Fatalf("repo init failed: %v", err)
    }

    wt, err := repo.Worktree()
    if err != nil {
        t.Fatalf("worktree failed: %v", err)
    }

    // main 커밋
    now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
    mainHash := commitFile(t, wt, tempDir, "init.txt", "init", "initial commit", now)
    
    // feature 브랜치 생성 및 커밋
    createBranchAt(t, repo, "feature/foo", mainHash)
    checkoutBranch(t, wt, "feature/foo")
    featureHash := commitFile(t, wt, tempDir, "feat.txt", "feat", "feature commit", now)
    updateBranch(t, repo, "feature/foo", featureHash)

    // And: 브랜치가 삭제됨
    removeBranch(t, repo, "feature/foo")

    // When: 삭제된 브랜치로 MergePreview 호출
    gin.SetMode(gin.TestMode)
    router := gin.New()
    // handler 등록 (실제 코드에 맞게 수정)
    // router.GET("/merges/preview", handler.HandleMergePreview)

    req := httptest.NewRequest(
        http.MethodGet,
        "/merges/preview?source_ref=feature/foo&target_ref=main",
        nil,
    )
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)

    // Then: 404 반환 (ref not found)
    requireStatus(t, w, http.StatusNotFound)              // 핵심: 정답 강제
    requireNotStatus(t, w, http.StatusConflict)           // 보조: 옛날 버그 가드
}

// --- Helper functions ---
// 의도: boilerplate만 추상화, 핵심 로직은 테스트 본문에 유지

func commitFile(t *testing.T, wt *gogit.Worktree, repoPath, filename, content, message string, when time.Time) plumbing.Hash {
    t.Helper()

    filePath := filepath.Join(repoPath, filename)
    if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
        t.Fatalf("mkdir failed: %v", err)
    }
    if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
        t.Fatalf("write file failed: %v", err)
    }
    if _, err := wt.Add(filename); err != nil {
        t.Fatalf("git add failed: %v", err)
    }

    hash, err := wt.Commit(message, &gogit.CommitOptions{
        Author: &object.Signature{
            Name:  "Test",
            Email: "test@test.com",
            When:  when,
        },
    })
    if err != nil {
        t.Fatalf("commit failed: %v", err)
    }
    return hash
}

func createBranchAt(t *testing.T, repo *gogit.Repository, name string, hash plumbing.Hash) {
    t.Helper()
    ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName(name), hash)
    if err := repo.Storer.SetReference(ref); err != nil {
        t.Fatalf("create branch %s failed: %v", name, err)
    }
}

func updateBranch(t *testing.T, repo *gogit.Repository, name string, hash plumbing.Hash) {
    t.Helper()
    ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName(name), hash)
    if err := repo.Storer.SetReference(ref); err != nil {
        t.Fatalf("update branch %s failed: %v", name, err)
    }
}

func checkoutBranch(t *testing.T, wt *gogit.Worktree, name string) {
    t.Helper()
    if err := wt.Checkout(&gogit.CheckoutOptions{
        Branch: plumbing.NewBranchReferenceName(name),
    }); err != nil {
        t.Fatalf("checkout %s failed: %v", name, err)
    }
}

func removeBranch(t *testing.T, repo *gogit.Repository, name string) {
    t.Helper()
    if err := repo.Storer.RemoveReference(plumbing.NewBranchReferenceName(name)); err != nil {
        t.Fatalf("remove branch %s failed: %v", name, err)
    }
}

// --- Assertion helpers ---
// 핵심: "정답을 강제"하고, 실패 메시지에 body 포함

func requireStatus(t *testing.T, resp *httptest.ResponseRecorder, want int) {
    t.Helper()
    if resp.Code != want {
        t.Fatalf("status=%d want=%d body=%s", resp.Code, want, resp.Body.String())
    }
}

func requireNotStatus(t *testing.T, resp *httptest.ResponseRecorder, notWant int) {
    t.Helper()
    if resp.Code == notWant {
        t.Errorf("status=%d (should not be %d) body=%s", resp.Code, notWant, resp.Body.String())
    }
}

func requireBodyContains(t *testing.T, resp *httptest.ResponseRecorder, substr string) {
    t.Helper()
    if !strings.Contains(resp.Body.String(), substr) {
        t.Errorf("body does not contain %q: %s", substr, resp.Body.String())
    }
}
```

---

## 12. PR 리뷰 체크리스트

PR 리뷰에서 **아래 14개** 확인:

| # | 체크 항목 |
|---|-----------|
| 1 | 테스트 이름이 `Unit + Scenario + Expect` (정답 기준) 인가? |
| 2 | 테스트 이름에 `_Not409` 같은 **부정 기대**가 없는가? |
| 3 | `Given/When/Then` 3섹션인가? |
| 4 | **When이 1개**인가? (예외: 인프라 준비만 허용) |
| 5 | 핵심 상태 변화가 helper에 숨지 않았는가? |
| 6 | 에러를 무시(`_, _ :=`)하지 않았는가? |
| 7 | **assertion이 "정답을 강제"**하는가? (`if got == 409` 금지) |
| 8 | **`requireNotStatus` 단독 사용** 없는가? (반드시 `requireStatus`와 함께) |
| 9 | assertion 실패 메시지에 status/body가 포함되는가? |
| 10 | `time.Now`/랜덤이 고정되어 있는가? |
| 11 | I/O는 `t.TempDir()` 기반인가? |
| 12 | **helper에 `t.Helper()` 첫 줄**에 있는가? |
| 13 | 이 테스트가 L0/L1/L2 중 어디인지 명확한가? |
| 14 | helper 이름이 `setup*`가 아니고 **의미를 말하는가**? |

### 특히 주의: Assertion 금지 패턴

```go
// ❌ PR에서 발견하면 반드시 수정 요청
if got == 409 { t.Error(...) }                    // "특정 값만 나쁘다"
if status >= 500 { t.Error(...) }                 // "범위로 뭉뚱그림"
requireNotStatus(t, resp, http.StatusConflict)    // 단독 사용 (정답 없음)

// ✅ 이렇게 수정
requireStatus(t, resp, http.StatusNotFound)       // 정답 강제 (핵심)
requireNotStatus(t, resp, http.StatusConflict)    // 보조 가드 (optional)
```

---

## 13. 요약: 읽히는 테스트의 4가지 핵심

1. **이름은 "정답 스펙"**: `Test_<Unit>_<Scenario>_<Expect>` (부정 기대 금지)
2. **상태 변화가 눈에 보임**: Given → When → Then이 한 줄씩
3. **helper는 boilerplate만**: 핵심 로직은 inline
4. **assertion은 정답 강제**: `requireStatus(...)` 필수, `if got == 409` 금지

> **"짧게 쓰는 건 OK, 약하게 쓰는 건 NO"**

> **"테스트가 스펙처럼 읽히면, 버그도 스펙처럼 보인다."**

---

## 14. 절대 금지 목록 (Quick Reference)

| 금지 | 대신 |
|------|------|
| `if got == 409` | `requireStatus(t, resp, 404)` |
| `if status >= 500` | `requireStatus(t, resp, 404)` |
| `requireNotStatus` 단독 사용 | `requireStatus` + `requireNotStatus` |
| `Test_...Gets409_ShouldBe404` | `Test_...Returns404` |
| `setup*` helper 이름 | `newRepo(t)`, `commitFile(t, ...)` |
| helper에서 `t.Helper()` 누락 | 첫 줄에 `t.Helper()` |
| `_, _ :=` 에러 무시 | `if err != nil { t.Fatal(...) }` |
| assert 메시지에 body 없음 | `t.Fatalf("status=%d want=%d body=%s", ...)` |
| `time.Now()` 직접 사용 | `now := time.Date(...)` 고정 |
| 전역/상대 경로 | `t.TempDir()` |
| 전역 상태 변경 후 원복 없음 | `t.Cleanup(...)` |
| 전역 상태 있는데 `t.Parallel()` | `t.Parallel()` 제거 또는 상태 격리 |