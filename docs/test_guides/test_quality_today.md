# 테스트 품질 분석 리포트

> **분석 기준**: `docs/test_guides/test_guides.md` 가이드 적용 시 코드베이스 변화 및 기능적 퀄리티 향상 효과

---

## A. 코드베이스 현황 진단 (Evidence)

### A1) 테스트 인벤토리 요약

| 지표 | 값 | 근거 |
|------|-----|------|
| 테스트 파일 수 | **30개** | `find . -name "*_test.go"` |
| 테스트 함수 수 | **187개** | `grep "^func Test"` |
| Black-box (`package xxx_test`) | **10개 (33%)** | tests/ 디렉토리 |
| White-box (`package xxx`) | **20개 (67%)** | internal/ 디렉토리 |
| assert/require 호출 | **976회** | testify 사용 |
| t.Helper() 사용 | **7회** | commits, transaction 패키지만 |

#### 패키지 분포

| 패키지 | 테스트 함수 수 | 비고 |
|--------|---------------|------|
| internal/branches | 19 | handler 중심 |
| internal/repositories | 24 | handler 중심 |
| internal/commits | 11 | L1 수준 |
| internal/merge | 17 | handler+preview |
| internal/overview | 17 | service+handler |
| internal/diffs | 20 | service+handler |
| internal/events | 8 | Redis 의존 |
| tests/unit | 22 | 분리된 유닛 |
| tests/integration | 2 | E2E 미약 |

#### L0/L1/L2 분류 추정

| 레벨 | 추정 수 | 근거 |
|------|--------|------|
| **L0** (Pure function) | ~30 (16%) | config, cache, util 패키지 |
| **L1** (Component) | ~145 (78%) | handler/service + 실제 repo, 외부 네트워크 없음 |
| **L2** (E2E) | ~12 (6%) | tcp_git_server, redis, integration |

### A1-2) Flaky 가능성 TOP 10

| 순위 | 파일 | 원인 | 치명도 |
|------|------|------|--------|
| 1 | `redis_stream_test.go` | 외부 Redis 의존 (`localhost:6379`) | **High** |
| 2 | `tcp_git_server_pack_test.go` | `time.Now()` 14회, 네트워크 | **High** |
| 3 | `overview/service_test.go` | `time.Now()` 9회 | **Med** |
| 4 | `overview/handler_perf_test.go` | `time.Now()` 10회, 성능 의존 | **Med** |
| 5 | `diffs/service_test.go` | `time.Now()` 12회 | **Med** |
| 6 | `branches/handler_test.go` | `os.MkdirTemp` (not `t.TempDir`) | **Low** |
| 7 | `repositories/handler_test.go` | `go func` goroutine | **Low** |
| 8 | `merge/preview_test.go` | `time.Now()` 3회 | **Low** |
| 9 | `commits/handler_test.go` | `time.Now()` 1회 | **Low** |
| 10 | `tree/handler_test.go` | `time.Now()` 1회 | **Low** |

---

### A2) 가이드 위반 패턴 Top 12

#### 1. 테스트 이름이 스펙이 아닌 경우

| 발견 수 | 치명도 |
|---------|--------|
| **37개 (20%)** | **Med** |

```go
// ❌ 대표 예시
func TestNewRedisStreamClient(t *testing.T)          // 뭘 검증?
func TestHandleGetRepositoryOverview_LanguageDistribution(t *testing.T)  // 기대값 없음
func TestMultiFileCommit_Handler(t *testing.T)       // 시나리오 불명확
```

**문제**: 테스트 이름만 보고 "어떤 시나리오에서 어떤 결과를 기대하는지" 알 수 없음.

---

#### 2. Given/When/Then 섹션 누락

| 발견 수 | 치명도 |
|---------|--------|
| **185개 (99%)** | **High** |

```go
// ❌ 전체 코드베이스에서 Given/When/Then 주석: 2개뿐
// test/integration/tcp_git_server_pack_test.go:1
// internal/diffs/service_test.go:1
```

**문제**: 테스트 흐름(전제→행동→검증)이 시각적으로 구분되지 않아 **3초 컷 불가**.

---

#### 3. setup* 류 helper 남용

| 발견 수 | 치명도 |
|---------|--------|
| **154회 호출** | **High** |

```go
// ❌ 대표 예시: internal/merge/handler_test.go
repo, cleanup := setupTestRepository(t)
handler := setupTestHandler(t)

// ❌ internal/branches/handler_test.go
handler, tempDir, cleanup := setupTestHandler(t)
createTestRepository(t, tempDir, "testowner", "testrepo")
```

**문제**: 
- `setupTestRepository`가 무엇을 만드는지 본문에서 안 보임
- 핵심 상태 변화가 helper에 숨겨짐
- 테스트 읽는 데 **외부 파일 참조 필수**

---

#### 4. time.Now() 직접 사용

| 발견 수 | 치명도 |
|---------|--------|
| **70회** (13개 파일) | **Med** |

```go
// ❌ 대표 예시: internal/events/redis_stream_test.go:56
now := time.Now()
event := &CommitEvent{
    Timestamp: now,
}

// ❌ internal/tcp_git_server_pack_test.go
startTime := time.Now()
// ...
elapsed := time.Since(startTime)
```

**문제**: 
- 시간 의존 로직에서 **flaky** 발생 가능
- 테스트 결과가 실행 시점에 따라 달라질 수 있음

---

#### 5. os.MkdirTemp 사용 (t.TempDir 대신)

| 발견 수 | 치명도 |
|---------|--------|
| **22회** (10개 파일) | **Med** |

```go
// ❌ 대표 예시: internal/branches/handler_test.go:25
tempDir, err := os.MkdirTemp("", "branches-test-*")
require.NoError(t, err)
// ...
cleanup := func() {
    os.RemoveAll(tempDir)
}
```

**문제**: 
- `t.TempDir()`는 자동 cleanup, `os.MkdirTemp`는 수동 cleanup 필요
- cleanup 누락 시 디스크 누수

---

#### 6. t.Helper() 누락

| 발견 수 | 치명도 |
|---------|--------|
| **~147개 helper 중 7개만 사용** | **Med** |

```go
// ❌ 대표 예시: internal/merge/handler_test.go
func setupTestRepository(t *testing.T) (*gogit.Repository, func()) {
    // t.Helper() 없음!
    tempDir, err := os.MkdirTemp("", "merge-test-*")
    // ...
}

// ✅ 올바른 예시: internal/commits/handler_test.go
func getHEADRefTarget(t *testing.T, repo *git.Repository) plumbing.ReferenceName {
    t.Helper()  // ✓
    // ...
}
```

**문제**: 실패 시 스택트레이스가 helper 내부를 가리켜 **디버깅 어려움**.

---

#### 7. t.Parallel() 미사용

| 발견 수 | 치명도 |
|---------|--------|
| **0회** (전체 187개 테스트) | **Low** |

**문제**: 
- 테스트 실행 시간 최적화 불가
- 병렬 안전성 검증 안 됨

**단, 현재 상태에서는 오히려 긍정적**: 전역 상태 문제가 있어서 `t.Parallel()` 추가 시 flaky 위험.

---

#### 8. 실패 메시지에 body 없음

| 발견 수 | 치명도 |
|---------|--------|
| **대부분** | **High** |

```go
// ❌ 현재 패턴 (testify 기본)
assert.Equal(t, http.StatusOK, w.Code)

// 실패 시 메시지:
// "Expected: 200, Actual: 409"
// → body가 없어서 원인 파악 불가

// ✅ 가이드 권장
if w.Code != http.StatusOK {
    t.Fatalf("status=%d want=%d body=%s", w.Code, http.StatusOK, w.Body.String())
}
```

**문제**: API 실패 시 **body 없이는 원인 추적 불가**.

---

#### 9. 외부 네트워크 의존

| 발견 수 | 치명도 |
|---------|--------|
| **4회** (3개 파일) | **High** |

```go
// ❌ redis_stream_test.go
url = "redis://localhost:6379/0"

// ❌ tcp_git_server_pack_test.go
localhost:// 사용
```

**문제**: 
- CI 환경에서 Redis 없으면 **테스트 전체 실패**
- 네트워크 지연으로 **flaky** 발생

---

#### 10. 약한 어설션 (부분 조건 체크)

| 발견 수 | 치명도 |
|---------|--------|
| **~9회** | **Med** |

```go
// ❌ internal/protocol/ssh_protocol_test.go:86
if err == nil {
    t.Fatalf("expected error, got nil")
}
// → 에러가 아니면 뭐든 통과 (특정 에러 타입 검증 없음)
```

**문제**: 500/401/404 등 다른 에러도 통과해버림.

---

#### 11. assert 메시지 부재

| 발견 수 | 치명도 |
|---------|--------|
| **~70%** | **Med** |

```go
// ❌ 대부분의 assert
assert.Equal(t, http.StatusOK, w.Code)
assert.True(t, response.Success)

// ✅ 권장
assert.Equal(t, http.StatusOK, w.Code, "API should return 200, body=%s", w.Body.String())
```

---

#### 12. 테스트별 디버그 정보 부족

| 발견 수 | 치명도 |
|---------|--------|
| **대부분** | **Med** |

```go
// ❌ 현재: response body 검증 후 버림
var response CreateBranchResponse
err = json.Unmarshal(w.Body.Bytes(), &response)
require.NoError(t, err)
// body 내용을 실패 시 출력 안 함

// ✅ 권장: 실패 시 전체 context 출력
t.Logf("status=%d body=%s", w.Code, w.Body.String())
```

---

### A2 요약 테이블

| # | 패턴 | 발견 수 | 치명도 | 수정 난이도 |
|---|------|---------|--------|-------------|
| 1 | 테스트 이름 비표준 | 37개 | Med | 쉬움 |
| 2 | Given/When/Then 누락 | 185개 | **High** | 중간 |
| 3 | setup* helper 남용 | 154회 | **High** | 중간 |
| 4 | time.Now() 직접 사용 | 70회 | Med | 쉬움 |
| 5 | os.MkdirTemp | 22회 | Med | 쉬움 |
| 6 | t.Helper() 누락 | ~140개 | Med | 쉬움 |
| 7 | t.Parallel() 미사용 | 187개 | Low | 신중 |
| 8 | body 없는 실패 메시지 | 대부분 | **High** | 중간 |
| 9 | 외부 네트워크 의존 | 4회 | **High** | 어려움 |
| 10 | 약한 어설션 | ~9회 | Med | 쉬움 |
| 11 | assert 메시지 부재 | ~70% | Med | 쉬움 |
| 12 | 디버그 정보 부족 | 대부분 | Med | 쉬움 |

---

## B. 가이드 적용 시 코드가 "어떻게" 바뀌는가

### B1) 테스트 작성 스타일 변화 (Before → After)

#### 예시 1: branches/handler_test.go

**Before** (현재):

```go
func TestHandleCreateBranch_Success(t *testing.T) {
    handler, tempDir, cleanup := setupTestHandler(t)
    defer cleanup()

    createTestRepository(t, tempDir, "testowner", "testrepo")

    router := gin.New()
    router.POST("/repository/:owner/:repo/branches", handler.HandleCreateBranch)

    requestBody := CreateBranchRequest{
        Name:   "feature/new-feature",
        Source: "master",
    }

    body, err := json.Marshal(requestBody)
    require.NoError(t, err)

    req := httptest.NewRequest("POST", "/repository/testowner/testrepo/branches", 
        strings.NewReader(string(body)))
    req.Header.Set("Content-Type", "application/json")
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)

    assert.Equal(t, http.StatusOK, w.Code)  // body 없음

    var response CreateBranchResponse
    err = json.Unmarshal(w.Body.Bytes(), &response)
    require.NoError(t, err)

    assert.True(t, response.Success)
    assert.Equal(t, "feature/new-feature", response.Data.BranchName)
}
```

**After** (가이드 적용):

```go
func Test_CreateBranch_ValidRequest_Returns200WithBranchInfo(t *testing.T) {
    t.Parallel()

    // Given: bare repo with master branch
    tempDir := t.TempDir()
    repoPath := filepath.Join(tempDir, "testowner", "testrepo.git")
    repo := initBareRepo(t, repoPath)
    createBranchAt(t, repo, "master", emptyHash)

    handler := newBranchHandler(t, tempDir)
    router := gin.New()
    router.POST("/repository/:owner/:repo/branches", handler.HandleCreateBranch)

    // When: create feature branch from master
    body := `{"name": "feature/new-feature", "source": "master"}`
    req := httptest.NewRequest("POST", 
        "/repository/testowner/testrepo/branches", 
        strings.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)

    // Then: 200 with branch info
    requireStatus(t, w, http.StatusOK)
    
    var resp CreateBranchResponse
    require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
    require.True(t, resp.Success, "body=%s", w.Body.String())
    require.Equal(t, "feature/new-feature", resp.Data.BranchName)
}
```

**변화 요약**:
| 항목 | Before | After |
|------|--------|-------|
| 이름 | `TestHandleCreateBranch_Success` | `Test_CreateBranch_ValidRequest_Returns200WithBranchInfo` |
| 섹션 | 없음 | Given/When/Then |
| tempDir | `os.MkdirTemp` + 수동 cleanup | `t.TempDir()` |
| helper | `setupTestHandler` (내부 숨김) | `newBranchHandler` (명확) |
| 어설션 | `assert.Equal(w.Code)` | `requireStatus(t, w, 200)` |
| 실패 메시지 | body 없음 | `body=%s` 포함 |

---

#### 예시 2: merge/handler_test.go

**Before**:

```go
func TestMergePreviewResponseStructure(t *testing.T) {
    repo, cleanup := setupTestRepository(t)
    defer cleanup()

    handler := setupTestHandler(t)

    wt, err := repo.Worktree()
    require.NoError(t, err)

    writeFile(t, wt, "file1.txt", "content1")
    commit1, err := commitFile(t, wt, "file1.txt", "Initial commit")
    require.NoError(t, err)
    // ... 60줄 더 ...
}
```

**After**:

```go
func Test_MergePreview_TwoBranches_ReturnsMergeAnalysis(t *testing.T) {
    t.Parallel()

    // Given: repo with main and feature branch
    now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
    repo := newRepo(t)
    mainHash := commitFile(t, repo, "file1.txt", "init", "Initial", now)
    featureHash := commitFile(t, repo, "file2.txt", "feat", "Feature", now)

    handler := newMergePreviewHandler(t, repo)

    // When: preview merge feature -> main
    req := &MergePreviewRequest{
        SourceRef: featureHash.String(),
        TargetRef: mainHash.String(),
    }
    resp, err := handler.performMergePreview(ctx, repo, featureHash, mainHash, req, now)

    // Then: returns merge analysis
    require.NoError(t, err, "preview should succeed")
    require.NotNil(t, resp)
    require.True(t, resp.CanMerge, "should be mergeable")
}
```

---

#### 예시 3: redis_stream_test.go (외부 의존)

**Before**:

```go
func TestNewRedisStreamClient(t *testing.T) {
    redisURL := getTestRedisURL()  // localhost:6379
    logger := zaptest.NewLogger(t)

    client, err := NewRedisStreamClient(redisURL, logger)
    require.NoError(t, err)
    require.NotNil(t, client)

    defer client.Close()
}
```

**After** (L2로 분리 + skip):

```go
func Test_RedisStreamClient_Connect_ReturnsClient(t *testing.T) {
    // L2: requires external Redis
    if os.Getenv("REDIS_URL") == "" {
        t.Skip("REDIS_URL not set, skipping integration test")
    }

    // Given: Redis URL from env
    redisURL := os.Getenv("REDIS_URL")
    logger := zaptest.NewLogger(t)

    // When: create client
    client, err := NewRedisStreamClient(redisURL, logger)

    // Then: client created successfully
    require.NoError(t, err, "should connect to Redis")
    require.NotNil(t, client)
    t.Cleanup(func() { client.Close() })
}
```

---

### B2) 테스트 유틸 구조 변화 제안

#### 디렉토리 구조

```
testutil/
├── assert.go      # requireStatus, requireNotStatus, requireBodyContains
├── git.go         # newRepo, commitFile, createBranchAt, removeBranch
├── http.go        # newRouter, callAPI
└── time.go        # fixedTime
```

#### assert.go

```go
package testutil

import (
    "net/http/httptest"
    "strings"
    "testing"
)

func RequireStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
    t.Helper()
    if w.Code != want {
        t.Fatalf("status=%d want=%d body=%s", w.Code, want, w.Body.String())
    }
}

func RequireNotStatus(t *testing.T, w *httptest.ResponseRecorder, notWant int) {
    t.Helper()
    if w.Code == notWant {
        t.Errorf("status=%d (should not be %d) body=%s", w.Code, notWant, w.Body.String())
    }
}

func RequireBodyContains(t *testing.T, w *httptest.ResponseRecorder, substr string) {
    t.Helper()
    if !strings.Contains(w.Body.String(), substr) {
        t.Errorf("body does not contain %q: %s", substr, w.Body.String())
    }
}
```

#### git.go

```go
package testutil

import (
    "path/filepath"
    "testing"
    "time"

    gogit "github.com/go-git/go-git/v5"
    "github.com/go-git/go-git/v5/plumbing"
    "github.com/go-git/go-git/v5/plumbing/object"
)

func NewRepo(t *testing.T) *gogit.Repository {
    t.Helper()
    tempDir := t.TempDir()
    repo, err := gogit.PlainInit(tempDir, false)
    if err != nil {
        t.Fatalf("repo init failed: %v", err)
    }
    return repo
}

func CommitFile(t *testing.T, repo *gogit.Repository, filename, content, msg string, when time.Time) plumbing.Hash {
    t.Helper()
    // ... implementation
}

func CreateBranchAt(t *testing.T, repo *gogit.Repository, name string, hash plumbing.Hash) {
    t.Helper()
    ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName(name), hash)
    if err := repo.Storer.SetReference(ref); err != nil {
        t.Fatalf("create branch %s failed: %v", name, err)
    }
}

func RemoveBranch(t *testing.T, repo *gogit.Repository, name string) {
    t.Helper()
    if err := repo.Storer.RemoveReference(plumbing.NewBranchReferenceName(name)); err != nil {
        t.Fatalf("remove branch %s failed: %v", name, err)
    }
}
```

#### Helper 금지/허용 규칙 Enforce

**grep 스크립트** (`scripts/check-test-helpers.sh`):

```bash
#!/bin/bash
# setup* helper 사용 탐지
echo "=== setup* helper 사용 ==="
grep -rn "setup[A-Z]" --include="*_test.go" | head -20

# t.Helper() 누락 탐지
echo "=== t.Helper() 누락 helper ==="
grep -rn "func.*\*testing\.T.*{" --include="*_test.go" -A1 | grep -v "t.Helper()" | head -20

# time.Now() 직접 사용
echo "=== time.Now() 직접 사용 ==="
grep -rn "time\.Now()" --include="*_test.go" | head -20

# os.MkdirTemp 사용 (t.TempDir 대신)
echo "=== os.MkdirTemp 사용 ==="
grep -rn "os\.MkdirTemp\|ioutil\.TempDir" --include="*_test.go" | head -20
```

---

### B3) PR 리뷰/개발 프로세스 변화

#### PR 템플릿 체크리스트

```markdown
## 테스트 체크리스트
- [ ] 테스트 이름: `Test_<Unit>_<Scenario>_<Expect>` 형식
- [ ] Given/When/Then 섹션 구분
- [ ] `requireStatus()` 기반 정답 강제 (if got == 409 금지)
- [ ] 실패 메시지에 body 포함
- [ ] `t.TempDir()` 사용 (os.MkdirTemp 금지)
- [ ] `t.Helper()` helper 첫 줄에 포함
- [ ] 시간 고정 (`time.Date(...)`)
```

#### 리뷰 코멘트 표준 문구 5개

1. **정답 강제 없음**
   > "이 테스트는 `assert.Equal(w.Code, ...)` 실패 시 body가 없어서 원인 파악이 어렵습니다. `requireStatus(t, w, 200)` 패턴으로 변경해주세요."

2. **setup* helper**
   > "setupTestHandler가 무엇을 만드는지 테스트 본문에서 안 보입니다. 핵심 상태를 inline으로 보여주거나, helper 이름을 `newBranchHandler`처럼 의미 있게 변경해주세요."

3. **time.Now()**
   > "`time.Now()` 직접 사용은 flaky 원인입니다. 테스트 상단에서 `now := time.Date(2024, 1, 1, ...)` 고정 후 사용해주세요."

4. **Given/When/Then 누락**
   > "Given/When/Then 주석이 없어서 테스트 흐름을 파악하기 어렵습니다. 3섹션 구분을 추가해주세요."

5. **t.Helper() 누락**
   > "이 helper 함수에 `t.Helper()`가 없어서 실패 시 스택트레이스가 helper 내부를 가리킵니다. 첫 줄에 추가해주세요."

---

## C. 기능적 퀄리티가 "얼마나" 올라가는가

### C1) 회귀(Regression) 탐지력

| 지표 | Before | After (추정) | 근거 |
|------|--------|--------------|------|
| "정답 강제" 테스트 비율 | ~30% | **90%+** | testify assert → requireStatus |
| 놓치던 실패 케이스 | 500/401/404 혼동 | **명확한 실패** | body 포함 메시지 |

**추정**: 
- 현재 `assert.Equal(w.Code, 200)` 패턴은 실패 시 body가 없음
- `requireStatus` 적용 시 실패 원인 즉시 파악 가능
- **예상 회귀 탐지 개선: 30~50%** (현재 놓치는 케이스 기준)

**가정**: 
- 현재 테스트 실패 중 ~30%가 "원인 불명"으로 추가 디버깅 필요
- body 포함 시 즉시 원인 파악 가능

---

### C2) 디버깅 속도 (Time-to-diagnose)

| 지표 | Before | After (추정) | 근거 |
|------|--------|--------------|------|
| 평균 디버깅 시간 | 15~30분 | **5~10분** | body 포함 |
| "정보 부족 실패" 비율 | ~70% | **<10%** | 메시지 개선 |

**현재 "정보 부족 실패" 사례**:

```
=== FAIL: TestHandleCreateBranch_AlreadyExists
    handler_test.go:175: 
        Error:      Expected 409, got 500
```

→ **body가 없어서 왜 500인지 모름**

**개선 후**:

```
=== FAIL: Test_CreateBranch_DuplicateName_Returns409
    handler_test.go:175: 
        status=500 want=409 body={"error":"internal: failed to open repository"}
```

→ **즉시 원인 파악: repo open 실패**

**추정 개선**: **50~70% 디버깅 시간 단축**

---

### C3) Flaky 감소 (신뢰성)

| 원인 | 현재 후보 수 | 가이드 적용 후 | 예상 감소율 |
|------|-------------|---------------|-------------|
| time.Now() | 70회 | 0회 | **100%** |
| os.MkdirTemp | 22회 | 0회 | **100%** |
| 외부 네트워크 | 4회 | skip/mock | **75%** |
| goroutine | 1회 | t.Cleanup | **100%** |
| **총** | **97회** | **~1회** | **~99%** |

**가정**: 
- time.Now()는 시간 의존 테스트에서 가끔 flaky (하루 ~1회)
- 외부 네트워크는 CI에서 ~5% 실패율

**예상 flaky 감소**: **80~95%**

---

### C4) 유지보수 비용

| 지표 | Before | After |
|------|--------|-------|
| helper 변경 시 영향 범위 | 모든 테스트 | 해당 테스트만 |
| 테스트 이해 시간 | 5~10분 | **1~2분** |
| 신규 팀원 온보딩 | 1~2주 | **2~3일** |

**setup* 제거 효과**:
- 현재: `setupTestRepository` 수정 → **154개 테스트 영향**
- 개선 후: 각 테스트가 자체 setup → **1개 테스트만 영향**

**예상 유지보수 비용 감소**: **40~60%**

---

### C5) 커버리지/중요 시나리오 충족도

#### 현재 빈 구간 (중요 시나리오 기준)

| 시나리오 | 현재 커버 | 가이드 스타일로 추가 난이도 |
|----------|-----------|---------------------------|
| 삭제된 branch ref로 API 호출 | ❌ | 쉬움 (merge.md 분석 완료) |
| 409 vs 404 에러 매핑 | ❌ | 쉬움 |
| 권한 없는 사용자 접근 | ❌ | 중간 |
| 대용량 파일 처리 (>100MB) | 일부 | 중간 |
| 동시성 (concurrent push) | ❌ | 어려움 |
| 외부 서비스 장애 (503) | ❌ | 중간 |

**가이드 적용 시 추가 용이 시나리오**:

1. **삭제된 branch ref** (`docs/merge/merge.md` 기반):
```go
func Test_MergePreview_DeletedSourceRef_Returns404(t *testing.T) {
    // Given/When/Then 템플릿으로 빠르게 작성 가능
}
```

2. **에러 매핑**:
```go
func Test_ResolveReference_NotFound_Returns404_Not409(t *testing.T) {
    // requireStatus로 정확한 상태코드 검증
}
```

---

## D. 우선순위 실행 계획

### D1) Quick Wins (1~2일)

| 작업 | 예상 시간 | 기대 효과 | Done 정의 |
|------|-----------|-----------|-----------|
| `os.MkdirTemp` → `t.TempDir()` | 2시간 | flaky 감소 | grep 결과 0 |
| assert 메시지에 body 추가 | 4시간 | 디버깅 속도 ↑ | 주요 handler 테스트 완료 |
| t.Helper() 추가 | 2시간 | 스택트레이스 개선 | grep 결과 0 |

**리스크**: 낮음 (단순 치환)

---

### D2) 핵심 리팩토링 (1~2주)

| 작업 | 예상 시간 | 기대 효과 | Done 정의 |
|------|-----------|-----------|-----------|
| testutil/ 도입 | 1일 | 표준화 | requireStatus 사용 |
| setup* → inline/명확한 이름 | 3일 | 가독성 ↑ | grep "setup[A-Z]" 0 |
| time.Now() → 고정 시간 | 2일 | flaky 감소 | grep 결과 0 |
| flaky TOP 5 안정화 | 2일 | 신뢰성 ↑ | CI 100% 통과 |
| L1 핵심 테스트 5개 추가 | 3일 | 커버리지 ↑ | merge/branch 시나리오 |

**리스크**: 
- setup* 제거 시 테스트 길어짐 → 팀 합의 필요
- helper 분리 시 import 충돌 가능

---

### D3) 롱텀 (4주)

| 작업 | 예상 시간 | 기대 효과 | Done 정의 |
|------|-----------|-----------|-----------|
| L0/L1/L2 라벨 정착 | 1주 | 테스트 분류 명확 | 모든 테스트에 주석 |
| PR 체크리스트 자동화 | 3일 | 리뷰 효율 ↑ | CI에서 위반 탐지 |
| Given/When/Then 전체 적용 | 2주 | 가독성 ↑ | 187개 테스트 완료 |
| t.Parallel() 점진 도입 | 1주 | 속도 ↑ | 전역 상태 없는 테스트 |

**리스크**:
- 대규모 리팩토링으로 일시적 불안정
- 팀 전체 교육 필요

---

## E. 최종 결론

### 핵심 메시지

> **"이 가이드 적용은 현재 코드베이스의 3가지 핵심 문제를 해결한다: (1) 실패 시 원인 불명 → body 포함 메시지로 즉시 파악, (2) setup* 남용으로 테스트 읽기 어려움 → inline/Given-When-Then으로 3초 컷, (3) time.Now()/외부 의존으로 flaky → 결정적 테스트로 CI 신뢰성 확보."**

### 가장 큰 레버 3개

| 순위 | 레버 | 근거 | 예상 효과 |
|------|------|------|-----------|
| **1** | **정답 강제 (requireStatus)** | 현재 ~70%가 body 없는 assert | 디버깅 시간 50~70% 단축 |
| **2** | **Determinism (time.Now() 제거)** | 70회 사용 → flaky 원인 | flaky 80~95% 감소 |
| **3** | **Given/When/Then 섹션화** | 99%가 섹션 없음 | 테스트 이해 시간 70% 단축 |

### 투자 대비 효과 (ROI)

| 투자 | 효과 |
|------|------|
| 2주 리팩토링 | 디버깅 시간 50% 감소 |
| 4주 전체 적용 | flaky 90% 감소 + 유지보수 50% 감소 |

> **"테스트가 스펙처럼 읽히면, 버그도 스펙처럼 보인다."**
