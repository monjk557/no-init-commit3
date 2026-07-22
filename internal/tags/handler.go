package tags

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/events"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/protocol"
	"github.com/collabops/git-executor/internal/protocol/gitcmd"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
)

// userIDHeader carries the acting user, forwarded by the gateway/orchestrator.
// Same header the git-push path reads (ReceivePackHandler).
const userIDHeader = "X-USER-ID"

type TagsHandler struct {
	pathConfig   *repositories.PathConfig
	eventsClient events.RedisStreamClient
}

func NewTagsHandler(cfg *config.Config) (*TagsHandler, error) {
	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	// Web tag create/delete must emit the same tag_push / tag_delete events as
	// the git-push path, so downstream consumers see web-driven tags too
	// (previously only pushed tags emitted). Best-effort: a missing events client
	// degrades to no-op, exactly like the push path.
	var eventsClient events.RedisStreamClient
	if cfg.Events.Enabled {
		logger, lerr := logging.NewLogger(cfg.Logging)
		if lerr != nil {
			return nil, fmt.Errorf("failed to create logger: %w", lerr)
		}
		eventsClient = events.NewLazyRedisStreamClient(cfg.Events, logger.Logger)
	}

	return &TagsHandler{pathConfig: pathConfig, eventsClient: eventsClient}, nil
}

// invalidBindErr 는 c.ShouldBindJSON 실패를 details 포함 응답으로 변환하기
// 위한 wrapper. routes.go 의 ErrorMapper 가 이 타입을 인식해서 legacy 와
// 동일한 {success, error: "Invalid request body", details: <err.Error>} 응답을
// 만든다.
type invalidBindErr struct {
	inner error
}

func (e *invalidBindErr) Error() string { return e.inner.Error() }

// HandleGetTags 는 GET /repos/{owner}/{repo}/tags 를 처리한다.
//
// Typed handler — limit/skip/sort 는 GetTagsRequest query struct 로 바인딩
// (RegisterGetWithQuery, 브랜치 list 와 동일 패턴). 단순 4xx 는
// api.NewCoded, GetTags 실패는 raw error 로 반환하여 ErrorMapper 가
// details 필드를 첨부.
func (h *TagsHandler) HandleGetTags(c *gin.Context, req GetTagsRequest) (TagsResponse, error) {
	owner := c.Param("owner")
	repo := c.Param("repo")

	limitStr := req.Limit
	skipStr := req.Skip
	searchQuery := req.Search

	// sort: "newest" (기본) | "oldest" | "name" — 브랜치 list 와 동일.
	sortParam := req.Sort
	if sortParam == "" {
		sortParam = "newest"
	}
	sort := ParseTagSort(sortParam)

	limit := DefaultTagsLimit
	if limitStr != "" {
		parsedLimit, err := strconv.Atoi(limitStr)
		if err != nil || parsedLimit < MinTagsLimit {
			return TagsResponse{}, api.NewCoded(api.ErrCodeInvalidInput, fmt.Sprintf("invalid limit: must be >= %d", MinTagsLimit))
		}
		if parsedLimit > MaxTagsLimit {
			limit = MaxTagsLimit
		} else {
			limit = parsedLimit
		}
	}

	skip := 0
	if skipStr != "" {
		parsedSkip, err := strconv.Atoi(skipStr)
		if err != nil || parsedSkip < 0 {
			return TagsResponse{}, api.NewCoded(api.ErrCodeInvalidInput, "invalid skip: must be >= 0")
		}
		skip = parsedSkip
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return TagsResponse{}, api.NewCoded(api.ErrCodeRepositoryNotFound)
	}

	tagList, totalCount, err := GetTagsSorted(c.Request.Context(), repoPath, limit, skip, searchQuery, sort)
	if err != nil {
		return TagsResponse{}, err
	}

	if tagList == nil {
		tagList = []Tag{}
	}

	currentPage := 1
	if limit > 0 {
		currentPage = skip/limit + 1
	}

	pagination := buildPagination(totalCount, currentPage, limit, c.Request.URL.String())

	return TagsResponse{
		Success: true,
		Message: "Tags fetched successfully",
		Results: tagList,
		Pagination: PaginationInfo{
			Count:       totalCount,
			Next:        pagination.Next,
			Previous:    pagination.Previous,
			CurrentPage: currentPage,
			TotalPages:  pagination.TotalPages,
			PageSize:    limit,
		},
	}, nil
}

// HandleCreateTag 는 POST /repos/{owner}/{repo}/tags 를 처리한다.
//
// Typed handler — bind error 의 사용자 친화 메시지/details 형식 보존을 위해
// 본문에서 c.ShouldBindJSON 직접 수행 (lenient bind archetype). 다중 status
// 매핑 (409 already exists, 404 not found, 400 default) 은 routes.go 의
// ErrorMapper 가 담당.
func (h *TagsHandler) HandleCreateTag(c *gin.Context) (CreateTagResponse, error) {
	owner := c.Param("owner")
	repo := c.Param("repo")

	var req CreateTagRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return CreateTagResponse{}, &invalidBindErr{inner: err}
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return CreateTagResponse{}, api.NewCoded(api.ErrCodeRepositoryNotFound)
	}

	tag, err := CreateTag(c.Request.Context(), repoPath, &req)
	if err != nil {
		return CreateTagResponse{}, err
	}

	// Mirror the git-push path: emit a tag_push event so downstream consumers
	// see web-created tags too.
	h.publishTagPush(c.Request.Context(), repoPath, owner, repo, tag.Name, tag.Hash, c.GetHeader(userIDHeader))

	return CreateTagResponse{Success: true, Tag: tag}, nil
}

// HandleDeleteTag 는 DELETE /repos/{owner}/{repo}/tags/*tag 를 처리한다.
//
// Typed handler — wildcard `*tag` 의 leading `/` 를 strings.TrimPrefix 로
// 제거 (legacy 와 동일). 다중 status 매핑 (404 not found, 400 default) 은
// routes.go 의 ErrorMapper 가 담당.
func (h *TagsHandler) HandleDeleteTag(c *gin.Context) (DeleteTagResponse, error) {
	owner := c.Param("owner")
	repo := c.Param("repo")
	tagName := strings.TrimPrefix(protocol.SafeParam(c, "tag"), "/")

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return DeleteTagResponse{}, api.NewCoded(api.ErrCodeRepositoryNotFound)
	}

	ctx := c.Request.Context()
	// Capture the tag's underlying commit BEFORE deletion so the tag_delete
	// event carries a commit SHA (consistent with the push path). Best-effort.
	oldCommit, _ := gitcmd.PeelToCommit(ctx, repoPath, "refs/tags/"+tagName)

	if err := DeleteTag(ctx, repoPath, tagName); err != nil {
		return DeleteTagResponse{}, err
	}

	h.publishTagDelete(owner, repo, tagName, oldCommit, c.GetHeader(userIDHeader))

	return DeleteTagResponse{Success: true, Tag: tagName}, nil
}

// HandleBulkDeleteTags 는 POST /repos/{owner}/{repo}/tags/bulk-delete 를
// 처리한다. 여러 태그를 한 번에 삭제하며, 개별 실패는 응답 내부에 기록되고
// HTTP status 는 항상 200.
//
// Typed handler — bind error 의 사용자 친화 메시지 보존을 위해 lenient bind
// archetype (본문에서 c.ShouldBindJSON 직접). 부분 실패는 error 가 아닌 정상
// 응답으로 반환되므로 ErrorMapper 불필요.
func (h *TagsHandler) HandleBulkDeleteTags(c *gin.Context) (BulkDeleteTagsResponse, error) {
	owner := c.Param("owner")
	repo := c.Param("repo")

	var req BulkDeleteTagsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return BulkDeleteTagsResponse{}, api.NewCoded(api.ErrCodeInvalidInput, "invalid request body: tags array is required")
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		return BulkDeleteTagsResponse{}, api.NewCoded(api.ErrCodeRepositoryNotFound)
	}

	ctx := c.Request.Context()
	userID := c.GetHeader(userIDHeader)
	results := make([]BulkDeleteTagResult, 0, len(req.Tags))
	succeeded := 0
	failed := 0

	for _, tagName := range req.Tags {
		// Capture the commit before deletion so each tag_delete event carries a
		// commit SHA (consistent with the push path). Best-effort.
		oldCommit, _ := gitcmd.PeelToCommit(ctx, repoPath, "refs/tags/"+tagName)

		if delErr := DeleteTag(ctx, repoPath, tagName); delErr != nil {
			results = append(results, BulkDeleteTagResult{
				TagName: tagName,
				Success: false,
				Error:   delErr.Error(),
			})
			failed++
			continue
		}

		results = append(results, BulkDeleteTagResult{
			TagName: tagName,
			Success: true,
		})
		succeeded++

		h.publishTagDelete(owner, repo, tagName, oldCommit, userID)
	}

	return BulkDeleteTagsResponse{
		Success:   failed == 0,
		Total:     len(req.Tags),
		Succeeded: succeeded,
		Failed:    failed,
		Results:   results,
	}, nil
}
