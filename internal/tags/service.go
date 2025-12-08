package tags

import (
	"fmt"
	"time"

	"github.com/collabops/git-executor/internal/repositories"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// GetTags는 저장소의 모든 태그를 가져옵니다 (페이지네이션 지원).
func GetTags(repoWrapper *repositories.RepositoryWrapper, limit, skip int) ([]Tag, int, error) {
	repo := repoWrapper.GetRepo()
	refs, err := repo.References()
	if err != nil {
		return nil, 0, err
	}
	defer refs.Close()

	var allTags []Tag

	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Name().IsTag() {
			tagName := ref.Name().Short()
			tagHash := ref.Hash()

			// Annotated tag인지 확인
			tagObj, err := repo.TagObject(tagHash)
			isAnnotated := err == nil

			tag := Tag{
				Name:        tagName,
				Hash:        tagHash.String(),
				IsAnnotated: isAnnotated,
			}

			// Annotated tag인 경우 추가 정보 설정
			if isAnnotated && tagObj != nil {
				tag.Message = tagObj.Message
				tag.Hash = tagObj.Target.String() // 태그가 가리키는 커밋 해시
				tag.Tagger = &Tagger{
					Name:  tagObj.Tagger.Name,
					Email: tagObj.Tagger.Email,
					Date:  tagObj.Tagger.When,
				}
			}

			allTags = append(allTags, tag)
		}
		return nil
	})

	if err != nil {
		return nil, 0, fmt.Errorf("failed to iterate references: %w", err)
	}

	totalCount := len(allTags)

	// 페이지네이션 적용
	if skip > 0 && skip < totalCount {
		allTags = allTags[skip:]
	}

	if limit > 0 && limit < len(allTags) {
		allTags = allTags[:limit]
	}

	return allTags, totalCount, nil
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

// CreateTag creates a tag (Lightweight or Annotated)
func CreateTag(repoWrapper *repositories.RepositoryWrapper, req *CreateTagRequest) (*Tag, error) {
	repo := repoWrapper.GetRepo()

	// Validate hash
	hash := plumbing.NewHash(req.Hash)
	if hash.IsZero() {
		return nil, fmt.Errorf("invalid tag hash: %s", req.Hash)
	}

	// Verify that the target commit exists
	_, err := repo.CommitObject(hash)
	if err != nil {
		return nil, fmt.Errorf("commit not found: %s", req.Hash)
	}

	tagRefName := plumbing.ReferenceName(fmt.Sprintf("refs/tags/%s", req.Name))

	// Create Annotated Tag if requested
	if req.Annotated {
		return createAnnotatedTag(repoWrapper, tagRefName, req, hash)
	}

	// Create Lightweight Tag (default)
	return createLightweightTag(repoWrapper, tagRefName, req.Name, hash)
}

// createLightweightTag는 Lightweight 태그를 생성합니다 (커밋을 직접 가리킴)
func createLightweightTag(repo *repositories.RepositoryWrapper, tagRefName plumbing.ReferenceName, tagName string, hash plumbing.Hash) (*Tag, error) {
	gitRepo := repo.GetRepo()

	// 커밋을 직접 가리키는 태그 참조 생성
	tagRef := plumbing.NewHashReference(tagRefName, hash)

	// 저장소에 태그 참조 설정
	if err := gitRepo.Storer.SetReference(tagRef); err != nil {
		return nil, fmt.Errorf("failed to create tag: %w", err)
	}

	return &Tag{
		Name:        tagName,
		Hash:        hash.String(),
		IsAnnotated: false,
	}, nil
}

// createAnnotatedTag는 메시지와 작성자 정보가 포함된 Annotated 태그를 생성합니다
func createAnnotatedTag(repo *repositories.RepositoryWrapper, tagRefName plumbing.ReferenceName, req *CreateTagRequest, targetHash plumbing.Hash) (*Tag, error) {
	gitRepo := repo.GetRepo()

	// 작성자 정보가 제공되지 않으면 기본값 설정
	tagger := req.Tagger
	if tagger == nil {
		tagger = &Tagger{
			Name:  "Git Executor",
			Email: "git-executor@collabops.dev",
			Date:  time.Now(),
		}
	}

	// 메시지가 제공되지 않으면 기본 메시지 설정
	message := req.Message
	if message == "" {
		message = fmt.Sprintf("Tag %s", req.Name)
	}

	// TagObject 생성
	tagObj := &object.Tag{
		Name:       req.Name,
		Tagger:     object.Signature{Name: tagger.Name, Email: tagger.Email, When: tagger.Date},
		Message:    message,
		TargetType: plumbing.CommitObject,
		Target:     targetHash,
	}

	// 태그 객체 인코딩 및 저장
	encodedTag := gitRepo.Storer.NewEncodedObject()
	if err := tagObj.Encode(encodedTag); err != nil {
		return nil, fmt.Errorf("failed to encode tag object: %w", err)
	}

	tagHash, err := gitRepo.Storer.SetEncodedObject(encodedTag)
	if err != nil {
		return nil, fmt.Errorf("failed to store tag object: %w", err)
	}

	// 태그 객체를 가리키는 태그 참조 생성
	tagRef := plumbing.NewHashReference(tagRefName, tagHash)
	if err := gitRepo.Storer.SetReference(tagRef); err != nil {
		return nil, fmt.Errorf("failed to create tag reference: %w", err)
	}

	return &Tag{
		Name:        req.Name,
		Hash:        targetHash.String(), // 태그가 가리키는 커밋 해시
		IsAnnotated: true,
		Message:     message,
		Tagger:      tagger,
	}, nil
}

// UpdateTag는 태그를 업데이트합니다 (Lightweight와 Annotated 간 변환 가능)
func UpdateTag(repoWrapper *repositories.RepositoryWrapper, tagName string, req *UpdateTagRequest) (*Tag, error) {
	gitRepo := repoWrapper.GetRepo()

	// 해시 검증
	hash := plumbing.NewHash(req.Hash)
	if hash.IsZero() {
		return nil, fmt.Errorf("invalid tag hash: %s", req.Hash)
	}

	// 대상 커밋이 존재하는지 확인
	_, err := gitRepo.CommitObject(hash)
	if err != nil {
		return nil, fmt.Errorf("commit not found: %s", req.Hash)
	}

	// Check if tag exists
	tagRefName := plumbing.ReferenceName(fmt.Sprintf("refs/tags/%s", tagName))
	existingRef, err := gitRepo.Reference(tagRefName, false)
	if err != nil {
		return nil, fmt.Errorf("tag not found: %s", tagName)
	}

	// 기존 태그가 Annotated 태그인지 확인
	existingTagObj, err := gitRepo.TagObject(existingRef.Hash())
	isAnnotated := err == nil

	// Annotated 태그로 생성할지 결정
	shouldBeAnnotated := req.Annotated
	if shouldBeAnnotated && (req.Message != "" || req.Tagger != nil) {
		// Annotated 태그로 생성 또는 업데이트
		return updateAnnotatedTag(gitRepo, tagRefName, tagName, req, hash, isAnnotated, existingTagObj)
	}

	// Lightweight 태그로 업데이트 (또는 Annotated에서 Lightweight로 변환)
	// 기존 태그가 Annotated였던 경우
	if isAnnotated {
		// 참고: 태그 객체를 실제로 삭제할 수는 없지만, 참조를 커밋을 직접 가리키도록 업데이트하여
		// 효과적으로 Lightweight 태그로 변환할 수 있습니다
	}

	newTagRef := plumbing.NewHashReference(tagRefName, hash)
	if err := gitRepo.Storer.SetReference(newTagRef); err != nil {
		return nil, fmt.Errorf("failed to update tag: %w", err)
	}

	return &Tag{
		Name:        tagName,
		Hash:        hash.String(),
		IsAnnotated: false,
	}, nil
}

// updateAnnotatedTag는 Annotated 태그를 업데이트하거나 생성합니다
func updateAnnotatedTag(repo *gogit.Repository, tagRefName plumbing.ReferenceName, tagName string, req *UpdateTagRequest, targetHash plumbing.Hash, wasAnnotated bool, existingTagObj *object.Tag) (*Tag, error) {
	// 제공되지 않으면 기존 작성자/메시지 사용
	tagger := req.Tagger
	message := req.Message

	if wasAnnotated && existingTagObj != nil {
		if tagger == nil {
			tagger = &Tagger{
				Name:  existingTagObj.Tagger.Name,
				Email: existingTagObj.Tagger.Email,
				Date:  existingTagObj.Tagger.When,
			}
		}
		if message == "" {
			message = existingTagObj.Message
		}
	}

	// 여전히 제공되지 않으면 기본값 설정
	if tagger == nil {
		tagger = &Tagger{
			Name:  "Git Executor",
			Email: "git-executor@collabops.dev",
			Date:  time.Now(),
		}
	}
	if message == "" {
		message = fmt.Sprintf("Tag %s", tagName)
	}

	// 새로운 TagObject 생성
	tagObj := &object.Tag{
		Name:       tagName,
		Tagger:     object.Signature{Name: tagger.Name, Email: tagger.Email, When: tagger.Date},
		Message:    message,
		TargetType: plumbing.CommitObject,
		Target:     targetHash,
	}

	// 태그 객체 인코딩 및 저장
	encodedTag := repo.Storer.NewEncodedObject()
	if err := tagObj.Encode(encodedTag); err != nil {
		return nil, fmt.Errorf("failed to encode tag object: %w", err)
	}

	tagHash, err := repo.Storer.SetEncodedObject(encodedTag)
	if err != nil {
		return nil, fmt.Errorf("failed to store tag object: %w", err)
	}

	// 새로운 태그 객체를 가리키도록 태그 참조 업데이트
	tagRef := plumbing.NewHashReference(tagRefName, tagHash)
	if err := repo.Storer.SetReference(tagRef); err != nil {
		return nil, fmt.Errorf("failed to update tag reference: %w", err)
	}

	return &Tag{
		Name:        tagName,
		Hash:        targetHash.String(),
		IsAnnotated: true,
		Message:     message,
		Tagger:      tagger,
	}, nil
}

// DeleteTag는 태그를 삭제합니다
func DeleteTag(repoWrapper *repositories.RepositoryWrapper, tagName string) error {
	repo := repoWrapper.GetRepo()

	// 태그 존재 여부 확인
	tagRefName := plumbing.ReferenceName(fmt.Sprintf("refs/tags/%s", tagName))
	_, err := repo.Reference(tagRefName, false)
	if err != nil {
		return fmt.Errorf("tag not found: %s", tagName)
	}

	// 태그 참조 삭제
	if err := repo.Storer.RemoveReference(tagRefName); err != nil {
		return fmt.Errorf("failed to delete tag: %w", err)
	}

	return nil
}
