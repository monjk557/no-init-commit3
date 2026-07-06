package context

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/collabops/git-executor/internal/logging"
	"go.uber.org/zap"
)

// isWorkflowYAML: 워크플로우 파일(.yaml / .yml)인지 판별.
func isWorkflowYAML(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}

// Service: bare repo 컨텍스트 조회 서비스
type Service struct {
	logger *logging.Logger
}

// NewService: 새 컨텍스트 서비스 생성
func NewService(logger *logging.Logger) *Service {
	return &Service{
		logger: logger,
	}
}

// GetContext: bare repo에서 diff 파일 목록 및 워크플로우 내용 조회
func (s *Service) GetContext(ctx context.Context, repoPath, oldRev, newRev string) (*ContextResponse, error) {
	// 1. SHA 존재 검증 (null SHA는 최초 push이므로 스킵)
	if !isNullSHA(oldRev) {
		if err := nativeValidateSHA(ctx, repoPath, oldRev); err != nil {
			return nil, fmt.Errorf("old_rev validation failed: %w", err)
		}
	}
	if err := nativeValidateSHA(ctx, repoPath, newRev); err != nil {
		return nil, fmt.Errorf("new_rev validation failed: %w", err)
	}

	// 2. diff 파일 목록 조회
	diffFiles, err := nativeDiffFiles(ctx, repoPath, oldRev, newRev)
	if err != nil {
		return nil, fmt.Errorf("diff failed: %w", err)
	}

	// 3. .collabops/workflows/*.yaml 파일 조회 (newRev 트리 기준)
	workflows, wfErr := s.readWorkflows(ctx, repoPath, newRev)
	if wfErr != nil {
		s.logger.Warn("Failed to read workflows from bare repo",
			zap.String("repo_path", repoPath),
			zap.String("new_rev", newRev),
			zap.Error(wfErr),
		)
		workflows = make(map[string]string)
	}

	// 4. 삭제된 워크플로우 목록 계산 (oldRev 에 있었으나 newRev 에서 사라진 파일).
	// newRev 워크플로우를 신뢰할 수 없으면(wfErr) 오탐(전부 삭제로 오인) 방지 위해 skip.
	var deletedWorkflows []string
	if wfErr == nil {
		deletedWorkflows = s.computeDeletedWorkflows(ctx, repoPath, oldRev, workflows)
	} else {
		deletedWorkflows = []string{}
	}

	return &ContextResponse{
		Success:          true,
		DiffFiles:        diffFiles,
		Workflows:        workflows,
		DeletedWorkflows: deletedWorkflows,
	}, nil
}

// computeDeletedWorkflows: oldRev 워크플로우 파일명 중 currentWorkflows(newRev)에
// 없는 것을 삭제로 판정해 정렬된 basename 목록으로 반환합니다.
//
// oldRev 가 null SHA(최초 push)면 이전 트리가 없으므로 삭제 없음. oldRev 트리 조회
// 실패 시에도 오탐 방지를 위해 빈 목록을 반환합니다(best-effort).
func (s *Service) computeDeletedWorkflows(
	ctx context.Context, repoPath, oldRev string, currentWorkflows map[string]string,
) []string {
	if isNullSHA(oldRev) {
		return []string{}
	}

	oldNames, err := s.listWorkflowNames(ctx, repoPath, oldRev)
	if err != nil {
		s.logger.Warn("Failed to list old workflow names for deletion diff",
			zap.String("repo_path", repoPath),
			zap.String("old_rev", oldRev),
			zap.Error(err),
		)
		return []string{}
	}

	deleted := make([]string, 0)
	for name := range oldNames {
		if _, stillPresent := currentWorkflows[name]; !stillPresent {
			deleted = append(deleted, name)
		}
	}
	sort.Strings(deleted)
	return deleted
}

// listWorkflowNames: 특정 커밋의 .collabops/workflows/ 하위 워크플로우 파일명(basename)
// 집합을 반환합니다. 내용은 읽지 않아 readWorkflows 보다 가볍습니다.
func (s *Service) listWorkflowNames(ctx context.Context, repoPath, sha string) (map[string]struct{}, error) {
	names := make(map[string]struct{})

	files, err := nativeListTreeFiles(ctx, repoPath, sha, ".collabops/workflows/")
	if err != nil {
		return names, err
	}

	for _, filePath := range files {
		name := filepath.Base(filePath)
		if isWorkflowYAML(name) {
			names[name] = struct{}{}
		}
	}

	return names, nil
}

// readWorkflows: bare repo에서 .collabops/workflows/*.yaml 파일 읽기
func (s *Service) readWorkflows(ctx context.Context, repoPath, sha string) (map[string]string, error) {
	workflows := make(map[string]string)

	files, err := nativeListTreeFiles(ctx, repoPath, sha, ".collabops/workflows/")
	if err != nil {
		return workflows, err
	}

	for _, filePath := range files {
		name := filepath.Base(filePath)
		if !isWorkflowYAML(name) {
			continue
		}

		content, err := nativeShowFile(ctx, repoPath, sha, filePath)
		if err != nil {
			s.logger.Warn("Failed to read workflow file",
				zap.String("file", filePath),
				zap.Error(err),
			)
			continue
		}
		workflows[name] = content
	}

	return workflows, nil
}
