# Skip 테스트 사유 정리

> `test_cases.md`에 체크된 테스트 중 `t.Skip()`으로 실행을 건너뛰는 항목과 그 사유를 정리한 문서.  
> 기능/미들웨어 구현 후 해당 테스트의 skip을 제거하고 활성화할 수 있다.

---

## 1. API/파라미터 미구현

| 테스트 | 파일 | Skip 사유 |
|--------|------|-----------|
| `Test_GetBranches_SortByName_ReturnsSorted` | internal/branches/handler_test.go | sort by name 파라미터 미구현 |
| `Test_GetBranches_SortByDate_ReturnsSorted` | internal/branches/handler_test.go | sort by date 파라미터 미구현 |
| `Test_GetBranches_FilterByPattern_ReturnsMatching` | internal/branches/handler_test.go | filter by pattern 파라미터 미구현 |
| `Test_Diff_RenameDetection_On_ReturnsRenamed` | internal/diffs/handler_test.go | 옵션 파라미터 미구현 |
| `Test_Diff_RenameDetection_Off_ReturnsDeleteAdd` | internal/diffs/handler_test.go | 옵션 파라미터 미구현 |
| `Test_Diff_WhitespaceIgnore_OptionChangesPatch` | internal/diffs/handler_test.go | whitespace ignore 옵션 미구현 |
| `Test_CommitsAPI_ByPath_ReturnsCommitsAffectingPath` | internal/commits/handler_test.go | 경로 기준 조회 API 지원 확인 필요 |
| `Test_GetCommit_ValidSHA_ReturnsCommit` | internal/commits/handler_test.go | GetCommit 핸들러 확인 필요 |
| `Test_GetCommit_ShortSHA_ReturnsCommit` | internal/commits/handler_test.go | GetCommit 핸들러 확인 필요 |
| `Test_GetCommit_InvalidSHA_Returns404` | internal/commits/handler_test.go | GetCommit 핸들러 확인 필요 |
| `Test_GetCommit_WithFiles_ReturnsFilesChanged` | internal/commits/handler_test.go | GetCommit 핸들러 확인 필요 |

---

## 2. 정책/보호/락 시스템 미구현

| 테스트 | 파일 | Skip 사유 |
|--------|------|-----------|
| `Test_DeleteBranch_ProtectedBranch_Returns403_POLICY_VIOLATION` | internal/branches/handler_test.go | 브랜치 보호 정책 시스템 미구현 |
| `Test_DeleteBranch_NotMerged_RequiresForce` | internal/branches/handler_test.go | not-merged 삭제 정책 미구현 |
| `Test_Merge_RepoLockContention_Returns423_LOCKED` | internal/merge/status_codes_test.go | Repository lock system not yet implemented |
| `Test_Merge_RejectsWhenBranchProtectionBlocksPush_Returns403_POLICY_VIOLATION` | internal/merge/status_codes_test.go | Branch protection policy system not yet implemented |
| `Test_Merge_NoFastForwardPolicy_RejectsMergeCommit_Returns403` | internal/merge/status_codes_test.go | Fast-forward only policy system not yet implemented |
| `Test_PolicyViolation_Returns403_POLICY_VIOLATION` | internal/merge/status_codes_section13_test.go | Branch protection policy system not yet implemented |
| `Test_RepoLocked_Returns423_LOCKED` | internal/merge/status_codes_section13_test.go | Repository lock system not yet implemented |
| `Test_RepoLocked_423_IncludesRetryAfterHeader` | internal/merge/status_codes_section13_test.go | Repository lock system not yet implemented |
| `Test_MergePreview_ProtectedTargetBranch_Returns403_POLICY_VIOLATION` | internal/merge/merge_preview_ref_resolution_test.go | Protected branch policy system not yet implemented |
| `Test_MergePreview_FastForwardOnlyPolicy_BlocksNonFF_Returns403` | internal/merge/merge_preview_ref_resolution_test.go | Fast-forward only policy system not yet implemented |
| `Test_MergePreview_RequiredStatusChecksNotMet_Returns403_POLICY_VIOLATION` | internal/merge/merge_preview_ref_resolution_test.go | Required status checks system not yet implemented |
| `Test_MergePreview_InsufficientPermission_Returns403_FORBIDDEN` | internal/merge/merge_preview_ref_resolution_test.go | Permission system not yet implemented |
| `Test_MergePreview_SignedCommitsRequired_UnsignedCommit_Returns403_POLICY_VIOLATION` | internal/merge/merge_preview_ref_resolution_test.go | Signed commit policy system not yet implemented |

---

## 3. Case-insensitive FS / Unicode

| 테스트 | 파일 | Skip 사유 |
|--------|------|-----------|
| `Test_CreateBranch_UpperLowerCollision_CaseInsensitiveFS_Returns409` | internal/branches/handler_test.go | case-insensitive FS 처리 로직 미구현 |
| `Test_CreateBranch_NameUnicodeNormalization_CollisionsHandled` | internal/branches/handler_test.go | unicode 정규화 충돌 처리 미구현 |
| `Test_UpdateBranch_RenameToDifferentCase_CaseInsensitiveFS_BehavesDeterministically` | internal/branches/handler_test.go | case-insensitive FS 처리 로직 미구현 |
| `Test_UpdateBranch_RenameCaseOnly_WhenOtherCaseExists_Returns409` | internal/branches/handler_test.go | case-insensitive FS 처리 로직 미구현 |
| `Test_MergePreview_PathRenameCaseOnly_CaseInsensitiveFS_Deterministic` | internal/merge/merge_preview_ref_resolution_test.go | Case-insensitive filesystem handling not yet implemented |

---

## 4. 동시성 / 경합 검증 (나중에 구현)

| 테스트 | 파일 | Skip 사유 |
|--------|------|-----------|
| `Test_DeleteBranch_ConcurrentDelete_SecondReturns404` | internal/branches/handler_test.go | 동시 삭제 검증 테스트 (나중에 구현) |
| `Test_UpdateBranch_Rename_ConcurrentCreateSameName_Returns409_ALREADY_EXISTS` | internal/branches/handler_test.go | 동시 rename/create 검증 테스트 (나중에 구현) |
| `Test_UpdateBranch_RenameWhileClientReads_ListDeterministic` | internal/branches/handler_test.go | rename 중 list 검증 테스트 (나중에 구현) |
| `Test_MultiFileCommit_ConcurrentSameBranch_NoConflict` | internal/commits/handler_test.go | 동시성 테스트, 나중에 |
| `Test_MultiFileCommit_ConcurrentSameFile_Returns409` | internal/commits/handler_test.go | 동시성 테스트, 나중에 |
| `Test_MultiFileCommit_ConcurrentSameBranch_DifferentFiles_OneFastForwardOne409` | internal/commits/handler_test.go | 동시성 테스트, 나중에 |

---

## 5. 멱등성 / 재시도

| 테스트 | 파일 | Skip 사유 |
|--------|------|-----------|
| `Test_MultiFileCommit_IdempotencyKey_RetryDoesNotDuplicateCommit` | internal/commits/handler_test.go | idempotency key 미구현 |
| `Test_MultiFileCommit_ClientRetryAfterTimeout_DoesNotCreateTwoCommits` | internal/commits/handler_test.go | 복잡한 테스트, 나중에 |
| `Test_MultiFileCommit_IdempotencyKey_ServerCrashAfterWrite_NoDuplicateOnRetry` | internal/commits/handler_test.go | idempotency key 미구현 |

---

## 6. Symlink / Submodule

| 테스트 | 파일 | Skip 사유 |
|--------|------|-----------|
| `Test_MultiFileCommit_SymlinkCreation_HandledOrRejected` | internal/commits/handler_test.go | symlink 지원 확인 필요 |
| `Test_MergePreview_SubmoduleChange_IsReportedAsSubmodule` | internal/merge/merge_preview_ref_resolution_test.go | Submodule support not yet implemented |
| `Test_Diff_Submodule_ReportsSubmoduleDelta` | internal/diffs/handler_test.go | submodule diff 지원 확인 필요 |
| `Test_Tree_SymlinkPointsOutside_...` (symlink 생성 실패 시) | internal/tree/handler_test.go | symlink not supported on this platform |

---

## 7. Diff 옵션 / Truncation / Timeout / 관측성

| 테스트 | 파일 | Skip 사유 |
|--------|------|-----------|
| `Test_Diff_HugeBinary_ReturnsNoPatchButSizeAndOID` | internal/diffs/handler_test.go | OID/size 메타데이터 계약 필요 |
| `Test_Diff_RenameBinaryFile_ReturnsNoPatchButMetadata` | internal/diffs/handler_test.go | 계약/지원 확인 필요 |
| `Test_CommitDiff_LargeDiff_ReturnsTruncatedFlagAndStableCounts` | internal/diffs/handler_test.go | truncated 플래그/stable counts 계약 확인 필요 |
| `Test_CommitDiff_MaxFilesExceeded_ReturnsPartialAndNextCursor` | internal/diffs/handler_test.go | pagination/cursor 시스템 미구현 |
| `Test_BranchDiff_LargeDiff_ReturnsTruncatedFlagAndStableCounts` | internal/diffs/handler_test.go | truncated 플래그 미구현 |
| `Test_CommitDiff_LargeRepo_ExceedsDeadline_Returns503_TIMEOUT_WithRetryAfter` | internal/diffs/handler_test.go | deadline/timeout 미들웨어 미구현 |
| `Test_CommitDiff_ContextCanceled_Returns499Or408_CANCELED` | internal/diffs/handler_test.go | context canceled 응답 코드 미구현 |
| `Test_CommitDiff_UpstreamGoGitTimeout_Returns503_DEPENDENCY_TIMEOUT` | internal/diffs/handler_test.go | dependency timeout 미구현 |
| `Test_CommitDiff_TooManyConcurrentRequests_Returns429Or503_WithRetryAfter` | internal/diffs/handler_test.go | 과부하/429 미구현 |
| `Test_CommitDiff_ResponseSizeLimit_TriggersTruncationNot500` | internal/diffs/handler_test.go | 응답 크기 제한 미구현 |
| `Test_CommitDiff_HugePatch_DoesNotOOM_StreamsOrTruncates` | internal/diffs/handler_test.go | 스트리밍/truncation 미구현 |
| `Test_ErrorResponse_TimeoutOrOverload_IncludesRequestId_ErrorCode_Correlation` | internal/diffs/handler_test.go | request id/관측성 미구현 |
| `Test_DiffEndpoint_EmitsMetrics_Duration_Size_Truncated_Flag` | internal/diffs/handler_test.go | 메트릭 미구현 |
| `Test_MergePreview_WhitespaceOnlyChange_DoesNotCreateFalseConflict_WhenIgnoreWhitespaceEnabled` | internal/merge/merge_preview_ref_resolution_test.go | Whitespace ignore option not yet implemented |
| `Test_MergePreview_MaxFilesExceeded_ReturnsPartialAndNextCursor` | internal/merge/merge_preview_ref_resolution_test.go | Pagination/cursor system not yet implemented |
| `Test_MergePreview_DiffPagination_StableOrderAcrossPages` | internal/merge/merge_preview_ref_resolution_test.go | Pagination system not yet implemented |
| `Test_MergePreview_DiffPagination_ResumeWithCursor_NoOverlapNoGap` | internal/merge/merge_preview_ref_resolution_test.go | Pagination/cursor system not yet implemented |

---

## 8. Error handling / API 계약 (미들웨어·핸들러 미구현)

| 테스트 | 파일 | Skip 사유 |
|--------|------|-----------|
| `Test_AllAPIs_InternalError_Returns500` | internal/api/error_handling_test.go | 500을 유발하는 통합 경로 없음; 계약 검증은 미들웨어/핸들러 추가 시 활성화 |
| `Test_ExternalServiceError_IncludesRequestId_NoInternals` | internal/api/error_handling_test.go | request_id는 RequestMetadataMiddleware 사용 시 검증 |
| `Test_Timeout_Returns503_TIMEOUT_WithRetryAfter` | internal/api/error_handling_test.go | timeout 미들웨어/핸들러 구현 후 활성화 |
| `Test_Overload_Returns503_OVERLOADED_WithRetryAfter` | internal/api/error_handling_test.go | overload 미들웨어 구현 후 활성화 |
| `Test_RateLimit_Returns429_RATE_LIMITED_WithRetryAfter` | internal/api/error_handling_test.go | 429/Retry-After는 internal/security/rate_limiter_test.go에서 검증 |
| `Test_DependencyTimeout_Returns503_DEPENDENCY_TIMEOUT` | internal/api/error_handling_test.go | dependency timeout 핸들링 구현 후 활성화 |
| `Test_ClientCanceled_Returns499Or408_CANCELED` | internal/api/error_handling_test.go | client cancel 핸들링 구현 후 활성화 |

---

## 9. Short 모드 / 성능·대용량 (의도적 skip)

| 테스트 | 파일 | Skip 사유 |
|--------|------|-----------|
| `Test_LargeRepo_10000Commits_HandlesInTime` | internal/api/performance_load_test.go | `-short` 시 skip: 대용량 커밋 테스트 |
| `Test_LargeRepo_1000Branches_HandlesInTime` | internal/api/performance_load_test.go | `-short` 시 skip: 대용량 브랜치 테스트 |
| `Test_LargeFile_100MB_HandlesInTime` | internal/api/performance_load_test.go | `-short` 시 skip: 100MB 파일 테스트 |
| `Test_ManyFiles_10000Files_HandlesInTime` | internal/api/performance_load_test.go | `-short` 시 skip: 다수 파일 테스트 |
| `TestOverviewAPI_Performance_LargeRepository` | internal/overview/handler_perf_test.go | `-short` 시 skip: 성능 테스트 |
| `TestOverviewAPI_Performance_CommitCounting` | internal/overview/handler_perf_test.go | `-short` 시 skip: 성능 테스트 |

---

## 10. 환경/외부 의존성

| 테스트 | 파일 | Skip 사유 |
|--------|------|-----------|
| `Test_MergeBase_WithCommitGraphEnabled_SameResult` | internal/merge/r6_storage_test.go | git binary 없거나 `git commit-graph write` 실패 시 skip |
| `Test_BranchOps_DefaultBranchRename_MasterToMain_ConsistencyAcrossAPIs` | internal/overview/handler_test.go | cross-API default branch 일관성은 통합 테스트 범위 |
| `Test_OpenRepository_PermissionDenied_ReturnsError` (root일 때) | internal/repositories/manager_test.go | root 사용자면 skip (권한 테스트 무의미) |
| `Test_Diff_...` (파일 과대 시) | internal/diffs/service_test.go | File too large - timeout prevention working correctly |
| `TestDuplexTCPServerStartStop` 등 | internal/server/test/tcp_git_server_test.go, tests/unit/api/tcp_git_server_test.go | external permission service 등 외부 의존성 필요 |

---

## 요약: Skip 사유별 개수

| 분류 | 개수 | 비고 |
|------|------|------|
| API/파라미터 미구현 | 11 | sort, filter, GetCommit, diff 옵션 등 |
| 정책/보호/락 | 13 | branch protection, repo lock, FF-only, signed commit 등 |
| Case-insensitive / Unicode | 5 | mac FS, NFC/NFD |
| 동시성 검증 | 6 | 나중에 구현 |
| 멱등성/재시도 | 3 | idempotency key |
| Symlink/Submodule | 4 | 지원 여부 확인 |
| Diff truncation/timeout/관측성 | 16 | timeout 미들웨어, 메트릭, pagination 등 |
| Error handling/계약 | 7 | 500/503/429/499 등 미들웨어 |
| Short 모드(성능) | 6 | `go test -short` 시 의도적 skip |
| 환경/외부 의존성 | 5+ | git binary, root, 통합 범위 등 |

---

*문서 갱신: 테스트 추가/삭제 또는 skip 사유 변경 시 이 파일을 함께 수정한다.*