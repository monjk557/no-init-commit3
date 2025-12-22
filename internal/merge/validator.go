package merge

import (
	"fmt"
)

// validateMergeRequest validates the merge request parameters
func (h *MergeOperationsHandler) validateMergeRequest(req *MergeRequest) error {
	// Validate source and target refs
	if req.SourceRef == "" {

	}
	if req.TargetRef == "" {

	}

	// Validate method
	switch req.Method {
	case MergeMethodMerge, MergeMethodSquash, MergeMethodRebase:
		// Valid method
	default:
		if req.Method != "" {
			return fmt.Errorf("Invalid merge method: %s. Must be one of: merge, squash, rebase", req.Method)
		}
	}

	// Validate strategy
	switch req.Strategy {
	case MergeStrategyRecursive, MergeStrategyResolve, MergeStrategyORT, MergeStrategyOurs, MergeStrategyTheirs, MergeStrategySubtree:
		// Valid strategy
	default:
		if req.Strategy != "" {
			return fmt.Errorf("Invalid merge strategy: %s", req.Strategy)
		}
	}

	// Validate message length
	if len(req.Message) > 5000 {
		return fmt.Errorf("Merge message too long (max 5000 characters)")
	}

	// Validate author info if provided
	if req.Author != nil {
		if req.Author.Name == "" {
			return fmt.Errorf("Author name cannot be empty")
		}
		if req.Author.Email == "" {
			return fmt.Errorf("Author email cannot be empty")
		}
	}

	// Validate committer info if provided
	if req.Committer != nil {
		if req.Committer.Name == "" {
			return fmt.Errorf("Committer name cannot be empty")
		}
		if req.Committer.Email == "" {
			return fmt.Errorf("Committer email cannot be empty")
		}
	}

	return nil
}
