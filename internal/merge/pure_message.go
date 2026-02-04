package merge

import (
	"fmt"
	"strings"
)

// message.go - Layer 0: Pure Functions
// commit/response 메시지 생성 (순수 함수, 부작용 없음)

// GenerateMergeCommitMsg generates a merge commit message
// If customMessage is provided, uses it; otherwise generates default
func GenerateMergeCommitMsg(customMessage, sourceRef, targetRef string) string {
	if customMessage != "" {
		return customMessage
	}

	// Simplify ref names
	sourceRef = simplifyRefName(sourceRef)
	targetRef = simplifyRefName(targetRef)

	return fmt.Sprintf("Merge branch '%s' into '%s'", sourceRef, targetRef)
}

// GenerateSquashCommitMsg generates a squash merge commit message
func GenerateSquashCommitMsg(customMessage, sourceRef, targetRef string) string {
	if customMessage != "" {
		return customMessage
	}

	// Simplify ref names
	sourceRef = simplifyRefName(sourceRef)
	targetRef = simplifyRefName(targetRef)

	return fmt.Sprintf("Squash merge branch '%s' into '%s'", sourceRef, targetRef)
}

// GenerateMergeResponseMsg generates a response message for completed merge
func GenerateMergeResponseMsg(isFastForward, hasConflicts bool, sourceRef, targetRef string) string {
	if isFastForward {
		return fmt.Sprintf("Fast-forward merge from '%s' to '%s' completed successfully", sourceRef, targetRef)
	}

	if hasConflicts {
		return fmt.Sprintf("Merge from '%s' to '%s' completed with conflicts resolved", sourceRef, targetRef)
	}

	return fmt.Sprintf("Merge from '%s' to '%s' completed successfully", sourceRef, targetRef)
}

// GetMergeTypeString returns the type of merge performed as a string
func GetMergeTypeString(method MergeMethod, isFastForward, hasConflicts bool) string {
	switch method {
	case MergeMethodSquash:
		return "squash"
	case MergeMethodRebase:
		return "rebase"
	case MergeMethodMerge:
		fallthrough
	default:
		if isFastForward {
			return "fast-forward"
		}
		if hasConflicts {
			return "three-way-with-conflicts"
		}
		return "three-way"
	}
}

// simplifyRefName removes common ref prefixes
func simplifyRefName(ref string) string {
	ref = strings.TrimPrefix(ref, "refs/heads/")
	ref = strings.TrimPrefix(ref, "refs/tags/")
	ref = strings.TrimPrefix(ref, "refs/")
	return ref
}

// NormalizeRefName ensures ref has proper prefix
func NormalizeRefName(ref string) string {
	if strings.HasPrefix(ref, "refs/") {
		return ref
	}
	return "refs/heads/" + ref
}
