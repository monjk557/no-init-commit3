// Package responses는 Git 커밋 관련 HTTP 응답의 JSON 스키마를 정의합니다.
// 다중 파일 커밋 작업의 결과와 개별 작업 결과를 반환하는 데이터 구조체들을 포함합니다.
package responses

import "time"

type Operation string

const (
	OperationCreate Operation = "create"
	OperationUpdate Operation = "update"
	OperationDelete Operation = "delete"
	OperationMove   Operation = "move"
)

// OperationResult는 개별 파일 작업의 결과를 나타냅니다.
// 각 파일에 대한 작업 상태와 오류 메시지를 포함합니다.
type OperationResult struct {
	Path      string    `json:"path"`
	Operation Operation `json:"operation"`
	Success   bool      `json:"success"`
	Error     string    `json:"error,omitempty"`
}

// MultiFileCommitResponse는 다중 파일 커밋 작업에 대한 응답을 나타냅니다.
// 트랜잭션 ID, 전체 작업 현황, 개별 작업 결과 및 실행 시간을 포함합니다.
type MultiFileCommitResponse struct {
	Success          bool                   `json:"success"`
	CommitSHA        string                 `json:"commit_sha,omitempty"`
	TransactionID    string                 `json:"transaction_id"`
	Message          string                 `json:"message"`
	TotalOperations  int                    `json:"total_operations"`
	SuccessfulOps    int                    `json:"successful_operations"`
	FailedOps        int                    `json:"failed_operations"`
	OperationResults []OperationResult      `json:"operation_results,omitempty"`
	ValidationOnly   bool                   `json:"validation_only,omitempty"`
	Duration         time.Duration          `json:"duration_ms"` // in milliseconds
	Details          map[string]interface{} `json:"details,omitempty"`
}

// OperationResultResponse는 작업 결과 응답을 나타냅니다.
// OperationResult와 동일한 구조를 가지며 호환성을 위해 사용됩니다.
type OperationResultResponse struct {
	Path      string `json:"path"`
	Operation string `json:"operation"`
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
}
