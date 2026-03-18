package metadata

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Repository는 LFS 메타데이터 CRUD를 담당합니다.
type Repository struct {
	db     *gorm.DB
	logger *zap.Logger
}

// NewRepository는 SQLite 기반 메타데이터 저장소를 생성합니다.
func NewRepository(dbPath string, logger *zap.Logger) (*Repository, error) {
	db, err := gorm.Open(sqlite.Open(dbPath+"?_journal_mode=WAL&_busy_timeout=5000"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open metadata database: %w", err)
	}

	// 테이블 자동 생성
	if err := db.AutoMigrate(&LFSObject{}, &LFSLock{}); err != nil {
		return nil, fmt.Errorf("failed to migrate metadata tables: %w", err)
	}

	return &Repository{db: db, logger: logger}, nil
}

// --- LFS Object 관련 ---

// PutObject는 LFS 오브젝트 메타데이터를 저장합니다 (upsert).
func (r *Repository) PutObject(_ context.Context, owner, repo, oid string, size int64) error {
	obj := LFSObject{
		OID:   oid,
		Owner: owner,
		Repo:  repo,
		Size:  size,
	}
	result := r.db.Where("oid = ? AND owner = ? AND repo = ?", oid, owner, repo).
		FirstOrCreate(&obj)
	return result.Error
}

// ObjectExists는 메타데이터 DB에 오브젝트가 존재하는지 확인합니다.
func (r *Repository) ObjectExists(_ context.Context, owner, repo, oid string) (bool, error) {
	var count int64
	err := r.db.Model(&LFSObject{}).
		Where("oid = ? AND owner = ? AND repo = ?", oid, owner, repo).
		Count(&count).Error
	return count > 0, err
}

// GetObject는 오브젝트 메타데이터를 조회합니다.
func (r *Repository) GetObject(_ context.Context, owner, repo, oid string) (*LFSObject, error) {
	var obj LFSObject
	err := r.db.Where("oid = ? AND owner = ? AND repo = ?", oid, owner, repo).First(&obj).Error
	if err != nil {
		return nil, err
	}
	return &obj, nil
}

// DeleteObject는 오브젝트 메타데이터를 삭제합니다.
func (r *Repository) DeleteObject(_ context.Context, owner, repo, oid string) error {
	return r.db.Where("oid = ? AND owner = ? AND repo = ?", oid, owner, repo).
		Delete(&LFSObject{}).Error
}

// GetStorageUsage는 워크스페이스(owner)의 총 스토리지 사용량을 반환합니다.
func (r *Repository) GetStorageUsage(_ context.Context, owner string) (totalBytes int64, objectCount int64, err error) {
	var result struct {
		TotalBytes  int64
		ObjectCount int64
	}
	err = r.db.Model(&LFSObject{}).
		Select("COALESCE(SUM(size), 0) as total_bytes, COUNT(*) as object_count").
		Where("owner = ?", owner).
		Scan(&result).Error
	return result.TotalBytes, result.ObjectCount, err
}

// GetRepoStorageUsage는 레포지토리별 스토리지 사용량을 반환합니다.
func (r *Repository) GetRepoStorageUsage(_ context.Context, owner string) ([]RepoStorageInfo, error) {
	var results []RepoStorageInfo
	err := r.db.Model(&LFSObject{}).
		Select("repo, COALESCE(SUM(size), 0) as total_bytes, COUNT(*) as object_count").
		Where("owner = ?", owner).
		Group("repo").
		Scan(&results).Error
	return results, err
}

// RepoStorageInfo는 레포지토리별 스토리지 정보입니다.
type RepoStorageInfo struct {
	Repo        string `json:"repo"`
	TotalBytes  int64  `json:"total_bytes"`
	ObjectCount int64  `json:"object_count"`
}

// --- LFS Lock 관련 ---

// CreateLock은 파일 잠금을 생성합니다.
func (r *Repository) CreateLock(_ context.Context, owner, repo, path, lockedBy string) (*LFSLock, error) {
	// 이미 잠긴 파일인지 확인
	var existing LFSLock
	err := r.db.Where("owner = ? AND repo = ? AND path = ?", owner, repo, path).First(&existing).Error
	if err == nil {
		return &existing, fmt.Errorf("already locked by %s since %s", existing.LockedBy, existing.LockedAt.Format(time.RFC3339))
	}

	lock := LFSLock{
		Owner:    owner,
		Repo:     repo,
		Path:     path,
		LockedBy: lockedBy,
	}
	if err := r.db.Create(&lock).Error; err != nil {
		return nil, fmt.Errorf("failed to create lock: %w", err)
	}
	return &lock, nil
}

// ListLocks는 레포지토리의 잠금 목록을 반환합니다.
func (r *Repository) ListLocks(_ context.Context, owner, repo, path, cursor string, limit int) ([]LFSLock, error) {
	query := r.db.Where("owner = ? AND repo = ?", owner, repo)
	if path != "" {
		query = query.Where("path = ?", path)
	}
	if cursor != "" {
		query = query.Where("id > ?", cursor)
	}
	if limit <= 0 {
		limit = 100
	}

	var locks []LFSLock
	err := query.Order("id ASC").Limit(limit).Find(&locks).Error
	return locks, err
}

// DeleteLock은 파일 잠금을 해제합니다.
func (r *Repository) DeleteLock(_ context.Context, id uint, user string, force bool) (*LFSLock, error) {
	var lock LFSLock
	if err := r.db.First(&lock, id).Error; err != nil {
		return nil, fmt.Errorf("lock not found: %w", err)
	}

	if !force && lock.LockedBy != user {
		return &lock, fmt.Errorf("lock is owned by %s", lock.LockedBy)
	}

	if err := r.db.Delete(&lock).Error; err != nil {
		return nil, fmt.Errorf("failed to delete lock: %w", err)
	}
	return &lock, nil
}

// VerifyLocks는 현재 사용자 소유/타인 소유 잠금을 구분하여 반환합니다.
func (r *Repository) VerifyLocks(_ context.Context, owner, repo, user, cursor string, limit int) (ours, theirs []LFSLock, err error) {
	query := r.db.Where("owner = ? AND repo = ?", owner, repo)
	if cursor != "" {
		query = query.Where("id > ?", cursor)
	}
	if limit <= 0 {
		limit = 100
	}

	var allLocks []LFSLock
	if err = query.Order("id ASC").Limit(limit).Find(&allLocks).Error; err != nil {
		return nil, nil, err
	}

	for _, lock := range allLocks {
		if lock.LockedBy == user {
			ours = append(ours, lock)
		} else {
			theirs = append(theirs, lock)
		}
	}
	return ours, theirs, nil
}
