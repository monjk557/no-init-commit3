package maintenance

// schema.go defines the named request/response structs for the maintenance
// HTTP domain. Per CLAUDE.md's typed handler architecture these must match the
// runtime response exactly — no ad-hoc gin.H responses.

// MaintainRepoResponse is the report returned by POST
// /repos/{owner}/{repo}/maintenance. Success is the aggregate of every task; a
// single task failure flips it to false while still returning 200 with the
// per-task breakdown, so the caller (import-worker / api-service) gets a full
// report rather than an opaque error.
type MaintainRepoResponse struct {
	Success    bool                 `json:"success"`
	Owner      string               `json:"owner"`
	Repo       string               `json:"repo"`
	DurationMs int64                `json:"duration_ms"`
	Tasks      []MaintainTaskResult `json:"tasks"`
}

// MaintainTaskResult is the outcome of one maintenance task within a run.
type MaintainTaskResult struct {
	Task       string `json:"task"`
	Success    bool   `json:"success"`
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}
