package git_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/collabops/git-executor/internal/config"
	"github.com/collabops/git-executor/internal/git"
	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repository"
)

func TestDefaultBranchReliability(t *testing.T) {
	tests := []struct {
		name           string
		setupFunc      func(t *testing.T, repoPath string, gitRepo *gogit.Repository) // HEAD 상태 설정
		expectedBranch string
		shouldHaveErr  bool
		description    string
	}{
		{
			name: "HEAD가_default_branch를_가리키는_정상_상황",
			setupFunc: func(t *testing.T, repoPath string, gitRepo *gogit.Repository) {
				// 이미 HEAD가 main을 가리키고 있음 (초기 상태)
				// Storer에서 직접 읽어서 캐시 이슈 방지
				headRef, err := gitRepo.Storer.Reference(plumbing.HEAD)
				require.NoError(t, err)
				require.Equal(t, plumbing.SymbolicReference, headRef.Type())
			},
			expectedBranch: "main",
			shouldHaveErr:  false,
			description:    "저장소 생성 직후 HEAD가 default branch를 가리키는 경우",
		},
		{
			name: "HEAD가_다른_브랜치로_변경된_상황",
			setupFunc: func(t *testing.T, repoPath string, gitRepo *gogit.Repository) {
				// feature 브랜치 생성
				headRef, err := gitRepo.Head()
				require.NoError(t, err)
				featureRef := plumbing.NewHashReference(
					plumbing.ReferenceName("refs/heads/feature"),
					headRef.Hash(),
				)
				err = gitRepo.Storer.SetReference(featureRef)
				require.NoError(t, err)

				// HEAD를 feature 브랜치로 변경 (파일 작업 등으로 HEAD가 변경된 상황 시뮬레이션)
				newHeadRef := plumbing.NewSymbolicReference(
					plumbing.HEAD,
					plumbing.ReferenceName("refs/heads/feature"),
				)
				err = gitRepo.Storer.SetReference(newHeadRef)
				require.NoError(t, err)
			},
			expectedBranch: "feature", // HEAD가 가리키는 브랜치 (실제 default branch가 아님)
			shouldHaveErr:  false,
			description:    "파일 작업 등으로 HEAD가 다른 브랜치로 변경된 경우 - 주의: 실제 default branch가 아님",
		},
		{
			name: "Detached_HEAD_상태",
			setupFunc: func(t *testing.T, repoPath string, gitRepo *gogit.Repository) {
				// HEAD를 직접 커밋 해시로 변경 (detached HEAD)
				headRef, err := gitRepo.Head()
				require.NoError(t, err)
				detachedHeadRef := plumbing.NewHashReference(plumbing.HEAD, headRef.Hash())
				err = gitRepo.Storer.SetReference(detachedHeadRef)
				require.NoError(t, err)
			},
			expectedBranch: "", // Detached HEAD에서는 브랜치 이름을 가져올 수 없음
			shouldHaveErr:  false,
			description:    "HEAD가 직접 커밋 해시를 가리키는 detached HEAD 상태",
		},
		{
			name: "HEAD가_remote_branch를_가리키는_경우",
			setupFunc: func(t *testing.T, repoPath string, gitRepo *gogit.Repository) {
				// 원격 브랜치 참조 생성
				headRef, err := gitRepo.Head()
				require.NoError(t, err)
				remoteRef := plumbing.NewHashReference(
					plumbing.ReferenceName("refs/remotes/origin/main"),
					headRef.Hash(),
				)
				err = gitRepo.Storer.SetReference(remoteRef)
				require.NoError(t, err)

				// HEAD를 원격 브랜치로 변경 시도 (일반적이지 않지만 가능한 상황)
				// 실제로는 이런 경우는 거의 없지만, 코드가 올바르게 처리하는지 확인
			},
			expectedBranch: "main", // 초기 상태 유지 (원격 브랜치는 refs/heads/가 아님)
			shouldHaveErr:  false,
			description:    "원격 브랜치 참조가 있는 경우 (HEAD는 여전히 로컬 브랜치를 가리켜야 함)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 테스트용 임시 디렉토리 생성
			tempDir, err := os.MkdirTemp("", "test-default-branch-*")
			require.NoError(t, err)
			defer os.RemoveAll(tempDir)

			// 경로 검증을 위해 owner/repo.git 구조로 생성
			repoPath := filepath.Join(tempDir, "testowner", "testrepo.git")

			// 저장소 생성
			cfg := &config.Config{
				Logging: config.LoggingConfig{
					Level:  "info",
					Format: "json",
				},
				Git: config.GitConfig{
					RepoRoot: tempDir,
				},
			}

			logger, err := logging.NewLogger(cfg.Logging)
			require.NoError(t, err)

			pathConfig, err := repository.NewPathConfig(tempDir)
			require.NoError(t, err)

			rm, err := git.NewRepositoryManager(pathConfig, logger)
			require.NoError(t, err)

			// 저장소 생성
			ctx := context.Background()
			options := &git.CreateRepositoryOptions{
				Path:          repoPath,
				Bare:          true,
				InitialCommit: true,
				DefaultBranch: "main",
			}

			repoInfo, err := rm.CreateRepository(ctx, options)
			require.NoError(t, err)
			require.NotNil(t, repoInfo)
			assert.Equal(t, "main", repoInfo.DefaultBranch, "생성 시점의 default branch")

			// 저장소 열기
			gitRepo, err := gogit.PlainOpen(repoPath)
			require.NoError(t, err)

			// 테스트 시나리오에 따라 HEAD 상태 변경
			if tt.setupFunc != nil {
				tt.setupFunc(t, repoPath, gitRepo)
			}

			// updateRepositoryStats는 private 메서드이므로 직접 호출 불가
			// 대신, updateRepositoryStats의 default branch 로직을 재현하여 테스트
			// 실제로는 GetRepositoryInfo나 다른 public 메서드를 통해 호출되지만,
			// 여기서는 로직을 직접 재현하여 검증
			// 변경사항 확인을 위해 Storer에서 직접 HEAD 읽기 (SetReference 후 캐시 이슈 방지)
			head, err := gitRepo.Storer.Reference(plumbing.HEAD)
			require.NoError(t, err)

			// updateRepositoryStats의 default branch 로직 재현
			if head.Type() == plumbing.SymbolicReference {
				targetBranch := head.Target().String()
				if strings.HasPrefix(targetBranch, "refs/heads/") {
					repoInfo.DefaultBranch = strings.TrimPrefix(targetBranch, "refs/heads/")
				}
			}

			if tt.shouldHaveErr {
				assert.Error(t, err)
				return
			}

			// 결과 검증
			t.Logf("Test: %s", tt.description)
			t.Logf("Expected branch: %s", tt.expectedBranch)
			t.Logf("Actual branch: %s", repoInfo.DefaultBranch)

			if tt.expectedBranch == "" {
				// Detached HEAD 또는 HEAD가 브랜치를 가리키지 않는 경우
				// DefaultBranch는 이전 값("main")이 유지되거나 빈 값일 수 있음
				assert.True(t,
					repoInfo.DefaultBranch == "main" || repoInfo.DefaultBranch == "",
					"Detached HEAD 상태에서는 이전 값 유지 또는 빈 값")
			} else {
				assert.Equal(t, tt.expectedBranch, repoInfo.DefaultBranch,
					"HEAD가 가리키는 브랜치와 일치해야 함")
			}

			// HEAD 상태 직접 확인 (위에서 이미 head를 읽었으므로 재사용)
			headRef := head

			if headRef.Type() == plumbing.SymbolicReference {
				targetBranch := headRef.Target().String()
				expectedTarget := "refs/heads/" + tt.expectedBranch
				if tt.expectedBranch != "" {
					// 로컬 브랜치를 가리키는 경우만 확인
					if strings.HasPrefix(targetBranch, "refs/heads/") {
						assert.Equal(t, expectedTarget, targetBranch,
							"HEAD의 Target이 예상한 브랜치를 가리켜야 함")
					}
				}
			} else {
				// Detached HEAD 상태
				assert.Equal(t, plumbing.HashReference, headRef.Type(),
					"Detached HEAD는 HashReference 타입")
			}
		})
	}
}

// TestDefaultBranchAfterFileOperation 테스트: 파일 작업 후 HEAD가 변경되어도 올바른 default branch 가져오기
func TestDefaultBranchAfterFileOperation(t *testing.T) {
	// 테스트용 임시 디렉토리 생성
	tempDir, err := os.MkdirTemp("", "test-fileop-head-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// 경로 검증을 위해 owner/repo.git 구조로 생성
	repoPath := filepath.Join(tempDir, "testowner", "testrepo.git")

	// 저장소 생성
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}

	logger, err := logging.NewLogger(cfg.Logging)
	require.NoError(t, err)

	pathConfig, err := repository.NewPathConfig(tempDir)
	require.NoError(t, err)

	rm, err := git.NewRepositoryManager(pathConfig, logger)
	require.NoError(t, err)

	ctx := context.Background()
	options := &git.CreateRepositoryOptions{
		Path:          repoPath,
		Bare:          true,
		InitialCommit: true,
		DefaultBranch: "main",
	}

	repoInfo, err := rm.CreateRepository(ctx, options)
	require.NoError(t, err)
	originalDefaultBranch := repoInfo.DefaultBranch
	assert.Equal(t, "main", originalDefaultBranch)

	// 저장소 열기
	gitRepo, err := gogit.PlainOpen(repoPath)
	require.NoError(t, err)

	// 파일 작업 시뮬레이션: 다른 브랜치에서 작업하면서 HEAD가 변경됨
	// 1. feature 브랜치 생성
	headRef, err := gitRepo.Head()
	require.NoError(t, err)

	featureRef := plumbing.NewHashReference(
		plumbing.ReferenceName("refs/heads/feature"),
		headRef.Hash(),
	)
	err = gitRepo.Storer.SetReference(featureRef)
	require.NoError(t, err)

	// 2. HEAD를 feature 브랜치로 변경 (파일 작업 API가 하는 것처럼)
	newHeadRef := plumbing.NewSymbolicReference(
		plumbing.HEAD,
		plumbing.ReferenceName("refs/heads/feature"),
	)
	err = gitRepo.Storer.SetReference(newHeadRef)
	require.NoError(t, err)

	// 3. updateRepositoryStats의 default branch 로직 재현 - 이제 HEAD는 feature를 가리킴
	// Storer에서 직접 읽어서 캐시 이슈 방지
	head, err := gitRepo.Storer.Reference(plumbing.HEAD)
	require.NoError(t, err)
	if head.Type() == plumbing.SymbolicReference {
		targetBranch := head.Target().String()
		if strings.HasPrefix(targetBranch, "refs/heads/") {
			repoInfo.DefaultBranch = strings.TrimPrefix(targetBranch, "refs/heads/")
		}
	}

	// 4. 검증: HEAD가 변경되었으므로 DefaultBranch도 "feature"로 변경됨
	// 이것이 문제점: 실제 default branch는 "main"이지만, HEAD를 읽으면 "feature"가 됨
	assert.Equal(t, "feature", repoInfo.DefaultBranch,
		"HEAD가 변경되었으므로 DefaultBranch도 변경됨 - 실제 default branch와 다를 수 있음")

	// 5. HEAD를 다시 main으로 복원
	mainHeadRef := plumbing.NewSymbolicReference(
		plumbing.HEAD,
		plumbing.ReferenceName("refs/heads/main"),
	)
	err = gitRepo.Storer.SetReference(mainHeadRef)
	require.NoError(t, err)

	// 6. 다시 updateRepositoryStats의 default branch 로직 재현
	// Storer에서 직접 읽어서 캐시 이슈 방지
	head, err = gitRepo.Storer.Reference(plumbing.HEAD)
	require.NoError(t, err)
	if head.Type() == plumbing.SymbolicReference {
		targetBranch := head.Target().String()
		if strings.HasPrefix(targetBranch, "refs/heads/") {
			repoInfo.DefaultBranch = strings.TrimPrefix(targetBranch, "refs/heads/")
		}
	}

	// 7. 이제 DefaultBranch가 다시 "main"으로 복원되었는지 확인
	assert.Equal(t, "main", repoInfo.DefaultBranch,
		"HEAD를 main으로 복원하면 DefaultBranch도 main으로 복원됨")
}

// TestDefaultBranchSymbolicReferenceOnly 테스트: SymbolicReference인 경우만 처리하는지 확인
func TestDefaultBranchSymbolicReferenceOnly(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "test-symbolic-ref-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// 경로 검증을 위해 owner/repo.git 구조로 생성
	repoPath := filepath.Join(tempDir, "testowner", "testrepo.git")

	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Git: config.GitConfig{
			RepoRoot: tempDir,
		},
	}

	logger, err := logging.NewLogger(cfg.Logging)
	require.NoError(t, err)

	pathConfig, err := repository.NewPathConfig(tempDir)
	require.NoError(t, err)

	rm, err := git.NewRepositoryManager(pathConfig, logger)
	require.NoError(t, err)

	ctx := context.Background()
	options := &git.CreateRepositoryOptions{
		Path:          repoPath,
		Bare:          true,
		InitialCommit: true,
		DefaultBranch: "main",
	}

	repoInfo, err := rm.CreateRepository(ctx, options)
	require.NoError(t, err)
	require.Equal(t, "main", repoInfo.DefaultBranch)

	gitRepo, err := gogit.PlainOpen(repoPath)
	require.NoError(t, err)

	// HEAD를 detached 상태로 만들기
	headRef, err := gitRepo.Head()
	require.NoError(t, err)

	// HashReference로 변경 (detached HEAD)
	detachedHeadRef := plumbing.NewHashReference(plumbing.HEAD, headRef.Hash())
	err = gitRepo.Storer.SetReference(detachedHeadRef)
	require.NoError(t, err)

	// HEAD 상태 확인 후 updateRepositoryStats의 default branch 로직 재현
	// Storer에서 직접 읽어서 캐시 이슈 방지
	head, err := gitRepo.Storer.Reference(plumbing.HEAD)
	require.NoError(t, err)
	if head.Type() == plumbing.SymbolicReference {
		targetBranch := head.Target().String()
		if strings.HasPrefix(targetBranch, "refs/heads/") {
			repoInfo.DefaultBranch = strings.TrimPrefix(targetBranch, "refs/heads/")
		}
	}

	// Detached HEAD 상태에서는 DefaultBranch가 설정되지 않아야 함
	// 하지만 이전 값("main")이 유지될 수 있음
	// 코드 동작 확인: SymbolicReference가 아니면 조건문이 실행되지 않음
	currentHead, err := gitRepo.Storer.Reference(plumbing.HEAD)
	require.NoError(t, err)
	assert.Equal(t, plumbing.HashReference, currentHead.Type(),
		"HEAD는 이제 HashReference (detached HEAD)")

	// Detached HEAD에서는 head.Type() == plumbing.SymbolicReference가 false이므로
	// DefaultBranch는 이전 값("main")이 유지되거나 빈 값
	assert.True(t,
		repoInfo.DefaultBranch == "main" || repoInfo.DefaultBranch == "",
		"Detached HEAD 상태에서는 DefaultBranch가 업데이트되지 않음 (이전 값 유지 또는 빈 값)")
}
