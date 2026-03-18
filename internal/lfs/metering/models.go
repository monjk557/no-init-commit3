package metering

import "time"

// StorageEvent는 스토리지 변동 이벤트를 기록하는 DB 모델입니다.
// upload/delete 시 기록되어 워크스페이스별 사용량 집계에 사용됩니다.
type StorageEvent struct {
	ID        uint      `gorm:"primaryKey"`
	Owner     string    `gorm:"index:idx_storage_owner;size:255;not null"`
	Repo      string    `gorm:"size:255;not null"`
	OID       string    `gorm:"size:64;not null"`
	Size      int64     `gorm:"not null"`
	Action    string    `gorm:"size:10;not null"` // "upload" 또는 "delete"
	Timestamp time.Time `gorm:"autoCreateTime;index:idx_storage_ts"`
}

// TableName은 GORM 테이블명을 지정합니다.
func (StorageEvent) TableName() string {
	return "lfs_storage_events"
}

// BandwidthEvent는 다운로드 대역폭 사용을 기록하는 DB 모델입니다.
// Batch API에서 download URL 발급 시 기록됩니다.
type BandwidthEvent struct {
	ID        uint      `gorm:"primaryKey"`
	Owner     string    `gorm:"index:idx_bw_owner_month;size:255;not null"`
	Repo      string    `gorm:"size:255;not null"`
	OID       string    `gorm:"size:64;not null"`
	Size      int64     `gorm:"not null"`
	Month     string    `gorm:"index:idx_bw_owner_month;size:7;not null"` // "2026-03"
	Timestamp time.Time `gorm:"autoCreateTime"`
}

// TableName은 GORM 테이블명을 지정합니다.
func (BandwidthEvent) TableName() string {
	return "lfs_bandwidth_events"
}
