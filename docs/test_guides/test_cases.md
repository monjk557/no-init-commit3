# Edge Case Test Cases by Domain

> **목적**: 도메인별 edge case를 탑다운으로 나열하여 테스트 커버리지 갭 식별
> **원칙**: "이 테스트가 막는 회귀"를 1문장으로 설명 가능해야 함

---

## 0. 차이 나는 축 (Cross-Cutting Concerns)

> go-git/호스팅별로 동작이 갈라지기 쉬운 9개 축. 각 도메인에 "주입"하여 커버리지 갭 최소화.

| 축 | 설명 | 회귀 문장 템플릿 |
|----|------|-----------------|
| **Ref 문법/해석** | branch/tag/sha/"특수 ref" (HEAD, @{-1}, refs/pull/*, refs/merge-requests/*) | "사용자 입력 ref가 tag/branch/sha로 해석이 갈려도 결과가 비결정적으로 바뀌지 않는다" |
| **권한/보호 정책** | protected branch, required status checks, fast-forward only, signed commits | "보호 브랜치/FF-only 정책이 활성화돼도 우회되거나 애매한 상태코드가 나오지 않는다" |
| **대용량/제한** | diff/patch truncation, max files, max blob size, pack/stream | "대용량 diff/blob에서 응답이 잘려도 클라이언트가 깨지지 않도록 계약이 유지된다" |
| **비결정성/경합** | concurrent delete/rename/merge, GC/packing 중 읽기 | "동시 업데이트에서 repo ref가 손상되거나 부분 적용되지 않는다" |
| **파일 시스템** | case sensitivity(mac), unicode normalization, symlink/submodule | "대소문자/유니코드/심볼릭링크가 섞여도 path/branch가 충돌하거나 탈출하지 않는다" |
| **프로토콜** | smart HTTP/SSH, LFS, partial clone, shallow clone | "실제 git 클라이언트가 기대하는 handshake를 만족하며, 실패 시 원인이 명확히 표현된다" |
| **Object Format/Storage** | pack/loose 혼재, packed-refs, bitmap, commit-graph, alternates | "repo 저장 포맷(pack/loose/packed-refs)이 달라도 결과가 동일하고 성능만 달라진다" |
| **Lineage/History Oddities** | future timestamp, identical committer time, non-monotonic time, grafts, rewritten refs | "커밋 타임스탬프가 이상해도 merge-base/정렬이 비결정적으로 바뀌지 않는다" |
| **Error Observability** | request_id, error.code, retry-after, correlation id, redaction | "실패 시 원인이 machine-readable(error.code)로 고정되며 민감 정보가 노출되지 않는다" |

---

## 0.1 Ref Input Contract

> **목적**: ref 입력 지원 범위/실패 코드를 고정하여 404/409 혼선을 제거한다.
> **원칙**: "지원 안 하는 ref는 400(UNSUPPORTED_REF)로 명확히 거절", "존재하지 않는 ref는 404(REF_NOT_FOUND)".

| 입력 | 지원 | 실패 코드 | 실패 reason (error.code) |
|------|------|----------|-------------------------|
| `main` (branch) | ✅ | 404 | `REF_NOT_FOUND` |
| `v1.0.0` (tag) | ✅ | 404 | `REF_NOT_FOUND` |
| `a1b2c3d4e5f6...` (full sha) | ✅ | 404 | `OBJECT_NOT_FOUND` |
| `a1b2c3d` (short sha, 7+) | ✅ (권장) | 404 | `OBJECT_NOT_FOUND` |
| `xyz` (malformed sha) | - | 400 | `INVALID_REF` (길이/hex 아님) |
| `deadbee` (sha-like branch) | ✅ | - | **branch 우선** (tag 충돌 시만 `AMBIGUOUS_REF`) |
| `refs/heads/main` (full ref) | ❌ | 400 | `UNSUPPORTED_REF` (내부 ref 직접 입력 금지) |
| `refs/tags/v1.0.0` (full tag ref) | ❌ | 400 | `UNSUPPORTED_REF` (내부 ref 일관성) |
| `HEAD` | 정책 | 400 | `UNSUPPORTED_REF` |
| `origin/main` | ❌ | 400 | `UNSUPPORTED_REF` |
| `refs/pull/1/head` | GitHub only | 400 | `UNSUPPORTED_REF` |
| `refs/merge-requests/1/head` | GitLab only | 400 | `UNSUPPORTED_REF` |
| `@{-1}` | ❌ | 400 | `UNSUPPORTED_REF` |
| `main^`, `main~1` | ❌ | 400 | `UNSUPPORTED_REF` (rev-parse 표현식 금지) |
| `main:README.md` | ❌ | 400 | `UNSUPPORTED_REF` (tree-ish:path 금지) |
| `FETCH_HEAD`, `ORIG_HEAD` | ❌ | 400 | `UNSUPPORTED_REF` (로컬 state ref 금지) |
| `feature ` (공백/개행) | ❌ | 400 | `INVALID_REF` |
| `` (빈 문자열), `.`, `..` | ❌ | 400 | `INVALID_REF` |

### Ref Resolution 규칙

1. **branch 우선**: sha-like 이름이 branch로 존재하면 branch로 해석 (sha 모양일 뿐, ambiguous 아님)
2. **tag dereference**: annotated tag → commit으로 역참조, lightweight tag → commit 직접
3. **명시적 거절**: 지원하지 않는 ref 문법은 400 + `UNSUPPORTED_REF`
4. **404 vs 400**: 존재하지 않으면 404, 문법 오류면 400
5. **ambiguous는 branch vs tag 충돌에만**: 같은 이름의 branch와 tag가 동시에 존재할 때만 400 + `AMBIGUOUS_REF`

---

## 1. Merge / MergePreview (가장 복잡)

### 1.1 Ref Resolution 문제 (409 버그 원인)
- [x] `Test_MergePreview_DeletedSourceBranch_Returns404` - 삭제된 source 브랜치
- [x] `Test_MergePreview_DeletedTargetBranch_Returns404` - 삭제된 target 브랜치
- [x] `Test_MergePreview_DeletedBothBranches_Returns404` - 둘 다 삭제된 경우
- [x] `Test_MergePreview_BranchNameWithSlash_ReturnsSuccess` - feature/foo 형태
- [x] `Test_MergePreview_BranchNameWithMultipleSlashes_ReturnsSuccess` - feature/foo/bar
- [x] `Test_MergePreview_CommitSHA_ReturnsSuccess` - 브랜치 대신 SHA 직접 사용
- [x] `Test_MergePreview_ShortSHA_ReturnsSuccess` - 7자리 short SHA
- [x] `Test_MergePreview_NonExistentSHA_Returns404_OBJECT_NOT_FOUND` ⭐ P0
  - 회귀: "존재하지 않는 SHA는 404로 떨어져 409/400과 섞이지 않는다"
- [x] `Test_MergePreview_MalformedRef_Returns400_INVALID_REF` ⭐ P0
  - 회귀: "형식이 명확히 잘못된 ref는 400으로 거절된다 (공백, rev-parse, tree-ish 등)"
  - 참고: Branch-First Policy에 따라 `xyz123`, `deadbeef` 같은 입력은 malformed가 아닌 404
- [x] `Test_MergePreview_AmbiguousRef_Returns400_AMBIGUOUS_REF` - 브랜치/태그 이름 충돌

### 1.1A Ref Resolution 확장 — "성공하지만 다른 객체를 가리키는" 케이스 ⚠️ 고위험
- [ ] `Test_MergePreview_RefIsHEAD_Returns400_UNSUPPORTED_REF` - HEAD를 ref로 입력
- [ ] `Test_MergePreview_RefIsFullRef_RefsHeadsMain_Returns400_UNSUPPORTED_REF`
  - 회귀: "refs/heads/* 같은 내부 ref를 직접 넣어도 우회되지 않는다"
- [ ] `Test_MergePreview_RefIsAnnotatedTag_DereferencesToCommit_Deterministic` ⭐ P0
  - 회귀: "annotated tag(tag object)를 입력해도 commit으로 안정적으로 역참조된다"
- [ ] `Test_MergePreview_RefIsLightweightTag_PointsToCommit_Deterministic`
  - 회귀: "lightweight tag가 commit을 가리킬 때 branch와 동일하게 처리된다"
- [ ] `Test_MergePreview_RefLooksLikeSHA_ButIsBranchName_PrefersBranch` ⭐ P0
  - 회귀: "sha 모양 branch가 있으면 sha로 해석 안 하고 branch로 해석한다"
- [ ] `Test_MergePreview_RemoteStyleRef_OriginMain_Returns400` - origin/main 입력 실수 방지
- [ ] `Test_MergePreview_RefContainsWhitespaceOrNewline_Returns400`
  - 회귀: "ref에 공백/개행이 섞여도 parser가 흔들리거나 로그가 오염되지 않는다"
- [ ] `Test_MergePreview_RefIsEmptyOrDotOrDotDot_Returns400`
  - 회귀: "빈 ref, '.', '..'가 들어와도 git 내부 path/ref로 흘러가지 않는다"
- [ ] `Test_MergePreview_RefIsRevParseExpression_MainCaret_Returns400_UNSUPPORTED_REF`
  - 회귀: "rev-parse 문법(main^, main~1)이 입력으로 들어와도 다른 ref로 오해되지 않는다"
- [ ] `Test_MergePreview_RefIsTreeishPath_Returns400_UNSUPPORTED_REF`
  - 회귀: "tree-ish:path 입력이 path traversal/오해로 이어지지 않는다"

### 1.1B 호스팅별 특수 Ref ⚠️ 고위험
- [ ] `Test_MergePreview_PullRequestRef_Returns400_UNSUPPORTED_REF` - refs/pull/<id>/head
- [ ] `Test_MergePreview_MRRef_Returns400_UNSUPPORTED_REF` - refs/merge-requests/<id>/head
- [ ] `Test_MergePreview_PullRequestRef_OnNonGitHubHosting_Returns400`
  - 회귀: "호스팅별 특수 ref가 타 호스팅에서 조용히 다른 걸 가리키지 않는다"
- [ ] `Test_MergePreview_MRRef_OnNonGitLabHosting_Returns400`
  - 회귀: "MR ref가 일반 ref처럼 오해되어 merge base가 틀어지지 않는다"

> 포인트: "지원 안 하면 400으로 명확히 거절" / "지원하면 동작이 고정". 애매하게 404/409 섞이면 디버깅 지옥.

### 1.2 Merge Base 계산
- [ ] `Test_FindMergeBase_NormalBranching_ReturnsBranchPoint` - 일반 분기
- [ ] `Test_FindMergeBase_LinearHistory_ReturnsAncestor` - 선형 히스토리
- [ ] `Test_FindMergeBase_SameCommit_ReturnsSelf` - 같은 커밋
- [ ] `Test_FindMergeBase_NoCommonAncestor_Returns422_NO_COMMON_ANCESTOR`
  - 회귀: "공통 조상 없는 레포에서 에러가 409/500으로 뭉개지지 않는다"
- [ ] `Test_FindMergeBase_MultipleMergeBases_ReturnsDeterministicByRule` ⭐ P0
  - 회귀: "multiple merge base에서 결과가 순회 순서에 따라 바뀌지 않는다"
  - 규칙: **가장 최근(committer time) merge base 선택**
- [ ] `Test_FindMergeBase_MultipleMergeBases_Returns409_AMBIGUOUS_MERGE_BASE` (대안)
  - 회귀: "criss-cross에서 ambiguous 상태를 명시적으로 알린다"
- [ ] `Test_FindMergeBase_DeepHistory_HandlesPerformance` - 1000+ 커밋 히스토리
- [ ] `Test_FindMergeBase_OctopusMerge_ReturnsCorrectBase` - 3개 이상 부모

### 1.2A Merge Base / FF — 정책 & 보호 브랜치 ⚠️ 고위험
- [ ] `Test_MergePreview_ProtectedTargetBranch_Returns403_POLICY_VIOLATION` ⭐ P0
  - 회귀: "보호 브랜치 정책 위반이 409/500으로 변질되지 않는다"
- [ ] `Test_MergePreview_FastForwardOnlyPolicy_BlocksNonFF_Returns403`
- [ ] `Test_MergePreview_RequiredStatusChecksNotMet_Returns403_POLICY_VIOLATION`
  - 회귀: "required checks 미충족이 애매한 에러로 섞이지 않는다"
- [ ] `Test_MergePreview_InsufficientPermission_Returns403_FORBIDDEN`
  - 회귀: "권한 부족이 404로 위장되지 않는다 (보안 정책이면 반대로 고정)"
- [ ] `Test_MergePreview_SignedCommitsRequired_UnsignedCommit_Returns403_POLICY_VIOLATION`
  - 회귀: "signed-commit 정책이 있어도 우회되거나 모호한 상태코드가 나오지 않는다"
- [ ] `Test_MergePreview_AlreadyMerged_ReturnsAlreadyMergedTrue` - 응답 스키마 고정

### 1.2B MergePreview 결과 스키마 — 경계 케이스 ⚠️ 고위험
- [ ] `Test_MergePreview_SourceEqualsTarget_ReturnsAlreadyMergedOrNoop`
  - 회귀: "source==target 입력 실수에서 500/409가 나지 않는다"
- [ ] `Test_MergePreview_OrphanBranch_NoCommonAncestor_Returns422_NO_COMMON_ANCESTOR`
  - 회귀: "공통 조상 없는 레포에서 에러가 409/500으로 뭉개지지 않는다"
- [ ] `Test_MergePreview_SourceIsAncestor_AlreadyMerged_HasConflictsFalse_StableSchema`
  - 회귀: "already-merged 상태에서 conflicts/diff 필드가 흔들리지 않는다"

### 1.2C Preview와 Merge 정책 일관성 ⚠️ 고위험
- [ ] `Test_MergePreview_PolicyAllows_ButMergeRejects_IsImpossible`
  - 회귀: "preview에서 가능하다고 한 merge가 실제 merge에서 정책으로 거절되지 않는다"
- [ ] `Test_MergePreview_PolicyRejects_MergeAlsoRejects_SameErrorCode`
  - 회귀: "preview와 merge가 같은 정책 위반에 대해 동일한 status/error.code를 반환한다"
- [ ] `Test_Merge_PolicyStateChangesBetweenPreviewAndMerge_Returns409Or403_ByContract`
  - 회귀: "preview 후 정책/상태가 변해도 500이나 이상한 409가 나오지 않는다"

### 1.3 Fast-Forward 케이스
- [ ] `Test_MergePreview_FastForward_IsFastForwardTrue` - FF 가능
- [ ] `Test_MergePreview_NotFastForward_IsFastForwardFalse` - FF 불가
- [ ] `Test_MergePreview_TargetIsAncestor_IsFastForwardTrue` - target이 조상
- [ ] `Test_MergePreview_SourceIsAncestor_AlreadyMerged` - source가 조상 (이미 머지됨)

### 1.4 충돌 감지
- [ ] `Test_MergePreview_NoConflict_HasConflictsFalse` - 충돌 없음
- [ ] `Test_MergePreview_SingleFileConflict_HasConflictsTrue` - 단일 파일 충돌
- [ ] `Test_MergePreview_MultipleFileConflicts_ReturnsAllConflicts` - 다중 파일 충돌
- [ ] `Test_MergePreview_SameLineConflict_DetectsConflict` - 같은 줄 수정
- [ ] `Test_MergePreview_AdjacentLineConflict_DetectsConflict` - 인접 줄 수정
- [ ] `Test_MergePreview_BinaryFileConflict_MarksAsBinary` - 바이너리 파일 충돌
- [ ] `Test_MergePreview_DeleteVsModify_DetectsConflict` - 삭제 vs 수정 충돌
- [ ] `Test_MergePreview_RenameConflict_DetectsConflict` - 이름 변경 충돌
- [ ] `Test_MergePreview_DirectoryFileConflict_DetectsConflict` - 디렉토리/파일 충돌

### 1.4A 충돌 — rename/add/add, filemode, eol ⚠️ 고위험
- [ ] `Test_MergePreview_AddAddSamePath_DetectsConflict` - 양쪽에서 같은 경로에 파일 추가
- [ ] `Test_MergePreview_FileModeChange_ExecutableBit_Handled` - 100755 vs 100644
- [ ] `Test_MergePreview_EOLOnlyChange_DoesNotCreateFalseConflict` - CRLF/LF 차이
- [ ] `Test_MergePreview_SubmoduleChange_IsReportedAsSubmodule`
- [ ] `Test_MergePreview_PathRenameCaseOnly_CaseInsensitiveFS_Deterministic` ⭐ R4_weirdfs 연결

### 1.4B 충돌 — 현업 최다 3개 ⚠️ 고위험
- [ ] `Test_MergePreview_WhitespaceOnlyChange_DoesNotCreateFalseConflict_WhenIgnoreWhitespaceEnabled`
  - 회귀: "whitespace 옵션이 있을 때 충돌 판정이 일관된다"
- [ ] `Test_MergePreview_BinaryAndTextMix_ReportsBinaryWithoutPatch`
  - 회귀: "바이너리 + 텍스트 혼합 diff에서도 patch 계약이 깨지지 않는다"
- [ ] `Test_MergePreview_RenameToExistingPath_DetectsConflict`
  - 회귀: "rename이 기존 경로와 충돌해도 누락 없이 감지된다"

### 1.5 Diff 계산 (MergePreview Response)
- [ ] `Test_MergePreview_AddedFiles_CountsCorrectly` - 추가된 파일 수
- [ ] `Test_MergePreview_ModifiedFiles_CountsCorrectly` - 수정된 파일 수
- [ ] `Test_MergePreview_DeletedFiles_CountsCorrectly` - 삭제된 파일 수
- [ ] `Test_MergePreview_RenamedFiles_CountsCorrectly` - 이름 변경 파일 수
- [ ] `Test_MergePreview_LargeFile_HandlesMaxSize` - 대용량 파일 (>1MB)
- [ ] `Test_MergePreview_ManyFiles_HandlesMaxFiles` - 파일 수 제한 (1000+)
- [ ] `Test_MergePreview_EmptyFiles_HandlesCorrectly` - 빈 파일

### 1.5A Diff/Response — truncation 계약 고정 ⚠️ 고위험
- [ ] `Test_MergePreview_LargeDiff_ReturnsTruncatedFlagAndStableCounts`
- [ ] `Test_MergePreview_MaxFilesExceeded_ReturnsPartialAndNextCursor`
- [ ] `Test_MergePreview_BinaryDiff_ReturnsNoPatchButHasMetadata`

### 1.5B Diff Pagination 안정성 ⚠️ 고위험
- [ ] `Test_MergePreview_DiffPagination_StableOrderAcrossPages`
  - 회귀: "페이지네이션 중에도 파일 순서가 바뀌어 중복/누락이 생기지 않는다"
- [ ] `Test_MergePreview_DiffPagination_ResumeWithCursor_NoOverlapNoGap`
  - 회귀: "cursor 기반 재개 시 overlap/skip이 발생하지 않는다"

### 1.6 실제 Merge 수행 (MergeOperations)
- [ ] `Test_Merge_FastForward_UpdatesRef` - FF merge 후 ref 업데이트
- [ ] `Test_Merge_ThreeWay_CreatesMergeCommit` - 3-way merge 커밋 생성
- [ ] `Test_Merge_WithConflicts_Returns422_MERGE_CONFLICT` - 충돌 시 에러
- [ ] `Test_Merge_Squash_CreatesSingleCommit` - squash merge
- [ ] `Test_Merge_NoFastForward_CreatesMergeCommit` - --no-ff 옵션
- [ ] `Test_Merge_FailsRollback_OriginalStateRestored` - 실패 시 롤백

### 1.6A 실제 Merge — 원자성/락/경합 ⚠️ 고위험
- [ ] `Test_Merge_ConcurrentTargetAdvanced_Returns409_CONCURRENT_UPDATE` ⭐ P0
  - 회귀: "merge 하는 동안 target이 다른 커밋으로 이동해도 ref가 손상되지 않는다"
- [ ] `Test_Merge_TargetDeletedDuringMerge_Returns409_CONCURRENT_UPDATE` ⭐ P0
  - 회귀: "merge 도중 target이 삭제돼도 repo가 손상되지 않는다"
- [ ] `Test_Merge_SourceAdvancedDuringMerge_Returns409_CONCURRENT_UPDATE`
  - 회귀: "merge 중 source가 변경돼도 잘못된 결과로 머지되지 않는다"
- [ ] `Test_Merge_FFOnly_TargetAdvancedDuringMerge_Returns409_Not403`
  - 회귀: "경합은 정책위반(403)이 아니라 동시성(409)으로 분류가 흔들리지 않는다"
- [ ] `Test_Merge_RepoLockContention_Returns423_LOCKED`
  - API는 423 반환, 서버 내부 retry는 일정 횟수 후 423
- [ ] `Test_Merge_FailureMidway_DoesNotLeaveGitStateFiles`
  - 회귀: "MERGE_HEAD, CHERRY_PICK_HEAD, REBASE_HEAD, sequencer 등이 남아 연쇄 실패하지 않는다"

### 1.6B 실제 Merge — Hook/Server-side Rules ⚠️ 고위험
- [ ] `Test_Merge_RejectsWhenBranchProtectionBlocksPush_Returns403_POLICY_VIOLATION`
  - 회귀: "merge가 내부적으로 ref update(push)로 귀결될 때 보호 정책이 일관되게 적용된다"
- [ ] `Test_Merge_NoFastForwardPolicy_RejectsMergeCommit_Returns403`
  - 회귀: "FF-only 정책이 merge commit 생성 경로를 막아도 우회되지 않는다"

### 1.7 성능/제한
- [ ] `Test_MergePreview_LargeDiff_Timeout` - 대용량 diff 타임아웃
- [ ] `Test_MergePreview_DeepNesting_HandlesCorrectly` - 깊은 디렉토리 구조
- [ ] `Test_MergePreview_ConcurrentRequests_NoRaceCondition` - 동시 요청

---

## 2. Branches

### 2.1 Create Branch
- [ ] `Test_CreateBranch_ValidName_ReturnsSuccess` - 정상 생성
- [ ] `Test_CreateBranch_DuplicateName_Returns409_ALREADY_EXISTS` - 중복 이름
- [ ] `Test_CreateBranch_WithForce_OverwritesExisting` - force 옵션
- [ ] `Test_CreateBranch_FromCommitSHA_ReturnsSuccess` - SHA에서 생성
- [ ] `Test_CreateBranch_FromTag_ReturnsSuccess` - 태그에서 생성
- [ ] `Test_CreateBranch_InvalidSource_Returns404` - 존재하지 않는 source
- [ ] `Test_CreateBranch_SlashInName_ReturnsSuccess` - feature/foo
- [ ] `Test_CreateBranch_MultipleSlashes_ReturnsSuccess` - feature/foo/bar
- [ ] `Test_CreateBranch_SpecialChars_Returns400` - 특수문자 (.., ~, ^, :)
- [ ] `Test_CreateBranch_StartWithSlash_Returns400` - /feature
- [ ] `Test_CreateBranch_EndWithSlash_Returns400` - feature/
- [ ] `Test_CreateBranch_DoubleDot_Returns400` - feature..bar
- [ ] `Test_CreateBranch_Tilde_Returns400` - feature~1
- [ ] `Test_CreateBranch_Space_Returns400` - "feature branch"
- [ ] `Test_CreateBranch_RefsHeads_Returns400` - refs/heads/... 직접 입력

### 2.1A Git이 금지하는 패턴 ⚠️ 고위험
- [ ] `Test_CreateBranch_EndsWithDotLock_Returns400` - feature.lock
- [ ] `Test_CreateBranch_ContainsAtBrace_Returns400` - foo@{1}
- [ ] `Test_CreateBranch_ContainsBackslash_Returns400` - Windows/escape
- [ ] `Test_CreateBranch_ContainsQuestionAsteriskBracket_Returns400`
- [ ] `Test_CreateBranch_UpperLowerCollision_CaseInsensitiveFS_Returns409`
  - 회귀: "mac 기본 FS에서 Feature vs feature 충돌이 적절히 처리된다"
- [ ] `Test_CreateBranch_NameUnicodeNormalization_CollisionsHandled` - NFC vs NFD
- [ ] `Test_CreateBranch_NameIsDashOrDot_Returns400` ⭐ -, . 단독/특수 케이스
- [ ] `Test_CreateBranch_LeadingTrailingWhitespace_Returns400` ⭐ " main" / "main "

### 2.2 Update Branch
- [ ] `Test_UpdateBranch_Rename_ReturnsSuccess` - 이름 변경
- [ ] `Test_UpdateBranch_RenameToDuplicate_Returns409` - 중복 이름으로 변경
- [ ] `Test_UpdateBranch_FastForward_ReturnsSuccess` - FF 업데이트
- [ ] `Test_UpdateBranch_NonFastForward_Returns400` - non-FF 업데이트 거부
- [ ] `Test_UpdateBranch_NonFastForwardForce_ReturnsSuccess` - force non-FF
- [ ] `Test_UpdateBranch_NonExistent_Returns404` - 존재하지 않는 브랜치
- [ ] `Test_UpdateBranch_RenameDefault_Returns400` - 기본 브랜치 이름 변경 제한

### 2.2A Rename/Delete 정책 ⚠️ 고위험
- [ ] `Test_UpdateBranch_RenameToDifferentCase_CaseInsensitiveFS_BehavesDeterministically`
- [ ] `Test_UpdateBranch_RenameCaseOnly_WhenOtherCaseExists_Returns409` ⭐ mac 자주 터짐
- [ ] `Test_DeleteBranch_ProtectedBranch_Returns403_POLICY_VIOLATION`

### 2.2B 삭제/리네임 경합 ⚠️ 고위험
- [ ] `Test_DeleteBranch_ConcurrentDelete_SecondReturns404`
  - 회귀: "동시 삭제에서 하나는 성공, 다른 하나는 404로 안정적으로 종료한다"
- [ ] `Test_UpdateBranch_Rename_ConcurrentCreateSameName_Returns409_ALREADY_EXISTS`
  - 회귀: "rename 중 동일 이름 create 경합이 나도 ref가 손상되지 않는다"
- [ ] `Test_UpdateBranch_RenameWhileClientReads_ListDeterministic`
  - 회귀: "rename 중에도 list API가 깨지거나 중복 ref를 내지 않는다"

### 2.3 Delete Branch
- [ ] `Test_DeleteBranch_Existing_ReturnsSuccess` - 정상 삭제
- [ ] `Test_DeleteBranch_NonExistent_Returns404` - 존재하지 않는 브랜치
- [ ] `Test_DeleteBranch_DefaultBranch_Returns400` - 기본 브랜치 삭제 시도
- [ ] `Test_DeleteBranch_CurrentHEAD_Returns400` - HEAD가 가리키는 브랜치
- [ ] `Test_DeleteBranch_WithSlash_ReturnsSuccess` - feature/foo 삭제
- [ ] `Test_DeleteBranch_NotMerged_RequiresForce` - 머지되지 않은 브랜치
- [ ] `Test_DeleteBranch_AlreadyDeleted_Returns404` - 이미 삭제된 브랜치

### 2.4 List Branches
- [ ] `Test_GetBranches_EmptyRepo_ReturnsEmptyArray` - 빈 레포
- [ ] `Test_GetBranches_WithBranches_ReturnsAll` - 브랜치 목록
- [ ] `Test_GetBranches_Pagination_ReturnsCorrectPage` - 페이지네이션
- [ ] `Test_GetBranches_PaginationOutOfRange_ReturnsEmptyArray` - 범위 초과
- [ ] `Test_GetBranches_SortByName_ReturnsSorted` - 이름순 정렬
- [ ] `Test_GetBranches_SortByDate_ReturnsSorted` - 날짜순 정렬
- [ ] `Test_GetBranches_FilterByPattern_ReturnsMatching` - 패턴 필터

### 2.5 Validate Branch Name
- [ ] `Test_ValidateBranchName_Available_ReturnsTrue` - 사용 가능
- [ ] `Test_ValidateBranchName_AlreadyExists_ReturnsFalse` - 이미 존재
- [ ] `Test_ValidateBranchName_InvalidFormat_ReturnsFalse` - 잘못된 형식
- [ ] `Test_ValidateBranchName_RepoNotFound_Returns404` - 레포 없음

---

## 3. Commits

### 3.1 Multi-File Commit
- [ ] `Test_MultiFileCommit_CreateSingle_ReturnsSuccess` - 단일 파일 생성
- [ ] `Test_MultiFileCommit_CreateMultiple_ReturnsSuccess` - 다중 파일 생성
- [ ] `Test_MultiFileCommit_UpdateFile_ReturnsSuccess` - 파일 수정
- [ ] `Test_MultiFileCommit_DeleteFile_ReturnsSuccess` - 파일 삭제
- [ ] `Test_MultiFileCommit_MixedOperations_ReturnsSuccess` - 생성/수정/삭제 혼합
- [ ] `Test_MultiFileCommit_DuplicateCreate_Returns409` - 이미 존재하는 파일 생성
- [ ] `Test_MultiFileCommit_UpdateNonExistent_Returns404` - 존재하지 않는 파일 수정
- [ ] `Test_MultiFileCommit_DeleteNonExistent_Returns404` - 존재하지 않는 파일 삭제
- [ ] `Test_MultiFileCommit_EmptyOperations_Returns400` - 빈 작업 목록
- [ ] `Test_MultiFileCommit_EmptyContent_ReturnsSuccess` - 빈 파일 생성
- [ ] `Test_MultiFileCommit_LargeFile_ReturnsSuccess` - 대용량 파일
- [ ] `Test_MultiFileCommit_BinaryFile_ReturnsSuccess` - 바이너리 파일
- [ ] `Test_MultiFileCommit_Subdirectory_ReturnsSuccess` - 서브디렉토리 파일
- [ ] `Test_MultiFileCommit_DeepNesting_ReturnsSuccess` - 깊은 디렉토리 구조
- [ ] `Test_MultiFileCommit_SpecialCharsInPath_ReturnsSuccess` - 특수문자 경로

### 3.1A 요청 재시도 / 멱등성 ⚠️ 고위험
- [ ] `Test_MultiFileCommit_IdempotencyKey_RetryDoesNotDuplicateCommit`
- [ ] `Test_MultiFileCommit_ClientRetryAfterTimeout_DoesNotCreateTwoCommits`
  - 회귀: "서버는 성공했는데 클라가 타임아웃 후 재시도해도 중복 커밋이 생기지 않는다"
- [ ] `Test_MultiFileCommit_IdempotencyKey_ServerCrashAfterWrite_NoDuplicateOnRetry`
  - 회귀: "서버가 중간에 죽어도 재시도 시 중복 커밋이 생기지 않는다"
- [ ] `Test_MultiFileCommit_ReturnsCommitSHA_AndIsReadableImmediately`
  - 회귀: "응답한 SHA는 즉시 GetCommit/Blob에서 조회 가능하다(쓰기 후 읽기 일관성)"

### 3.1B Author/Committer/Signing/Timezone ⚠️ 고위험
- [ ] `Test_MultiFileCommit_MissingAuthor_Returns400`
- [ ] `Test_MultiFileCommit_InvalidEmail_AllowsOrRejectsByContract`
- [ ] `Test_MultiFileCommit_CommitMessageEncoding_UTF8AndNonUTF8`
- [ ] `Test_MultiFileCommit_LargeMessage_RejectsOrTruncatesExplicitly`

### 3.1C Line endings / filemode / symlink ⚠️ 고위험
- [ ] `Test_MultiFileCommit_SymlinkCreation_HandledOrRejected`
- [ ] `Test_MultiFileCommit_FileModeExecutableBit_Preserved`

### 3.1D Path / Binary Validation ⚠️ 고위험
- [ ] `Test_MultiFileCommit_PathNormalization_NoEscape` ⭐ a/../b 같은 입력은 400
- [ ] `Test_MultiFileCommit_BinaryContent_Base64Validation` ⭐ binary 허용 시 포맷 검증
- [ ] `Test_MultiFileCommit_DuplicatePathInOperations_Returns400` ⭐ 같은 path 중복 시 400
  - 회귀: "서버가 순서 의존적으로 적용해서 부분 반영되는 회귀를 막는다"

### 3.2 Branch Creation with Commit
- [ ] `Test_MultiFileCommit_CreateBranch_ReturnsSuccess` - 새 브랜치 생성
- [ ] `Test_MultiFileCommit_CreateBranch_DuplicateName_Returns409` - 중복 브랜치
- [ ] `Test_MultiFileCommit_CreateBranch_CommitFailure_RollbacksBranch` - 실패 시 롤백
- [ ] `Test_MultiFileCommit_CreateBranch_InvalidSource_Returns404` - 잘못된 source

### 3.3 Commit History
- [ ] `Test_CommitsAPI_List_ReturnsCommits` - 커밋 목록
- [ ] `Test_CommitsAPI_Pagination_ReturnsCorrectPage` - 페이지네이션
- [ ] `Test_CommitsAPI_PaginationOutOfRange_ReturnsEmptyArray` - 범위 초과
- [ ] `Test_CommitsAPI_ByRef_ReturnsCommitsFromRef` - ref 기준 조회
- [ ] `Test_CommitsAPI_ByPath_ReturnsCommitsAffectingPath` - 경로 기준 조회
- [ ] `Test_CommitsAPI_CompareMode_ReturnsOnlyAheadCommits` - source/target 비교

### 3.4 Single Commit
- [ ] `Test_GetCommit_ValidSHA_ReturnsCommit` - 정상 조회
- [ ] `Test_GetCommit_ShortSHA_ReturnsCommit` - short SHA
- [ ] `Test_GetCommit_InvalidSHA_Returns404` - 잘못된 SHA
- [ ] `Test_GetCommit_WithFiles_ReturnsFilesChanged` - 변경된 파일 목록

### 3.5 트랜잭션/원자성
- [ ] `Test_MultiFileCommit_PartialFailure_NoPartialCommit` - 부분 실패 시 전체 롤백
- [ ] `Test_MultiFileCommit_ConcurrentSameBranch_NoConflict` - 동시 커밋 처리
- [ ] `Test_MultiFileCommit_ConcurrentSameFile_Returns409` - 동일 파일 동시 수정

### 3.5A 동시성 — 같은 브랜치 다른 파일 ⚠️ 고위험
- [ ] `Test_MultiFileCommit_ConcurrentSameBranch_DifferentFiles_OneFastForwardOne409`
  - 회귀: "read old head -> write new head에서 경합 시 하나는 409로 튕긴다"

---

## 4. Diffs

### 4.1 Commit Diff
- [ ] `Test_CommitDiff_SingleLineAddition_ReturnsCorrectCounts` - 1줄 추가
- [ ] `Test_CommitDiff_SingleLineDeletion_ReturnsCorrectCounts` - 1줄 삭제
- [ ] `Test_CommitDiff_FileModification_ReturnsCorrectDelta` - 파일 수정
- [ ] `Test_CommitDiff_FileAddition_StatusAdded` - 파일 추가
- [ ] `Test_CommitDiff_FileDeletion_StatusDeleted` - 파일 삭제
- [ ] `Test_CommitDiff_FileRename_StatusRenamed` - 파일 이름 변경
- [ ] `Test_CommitDiff_BinaryFile_MarksAsBinary` - 바이너리 파일
- [ ] `Test_CommitDiff_LargeFile_TruncatesContent` - 대용량 파일
- [ ] `Test_CommitDiff_EmptyFile_HandlesCorrectly` - 빈 파일

### 4.1A Rename detection, whitespace options ⚠️ 고위험
- [ ] `Test_Diff_RenameDetection_On_ReturnsRenamed`
- [ ] `Test_Diff_RenameDetection_Off_ReturnsDeleteAdd`
- [ ] `Test_Diff_WhitespaceIgnore_OptionChangesPatch`
- [ ] `Test_Diff_Submodule_ReportsSubmoduleDelta`
- [ ] `Test_Diff_HugeBinary_ReturnsNoPatchButSizeAndOID`
- [ ] `Test_Diff_RenameBinaryFile_ReturnsNoPatchButMetadata` ⭐ UI 깨짐 방지

### 4.1B Diff Truncation 계약 (CommitDiff/BranchDiff 공통) ⚠️ 고위험
- [ ] `Test_CommitDiff_LargeDiff_ReturnsTruncatedFlagAndStableCounts`
  - 회귀: "대용량 diff에서도 truncated 플래그와 count가 일관되게 반환된다"
- [ ] `Test_CommitDiff_MaxFilesExceeded_ReturnsPartialAndNextCursor`
- [ ] `Test_BranchDiff_LargeDiff_ReturnsTruncatedFlagAndStableCounts`

### 4.1C Diff Timeout/과부하 계약 ⭐ P0 (500 방지)
- [ ] `Test_CommitDiff_LargeRepo_ExceedsDeadline_Returns503_TIMEOUT_WithRetryAfter` ⭐ P0
  - 회귀: "대용량 diff가 deadline을 넘으면 500이 아니라 retry 가능한 코드로 떨어진다"
- [ ] `Test_CommitDiff_ContextCanceled_Returns499Or408_CANCELED` ⭐ P0
  - 회귀: "클라이언트가 끊거나 취소하면 서버가 OOM/500으로 가지 않고 빠르게 정리한다"
- [ ] `Test_CommitDiff_UpstreamGoGitTimeout_Returns503_DEPENDENCY_TIMEOUT` ⭐ P0
  - 회귀: "내부 연산 타임아웃이 INTERNAL_ERROR로 뭉개지지 않는다"
- [ ] `Test_CommitDiff_TooManyConcurrentRequests_Returns429Or503_WithRetryAfter` ⭐ P0
  - 회귀: "동시 diff 폭주에서 500 대신 백프레셔 응답이 고정된다"
- [ ] `Test_CommitDiff_ResponseSizeLimit_TriggersTruncationNot500`
  - 회귀: "대용량 결과에서 응답 크기 제한을 만나도 계약(truncated/partial)이 유지된다"
- [ ] `Test_CommitDiff_HugePatch_DoesNotOOM_StreamsOrTruncates`
  - 회귀: "patch 생성이 메모리를 잡아먹어 OOM/500을 만들지 않는다"

### 4.1D Diff 관측성/디버깅 ⚠️ 고위험
- [ ] `Test_ErrorResponse_TimeoutOrOverload_IncludesRequestId_ErrorCode_Correlation`
- [ ] `Test_DiffEndpoint_EmitsMetrics_Duration_Size_Truncated_Flag`

### 4.2 Branch Diff
- [ ] `Test_BranchDiff_ValidBranches_ReturnsDiff` - 정상 브랜치 비교
- [ ] `Test_BranchDiff_SameBranch_ReturnsEmpty` - 같은 브랜치
- [ ] `Test_BranchDiff_NonExistentBranch_Returns404` - 존재하지 않는 브랜치
- [ ] `Test_BranchDiff_DeletedBranch_Returns404` - 삭제된 브랜치

### 4.3 File Diff
- [ ] `Test_FileDiff_ValidPath_ReturnsDiff` - 정상 파일 비교
- [ ] `Test_FileDiff_NonExistentPath_Returns404` - 존재하지 않는 경로
- [ ] `Test_FileDiff_MissingPath_Returns400` - path 파라미터 누락
- [ ] `Test_FileDiff_BinaryFile_ReturnsMetadataOnly` - 바이너리 파일

### 4.4 Context Lines
- [ ] `Test_CommitDiff_DefaultContext_Returns3Lines` - 기본 컨텍스트 3줄
- [ ] `Test_CommitDiff_CustomContext_ReturnsRequestedLines` - 커스텀 컨텍스트
- [ ] `Test_CommitDiff_ZeroContext_ReturnsNoContext` - 컨텍스트 0

### 4.5 Patch Generation
- [ ] `Test_Patch_ValidRefs_ReturnsPatch` - 정상 패치 생성
- [ ] `Test_Patch_InvalidBase_Returns400` - 잘못된 base
- [ ] `Test_Patch_Format_MatchesGitFormat` - Git 패치 형식 준수

---

## 5. Overview

### 5.1 Repository Overview
- [ ] `Test_Overview_ValidRepo_ReturnsOverview` - 정상 조회
- [ ] `Test_Overview_NonExistentRepo_Returns404` - 존재하지 않는 레포
- [ ] `Test_Overview_EmptyRepo_ReturnsEmptyData` - 빈 레포
- [ ] `Test_Overview_IncludesDefaultBranch` - 기본 브랜치 정보 포함
- [ ] `Test_Overview_IncludesBranchCount` - 브랜치 수 포함
- [ ] `Test_Overview_IncludesCommitCount` - 커밋 수 포함
- [ ] `Test_Overview_IncludesLanguageDistribution` - 언어 분포 포함

### 5.1A Default Branch 문제 ⚠️ 고위험
- [ ] `Test_Overview_DefaultBranchMissing_Returns404OrFallbackExplicitly`
  - 회귀: "default branch 삭제/변경 상황에서 명확한 동작"
- [ ] `Test_Overview_DefaultBranchIsMasterOrMain_ResolvedByRepoMetadata_NotByString`
  - 회귀: "default branch를 'main'이라고 가정하지 않고 실제 repo 설정으로 결정한다"
- [ ] `Test_BranchOps_DefaultBranchRename_MasterToMain_ConsistencyAcrossAPIs`
  - 회귀: "rename 후에도 Overview/Branches/Commits/Diffs가 같은 default branch를 본다"

### 5.2 Language Distribution
- [ ] `Test_LanguageDistribution_SingleLanguage_Returns100Percent` - 단일 언어
- [ ] `Test_LanguageDistribution_MultipleLanguages_ReturnsSorted` - 다중 언어
- [ ] `Test_LanguageDistribution_OthersCategory_GroupsSmallLanguages` - Others 카테고리
- [ ] `Test_LanguageDistribution_ExcludesVendor_SkipsVendorDir` - vendor 제외
- [ ] `Test_LanguageDistribution_ExcludesNodeModules_SkipsNodeModules` - node_modules 제외

### 5.3 Contributors
- [ ] `Test_Contributors_ReturnsUniqueContributors` - 중복 제거
- [ ] `Test_Contributors_SortsByCommitCount` - 커밋 수 정렬
- [ ] `Test_Contributors_IncludesEmail` - 이메일 포함

---

## 6. Tree

### 6.1 Tree Listing
- [ ] `Test_Tree_RootDir_ReturnsEntries` - 루트 디렉토리
- [ ] `Test_Tree_Subdirectory_ReturnsEntries` - 서브디렉토리
- [ ] `Test_Tree_NonExistentPath_Returns404` - 존재하지 않는 경로
- [ ] `Test_Tree_FilePath_Returns400` - 파일 경로 (디렉토리 아님)
- [ ] `Test_Tree_EmptyDirectory_ReturnsEmptyArray` - 빈 디렉토리
- [ ] `Test_Tree_ByRef_ReturnsEntriesAtRef` - ref 기준 조회
- [ ] `Test_Tree_ByCommitSHA_ReturnsEntriesAtCommit` - SHA 기준 조회

### 6.1A Path Validation ⚠️ 고위험
- [ ] `Test_Tree_PathTraversalSegments_Returns400` ⭐ a/../b 같은 path는 400
- [ ] `Test_Tree_SymlinkPointsOutside_EntryReturnedButContentFetchBlocked`
  - 회귀: "tree에 symlink가 있어도 repo root 밖 콘텐츠를 읽을 수 없다"

### 6.2 Entry Types
- [ ] `Test_Tree_FileEntry_TypeFile` - 파일 타입
- [ ] `Test_Tree_DirectoryEntry_TypeDir` - 디렉토리 타입
- [ ] `Test_Tree_SymlinkEntry_TypeSymlink` - 심볼릭 링크
- [ ] `Test_Tree_SubmoduleEntry_TypeSubmodule` - 서브모듈

### 6.2A Entry Types 확장 ⚠️ 고위험
- [ ] `Test_Tree_SubmoduleEntry_IncludesCommitOIDAndURLIfAvailable`
- [ ] `Test_Tree_SymlinkEntry_ReturnsLinkTargetOrMetadata`
- [ ] `Test_Tree_CaseCollision_BehaviorDeterministic` - mac

### 6.3 Recursive
- [ ] `Test_Tree_Recursive_ReturnsAllEntries` - 재귀적 조회
- [ ] `Test_Tree_Recursive_FlattensPaths` - 경로 평탄화

---

## 7. Blob

### 7.1 File Content
- [ ] `Test_Blob_ValidPath_ReturnsContent` - 정상 조회
- [ ] `Test_Blob_NonExistentPath_Returns404` - 존재하지 않는 경로
- [ ] `Test_Blob_DirectoryPath_Returns400` - 디렉토리 경로
- [ ] `Test_Blob_BinaryFile_ReturnsBase64` - 바이너리 파일
- [ ] `Test_Blob_LargeFile_Truncates` - 대용량 파일 제한
- [ ] `Test_Blob_EmptyFile_ReturnsEmpty` - 빈 파일
- [ ] `Test_Blob_ByRef_ReturnsContentAtRef` - ref 기준 조회
- [ ] `Test_Blob_ByCommitSHA_ReturnsContentAtCommit` - SHA 기준 조회

### 7.1A LFS / Range / Unicode ⚠️ 고위험
- [ ] `Test_Blob_LFSPointer_ReturnsPointerOrResolvesByPolicy`
- [ ] `Test_Blob_RangeRequest_ReturnsPartialContent`
- [ ] `Test_Blob_RangeRequest_InvalidRange_Returns416`
  - 회귀: "range가 잘못되면 200으로 어정쩡하게 반환되지 않는다"
- [ ] `Test_Blob_PathWithUnicodeNormalization_ReturnsSameBlobDeterministically`

### 7.1B Path Validation ⚠️ 고위험
- [ ] `Test_Blob_PathTraversalSegments_Returns400` ⭐ a/../b 같은 path는 400

### 7.2 Encoding
- [ ] `Test_Blob_UTF8_ReturnsCorrectEncoding` - UTF-8 인코딩
- [ ] `Test_Blob_NonUTF8_HandlesCorrectly` - 비UTF-8 인코딩
- [ ] `Test_Blob_MixedEncoding_DetectsCorrectly` - 혼합 인코딩

---

## 8. Tags

### 8.1 Create Tag
- [ ] `Test_CreateTag_Lightweight_ReturnsSuccess` - 경량 태그
- [ ] `Test_CreateTag_Annotated_ReturnsSuccess` - 주석 태그
- [ ] `Test_CreateTag_DuplicateName_Returns409` - 중복 이름
- [ ] `Test_CreateTag_InvalidTarget_Returns404` - 잘못된 target
- [ ] `Test_CreateTag_FromBranch_ReturnsSuccess` - 브랜치에서 생성
- [ ] `Test_CreateTag_FromCommit_ReturnsSuccess` - 커밋에서 생성

### 8.2 Delete Tag
- [ ] `Test_DeleteTag_Existing_ReturnsSuccess` - 정상 삭제
- [ ] `Test_DeleteTag_NonExistent_Returns404` - 존재하지 않는 태그

### 8.3 List Tags
- [ ] `Test_GetTags_ReturnsAll` - 태그 목록
- [ ] `Test_GetTags_EmptyRepo_ReturnsEmptyArray` - 빈 레포
- [ ] `Test_GetTags_SortByName_ReturnsSorted` - 이름순 정렬
- [ ] `Test_GetTags_SortByDate_ReturnsSorted` - 날짜순 정렬

---

## 9. Repositories

### 9.1 Repository Operations
- [ ] `Test_OpenRepository_Valid_ReturnsRepo` - 정상 열기
- [ ] `Test_OpenRepository_NonExistent_Returns404` - 존재하지 않는 레포
- [ ] `Test_OpenRepository_Corrupted_Returns500` - 손상된 레포
- [ ] `Test_OpenRepository_NotBare_HandlesCorrectly` - non-bare 레포

### 9.1A GC/Locking/Permissions ⚠️ 고위험
- [ ] `Test_OpenRepository_GCInProgress_ReadStillWorksOrReturnsRetryable`
- [ ] `Test_OpenRepository_PermissionDenied_Returns403`
- [ ] `Test_OpenRepository_LooseObjectsOnly_StillWorks` - pack 없을 때

### 9.2 Path Resolution
- [ ] `Test_ResolvePath_ValidOwnerRepo_ReturnsPath` - 정상 경로
- [ ] `Test_ResolvePath_PathTraversal_Returns400` - 경로 탐색 공격 방지
- [ ] `Test_ResolvePath_SpecialChars_Escapes` - 특수문자 이스케이프

### 9.2A Symlink Traversal ⚠️ 고위험
- [ ] `Test_ResolvePath_SymlinkTraversal_Blocked` - repo root 밖으로 나가는 symlink

---

## 10. Protocol (SSH / HTTP)

### 10.1 SSH Protocol
- [ ] `Test_SSH_ValidKey_Authenticates` - 정상 인증
- [ ] `Test_SSH_InvalidKey_RejectsAuth` - 잘못된 키
- [ ] `Test_SSH_ExpiredKey_RejectsAuth` - 만료된 키
- [ ] `Test_SSH_RevokedKey_RejectsAuth` - 폐기된 키
- [ ] `Test_SSH_GitUploadPack_ReturnsData` - git-upload-pack
- [ ] `Test_SSH_GitReceivePack_AcceptsData` - git-receive-pack
- [ ] `Test_SSH_LargePayload_Handles` - 대용량 페이로드

### 10.1A SSH Security ⚠️ 고위험
- [ ] `Test_SSH_RejectsCommandInjectionInOriginalCommand`
  - 회귀: "git-upload-pack 'repo.git; rm -rf /' 같은 형태가 차단된다"
- [ ] `Test_SSH_ForceCommand_Enforced` - authorized_keys force-command
- [ ] `Test_SSH_ReportsProperExitStatus_OnAuthFail`

### 10.2 HTTP Protocol (Smart HTTP)
- [ ] `Test_HTTP_InfoRefs_ReturnsRefs` - info/refs
- [ ] `Test_HTTP_UploadPack_ReturnsPackfile` - upload-pack
- [ ] `Test_HTTP_ReceivePack_AcceptsPackfile` - receive-pack

### 10.2A Smart HTTP 확장 ⚠️ 고위험
- [ ] `Test_HTTP_InfoRefs_AdvertisingIncludesServiceHeader`
- [ ] `Test_HTTP_UploadPack_ShallowClone_Works`
- [ ] `Test_HTTP_UploadPack_PartialClone_FilterBlobNone_WorksOr400Explicitly`
- [ ] `Test_HTTP_ReceivePack_RejectsNonFastForward_WhenPolicyEnabled`
- [ ] `Test_HTTP_ReceivePack_RejectsLargePack_Returns413` ⭐ 대용량 pack 제한

### 10.2B Smart HTTP 캐시/프록시 ⚠️ 고위험
- [ ] `Test_HTTP_InfoRefs_SetsNoCacheOrVaryHeaders_Deterministically`
  - 회귀: "프록시/캐시가 ref 광고를 섞어 clone/fetch가 깨지지 않는다"
- [ ] `Test_HTTP_ReceivePack_AuthFail_Returns401WithWWWAuthenticate`
  - 회귀: "git 클라이언트가 이해 가능한 인증 실패 형태로 떨어진다"
- [ ] `Test_HTTP_UploadPack_ClientDisconnect_ReleasesRepoLock`
  - 회귀: "클라이언트가 끊겨도 락/리소스가 남아 연쇄 장애를 만들지 않는다"

---

## 11. Events / Streaming

### 11.1 Redis Stream Events
- [ ] `Test_PublishEvent_ValidEvent_Publishes` - 정상 발행
- [ ] `Test_PublishEvent_RedisDown_ReturnsError` - Redis 다운
- [ ] `Test_PublishEvent_InvalidPayload_Returns400` - 잘못된 페이로드

### 11.1A At-least-once / Ordering / Dedupe ⚠️ 고위험
- [ ] `Test_PublishEvent_DuplicateDelivery_IsIdempotentConsumerSide`
- [ ] `Test_PublishEvent_OrderPreserved_PerRepoPartition`

### 11.2 Git Streaming
- [ ] `Test_GitStream_Clone_StreamsData` - clone 스트리밍
- [ ] `Test_GitStream_Push_AcceptsStream` - push 스트리밍
- [ ] `Test_GitStream_LargeRepo_HandlesChunking` - 대용량 레포 청킹

### 11.2A Backpressure / Disconnect ⚠️ 고위험
- [ ] `Test_GitStream_Backpressure_ClientSlow_DoesNotOOM`
- [ ] `Test_GitStream_ClientDisconnect_StopsWorkAndReleasesResources`

---

## 12. Security / Validation

### 12.1 Path Validation
- [ ] `Test_PathValidation_TraversalAttack_Blocked` - ../ 공격 차단
- [ ] `Test_PathValidation_NullByte_Blocked` - null byte 차단
- [ ] `Test_PathValidation_AbsolutePath_Blocked` - 절대 경로 차단

### 12.1A Git 내부 콘텐츠 기반 공격 ⚠️ 고위험
- [ ] `Test_Blob_PathContainsNewline_Rejected` - 로그/헤더 injection
- [ ] `Test_BranchName_ControlChars_Rejected`
- [ ] `Test_RefName_NewlineOrNull_Rejected`
- [ ] `Test_Tree_EntryNameWithUnicodeBidiCharacters_HandledSafely`
  - 회귀: "Trojan Source 계열 UI/로그 혼란 방지"

### 12.2 Input Validation
- [ ] `Test_InputValidation_XSSPayload_Escaped` - XSS 이스케이프
- [ ] `Test_InputValidation_SQLInjection_Blocked` - SQL 인젝션 차단
- [ ] `Test_InputValidation_LargeInput_Rejected` - 대용량 입력 거부

### 12.3 Rate Limiting
- [ ] `Test_RateLimit_UnderLimit_Allows` - 제한 이하
- [ ] `Test_RateLimit_OverLimit_Returns429` - 제한 초과

---

## 13. Error Handling / HTTP Status Codes

### 13.1 Status Code 정책 (고정됨)

| 상황 | 코드 | error.code | 재시도 | Retry-After |
|------|------|-----------|--------|-------------|
| 리소스 없음 | 404 | `*_NOT_FOUND` | ❌ | - |
| 문법/입력 오류 | 400 | `INVALID_*`, `UNSUPPORTED_*`, `VALIDATION_ERROR` | ❌ | - |
| 권한/정책 위반 | **403** | `FORBIDDEN`, `POLICY_VIOLATION` | ❌ | - |
| 동시성/경합 | **409** | `CONCURRENT_UPDATE`, `ALREADY_EXISTS` | ✅ 가능 | - |
| 파일 충돌 (merge) | **422** | `MERGE_CONFLICT`, `NO_COMMON_ANCESTOR`, `AMBIGUOUS_MERGE_BASE` | ❌ (사용자 해결) | - |
| 락 경합 | **423** | `LOCKED` | ✅ 가능 | ✅ 필수 |
| 과부하/Rate Limit | **429** | `RATE_LIMITED`, `OVERLOADED` | ✅ 가능 | ✅ 필수 |
| Timeout (서버측) | **503** | `TIMEOUT`, `DEPENDENCY_TIMEOUT`, `OVERLOADED` | ✅ 가능 | ✅ 권장 |
| 클라이언트 취소 | **499** (nginx style) 또는 **408** | `CANCELED`, `REQUEST_TIMEOUT` | - | - |
| 진짜 서버 버그 | **500** | `INTERNAL_ERROR` | ✅ 가능 | - |

> **핵심**: 
> - 500 = 진짜 버그 (조사 필요)
> - 503 = timeout/과부하 (재시도 가능, Retry-After로 타이밍 제공)
> - 429 = rate limit (재시도 가능, Retry-After로 타이밍 제공)
> - 409 = 동시성 경합 (재시도 가능)
> - 422 = 사용자 해결 필요 (재시도 무의미)
> - 403 = 정책/권한

### 13.1A Status Code 테스트
- [ ] `Test_AllAPIs_NotFound_Returns404` - 리소스 없음 -> 404
- [ ] `Test_AllAPIs_BadRequest_Returns400` - 잘못된 요청 -> 400
- [ ] `Test_AllAPIs_InternalError_Returns500` - 서버 에러 -> 500
- [ ] `Test_RefNotFound_Returns404_Not409` - ref 없음은 404 (409 아님!)

### 13.1B 409/422/403/423 분기 ⚠️ 고위험
- [ ] `Test_ConcurrentUpdate_Returns409_CONCURRENT_UPDATE` - 동시성 경합
- [ ] `Test_MergeFileConflict_Returns422_MERGE_CONFLICT` - 파일 충돌 (재시도 무의미)
- [ ] `Test_PolicyViolation_Returns403_POLICY_VIOLATION` - 정책 위반
- [ ] `Test_RepoLocked_Returns423_LOCKED` - 락 경합
- [ ] `Test_RepoLocked_423_IncludesRetryAfterHeader`
  - 회귀: "락 경합 시 클라이언트가 재시도 타이밍을 알 수 있다"

### 13.1C 503/429/499 Timeout/과부하 계약 ⭐ P0 (500 방지)
- [ ] `Test_Timeout_Returns503_TIMEOUT_WithRetryAfter` ⭐ P0
  - 회귀: "deadline 초과가 500 INTERNAL_ERROR로 뭉개지지 않는다"
- [ ] `Test_Overload_Returns503_OVERLOADED_WithRetryAfter` ⭐ P0
  - 회귀: "과부하 상태가 명확한 코드로 표현되어 재시도 전략이 가능하다"
- [ ] `Test_RateLimit_Returns429_RATE_LIMITED_WithRetryAfter`
  - 회귀: "rate limit이 503/500과 구분되어 클라이언트가 백오프 가능하다"
- [ ] `Test_DependencyTimeout_Returns503_DEPENDENCY_TIMEOUT`
  - 회귀: "upstream(go-git) 타임아웃이 500으로 변질되지 않는다"
- [ ] `Test_ClientCanceled_Returns499Or408_CANCELED`
  - 회귀: "클라이언트 끊김이 500으로 처리되지 않고 자원이 빠르게 정리된다"

### 13.2 Error Response 형식
- [ ] `Test_ErrorResponse_IncludesErrorField` - error 필드 포함
- [ ] `Test_ErrorResponse_IncludesSuccessFalse` - success: false 포함
- [ ] `Test_ErrorResponse_NoSensitiveInfo` - 민감 정보 노출 안 함
- [ ] `Test_ErrorResponse_RedactsFilesystemPaths_AndInternalRefs`
  - 회귀: "에러에 서버 내부 경로/.git 상태/스택트레이스가 노출되지 않는다"

### 13.2A Field Errors / Request ID ⚠️ 고위험
- [ ] `Test_ValidationError_IncludesFieldErrors` - branchName/path/ref
- [ ] `Test_ExternalServiceError_IncludesRequestId_NoInternals`

### 13.3 Error Contract (Canonical)

> **목적**: 모든 에러 응답의 표준 스키마를 정의하여 클라이언트가 machine-readable하게 처리 가능하게 한다.

**필수 필드**:
```json
{
  "success": false,
  "error": {
    "code": "REF_NOT_FOUND",
    "message": "branch 'ghost' does not exist",
    "request_id": "req-abc123",
    "details": {
      "ref": "ghost",
      "expected": "existing branch or tag"
    }
  }
}
```

**409 Conflict error.code 값** (재시도 가능):
| code | 의미 |
|------|------|
| `CONCURRENT_UPDATE` | target ref가 요청 중 이동 |
| `ALREADY_EXISTS` | branch/tag 중복 생성 |

**422 Unprocessable Entity error.code 값** (재시도 무의미):
| code | 의미 |
|------|------|
| `MERGE_CONFLICT` | merge 시 파일 충돌 |
| `NO_COMMON_ANCESTOR` | orphan branch, merge base 없음 |
| `AMBIGUOUS_MERGE_BASE` | criss-cross, 여러 merge base |

**423 Locked error.code 값**:
| code | 의미 | 추가 헤더 |
|------|------|----------|
| `LOCKED` | repo lock contention | `Retry-After: <seconds>` |

**429 Too Many Requests error.code 값**:
| code | 의미 | 추가 헤더 |
|------|------|----------|
| `RATE_LIMITED` | rate limit 초과 | `Retry-After: <seconds>` |
| `OVERLOADED` | 서버 과부하 | `Retry-After: <seconds>` |

**503 Service Unavailable error.code 값**:
| code | 의미 | 추가 헤더 |
|------|------|----------|
| `TIMEOUT` | handler deadline 초과 | `Retry-After: <seconds>` |
| `DEPENDENCY_TIMEOUT` | upstream(go-git) 연산 timeout | `Retry-After: <seconds>` |
| `OVERLOADED` | 서버 과부하 (429와 선택) | `Retry-After: <seconds>` |

**403 Forbidden error.code 값**:
| code | 의미 |
|------|------|
| `FORBIDDEN` | 권한 없음 |
| `POLICY_VIOLATION` | protected branch, FF-only, signed commits 등 정책 위반 |

**404 Not Found error.code 값**:
| code | 의미 |
|------|------|
| `REPO_NOT_FOUND` | repository 없음 |
| `REF_NOT_FOUND` | branch/tag 없음 |
| `PATH_NOT_FOUND` | 파일/디렉토리 경로 없음 |
| `OBJECT_NOT_FOUND` | commit SHA 없음 |

**400 Bad Request error.code 값**:
| code | 의미 |
|------|------|
| `INVALID_REF` | ref 문법 오류 (빈 문자열, 공백, hex 아님 등) |
| `UNSUPPORTED_REF` | 지원하지 않는 ref 형식 (refs/heads/*, HEAD, main^, main:path 등) |
| `AMBIGUOUS_REF` | branch/tag 이름 충돌 |
| `VALIDATION_ERROR` | 입력값 검증 실패 |

---

## 14. Performance / Load

### 14.1 대용량 처리
- [ ] `Test_LargeRepo_10000Commits_HandlesInTime` - 10000+ 커밋
- [ ] `Test_LargeRepo_1000Branches_HandlesInTime` - 1000+ 브랜치
- [ ] `Test_LargeFile_100MB_HandlesInTime` - 100MB 파일
- [ ] `Test_ManyFiles_10000Files_HandlesInTime` - 10000+ 파일

### 14.2 동시성
- [ ] `Test_ConcurrentReads_NoDeadlock` - 동시 읽기
- [ ] `Test_ConcurrentWrites_ProperLocking` - 동시 쓰기 잠금
- [ ] `Test_ConcurrentMixedOps_NoRaceCondition` - 혼합 동시 작업

---

## 15. 테스트 픽스처 레포 7종 세트

> 도메인별 테스트를 "매번 새 레포"로 만들면 유지보수 지옥. 목적별 레포 7개로 고정.

| 레포 | 용도 | 포함 내용 |
|------|------|-----------|
| **R0_min** | 기본 동작 | linear history, small text files |
| **R1_branches** | 브랜치/ref 테스트 | slash/multi-slash, tag/branch 충돌, sha-like branch (deadbee), annotated/lightweight tag |
| **R2_conflicts** | 충돌 시나리오 | rename/rename, delete/modify, add/add, dir/file conflict, eol change |
| **R3_big** | 성능/페이지네이션 | 10k commits, 1k branches, many files |
| **R4_weirdfs** | 파일시스템 edge | unicode(NFC/NFD), case-collision, symlink, submodule, executable bit |
| **R5_protocol** | 프로토콜 테스트 | LFS pointer, large blobs, shallow/partial clone 시나리오 |
| **R6_storage** | object/refs 저장 포맷 | loose-only, fully-packed, packed-refs on/off, commit-graph on/off, bitmap on/off |

### 15.1 Fixture Generation Strategy

> **목적**: 픽스처 레포를 어떻게 만들고 유지할지 정의한다.

**권장 방식: Hybrid (Golden + Builder)**

| 레포 | 방식 | 이유 |
|------|------|------|
| R0, R1, R2, R4, R5, R6 | **Golden Repo Bundle** | 빠름, 결정적, CI 안정 |
| R3_big | **Builder Script** | 10k commits를 커밋하기엔 용량 큼 |

**Golden Repo Bundle 방식**:
- `testdata/repos/R2_conflicts.git` 같은 bare repo를 커밋
- 테스트는 `t.TempDir()`에 복사해서 사용
- 장점: 빠름, 결정적, CI에서 안정
- 단점: repo가 커질 수 있음 (압축으로 해결)

**Builder Script 방식**:
- Go helper로 "커밋 그래프 생성기"를 만들고 테스트 시작 시 생성
- 장점: repo 크기 작음, 변경 쉬움
- 단점: 생성 시간이 길어질 수 있음 (특히 R3_big)

**Golden Repo 생성 원칙** ⚠️ 중요:
1. `alternates` 사용 금지 (`objects/info/alternates` 없어야 함)
2. `packed-refs` 포함 여부 고정 (유무 통일)
3. 테스트는 Go helper로 복사 (`cp -a` 대신) + 쓰기 권한 정리
4. 모든 테스트는 `t.TempDir()`에 복사 후 실행하여 격리

**실행 원칙**:
1. R3_big은 nightly/perf suite로 분리 가능
2. 픽스처 변경 시 PR에 "Fixture: R2_conflicts updated" 명시

### 15.2 R6_storage 테스트 예시

> "repo 저장 포맷이 달라도 결과가 동일하고 성능만 달라진다"

- [ ] `Test_ListBranches_PackedRefsOnOff_SameResult`
- [ ] `Test_ResolveRef_LooseVsPacked_SameOID`
- [ ] `Test_MergeBase_WithCommitGraphEnabled_SameResult`

---

## 16. 회귀 문장 템플릿

> 각 테스트에 아래 중 하나로 1줄만 붙이면 품질 상승

| 축 | 템플릿 |
|----|--------|
| **Ref/Resolve** | "사용자 입력 ref가 tag/branch/sha로 해석이 갈려도 결과가 비결정적으로 바뀌지 않는다" |
| **Concurrency** | "동시 업데이트에서 repo ref가 손상되거나 부분 적용되지 않는다" |
| **Truncation** | "대용량 diff/blob에서 응답이 잘려도 클라이언트가 깨지지 않도록 계약이 유지된다" |
| **Policy** | "보호 브랜치/FF-only 정책이 활성화돼도 우회되거나 애매한 상태코드가 나오지 않는다" |
| **FS Weirdness** | "대소문자/유니코드/심볼릭링크가 섞여도 path/branch가 충돌하거나 탈출하지 않는다" |
| **Protocol** | "실제 git 클라이언트가 기대하는 handshake를 만족하며, 실패 시 원인이 명확히 표현된다" |
| **Storage Format** | "repo 저장 포맷(pack/loose/packed-refs)이 달라도 결과가 동일하고 성능만 달라진다" |
| **Lineage/Time** | "커밋 타임스탬프가 이상해도 merge-base/정렬이 비결정적으로 바뀌지 않는다" |
| **Error Observability** | "실패 시 원인이 machine-readable(error.code)로 고정되며 민감 정보가 노출되지 않는다" |
| **Timeout/Overload** | "과부하/타임아웃이 500으로 뭉개지지 않고 재시도 가능한 코드(503/429)로 고정된다" |

---

## Priority (우선순위)

### P0 - 즉시 필요 (409 버그 뿌리 제거 + 500 Timeout/과부하 분리)

**구현 순서 (권장)**:
1. `Test_RefNotFound_Returns404_Not409` - RefNotFound 404 고정
2. `Test_MergePreview_NonExistentSHA_Returns404_OBJECT_NOT_FOUND` ⭐ sha 존재 여부
3. `Test_MergePreview_MalformedSHA_Returns400_INVALID_REF` ⭐ sha 문법 오류
4. `Test_MergePreview_RefIsAnnotatedTag_DereferencesToCommit_Deterministic` ⭐ tag dereference
5. `Test_MergePreview_RefLooksLikeSHA_ButIsBranchName_PrefersBranch` ⭐ branch 우선
6. `Test_FindMergeBase_MultipleMergeBases_ReturnsDeterministicByRule` ⭐ criss-cross
7. `Test_MergePreview_ProtectedTargetBranch_Returns403_POLICY_VIOLATION` ⭐ status 고정
8. `Test_Merge_ConcurrentTargetAdvanced_Returns409_CONCURRENT_UPDATE` - 동시성
9. `Test_Merge_TargetDeletedDuringMerge_Returns409_CONCURRENT_UPDATE` ⭐ 경합
10. `Test_CommitDiff_LargeRepo_ExceedsDeadline_Returns503_TIMEOUT_WithRetryAfter` ⭐ 500 방지
11. `Test_CommitDiff_TooManyConcurrentRequests_Returns429Or503_WithRetryAfter` ⭐ 과부하

> P0 순서: 먼저 "404/409 혼선"을 정리하고, 그 다음 "500 timeout/과부하"를 분리

### P1 - 높음 (핵심 기능)
1. Merge 충돌 감지 테스트
2. Branch CRUD 완전성
3. Commit 트랜잭션 원자성
4. 정책 관련 (protected branch, FF-only, signed commits)
5. Preview/Merge 정책 일관성
6. CommitDiff/BranchDiff truncation 계약

### P2 - 중간 (품질)
1. 성능 테스트
2. 보안 테스트
3. 에러 응답 일관성 + Retry-After
4. 파일시스템 edge (unicode, case sensitivity)
5. Storage format 일관성 (R6_storage)
6. master/main 전환 일관성

### P3 - 낮음 (개선)
1. 캐시 테스트
2. 이벤트 스트리밍
3. 메트릭/관측성
4. LFS 지원

---

## Checklist Template

테스트 추가 시 체크:
- [ ] 테스트 이름이 `Test_<Unit>_<Scenario>_<Expect>` 형식인가?
- [ ] `t.Parallel()` 적용했는가?
- [ ] 시간/랜덤이 고정되어 있는가?
- [ ] `requireStatus`로 정답을 강제하는가?
- [ ] 실패 메시지에 body가 포함되는가?
- [ ] 회귀 문장을 주석으로 작성했는가?
- [ ] 적절한 픽스처 레포(R0~R6)를 사용했는가?
- [ ] error.code가 13.3 Error Contract에 정의된 값인가?
- [ ] 409/422/403/423/429/503 분기가 13.1 정책을 따르는가?
- [ ] 423/429/503 응답에 Retry-After 헤더가 포함되는가?
- [ ] 500은 "진짜 버그"로만 사용되는가? (timeout/과부하는 503/429)
