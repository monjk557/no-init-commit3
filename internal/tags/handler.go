package tags

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/protocol"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
)

type TagsHandler struct {
	pathConfig *repositories.PathConfig
}

func NewTagsHandler(cfg *config.Config) (*TagsHandler, error) {
	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}
	return &TagsHandler{pathConfig: pathConfig}, nil
}

func (h *TagsHandler) HandleGetTags(c *gin.Context) {
	owner := c.Param("owner")
	repo := c.Param("repo")

	// 파라미터 추출 및 검증
	limitStr := c.Query("limit")
	skipStr := c.Query("skip")

	// limit 파라미터 파싱 및 검증
	limit := DefaultTagsLimit
	if limitStr != "" {
		parsedLimit, err := strconv.Atoi(limitStr)
		if err != nil || parsedLimit < MinTagsLimit {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": fmt.Sprintf("invalid limit: must be >= %d", MinTagsLimit)})
			return
		}
		if parsedLimit > MaxTagsLimit {
			limit = MaxTagsLimit
		} else {
			limit = parsedLimit
		}
	}

	// skip 파라미터 파싱 및 검증
	skip := 0
	if skipStr != "" {
		parsedSkip, err := strconv.Atoi(skipStr)
		if err != nil || parsedSkip < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid skip: must be >= 0"})
			return
		}
		skip = parsedSkip
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "Repository not found"})
		return
	}

	tags, totalCount, err := GetTags(c.Request.Context(), repoPath, limit, skip)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "Failed to get tags", "details": err.Error()})
		return
	}

	// Ensure tags is never nil - return empty array instead of null
	if tags == nil {
		tags = []Tag{}
	}

	// 페이지네이션 정보 계산
	currentPage := 1
	if limit > 0 {
		currentPage = skip/limit + 1
	}

	// 페이지네이션 정보와 함께 응답 생성
	response := TagsResponse{
		Success: true,
		Message: "Tags fetched successfully",
		Results: tags,
		Pagination: PaginationInfo{
			Count:       totalCount,
			Next:        buildPagination(totalCount, currentPage, limit, c.Request.URL.String()).Next,
			Previous:    buildPagination(totalCount, currentPage, limit, c.Request.URL.String()).Previous,
			CurrentPage: currentPage,
			TotalPages:  buildPagination(totalCount, currentPage, limit, c.Request.URL.String()).TotalPages,
			PageSize:    limit,
		},
	}

	c.JSON(http.StatusOK, response)
}

func (h *TagsHandler) HandleCreateTag(c *gin.Context) {
	owner := c.Param("owner")
	repo := c.Param("repo")

	var req CreateTagRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "Invalid request body", "details": err.Error()})
		return
	}

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "Repository not found"})
		return
	}

	tag, err := CreateTag(c.Request.Context(), repoPath, &req)
	if err != nil {
		// 에러 타입에 따라 적절한 상태 코드 선택
		if errors.Is(err, ErrTagAlreadyExists) {
			c.JSON(http.StatusConflict, gin.H{
				"success": false,
				"error":   "Failed to create tag",
				"details": "tag already exists",
			})
			return
		}

		errMsg := err.Error()
		if strings.Contains(errMsg, "not found") || strings.Contains(errMsg, "commit not found") {
			c.JSON(http.StatusNotFound, gin.H{
				"success": false,
				"error":   "Failed to create tag",
				"details": errMsg,
			})
			return
		}

		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "Failed to create tag",
			"details": errMsg,
		})
		return
	}

	response := CreateTagResponse{
		Success: true,
		Tag:     tag,
	}
	c.JSON(http.StatusOK, response)
}

func (h *TagsHandler) HandleDeleteTag(c *gin.Context) {
	owner := c.Param("owner")
	repo := c.Param("repo")
	tagName := strings.TrimPrefix(protocol.SafeParam(c, "tag"), "/")

	repoPath, err := repositories.GetAndValidateRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "Repository not found"})
		return
	}

	err = DeleteTag(c.Request.Context(), repoPath, tagName)
	if err != nil {
		// 에러 메시지에 따라 적절한 상태 코드 선택
		statusCode := http.StatusBadRequest
		errMsg := err.Error()
		if strings.Contains(errMsg, "not found") {
			statusCode = http.StatusNotFound
		}
		c.JSON(statusCode, gin.H{"success": false, "error": "Failed to delete tag", "details": errMsg})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "tag": tagName})
}
