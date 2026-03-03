//go:build gogit

package merge

import (
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"
)

func Test_generateMergeMessage_FastForward_ReturnsSuccessMessage(t *testing.T) {
	t.Parallel()

	result := &MergeResult{IsFastForward: true, HasConflicts: false}
	req := &MergeRequest{SourceRef: "feature", TargetRef: "main"}

	got := generateMergeMessage(result, req)

	require.Contains(t, got, "Fast-forward merge")
	require.Contains(t, got, "feature")
	require.Contains(t, got, "main")
}

func Test_generateMergeMessage_WithConflicts_ReturnsConflictsResolvedMessage(t *testing.T) {
	t.Parallel()

	result := &MergeResult{IsFastForward: false, HasConflicts: true}
	req := &MergeRequest{SourceRef: "feature", TargetRef: "main"}

	got := generateMergeMessage(result, req)

	require.Contains(t, got, "completed with conflicts resolved")
	require.Contains(t, got, "feature")
	require.Contains(t, got, "main")
}

func Test_generateMergeMessage_ThreeWay_ReturnsSuccessMessage(t *testing.T) {
	t.Parallel()

	result := &MergeResult{IsFastForward: false, HasConflicts: false}
	req := &MergeRequest{SourceRef: "feature", TargetRef: "main"}

	got := generateMergeMessage(result, req)

	require.Contains(t, got, "completed successfully")
	require.NotContains(t, got, "Fast-forward")
	require.NotContains(t, got, "conflicts resolved")
}

func Test_getMergeType_FastForward_ReturnsFastForward(t *testing.T) {
	t.Parallel()

	result := &MergeResult{IsFastForward: true, HasConflicts: false}

	got := getMergeType(result)

	require.Equal(t, "fast-forward", got)
}

func Test_getMergeType_WithConflicts_ReturnsThreeWayWithConflicts(t *testing.T) {
	t.Parallel()

	result := &MergeResult{IsFastForward: false, HasConflicts: true}

	got := getMergeType(result)

	require.Equal(t, "three-way-with-conflicts", got)
}

func Test_getMergeType_ThreeWay_ReturnsThreeWay(t *testing.T) {
	t.Parallel()

	result := &MergeResult{IsFastForward: false, HasConflicts: false}

	got := getMergeType(result)

	require.Equal(t, "three-way", got)
}

func Test_getMergeTypeForMethod_Squash_ReturnsSquash(t *testing.T) {
	t.Parallel()

	result := &MergeResult{IsFastForward: false, HasConflicts: false}

	got := getMergeTypeForMethod(MergeMethodSquash, result)

	require.Equal(t, "squash", got)
}

func Test_getMergeTypeForMethod_Rebase_ReturnsRebase(t *testing.T) {
	t.Parallel()

	result := &MergeResult{IsFastForward: false, HasConflicts: false}

	got := getMergeTypeForMethod(MergeMethodRebase, result)

	require.Equal(t, "rebase", got)
}

func Test_getMergeTypeForMethod_Merge_DelegatesToGetMergeType(t *testing.T) {
	t.Parallel()

	result := &MergeResult{IsFastForward: true, HasConflicts: false}

	got := getMergeTypeForMethod(MergeMethodMerge, result)

	require.Equal(t, "fast-forward", got)
}

func Test_commitToInfo_ReturnsMapWithShaMessageAuthorCommitter(t *testing.T) {
	t.Parallel()

	when := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	commit := &object.Commit{
		Hash:    plumbing.NewHash("abc1230000000000000000000000000000000000"),
		Message: "fix: something",
		Author: object.Signature{
			Name:  "Author",
			Email: "a@example.com",
			When:  when,
		},
		Committer: object.Signature{
			Name:  "Committer",
			Email: "c@example.com",
			When:  when,
		},
	}

	h := &MergeOperationsHandler{}
	got := h.commitToInfo(commit)

	require.Equal(t, commit.Hash.String(), got["sha"])
	require.Equal(t, "fix: something", got["message"])
	require.NotNil(t, got["author"])
	require.NotNil(t, got["committer"])
	require.Equal(t, when, got["created_at"])
}
