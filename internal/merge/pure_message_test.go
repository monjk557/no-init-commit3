package merge

import (
	"testing"
)

func Test_GenerateMergeCommitMsg_CustomMessage_ReturnsCustom(t *testing.T) {
	t.Parallel()

	got := GenerateMergeCommitMsg("Custom merge message", "feature", "main")

	if got != "Custom merge message" {
		t.Fatalf("GenerateMergeCommitMsg()=%q want=%q", got, "Custom merge message")
	}
}

func Test_GenerateMergeCommitMsg_EmptyCustom_ReturnsDefault(t *testing.T) {
	t.Parallel()

	got := GenerateMergeCommitMsg("", "feature/test", "main")
	want := "Merge branch 'feature/test' into 'main'"

	if got != want {
		t.Fatalf("GenerateMergeCommitMsg()=%q want=%q", got, want)
	}
}

func Test_GenerateMergeCommitMsg_SimplifiesFullRefs(t *testing.T) {
	t.Parallel()

	got := GenerateMergeCommitMsg("", "refs/heads/feature", "refs/heads/main")
	want := "Merge branch 'feature' into 'main'"

	if got != want {
		t.Fatalf("GenerateMergeCommitMsg()=%q want=%q", got, want)
	}
}

func Test_GenerateSquashCommitMsg_CustomMessage_ReturnsCustom(t *testing.T) {
	t.Parallel()

	got := GenerateSquashCommitMsg("Squash: all features", "feature", "main")

	if got != "Squash: all features" {
		t.Fatalf("GenerateSquashCommitMsg()=%q want=%q", got, "Squash: all features")
	}
}

func Test_GenerateSquashCommitMsg_EmptyCustom_ReturnsDefault(t *testing.T) {
	t.Parallel()

	got := GenerateSquashCommitMsg("", "feature/test", "main")
	want := "Squash merge branch 'feature/test' into 'main'"

	if got != want {
		t.Fatalf("GenerateSquashCommitMsg()=%q want=%q", got, want)
	}
}

func Test_GenerateMergeResponseMsg_FastForward(t *testing.T) {
	t.Parallel()

	got := GenerateMergeResponseMsg(true, false, "feature", "main")
	want := "Fast-forward merge from 'feature' to 'main' completed successfully"

	if got != want {
		t.Fatalf("GenerateMergeResponseMsg()=%q want=%q", got, want)
	}
}

func Test_GenerateMergeResponseMsg_WithConflicts(t *testing.T) {
	t.Parallel()

	got := GenerateMergeResponseMsg(false, true, "feature", "main")
	want := "Merge from 'feature' to 'main' completed with conflicts resolved"

	if got != want {
		t.Fatalf("GenerateMergeResponseMsg()=%q want=%q", got, want)
	}
}

func Test_GenerateMergeResponseMsg_Normal(t *testing.T) {
	t.Parallel()

	got := GenerateMergeResponseMsg(false, false, "feature", "main")
	want := "Merge from 'feature' to 'main' completed successfully"

	if got != want {
		t.Fatalf("GenerateMergeResponseMsg()=%q want=%q", got, want)
	}
}

func Test_GetMergeTypeString_Squash(t *testing.T) {
	t.Parallel()

	got := GetMergeTypeString(MergeMethodSquash, false, false)

	if got != "squash" {
		t.Fatalf("GetMergeTypeString(Squash)=%q want=%q", got, "squash")
	}
}

func Test_GetMergeTypeString_Rebase(t *testing.T) {
	t.Parallel()

	got := GetMergeTypeString(MergeMethodRebase, false, false)

	if got != "rebase" {
		t.Fatalf("GetMergeTypeString(Rebase)=%q want=%q", got, "rebase")
	}
}

func Test_GetMergeTypeString_FastForward(t *testing.T) {
	t.Parallel()

	got := GetMergeTypeString(MergeMethodMerge, true, false)

	if got != "fast-forward" {
		t.Fatalf("GetMergeTypeString(FF)=%q want=%q", got, "fast-forward")
	}
}

func Test_GetMergeTypeString_ThreeWayWithConflicts(t *testing.T) {
	t.Parallel()

	got := GetMergeTypeString(MergeMethodMerge, false, true)

	if got != "three-way-with-conflicts" {
		t.Fatalf("GetMergeTypeString(3way+conflicts)=%q want=%q", got, "three-way-with-conflicts")
	}
}

func Test_GetMergeTypeString_ThreeWay(t *testing.T) {
	t.Parallel()

	got := GetMergeTypeString(MergeMethodMerge, false, false)

	if got != "three-way" {
		t.Fatalf("GetMergeTypeString(3way)=%q want=%q", got, "three-way")
	}
}

func Test_NormalizeRefName_AlreadyHasPrefix(t *testing.T) {
	t.Parallel()

	got := NormalizeRefName("refs/heads/feature")

	if got != "refs/heads/feature" {
		t.Fatalf("NormalizeRefName()=%q want=%q", got, "refs/heads/feature")
	}
}

func Test_NormalizeRefName_AddsPrefix(t *testing.T) {
	t.Parallel()

	got := NormalizeRefName("feature")
	want := "refs/heads/feature"

	if got != want {
		t.Fatalf("NormalizeRefName()=%q want=%q", got, want)
	}
}

func Test_NormalizeRefName_TagRef_Preserved(t *testing.T) {
	t.Parallel()

	got := NormalizeRefName("refs/tags/v1.0.0")

	if got != "refs/tags/v1.0.0" {
		t.Fatalf("NormalizeRefName(tag)=%q want=%q", got, "refs/tags/v1.0.0")
	}
}
