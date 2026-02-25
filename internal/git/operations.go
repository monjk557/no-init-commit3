package git

import (
	"context"
	"time"

	gogit "github.com/go-git/go-git/v5"
)

// Operation represents a Git operation result
type Operation struct {
	Success  bool
	Duration time.Duration
	Error    error
}

// PerformOperation performs a generic Git operation
func PerformOperation(ctx context.Context, repo *gogit.Repository, operation string) Operation {
	start := time.Now()

	// Simulate Git operation
	switch operation {
	case "status":
		_, err := repo.Head()
		return Operation{
			Success:  err == nil,
			Duration: time.Since(start),
			Error:    err,
		}
	case "refs":
		_, err := repo.References()
		return Operation{
			Success:  err == nil,
			Duration: time.Since(start),
			Error:    err,
		}
	default:
		return Operation{
			Success:  true,
			Duration: time.Since(start),
		}
	}
}
