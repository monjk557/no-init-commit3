// Package fork는 Git 저장소 포크 기능을 제공합니다.
// 저장소 복제, 업스트림 추적, 동기화 기능을 구현합니다.
package fork

import (
	"errors"
	"time"
)

// Sentinel errors for fork operations.
var (
	ErrSourceNotFound    = errors.New("source repository not found")
	ErrForkAlreadyExists = errors.New("fork target repository already exists")
	ErrNotAFork          = errors.New("repository is not a fork")
	ErrUpstreamNotFound  = errors.New("upstream repository no longer exists")
)

// ForkRepositoryRequest는 저장소 포크를 위한 JSON 페이로드를 나타냅니다.
type ForkRepositoryRequest struct {
	// Owner는 포크 대상 소유자입니다 (필수).
	Owner string `json:"owner" binding:"required"`
	// Repo는 포크 대상 저장소 이름입니다 (선택, 미지정 시 원본과 동일).
	Repo string `json:"repo,omitempty"`
	// DefaultBranch는 포크의 기본 브랜치를 오버라이드합니다 (선택).
	DefaultBranch string `json:"default_branch,omitempty"`
}

// ForkRepositoryResponse는 포크 생성 결과를 나타냅니다.
type ForkRepositoryResponse struct {
	Success          bool          `json:"success"`
	Repository       string        `json:"repository"`
	SourceRepository string        `json:"source_repository"`
	Owner            string        `json:"owner"`
	Name             string        `json:"name"`
	DefaultBranch    string        `json:"default_branch"`
	CreatedAt        time.Time     `json:"created_at"`
	ForkMetadata     *ForkMetadata `json:"fork_metadata"`
}

// ForkMetadata는 포크 관계 메타데이터를 나타냅니다.
// bare 저장소 루트에 .fork-metadata.json 파일로 저장됩니다.
type ForkMetadata struct {
	SourceOwner string    `json:"source_owner"`
	SourceRepo  string    `json:"source_repo"`
	SourcePath  string    `json:"source_path"`
	ForkedAt    time.Time `json:"forked_at"`
	ForkedBy    string    `json:"forked_by,omitempty"`
}

// ForkInfoResponse는 포크 정보 조회 결과를 나타냅니다.
type ForkInfoResponse struct {
	Success      bool          `json:"success"`
	IsFork       bool          `json:"is_fork"`
	Repository   string        `json:"repository"`
	ForkMetadata *ForkMetadata `json:"fork_metadata,omitempty"`
}

// SyncUpstreamResponse는 업스트림 동기화 결과를 나타냅니다.
type SyncUpstreamResponse struct {
	Success        bool             `json:"success"`
	BranchesSynced []SyncedBranch   `json:"branches_synced"`
	Conflicts      []ConflictBranch `json:"conflicts,omitempty"`
}

// SyncedBranch는 성공적으로 동기화된 브랜치 정보를 나타냅니다.
type SyncedBranch struct {
	Name          string `json:"name"`
	OldCommitSHA  string `json:"old_commit_sha"`
	NewCommitSHA  string `json:"new_commit_sha"`
	CommitsBehind int    `json:"commits_behind"`
}

// ConflictBranch는 동기화 시 충돌이 발생한 브랜치 정보를 나타냅니다.
type ConflictBranch struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}
