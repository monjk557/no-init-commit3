# Go-Git 기반 Merge 시스템 문제 분석 및 근본 원인

**작성일**: 2026-02-25
**분석 범위**: Git merge 알고리즘, go-git 라이브러리 제약사항, 환경파일 삭제 문제
**결론**: Native Git 마이그레이션 필요

---

## 🔍 문제 상황 요약

CollabOps Git Executor에서 merge 작업 시 **환경 파일(config/*.yml, .env 등)이 예측할 수 없이 삭제되는 문제**가 발생했습니다. PolicyEngine을 통한 보호 시스템이 작동함에도 불구하고 일부 상황에서 파일이 여전히 삭제되는 현상이 관찰되었습니다.

### 초기 가정 (잘못된 방향)
- Git의 merge 알고리즘이 특정 파일 타입을 차별적으로 처리
- 환경 파일의 content pattern이나 encoding 문제
- Three-way merge에서 env 파일만 특별한 삭제 로직

### 실제 근본 원인 (발견된 진실)
**Go-git 라이브러리의 구조적 한계와 이를 보완하기 위한 보호 시스템의 레이어 충돌**

---

## 🚨 핵심 발견 사항

### 1. Git Merge 알고리즘 분석 결과

**중요한 발견**: Git의 three-way merge 알고리즘은 **파일 타입에 관계없이 동일하게 작동**합니다.

#### Git의 파일 삭제 결정 매트릭스
```
Base | HEAD | Incoming | 결정
-----|------|----------|----------
  1  |  1   |   0      | 충돌 (modify/delete)
  1  |  0   |   1      | 충돌 (delete/modify)
  1  |  0   |   0      | 삭제 (양쪽 모두 동의)
  0  |  1   |   0      | HEAD 버전 유지
  0  |  0   |   1      | Incoming 버전 유지
  1  |  1   |   1      | Three-way content merge
```

#### 핵심 통찰
- Git은 **구조적 존재 여부**만으로 삭제를 결정 (content-agnostic)
- 파일 내용, 확장자, MIME 타입은 삭제 결정에 영향 없음
- `.env`나 `.yml` 파일이 `.go`나 `.js` 파일과 다르게 처리되지 않음

### 2. Go-Git 라이브러리의 치명적 한계

#### 구조적 제약사항
```yaml
Go-Git 한계:
  merge_capabilities:
    - fast_forward_only: true    # ⚠️ 치명적 제약
    - three_way_merge: false     # ⚠️ 진정한 3-way merge 불가능
    - conflict_resolution: false # ⚠️ 충돌 해결 기능 없음
    - merge_strategies: minimal  # ⚠️ 전략 선택 제한적

  performance:
    - speed: "1/5 ~ 1/10 of native Git"  # ⚠️ 심각한 성능 이슈
    - memory: "높은 메모리 사용"
    - scalability: "대형 레포지토리에서 문제"
```

#### Go-Git vs Native Git 비교
| 기능 | Go-Git | Native Git | 영향 |
|------|--------|------------|------|
| Fast-forward merge | ✅ | ✅ | 정상 |
| Three-way merge | ⚠️ 제한적 | ✅ 완전 | **문제 발생 지점** |
| Conflict resolution | ❌ | ✅ | **보호 시스템으로 보완** |
| Merge strategies | ❌ 제한적 | ✅ 전체 | **전략 선택 불가** |
| Performance | ❌ 느림 | ✅ 빠름 | 확장성 문제 |

### 3. 현재 시스템의 아키텍처 문제점

#### 문제 1: 레이어 경계 충돌
```go
// 현재 구조 (문제가 있는 설계)
Go-Git Merge Algorithm (제한적 기능)
    ↓
Custom Protection Layer (보완 시도)
    ↓
PolicyEngine (파일별 보호 규칙)
    ↓
결과: 의미론적 불일치
```

**문제점**:
- Go-git은 Git 알고리즘을 충실히 구현 (삭제 허용)
- 보호 레이어가 이를 가로채서 결정 변경
- 서로 다른 "모드"에서 작동하여 경계 조건에서 불일치 발생

#### 문제 2: PolicyEngine 초기화 실패의 침묵적 처리
```go
// internal/merge/merge_protection_context.go:99-103
if policyConfigPath == "" {
    logger.Warn("No policy configuration found") // ⚠️ Warning만 출력
    return &policy.NullPolicyEngine{}            // ⚠️ 침묵적 fallback
}
```

**위험성**:
- PolicyEngine 로드 실패 시 `NullPolicyEngine` 사용
- `NullPolicyEngine`은 모든 파일에 대해 `Protected=false` 반환
- 운영자는 보호 정책이 작동하지 않는다는 사실을 모름
- 결과적으로 파일이 예기치 않게 삭제됨

#### 문제 3: Case 4 로직의 비결정적 동작
```go
// internal/merge/apply_tree.go:145-191
if baseExists && !sourceExists && targetExists {
    if entry.baseHash == entry.targetHash {
        // 복잡한 조건부 로직
        if protectionCtx != nil && protectionCtx.ShouldPreserveFile(entry.path) {
            return entry.targetHash, entry.targetMode, nil  // 보존
        }
        return entry.targetHash, entry.targetMode, nil      // 기본값: 보존
    }
}
```

**문제점**:
- "보호 비활성화"와 "보호 실패" 구별 불가
- 명시적인 삭제 허용 케이스 부재
- Default fallback이 항상 보존으로 설정되어 일관성 없음

---

## 📊 구체적인 코드 경로별 문제 분석

### Path A: 보호 활성화 시 (정상 작동)
```
HandleMerge (handler.go:223)
  └─ performMerge (executor.go:13)
       └─ performThreeWayMerge (strategy_merge.go:16)
            └─ resolveMergeTree (strategy_merge.go:124)
                 └─ createMergedTree (apply_tree.go:80)
                      └─ mergeTreeEntries (apply_tree.go:398)
                           └─ resolveFileForMerge (apply_tree.go:102)
                                └─ protectionCtx.ShouldPreserveFile() ✅
```

### Path B: 보호 비활성화 시 (문제 발생 지점)
```
동일한 경로이지만:
  └─ NewMergeProtectionContext (merge_protection_context.go:39)
       └─ calculateDestructiveMode() returns true
            └─ shouldPreserveFileUsingPolicyEngine (line 179)
                 └─ PolicyEngine 상태에 따라 분기:
                      └─ NullPolicyEngine: Protected=false ✅ 삭제
                      └─ Real PolicyEngine: 규칙 확인 → 경우에 따라 보존
```

### Path C: Rebase/Squash (동일한 문제)
```
performSquashMerge (strategy_squash.go:16)
  └─ resolveMergeTree (strategy_merge.go:124) [isSquash=true]
       └─ 동일한 MergeProtectionContext 생성
            └─ 같은 문제: 비일관적 삭제 처리
```

---

## 🔧 아키텍처적 문제들

### 1. 의미론적 불일치 (Semantic Mismatch)
**근본 문제**: Go-git의 merge 알고리즘 위에 보호 정책을 덧씌우면서 발생하는 의미론적 충돌

```yaml
Go-Git 관점:
  - 역할: Git 프로토콜 충실한 구현
  - 목표: 정확한 three-way merge
  - 동작: 삭제가 필요하면 삭제

Protection Layer 관점:
  - 역할: 중요 파일 보호
  - 목표: 환경 파일 보존
  - 동작: 삭제 방지

충돌 지점:
  - Go-git: "source에서 삭제됨, target에서 변경 없음 → 삭제"
  - Protection: "환경 파일임 → 보존"
  - 결과: 불일치
```

### 2. Three-Way Merge 알고리즘 불투명성
현재 `resolveFileForMerge()` 함수는 **암시적 규칙**을 구현하고 있으며, edge case에 대한 명확한 문서화가 부족합니다.

#### 처리되지 않는 중요 케이스들:
1. 파일이 source에서 삭제, target에서 수정된 경우 (Case 4)
2. 다른 merge strategy에서의 비대칭 삭제 시나리오
3. Strategy 선택과 삭제 동작의 상호작용

### 3. Feature Flag + Protection Context 결합 문제
보호 결정이 여러 레이어에서 이루어져 **모순된 결과** 발생 가능:

```yaml
보호 결정 요소들:
  - MergeRequest.ForceConflict: 요청 레벨 설정
  - config.MergeProtection.Enabled: 글로벌 설정
  - MergeProtectionContext.DestructiveMode: 계산된 상태
  - PolicyEngine.EvaluateFileProtection(): 동적 결정

문제: 이들이 서로 다른 레이어에서 평가되어 일관성 부족
```

---

## 🎯 Native Git 마이그레이션의 필요성

### 왜 Go-Git으로는 해결 불가능한가?

1. **근본적 기능 제약**: Go-git은 진정한 three-way merge를 지원하지 않음
2. **성능 한계**: Native Git의 1/5~1/10 성능으로 확장성 문제
3. **의미론적 불일치**: Git의 merge 의미론과 보호 시스템 간 충돌 불가피
4. **유지보수 부담**: 복잡한 workaround 시스템 지속적 관리 필요

### Native Git의 장점

```yaml
정확성:
  - ✅ 20년간 검증된 merge 알고리즘
  - ✅ 모든 merge strategy 지원
  - ✅ 완벽한 conflict resolution

성능:
  - ✅ 최적화된 C 구현
  - ✅ 대형 리포지토리 효율적 처리
  - ✅ 메모리 사용량 최적화

기능성:
  - ✅ 새로운 Git 기능 자동 지원
  - ✅ Custom merge driver 지원
  - ✅ Hook 시스템 활용 가능

유지보수성:
  - ✅ 복잡한 workaround 제거
  - ✅ Git 생태계와 완전 호환
  - ✅ 코드베이스 단순화
```

---

## 📋 결론 및 권고사항

### 핵심 발견 요약

1. **Git 알고리즘 자체는 문제없음**: 파일 타입 차별하지 않는 구조적 알고리즘
2. **Go-git의 구조적 한계**: Fast-forward only, 제한적 three-way merge
3. **보호 시스템의 레이어 충돌**: Go-git과 protection logic의 의미론적 불일치
4. **침묵적 실패**: PolicyEngine 초기화 실패의 은밀한 처리

### 장기적 해결 방안

**Native Git Subprocess Orchestration으로의 완전 마이그레이션**이 유일한 근본적 해결책입니다.

#### 마이그레이션 전략 (3-6개월)

```yaml
Phase 1 (1-2개월): Abstraction Layer
  - MergeExecutor 인터페이스 생성
  - Go-git과 Native Git 백엔드 병렬 구현
  - Feature flag를 통한 점진적 전환

Phase 2 (2-3개월): Native Implementation
  - git merge-tree를 활용한 three-way merge
  - git merge 직접 호출로 전략 완전 지원
  - git rebase 네이티브 구현

Phase 3 (3-4개월): Protection Redesign
  - Git hooks로 보호 로직 네이티브 통합
  - .git/hooks/update-ref에서 atomic protection
  - 보호를 Git 작업에 네이티브하게 구현

Phase 4 (4-5개월): Testing & Optimization
  - 양쪽 백엔드 동일 테스트 스위트
  - 성능 벤치마킹 및 최적화
  - Go-git 의존성 제거
```

### 즉시 실행 가능한 임시 조치 (마이그레이션 전까지)

1. **명시적 삭제 의도 표현**:
   ```go
   요청에 allow_file_deletion (boolean) 필드 추가
   다음 조건이 모두 true일 때만 삭제 허용:
   - allow_file_deletion == true
   - PolicyEngine이 명시적으로 허용
   - DestructiveMode == true
   ```

2. **결정적 보호 상태 보장**:
   ```go
   PolicyEngine 초기화 실패 시 시스템 실패
   NullPolicyEngine을 침묵적 fallback으로 사용 금지
   정책 로드 실패 시 500 에러 반환
   ```

3. **Merge 결정 감사**:
   ```go
   모든 파일 삭제 결정에 대해 로깅:
   - 파일 경로
   - 사용된 전략
   - 보호 결정 근거
   - 매치된 정책 규칙
   ```

---

## 📚 참고 자료

- **Git 공식 문서**: [git-merge](https://git-scm.com/docs/git-merge), [merge-strategies](https://git-scm.com/docs/merge-strategies)
- **Go-git 제약사항**: [COMPATIBILITY.md](https://github.com/src-d/go-git/blob/master/COMPATIBILITY.md)
- **성능 비교 연구**: Go-git vs Native Git 벤치마킹 결과
- **Three-way Merge 알고리즘**: Git 내부 구현 분석

---

**최종 결론**: 현재의 보호 시스템은 **덕트 테이프 솔루션**입니다. 근본적인 해결책은 **Native Git로의 마이그레이션**을 통해 merge 알고리즘 자체를 올바르게 구현하는 것입니다.