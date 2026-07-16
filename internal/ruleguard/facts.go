package ruleguard

import (
	"bufio"
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"

	"github.com/collabops/git-executor/internal/gitutil"
	"github.com/collabops/git-executor/internal/protocol/gitcmd"
)

// zeroOID is git's all-zero object id — the "old" side of a ref creation and the
// "new" side of a ref deletion.
const zeroOID = "0000000000000000000000000000000000000000"

// maxPushFactCommits bounds how many pushed commits we introspect for facts.
// Beyond this we drop the file-derived facts (fail-open) rather than let a giant
// push stall the enforcement choke point — merge-commit detection over the first
// N still stands.
const maxPushFactCommits = 1000

// bytesPerMB converts blob byte sizes to the megabytes the max_file_size rule
// compares against (push.max_file_size_mb).
const bytesPerMB = 1024.0 * 1024.0

// BuildPushFacts computes the ruleset evaluation `context` for one ref update,
// read from the pushed objects. It MUST run after the packfile is indexed
// (objects present) and before the ref is updated — see the Point-B validation
// in receive_pack.go.
//
// The returned map mirrors exactly the keys api-service build_facts() reads:
//
//	ref.is_creation / is_update / is_deletion   (ref transition)
//	commit.is_force_push                        (old is NOT an ancestor of new)
//	commit.is_merge_commit                      (ANY pushed commit has >1 parent)
//	push.file_paths                             (union of changed paths)
//	push.max_file_size_mb                       (largest added blob, in MB)
//
// Facts that cannot be computed (e.g. a range that git rejects, or a push above
// the introspection cap) are OMITTED, not guessed — the engine treats a missing
// fact as non-firing (fail-open), so a computation gap never fabricates a denial.
// Signature / committer-identity / actor-role facts are intentionally not
// produced here (deferred phases); their absence keeps the fail-closed factors at
// their current behavior, unchanged by this producer.
func BuildPushFacts(ctx context.Context, repoPath, oldHash, newHash, refName string) map[string]any {
	isCreation := oldHash == zeroOID
	isDeletion := newHash == zeroOID
	isUpdate := !isCreation && !isDeletion

	facts := map[string]any{
		"ref": map[string]any{
			"is_creation": isCreation,
			"is_update":   isUpdate,
			"is_deletion": isDeletion,
		},
	}

	// A deletion carries no pushed objects — ref-transition facts only.
	if isDeletion {
		return facts
	}

	commit := map[string]any{
		// Force-push = a non-fast-forward update (old not reachable from new).
		// A creation introduces new history, never rewrites it → not force.
		"is_force_push": isUpdate && !isAncestor(ctx, repoPath, oldHash, newHash),
	}

	isMerge, paths, maxBlobBytes, capped := introspectRange(ctx, repoPath, oldHash, newHash, isCreation)
	commit["is_merge_commit"] = isMerge

	// Tip (newest) commit metadata for message / author-email / committer-email
	// pattern rules. The evaluation fact contract is a SINGLE commit, so a
	// multi-commit push only validates the tip here — full per-commit coverage is
	// a later api-service change (iterate a commit list). Populating these also
	// clears the fail-closed deny-all these rules would otherwise trigger when no
	// commit facts are sent at all.
	if info, err := gitcmd.GetCommitInfo(ctx, repoPath, newHash); err == nil {
		commit["message"] = info.Message
		commit["author"] = map[string]any{"email": info.AuthorEmail}
		commit["committer"] = map[string]any{"email": info.CommitterEmail}
	}

	facts["commit"] = commit

	// Drop file facts on a capped/failed introspection (fail-open). An incomplete
	// path set could silently miss a restrict_file_paths match, so omitting is
	// safer than sending a partial set.
	if !capped {
		push := map[string]any{}
		if paths != nil {
			push["file_paths"] = paths
		}
		if maxBlobBytes > 0 {
			push["max_file_size_mb"] = float64(maxBlobBytes) / bytesPerMB
		}
		if len(push) > 0 {
			facts["push"] = push
		}
	}

	return facts
}

// isAncestor reports whether commit a is an ancestor of commit b
// (`git merge-base --is-ancestor a b`: exit 0 = yes). Any error (exit 1 or a
// missing object) is treated as "not an ancestor".
func isAncestor(ctx context.Context, repoPath, a, b string) bool {
	if a == b {
		return true
	}
	cmd := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", a, b)
	cmd.Dir = repoPath
	return cmd.Run() == nil
}

// introspectRange walks the commits introduced by the push in a single
// `git log --raw` process and returns:
//   - isMerge:       any pushed commit has more than one parent
//   - paths:         the union of changed file paths (nil if none/failure)
//   - maxBlobBytes:  the size of the largest added/modified blob (0 if none)
//   - capped:        the push exceeded maxPushFactCommits or git failed
//
// Range selection: an update walks old..new; a creation walks new excluding every
// existing ref (--not --all) — at Point-B the new objects are indexed but no ref
// points at them yet, so --all correctly excludes only pre-existing history.
func introspectRange(ctx context.Context, repoPath, oldHash, newHash string, isCreation bool) (isMerge bool, paths []string, maxBlobBytes int64, capped bool) {
	args := []string{
		"log", "--no-renames", "--no-abbrev", "--raw",
		"--format=C %H %P",
		"--max-count=" + strconv.Itoa(maxPushFactCommits+1),
	}
	if isCreation {
		args = append(args, newHash, "--not", "--all")
	} else {
		args = append(args, oldHash+".."+newHash)
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoPath
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return false, nil, 0, true // fail-open: omit commit-derived facts
	}

	pathSet := make(map[string]struct{})
	blobSet := make(map[string]struct{})
	commitCount := 0

	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "C "):
			commitCount++
			// "C <sha> <parent1> <parent2> ..." — >1 parent ⇒ merge commit.
			if len(strings.Fields(line[2:])) > 2 {
				isMerge = true
			}
		case strings.HasPrefix(line, ":"):
			// Raw diff line: ":<oldmode> <newmode> <oldsha> <newsha> <status>\t<path>"
			tab := strings.IndexByte(line, '\t')
			if tab < 0 {
				continue
			}
			pathSet[line[tab+1:]] = struct{}{}
			if meta := strings.Fields(line[:tab]); len(meta) >= 4 {
				if newSHA := meta[3]; newSHA != "" && newSHA != zeroOID {
					blobSet[newSHA] = struct{}{}
				}
			}
		}
	}

	if commitCount > maxPushFactCommits {
		return isMerge, nil, 0, true
	}

	return isMerge, setToSlice(pathSet), maxBlobSize(ctx, repoPath, setToSlice(blobSet)), false
}

// maxBlobSize returns the size in bytes of the largest object among shas, using a
// single `git cat-file --batch-check` process. Returns 0 on empty input or error.
func maxBlobSize(ctx context.Context, repoPath string, shas []string) int64 {
	if len(shas) == 0 {
		return 0
	}

	cmd := exec.CommandContext(ctx, "git", "cat-file", "--batch-check=%(objectsize)")
	cmd.Dir = repoPath
	var stdin bytes.Buffer
	for _, s := range shas {
		stdin.WriteString(s)
		stdin.WriteByte('\n')
	}
	cmd.Stdin = &stdin
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return 0
	}

	var maxBytes int64
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasSuffix(line, "missing") {
			continue
		}
		if n, err := strconv.ParseInt(line, 10, 64); err == nil && n > maxBytes {
			maxBytes = n
		}
	}
	return maxBytes
}

// setToSlice flattens a set to a slice, returning nil for an empty set so callers
// can distinguish "no paths" (omit the fact) from "empty list".
func setToSlice(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}
