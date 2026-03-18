// bare repo에서 직접 컨텍스트 정보(diff 파일 목록 + 워크플로우)를 추출하는 API
package context

// ContextRequest: bare repo 컨텍스트 조회 요청
type ContextRequest struct {
	OldRev string `json:"old_rev" binding:"required"` // diff 기준 커밋 (before), null SHA 허용
	NewRev string `json:"new_rev" binding:"required"` // diff 대상 커밋 (after)
}

// ContextResponse: diff 파일 목록 및 워크플로우 내용
type ContextResponse struct {
	Success    bool              `json:"success"`
	DiffFiles  []string          `json:"diff_files"`  // 변경된 파일 경로 목록
	Workflows  map[string]string `json:"workflows"`   // .collabops/workflows/*.yaml 파일명→내용
	CommitSHAs []string          `json:"commit_shas"` // old_rev..new_rev 범위의 커밋 SHA 목록
}
