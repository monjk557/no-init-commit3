package overview

import (
	"time"

	"github.com/collabops/git-executor/internal/repositories"
)

// RepositoryOverviewRequest 는 GET /overview 의 query parameter 를 나타낸다 —
// ref / path 를 in: query 로 노출 (Issue-5773).
type RepositoryOverviewRequest struct {
	Ref  string `form:"ref"`
	Path string `form:"path"`
}

// TreeWithCommitOverviewRequest 는 GET /overview/tree/commits 의 query
// parameter 를 나타낸다.
//
// typed.RegisterGetWithQuery wrapper 가 form 태그를 reflect 하여 OpenAPI
// parameters (in: query) 로 expand 한다 — orchestrator auto-gen client 가
// ref / path 인자를 누락하지 않도록 노출 (Issue-2317 후속).
type TreeWithCommitOverviewRequest struct {
	Ref  string `form:"ref"`
	Path string `form:"path"`
}

// LatestCommitOverviewRequest 는 GET /overview/latest-commit 의 query
// parameter 를 나타낸다 — ref + path 를 in: query 로 노출.
type LatestCommitOverviewRequest struct {
	Ref  string `form:"ref"`
	Path string `form:"path"`
}

// BlobOverviewRequest 는 GET /overview/blob 의 query parameter 를
// 나타낸다 — ref / path / include_all_commits 를 in: query 로 노출
// (Issue-5761, Issue-2317 후속 누락분). path 는 handler 본문에서 필수
// 검증(빈 문자열이면 400)하므로 여기서는 binding:"required" 를 걸지
// 않는다 — 걸면 gin 기본 validator 에러 메시지가 기존 커스텀 메시지
// ("Missing required parameter: path") 를 대체해 runtime 응답이 바뀐다.
type BlobOverviewRequest struct {
	Ref               string `form:"ref"`
	Path              string `form:"path"`
	IncludeAllCommits string `form:"include_all_commits"`
}

// OverviewResponse is the response type for repository overview API.
// latest_commits and language_distribution were dropped — the orchestrator
// sources those from DRF. remotes was dropped — not meaningful for
// server-hosted bare repos.
type RepositoryOverviewResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	IsEmpty bool   `json:"is_empty"`
	// DefaultBranch 는 HEAD 가 가리키는 브랜치다 (bare 저장소의 git 쪽 진실).
	// 메타데이터 DB 의 default_branch 는 push·설정변경 뒤 비동기로 따라오므로,
	// 읽기 경로가 그 값을 쓰면 아직 갱신 전인 브랜치를 조회하게 된다.
	// HEAD 가 unborn(=커밋 없는 저장소)이어도 가리키는 이름은 그대로 싣는다 —
	// "이 브랜치로 첫 push 하라" 안내가 그 값을 쓴다.
	DefaultBranch string                   `json:"default_branch"`
	Repository    string                   `json:"repository"`
	Owner         string                   `json:"owner"`
	Name          string                   `json:"name"`
	Path          string                   `json:"path"`
	Tree          []GitTreeEntryWithCommit `json:"tree"`
	Branches      []GitBranch              `json:"branches"`
	Tags          []GitTag                 `json:"tags"`
	Count         RepositoryCount          `json:"count"`
	Readme        string                   `json:"readme"`
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
// 채워집니다. mirror 매핑 / reachability 해석 (resolved_repo / is_resolvable) 은
// 단일 인스턴스 git 연산만 담당하는 git-executor 의 책임이 아니므로 여기서
// 노출하지 않는다. cross-repo 지식을 가진 orchestrator 가 최종 응답에서 채운다.
type SubmoduleInfo struct {
	Commit string                      `json:"commit"`           // pinned commit SHA (full hex)
	URL    string                      `json:"url,omitempty"`    // .gitmodules 의 url
	Branch string                      `json:"branch,omitempty"` // .gitmodules 의 branch (optional)
	Parsed *repositories.ParsedRepoURL `json:"parsed,omitempty"` // url 파싱 성공 시 (host/owner/repo)
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

// LFSPointerInfo는 Git LFS pointer 파일에서 추출한 메타데이터입니다.
// /overview/blob 응답에서 LFS pointer 일 때만 채워집니다 (그 외에는 nil).
type LFSPointerInfo struct {
	Oid  string `json:"oid"`  // sha256 hash (64 hex chars)
	Size int64  `json:"size"` // pointed object 의 byte size
}

// BlobOverviewResponse is the response type for blob overview API.
//
// IsLFSPointer 가 true 면 Content / Lines 는 비어 있고 LFS 메타데이터는 LFS
// 필드에서 읽는다. 이는 client (orchestrator/web) 가 raw blob 의 LFS JSON
// 분기에 의존하지 않고 overview 에서 LFS 정보를 얻도록 하기 위한 책임 이전.
type BlobOverviewResponse struct {
	Success      bool                     `json:"success"`
	IsBinary     bool                     `json:"is_binary"`
	IsLFSPointer bool                     `json:"is_lfs_pointer"`
	LFS          *LFSPointerInfo          `json:"lfs,omitempty"`
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

	// CommitCount는 Ref에서 Path를 건드린 커밋 수입니다(Path가 비면 ref 전체),
	// 즉 `git rev-list --count <ref> -- <path>`. best-effort로 계산되며 타임아웃/에러로
	// 산정 실패 시 nil입니다 — 소비측(web)이 "unknown"(History 라벨 폴백)과 실제 숫자를
	// 구분할 수 있도록 pointer를 사용합니다.
	CommitCount *int `json:"commit_count,omitempty"`
	// CommitCountApproximate는 CommitCount가 overviewCommitCountCap에서 잘렸을 때 true.
	CommitCountApproximate bool `json:"commit_count_approximate,omitempty"`
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
