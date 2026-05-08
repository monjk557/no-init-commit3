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

// TreeAPIRequest는 `GET /tree` 의 query parameter struct 다.
//
// typed.RegisterGetWithQuery wrapper 가 form 태그를 reflect 하여 OpenAPI
// parameters (in: query) 로 expand 한다 — orchestrator auto-gen client 가
// ref/path/recursive 인자를 누락하지 않도록 노출하기 위함 (Issue-2327).
//
// LEGACY (c.Query) 는 모든 필드가 optional 이고 잘못된 ref/path 는 핸들러가
// 자체 검증 / 404 처리. recursive 는 LEGACY 가 strconv.ParseBool 의 에러를
// 무시했으므로 garbage 값은 false 로 동작했다 — typed 화 후 garbage 값은
// gin 의 strict bool 파싱으로 400 이 된다 (의도된 strictness 향상).
type TreeAPIRequest struct {
	Ref       string `form:"ref,omitempty"`
	Path      string `form:"path,omitempty"`
	Recursive bool   `form:"recursive,omitempty"`
}

// TreeSearchRequest는 `GET /tree/search` 의 query parameter struct 다.
//
// Query 의 binding:"required" 는 의도적으로 빠져 있다 — LEGACY 핸들러가
// 비어 있을 때 친화적인 메시지 ("query parameter is required") 를 반환했고,
// gin 의 default required 에러 메시지는 struct 이름이 노출되어 client UX
// 가 떨어진다. 핸들러 본문에서 빈 값을 직접 검증한다.
type TreeSearchRequest struct {
	Ref   string `form:"ref,omitempty"`
	Query string `form:"query,omitempty"`
	Limit int    `form:"limit,omitempty"`
}
