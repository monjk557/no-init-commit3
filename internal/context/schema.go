// bare repo에서 직접 컨텍스트 정보(diff 파일 목록 + 워크플로우)를 추출하는 API
package context

// ContextRequest: bare repo 컨텍스트 조회 요청
type ContextRequest struct {
	OldRev string `json:"old_rev" binding:"required"` // diff 기준 커밋 (before), null SHA 허용
	NewRev string `json:"new_rev" binding:"required"` // diff 대상 커밋 (after)
}

// ContextResponse: diff 파일 목록 및 워크플로우 내용
type ContextResponse struct {
	Success   bool              `json:"success"`
	DiffFiles []string          `json:"diff_files"` // 변경된 파일 경로 목록
	Workflows map[string]string `json:"workflows"`  // .collabops/workflows/*.yaml 파일명→내용
	// DeletedWorkflows: oldRev 에는 있었으나 newRev 에서 사라진 워크플로우 파일명 목록
	// (Workflows 키와 같은 basename 컨벤션). YAML 파일 자체가 삭제된 경우 downstream
	// (piper) 이 해당 schedule / workflow_dispatch 메타데이터를 soft delete 하도록 신호.
	DeletedWorkflows []string `json:"deleted_workflows"`
}
