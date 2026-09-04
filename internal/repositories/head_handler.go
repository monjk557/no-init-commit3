package repositories

import (
	"fmt"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/collabops/git-executor/internal/api"
	"github.com/collabops/git-executor/internal/gitutil"
)

// HandleSetDefaultBranch handles PUT /repos/{owner}/{repo}/head — 기존 브랜치를
// 저장소의 default branch 로 지정한다.
//
// bare 저장소의 default branch 는 HEAD 의 symbolic-ref 하나가 전부다. 그래서 이
// 핸들러가 하는 일도 ref 하나를 바꿔 쓰는 것뿐이고, 브랜치·객체는 전혀 건드리지
// 않는다. 브랜치를 새로 만들거나 rename 하는 기능이 아니다 — 대상 브랜치가 이미
// 있어야 한다.
//
// 존재 검증이 핵심이다: git 은 HEAD 가 없는 브랜치를 가리켜도 막지 않고, 그렇게
// 되면 clone 이 아무것도 체크아웃하지 못하는 저장소가 조용히 만들어진다.
func (h *RepositoryCreationHandler) HandleSetDefaultBranch(c *gin.Context) (SetDefaultBranchResponse, error) {
	owner := c.Param("owner")
	repo := c.Param("repo")

	if err := h.pathValidator.ValidateFilePath(fmt.Sprintf("%s/%s", owner, repo)); err != nil {
		return SetDefaultBranchResponse{}, api.NewCoded(api.ErrCodeInvalidInput, fmt.Sprintf("Invalid path: %v", err))
	}

	var req SetDefaultBranchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return SetDefaultBranchResponse{}, api.NewCoded(api.ErrCodeInvalidRequestBody, fmt.Sprintf("Invalid JSON: %v", err))
	}

	branch, err := normalizeBranchName(req.Branch)
	if err != nil {
		return SetDefaultBranchResponse{}, api.NewCoded(api.ErrCodeInvalidInput, err.Error())
	}

	repoPath := h.pathConfig.FormatRepoPath(owner, repo)
	if _, statErr := os.Stat(repoPath); os.IsNotExist(statErr) {
		return SetDefaultBranchResponse{}, api.NewCoded(api.ErrCodeRepositoryNotFound,
			fmt.Sprintf("Repository %s/%s not found", owner, repo))
	}

	// push 와 같은 write 락 — receive-pack 이 ref 를 갱신하는 도중 HEAD 를 바꾸면
	// 존재 검증과 실제 기록 사이에 브랜치가 사라질 수 있다.
	repoLock := GetRepoRWLock(repoPath)
	repoLock.Lock()
	defer repoLock.Unlock()

	ctx := c.Request.Context()
	refName := gitutil.BranchRefPrefix + branch
	if !gitutil.RefExists(ctx, repoPath, refName) {
		return SetDefaultBranchResponse{}, api.NewCoded(api.ErrCodeRefNotFound,
			fmt.Sprintf("Branch '%s' does not exist", branch))
	}

	// 이전 값은 응답에만 쓴다 — 읽기 실패로 요청을 실패시키지 않는다.
	previous := ""
	if head, headErr := gitutil.HeadRefName(ctx, repoPath); headErr == nil {
		previous = strings.TrimPrefix(head, gitutil.BranchRefPrefix)
	}

	if setErr := gitutil.SetHeadRef(ctx, repoPath, refName); setErr != nil {
		h.logger.Error("Failed to set default branch",
			zap.String("owner", owner),
			zap.String("repo", repo),
			zap.String("branch", branch),
			zap.Error(setErr))
		return SetDefaultBranchResponse{}, api.NewCoded(api.ErrCodeInternalError,
			fmt.Sprintf("Failed to set default branch: %v", setErr))
	}

	// 읽기 경로(overview/state)는 저장소 정보를 캐시한다 — HEAD 를 바꿨으면
	// 즉시 무효화해야 다음 조회가 새 default branch 를 본다.
	GetSimpleCache().Invalidate(repoPath)

	h.logger.Info("Default branch updated",
		zap.String("owner", owner),
		zap.String("repo", repo),
		zap.String("previous", previous),
		zap.String("default_branch", branch))

	return SetDefaultBranchResponse{
		Success:               true,
		Owner:                 owner,
		Name:                  repo,
		DefaultBranch:         branch,
		PreviousDefaultBranch: previous,
	}, nil
}

// normalizeBranchName 은 요청의 브랜치 값을 순수 브랜치 이름으로 정규화한다.
// `refs/heads/` 를 붙여 보내는 호출자도 받아주되, ref 경로로 해석될 수 있는
// 입력은 거른다. 실재 여부는 호출부의 show-ref 가 최종 판정한다.
func normalizeBranchName(raw string) (string, error) {
	branch := strings.TrimSpace(raw)
	branch = strings.TrimPrefix(branch, gitutil.BranchRefPrefix)
	switch {
	case branch == "":
		return "", fmt.Errorf("branch is required")
	case strings.HasPrefix(branch, "-"):
		// git 이 옵션으로 해석한다.
		return "", fmt.Errorf("invalid branch name: %q", raw)
	case strings.HasPrefix(branch, "refs/"):
		return "", fmt.Errorf("branch must be a branch name, not a ref path: %q", raw)
	}
	return branch, nil
}
