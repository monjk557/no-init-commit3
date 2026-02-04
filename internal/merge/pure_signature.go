package merge

import (
	"time"

	"github.com/go-git/go-git/v5/plumbing/object"
)

// signature.go - Layer 0: Pure Types + Helper Functions
// author/committer 생성 로직 (순수 함수, 부작용 없음)

// SignatureInput represents the input for creating a signature
// This is a pure data structure
type SignatureInput struct {
	Name  string
	Email string
	Date  time.Time
}

// CreateSignature creates a go-git Signature from input
// If input is nil, returns the fallback signature
// If input.Date is zero, uses fallbackTime
func CreateSignature(input *SignatureInput, fallback *object.Signature, fallbackTime time.Time) object.Signature {
	if input == nil {
		if fallback != nil {
			return *fallback
		}
		return object.Signature{
			Name:  "Git Executor",
			Email: "git@executor.com",
			When:  fallbackTime,
		}
	}

	date := input.Date
	if date.IsZero() {
		date = fallbackTime
	}

	return object.Signature{
		Name:  input.Name,
		Email: input.Email,
		When:  date,
	}
}

// CreateAuthorSignature creates an author signature from MergeRequest
func CreateAuthorSignature(req *MergeRequest, fallback *object.Signature, now time.Time) object.Signature {
	if req == nil || req.Author == nil {
		return CreateSignature(nil, fallback, now)
	}

	return CreateSignature(&SignatureInput{
		Name:  req.Author.Name,
		Email: req.Author.Email,
		Date:  req.Author.Date,
	}, fallback, now)
}

// CreateCommitterSignature creates a committer signature from MergeRequest
// If committer is not specified, uses the author signature
func CreateCommitterSignature(req *MergeRequest, author *object.Signature, now time.Time) object.Signature {
	if req == nil || req.Committer == nil {
		if author != nil {
			return *author
		}
		return CreateSignature(nil, nil, now)
	}

	return CreateSignature(&SignatureInput{
		Name:  req.Committer.Name,
		Email: req.Committer.Email,
		Date:  req.Committer.Date,
	}, author, now)
}

// CreateRebaseAuthorSignature creates an author signature for rebase
// Preserves original commit author if not overridden
func CreateRebaseAuthorSignature(req *MergeRequest, originalAuthor object.Signature, now time.Time) object.Signature {
	if req == nil || req.Author == nil {
		return originalAuthor
	}

	date := req.Author.Date
	if date.IsZero() {
		date = originalAuthor.When // Preserve original author date for rebase
	}

	return object.Signature{
		Name:  req.Author.Name,
		Email: req.Author.Email,
		When:  date,
	}
}

// CreateRebaseCommitterSignature creates a committer signature for rebase
// Uses current time by default (rebase changes commit time)
func CreateRebaseCommitterSignature(req *MergeRequest, author object.Signature, now time.Time) object.Signature {
	if req == nil || req.Committer == nil {
		return object.Signature{
			Name:  author.Name,
			Email: author.Email,
			When:  now, // Rebase changes commit time
		}
	}

	date := req.Committer.Date
	if date.IsZero() {
		date = now // Use current time for rebase
	}

	return object.Signature{
		Name:  req.Committer.Name,
		Email: req.Committer.Email,
		When:  date,
	}
}
