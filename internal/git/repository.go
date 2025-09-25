// Package git - go-git를 사용한 Git 리포지토리 작업 처리
// 이 패키지는 Git 리포지토리에 대한 모든 작업을 추상화하고 캐싱을 제공함
package git

import (
	"context"
	"time"
)

// Repository - Git 리포지토리 작업을 위한 인터페이스
type Repository interface {
	// Path - 리포지토리의 파일시스템 경로 반환
	Path() string

	// GetTree - 특정 참조(ref)에서 디렉토리 구조 조회
	GetTree(ctx context.Context, ref string) ([]TreeEntry, error)

	// GetTreeWithCommits - 특정 참조(ref)에서 디렉토리 구조와 각 파일의 최신 커밋 정보 조회
	GetTreeWithCommits(ctx context.Context, ref string) ([]TreeEntry, error)

	// GetBlob - 특정 경로와 참조에서 파일 내용 조회
	GetBlob(ctx context.Context, path, ref string) ([]byte, error)

	// GetCommits - 페이지네이션을 사용한 커밋 히스토리 조회
	GetCommits(ctx context.Context, ref string, limit, skip int) ([]Commit, error)

	// GetBranches - 모든 브랜치 목록 조회
	GetBranches(ctx context.Context) ([]Branch, error)

	// GetTags - 모든 태그 목록 조회
	GetTags(ctx context.Context) ([]Tag, error)

	// GetDescription - 저장소 설명 조회
	GetDescription(ctx context.Context) (string, error)

	// GetRemotes - 모든 원격 저장소 목록 조회
	GetRemotes(ctx context.Context) ([]Remote, error)

	// Close - 리포지토리가 보유한 리소스 해제
	Close() error
}

// TreeEntry - Git 트리의 항목 (디렉토리 리스팅)
type TreeEntry struct {
	// Path - 트리 내에서의 상대 경로
	Path string `json:"path"`

	// Mode - 파일 모드 (권한 및 타입)
	Mode string `json:"mode"`

	// Type - 항목 타입 (blob: 파일, tree: 디렉토리, commit: 서브모듈)
	Type EntryType `json:"type"`

	// Hash - Git 객체 해시
	Hash string `json:"hash"`

	// Size - 파일 크기 (바이트 단위, blob일 때만)
	Size int64 `json:"size"`

	// LatestCommit - 이 파일의 최신 커밋 정보 (blob일 때만)
	LatestCommit *Commit `json:"latest_commit,omitempty"`
}

// EntryType - Git 트리 항목의 타입
type EntryType string

const (
	// EntryTypeBlob - 파일
	EntryTypeBlob EntryType = "blob"

	// EntryTypeTree - 디렉토리
	EntryTypeTree EntryType = "tree"

	// EntryTypeCommit - 서브모듈 커밋
	EntryTypeCommit EntryType = "commit"
)

// Commit - Git 커밋 정보
type Commit struct {
	// Sha - 커밋 해시
	Sha string `json:"sha"`

	// Author - 커밋 작성자 정보
	Author CommitAuthor `json:"author"`

	// Committer - 커밋 실행자 정보
	Committer CommitAuthor `json:"committer"`

	// Message - 커밋 메시지
	Message string `json:"message"`

	// ParentHashes - 부모 커밋들의 해시
	ParentHashes []string `json:"parent_hashes"`

	// TreeHash - 커밋의 트리 객체 해시
	TreeHash string `json:"tree_hash"`
}

// CommitAuthor - 커밋 작성자/실행자 정보
type CommitAuthor struct {
	// Name - 작성자/실행자 이름
	Name string `json:"name"`

	// Email - 작성자/실행자 이메일
	Email string `json:"email"`

	// Date - 커밋 타임스탬프
	Date time.Time `json:"date"`
}

// Branch - Git 브랜치 정보
type Branch struct {
	// Name - 브랜치 이름
	Name string `json:"name"`

	// Hash - 브랜치가 가리키는 커밋 해시
	Hash string `json:"hash"`

	// IsRemote - 원격 브랜치 여부
	IsRemote bool `json:"is_remote"`

	// IsCurrent - 현재 체크아웃된 브랜치 여부
	IsCurrent bool `json:"is_current"`
}

// Tag - Git 태그 정보
type Tag struct {
	// Name - 태그 이름
	Name string `json:"name"`

	// Hash - 태그가 가리키는 커밋 해시
	Hash string `json:"hash"`

	// Message - 태그 메시지 (annotated tag인 경우)
	Message string `json:"message,omitempty"`

	// Tagger - 태그 작성자 정보 (annotated tag인 경우)
	Tagger *CommitAuthor `json:"tagger,omitempty"`

	// IsAnnotated - annotated tag 여부
	IsAnnotated bool `json:"is_annotated"`
}

// Remote - Git 원격 저장소 정보
type Remote struct {
	// Name - 원격 저장소 이름 (예: origin, upstream)
	Name string `json:"name"`

	// URL - 원격 저장소 URL
	URL string `json:"url"`

	// IsDefault - 기본 원격 저장소 여부 (origin)
	IsDefault bool `json:"is_default"`
}

// OpenRepositoryFunc - 리포지토리를 여는 함수 타입
type OpenRepositoryFunc func(path string) (Repository, error)

// RepositoryManager - 고수준 리포지토리 관리 작업 제공
type RepositoryManager interface {
	// CreateRepository - 지정된 경로에 새 Git 리포지토리 생성
	CreateRepository(ctx context.Context, options *CreateRepositoryOptions) (*RepositoryInfo, error)

	// OpenRepository - 기존 Git 리포지토리 열기
	OpenRepository(ctx context.Context, path string) (Repository, error)

	// RepositoryExists checks if a repository exists at the given path
	RepositoryExists(ctx context.Context, path string) (bool, error)

	// DeleteRepository removes a repository from the filesystem
	DeleteRepository(ctx context.Context, path string) error

	// ListRepositories returns a list of repositories in a directory
	ListRepositories(ctx context.Context, rootPath string) ([]RepositoryInfo, error)
}

// CreateRepositoryOptions contains options for creating a new repository
type CreateRepositoryOptions struct {
	// Path is the filesystem path where the repository should be created
	Path string

	// Bare indicates whether to create a bare repository
	Bare bool

	// Description is an optional description for the repository
	Description string

	// DefaultBranch is the name of the default branch (defaults to "main")
	DefaultBranch string

	// InitialCommit specifies whether to create an initial commit
	InitialCommit bool

	// Author information for the initial commit
	Author *CommitAuthor

	// InitialFiles is a map of file paths to their content for the initial commit
	InitialFiles map[string]string

	// Template is an optional template repository to copy from
	Template string
}

// RepositoryInfo contains metadata about a Git repository
type RepositoryInfo struct {
	// Path is the filesystem path of the repository
	Path string `json:"path"`

	// Name is the repository name (derived from path)
	Name string `json:"name"`

	// Description is the repository description
	Description string `json:"description,omitempty"`

	// DefaultBranch is the name of the default branch
	DefaultBranch string `json:"default_branch"`

	// IsBare indicates whether this is a bare repository
	IsBare bool `json:"is_bare"`

	// CreatedAt is the creation timestamp
	CreatedAt time.Time `json:"created_at"`

	// LastModified is the last modification timestamp
	LastModified time.Time `json:"last_modified"`

	// Size is the repository size in bytes
	Size int64 `json:"size"`

	// CommitCount is the total number of commits
	CommitCount int `json:"commit_count"`

	// BranchCount is the total number of branches
	BranchCount int `json:"branch_count"`

	// TagCount is the total number of tags
	TagCount int `json:"tag_count"`
}
