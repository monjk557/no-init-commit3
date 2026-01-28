# Go 테스트 작성 가이드

> **Go 철학**: 테스트도 코드다. **코드 자체가 읽혀야 한다.** 주석이나 형식이 아니라, **이름과 구조로 의도를 전달**한다.

참고: [GopherCon 세션 정리](../gophercon/notes.md) - Go의 최소 추상 철학, mock보다 real test

---

## 0. Go-스러운 테스트 원칙 (10줄)

```
1. real test 우선            mock은 외부 의존/비결정성에서만
2. t.Parallel() 권장         없으면 이유 주석 필수
3. 이름이 스펙                Test_<Unit>_<Scenario>_<Expect>
4. 코드가 문서                주석 없이도 읽혀야 함 (검증도 코드로)
5. 정답 강제                  requireStatus(t, resp, 404)
6. 에러 무시 금지             _, _ := 금지
7. t.TempDir()               격리된 환경
8. 시간 고정                  비결정성 제거
9. 의도를 숨기지 말라         helper는 boilerplate만
10. 실패 메시지에 context     status=%d want=%d body=%s
11. 테스트용 코드 프로덕션 금지  테스트에서만 쓰이는 함수/메서드를 프로덕션 코드에 넣지 않는다
```

**핵심**: mock 대신 **실제 서버/repo로 테스트**, 추상화보다 **inline 우선**

> ⚠️ **11번 원칙 상세**: 테스트를 위해 프로덕션 코드에 함수를 추가하면:
> - 실제 API 동작을 검증하지 않음
> - 프로덕션 코드 복잡도 증가
> - 테스트가 통과해도 실제 버그를 못 잡음
> - **해결**: HTTP 엔드포인트 테스트 (실제 요청/응답 검증)

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
    assertNotStatus(t, resp, http.StatusConflict)        // 보조: 옛날 버그 가드
}

// ❌ 나쁨 - 이름에 부정 기대
func Test_MergePreview_DeletedSourceRef_Returns404_Not409(t *testing.T) { ... }
```

---

## 2. 테스트 구조: 코드가 문서다

### Go 원칙: 주석이 아니라 **코드 자체가 읽혀야 한다**

```go
// ✅ Go-스러움: 코드만 봐도 의도가 명확 (검증까지 코드로)
func Test_CreateBranch_DuplicateName_Returns409(t *testing.T) {
    t.Parallel()

    repo := newRepo(t)
    createBranch(t, repo, "feature")          // helper가 내부에서 성공 보장
    resp := createBranch(t, repo, "feature")  // 같은 이름으로 또 생성

    requireStatus(t, resp, http.StatusConflict)  // 검증도 코드
}

// ❌ 주석만 있고 검증이 없음
func Test_CreateBranch_DuplicateName_Returns409(t *testing.T) {
    repo := newRepo(t)
    createBranch(t, repo, "feature")
    createBranch(t, repo, "feature")
    // → 409 기대  ← 검증이 주석? 안 됨
}
```

### 주석은 **선택적**

- 코드가 읽히면 주석 생략 가능
- 복잡한 시나리오에서만 Given/When/Then 사용
- **빈 줄**로 구조 구분 (주석 대신)

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

#### testutil 사용 버전 (권장)

```go
import "github.com/collabops/git-executor/internal/testutil/gitrepo"

func Test_MergePreview_DeletedSourceRef_Returns404(t *testing.T) {
    t.Parallel()

    // Given: feature 브랜치가 있는 repo
    repo := gitrepo.NewRepo(t)
    repo.Commit("init.txt", "init", "initial commit")
    repo.BranchCreate("feature/foo")
    repo.Commit("feat.txt", "feat", "feature commit")
    repo.BranchCheckout("main")

    // And: 브랜치가 삭제됨
    repo.BranchDelete("feature/foo")

    // When: 삭제된 브랜치로 MergePreview 호출
    resp := callMergePreview(t, repo, "feature/foo", "main")

    // Then: 404 반환 (ref not found)
    gitrepo.Require404RefNotFound(t, resp)  // status + error.code 동시 검증
    gitrepo.AssertNo500(t, resp)            // 500 방지
}
```

#### 직접 helper 사용 버전 (선택적)

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
    assertNotStatus(t, resp, http.StatusConflict)        // 보조: 옛날 버그 가드
}
```

---

## 3. Helper(추상화) 정책

> **Go 철학**: 추상화는 최소로. **inline이 기본**, helper는 **boilerplate 제거용**만.

### 3.1 Go-스러운 테스트: 추상화 피하기

```go
// ✅ Go-스러움: 테스트 본문에서 모든 것이 보임
func Test_DeleteBranch_NotFound_Returns404(t *testing.T) {
    t.Parallel()
    
    tempDir := t.TempDir()
    repo, err := git.PlainInit(tempDir, false)
    require.NoError(t, err)
    
    // 존재하지 않는 브랜치 삭제 시도
    err = repo.Storer.RemoveReference(plumbing.NewBranchReferenceName("ghost"))
    
    require.Error(t, err)
}

// ❌ 과도한 추상화: 무슨 일이 일어나는지 안 보임
func Test_DeleteBranch_NotFound_Returns404(t *testing.T) {
    repo := setupTestRepoWithBranches(t, "main", "feature")
    err := deleteBranchHelper(t, repo, "ghost")
    assertBranchNotFoundError(t, err)
}
```

### 3.2 Helper 허용 기준

| 기준 | 허용 | 금지 |
|------|------|------|
| **순수 boilerplate** | `t.TempDir()`, `httptest.NewRecorder()` | - |
| **의도를 숨기지 않음** | `newRepo(t)`, `createBranchAt(t, repo, name, hash)` | `setupTestRepoWithBranches(t, ...)` |
| **목적어가 구체적** | `createUser(t, store, User{...})` | `create(t)`, `createCommit(t)` (뭘 만드는지 불명확) |

> **핵심**: 줄 수가 아니라 **"의도를 숨기는가"**가 기준. 핵심 전제/상태변화/검증은 숨기면 안 됨.

### 3.3 Helper 네이밍 규칙

`setup*` **금지**. `create*`/`new*`는 **목적어가 구체적이면 허용**.

```go
// ✅ 좋음 - 목적어가 구체적
newRepo(t)
createBranchAt(t, repo, "feature", hash)
createUser(t, store, User{Name: "test"})
commitFile(t, repo, "a.txt", "content", "msg")

// ❌ 나쁨 - 뭘 만드는지/하는지 불명확
setupTestRepository(t)      // setup이 뭘 하는지 모름
create(t)                   // 뭘 만드는지 모름
createCommit(t)             // 무슨 파일? 무슨 의미?
```

### 3.4 t.Helper() 필수

```go
func newRepo(t *testing.T) *git.Repository {
    t.Helper()  // ← 첫 줄에 필수
    // ...
}
```

### 3.5 파일 분리: 선택적 (inline 우선, 중복 시 분리)

> **Go 철학**: 패키지 분리보다 **inline 우선**. helper가 테스트 파일에 같이 있어도 됨.
> **단, 여러 패키지에서 동일 helper가 반복되면 `testutil`로 분리**한다.

```go
// ✅ 권장: 같은 파일에 helper 정의 (패키지 내 1-2개)
func Test_Something(t *testing.T) { ... }

func newRepo(t *testing.T) *git.Repository { ... }  // 같은 파일

// ⚠️ 여러 패키지에서 중복 시: testutil로 분리
// internal/testutil/gitrepo/assertions.go
//   - RequireStatus, RequireBodyContains, RequireNo500 등
```

**분리 기준**:
- 패키지 **내** 1-2개 helper → inline OK
- **3개 이상 패키지**에서 동일 helper → `testutil`로 분리

```go
// 각 패키지에서는 wrapper로 호출
func requireStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
    t.Helper()
    gitrepo.RequireStatus(t, w, want)  // testutil 사용
}
```

### 3.6 testutil 패키지 사용 (권장)

> **프로젝트 표준**: `internal/testutil/gitrepo` 패키지는 **공유 helper**로 제공되며, **사용을 권장**합니다.

#### 3.6.1 gitrepo 패키지 사용

```go
import "github.com/collabops/git-executor/internal/testutil/gitrepo"

// ✅ 권장: testutil 사용
func Test_MergePreview_DeletedBranch_Returns404(t *testing.T) {
    t.Parallel()
    
    repo := gitrepo.NewRepo(t)  // 자동으로 t.TempDir() + git init + HEAD 설정
    repo.Commit("file.txt", "content", "Initial commit")
    repo.BranchCreate("feature")
    repo.Commit("feature.txt", "feature", "Feature commit")
    repo.BranchCheckout("main")
    repo.BranchDelete("feature")
    
    // 테스트 실행...
}
```

**장점**:
- **Deterministic**: 시간 자동 고정 (`baseTime` + `timeStep`)
- **간결성**: boilerplate 제거 (git init, HEAD 설정, user config 등)
- **일관성**: 모든 테스트에서 동일한 레포 초기 상태
- **에러 처리**: 내부에서 `require.NoError`로 실패 시 즉시 종료

#### 3.6.2 gitrepo 주요 메서드

```go
// Repository 생성
repo := gitrepo.NewRepo(t)        // 일반 레포
bareRepo := gitrepo.NewBareRepo(t) // bare 레포

// 커밋
hash := repo.Commit("file.txt", "content", "message")
hash := repo.CommitWithTime("file.txt", "content", "message", time.Date(...))
hash := repo.CommitMultiple(map[string]string{"a.txt": "A", "b.txt": "B"}, "msg")

// 브랜치
repo.BranchCreate("feature")
repo.BranchCheckout("main")
repo.BranchDelete("feature")
repo.BranchRename("old", "new")

// 태그
repo.TagLightweight("v1.0.0", hash)
tagHash := repo.TagAnnotated("v1.0.0", "Release", hash)
repo.TagDelete("v1.0.0")

// 파일 조작 (커밋 전)
repo.SetFile("file.txt", "content")
repo.DeleteFile("file.txt")
repo.StageFile("file.txt")
repo.StageRemove("file.txt")

// 조회
headHash := repo.HeadHash()
commit := repo.GetCommit(hash)
hasBranch := repo.HasBranch("feature")
hasTag := repo.HasTag("v1.0.0")
```

#### 3.6.3 testutil assertion 사용

```go
import "github.com/collabops/git-executor/internal/testutil/gitrepo"

// ✅ 권장: testutil assertion 사용
func Test_MergePreview_InvalidRef_Returns400(t *testing.T) {
    t.Parallel()
    
    setup := setupMergePreviewTest(t)
    resp := setup.doPreview("invalid ref", "main")
    
    // testutil assertion 사용 (status + error.code 한번에 검증)
    gitrepo.Require400InvalidRef(t, resp)
    // 또는
    gitrepo.RequireStatus(t, resp, http.StatusBadRequest)
    gitrepo.RequireErrCode(t, resp, "INVALID_REF")
}
```

**주요 assertion 함수**:
```go
// 기본 검증
gitrepo.RequireStatus(t, resp, http.StatusNotFound)
gitrepo.AssertStatus(t, resp, http.StatusOK)

// 에러 코드 검증 (status + error.code 동시 검증)
gitrepo.Require400InvalidRef(t, resp)
gitrepo.Require400UnsupportedRef(t, resp)
gitrepo.Require404RefNotFound(t, resp)
gitrepo.Require404ObjectNotFound(t, resp)
gitrepo.Require403PolicyViolation(t, resp)
gitrepo.Require409ConcurrentUpdate(t, resp)

// 500 방지 (모든 테스트에 권장)
gitrepo.RequireNo500(t, resp)  // fatal
gitrepo.AssertNo500(t, resp)   // non-fatal

// Retry-After 헤더 검증
gitrepo.RequireRetryAfter(t, resp)

// 스키마 안정성
gitrepo.RequireStableSchema(t, resp, "success", "data", "error")
gitrepo.RequireJSONField(t, resp, "error.code", "INVALID_REF")
```

**장점**:
- **일관성**: 모든 테스트에서 동일한 assertion 사용
- **간결성**: 여러 검증을 한 줄로 처리
- **에러 메시지**: 실패 시 body 자동 포함
- **회귀 방지**: 500 에러 방지 검증 자동 포함

#### 3.6.4 testutil vs 직접 helper

| 상황 | 권장 방법 |
|------|----------|
| **Git 레포 생성/조작** | `gitrepo.NewRepo(t)`, `repo.Commit()`, `repo.BranchCreate()` 등 |
| **HTTP assertion** | `gitrepo.Require400InvalidRef()`, `gitrepo.RequireStatus()` 등 |
| **패키지별 특수 helper** | 테스트 파일에 직접 정의 (inline 우선) |
| **공유 helper가 많을 때** | testutil 패키지에 추가 고려 |

> **원칙**: testutil은 **boilerplate 제거용**으로 사용. 핵심 로직은 여전히 테스트 본문에 inline으로 유지.

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
assertNotStatus(t, resp, 409)
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
assertNotStatus(t, resp, http.StatusConflict)        // 보조: 옛날 버그 가드 (optional)
```

### 5.2 assertNotStatus는 "보조 가드 전용"

> **단독 사용 금지**: `assertNotStatus`는 반드시 `requireStatus`와 함께 쓴다.

```go
// ❌ 금지 - assertNotStatus 단독 사용
assertNotStatus(t, resp, http.StatusConflict)  // 409 아니면 뭐든 통과

// ✅ 좋음 - requireStatus가 핵심, assertNotStatus는 보조
requireStatus(t, resp, http.StatusNotFound)           // 핵심: 정답 강제
assertNotStatus(t, resp, http.StatusConflict)        // 보조: 과거 회귀 방지
```

> `assertNotStatus`는 `t.Errorf`(non-fatal)이므로, `requireStatus`가 이미 fail이면 의미가 없다. 메시지 강화/문서화 용도로만 사용.

### 5.3 한 테스트는 "핵심 assertion 1개 + 보조 assertion 몇 개"

```go
// 핵심: 정답을 강제
requireStatus(t, resp, http.StatusNotFound)

// 보조: 옛날 버그 가드 (optional)
assertNotStatus(t, resp, http.StatusConflict)

// 보조: error body shape
requireBodyContains(t, resp, "unable to resolve reference")
```

### 5.3 TestResponse: HTTP 테스트의 표준 타입

> **규칙**: HTTP 레벨은 **무조건 TestResponse**. `*httptest.ResponseRecorder`는 raw로만.

```go
// 표준 타입 정의
type TestResponse struct {
    Status int
    Body   string
    Header http.Header
}

func asResp(w *httptest.ResponseRecorder) TestResponse {
    return TestResponse{
        Status: w.Code,
        Body:   w.Body.String(),
        Header: w.Header(),
    }
}
```

### 5.4 Assertion 구현 (팀 표준)

> **권장**: `internal/testutil/gitrepo` 패키지의 assertion 함수 사용. 직접 구현보다 표준 함수 사용을 우선.

#### testutil 사용 (권장)

```go
import "github.com/collabops/git-executor/internal/testutil/gitrepo"

// ✅ 권장: testutil assertion 사용
gitrepo.RequireStatus(t, resp, http.StatusNotFound)
gitrepo.Require400InvalidRef(t, resp)  // status + error.code 동시 검증
gitrepo.AssertNo500(t, resp)          // 500 방지
```

#### 직접 구현 (선택적)

> **네이밍 규칙**: 정답은 `require*` (fatal), 회귀 가드는 `assert*` (non-fatal)

```go
// 정답 강제 (fatal)
func requireStatus(t *testing.T, resp TestResponse, want int) {
    t.Helper()
    if resp.Status != want {
        t.Fatalf("status=%d want=%d body=%s", resp.Status, want, resp.Body)
    }
}

// 회귀 가드 (non-fatal, 단독 사용 금지)
func assertNotStatus(t *testing.T, resp TestResponse, notWant int) {
    t.Helper()
    if resp.Status == notWant {
        t.Errorf("status=%d (should not be %d) body=%s", resp.Status, notWant, resp.Body)
    }
}

// 확장
func requireHeader(t *testing.T, resp TestResponse, key, want string) { ... }
func requireBodyContains(t *testing.T, resp TestResponse, substr string) { ... }
```

### 5.5 사용 패턴

```go
// ✅ 표준 패턴: raw → TestResponse → assert
w := httptest.NewRecorder()
router.ServeHTTP(w, req)
resp := asResp(w)

requireStatus(t, resp, http.StatusNotFound)      // 정답 강제
assertNotStatus(t, resp, http.StatusConflict)    // 회귀 가드 (선택)
```

### 5.6 (선택) 단독 사용 컴파일 레벨 금지

> assertNotStatus 단독 사용을 **컴파일 타임에 막고 싶으면**:

```go
type MustExpect struct{}

func requireStatus(t *testing.T, resp TestResponse, want int) MustExpect {
    t.Helper()
    if resp.Status != want {
        t.Fatalf("status=%d want=%d body=%s", resp.Status, want, resp.Body)
    }
    return MustExpect{}
}

func assertNotStatus(t *testing.T, _ MustExpect, resp TestResponse, notWant int) {
    t.Helper()
    if resp.Status == notWant {
        t.Errorf("status=%d (should not be %d) body=%s", resp.Status, notWant, resp.Body)
    }
}

// 사용: 반드시 requireStatus 먼저 호출해야 컴파일됨
exp := requireStatus(t, resp, http.StatusNotFound)
assertNotStatus(t, exp, resp, http.StatusConflict)
```

### 5.7 require vs assert

| 사용 | 조건 |
|------|------|
| `require.*` / `t.Fatalf` | 조건이 깨지면 테스트 진행 의미 없음 |
| `assert.*` / `t.Errorf` | 계속 진행해도 됨 |

### 5.8 에러 메시지 템플릿

```go
// ✅ 좋음 - 디버깅에 필요한 정보 포함
t.Fatalf("status=%d want=%d body=%s", got, want, body)

// ❌ 나쁨 - 무슨 상황인지 모름
t.Fatal("wrong status")
```

**항상 body를 넣어라.** 디버깅 속도가 달라진다.

---

## 6. Real Test 우선 원칙

> **Go 철학**: mock보다 **실제 서버/repo로 테스트**한다. mock은 **외부 의존/비결정성에서만**.

### 6.1 핵심: Real Test가 기본

```go
// ✅ Go-스러움: 실제 repo로 테스트
func Test_CreateBranch_Success(t *testing.T) {
    t.Parallel()
    repo := newRepo(t)                    // 실제 git repo
    createBranch(t, repo, "feature")
    // 실제 동작 검증
}

// ❌ 안티패턴: mock으로 시작
func Test_CreateBranch_Success(t *testing.T) {
    mockRepo := &MockRepo{}
    mockRepo.On("CreateBranch", ...).Return(nil)
    // mock이 실제 동작을 보장하지 않음
}
```

### 6.2 TestMain 패턴: L2에서만 신중하게

> **⚠️ 주의**: TestMain 서버 공유는 **L2 (E2E) suite에서만**. L1은 테스트마다 router 인스턴스가 기본.

```go
// ✅ L1 기본: 테스트마다 격리된 router
func Test_CreateBranch_Success(t *testing.T) {
    t.Parallel()
    router := newRouter()  // 테스트마다 새 인스턴스
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)
}

// ⚠️ L2에서만: 무겁고 느린 E2E suite
var testServer *httptest.Server

func TestMain(m *testing.M) {
    // L2: 완전 무상태(stateless) 서버만 공유
    testServer = httptest.NewServer(newRouter())
    code := m.Run()
    testServer.Close()
    os.Exit(code)
}
```

**TestMain 공유 위험**:
- 테스트 간 상태 누수 (미들웨어, in-memory store)
- 순서 의존 flakiness
- 병렬성/독립성 깨짐

### 6.3 mock 허용 구역 (이것만)

| 허용 | 이유 |
|------|------|
| `time.Now()` | 비결정성 |
| `uuid.New()` | 비결정성 |
| `rand` / `crypto/rand` | 비결정성 (seed 고정 필요) |
| 외부 SaaS API | 네트워크 의존 |
| 외부 프로세스/시스템 자원 (exec, 포트, 권한) | 환경 의존/비결정성 |

> **랜덤이 필요하면** `rand.New(rand.NewSource(1))`로 seed 고정

**그 외는 real.**

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

### 7.4 map 순서 의존 금지

> **flaky 1순위**: Go map은 iteration 순서가 비결정적.

```go
// ❌ 나쁨 - map 순서에 의존
keys := []string{}
for k := range m {
    keys = append(keys, k)
}
assert.Equal(t, []string{"a", "b"}, keys)  // 순서 보장 안 됨

// ✅ 좋음 - 정렬 후 비교
sort.Strings(keys)
assert.Equal(t, []string{"a", "b"}, keys)

// ✅ 또는 - 길이/존재만 검증
assert.Len(t, m, 2)
assert.Contains(t, m, "a")
```

### 7.5 전역 상태/환경 변수

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

// ✅ gin mode는 init()에서 고정 (테스트 본문에서 하지 말 것)
func init() {
    gin.SetMode(gin.TestMode)
}

// ❌ 나쁨 - 테스트 본문에서 전역 상태 변경
func Test_Something(t *testing.T) {
    t.Parallel()  // ← 충돌 위험!
    gin.SetMode(gin.TestMode)  // 여기서 하면 안 됨
}
```

### 7.6 t.Parallel(): 권장 기본값

> **Go 철학**: `t.Parallel()`은 **권장 기본값**. 단, 전역 상태를 만지면 **금지하고 이유를 남긴다**.

```go
// ✅ 기본: 전역 상태 없으면 t.Parallel()
func Test_ResolveReference_ValidSHA_ReturnsHash(t *testing.T) {
    t.Parallel()
    // ...
}

// ⚠️ 금지 시 이유를 주석으로 남긴다
func Test_ConfigFromEnv_ReadsEnvVar(t *testing.T) {
    // no t.Parallel: mutates process env
    original := os.Getenv("MY_VAR")
    t.Cleanup(func() { os.Setenv("MY_VAR", original) })
    os.Setenv("MY_VAR", "test")
    // ...
}
```

**금지해야 하는 경우** (주석 필수):
- 환경 변수 수정
- package-level 전역/싱글톤
- timezone/locale 변경
- 공유 tmp 파일 (파일명 충돌)
- 라이브러리 내부 전역 캐시 (go-git 등)

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
    router := newRouter()

    // When: ...
    req := httptest.NewRequest(http.MethodGet, "/path", nil)
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)
    resp := asResp(w)  // raw → TestResponse

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
    assertNotStatus(t, resp, http.StatusConflict) // optional: guards old bug
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
    // gin.SetMode는 init()에서 설정됨
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
    resp := asResp(w)  // raw → TestResponse

    // Then: 404 반환 (ref not found)
    requireStatus(t, resp, http.StatusNotFound)          // 핵심: 정답 강제
    assertNotStatus(t, resp, http.StatusConflict)        // 보조: 옛날 버그 가드
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

// --- TestResponse 타입 및 변환 ---

type TestResponse struct {
    Status int
    Body   string
    Header http.Header
}

func asResp(w *httptest.ResponseRecorder) TestResponse {
    return TestResponse{
        Status: w.Code,
        Body:   w.Body.String(),
        Header: w.Header(),
    }
}

// --- Assertion helpers ---
// 핵심: "정답을 강제"하고, 실패 메시지에 body 포함

func requireStatus(t *testing.T, resp TestResponse, want int) {
    t.Helper()
    if resp.Status != want {
        t.Fatalf("status=%d want=%d body=%s", resp.Status, want, resp.Body)
    }
}

func assertNotStatus(t *testing.T, resp TestResponse, notWant int) {
    t.Helper()
    if resp.Status == notWant {
        t.Errorf("status=%d (should not be %d) body=%s", resp.Status, notWant, resp.Body)
    }
}

func requireBodyContains(t *testing.T, resp TestResponse, substr string) {
    t.Helper()
    if !strings.Contains(resp.Body, substr) {
        t.Errorf("body does not contain %q: %s", substr, resp.Body)
    }
}
```

---

## 12. PR 리뷰 체크리스트

PR 리뷰에서 **아래 13개** 확인:

| # | 체크 항목 | Go 원칙 |
|---|-----------|---------|
| 1 | **real test**인가? (mock 대신 실제 repo/server) | mock은 외부 의존만 |
| 2 | **`t.Parallel()`** 있거나, 없으면 **이유 주석**이 있는가? | 권장 기본값 |
| 3 | 테스트 이름이 `Unit + Scenario + Expect` 인가? | 이름이 스펙 |
| 4 | **코드만 봐도 읽히는가**? (주석 없이도) | 코드가 문서 |
| 5 | **핵심 로직이 inline**인가? (helper에 숨지 않음) | 추상화 최소 |
| 6 | 에러 무시(`_, _ :=`)하지 않았는가? | 명시적 에러 처리 |
| 7 | **정답 강제**인가? (`requireStatus`, `if got==409` 금지) | 정확한 검증 |
| 8 | 실패 메시지에 body 포함되는가? | 디버깅 용이 |
| 9 | `t.TempDir()` 사용하는가? | 격리된 환경 |
| 10 | 시간 고정되어 있는가? | 비결정성 제거 |
| 11 | helper에 `t.Helper()` 있는가? | 스택트레이스 |
| 12 | `setup*` helper가 없는가? | 의미 있는 이름 |
| 13 | **이 테스트가 막는 회귀를 1문장**으로 말할 수 있는가? | 의미 있는 테스트 |

> **#13 예시**: "삭제된 브랜치로 merge preview를 호출하면 404를 반환해야 하며, 409로 새면 안 된다."

### 특히 주의: Assertion 금지 패턴

```go
// ❌ PR에서 발견하면 반드시 수정 요청
if got == 409 { t.Error(...) }                    // "특정 값만 나쁘다"
if status >= 500 { t.Error(...) }                 // "범위로 뭉뚱그림"
assertNotStatus(t, resp, http.StatusConflict)    // 단독 사용 (정답 없음)

// ✅ 이렇게 수정
requireStatus(t, resp, http.StatusNotFound)       // 정답 강제 (핵심)
assertNotStatus(t, resp, http.StatusConflict)    // 보조 가드 (optional)
```

---

## 13. 요약: Go-스러운 테스트의 4가지 핵심

1. **real test 우선**: mock 대신 **실제 서버/repo로** 테스트
2. **t.Parallel() 기본**: 순수 로직은 **병렬**로 돌려라
3. **코드가 문서**: 주석 없이도 **테스트 이름과 구조로** 의도 전달
4. **추상화 최소**: helper는 **boilerplate만**, 핵심 로직은 **inline**

> **Go 철학**: 불필요한 추상을 제거하고, **필요한 것만 최소 크기로** 유지한다.

> **"테스트도 코드다. 코드 자체가 읽혀야 한다."**

---

## 14. 절대 금지 목록 (Quick Reference)

| 금지 | 대신 | Go 원칙 |
|------|------|---------|
| **mock 먼저** | 실제 repo/server로 테스트 | real test 우선 |
| `t.Parallel()` 없는데 이유 주석도 없음 | 주석으로 이유 명시 | 권장 기본값 |
| **의도를 숨기는 helper** (setupTestRepo) | inline 또는 단순 helper | 추상화 최소 |
| `if got == 409` | `requireStatus(t, resp, 404)` | 정답 강제 |
| `setup*` helper 이름 | `newRepo(t)`, `commitFile(t, ...)` | 명확한 이름 |
| `_, _ :=` 에러 무시 | `if err != nil { t.Fatal(...) }` | 명시적 에러 |
| assert 메시지에 body 없음 | `t.Fatalf("...body=%s", ...)` | 디버깅 정보 |
| `time.Now()` 직접 사용 | `now := time.Date(...)` 고정 | 비결정성 제거 |
| 전역/상대 경로 | `t.TempDir()` | 격리 |
| `t.Helper()` 누락 | 첫 줄에 `t.Helper()` | 스택트레이스 |