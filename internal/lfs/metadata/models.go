package metadata

import "time"

// LFSObject는 LFS 오브젝트의 메타데이터를 저장하는 DB 모델입니다.
// 실제 blob은 GCS에 저장되고, 이 테이블은 존재 여부와 크기만 추적합니다.
type LFSObject struct {
	ID        uint      `gorm:"primaryKey"`
	OID       string    `gorm:"uniqueIndex:idx_owner_repo_oid;size:64"` // SHA-256 hex
	Owner     string    `gorm:"uniqueIndex:idx_owner_repo_oid;size:255;index:idx_owner"`
	Repo      string    `gorm:"uniqueIndex:idx_owner_repo_oid;size:255"`
	Size      int64     `gorm:"not null"`
	CreatedAt time.Time `gorm:"autoCreateTime"`
}

// TableName은 GORM 테이블명을 지정합니다.
func (LFSObject) TableName() string {
	return "lfs_objects"
}

// LFSLock은 파일 잠금 정보를 저장하는 DB 모델입니다.
type LFSLock struct {
	ID       uint      `gorm:"primaryKey"`
	Owner    string    `gorm:"index:idx_lock_owner_repo;size:255"`
	Repo     string    `gorm:"index:idx_lock_owner_repo;size:255"`
	Path     string    `gorm:"uniqueIndex:idx_lock_path;size:1024;not null"`
	LockedBy string    `gorm:"size:255;not null"`
	LockedAt time.Time `gorm:"autoCreateTime"`
}

// TableName은 GORM 테이블명을 지정합니다.
func (LFSLock) TableName() string {
	return "lfs_locks"
}
