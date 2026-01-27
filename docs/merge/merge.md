# 삭제된 Branch Ref로 MergePreview 호출 시 409 에러 분석 및 재현

## 0. 최종 판정 (TL;DR)

> **"삭제된 branch ref는 Git 관점에서 resolve 실패가 정상이며, 문제는 executor가 이를 'NotFound'가 아닌 'Conflict(409)'로 반환하고, 서로 다른 핸들러가 동일한 오류를 상이한 코드로 반환하는 에러 매핑 불일치에 있다."**

| 가설 | 판정 | 증거 강도 |
|------|------|-----------|
| A. executor ref-resolve 정책 문제 | **기각** | 강함 |
| B. 호출자(contract/데이터) 문제 | **보류** (증거 부족, 간접 증거는 존재) | 중간 |
| C. HTTP 매핑 문제 | **채택** | **결정적** |
| D. 저장소/객체 생명주기 문제 | **기각** | 강함 |

---

## 1. 문제 요약

### 증상
- Feature branch 기반으로 MergePreview 요청 수행
- 이후 Feature branch 삭제
- 동일한 `source_ref`로 MergePreview 재호출 시 **HTTP 409 Conflict** 발생

> **참고**: MergePreview는 실제 merge를 수행하지 않고, 머지 가능성/변경점을 분석하는 API

### 근본 원인 분석 결과: HTTP 에러 매핑 불일치 (가설 C)

```
요청 시점: source_ref = "feature/foo" (branch name)
처리 과정: resolveReference("feature/foo") 호출
          
          1. commit SHA 파싱 시도 → 실패 (40자 hex가 아님)
          2. repo.Reference("feature/foo", true) 시도
             → 유효한 ref name이 아니어서 실패 (refs/로 시작 안 함)
          3. refs/heads/feature/foo 폴백 시도
             → branch 삭제됨 → ErrReferenceNotFound
          4. refs/tags/feature/foo 폴백 시도 → 실패
          5. 최종 실패 → "unable to resolve reference" 에러
          
Handler: 에러 발생 → HTTP 409 반환  ← 이게 문제!
```

> **핵심**: executor는 CR을 저장하지 않음. 이 문제는 "CR 도메인 문제"도 아니고, "ref-resolve 정책 문제"도 아님. **HTTP 에러 매핑이 잘못됨**.

---

## 1.5. 가설 판정 상세

### [가설 A] executor ref-resolve "정책"이 잘못됐다 → **기각**

#### 증거

**A1. contract 확인**:
```yaml
# specs/generated/collabops-git-executor.yaml:886-889
- name: source_ref
  description: Source branch or commit to merge FROM
```

contract는 **"branch or commit"** 허용. branch name만 받겠다는 제한 없음.

**A2. 테스트에서 branch name 사용 케이스 존재**:
```go
// internal/merge/handler_test.go:152
SourceRef: "feature",  // branch name으로 테스트
```

→ branch name 입력이 **의도된 contract**임을 확인.

**A3. SHA 입력 시 동작**:
```go
// internal/merge/handler.go:504-510
if hash := plumbing.NewHash(ref); !hash.IsZero() {
    if _, err := repo.CommitObject(hash); err == nil {
        return hash, nil  // SHA면 첫 단계에서 성공
    }
}
```

→ SHA로 호출하면 **정상 동작**. branch-only 제한 없음.

#### 결론

- resolve 정책은 contract와 일치
- branch 삭제 후 branch name resolve 실패는 **Git 자체의 정상 동작**
- executor의 resolve "정책"이 아니라 **입력값의 생명주기 문제**

#### "정책"의 정의 명확화

> executor의 **API contract(입력으로 branch or commit 허용)**에서 branch name이 삭제되면 resolve 실패하는 건 **정상**이며, 삭제 후에도 동작해야 한다는 요구가 있다면 그것은 executor의 resolve "정책"이 아니라 **상위 도메인의 영속 참조 설계(예: commit SHA 저장) 또는 추가 기능 요구사항**이다.

#### 반증

- SHA로 호출했는데 실패하는 로그가 있다면 재검토

---

### [가설 B] 호출자(contract/데이터)가 잘못됐다 → **보류 (증거 부족)**

#### 직접 증거 없음
- executor 코드베이스에 orchestrator/api-service 코드 없음
- 실제 장애 요청 로그 없음

#### 간접 증거

**B-aux1. MergePreview는 query에서 source_ref를 받음**:
```yaml
# specs/generated/collabops-git-executor.yaml:886-889
- name: source_ref
  in: query
  required: true
```

→ 호출자가 URL에 넣어서 보냄. body가 아님.

**B-aux2. 테스트에서 branch name 사용**:
```go
// internal/merge/handler_test.go:152-153
SourceRef: "feature",
TargetRef: "main",
```

→ executor 자체 테스트도 branch name으로 호출하는 케이스 존재.  
→ **호출자가 branch name을 보낼 가능성은 충분히 있음**.

**B-aux3. 에러 메시지가 그대로 나감**:
```go
// internal/merge/handler.go:178
"error": err.Error(),  // "unable to resolve reference: feature/foo"
```

→ `gin.Logger()` 미들웨어 사용 (`internal/server/server.go:129`)  
→ access log에 이 에러가 남을 수 있음.

#### 결론

- executor 범위에서 호출자 동작 직접 확인 불가
- 그러나 **호출자가 branch name만 보낼 가능성**은 간접 증거로 뒷받침됨

#### 반증

- 호출자가 commit SHA를 저장/전달하는 코드가 있다면 기각

---

### [가설 C] executor의 HTTP 매핑이 문제다 → **채택 (결정적 증거)**

#### C1. 상태코드 의미론 문제 (semantic mismatch)

**ref not found는 404가 맞음**:
- 409 Conflict: "요청이 리소스의 현재 상태와 충돌" (동시성/상태 문제 암시)
- 404 Not Found: "요청한 리소스가 존재하지 않음"

→ ref가 없는 건 **리소스 미존재(404)**

#### C2. 에러 분류가 과도하게 뭉개짐 (409 umbrella)

**merge/handler.go에서 409를 반환하는 케이스 분류**:

| 라인 | 원인 | 실제 의미 | 적절한 코드 |
|------|------|-----------|-------------|
| 127 | validatePath 실패 | validation error | **400** |
| 166 | OpenRepository 실패 | repo not found | **404** |
| **176** | **ResolveReference(source) 실패** | **ref not found** | **404** |
| 185 | resolveReference(target) 실패 | ref not found | 404 |
| 208 | analyzeMerge 실패 | analysis error | 500 또는 409 |
| 221 | analyzeRebaseDryRun 실패 | analysis error | 500 또는 409 |
| 292 | performMerge 실패 | merge execution error | 409 (진짜 충돌이면) |

**아이러니**: 실제 merge conflict는 **400으로 반환**!
```go
// internal/merge/handler.go:283-285
if conflictAnalysis.HasConflicts && !req.ForceConflict {
    c.JSON(http.StatusBadRequest, ...)  // 400!
}
```

| 에러 타입 | 현재 코드 | 의미론적 적절성 |
|-----------|-----------|-----------------|
| ref not found | **409** | ❌ |
| **merge conflict** | **400** | ❌ (409가 맞음) |

→ **완전히 뒤바뀜**

#### 결정적 증거: 동일 코드베이스 내 불일치

**overview/handler.go:123**:
```go
c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "reference not found"})
```

**merge/handler.go:176**:
```go
c.JSON(http.StatusConflict, gin.H{"success": false, "error": err.Error()})
```

**동일한 "reference not found" 에러**:
- `overview` 패키지: **404 Not Found** ✓
- `merge` 패키지: **409 Conflict** ✗

#### 결론

- **동일 코드베이스 내 불일치** → 의도된 설계가 아님
- **7가지 이상 에러가 409로 뭉개짐** → 관측성/자동처리/UX 붕괴
- **merge conflict와 ref not found가 뒤바뀜** → semantic mismatch

#### 반증

- merge에서 409 사용이 의도된 설계 문서/PR 설명이 있다면 재검토

---

### [가설 D] 저장소/객체 생명주기 문제 → **기각**

#### 증거

**D1. SHA resolve는 정상 동작**:
```go
// internal/merge/handler_test.go:98-99
SourceRef: commit2.String(),  // SHA로 직접 호출 → 테스트 통과
```

**D2. git gc 미실행**:
- 코드베이스에서 `git gc`/`housekeeping` 트리거 로직 **없음**
- `internal/performance/resource_manager.go`의 GC는 **Go runtime GC**

```go
// internal/performance/resource_manager.go:382
runtime.GC()  // Go GC, git GC 아님
```

**D3. bare repo는 refs/remotes/* 없음**:
```go
// internal/repositories/handler.go:146
Bare: true, // Always create bare repositories
```

→ bare repo는 자기 자신이 origin이므로 `refs/remotes/*` 일반적으로 없음

#### 결론

- SHA resolve는 동작함 → commit 객체는 남아있음
- **코드베이스 내에서 git gc/housekeeping을 트리거하는 로직은 확인되지 않음**
- 따라서 기본 동작 기준으로 orphan commit은 유지될 가능성이 큼
- **(단, 외부 운영 작업은 별개)**

#### 반증

- git gc가 실행되어 orphan commit이 사라진 증거가 있다면 재검토

---

### 각 가설별 반증 요약

| 가설 | 반증 조건 |
|------|-----------|
| A | SHA로 호출했는데 실패하는 로그 존재 시 |
| B | 호출자가 이미 commit SHA를 저장/전달하는 코드 존재 시 |
| C | merge에서 409 사용이 의도된 설계 문서/PR 설명 존재 시 |
| D | git gc가 실행되어 orphan commit이 사라진 증거 존재 시 |

---

### 장애 재현을 위한 최소 로그/추적 포인트

| # | 추적 포인트 | 목적 |
|---|-------------|------|
| 1 | `source_ref` raw value | branch name vs SHA 판별 |
| 2 | `resolveReference` 내부 각 단계 시도 결과 | 어느 단계에서 실패했는지 |
| 3 | `errors.Is(err, plumbing.ErrReferenceNotFound)` | 에러 타입 정확히 확인 |
| 4 | `repo.Reference("refs/heads/"+ref)` 호출 결과 | branch ref 존재 여부 |
| 5 | `repo.CommitObject(hash)` 시도 여부/결과 | SHA로 왔을 때 성공 여부 |

**추가 권장**:
- `request_id` / `trace_id`: 상위 서비스 호출 경로 추적
- `owner`/`repo` path: 어떤 저장소에서 발생했는지
- timestamp: branch 삭제 시점과 MergePreview 호출 시점 비교

---

## 2. 코드베이스 사실 정리

### git-executor의 역할
- **CR을 저장하지 않음** (외부 시스템: orchestrator/api-service에서 관리)
- MergePreview/Merge API 호출 시 `source_ref`, `target_ref`를 받아 실시간으로 commit hash 변환

### 409 발생 위치
```go
// internal/merge/handler.go:174-180
sourceCommit, err := h.objectManager.ResolveReference(repository, req.SourceRef)
if err != nil {
    c.JSON(http.StatusConflict, gin.H{
        "success": false,
        "error":   err.Error(),
    })
    return
}
```

### resolveReference 실제 동작 순서

```go
// internal/merge/handler.go:504-537
func (h *MergeOperationsHandler) resolveReference(repo *gogit.Repository, ref string) (plumbing.Hash, error) {
    // 1. commit SHA 파싱 + CommitObject 검증
    if hash := plumbing.NewHash(ref); !hash.IsZero() {
        if _, err := repo.CommitObject(hash); err == nil {
            return hash, nil
        }
    }

    // 2. repo.Reference(ref, true) - 원문 ref 그대로 시도
    //    "feature/foo" 같은 단순 이름은 유효한 ref name이 아니어서 실패
    //    "refs/heads/feature/foo" 같은 full ref는 여기서 성공 가능
    reference, err := repo.Reference(plumbing.ReferenceName(ref), true)
    if err == nil {
        return reference.Hash(), nil
    }

    // 3. refs/heads/{ref} 시도
    if !strings.HasPrefix(ref, "refs/") {
        branchRef := "refs/heads/" + ref
        reference, err := repo.Reference(plumbing.ReferenceName(branchRef), true)
        if err == nil {
            return reference.Hash(), nil
        }

        // 4. refs/tags/{ref} 시도
        tagRef := "refs/tags/" + ref
        reference, err = repo.Reference(plumbing.ReferenceName(tagRef), true)
        if err == nil {
            return reference.Hash(), nil
        }
    }

    // 5. 모두 실패
    return plumbing.ZeroHash, fmt.Errorf("unable to resolve reference: %s", ref)
}
```

### bare repository 관련 사실
```go
// internal/repositories/handler.go:146
Bare: true, // Always create bare repositories for server-side storage
```

- executor는 **bare repo를 사용**
- executor는 현재 구현상 remote-tracking refs를 생성/유지하지 않음
- 따라서 `refs/remotes/*` 폴백은 **대부분의 환경에서 기대하기 어려움**

---

## 3. 재현 시나리오

### 실제 엔드포인트 스펙

```
GET /repos/{owner}/{repo}/merges/preview?source_ref={source}&target_ref={target}
```

- 메서드: **GET**
- 파라미터: **Query string**
- 참고: Merge 수행은 `POST /repos/{owner}/{repo}/merges` (body)

### 타임라인 (executor 관점)

```
[T1] main branch 초기 커밋
     └── commit: A (main)

[T2] feature branch 생성 + 커밋
     └── commit: A (main)
              \
               B (feature/foo)

[T3] MergePreview API 호출 (성공)
     GET /repos/{owner}/{repo}/merges/preview?source_ref=feature/foo&target_ref=main
     
     resolveReference("feature/foo"):
       - (1) SHA 파싱: 실패
       - (2) repo.Reference("feature/foo"): 실패 (유효한 ref name 아님)
       - (3) refs/heads/feature/foo: 성공 ✓
     → 200 OK

[T4] feature branch 삭제
     DELETE /repos/{owner}/{repo}/branches/feature/foo
     → refs/heads/feature/foo 제거
     → commit B는 여전히 존재 (orphan 상태)

[T5] MergePreview API 재호출 (실패)
     GET /repos/{owner}/{repo}/merges/preview?source_ref=feature/foo&target_ref=main
     
     resolveReference("feature/foo"):
       - (1) SHA 파싱: 실패
       - (2) repo.Reference("feature/foo"): 실패
       - (3) refs/heads/feature/foo: 실패 (삭제됨) ✗
       - (4) refs/tags/feature/foo: 실패
     → "unable to resolve reference" 에러
     → HTTP 409 Conflict

[T6] commit SHA로 호출 (성공)
     GET /repos/{owner}/{repo}/merges/preview?source_ref=abc123...&target_ref=main
     
     resolveReference("abc123..."):
       - (1) SHA 파싱 + CommitObject 검증: 성공 ✓
     → 200 OK
```

### 핵심 검증 포인트

| 단계 | 입력 | 예상 결과 | 검증 목적 |
|------|------|-----------|-----------|
| T3 | branch name | 200 OK | 정상 동작 확인 |
| T5 | branch name (삭제됨) | 409 Conflict | 문제 재현 |
| T6 | commit SHA | 200 OK | 해결책 검증 |

---

## 4. 테스트 설계 원칙

### 테스트 책임 분리

| 레벨 | 검증 대상 | 환경 요구사항 |
|------|-----------|---------------|
| **Unit** | `resolveReference` 정책: 삭제된 branch name → 실패, SHA → 성공 | repo만 있으면 됨 |
| **Integration** | HTTP 매핑: 에러 타입/메시지 → 409, 응답 바디 구조 | gin router 필요 |

> **bare repo는 "환경 충실도 강화 옵션"**. 테스트 목적(resolve 정책 vs HTTP 매핑)과는 별개.

### 권장 구조: resolveReference 분리

**현재 문제**: `handler.resolveReference()`를 테스트하려면 handler 전체를 초기화해야 함

**권장 개선**:
```go
// internal/merge/resolve.go (새 파일)
package merge

// ResolveReference resolves a Git reference to a commit hash
// 패키지 레벨 함수로 분리하여 테스트 용이성 확보
func ResolveReference(repo *gogit.Repository, ref string) (plumbing.Hash, error) {
    // ... 구현
}
```

```go
// handler에서는 호출만
func (h *MergeOperationsHandler) resolveReference(repo *gogit.Repository, ref string) (plumbing.Hash, error) {
    return ResolveReference(repo, ref)
}
```

**장점**:
- 테스트에서 handler 초기화 없이 `ResolveReference()` 직접 호출
- "프로덕션 구현 복제 금지" 원칙 준수
- handler와 resolve 정책의 관심사 분리

---

## 5. Unit 테스트: ResolveReference 정책 검증

### 파일: `internal/merge/branch_deletion_test.go`

```go
package merge

import (
    "errors"
    "os"
    "path/filepath"
    "testing"
    "time"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"

    gogit "github.com/go-git/go-git/v5"
    "github.com/go-git/go-git/v5/plumbing"
    "github.com/go-git/go-git/v5/plumbing/object"
)

// Test_ResolveReference_Fails_When_BranchDeleted_But_SHA_Succeeds
//
// 검증 대상: resolveReference 정책
//   - 삭제된 branch name → 실패
//   - commit SHA → 성공
//
// 환경: non-bare repository (worktree 사용으로 커밋 생성 간편)
// 참고: executor는 bare repo 사용. 이 테스트는 resolve 정책만 검증.
//       환경 충실도가 필요하면 bare repo 테스트를 별도 작성.
func Test_ResolveReference_Fails_When_BranchDeleted_But_SHA_Succeeds(t *testing.T) {
    // === Setup ===
    tempDir := t.TempDir()
    repo, err := gogit.PlainInit(tempDir, false)
    require.NoError(t, err, "리포지토리 생성 실패")

    wt, err := repo.Worktree()
    require.NoError(t, err)

    // main 브랜치에 초기 커밋
    mainCommit := createCommit(t, wt, "initial.txt", "init", "Initial commit")

    mainRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), mainCommit)
    require.NoError(t, repo.Storer.SetReference(mainRef))

    headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main"))
    require.NoError(t, repo.Storer.SetReference(headRef))

    // feature 브랜치 생성 (슬래시 포함 - 실전 케이스)
    featureBranchName := "feature/test-deletion"
    featureRef := plumbing.NewHashReference(
        plumbing.NewBranchReferenceName(featureBranchName),
        mainCommit,
    )
    require.NoError(t, repo.Storer.SetReference(featureRef))

    // feature 브랜치로 checkout 후 커밋
    err = wt.Checkout(&gogit.CheckoutOptions{
        Branch: plumbing.NewBranchReferenceName(featureBranchName),
    })
    require.NoError(t, err)

    featureCommit := createCommit(t, wt, "feature.txt", "feature content", "Feature commit")

    featureRef = plumbing.NewHashReference(
        plumbing.NewBranchReferenceName(featureBranchName),
        featureCommit,
    )
    require.NoError(t, repo.Storer.SetReference(featureRef))

    t.Logf("Setup 완료:")
    t.Logf("  main commit:    %s", mainCommit.String()[:8])
    t.Logf("  feature commit: %s", featureCommit.String()[:8])
    t.Logf("  feature branch: %s", featureBranchName)

    // resolve 함수 (실제 구현과 동일해야 함)
    // TODO: resolveReference를 패키지 함수로 분리 후 직접 호출로 변경
    handler := setupTestHandler(t)

    // === T3: branch name으로 resolve (성공) ===
    t.Run("T3_BranchName_BeforeDeletion_Succeeds", func(t *testing.T) {
        hash, err := handler.resolveReference(repo, featureBranchName)
        require.NoError(t, err, "삭제 전 branch name resolve 실패")
        assert.Equal(t, featureCommit, hash)
    })

    // === T3-b: full ref path로도 resolve 성공 ===
    t.Run("T3b_FullRefPath_BeforeDeletion_Succeeds", func(t *testing.T) {
        fullRef := "refs/heads/" + featureBranchName
        hash, err := handler.resolveReference(repo, fullRef)
        require.NoError(t, err, "full ref path resolve 실패")
        assert.Equal(t, featureCommit, hash)
    })

    // === T4: branch 삭제 ===
    err = repo.Storer.RemoveReference(plumbing.NewBranchReferenceName(featureBranchName))
    require.NoError(t, err, "브랜치 삭제 실패")
    t.Logf("브랜치 삭제됨: %s", featureBranchName)

    // 삭제 확인 (errors.Is 사용 - 에러 래핑 대응)
    _, err = repo.Reference(plumbing.NewBranchReferenceName(featureBranchName), false)
    require.Error(t, err)
    assert.True(t, errors.Is(err, plumbing.ErrReferenceNotFound),
        "예상: ErrReferenceNotFound, 실제: %v", err)

    // === T5: branch name으로 resolve (실패) ===
    t.Run("T5_BranchName_AfterDeletion_Fails", func(t *testing.T) {
        _, err := handler.resolveReference(repo, featureBranchName)
        require.Error(t, err, "삭제 후 branch name resolve가 성공하면 안됨")
        assert.Contains(t, err.Error(), "unable to resolve reference")
        t.Logf("예상된 에러: %v", err)
    })

    // === T5-b: full ref path로도 실패 ===
    t.Run("T5b_FullRefPath_AfterDeletion_Fails", func(t *testing.T) {
        fullRef := "refs/heads/" + featureBranchName
        _, err := handler.resolveReference(repo, fullRef)
        require.Error(t, err)
        assert.Contains(t, err.Error(), "unable to resolve reference")
    })

    // === T6: commit SHA로 resolve (성공) ===
    t.Run("T6_CommitSHA_AfterDeletion_Succeeds", func(t *testing.T) {
        hash, err := handler.resolveReference(repo, featureCommit.String())
        require.NoError(t, err, "commit SHA resolve 실패 - 이건 성공해야 함")
        assert.Equal(t, featureCommit, hash)
        t.Logf("commit SHA로 resolve 성공: %s", hash.String()[:8])
    })

    // === 추가: commit 객체 자체는 여전히 존재 ===
    t.Run("CommitObject_StillExists", func(t *testing.T) {
        commit, err := repo.CommitObject(featureCommit)
        require.NoError(t, err, "commit 객체가 사라짐")
        assert.Equal(t, "Feature commit", commit.Message)
    })
}

// createCommit creates a commit with a single file
func createCommit(t *testing.T, wt *gogit.Worktree, filename, content, message string) plumbing.Hash {
    t.Helper()

    repoPath := wt.Filesystem.Root()
    filePath := filepath.Join(repoPath, filename)

    if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
        t.Fatalf("디렉토리 생성 실패: %v", err)
    }

    if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
        t.Fatalf("파일 쓰기 실패: %v", err)
    }

    if _, err := wt.Add(filename); err != nil {
        t.Fatalf("staging 실패: %v", err)
    }

    hash, err := wt.Commit(message, &gogit.CommitOptions{
        Author: &object.Signature{
            Name:  "Test",
            Email: "test@test.com",
            When:  time.Now(),
        },
    })
    if err != nil {
        t.Fatalf("commit 실패: %v", err)
    }

    return hash
}
```

---

## 6. Integration 테스트: HTTP 409 매핑 검증

### 파일: `internal/merge/handler_integration_test.go`

```go
package merge

import (
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/gin-gonic/gin"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

// Test_MergePreview_Returns409_When_SourceBranchRefDeleted
//
// 검증 대상: HTTP 매핑
//   - resolveReference 에러 → HTTP 409 Conflict
//   - 응답 바디에 에러 메시지 포함
//
// 참고: resolve 정책 자체는 Unit 테스트에서 검증
func Test_MergePreview_Returns409_When_SourceBranchRefDeleted(t *testing.T) {
    // Setup: 실제 라우트와 동일하게 구성
    gin.SetMode(gin.TestMode)
    
    // ... repo 생성, feature branch 생성, 삭제 (Unit 테스트와 동일)
    
    handler, err := NewMergePreviewHandler(testConfig())
    require.NoError(t, err)

    router := gin.New()
    // 실제 라우트: GET /repos/{owner}/{repo}/merges/preview
    router.GET("/repos/:owner/:repo/merges/preview", handler.HandleMergePreview)

    // Request: 삭제된 branch name으로 호출 (Query string)
    req := httptest.NewRequest(
        http.MethodGet,
        "/repos/testowner/testrepo/merges/preview?source_ref=feature/deleted&target_ref=main",
        nil,
    )
    w := httptest.NewRecorder()
    router.ServeHTTP(w, req)

    // Assert: HTTP 409 Conflict
    assert.Equal(t, http.StatusConflict, w.Code,
        "삭제된 branch ref로 호출 시 409 반환해야 함")

    // Assert: 에러 메시지 포함
    assert.Contains(t, w.Body.String(), "unable to resolve reference")
    assert.Contains(t, w.Body.String(), `"success":false`)
}
```

---

## 7. bare repo 테스트 (환경 충실도 강화, 선택)

executor와 동일한 bare repo 환경에서 테스트하려면:

### 방법 1: plumbing으로 직접 객체 생성 (권장)

```go
func createCommitInBareRepo(t *testing.T, repo *gogit.Repository, message string) plumbing.Hash {
    t.Helper()
    storer := repo.Storer

    // blob 생성
    blobObj := storer.NewEncodedObject()
    blobObj.SetType(plumbing.BlobObject)
    content := []byte("test content")
    blobObj.SetSize(int64(len(content)))
    writer, _ := blobObj.Writer()
    writer.Write(content)
    writer.Close()
    blobHash, _ := storer.SetEncodedObject(blobObj)

    // tree 생성
    tree := &object.Tree{
        Entries: []object.TreeEntry{{
            Name: "test.txt",
            Mode: filemode.Regular,
            Hash: blobHash,
        }},
    }
    treeObj := storer.NewEncodedObject()
    tree.Encode(treeObj)
    treeHash, _ := storer.SetEncodedObject(treeObj)

    // commit 생성
    commit := &object.Commit{
        Message:   message,
        TreeHash:  treeHash,
        Author:    object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
        Committer: object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
    }
    commitObj := storer.NewEncodedObject()
    commit.Encode(commitObj)
    commitHash, _ := storer.SetEncodedObject(commitObj)

    return commitHash
}
```

### 방법 2: non-bare에서 생성 후 bare로 clone

```go
func setupBareRepoWithCommits(t *testing.T) (*gogit.Repository, plumbing.Hash) {
    // 1. non-bare에서 커밋 생성
    nonBareDir := t.TempDir()
    nonBare, _ := gogit.PlainInit(nonBareDir, false)
    wt, _ := nonBare.Worktree()
    commitHash := createCommit(t, wt, "test.txt", "content", "Test commit")

    // 2. bare로 clone
    bareDir := t.TempDir()
    bare, _ := gogit.PlainClone(bareDir, true, &gogit.CloneOptions{
        URL: nonBareDir,
    })

    return bare, commitHash
}
```

> **주의**: 로컬 경로 clone은 환경/옵션에 따라 제약이 있을 수 있음.
> 안정적으로 하려면 plumbing 방식(방법 1)을 우선 고려.

---

## 8. 테스트 실행 방법

```bash
# Unit 테스트: resolve 정책
go test -v -run Test_ResolveReference_Fails ./internal/merge/

# 특정 서브테스트만
go test -v -run Test_ResolveReference_Fails/T5 ./internal/merge/

# Integration 테스트: 409 매핑
go test -v -run Test_MergePreview_Returns409 ./internal/merge/
```

---

## 9. 해결 방향 (참고용)

### HTTP 상태 코드 변경: 409 → 404

**현재**: 409 Conflict
**권장**: 404 Not Found

**근거**: ref가 존재하지 않는 것은 **리소스 미존재(404)**에 해당하며, 409는 **동시성/상태충돌** 의미가 강해 오해를 만든다.

### 에러 응답 구조화

현재:
```json
{
  "success": false,
  "error": "unable to resolve reference: feature/foo"
}
```

개선:
```json
{
  "success": false,
  "error_code": "GIT_REF_NOT_FOUND",
  "error": "unable to resolve reference: feature/foo",
  "ref": "feature/foo",
  "hint": "Branch may have been deleted. Try using commit SHA instead."
}
```

### orchestrator 측 변경 (executor 범위 밖)

CR 저장 시:
```json
{
  "source_branch": "feature/foo",
  "source_commit": "abc123..."  // ← 추가
}
```

CR 조회 시:
1. `source_branch`로 시도
2. 404 받으면 `source_commit`으로 fallback

---

## 10. 결론

### 핵심 메시지
**"branch name은 일시적, commit hash는 영속적"**

| 구분 | branch name | commit SHA |
|------|-------------|------------|
| 생명주기 | 삭제 가능 | **객체가 저장소에 남아있는 한** resolve 가능 |
| resolve 안정성 | branch 존재에 의존 | 객체 존재에만 의존 |
| 권장 사용처 | UI 표시용 | 영속적 참조용 |

> **주의**: branch 삭제는 ref 제거일 뿐, 객체 삭제와는 별개.
> 단, **GC/housekeeping 정책에 따라 orphan commit은 추후 제거될 수 있음**.

### 테스트가 검증하는 것

| 테스트 | 검증 대상 |
|--------|-----------|
| Unit (T3, T5, T6) | resolveReference 정책: 삭제된 branch → 실패, SHA → 성공 |
| Integration | HTTP 매핑: 해당 에러 → 409 (현재), 404 (권장) |

---

## 11. 증거 기반 다음 액션 TODO

### TODO 1: ErrReferenceNotFound → 404 분리 테스트

**목표**: merge/handler에서 `ErrReferenceNotFound`를 `404 + error_code=GIT_REF_NOT_FOUND`로 분리

**검증**:
```go
func Test_MergePreview_Returns404_When_RefNotFound(t *testing.T) {
    // 삭제된 branch ref로 호출
    // 현재: 409 → 변경 후: 404
    // error_code: GIT_REF_NOT_FOUND 확인
}
```

### TODO 2: overview/merge 에러 매핑 통일

**현재 불일치**:
- `overview/handler.go`: reference not found → 404
- `merge/handler.go`: reference not found → 409

**목표**: 공통 유틸로 통일
```go
// internal/api/errors.go (신규)
func RespondRefNotFound(c *gin.Context, ref string) {
    c.JSON(http.StatusNotFound, gin.H{
        "success":    false,
        "error_code": "GIT_REF_NOT_FOUND",
        "error":      fmt.Sprintf("unable to resolve reference: %s", ref),
        "ref":        ref,
    })
}
```

### TODO 3: 409는 진짜 conflict에서만 사용하는지 분류 테스트

**현재 아이러니**:
- ref not found → 409 ❌
- merge conflict → 400 ❌ (뒤바뀜)

**목표**:
- ref not found → **404**
- validation error → **400**
- merge conflict → **409** (진짜 충돌만)

**검증**:
```go
func Test_MergeStatusCodes_SemanticCorrectness(t *testing.T) {
    t.Run("RefNotFound_Returns404", ...)
    t.Run("ValidationError_Returns400", ...)
    t.Run("MergeConflict_Returns409", ...)
}
```

---

## 12. 최종 요약

### 문제 정의
Branch 삭제 후 MergePreview가 409를 반환하는 현상

### 근본 원인
**가설 C 채택**: executor의 HTTP 에러 매핑 불일치

1. **C1 (semantic mismatch)**: ref not found를 409로 반환 (404가 맞음)
2. **C2 (409 umbrella)**: 7가지+ 다른 에러가 모두 409로 뭉개짐
3. **아이러니**: 실제 merge conflict는 400으로 반환 (409가 맞음)

### 결정적 증거
동일 코드베이스 내 `overview` vs `merge` 패키지의 reference not found 처리 불일치

### 기각된 가설들
- **A (정책 문제)**: resolve 정책은 contract와 일치. SHA 입력 시 정상 동작.
- **D (생명주기 문제)**: commit 객체는 남아있음. git gc 미실행.

### 보류된 가설
- **B (호출자 문제)**: executor 범위에서 직접 확인 불가. 간접 증거는 존재.

### 수정 우선순위
1. `ErrReferenceNotFound` → 404로 변경 + 에러 코드 구조화
2. overview/merge 에러 매핑 통일
3. 409는 진짜 merge conflict에서만 사용
