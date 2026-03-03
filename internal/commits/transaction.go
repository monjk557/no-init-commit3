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
	Timeout            time.Duration
	ValidateOnly       bool
	AllowPartialCommit bool
	MaxRetries         int
}

// FileChange는 파일 변경 사항을 나타냅니다
type FileChange struct {
	Path         string
	Operation    FileOperation
	Content      []byte
	Mode         *string // "100644", "100755"
	OriginalPath string
}

// CommitInfo는 커밋 메타데이터를 나타냅니다
type CommitInfo struct {
	Message   string
	Author    NativeSignature
	Committer *NativeSignature
	Parents   []string
}

// CommitResult는 커밋 작업의 결과를 나타냅니다
type CommitResult struct {
	Hash      string
	TreeHash  string
	Message   string
	Author    NativeSignature
	Committer NativeSignature
	Parents   []string
}

// TransactionOperation은 트랜잭션 내의 단일 작업을 나타냅니다
type TransactionOperation struct {
	ID         string
	Type       FileOperation
	FileChange FileChange
	State      TransactionState
	Error      error
	Timestamp  time.Time
}

// TransactionResult는 트랜잭션의 결과를 나타냅니다
type TransactionResult struct {
	TransactionID        string
	State                TransactionState
	CommitResult         *CommitResult
	Operations           []*TransactionOperation
	SuccessfulOperations []string
	FailedOperations     map[string]error
	StartTime            time.Time
	EndTime              time.Time
	Duration             time.Duration
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
	ID             string
	State          TransactionState
	RepoPath       string
	BaseCommitHash string // empty string for root commit
	BaseTreeHash   string // empty string for root commit
	Operations     []*TransactionOperation
	CommitInfo     CommitInfo
	Options        TransactionOptions
	StartTime      time.Time
	TimeoutTimer   *time.Timer
	mutex          sync.RWMutex
	manager        *TransactionManager
}

// TransactionManager는 원자적 다중 파일 작업을 위한 Git 트랜잭션을 관리합니다
type TransactionManager struct {
	logger             *logging.Logger
	activeTransactions map[string]*Transaction
	mutex              sync.RWMutex
	defaultTimeout     time.Duration
}

// NewTransactionManager는 새로운 트랜잭션 매니저를 생성합니다
func NewTransactionManager(logger *logging.Logger) *TransactionManager {
	return &TransactionManager{
		logger:             logger,
		activeTransactions: make(map[string]*Transaction),
		defaultTimeout:     5 * time.Minute,
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
// baseCommit이 빈 문자열이면 root commit(첫 커밋)용으로 BaseTreeHash 없이 시작합니다.
func (tm *TransactionManager) BeginTransaction(ctx context.Context, repoPath string, baseCommit string, commitInfo CommitInfo, options TransactionOptions) (*Transaction, error) {
	txnID := fmt.Sprintf("txn_%d_%s", time.Now().UnixNano(), generateRandomID(8))

	if options.Timeout == 0 {
		options.Timeout = tm.defaultTimeout
	}

	var baseTreeHash string
	if baseCommit != "" {
		var err error
		baseTreeHash, err = nativeGetTreeHash(ctx, repoPath, baseCommit)
		if err != nil {
			return nil, fmt.Errorf("failed to get base commit tree: %w", err)
		}
	}

	txn := &Transaction{
		ID:             txnID,
		State:          TransactionStateActive,
		RepoPath:       repoPath,
		BaseCommitHash: baseCommit,
		BaseTreeHash:   baseTreeHash,
		Operations:     make([]*TransactionOperation, 0),
		CommitInfo:     commitInfo,
		Options:        options,
		StartTime:      time.Now(),
		manager:        tm,
	}

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

	// root commit(BaseTreeHash empty)인 경우 create 작업만 허용
	if txn.BaseTreeHash == "" {
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

		if op.Type == FileOperationUpdate || op.Type == FileOperationDelete {
			exists, err := nativeLsTreePath(ctx, txn.RepoPath, txn.BaseTreeHash, op.FileChange.Path)
			if err != nil || !exists {
				op.State = TransactionStateFailed
				op.Error = fmt.Errorf("file not found for %s operation: %s", op.Type, op.FileChange.Path)
				if !txn.Options.AllowPartialCommit {
					txn.State = TransactionStateFailed
					return op.Error
				}
			}
		}

		if op.Type == FileOperationMove {
			exists, err := nativeLsTreePath(ctx, txn.RepoPath, txn.BaseTreeHash, op.FileChange.OriginalPath)
			if err != nil || !exists {
				op.State = TransactionStateFailed
				op.Error = fmt.Errorf("file not found for move operation: %s", op.FileChange.OriginalPath)
				if !txn.Options.AllowPartialCommit {
					txn.State = TransactionStateFailed
					return op.Error
				}
			}
		}
	}

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

	if txn.TimeoutTimer != nil {
		txn.TimeoutTimer.Stop()
	}

	if txn.Options.ValidateOnly {
		result.State = TransactionStateCommitted
		result.EndTime = time.Now()
		result.Duration = result.EndTime.Sub(result.StartTime)
		txn.State = TransactionStateCommitted
		return result, nil
	}

	validChanges := make([]FileChange, 0, len(txn.Operations))
	for _, op := range txn.Operations {
		if op.State != TransactionStateFailed {
			validChanges = append(validChanges, op.FileChange)
			result.SuccessfulOperations = append(result.SuccessfulOperations, op.ID)
		} else {
			result.FailedOperations[op.ID] = op.Error
		}
	}

	if len(validChanges) == 0 && !txn.Options.AllowPartialCommit {
		txn.State = TransactionStateFailed
		result.State = TransactionStateFailed
		result.EndTime = time.Now()
		result.Duration = result.EndTime.Sub(result.StartTime)
		return result, fmt.Errorf("no valid operations to commit")
	}

	treeResult, err := createTreeFromChanges(ctx, txn.RepoPath, txn.BaseTreeHash, validChanges)
	if err != nil {
		txn.State = TransactionStateFailed
		result.State = TransactionStateFailed
		result.EndTime = time.Now()
		result.Duration = result.EndTime.Sub(result.StartTime)
		return result, fmt.Errorf("failed to create tree: %w", err)
	}

	commitResult, err := createCommitNative(ctx, txn.RepoPath, treeResult.TreeHash, txn.CommitInfo)
	if err != nil {
		txn.State = TransactionStateFailed
		result.State = TransactionStateFailed
		result.EndTime = time.Now()
		result.Duration = result.EndTime.Sub(result.StartTime)
		return result, fmt.Errorf("failed to create commit: %w", err)
	}

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

	if txn.TimeoutTimer != nil {
		txn.TimeoutTimer.Stop()
	}

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
	TreeHash     string
	ChangedFiles []string
	CreatedFiles []string
	DeletedFiles []string
}

// TreeFileInfo는 트리 파일 정보를 나타냅니다
type TreeFileInfo struct {
	Hash string
	Mode string // "100644", "100755"
}

// createTreeFromChanges는 변경 사항으로부터 트리를 생성합니다.
// baseTreeHash가 빈 문자열이면 root commit으로 빈 트리에서 시작합니다.
func createTreeFromChanges(ctx context.Context, repoPath, baseTreeHash string, changes []FileChange) (*TreeManipulationResult, error) {
	// 모든 파일을 경로별로 맵에 저장
	files := make(map[string]TreeFileInfo)
	if baseTreeHash != "" {
		entries, err := nativeLsTree(ctx, repoPath, baseTreeHash)
		if err != nil {
			return nil, fmt.Errorf("failed to list base tree: %w", err)
		}
		for _, e := range entries {
			files[e.Path] = TreeFileInfo{
				Hash: e.Hash,
				Mode: e.Mode,
			}
		}
	}

	var createdFiles, changedFiles, deletedFiles []string

	for _, change := range changes {
		switch change.Operation {
		case FileOperationCreate, FileOperationUpdate:
			blobHash, err := nativeHashObject(ctx, repoPath, change.Content)
			if err != nil {
				return nil, fmt.Errorf("failed to create blob for %s: %w", change.Path, err)
			}

			mode := "100644"
			if change.Mode != nil {
				mode = *change.Mode
			}

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
			delete(files, change.Path)
			deletedFiles = append(deletedFiles, change.Path)

		case FileOperationMove:
			if strings.TrimSpace(change.OriginalPath) == "" {
				return nil, fmt.Errorf("original_path is required for move operation")
			}

			srcInfo, ok := files[change.OriginalPath]
			if !ok {
				return nil, fmt.Errorf("file '%s' not found in repository", change.OriginalPath)
			}
			if _, exists := files[change.Path]; exists {
				return nil, fmt.Errorf("file already exists: %s", change.Path)
			}

			newHash := srcInfo.Hash
			if len(change.Content) > 0 {
				blobHash, err := nativeHashObject(ctx, repoPath, change.Content)
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

	treeHash, err := buildTreeRecursive(ctx, repoPath, files, "")
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

// buildTreeRecursive는 재귀적으로 트리를 생성합니다.
func buildTreeRecursive(ctx context.Context, repoPath string, files map[string]TreeFileInfo, dirPath string) (string, error) {
	var entries []nativeMkTreeEntry
	subdirs := make(map[string]bool)

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

		parts := strings.Split(relativePath, "/")
		if len(parts) > 1 {
			subdirs[parts[0]] = true
			continue
		}

		entries = append(entries, nativeMkTreeEntry{
			Mode: fileInfo.Mode,
			Type: "blob",
			Hash: fileInfo.Hash,
			Name: parts[0],
		})
	}

	for subdirName := range subdirs {
		var subdirPath string
		if dirPath == "" {
			subdirPath = subdirName
		} else {
			subdirPath = dirPath + "/" + subdirName
		}

		subdirTreeHash, err := buildTreeRecursive(ctx, repoPath, files, subdirPath)
		if err != nil {
			return "", fmt.Errorf("failed to build subtree for %s: %w", subdirPath, err)
		}

		entries = append(entries, nativeMkTreeEntry{
			Mode: "040000",
			Type: "tree",
			Hash: subdirTreeHash,
			Name: subdirName,
		})
	}

	// Sort tree entries (Git requirement): directories get "/" appended for comparison
	sortNativeTreeEntries(entries)

	return nativeMkTree(ctx, repoPath, entries)
}

// createCommitNative creates a commit object using native git.
func createCommitNative(ctx context.Context, repoPath, treeHash string, commitInfo CommitInfo) (*CommitResult, error) {
	committer := commitInfo.Author
	if commitInfo.Committer != nil {
		committer = *commitInfo.Committer
	}

	commitHash, err := nativeCommitTree(ctx, repoPath, treeHash, commitInfo.Parents, commitInfo.Message, &commitInfo.Author, &committer)
	if err != nil {
		return nil, fmt.Errorf("failed to create commit: %w", err)
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

// sortNativeTreeEntries sorts tree entries using Git's sorting convention:
// directories have "/" appended to their name for comparison.
func sortNativeTreeEntries(entries []nativeMkTreeEntry) {
	sort.Slice(entries, func(i, j int) bool {
		name1 := entries[i].Name
		name2 := entries[j].Name

		if entries[i].Mode == "040000" {
			name1 += "/"
		}
		if entries[j].Mode == "040000" {
			name2 += "/"
		}

		return name1 < name2
	})
}
