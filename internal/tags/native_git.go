package tags

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/gitutil"
)

const nativeGitTimeout = 5 * time.Second

// TagSortField는 태그 정렬 기준을 나타냅니다. 값 자체가 git for-each-ref 의
// --sort 플래그입니다 (브랜치의 BranchSortField 와 동일한 패턴).
type TagSortField string

const (
	// TagSortNewest는 최신 생성일 순 정렬입니다 (기본값).
	TagSortNewest TagSortField = "-creatordate"
	// TagSortOldest는 오래된 생성일 순 정렬입니다.
	TagSortOldest TagSortField = "creatordate"
	// TagSortName은 이름 오름차순 정렬입니다.
	TagSortName TagSortField = "refname"
)

// ParseTagSort는 쿼리 파라미터 문자열을 TagSortField로 변환합니다.
// 지원: "newest" (기본), "oldest", "name" — 브랜치 list 의 ParseBranchSort 와
// 동일한 파라미터 패턴.
func ParseTagSort(s string) TagSortField {
	switch s {
	case "oldest":
		return TagSortOldest
	case "name":
		return TagSortName
	default:
		return TagSortNewest
	}
}

// NativeGetTagsSorted는 native git으로 태그 목록을 정렬하여 조회합니다.
// go-git의 repo.References() + repo.TagObject() 조합을 대체합니다.
// search 가 비어 있지 않으면 페이지네이션 직전에 태그 이름 부분일치(대소문자
// 무시)로 필터링하고 totalCount 를 필터 결과 기준으로 재계산합니다 (브랜치
// list 와 동일).
func NativeGetTagsSorted(ctx context.Context, repoPath string, limit, skip int, search string, sortBy TagSortField) ([]Tag, int, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, nativeGitTimeout)
	defer cancel()

	// 정렬 플래그 결정 — TagSortField 값이 곧 --sort 플래그. zero value 는
	// 기본 정렬(최신순)로 폴백.
	sortFlag := string(sortBy)
	if sortFlag == "" {
		sortFlag = string(TagSortNewest)
	}

	// format: name\tsha\ttype\tderef_hash\tdate\ttagger_name\ttagger_email\tmessage
	// NOTE: Using tab separator instead of %x00 because Apple Git's for-each-ref
	// does not interpret %x00 as null byte (unlike git log --format).
	format := "%(refname:short)\t%(objectname)\t%(objecttype)\t%(*objectname)\t%(creatordate:iso8601-strict)\t%(taggername)\t%(taggeremail)\t%(contents:subject)"

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath,
		"for-each-ref", "refs/tags/",
		"--format="+format,
		"--sort="+sortFlag)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return nil, 0, fmt.Errorf("git for-each-ref failed: %s: %w", strings.TrimSpace(stderr.String()), err)
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return []Tag{}, 0, nil
	}

	lines := strings.Split(raw, "\n")

	allTags := make([]Tag, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		tag, err := parseTagRefLine(line)
		if err != nil {
			continue
		}
		allTags = append(allTags, tag)
	}

	// 검색 필터 — 페이지네이션 전에 적용하여 totalCount 가 필터 결과를
	// 반영하도록 한다 (브랜치 list 와 동일한 순서: filter → count → slice).
	if search != "" {
		searchLower := strings.ToLower(search)
		filtered := make([]Tag, 0, len(allTags))
		for _, t := range allTags {
			if strings.Contains(strings.ToLower(t.Name), searchLower) {
				filtered = append(filtered, t)
			}
		}
		allTags = filtered
	}

	totalCount := len(allTags)

	// 페이지네이션 적용
	if skip > 0 && skip < totalCount {
		allTags = allTags[skip:]
	} else if skip >= totalCount {
		allTags = []Tag{}
	}

	if limit > 0 && limit < len(allTags) {
		allTags = allTags[:limit]
	}

	return allTags, totalCount, nil
}

// NativeVerifyCommitExists checks if a commit exists in the repository.
func NativeVerifyCommitExists(ctx context.Context, repoPath, hash string) error {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "cat-file", "-t", hash)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := gitutil.OutputWithLimit(cmd, gitutil.DefaultMaxOutput)
	if err != nil {
		return fmt.Errorf("commit not found: %s", hash)
	}
	objType := strings.TrimSpace(string(out))
	if objType != "commit" {
		return fmt.Errorf("object %s is a %s, not a commit", hash, objType)
	}
	return nil
}

// NativeCheckTagExists checks if a tag exists.
func NativeCheckTagExists(ctx context.Context, repoPath, tagName string) (bool, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	refName := "refs/tags/" + tagName
	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "rev-parse", "--verify", refName)
	err := cmd.Run()
	if err != nil {
		return false, nil
	}
	return true, nil
}

// NativeCreateLightweightTag creates a lightweight tag pointing to a commit.
func NativeCreateLightweightTag(ctx context.Context, repoPath, tagName, hash string) error {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	refName := "refs/tags/" + tagName
	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "update-ref", refName, hash)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create tag: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return nil
}

// NativeCreateAnnotatedTag creates an annotated tag with message and tagger info.
func NativeCreateAnnotatedTag(ctx context.Context, repoPath, tagName, hash, message, taggerName, taggerEmail string, taggerDate time.Time) error {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "tag", "-a", tagName, hash, "-m", message)
	cmd.Env = append(cmd.Environ(),
		"GIT_COMMITTER_NAME="+taggerName,
		"GIT_COMMITTER_EMAIL="+taggerEmail,
		"GIT_COMMITTER_DATE="+taggerDate.Format(time.RFC3339),
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create annotated tag: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return nil
}

// NativeDeleteTag deletes a tag reference.
func NativeDeleteTag(ctx context.Context, repoPath, tagName string) error {
	cmdCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	refName := "refs/tags/" + tagName
	cmd := exec.CommandContext(cmdCtx, "git", "--git-dir", repoPath, "update-ref", "-d", refName)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to delete tag: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return nil
}

// parseTagRefLine은 git for-each-ref의 null-separated 출력을 Tag로 파싱합니다.
func parseTagRefLine(line string) (Tag, error) {
	parts := strings.SplitN(line, "\t", 8)
	if len(parts) < 8 {
		return Tag{}, fmt.Errorf("unexpected format: got %d fields", len(parts))
	}

	name := parts[0]
	hash := parts[1]
	objType := parts[2]
	derefHash := parts[3]
	dateStr := parts[4]
	taggerName := parts[5]
	taggerEmail := parts[6]
	message := parts[7]

	isAnnotated := objType == "tag"

	tag := Tag{
		Name:        name,
		IsAnnotated: isAnnotated,
	}

	if isAnnotated {
		// Annotated tag: derefHash(*objectname)가 커밋을 가리킴
		if derefHash != "" {
			tag.Hash = derefHash
		} else {
			tag.Hash = hash
		}
		tag.Message = message

		date, _ := time.Parse(time.RFC3339, dateStr)
		tag.Tagger = &Tagger{
			Name:  taggerName,
			Email: taggerEmail,
			Date:  date,
		}
	} else {
		// Lightweight tag: hash가 직접 커밋을 가리킴
		tag.Hash = hash

		// creatordate는 lightweight tag의 경우 커밋 날짜
		date, _ := time.Parse(time.RFC3339, dateStr)
		if !date.IsZero() {
			tag.Tagger = &Tagger{
				Date: date,
			}
		}
	}

	return tag, nil
}
