package overview

import (
	"time"
)

// OverviewResponse is the response type for repository overview API.
// latest_commits and language_distribution were dropped — the orchestrator
// sources those from DRF. remotes was dropped — not meaningful for
// server-hosted bare repos.
type RepositoryOverviewResponse struct {
	Success    bool                     `json:"success"`
	Message    string                   `json:"message"`
	IsEmpty    bool                     `json:"is_empty"`
	Repository string                   `json:"repository"`
	Owner      string                   `json:"owner"`
	Name       string                   `json:"name"`
	Path       string                   `json:"path"`
	Tree       []GitTreeEntryWithCommit `json:"tree"`
	Branches   []GitBranch              `json:"branches"`
	Tags       []GitTag                 `json:"tags"`
	Count      RepositoryCount          `json:"count"`
	Readme     string                   `json:"readme"`
}

// RepositoryCount provides counts of different repository types.
type RepositoryCount struct {
	Commits            int  `json:"commits"`
	Branches           int  `json:"branches"`
	Tags               int  `json:"tags"`
	Total              int  `json:"total"`
	CommitsApproximate bool `json:"commits_approximate,omitempty"` // true when commit count was capped
}

type LanguageDistribution struct {
	Languages  []LanguageStats `json:"languages"`
	TotalBytes int64           `json:"total_bytes"`
	TotalFiles int             `json:"total_files"`
}

type LanguageStats struct {
	Language   string  `json:"language"`
	Bytes      int64   `json:"bytes"`
	Percentage float64 `json:"percentage"`
	Files      int     `json:"files"`
}

type GitCommit struct {
	SHA          string    `json:"sha"`
	Message      string    `json:"message"`
	Author       GitAuthor `json:"author"`
	Committer    GitAuthor `json:"committer"`
	ParentHashes []string  `json:"parent_hashes"`
	TreeHash     string    `json:"tree_hash"`
}

type GitAuthor struct {
	Name  string    `json:"name"`
	Email string    `json:"email"`
	Date  time.Time `json:"date"`
}

type GitBranch struct {
	Name      string `json:"name"`
	Hash      string `json:"hash"`
	IsRemote  bool   `json:"is_remote"`
	IsCurrent bool   `json:"is_current"`
}

type GitTag struct {
	Name        string `json:"name"`
	Hash        string `json:"hash"`
	IsAnnotated bool   `json:"is_annotated"`
}

type GitTreeEntry struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// SubmoduleInfo는 submodule (gitlink) 트리 엔트리의 부가 정보입니다.
// pinned commit 은 항상 채워지고, URL/branch 는 .gitmodules 가 존재할 때만
// 채워집니다. ResolvedRepo / IsResolvable 은 Phase 5 에서 채워질 예약 필드로,
// 현재는 항상 빈 문자열 / false 입니다.
type SubmoduleInfo struct {
	Commit       string `json:"commit"`                  // pinned commit SHA (full hex)
	URL          string `json:"url,omitempty"`           // .gitmodules 의 url
	Branch       string `json:"branch,omitempty"`        // .gitmodules 의 branch (optional)
	ResolvedRepo string `json:"resolved_repo,omitempty"` // mirror 매핑 결과 (Phase 5 reserved)
	IsResolvable bool   `json:"is_resolvable"`           // pin commit reachability (Phase 5 reserved)
}

// GitTreeEntryWithCommit는 커밋 정보를 포함한 트리 항목입니다.
type GitTreeEntryWithCommit struct {
	Type         string         `json:"type"` // "tree" | "blob" | "submodule"
	Name         string         `json:"name"`
	Path         string         `json:"path"`
	Mode         *string        `json:"mode,omitempty"`          // 파일 모드 (Unix permissions)
	Hash         *string        `json:"hash,omitempty"`          // Git object hash
	Size         *int64         `json:"size,omitempty"`          // 파일 크기 (바이트, blob인 경우만)
	LatestCommit *GitCommit     `json:"latest_commit,omitempty"` // 최신 커밋 정보
	Submodule    *SubmoduleInfo `json:"submodule,omitempty"`     // type == "submodule" 일 때만
}

// 내부 타입들

type RemoteInfo struct {
	Name      string
	URL       string
	IsDefault bool
}

// TreeOverviewResponse is the response type for tree overview API
type TreeOverviewResponse struct {
	Success      bool                     `json:"success"`
	Repository   string                   `json:"repository"`
	Owner        string                   `json:"owner"`
	Name         string                   `json:"name"`
	Path         string                   `json:"path"`
	Tree         []GitTreeEntryWithCommit `json:"tree"`
	Branches     []GitBranch              `json:"branches"`
	LatestCommit *GitCommit               `json:"latest_commit"`
	Commits      []GitCommit              `json:"commits"`
}

// BlobOverviewResponse is the response type for blob overview API
type BlobOverviewResponse struct {
	Success      bool                     `json:"success"`
	IsBinary     bool                     `json:"is_binary"`
	Content      string                   `json:"content"`
	ContentType  string                   `json:"content_type,omitempty"`
	Lines        int                      `json:"lines"`
	Size         int64                    `json:"size"`
	Tree         []GitTreeEntryWithCommit `json:"tree"`
	Branches     []GitBranch              `json:"branches"`
	LatestCommit *GitCommit               `json:"latest_commit"`
	Commits      []GitCommit              `json:"commits"`
}

// LatestCommitOverviewResponse is the response type for the lightweight
// GET /overview/latest-commit API. It returns a single latest commit for a ref
// (optionally narrowed by path) using `git log -1` — O(1) vs the per-path scan
// of /overview/tree/commits. Used by the UI to render the commit header fast
// without waiting for the full tree×commit overlay.
type LatestCommitOverviewResponse struct {
	Success    bool       `json:"success"`
	Repository string     `json:"repository"`
	Owner      string     `json:"owner"`
	Name       string     `json:"name"`
	Ref        string     `json:"ref"`
	Path       string     `json:"path,omitempty"`
	Commit     *GitCommit `json:"commit"`
}

// TreeWithCommitOverviewResponse is the response type for tree with commit overview API
type TreeWithCommitOverviewResponse struct {
	Success    bool                     `json:"success"`
	Repository string                   `json:"repository"`
	Owner      string                   `json:"owner"`
	Name       string                   `json:"name"`
	Path       string                   `json:"path"`
	Tree       []GitTreeEntryWithCommit `json:"tree"`
	Meta       *LatestCommitsMeta       `json:"meta,omitempty"`

	// 새로 추가: 페이지네이션 메타데이터
	Pagination *PaginationInfo `json:"pagination,omitempty"`
}

type LatestCommitsBatchRequest struct {
	Ref     string   `json:"ref"`
	DirPath string   `json:"dir_path"`
	Paths   []string `json:"paths"`
}

type LatestCommitsMeta struct {
	ResolvedRefHash string `json:"resolved_ref_hash"`
	Incomplete      bool   `json:"incomplete"`
	ScannedCommits  int    `json:"scanned_commits"`
	TimeBudgetMs    int    `json:"time_budget_ms"`
	CutReason       string `json:"cut_reason"`

	// Week 3 개선: 인덱싱 시스템 메타데이터
	IndexUsed      bool `json:"index_used,omitempty"`      // O(1) 인덱싱 사용 여부
	IndexedLookups int  `json:"indexed_lookups,omitempty"` // 인덱스로 조회한 파일 수
	GitFallbacks   int  `json:"git_fallbacks,omitempty"`   // Git 순회로 fallback한 파일 수
}

type LatestCommitsBatchResponse struct {
	Success       bool                  `json:"success"`
	Repository    string                `json:"repository"`
	Owner         string                `json:"owner"`
	Name          string                `json:"name"`
	DirPath       string                `json:"dir_path"`
	CommitsByPath map[string]*GitCommit `json:"commits_by_path"`
	Meta          LatestCommitsMeta     `json:"meta"`
	MissingPaths  []string              `json:"missing_paths,omitempty"`
}

// ============================================================================
// Phase 2 개선: 스마트 페이지네이션 구조체
// ============================================================================

// PaginationInfo는 페이지네이션 메타데이터를 제공합니다.
type PaginationInfo struct {
	Page        int    `json:"page"`                    // 현재 페이지 (1부터 시작)
	PerPage     int    `json:"per_page"`                // 페이지당 항목 수
	TotalFiles  int    `json:"total_files"`             // 전체 파일 수
	TotalPages  int    `json:"total_pages"`             // 전체 페이지 수
	HasNextPage bool   `json:"has_next_page"`           // 다음 페이지 존재 여부
	HasPrevPage bool   `json:"has_prev_page"`           // 이전 페이지 존재 여부
	NextPageURL string `json:"next_page_url,omitempty"` // 다음 페이지 URL (있는 경우)
	PrevPageURL string `json:"prev_page_url,omitempty"` // 이전 페이지 URL (있는 경우)
}

// TreeDirectoryRequest는 디렉토리 조회 요청 파라미터입니다.
type TreeDirectoryRequest struct {
	// 기본 파라미터
	Owner string `json:"owner"` // 저장소 소유자
	Repo  string `json:"repo"`  // 저장소 이름
	Ref   string `json:"ref"`   // 브랜치/태그/커밋 참조
	Path  string `json:"path"`  // 디렉토리 경로

	// 페이지네이션 파라미터
	Page    int `json:"page,omitempty"`     // 페이지 번호 (기본값: 1)
	PerPage int `json:"per_page,omitempty"` // 페이지당 항목 수 (0이면 자동 결정)

	// 성능 관련 파라미터
	TimeBudgetMs int `json:"time_budget_ms,omitempty"` // 최대 허용 시간 (밀리초)
}
