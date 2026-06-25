package models

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 회귀: "CommitPathIndex 의 GORM hook / 헬퍼가 일관된 값을 반환해야 함"

func TestCommitPathIndex_TableName(t *testing.T) {
	assert.Equal(t, "commit_path_index", CommitPathIndex{}.TableName())
}

func TestCommitPathIndex_BeforeCreate(t *testing.T) {
	t.Run("zero timestamps get set", func(t *testing.T) {
		c := &CommitPathIndex{}
		require.NoError(t, c.BeforeCreate(nil))
		assert.False(t, c.CreatedAt.IsZero())
		assert.False(t, c.UpdatedAt.IsZero())
	})

	t.Run("existing timestamps are preserved", func(t *testing.T) {
		fixed := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		c := &CommitPathIndex{CreatedAt: fixed, UpdatedAt: fixed}
		require.NoError(t, c.BeforeCreate(nil))
		assert.Equal(t, fixed, c.CreatedAt)
		assert.Equal(t, fixed, c.UpdatedAt)
	})
}

func TestCommitPathIndex_BeforeUpdate(t *testing.T) {
	c := &CommitPathIndex{}
	require.NoError(t, c.BeforeUpdate(nil))
	assert.False(t, c.UpdatedAt.IsZero())
}

func TestCommitPathIndex_GetUniqueConstraintKey(t *testing.T) {
	c := &CommitPathIndex{RepoID: "repo-1", FilePath: "src/main.go"}
	assert.Equal(t, "repo-1:src/main.go", c.GetUniqueConstraintKey())
}

func TestCommitPathIndex_IsDeleted(t *testing.T) {
	tests := []struct {
		changeType string
		want       bool
	}{
		{"deleted", true},
		{"added", false},
		{"modified", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.changeType, func(t *testing.T) {
			c := &CommitPathIndex{ChangeType: tt.changeType}
			assert.Equal(t, tt.want, c.IsDeleted())
		})
	}
}

func TestChangeType_String(t *testing.T) {
	assert.Equal(t, "added", ChangeTypeAdded.String())
	assert.Equal(t, "modified", ChangeTypeModified.String())
	assert.Equal(t, "deleted", ChangeTypeDeleted.String())
	assert.Equal(t, "renamed", ChangeTypeRenamed.String())
}

func TestChangeType_IsValid(t *testing.T) {
	valid := []ChangeType{ChangeTypeAdded, ChangeTypeModified, ChangeTypeDeleted, ChangeTypeRenamed}
	for _, ct := range valid {
		assert.True(t, ct.IsValid(), "%s should be valid", ct)
	}
	invalid := []ChangeType{ChangeType("bogus"), ChangeType("")}
	for _, ct := range invalid {
		assert.False(t, ct.IsValid(), "%q should be invalid", ct)
	}
}

func TestCommitIndexStats_Fields(t *testing.T) {
	// 직렬화/구조 sanity 체크 — 필드가 채워지는지 확인
	now := time.Now()
	s := CommitIndexStats{
		TotalIndexedFiles: 10,
		LatestIndexTime:   now,
		IndexedRepos:      2,
		IndexSize:         5,
	}
	assert.Equal(t, int64(10), s.TotalIndexedFiles)
	assert.Equal(t, now, s.LatestIndexTime)
	assert.Equal(t, int64(2), s.IndexedRepos)
	assert.Equal(t, int64(5), s.IndexSize)
}
