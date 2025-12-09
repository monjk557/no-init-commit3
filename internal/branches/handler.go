// Package protocol은 Git REST API 엔드포인트의 브랜치 작업을 제공합니다.
// 브랜치 생성, 업데이트, 삭제 등의 Git 브랜치 관리 기능을 구현합니다.
package branches

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// BranchOperationsHandler는 Git 브랜치 작업을 처리합니다.
// 브랜치 생성, 업데이트, 삭제 요청을 처리하고 적절한 응답을 제공합니다.
type BranchOperationsHandler struct {
	config             *config.Config
	logger             *logging.Logger
	pathConfig         *repositories.PathConfig
	pathValidator      *repositories.GitAPIPathValidator
	repoManager        *repositories.DefaultRepositoryManager
	objectManager      *repositories.GitObjectManager
	responseMiddleware *api.JSONResponseMiddleware
	errorMapper        *api.GitErrorMapper
	branchNamePattern  *regexp.Regexp
	service            *BranchService
}

// NewBranchOperationsHandler는 새로운 브랜치 작업 핸들러를 생성합니다.
// 설정과 권한 클라이언트를 사용하여 필요한 컴포너트들을 초기화합니다.
func NewBranchOperationsHandler(cfg *config.Config) (*BranchOperationsHandler, error) {
	logger, err := logging.NewLogger(cfg.Logging)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	// Create path configuration
	pathConfig, err := repositories.NewPathConfig(cfg.Git.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create path config: %w", err)
	}

	// Create path validator
	pathValidator := repositories.NewGitAPIPathValidator(pathConfig)

	// Create repository manager
	repoManager, err := repositories.NewRepositoryManager(pathConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create repository manager: %w", err)
	}

	// Create object manager
	objectManager, err := repositories.NewGitObjectManager(pathConfig, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create object manager: %w", err)
	}

	// Create response middleware
	responseMiddleware := api.NewJSONResponseMiddleware(logger, "v1")

	// Create error mapper
	errorMapper := api.NewGitErrorMapper()

	// Compile branch name pattern
	branchNamePattern, err := regexp.Compile(`^[a-zA-Z0-9_/-]+$`)
	if err != nil {
		return nil, fmt.Errorf("failed to compile branch name pattern: %w", err)
	}

	// Create branch service
	service := NewBranchService(logger)

	return &BranchOperationsHandler{
		config:             cfg,
		logger:             logger,
		pathConfig:         pathConfig,
		pathValidator:      pathValidator,
		repoManager:        repoManager,
		objectManager:      objectManager,
		responseMiddleware: responseMiddleware,
		errorMapper:        errorMapper,
		branchNamePattern:  branchNamePattern,
		service:            service,
	}, nil
}

// repositoryContext는 저장소 관련 공통 컨텍스트를 담는 구조체입니다.
type repositoryContext struct {
	owner       string
	repo        string
	repoPath    string
	repoWrapper *repositories.RepositoryWrapper
}

// validateAndOpenRepository는 저장소 검증 및 열기를 수행하는 공통 로직입니다.
func (h *BranchOperationsHandler) validateAndOpenRepository(ctx context.Context, c *gin.Context, owner, repo string) (*repositoryContext, bool) {
	// 경로 구성 요소 검증
	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Invalid path: %v", err),
		})
		return nil, false
	}

	// 저장소 경로 가져오기
	repoPath, err := repositories.GetRepoPath(h.pathConfig, owner, repo)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return nil, false
	}

	// 저장소 열기 (존재 여부는 OpenRepository에서 확인)
	repoWrapper, err := h.repoManager.OpenRepository(repoPath)
	if err != nil {
		// repository not found 또는 corrupted
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "repository not found"})
		return nil, false
	}

	return &repositoryContext{
		owner:       owner,
		repo:        repo,
		repoPath:    repoPath,
		repoWrapper: repoWrapper,
	}, true
}

// HandleCreateBranch handles POST /repos/{owner}/{repo}/branches for creating new branches
func (h *BranchOperationsHandler) HandleCreateBranch(c *gin.Context) {
	ctx := c.Request.Context()

	// Extract URL parameters
	owner := c.Param("owner")
	repo := c.Param("repo")

	h.logger.Info("Processing branch creation request",
		zap.String("owner", owner),
		zap.String("repo", repo))

	// Parse and validate JSON request
	var req CreateBranchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Invalid JSON: %v", err),
		})
		return
	}

	// Validate branch name
	if err := h.validateBranchName(req.Name); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Invalid branch name: %v", err),
		})
		return
	}

	// 공통 검증 및 저장소 열기
	repoCtx, ok := h.validateAndOpenRepository(ctx, c, owner, repo)
	if !ok {
		return
	}
	defer func() { _ = repoCtx.repoWrapper.Close() }()

	repository := repoCtx.repoWrapper.GetRepo()

	// Call service to create branch
	result, err := h.service.CreateBranch(repository, req)
	if err != nil {
		// Determine appropriate status code based on error message
		statusCode := http.StatusInternalServerError
		if strings.Contains(err.Error(), "already exists") {
			statusCode = http.StatusBadRequest
		} else if strings.Contains(err.Error(), "failed to resolve") {
			statusCode = http.StatusBadRequest
		}
		c.JSON(statusCode, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	response := CreateBranchResponse{
		Success: true,
		Data: CreateBranchData{
			BranchName:  result.BranchName,
			CommitSHA:   result.CommitSHA,
			Source:      result.Source,
			RefPath:     result.RefPath,
			IsNewBranch: result.IsNewBranch,
			Message:     result.Message,
			CreatedAt:   result.CreatedAt,
			Details: CreateBranchDetails{
				Checkout:  result.Details.Checkout,
				ForceUsed: result.Details.ForceUsed,
				Author:    result.Details.Author,
			},
		},
	}

	// Send success response
	c.JSON(http.StatusOK, response)
}

// HandleUpdateBranch handles PATCH /repos/{owner}/{repo}/branches/{branch} for updating branch pointers
func (h *BranchOperationsHandler) HandleUpdateBranch(c *gin.Context) {
	ctx := c.Request.Context()

	// Extract URL parameters
	owner := c.Param("owner")
	repo := c.Param("repo")

	// Extract branch name from wildcard parameter (remove leading slash)
	branchName := c.Param("branch")
	branchName = strings.TrimPrefix(branchName, "/")

	h.logger.Info("Processing branch update request",
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.String("branch", branchName))

	// Parse and validate JSON request
	var req UpdateBranchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Invalid JSON: %v", err),
		})
		return
	}

	// Validate that either target or new_name is provided, but not both
	hasTarget := req.Target != ""
	hasNewName := req.NewName != ""
	if !hasTarget && !hasNewName {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "Either 'target' (for commit update) or 'new_name' (for rename) must be provided",
		})
		return
	}
	if hasTarget && hasNewName {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "Cannot provide both 'target' and 'new_name'. Use 'target' for commit update or 'new_name' for rename",
		})
		return
	}

	// Validate branch name
	if err := h.validateBranchName(branchName); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Invalid branch name: %v", err),
		})
		return
	}

	// Validate new branch name if rename operation
	if hasNewName {
		if err := h.validateBranchName(req.NewName); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   fmt.Sprintf("Invalid new branch name: %v", err),
			})
			return
		}
	}

	// 공통 검증 및 저장소 열기
	repoCtx, ok := h.validateAndOpenRepository(ctx, c, owner, repo)
	if !ok {
		return
	}
	defer func() { _ = repoCtx.repoWrapper.Close() }()

	repository := repoCtx.repoWrapper.GetRepo()

	// Call service to update branch
	result, err := h.service.UpdateBranch(repository, branchName, req)
	if err != nil {
		// Determine appropriate status code based on error message
		statusCode := http.StatusInternalServerError
		if strings.Contains(err.Error(), "does not exist") {
			statusCode = http.StatusNotFound
		} else if strings.Contains(err.Error(), "cannot be the same") || strings.Contains(err.Error(), "already exists") {
			statusCode = http.StatusBadRequest
		} else if strings.Contains(err.Error(), "already exists") && strings.Contains(err.Error(), "force=true") {
			statusCode = http.StatusConflict
		} else if strings.Contains(err.Error(), "not a fast-forward") || strings.Contains(err.Error(), "must be provided") {
			statusCode = http.StatusBadRequest
		}
		c.JSON(statusCode, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// Prepare response data
	responseData := map[string]interface{}{
		"branch_name":   result.BranchName,
		"commit_hash":   result.CommitHash,
		"previous_hash": result.PreviousHash,
		"operation":     result.Operation,
		"fast_forward":  result.FastForward,
	}

	// Only include "forced" field if currentCommit != targetCommit (v1 compatibility)
	// When currentCommit == targetCommit, v1 omits the "forced" field
	if result.PreviousHash != result.CommitHash {
		responseData["forced"] = result.Forced
	}

	// Add rename-specific fields
	if result.Operation == "rename" {
		responseData["old_branch_name"] = result.OldBranchName
		responseData["new_branch_name"] = result.NewBranchName
		responseData["ref_path"] = result.RefPath
		responseData["head_updated"] = result.HeadUpdated
	}

	if result.Message != "" {
		responseData["message"] = result.Message
	}

	// Send success response
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    responseData,
	})
}

func (h *BranchOperationsHandler) HandleDeleteBranch(c *gin.Context) {
	ctx := c.Request.Context()

	// Extract URL parameters
	owner := c.Param("owner")
	repo := c.Param("repo")

	// Extract branch name from wildcard parameter (remove leading slash)
	branchName := c.Param("branch")
	branchName = strings.TrimPrefix(branchName, "/")

	h.logger.Info("Processing branch deletion request",
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.String("branch", branchName),
		zap.String("raw_branch_param", c.Param("branch")),
		zap.String("request_path", c.Request.URL.Path))

	// Validate branch name
	if err := h.validateBranchName(branchName); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Invalid branch name: %v", err),
		})
		return
	}

	// 공통 검증 및 저장소 열기
	repoCtx, ok := h.validateAndOpenRepository(ctx, c, owner, repo)
	if !ok {
		return
	}
	defer func() { _ = repoCtx.repoWrapper.Close() }()

	repository := repoCtx.repoWrapper.GetRepo()

	// Call service to delete branch
	result, err := h.service.DeleteBranch(repository, branchName)
	if err != nil {
		// Determine appropriate status code based on error message
		statusCode := http.StatusInternalServerError
		if strings.Contains(err.Error(), "does not exist") {
			statusCode = http.StatusNotFound
		}
		c.JSON(statusCode, gin.H{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// Prepare response data in the format expected by tests
	responseData := map[string]interface{}{
		"branch_name": result.BranchName,
		"commit_hash": result.CommitHash,
		"operation":   "delete",
	}

	// Prepare full response
	response := map[string]interface{}{
		"success": true,
		"data":    responseData,
	}

	// Send success response
	c.JSON(http.StatusOK, response)
}
