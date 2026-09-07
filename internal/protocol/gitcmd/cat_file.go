package gitcmd

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/gitutil"
)

// CommitInfo holds parsed commit metadata.
type CommitInfo struct {
	TreeOID        string
	ParentOIDs     []string
	AuthorName     string
	AuthorEmail    string
	AuthorDate     time.Time
	CommitterName  string
	CommitterEmail string
	CommitterDate  time.Time
	Message        string
}

// GetCommitInfo runs `git cat-file commit <oid>` and parses the commit object.
func GetCommitInfo(ctx context.Context, repoPath string, oid string) (*CommitInfo, error) {
	cmd := exec.CommandContext(ctx, "git", "cat-file", "commit", oid)
	cmd.Dir = repoPath
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return nil, fmt.Errorf("git cat-file commit %s: %w (stderr: %s)", oid, err, stderr.String())
	}

	return parseCommitObject(string(out))
}

// parseCommitObject parses raw commit object content.
func parseCommitObject(raw string) (*CommitInfo, error) {
	info := &CommitInfo{}

	// Split headers from message (separated by empty line)
	parts := strings.SplitN(raw, "\n\n", 2)
	if len(parts) == 2 {
		info.Message = parts[1]
	}

	// Parse headers
	for _, line := range strings.Split(parts[0], "\n") {
		switch {
		case strings.HasPrefix(line, "tree "):
			info.TreeOID = strings.TrimPrefix(line, "tree ")
		case strings.HasPrefix(line, "parent "):
			info.ParentOIDs = append(info.ParentOIDs, strings.TrimPrefix(line, "parent "))
		case strings.HasPrefix(line, "author "):
			name, email, t := parseIdentity(strings.TrimPrefix(line, "author "))
			info.AuthorName = name
			info.AuthorEmail = email
			info.AuthorDate = t
		case strings.HasPrefix(line, "committer "):
			name, email, t := parseIdentity(strings.TrimPrefix(line, "committer "))
			info.CommitterName = name
			info.CommitterEmail = email
			info.CommitterDate = t
		}
	}

	return info, nil
}

// parseIdentity parses "Name <email> timestamp timezone" format.
func parseIdentity(s string) (name, email string, t time.Time) {
	// Format: "Name <email> 1234567890 +0900"
	ltIdx := strings.LastIndex(s, "<")
	gtIdx := strings.LastIndex(s, ">")
	if ltIdx < 0 || gtIdx < 0 || gtIdx <= ltIdx {
		return s, "", time.Time{}
	}

	name = strings.TrimSpace(s[:ltIdx])
	email = s[ltIdx+1 : gtIdx]

	// Parse timestamp and timezone after ">"
	rest := strings.TrimSpace(s[gtIdx+1:])
	fields := strings.Fields(rest)
	if len(fields) >= 2 {
		// Parse as "unix_timestamp timezone"
		t, _ = time.Parse("2006-01-02T15:04:05Z07:00",
			"1970-01-01T00:00:00+00:00") // fallback
		if ts, err := fmt.Sscanf(fields[0], "%d", new(int64)); err == nil && ts == 1 {
			var unix int64
			if _, err := fmt.Sscanf(fields[0], "%d", &unix); err != nil {
				unix = 0 // 파싱 실패 시 0 fallback (기존 동작 유지)
			}
			loc := time.UTC
			if len(fields) >= 2 {
				if l, err := time.Parse("-0700", fields[1]); err == nil {
					loc = l.Location()
				}
			}
			t = time.Unix(unix, 0).In(loc)
		}
	}

	return name, email, t
}

// CatBlob reads the content of a blob object from the repository.
func CatBlob(ctx context.Context, repoPath string, oid string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", "cat-file", "blob", oid)
	cmd.Dir = repoPath
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// Blob content is capped at gitutil.MaxOutputLarge (50MB) — this path buffers
	// the whole object, unlike the streaming raw /blob endpoint.
	out, err := gitutil.OutputWithLimit(cmd, gitutil.MaxOutputLarge)
	if err != nil {
		return nil, fmt.Errorf("cat-file blob %s: %w (stderr: %s)", oid, err, stderr.String())
	}
	return out, nil
}

// DiffTree runs `git diff-tree` and returns the raw output.
func DiffTree(ctx context.Context, repoPath string, args ...string) ([]byte, error) {
	gitArgs := append([]string{"diff-tree"}, args...)
	cmd := exec.CommandContext(ctx, "git", gitArgs...)
	cmd.Dir = repoPath
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return nil, fmt.Errorf("diff-tree: %w (stderr: %s)", err, stderr.String())
	}
	return out, nil
}

// PeelToCommit resolves an object id to the commit it ultimately points at by
// running `git rev-parse --verify <oid>^{commit}`. It is idempotent across the
// three inputs we care about for tag refs:
//   - a lightweight tag (oid already a commit) → same commit SHA
//   - an annotated tag object → the peeled commit SHA (tag object SHA is never returned)
//   - a commit oid → the same commit SHA
//
// Downstream consumers (orchestrator) therefore always receive a commit SHA in
// the event payload, regardless of tag flavor (Issue-2829 design decision #4).
func PeelToCommit(ctx context.Context, repoPath string, oid string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", oid+"^{commit}")
	cmd.Dir = repoPath
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return "", fmt.Errorf("git rev-parse %s^{commit}: %w (stderr: %s)", oid, err, stderr.String())
	}
	return strings.TrimSpace(string(out)), nil
}

// FilterExistingObjects returns the subset of oids that exist in the repository
// as a membership set, using a single `git cat-file --batch-check` process.
//
// During fetch negotiation a client may advertise hundreds or thousands of
// "have" OIDs. Checking each one with ObjectExists spawns a separate
// `git cat-file -t` subprocess (O(N) fork/exec). Feeding every OID to one
// batch-check process collapses that to a single fork — matching how native
// git resolves haves in-process during upload-pack.
//
// Unknown OIDs (force-pushed away or GC'd) are reported by git as
// "<oid> missing" and excluded. Callers should iterate their original ordered
// slice and test membership against the returned set so that the
// protocol-significant order of ACKs is preserved.
func FilterExistingObjects(ctx context.Context, repoPath string, oids []string) (map[string]bool, error) {
	existing := make(map[string]bool, len(oids))
	if len(oids) == 0 {
		return existing, nil
	}

	cmd := exec.CommandContext(ctx, "git", "cat-file", "--batch-check=%(objectname) %(objecttype)")
	cmd.Dir = repoPath

	var stdin bytes.Buffer
	for _, oid := range oids {
		stdin.WriteString(oid)
		stdin.WriteByte('\n')
	}
	cmd.Stdin = &stdin

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return nil, fmt.Errorf("git cat-file --batch-check: %w (stderr: %s)", err, stderr.String())
	}

	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// "<oid> <type>" for an existing object; "<oid> missing" (or
		// "ambiguous") otherwise. Whitelist real object types so non-existent
		// or malformed entries are never treated as present.
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[1] {
		case "commit", "tree", "blob", "tag":
			existing[fields[0]] = true
		}
	}
	return existing, nil
}

// ObjectExists checks if an object exists in the repository and returns its type.
// Returns the object type ("commit", "tree", "blob", "tag") or error if not found.
func ObjectExists(ctx context.Context, repoPath string, oid string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "cat-file", "-t", oid)
	cmd.Dir = repoPath
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return "", fmt.Errorf("object %s not found: %w", oid, err)
	}
	return strings.TrimSpace(string(out)), nil
}
