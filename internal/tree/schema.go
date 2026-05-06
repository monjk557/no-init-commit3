package tree

import "github.com/collabops/git-executor/internal/repositories"

// TreeEntry는 Git 트리의 개별 항목(파일 또는 디렉토리)을 나타냅니다.
// 항목의 유형, 이름, 전체 경로를 포함합니다.

type EntryType string

const (
	// EntryTypeBlob — 파일
	EntryTypeBlob EntryType = "blob"
	// EntryTypeTree — 디렉터리
	EntryTypeTree EntryType = "tree"
	// EntryTypeSubmodule — gitlink (mode 160000). 과거에는 "commit" 으로 노출됐다.
	EntryTypeSubmodule EntryType = "submodule"
	// EntryTypeCommit — ls-tree raw type. wire 응답에서는 "submodule" 로 매핑된다.
	EntryTypeCommit EntryType = "commit"
)

// SubmoduleInfo는 submodule (gitlink) 트리 엔트리의 부가 정보입니다.
// pinned commit (ls-tree 의 mode 160000 hash) 은 항상 채워지고, URL/branch 는
// .gitmodules 가 존재할 때만 채워집니다. ResolvedRepo / IsResolvable 은 같은
// 인스턴스 안의 mirror 매핑 또는 reachability 검증이 가능할 때 채워질 예약
// 필드로, Phase 2 에서는 ResolvedRepo=빈 문자열, IsResolvable=false 로 고정됩니다.
type SubmoduleInfo struct {
	Commit       string                      `json:"commit"`                  // pinned commit SHA (full hex)
	URL          string                      `json:"url,omitempty"`           // .gitmodules 의 url
	Branch       string                      `json:"branch,omitempty"`        // .gitmodules 의 branch (optional)
	Parsed       *repositories.ParsedRepoURL `json:"parsed,omitempty"`        // url 파싱 성공 시 (host/owner/repo)
	ResolvedRepo string                      `json:"resolved_repo,omitempty"` // mirror 매핑 결과 (Phase 5 reserved)
	IsResolvable bool                        `json:"is_resolvable"`           // pin commit reachability (Phase 5 reserved)
}

type TreeEntry struct {
	Type      string         `json:"type"`                // "tree" | "blob" | "submodule"
	Name      string         `json:"name"`                // Entry name
	Path      string         `json:"path"`                // Full path
	Submodule *SubmoduleInfo `json:"submodule,omitempty"` // type == "submodule" 일 때만 채워짐
}

// TreeEntriesResponse는 트리 항목 목록 요청에 대한 응답을 나타냅니다 (flat, data 래퍼 없음).
type TreeEntriesResponse struct {
	Success bool        `json:"success"`
	Entries []TreeEntry `json:"entries"`
}

// TreeEntriesRecursiveResponse는 재귀적 트리 항목 목록 요청에 대한 응답을 나타냅니다 (flat).
type TreeEntriesRecursiveResponse struct {
	Success bool                   `json:"success"`
	Tree    map[string][]TreeEntry `json:"tree"`
}

// TreeSearchResponse는 파일명 검색 요청에 대한 응답을 나타냅니다.
type TreeSearchResponse struct {
	Success    bool        `json:"success"`
	Query      string      `json:"query"`
	Entries    []TreeEntry `json:"entries"`
	TotalCount int         `json:"total_count"`
	Truncated  bool        `json:"truncated"`
}

// 파일 검색 기본 제한값
const (
	DefaultSearchLimit = 100
	MaxSearchLimit     = 1000
)
