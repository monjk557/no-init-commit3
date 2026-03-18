package metering

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Collector는 LFS 사용량을 수집하고 조회하는 서비스입니다.
type Collector struct {
	db     *gorm.DB
	logger *zap.Logger
}

// NewCollector는 새로운 미터링 컬렉터를 생성합니다.
// 메타데이터와 동일한 SQLite DB를 공유합니다.
func NewCollector(dbPath string, logger *zap.Logger) (*Collector, error) {
	db, err := gorm.Open(sqlite.Open(dbPath+"?_journal_mode=WAL&_busy_timeout=5000"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open metering database: %w", err)
	}

	if err := db.AutoMigrate(&StorageEvent{}, &BandwidthEvent{}); err != nil {
		return nil, fmt.Errorf("failed to migrate metering tables: %w", err)
	}

	return &Collector{db: db, logger: logger}, nil
}

// RecordUpload는 LFS 오브젝트 업로드 이벤트를 기록합니다.
func (c *Collector) RecordUpload(_ context.Context, owner, repo, oid string, size int64) error {
	event := StorageEvent{
		Owner:  owner,
		Repo:   repo,
		OID:    oid,
		Size:   size,
		Action: "upload",
	}
	return c.db.Create(&event).Error
}

// RecordDelete는 LFS 오브젝트 삭제 이벤트를 기록합니다.
func (c *Collector) RecordDelete(_ context.Context, owner, repo, oid string, size int64) error {
	event := StorageEvent{
		Owner:  owner,
		Repo:   repo,
		OID:    oid,
		Size:   size,
		Action: "delete",
	}
	return c.db.Create(&event).Error
}

// RecordDownload는 다운로드 대역폭 사용 이벤트를 기록합니다.
func (c *Collector) RecordDownload(_ context.Context, owner, repo, oid string, size int64) error {
	event := BandwidthEvent{
		Owner: owner,
		Repo:  repo,
		OID:   oid,
		Size:  size,
		Month: time.Now().Format("2006-01"),
	}
	return c.db.Create(&event).Error
}

// GetMonthlyBandwidth는 워크스페이스의 월간 다운로드 대역폭 사용량을 반환합니다.
func (c *Collector) GetMonthlyBandwidth(_ context.Context, owner, month string) (int64, error) {
	var totalBytes int64
	err := c.db.Model(&BandwidthEvent{}).
		Select("COALESCE(SUM(size), 0)").
		Where("owner = ? AND month = ?", owner, month).
		Scan(&totalBytes).Error
	return totalBytes, err
}
