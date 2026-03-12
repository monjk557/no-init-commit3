package context

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/collabops/git-executor/internal/logging"
	"go.uber.org/zap"
)

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

	// 3. .collabops/workflows/*.yaml 파일 조회
	workflows, err := s.readWorkflows(ctx, repoPath, newRev)
	if err != nil {
		s.logger.Warn("Failed to read workflows from bare repo",
			zap.String("repo_path", repoPath),
			zap.String("new_rev", newRev),
			zap.Error(err),
		)
		workflows = make(map[string]string)
	}

	return &ContextResponse{
		Success:   true,
		DiffFiles: diffFiles,
		Workflows: workflows,
	}, nil
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
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
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
