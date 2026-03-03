package tags

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrTagAlreadyExists is returned when trying to create a tag that already exists
var ErrTagAlreadyExists = errors.New("tag already exists")

// SortOrder defines the sort order for tags
type SortOrder string

const (
	SortByName SortOrder = "name"
	SortByDate SortOrder = "date"
)

// GetTags는 저장소의 모든 태그를 가져옵니다 (페이지네이션 지원).
// native git을 사용합니다.
func GetTags(ctx context.Context, repoPath string, limit, skip int) ([]Tag, int, error) {
	return GetTagsSorted(ctx, repoPath, limit, skip, SortByName)
}

// GetTagsSorted는 저장소의 모든 태그를 정렬하여 가져옵니다.
// native git for-each-ref를 사용합니다.
func GetTagsSorted(ctx context.Context, repoPath string, limit, skip int, sortBy SortOrder) ([]Tag, int, error) {
	return NativeGetTagsSorted(ctx, repoPath, limit, skip, sortBy)
}

// getTagDate returns the date of a tag (tagger date or zero time)
func getTagDate(tag Tag) time.Time {
	if tag.Tagger != nil {
		return tag.Tagger.Date
	}
	return time.Time{}
}

// buildPagination은 페이지네이션 정보를 생성합니다.
func buildPagination(totalCount, currentPage, pageSize int, baseURL string) PaginationInfo {
	totalPages := (totalCount + pageSize - 1) / pageSize

	var next, previous *string

	if currentPage < totalPages {
		nextURL := fmt.Sprintf("%s?page=%d&page_size=%d", baseURL, currentPage+1, pageSize)
		next = &nextURL
	}

	if currentPage > 1 {
		prevURL := fmt.Sprintf("%s?page=%d&page_size=%d", baseURL, currentPage-1, pageSize)
		previous = &prevURL
	}

	return PaginationInfo{
		Count:       totalCount,
		Next:        next,
		Previous:    previous,
		CurrentPage: currentPage,
		TotalPages:  totalPages,
		PageSize:    pageSize,
	}
}

// CreateTag creates a tag (Lightweight or Annotated) using native git.
func CreateTag(ctx context.Context, repoPath string, req *CreateTagRequest) (*Tag, error) {
	// Validate hash format
	if len(req.Hash) < 7 || len(req.Hash) > 40 {
		return nil, fmt.Errorf("invalid tag hash: %s", req.Hash)
	}

	// Verify that the target commit exists
	if err := NativeVerifyCommitExists(ctx, repoPath, req.Hash); err != nil {
		return nil, err
	}

	// Check if tag already exists (return 409 Conflict)
	exists, err := NativeCheckTagExists(ctx, repoPath, req.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to check tag existence: %w", err)
	}
	if exists {
		return nil, ErrTagAlreadyExists
	}

	// Create Annotated Tag if requested
	if req.Annotated {
		return createAnnotatedTag(ctx, repoPath, req)
	}

	// Create Lightweight Tag (default)
	return createLightweightTag(ctx, repoPath, req.Name, req.Hash)
}

// createLightweightTag creates a lightweight tag using native git.
func createLightweightTag(ctx context.Context, repoPath, tagName, hash string) (*Tag, error) {
	if err := NativeCreateLightweightTag(ctx, repoPath, tagName, hash); err != nil {
		return nil, err
	}

	return &Tag{
		Name:        tagName,
		Hash:        hash,
		IsAnnotated: false,
	}, nil
}

// createAnnotatedTag creates an annotated tag using native git.
func createAnnotatedTag(ctx context.Context, repoPath string, req *CreateTagRequest) (*Tag, error) {
	// Set default tagger if not provided
	tagger := req.Tagger
	if tagger == nil {
		tagger = &Tagger{
			Name:  "Git Executor",
			Email: "git-executor@collabops.dev",
			Date:  time.Now(),
		}
	}

	// Set default message if not provided
	message := req.Message
	if message == "" {
		message = fmt.Sprintf("Tag %s", req.Name)
	}

	if err := NativeCreateAnnotatedTag(ctx, repoPath, req.Name, req.Hash, message, tagger.Name, tagger.Email, tagger.Date); err != nil {
		return nil, err
	}

	return &Tag{
		Name:        req.Name,
		Hash:        req.Hash,
		IsAnnotated: true,
		Message:     message,
		Tagger:      tagger,
	}, nil
}

// DeleteTag deletes a tag using native git.
func DeleteTag(ctx context.Context, repoPath, tagName string) error {
	// Check if tag exists
	exists, err := NativeCheckTagExists(ctx, repoPath, tagName)
	if err != nil {
		return fmt.Errorf("failed to check tag existence: %w", err)
	}
	if !exists {
		return fmt.Errorf("tag not found: %s", tagName)
	}

	return NativeDeleteTag(ctx, repoPath, tagName)
}
