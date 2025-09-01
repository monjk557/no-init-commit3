package responses

import "time"

// OperationResult represents the result of a single file operation
type OperationResult struct {
	Path      string `json:"path"`
	Operation string `json:"operation"`
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
}

// MultiFileCommitResponse represents the response for multi-file commit operations
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
