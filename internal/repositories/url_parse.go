package repositories

import (
	"net/url"
	"strings"
)

// (ParsedRepoURL struct lives in schema.go so the OpenAPI spec generator,
// which only scans schema.go files, can emit a $ref to it.)

// ParseRepoURL extracts (host, owner, repo) from a git remote URL.
// Returns nil for unrecognized formats (e.g. relative URLs, file://, or
// truncated input). Recognized formats:
//
//   - https://<host>/<owner>/<repo>(.git)?(/?)
//   - http://<host>/<owner>/<repo>(.git)?(/?)
//   - ssh://[user@]<host>[:port]/<owner>/<repo>(.git)?(/?)
//   - git://<host>(:port)?/<owner>/<repo>(.git)?
//   - SCP-style: [user@]<host>:<owner>/<repo>(.git)?
//
// Relative URLs (`./foo.git`, `../foo.git`) return nil — callers should
// resolve those against the parent repo URL before parsing.
func ParseRepoURL(raw string) *ParsedRepoURL {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	// Relative URL: cannot resolve without parent repo context.
	if strings.HasPrefix(raw, "./") || strings.HasPrefix(raw, "../") {
		return nil
	}

	if scp := parseSCPStyle(raw); scp != nil {
		return scp
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	switch u.Scheme {
	case "http", "https", "ssh", "git":
		// supported
	default:
		return nil
	}
	host := u.Hostname()
	if host == "" {
		return nil
	}
	owner, repo, ok := splitOwnerRepo(u.Path)
	if !ok {
		return nil
	}
	return &ParsedRepoURL{Host: host, Owner: owner, Repo: repo}
}

// parseSCPStyle handles `[user@]host:owner/repo(.git)?` — distinguished from
// `host:port/path` by the absence of a leading scheme and the `:` not being
// followed by a numeric port.
func parseSCPStyle(raw string) *ParsedRepoURL {
	// Reject anything containing a scheme separator.
	if strings.Contains(raw, "://") {
		return nil
	}
	colon := strings.IndexByte(raw, ':')
	if colon <= 0 {
		return nil
	}
	hostPart := raw[:colon]
	pathPart := raw[colon+1:]

	// Strip optional `user@` prefix from host part.
	if at := strings.IndexByte(hostPart, '@'); at >= 0 {
		hostPart = hostPart[at+1:]
	}
	if hostPart == "" || pathPart == "" {
		return nil
	}
	// SCP path must contain at least one `/` (owner/repo). Reject ":port"
	// shapes where pathPart is purely digits.
	if !strings.Contains(pathPart, "/") {
		return nil
	}
	owner, repo, ok := splitOwnerRepo(pathPart)
	if !ok {
		return nil
	}
	return &ParsedRepoURL{Host: hostPart, Owner: owner, Repo: repo}
}

// splitOwnerRepo extracts the trailing two non-empty path components and
// strips a `.git` suffix from the repo name. Leading and trailing `/` are
// tolerated. Returns ok=false when fewer than two segments are present.
func splitOwnerRepo(path string) (string, string, bool) {
	path = strings.Trim(path, "/")
	if path == "" {
		return "", "", false
	}
	segs := strings.Split(path, "/")
	// 마지막 두 segment 가 owner / repo. 더 깊은 namespace (gitlab subgroup
	// 등) 은 owner = 앞부분 join 으로 합쳐서 반환 → 단일 인스턴스 안 navigation
	// 시 그대로 path 에 사용 가능.
	if len(segs) < 2 {
		return "", "", false
	}
	repo := strings.TrimSuffix(segs[len(segs)-1], ".git")
	if repo == "" {
		return "", "", false
	}
	owner := strings.Join(segs[:len(segs)-1], "/")
	if owner == "" {
		return "", "", false
	}
	return owner, repo, true
}
