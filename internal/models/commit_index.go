package models

import (
	"gorm.io/gorm"
	"time"
)

// CommitPathIndex represents the database model for commit path indexing
// This enables O(1) lookup of latest commits for file paths, replacing O(C×N×F) Git traversal
type CommitPathIndex struct {
	ID            uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	RepoID        string    `gorm:"type:varchar(255);not null;index:idx_repo_path,priority:1" json:"repo_id"`
	FilePath      string    `gorm:"type:text;not null;index:idx_repo_path,priority:2" json:"file_path"`
	LastCommitSHA string    `gorm:"type:varchar(64);not null;index:idx_commit_sha" json:"last_commit_sha"`
	CommitTime    time.Time `gorm:"not null;index:idx_repo_time,priority:2" json:"commit_time"`
	ChangeType    string    `gorm:"type:varchar(20);not null" json:"change_type"` // added, modified, deleted
	FileSize      int64     `gorm:"default:0" json:"file_size"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// TableName specifies the table name for GORM
func (CommitPathIndex) TableName() string {
	return "commit_path_index"
}

// BeforeCreate sets timestamps before creating a record
func (c *CommitPathIndex) BeforeCreate(tx *gorm.DB) (err error) {
	now := time.Now()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = now
	}
	return nil
}

// BeforeUpdate sets updated timestamp before updating a record
func (c *CommitPathIndex) BeforeUpdate(tx *gorm.DB) (err error) {
	c.UpdatedAt = time.Now()
	return nil
}

// CommitIndexStats provides statistics about the commit index
type CommitIndexStats struct {
	TotalIndexedFiles int64     `json:"total_indexed_files"`
	LatestIndexTime   time.Time `json:"latest_index_time"`
	IndexedRepos      int64     `json:"indexed_repos"`
	IndexSize         int64     `json:"index_size_mb"`
}

// GetUniqueConstraintKey returns the unique constraint key for repo_id and file_path
func (c *CommitPathIndex) GetUniqueConstraintKey() string {
	return c.RepoID + ":" + c.FilePath
}

// IsDeleted checks if the file was deleted in the last commit
func (c *CommitPathIndex) IsDeleted() bool {
	return c.ChangeType == "deleted"
}

// ChangeTypeEnum defines the possible change types
type ChangeType string

const (
	ChangeTypeAdded    ChangeType = "added"
	ChangeTypeModified ChangeType = "modified"
	ChangeTypeDeleted  ChangeType = "deleted"
	ChangeTypeRenamed  ChangeType = "renamed"
)

// String returns the string representation of ChangeType
func (ct ChangeType) String() string {
	return string(ct)
}

// IsValid checks if the change type is valid
func (ct ChangeType) IsValid() bool {
	switch ct {
	case ChangeTypeAdded, ChangeTypeModified, ChangeTypeDeleted, ChangeTypeRenamed:
		return true
	default:
		return false
	}
}
