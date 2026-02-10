package commits

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/collabops/git-executor/internal/logging"
	"github.com/collabops/git-executor/internal/repositories"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"go.uber.org/zap"
)

// TransactionState는 트랜잭션의 상태를 나타냅니다
type TransactionState string

const (
	// TransactionStateActive - 트랜잭션이 활성 상태이며 작업을 받을 수 있음
	TransactionStateActive TransactionState = "active"
	// TransactionStateValidating - 트랜잭션이 검증 중
	TransactionStateValidating TransactionState = "validating"
	// TransactionStateCommitting - 트랜잭션이 커밋 중
	TransactionStateCommitting TransactionState = "committing"
	// TransactionStateCommitted - 트랜잭션이 성공적으로 커밋됨
	TransactionStateCommitted TransactionState = "committed"
	// TransactionStateRolledBack - 트랜잭션이 롤백됨
	TransactionStateRolledBack TransactionState = "rolled_back"
	// TransactionStateFailed - 트랜잭션이 실패함
	TransactionStateFailed TransactionState = "failed"
)

// FileOperation은 파일 작업 유형을 나타냅니다
type FileOperation string

const (
	// FileOperationCreate - 파일 생성
	FileOperationCreate FileOperation = "create"
	// FileOperationUpdate - 파일 업데이트
	FileOperationUpdate FileOperation = "update"
	// FileOperationDelete - 파일 삭제
	FileOperationDelete FileOperation = "delete"
	// FileOperationMove - 파일 이동/이름 변경
	FileOperationMove FileOperation = "move"
)

// TransactionOptions는 트랜잭션 동작을 설정합니다
type TransactionOptions struct {
	// Timeout은 트랜잭션의 최대 지속 시간
	Timeout time.Duration
	// ValidateOnly는 커밋 없이 검증만 수행
	ValidateOnly bool
	// AllowPartialCommit은 일부 작업이 실패해도 커밋을 허용
	AllowPartialCommit bool
	// MaxRetries는 최대 재시도 횟수
	MaxRetries int
}

// FileChange는 파일 변경 사항을 나타냅니다
type FileChange struct {
	// Path는 저장소 루트 기준 파일 경로
	Path string
	// Operation은 수행할 작업 유형
	Operation FileOperation
	// Content는 새 파일 내용 (create/update 작업용)
	Content []byte
	// Mode는 파일 모드 (선택사항, 기본값은 0644)
	Mode *filemode.FileMode
	// OriginalPath는 이름 변경 작업의 원본 경로
	OriginalPath string
}

// CommitInfo는 커밋 메타데이터를 나타냅니다
type CommitInfo struct {
	// Message는 커밋 메시지
	Message string
	// Author는 작성자 정보
	Author object.Signature
	// Committer는 커밋터 정보 (선택사항, 기본값은 Author)
	Committer *object.Signature
	// Parents는 부모 커밋 해시들
	Parents []plumbing.Hash
}

// CommitResult는 커밋 작업의 결과를 나타냅니다
type CommitResult struct {
	// Hash는 새 커밋 해시
	Hash plumbing.Hash
	// TreeHash는 트리 해시
	TreeHash plumbing.Hash
	// Message는 커밋 메시지
	Message string
	// Author는 커밋 작성자
	Author object.Signature
	// Committer는 커밋 커밋터
	Committer object.Signature
	// Parents는 부모 커밋 해시들
	Parents []plumbing.Hash
}

// TransactionOperation은 트랜잭션 내의 단일 작업을 나타냅니다
type TransactionOperation struct {
	// ID는 이 작업의 고유 식별자
	ID string
	// Type는 작업 유형
	Type FileOperation
	// FileChange는 파일 변경 사항
	FileChange FileChange
	// State는 이 작업의 현재 상태
	State TransactionState
	// Error는 이 작업 중 발생한 오류
	Error error
	// Timestamp는 이 작업이 추가된 시간
	Timestamp time.Time
}

// TransactionResult는 트랜잭션의 결과를 나타냅니다
type TransactionResult struct {
	// TransactionID는 이 트랜잭션의 고유 식별자
	TransactionID string
	// State는 트랜잭션의 최종 상태
	State TransactionState
	// CommitResult는 성공 시 커밋 정보를 포함
	CommitResult *CommitResult
	// Operations는 모든 작업에 대한 세부 정보
	Operations []*TransactionOperation
	// SuccessfulOperations는 성공한 작업의 ID 목록
	SuccessfulOperations []string
	// FailedOperations는 실패한 작업의 ID와 오류 맵
	FailedOperations map[string]error
	// StartTime는 트랜잭션이 시작된 시간
	StartTime time.Time
	// EndTime는 트랜잭션이 완료된 시간
	EndTime time.Time
	// Duration는 총 트랜잭션 지속 시간
	Duration time.Duration
}

// GetSuccessfulOperationCount는 성공한 작업 수를 반환합니다
func (tr *TransactionResult) GetSuccessfulOperationCount() int {
	return len(tr.SuccessfulOperations)
}

// GetFailedOperationCount는 실패한 작업 수를 반환합니다
func (tr *TransactionResult) GetFailedOperationCount() int {
	return len(tr.FailedOperations)
}

// Transaction은 원자적 다중 파일 작업을 나타냅니다
type Transaction struct {
	// ID는 고유 식별자
	ID string
	// State는 현재 트랜잭션 상태
	State TransactionState
	// Repository는 대상 Git 저장소
	Repository *repositories.RepositoryWrapper
	// BaseCommitHash는 이 트랜잭션의 기본 커밋
	BaseCommitHash plumbing.Hash
	// BaseTree는 이 트랜잭션의 기본 트리
	BaseTree *object.Tree
	// Operations는 이 트랜잭션의 모든 작업
	Operations []*TransactionOperation
	// CommitInfo는 커밋 메타데이터
	CommitInfo CommitInfo
	// Options는 트랜잭션 설정
	Options TransactionOptions
	// StartTime는 트랜잭션이 생성된 시간
	StartTime time.Time
	// TimeoutTimer는 트랜잭션 타임아웃을 추적
	TimeoutTimer *time.Timer
	// mutex는 동시 접근을 보호
	mutex sync.RWMutex
	// manager는 트랜잭션 매니저 참조
	manager *TransactionManager
}

// TransactionManager는 원자적 다중 파일 작업을 위한 Git 트랜잭션을 관리합니다
type TransactionManager struct {
	logger *logging.Logger
	// activeTransactions는 모든 활성 트랜잭션을 추적
	activeTransactions map[string]*Transaction
	// mutex는 activeTransactions에 대한 동시 접근을 보호
	mutex sync.RWMutex
	// defaultTimeout는 기본 트랜잭션 타임아웃
	defaultTimeout time.Duration
}

// NewTransactionManager는 새로운 트랜잭션 매니저를 생성합니다
func NewTransactionManager(logger *logging.Logger) *TransactionManager {
	return &TransactionManager{
		logger:             logger,
		activeTransactions: make(map[string]*Transaction),
		defaultTimeout:     5 * time.Minute, // 기본 5분 타임아웃
	}
}

// generateRandomID는 랜덤 ID를 생성합니다
func generateRandomID(length int) string {
	bytes := make([]byte, length/2)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(bytes)
}

// BeginTransaction은 새 트랜잭션을 시작합니다.
// baseCommit이 ZeroHash이면 root commit(첫 커밋)용으로 BaseTree 없이 시작합니다.
func (tm *TransactionManager) BeginTransaction(ctx context.Context, repo *repositories.RepositoryWrapper, baseCommit plumbing.Hash, commitInfo CommitInfo, options TransactionOptions) (*Transaction, error) {
	txnID := fmt.Sprintf("txn_%d_%s", time.Now().UnixNano(), generateRandomID(8))

	// 타임아웃이 지정되지 않은 경우 기본값 설정
	if options.Timeout == 0 {
		options.Timeout = tm.defaultTimeout
	}

	gitRepo := repo.GetRepo()
	if gitRepo == nil {
		return nil, fmt.Errorf("repository is closed")
	}

	var baseTree *object.Tree
	if baseCommit != plumbing.ZeroHash {
		commitObj, err := gitRepo.CommitObject(baseCommit)
		if err != nil {
			return nil, fmt.Errorf("failed to get base commit: %w", err)
		}
		baseTree, err = commitObj.Tree()
		if err != nil {
			return nil, fmt.Errorf("failed to get base tree: %w", err)
		}
	}
	// baseCommit == ZeroHash: root commit, BaseTree is nil

	txn := &Transaction{
		ID:             txnID,
		State:          TransactionStateActive,
		Repository:     repo,
		BaseCommitHash: baseCommit,
		BaseTree:       baseTree,
		Operations:     make([]*TransactionOperation, 0),
		CommitInfo:     commitInfo,
		Options:        options,
		StartTime:      time.Now(),
		manager:        tm,
	}

	// 타임아웃 타이머 설정
	txn.TimeoutTimer = time.AfterFunc(options.Timeout, func() {
		tm.logger.Warn("트랜잭션 타임아웃",
			zap.String("transaction_id", txnID),
			zap.Duration("timeout", options.Timeout))
		if rollbackErr := tm.RollbackTransaction(ctx, txn); rollbackErr != nil {
			tm.logger.Error("타임아웃된 트랜잭션 롤백 실패",
				zap.String("transaction_id", txnID),
				zap.Error(rollbackErr))
		}
	})

	// 트랜잭션 저장
	tm.mutex.Lock()
	tm.activeTransactions[txnID] = txn
	tm.mutex.Unlock()

	return txn, nil
}

// AddOperation은 트랜잭션에 작업을 추가합니다
func (tm *TransactionManager) AddOperation(ctx context.Context, txn *Transaction, operation TransactionOperation) error {
	txn.mutex.Lock()
	defer txn.mutex.Unlock()

	if txn.State != TransactionStateActive {
		return fmt.Errorf("cannot add operation to transaction in state: %s", txn.State)
	}

	// 작업 ID가 제공되지 않은 경우 생성
	if operation.ID == "" {
		operation.ID = fmt.Sprintf("op_%d_%s", len(txn.Operations), generateRandomID(6))
	}

	operation.State = TransactionStateActive
	operation.Timestamp = time.Now()

	tm.logger.Debug("트랜잭션에 작업 추가",
		zap.String("transaction_id", txn.ID),
		zap.String("operation_id", operation.ID),
		zap.String("operation_type", string(operation.Type)),
		zap.String("file_path", operation.FileChange.Path))

	txn.Operations = append(txn.Operations, &operation)
	return nil
}

// ValidateTransaction은 트랜잭션의 모든 작업을 검증합니다
func (tm *TransactionManager) ValidateTransaction(ctx context.Context, txn *Transaction) error {
	txn.mutex.Lock()
	defer txn.mutex.Unlock()

	if txn.State != TransactionStateActive {
		return fmt.Errorf("cannot validate transaction in state: %s", txn.State)
	}

	txn.State = TransactionStateValidating

	// root commit(BaseTree nil)인 경우 create 작업만 허용
	if txn.BaseTree == nil {
		for _, op := range txn.Operations {
			op.State = TransactionStateValidating
			if op.Type != FileOperationCreate {
				op.State = TransactionStateFailed
				op.Error = fmt.Errorf("root commit allows only create operations, got %s", op.Type)
				if !txn.Options.AllowPartialCommit {
					txn.State = TransactionStateFailed
					return op.Error
				}
			}
		}
		txn.State = TransactionStateActive
		return nil
	}

	// 개별 작업 검증
	for _, op := range txn.Operations {
		op.State = TransactionStateValidating

		// 필요한 경우 파일 존재 확인
		if op.Type == FileOperationUpdate || op.Type == FileOperationDelete {
			_, err := txn.BaseTree.File(op.FileChange.Path)
			if err != nil {
				op.State = TransactionStateFailed
				op.Error = fmt.Errorf("file not found for %s operation: %s", op.Type, op.FileChange.Path)
				if !txn.Options.AllowPartialCommit {
					txn.State = TransactionStateFailed
					return op.Error
				}
			}
		}

		// move의 경우 original_path가 존재해야 함
		if op.Type == FileOperationMove {
			_, err := txn.BaseTree.File(op.FileChange.OriginalPath)
			if err != nil {
				op.State = TransactionStateFailed
				op.Error = fmt.Errorf("file not found for move operation: %s", op.FileChange.OriginalPath)
				if !txn.Options.AllowPartialCommit {
					txn.State = TransactionStateFailed
					return op.Error
				}
			}
		}
	}

	// 여기까지 왔다면 검증 통과
	txn.State = TransactionStateActive

	return nil
}

// CommitTransaction은 트랜잭션의 모든 작업을 원자적으로 커밋합니다
func (tm *TransactionManager) CommitTransaction(ctx context.Context, txn *Transaction) (*TransactionResult, error) {
	txn.mutex.Lock()
	defer txn.mutex.Unlock()

	if txn.State != TransactionStateActive {
		return nil, fmt.Errorf("cannot commit transaction in state: %s", txn.State)
	}

	txn.State = TransactionStateCommitting

	result := &TransactionResult{
		TransactionID:        txn.ID,
		Operations:           txn.Operations,
		SuccessfulOperations: make([]string, 0),
		FailedOperations:     make(map[string]error),
		StartTime:            txn.StartTime,
	}

	// 타임아웃 타이머 중지
	if txn.TimeoutTimer != nil {
		txn.TimeoutTimer.Stop()
	}

	// 검증 전용 트랜잭션인 경우 커밋 없이 성공 반환
	if txn.Options.ValidateOnly {
		result.State = TransactionStateCommitted
		result.EndTime = time.Now()
		result.Duration = result.EndTime.Sub(result.StartTime)
		txn.State = TransactionStateCommitted

		return result, nil
	}

	// 원자적 커밋을 위한 파일 변경 사항 준비
	validChanges := make([]FileChange, 0, len(txn.Operations))
	for _, op := range txn.Operations {
		if op.State != TransactionStateFailed {
			validChanges = append(validChanges, op.FileChange)
			result.SuccessfulOperations = append(result.SuccessfulOperations, op.ID)
		} else {
			result.FailedOperations[op.ID] = op.Error
		}
	}

	// 유효한 변경 사항이 없고 부분 커밋을 허용하지 않는 경우 실패
	if len(validChanges) == 0 && !txn.Options.AllowPartialCommit {
		txn.State = TransactionStateFailed
		result.State = TransactionStateFailed
		result.EndTime = time.Now()
		result.Duration = result.EndTime.Sub(result.StartTime)
		return result, fmt.Errorf("no valid operations to commit")
	}

	// 변경 사항으로부터 트리 생성
	treeResult, err := createTreeFromChanges(txn.Repository, txn.BaseTree, validChanges)
	if err != nil {
		txn.State = TransactionStateFailed
		result.State = TransactionStateFailed
		result.EndTime = time.Now()
		result.Duration = result.EndTime.Sub(result.StartTime)
		return result, fmt.Errorf("failed to create tree: %w", err)
	}

	// 커밋 생성
	commitResult, err := createCommit(txn.Repository, treeResult.TreeHash, txn.CommitInfo)
	if err != nil {
		txn.State = TransactionStateFailed
		result.State = TransactionStateFailed
		result.EndTime = time.Now()
		result.Duration = result.EndTime.Sub(result.StartTime)
		return result, fmt.Errorf("failed to create commit: %w", err)
	}

	// 성공!
	txn.State = TransactionStateCommitted
	result.State = TransactionStateCommitted
	result.CommitResult = commitResult
	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)

	return result, nil
}

// RollbackTransaction은 트랜잭션을 롤백합니다
func (tm *TransactionManager) RollbackTransaction(ctx context.Context, txn *Transaction) error {
	txn.mutex.Lock()
	defer txn.mutex.Unlock()

	if txn.State == TransactionStateCommitted || txn.State == TransactionStateRolledBack {
		return fmt.Errorf("cannot rollback transaction in state: %s", txn.State)
	}

	tm.logger.Info("트랜잭션 롤백",
		zap.String("transaction_id", txn.ID),
		zap.String("previous_state", string(txn.State)))

	// 타임아웃 타이머 중지
	if txn.TimeoutTimer != nil {
		txn.TimeoutTimer.Stop()
	}

	// 모든 작업을 롤백됨으로 표시
	for _, op := range txn.Operations {
		if op.State != TransactionStateFailed {
			op.State = TransactionStateRolledBack
		}
	}

	txn.State = TransactionStateRolledBack
	return nil
}

// GetTransaction은 ID로 트랜잭션을 조회합니다
func (tm *TransactionManager) GetTransaction(txnID string) (*Transaction, error) {
	tm.mutex.RLock()
	defer tm.mutex.RUnlock()

	txn, exists := tm.activeTransactions[txnID]
	if !exists {
		return nil, fmt.Errorf("transaction not found: %s", txnID)
	}

	return txn, nil
}

// ListActiveTransactions는 모든 활성 트랜잭션을 반환합니다
func (tm *TransactionManager) ListActiveTransactions() []*Transaction {
	tm.mutex.RLock()
	defer tm.mutex.RUnlock()

	transactions := make([]*Transaction, 0, len(tm.activeTransactions))
	for _, txn := range tm.activeTransactions {
		transactions = append(transactions, txn)
	}

	return transactions
}

// CleanupTransaction은 완료된 트랜잭션을 제거합니다
func (tm *TransactionManager) CleanupTransaction(txnID string) {
	tm.mutex.Lock()
	defer tm.mutex.Unlock()

	delete(tm.activeTransactions, txnID)
}

// TreeManipulationResult는 트리 조작 작업의 결과를 나타냅니다
type TreeManipulationResult struct {
	// TreeHash는 결과 트리 해시
	TreeHash plumbing.Hash
	// ChangedFiles는 수정된 모든 파일 목록
	ChangedFiles []string
	// CreatedFiles는 생성된 모든 파일 목록
	CreatedFiles []string
	// DeletedFiles는 삭제된 모든 파일 목록
	DeletedFiles []string
}

// createTreeFromChanges는 변경 사항으로부터 트리를 생성합니다
// 서브디렉토리를 올바르게 처리하기 위해 재귀적으로 트리를 생성합니다.
// baseTree가 nil이면 root commit으로 빈 트리에서 시작합니다.
func createTreeFromChanges(repo *repositories.RepositoryWrapper, baseTree *object.Tree, changes []FileChange) (*TreeManipulationResult, error) {
	gitRepo := repo.GetRepo()
	if gitRepo == nil {
		return nil, fmt.Errorf("repository is closed")
	}

	// 모든 파일을 경로별로 맵에 저장 (기본 트리에서; root commit이면 빈 맵)
	files := make(map[string]TreeFileInfo)
	if baseTree != nil {
		_ = baseTree.Files().ForEach(func(f *object.File) error {
			files[f.Name] = TreeFileInfo{
				Hash: f.Hash,
				Mode: f.Mode,
			}
			return nil
		})
	}

	var createdFiles, changedFiles, deletedFiles []string

	// 변경 사항 적용
	for _, change := range changes {
		switch change.Operation {
		case FileOperationCreate, FileOperationUpdate:
			// 블롭 생성
			blobHash, err := createBlob(gitRepo, change.Content)
			if err != nil {
				return nil, fmt.Errorf("failed to create blob for %s: %w", change.Path, err)
			}

			mode := filemode.Regular
			if change.Mode != nil {
				mode = *change.Mode
			}

			// 파일 추가 또는 업데이트
			files[change.Path] = TreeFileInfo{
				Hash: blobHash,
				Mode: mode,
			}

			if change.Operation == FileOperationCreate {
				createdFiles = append(createdFiles, change.Path)
			} else {
				changedFiles = append(changedFiles, change.Path)
			}

		case FileOperationDelete:
			// 파일 제거
			delete(files, change.Path)
			deletedFiles = append(deletedFiles, change.Path)

		case FileOperationMove:
			// move/rename: original을 삭제하고, 새 path로 추가
			if strings.TrimSpace(change.OriginalPath) == "" {
				return nil, fmt.Errorf("original_path is required for move operation")
			}

			srcInfo, ok := files[change.OriginalPath]
			if !ok {
				return nil, fmt.Errorf("file '%s' not found in repository", change.OriginalPath)
			}
			// 대상이 이미 존재하면 충돌
			if _, exists := files[change.Path]; exists {
				return nil, fmt.Errorf("file already exists: %s", change.Path)
			}

			// content가 있으면 새 blob 생성, 없으면 기존 blob 유지
			newHash := srcInfo.Hash
			if len(change.Content) > 0 {
				blobHash, err := createBlob(gitRepo, change.Content)
				if err != nil {
					return nil, fmt.Errorf("failed to create blob for %s: %w", change.Path, err)
				}
				newHash = blobHash
			}

			mode := srcInfo.Mode
			if change.Mode != nil {
				mode = *change.Mode
			}

			delete(files, change.OriginalPath)
			files[change.Path] = TreeFileInfo{
				Hash: newHash,
				Mode: mode,
			}

			deletedFiles = append(deletedFiles, change.OriginalPath)
			createdFiles = append(createdFiles, change.Path)
		}
	}

	// 재귀적으로 트리 빌드
	treeHash, err := buildTreeRecursive(gitRepo, files, "")
	if err != nil {
		return nil, fmt.Errorf("failed to build tree: %w", err)
	}

	return &TreeManipulationResult{
		TreeHash:     treeHash,
		ChangedFiles: changedFiles,
		CreatedFiles: createdFiles,
		DeletedFiles: deletedFiles,
	}, nil
}

// TreeFileInfo는 트리 파일 정보를 나타냅니다
type TreeFileInfo struct {
	Hash plumbing.Hash
	Mode filemode.FileMode
}

// buildTreeRecursive는 재귀적으로 트리를 생성합니다
func buildTreeRecursive(gitRepo *git.Repository, files map[string]TreeFileInfo, dirPath string) (plumbing.Hash, error) {
	var entries []object.TreeEntry
	subdirs := make(map[string]bool)

	// 현재 디렉토리의 파일과 서브디렉토리 찾기
	for filePath, fileInfo := range files {
		var relativePath string
		if dirPath == "" {
			relativePath = filePath
		} else {
			if !strings.HasPrefix(filePath, dirPath+"/") {
				continue
			}
			relativePath = strings.TrimPrefix(filePath, dirPath+"/")
		}

		// 서브디렉토리에 있는 파일은 건너뛰기
		parts := strings.Split(relativePath, "/")
		if len(parts) > 1 {
			subdirs[parts[0]] = true
			continue
		}

		// 파일 엔트리 추가
		entries = append(entries, object.TreeEntry{
			Name: parts[0],
			Mode: fileInfo.Mode,
			Hash: fileInfo.Hash,
		})
	}

	// 서브디렉토리 엔트리 추가
	for subdirName := range subdirs {
		var subdirPath string
		if dirPath == "" {
			subdirPath = subdirName
		} else {
			subdirPath = dirPath + "/" + subdirName
		}

		subdirTreeHash, err := buildTreeRecursive(gitRepo, files, subdirPath)
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("failed to build subtree for %s: %w", subdirPath, err)
		}

		entries = append(entries, object.TreeEntry{
			Name: subdirName,
			Mode: filemode.Dir,
			Hash: subdirTreeHash,
		})
	}

	// 트리 엔트리 정렬 (Git 요구사항)
	sortTreeEntries(entries)

	// 트리 객체 생성
	tree := &object.Tree{Entries: entries}

	// 트리 인코딩 및 저장
	treeObj := gitRepo.Storer.NewEncodedObject()
	if err := tree.Encode(treeObj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to encode tree: %w", err)
	}

	treeHash, err := gitRepo.Storer.SetEncodedObject(treeObj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to store tree: %w", err)
	}

	return treeHash, nil
}

// createCommit는 커밋을 생성합니다
func createCommit(repo *repositories.RepositoryWrapper, treeHash plumbing.Hash, commitInfo CommitInfo) (*CommitResult, error) {
	gitRepo := repo.GetRepo()
	if gitRepo == nil {
		return nil, fmt.Errorf("repository is closed")
	}

	// 커밋터가 없으면 작성자 사용
	committer := commitInfo.Author
	if commitInfo.Committer != nil {
		committer = *commitInfo.Committer
	}

	// 커밋 객체 생성
	commit := &object.Commit{
		Author:       commitInfo.Author,
		Committer:    committer,
		Message:      commitInfo.Message,
		TreeHash:     treeHash,
		ParentHashes: commitInfo.Parents,
	}

	// 커밋 인코딩 및 저장
	commitObj := gitRepo.Storer.NewEncodedObject()
	if err := commit.Encode(commitObj); err != nil {
		return nil, fmt.Errorf("failed to encode commit: %w", err)
	}

	commitHash, err := gitRepo.Storer.SetEncodedObject(commitObj)
	if err != nil {
		return nil, fmt.Errorf("failed to store commit: %w", err)
	}

	return &CommitResult{
		Hash:      commitHash,
		TreeHash:  treeHash,
		Message:   commitInfo.Message,
		Author:    commitInfo.Author,
		Committer: committer,
		Parents:   commitInfo.Parents,
	}, nil
}

// createBlob는 블롭을 생성합니다
func createBlob(repo *git.Repository, content []byte) (plumbing.Hash, error) {
	// 블롭 객체 생성
	blobObj := repo.Storer.NewEncodedObject()
	blobObj.SetType(plumbing.BlobObject)
	blobObj.SetSize(int64(len(content)))

	// 콘텐츠 작성
	writer, err := blobObj.Writer()
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to create blob writer: %w", err)
	}

	if _, err := writer.Write(content); err != nil {
		_ = writer.Close()
		return plumbing.ZeroHash, fmt.Errorf("failed to write blob content: %w", err)
	}

	if err := writer.Close(); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to close blob writer: %w", err)
	}

	// 블롭 저장
	hash, err := repo.Storer.SetEncodedObject(blobObj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to store blob: %w", err)
	}

	return hash, nil
}

// sortTreeEntries는 트리 엔트리를 정렬합니다 (Git 요구사항)
// Git tree entries must be sorted using the same logic as go-git's TreeEntrySorter:
// - Directories have "/" appended to their name for comparison
// - This ensures directories come before files with the same prefix
// - Then entries are sorted alphabetically by the modified name
func sortTreeEntries(entries []object.TreeEntry) {
	// sort.Slice를 사용하여 효율적으로 정렬
	sort.Slice(entries, func(i, j int) bool {
		name1 := entries[i].Name
		name2 := entries[j].Name

		// 디렉토리 이름에 "/"를 추가하여 비교 (go-git의 TreeEntrySorter와 동일)
		if entries[i].Mode == filemode.Dir {
			name1 += "/"
		}
		if entries[j].Mode == filemode.Dir {
			name2 += "/"
		}

		return name1 < name2
	})
}
