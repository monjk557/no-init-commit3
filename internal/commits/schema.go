package commits

import "time"

// CommitDetailResponse는 단일 커밋의 메타데이터 응답 구조를 나타냅니다.
type CommitDetailResponse struct {
	Success   bool               `json:"success"`
	Sha       string             `json:"sha"`
	Message   string             `json:"message"`
	Author    CommitAuthorDetail `json:"author"`
	Committer CommitAuthorDetail `json:"committer"`
	Parents   []string           `json:"parents"`
	TreeHash  string             `json:"tree_hash"`
}

// CommitAuthorDetail는 커밋 작성자/실행자의 상세 정보입니다.
type CommitAuthorDetail struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Date  string `json:"date"`
}

// ComparisonSummaryRequest 는 GET /commits/compare/summary 의 query
// parameter 를 나타낸다.
//
// typed.RegisterGetWithQuery wrapper 가 form 태그를 reflect 하여 OpenAPI
// parameters (in: query) 로 expand 한다 — orchestrator auto-gen client 가
// source_ref/target_ref 인자를 누락하지 않도록 노출하기 위함 (Issue-2317).
type ComparisonSummaryRequest struct {
	SourceRef string `form:"source_ref" binding:"required"` // Source ref (head; merge FROM)
	TargetRef string `form:"target_ref" binding:"required"` // Target ref (base; merge INTO)
}

// CommitsListRequest 는 GET /commits 의 query parameter 를 나타낸다.
//
// 모든 필드는 optional 이며 default / 범위 검증은 handler 본문에서 수행한다
// — 기존 custom error message ("invalid limit parameter: must be between N and M"
// 등) 를 보존. wrapper 가 form 태그를 reflect 하여 OpenAPI parameters (in: query)
// 로 expand 한다 (Issue-2317 후속).
type CommitsListRequest struct {
	Ref       string `form:"ref"`        // default "HEAD"
	SourceRef string `form:"source_ref"` // pair with target_ref
	TargetRef string `form:"target_ref"` // pair with source_ref
	Limit     string `form:"limit"`
	Skip      string `form:"skip"`
	GroupBy   string `form:"group_by"` // "" | "committer_date"
}

// ComparisonSummaryResponse는 두 커밋 간 비교 요약 응답입니다.
type ComparisonSummaryResponse struct {
	Success          bool   `json:"success"`
	MergeBase        string `json:"merge_base"`
	SourceSha        string `json:"source_sha"`
	TargetSha        string `json:"target_sha"`
	CommitCount      int    `json:"commit_count"`
	ChangedFileCount int    `json:"changed_file_count"`
}

// CommitsRequestParams는 커밋 로그 엔드포인트의 요청 파라미터를 나타냅니다.
type CommitsRequestParams struct {
	Owner string
	Repo  string
	Ref   string
	// Compare mode (optional): if both are set, commits are returned after merge-base up to source_ref
	SourceRef string
	TargetRef string
	Limit     int
	Skip      int
	// GroupBy는 커밋 날짜 그룹핑 기준을 지정합니다.
	// "committer_date": committer date 기준 그룹핑 (rebase 등 반영)
	// 기본값(빈 문자열): author date 기준 그룹핑 (하위호환)
	GroupBy string
}

// CommitsResponse는 커밋 로그 엔드포인트의 응답 구조를 나타냅니다.
// 모든 커밋은 날짜별로 그룹화되어 날짜 기준 내림차순으로 정렬됩니다.
type CommitsResponse struct {
	Success    bool            `json:"success"`
	Message    string          `json:"message"`
	Results    []CommitsByDate `json:"results"` // 날짜별로 그룹화된 커밋 목록 (날짜 기준 내림차순)
	HasNext    bool            `json:"has_next"`
	MergeBase  string          `json:"merge_base,omitempty"`
	Pagination PaginationInfo  `json:"pagination"`
}

// PaginationInfo는 커밋 응답의 페이지네이션 정보를 나타냅니다.
// Python PaginatedResponse와 호환되는 필드들을 포함합니다.
type PaginationInfo struct {
	Count       int     `json:"count"`        // 전체 항목 수
	Next        *string `json:"next"`         // 다음 페이지 URL
	Previous    *string `json:"previous"`     // 이전 페이지 URL
	CurrentPage int     `json:"current_page"` // 현재 페이지 번호
	TotalPages  int     `json:"total_pages"`  // 전체 페이지 수
	PageSize    int     `json:"page_size"`    // 페이지당 항목 수
}

// CommitsErrorResponse는 커밋 엔드포인트의 표준화된 에러 응답을 나타냅니다.
type CommitsErrorResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	Code    string `json:"code,omitempty"`
	Details string `json:"details,omitempty"`
}

// 기본 페이지네이션 값들
const (
	DefaultCommitsLimit = 35
	MaxCommitsLimit     = 100
	MinCommitsLimit     = 1
)

// CommitMetadata는 커밋 정보를 나타냅니다
type CommitMetadata struct {
	SHA     string   `json:"sha"`
	Author  string   `json:"author"`
	Email   string   `json:"email"`
	Date    string   `json:"date"`
	Message string   `json:"message"`
	Parents []string `json:"parents"`
}

// CommitStats는 브랜치 비교를 위한 커밋 수 차이를 나타냅니다
type CommitStats struct {
	Ahead  int `json:"ahead"`  // base에 없는 head의 커밋들
	Behind int `json:"behind"` // head에 없는 base의 커밋들
}

// CommitsByDate는 날짜별로 그룹화된 커밋 목록을 나타냅니다
type CommitsByDate struct {
	Date    string           `json:"date"`    // YYYY-MM-DD 형식의 날짜
	Commits []CommitMetadata `json:"commits"` // 해당 날짜의 커밋 목록
}

// Commit - Git 커밋 정보
type Commit struct {
	Sha          string       `json:"sha"`           // Sha - 커밋 해시
	Author       CommitAuthor `json:"author"`        // Author - 커밋 작성자 정보
	Committer    CommitAuthor `json:"committer"`     // Committer - 커밋 실행자 정보
	Message      string       `json:"message"`       // Message - 커밋 메시지
	ParentHashes []string     `json:"parent_hashes"` // ParentHashes - 부모 커밋들의 해시
	TreeHash     string       `json:"tree_hash"`     // TreeHash - 커밋의 트리 객체 해시
}

// CommitAuthor - 커밋 작성자/실행자 정보
type CommitAuthor struct {
	Name  string    `json:"name"`  // Name - 작성자/실행자 이름
	Email string    `json:"email"` // Email - 작성자/실행자 이메일
	Date  time.Time `json:"date"`  // Date - 커밋 타임스탬프
}

type MultiFileCommitRequest struct {
	Message string `json:"message" binding:"required"`
	Branch  string `json:"branch,omitempty"`
	// create_branch_name이 있으면 source_branch_name(또는 branch/HEAD) 기준으로 새 브랜치를 만든 뒤,
	// 그 브랜치에 커밋을 생성합니다.
	CreateBranchName string               `json:"create_branch_name,omitempty"`
	SourceBranchName string               `json:"source_branch_name,omitempty"`
	Author           *CommitAuthor        `json:"author,omitempty"`
	Committer        *CommitAuthor        `json:"committer,omitempty"`
	Operations       []MultiFileOperation `json:"operations" binding:"required,min=1"`
	Options          *CommitOptions       `json:"options,omitempty"`
	Metadata         map[string]string    `json:"metadata,omitempty"`
}

// MultiFileOperation represents a single file operation in a multi-file commit
type MultiFileOperation struct {
	Path         string    `json:"path" binding:"required"`
	Operation    Operation `json:"operation" binding:"required"` // "create", "update", "delete", "move"
	Content      string    `json:"content,omitempty"`            // required for create/update, optional for move
	Encoding     string    `json:"encoding,omitempty"`           // optional, defaults to utf-8
	Mode         string    `json:"mode,omitempty"`               // optional file mode
	OriginalPath string    `json:"original_path,omitempty"`      // required for move operation
}

// CommitOptions represents additional options for commit operations
type CommitOptions struct {
	ValidateOnly       bool          `json:"validate_only,omitempty"`        // only validate, don't commit
	AllowPartialCommit bool          `json:"allow_partial_commit,omitempty"` // continue with successful operations if some fail
	Timeout            time.Duration `json:"timeout,omitempty"`              // transaction timeout
	DryRun             bool          `json:"dry_run,omitempty"`              // alias for validate_only
}

// Operation은 파일 작업 유형을 나타냅니다
type Operation string

const (
	// OperationCreate - 파일 생성
	OperationCreate Operation = "create"
	// OperationUpdate - 파일 업데이트
	OperationUpdate Operation = "update"
	// OperationDelete - 파일 삭제
	OperationDelete Operation = "delete"
	// OperationMove - 파일 이동/이름 변경
	OperationMove Operation = "move"
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
