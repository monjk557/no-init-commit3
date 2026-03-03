//go:build gogit

package merge

import (
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/object"
)

func Test_CreateSignature_NilInput_ReturnsFallback(t *testing.T) {
	t.Parallel()

	fallback := &object.Signature{
		Name:  "Fallback User",
		Email: "fallback@test.com",
		When:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	fallbackTime := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)

	got := CreateSignature(nil, fallback, fallbackTime)

	if got.Name != "Fallback User" {
		t.Fatalf("Name=%q want=%q", got.Name, "Fallback User")
	}
	if got.Email != "fallback@test.com" {
		t.Fatalf("Email=%q want=%q", got.Email, "fallback@test.com")
	}
}

func Test_CreateSignature_NilInputNilFallback_ReturnsDefault(t *testing.T) {
	t.Parallel()

	fallbackTime := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)

	got := CreateSignature(nil, nil, fallbackTime)

	if got.Name != "Git Executor" {
		t.Fatalf("Name=%q want=%q", got.Name, "Git Executor")
	}
	if got.Email != "git@executor.com" {
		t.Fatalf("Email=%q want=%q", got.Email, "git@executor.com")
	}
	if !got.When.Equal(fallbackTime) {
		t.Fatalf("When=%v want=%v", got.When, fallbackTime)
	}
}

func Test_CreateSignature_WithInput_UsesInputValues(t *testing.T) {
	t.Parallel()

	input := &SignatureInput{
		Name:  "Test Author",
		Email: "author@test.com",
		Date:  time.Date(2024, 3, 15, 10, 30, 0, 0, time.UTC),
	}
	fallbackTime := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)

	got := CreateSignature(input, nil, fallbackTime)

	if got.Name != "Test Author" {
		t.Fatalf("Name=%q want=%q", got.Name, "Test Author")
	}
	if got.Email != "author@test.com" {
		t.Fatalf("Email=%q want=%q", got.Email, "author@test.com")
	}
	if !got.When.Equal(input.Date) {
		t.Fatalf("When=%v want=%v", got.When, input.Date)
	}
}

func Test_CreateSignature_ZeroDate_UsesFallbackTime(t *testing.T) {
	t.Parallel()

	input := &SignatureInput{
		Name:  "Test Author",
		Email: "author@test.com",
		Date:  time.Time{}, // zero time
	}
	fallbackTime := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)

	got := CreateSignature(input, nil, fallbackTime)

	if !got.When.Equal(fallbackTime) {
		t.Fatalf("When=%v want=%v (fallback time when Date is zero)", got.When, fallbackTime)
	}
}

func Test_CreateAuthorSignature_NilRequest_ReturnsFallback(t *testing.T) {
	t.Parallel()

	fallback := &object.Signature{
		Name:  "Fallback",
		Email: "fallback@test.com",
	}
	now := time.Now()

	got := CreateAuthorSignature(nil, fallback, now)

	if got.Name != "Fallback" {
		t.Fatalf("Name=%q want=%q", got.Name, "Fallback")
	}
}

func Test_CreateAuthorSignature_NilAuthor_ReturnsFallback(t *testing.T) {
	t.Parallel()

	req := &MergeRequest{
		SourceRef: "feature",
		TargetRef: "main",
		Author:    nil,
	}
	fallback := &object.Signature{
		Name:  "Default",
		Email: "default@test.com",
	}
	now := time.Now()

	got := CreateAuthorSignature(req, fallback, now)

	if got.Name != "Default" {
		t.Fatalf("Name=%q want=%q", got.Name, "Default")
	}
}

func Test_CreateAuthorSignature_WithAuthor_UsesAuthorValues(t *testing.T) {
	t.Parallel()

	authorDate := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	req := &MergeRequest{
		SourceRef: "feature",
		TargetRef: "main",
		Author: &AuthorInfo{
			Name:  "Request Author",
			Email: "request@test.com",
			Date:  authorDate,
		},
	}
	now := time.Now()

	got := CreateAuthorSignature(req, nil, now)

	if got.Name != "Request Author" {
		t.Fatalf("Name=%q want=%q", got.Name, "Request Author")
	}
	if got.Email != "request@test.com" {
		t.Fatalf("Email=%q want=%q", got.Email, "request@test.com")
	}
	if !got.When.Equal(authorDate) {
		t.Fatalf("When=%v want=%v", got.When, authorDate)
	}
}

func Test_CreateCommitterSignature_NilCommitter_UsesAuthor(t *testing.T) {
	t.Parallel()

	author := &object.Signature{
		Name:  "Author Name",
		Email: "author@test.com",
		When:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	req := &MergeRequest{
		Committer: nil,
	}
	now := time.Now()

	got := CreateCommitterSignature(req, author, now)

	if got.Name != "Author Name" {
		t.Fatalf("Name=%q want=%q (should copy from author)", got.Name, "Author Name")
	}
	if got.Email != "author@test.com" {
		t.Fatalf("Email=%q want=%q", got.Email, "author@test.com")
	}
}

func Test_CreateCommitterSignature_WithCommitter_UsesCommitterValues(t *testing.T) {
	t.Parallel()

	committerDate := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	req := &MergeRequest{
		Committer: &AuthorInfo{
			Name:  "Committer Name",
			Email: "committer@test.com",
			Date:  committerDate,
		},
	}
	now := time.Now()

	got := CreateCommitterSignature(req, nil, now)

	if got.Name != "Committer Name" {
		t.Fatalf("Name=%q want=%q", got.Name, "Committer Name")
	}
	if got.Email != "committer@test.com" {
		t.Fatalf("Email=%q want=%q", got.Email, "committer@test.com")
	}
}

func Test_CreateRebaseAuthorSignature_NilAuthor_PreservesOriginal(t *testing.T) {
	t.Parallel()

	original := object.Signature{
		Name:  "Original Author",
		Email: "original@test.com",
		When:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	req := &MergeRequest{
		Author: nil,
	}
	now := time.Now()

	got := CreateRebaseAuthorSignature(req, original, now)

	if got.Name != "Original Author" {
		t.Fatalf("Name=%q want=%q", got.Name, "Original Author")
	}
	if got.Email != "original@test.com" {
		t.Fatalf("Email=%q want=%q", got.Email, "original@test.com")
	}
}

func Test_CreateRebaseAuthorSignature_ZeroDate_PreservesOriginalDate(t *testing.T) {
	t.Parallel()

	originalDate := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	original := object.Signature{
		Name:  "Original",
		Email: "original@test.com",
		When:  originalDate,
	}
	req := &MergeRequest{
		Author: &AuthorInfo{
			Name:  "New Author",
			Email: "new@test.com",
			Date:  time.Time{}, // zero
		},
	}
	now := time.Now()

	got := CreateRebaseAuthorSignature(req, original, now)

	if got.Name != "New Author" {
		t.Fatalf("Name=%q want=%q", got.Name, "New Author")
	}
	if !got.When.Equal(originalDate) {
		t.Fatalf("When=%v want=%v (should preserve original date)", got.When, originalDate)
	}
}

func Test_CreateRebaseCommitterSignature_NilCommitter_UsesCurrentTime(t *testing.T) {
	t.Parallel()

	author := object.Signature{
		Name:  "Author",
		Email: "author@test.com",
		When:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	req := &MergeRequest{
		Committer: nil,
	}
	now := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)

	got := CreateRebaseCommitterSignature(req, author, now)

	if got.Name != "Author" {
		t.Fatalf("Name=%q want=%q", got.Name, "Author")
	}
	if !got.When.Equal(now) {
		t.Fatalf("When=%v want=%v (rebase should use current time)", got.When, now)
	}
}
