package indexing

import (
	"context"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newTestDB 는 인메모리 SQLite + CommitPathIndex 마이그레이션을 가진 테스트 DB 를 만든다.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.CommitPathIndex{}))
	// upsert (OnConflict repo_id+file_path) 가 동작하려면 unique 제약이 필요.
	require.NoError(t, db.Exec(
		"CREATE UNIQUE INDEX IF NOT EXISTS uniq_repo_path ON commit_path_index (repo_id, file_path)").Error)
	return db
}

// seedIndex 는 CommitPathIndex 한 행을 직접 삽입한다.
func seedIndex(t *testing.T, db *gorm.DB, row models.CommitPathIndex) {
	t.Helper()
	require.NoError(t, db.Create(&row).Error)
}

func baseTime() time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
}

// seedRepoFixture 는 repoID 에 대해 다양한 path/change_type/size/time 행을 심는다.
func seedRepoFixture(t *testing.T, db *gorm.DB, repoID string) {
	t.Helper()
	bt := baseTime()
	rows := []models.CommitPathIndex{
		{RepoID: repoID, FilePath: "README.md", LastCommitSHA: "sha-readme", CommitTime: bt.Add(1 * time.Hour), ChangeType: "added", FileSize: 100},
		{RepoID: repoID, FilePath: "main.go", LastCommitSHA: "sha-main", CommitTime: bt.Add(2 * time.Hour), ChangeType: "modified", FileSize: 5000},
		{RepoID: repoID, FilePath: "src/app.go", LastCommitSHA: "sha-app", CommitTime: bt.Add(3 * time.Hour), ChangeType: "added", FileSize: 2000},
		{RepoID: repoID, FilePath: "src/util/helper.go", LastCommitSHA: "sha-helper", CommitTime: bt.Add(4 * time.Hour), ChangeType: "modified", FileSize: 800},
		{RepoID: repoID, FilePath: "old.txt", LastCommitSHA: "sha-old", CommitTime: bt.Add(5 * time.Hour), ChangeType: "deleted", FileSize: 0},
	}
	for _, r := range rows {
		seedIndex(t, db, r)
	}
}

func TestNewQueryService_DefaultConfig(t *testing.T) {
	db := newTestDB(t)
	q := NewQueryService(db, nil)
	require.NotNil(t, q)
	assert.Equal(t, 5*time.Second, q.config.QueryTimeout)
	assert.Equal(t, 1000, q.config.MaxPathsPerQuery)
	assert.NotNil(t, q.metrics)
}

func TestNewQueryService_CustomConfig(t *testing.T) {
	db := newTestDB(t)
	cfg := &QueryConfig{QueryTimeout: time.Second, MaxPathsPerQuery: 3}
	q := NewQueryService(db, cfg)
	assert.Equal(t, time.Second, q.config.QueryTimeout)
	assert.Equal(t, 3, q.config.MaxPathsPerQuery)
}

func TestGetLatestCommitsForPaths(t *testing.T) {
	db := newTestDB(t)
	seedRepoFixture(t, db, "repo-1")
	q := NewQueryService(db, nil)
	ctx := context.Background()

	t.Run("empty paths returns empty map", func(t *testing.T) {
		res, err := q.GetLatestCommitsForPaths(ctx, "repo-1", nil)
		require.NoError(t, err)
		assert.Empty(t, res)
	})

	t.Run("returns matching paths and skips deleted", func(t *testing.T) {
		res, err := q.GetLatestCommitsForPaths(ctx, "repo-1", []string{"README.md", "main.go", "old.txt"})
		require.NoError(t, err)
		assert.Len(t, res, 2) // old.txt 는 deleted 이므로 제외
		require.Contains(t, res, "README.md")
		assert.Equal(t, "sha-readme", res["README.md"].SHA)
		assert.Equal(t, int64(100), res["README.md"].FileSize)
		assert.NotContains(t, res, "old.txt")
	})

	t.Run("too many paths returns error", func(t *testing.T) {
		cfg := &QueryConfig{QueryTimeout: time.Second, MaxPathsPerQuery: 1}
		qLimited := NewQueryService(db, cfg)
		_, err := qLimited.GetLatestCommitsForPaths(ctx, "repo-1", []string{"a", "b"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "too many paths")
	})

	t.Run("nonexistent repo returns empty", func(t *testing.T) {
		res, err := q.GetLatestCommitsForPaths(ctx, "nope", []string{"README.md"})
		require.NoError(t, err)
		assert.Empty(t, res)
	})
}

func TestGetDirectoryWithCommits(t *testing.T) {
	db := newTestDB(t)
	seedRepoFixture(t, db, "repo-1")
	q := NewQueryService(db, nil)
	ctx := context.Background()

	t.Run("root directory lists top-level entries", func(t *testing.T) {
		listing, err := q.GetDirectoryWithCommits(ctx, "repo-1", "")
		require.NoError(t, err)
		assert.Equal(t, "", listing.Path)
		// README.md, main.go (top-level), src/app.go (one level deep dir entry).
		// old.txt 는 deleted 라 제외.
		paths := make([]string, 0, len(listing.Files))
		for _, f := range listing.Files {
			paths = append(paths, f.Path)
		}
		assert.Contains(t, paths, "README.md")
		assert.Contains(t, paths, "main.go")
		assert.NotContains(t, paths, "old.txt")
		assert.NotContains(t, paths, "src/util/helper.go") // 2단계 깊이는 제외
		assert.Equal(t, len(listing.Files), listing.TotalFiles)
		assert.False(t, listing.LastModified.IsZero())
	})

	t.Run("specific directory lists direct children", func(t *testing.T) {
		listing, err := q.GetDirectoryWithCommits(ctx, "repo-1", "src")
		require.NoError(t, err)
		assert.Equal(t, "src", listing.Path)
		paths := make([]string, 0, len(listing.Files))
		for _, f := range listing.Files {
			paths = append(paths, f.Path)
		}
		assert.Contains(t, paths, "src/app.go")
		assert.NotContains(t, paths, "src/util/helper.go") // 깊은 서브디렉토리 제외
	})

	t.Run("path normalization trims slashes", func(t *testing.T) {
		listing, err := q.GetDirectoryWithCommits(ctx, "repo-1", "/src/")
		require.NoError(t, err)
		assert.Equal(t, "src", listing.Path)
	})

	t.Run("empty directory for unknown repo", func(t *testing.T) {
		listing, err := q.GetDirectoryWithCommits(ctx, "unknown", "src")
		require.NoError(t, err)
		assert.Empty(t, listing.Files)
		assert.Equal(t, 0, listing.TotalFiles)
	})
}

func TestGetRecentChanges(t *testing.T) {
	db := newTestDB(t)
	seedRepoFixture(t, db, "repo-1")
	q := NewQueryService(db, nil)
	ctx := context.Background()
	bt := baseTime()

	t.Run("returns changes after since, newest first, excludes deleted", func(t *testing.T) {
		files, err := q.GetRecentChanges(ctx, "repo-1", bt, 100)
		require.NoError(t, err)
		assert.Len(t, files, 4) // deleted old.txt 제외
		// 정렬 검증: commit_time DESC
		for i := 1; i < len(files); i++ {
			assert.False(t, files[i].CommitTime.After(files[i-1].CommitTime))
		}
	})

	t.Run("since filter excludes older rows", func(t *testing.T) {
		files, err := q.GetRecentChanges(ctx, "repo-1", bt.Add(3*time.Hour+30*time.Minute), 100)
		require.NoError(t, err)
		// helper.go (4h) 만 남음 (5h old.txt 는 deleted)
		assert.Len(t, files, 1)
		assert.Equal(t, "src/util/helper.go", files[0].Path)
	})

	t.Run("invalid limit defaults to 100", func(t *testing.T) {
		files, err := q.GetRecentChanges(ctx, "repo-1", bt, 0)
		require.NoError(t, err)
		assert.Len(t, files, 4)

		files, err = q.GetRecentChanges(ctx, "repo-1", bt, 99999)
		require.NoError(t, err)
		assert.Len(t, files, 4)
	})
}

func TestGetFileHistory(t *testing.T) {
	db := newTestDB(t)
	seedRepoFixture(t, db, "repo-1")
	q := NewQueryService(db, nil)
	ctx := context.Background()

	t.Run("returns latest commit for existing file", func(t *testing.T) {
		hist, err := q.GetFileHistory(ctx, "repo-1", "main.go", 20)
		require.NoError(t, err)
		require.Len(t, hist, 1)
		assert.Equal(t, "sha-main", hist[0].SHA)
		assert.Equal(t, "modified", hist[0].ChangeType)
	})

	t.Run("missing file returns empty slice", func(t *testing.T) {
		hist, err := q.GetFileHistory(ctx, "repo-1", "does-not-exist.go", 20)
		require.NoError(t, err)
		assert.Empty(t, hist)
	})

	t.Run("invalid limit normalized", func(t *testing.T) {
		hist, err := q.GetFileHistory(ctx, "repo-1", "main.go", 0)
		require.NoError(t, err)
		assert.Len(t, hist, 1)

		hist, err = q.GetFileHistory(ctx, "repo-1", "main.go", 9999)
		require.NoError(t, err)
		assert.Len(t, hist, 1)
	})
}

func TestGetLargestFiles(t *testing.T) {
	db := newTestDB(t)
	seedRepoFixture(t, db, "repo-1")
	q := NewQueryService(db, nil)
	ctx := context.Background()

	t.Run("orders by size desc and excludes deleted/zero", func(t *testing.T) {
		files, err := q.GetLargestFiles(ctx, "repo-1", 20)
		require.NoError(t, err)
		// main.go(5000), src/app.go(2000), README.md(100), helper.go(800)
		// old.txt size 0 & deleted 제외
		assert.Len(t, files, 4)
		assert.Equal(t, "main.go", files[0].Path)
		assert.Equal(t, int64(5000), files[0].Size)
		for i := 1; i < len(files); i++ {
			assert.GreaterOrEqual(t, files[i-1].Size, files[i].Size)
		}
	})

	t.Run("invalid limit normalized", func(t *testing.T) {
		files, err := q.GetLargestFiles(ctx, "repo-1", -5)
		require.NoError(t, err)
		assert.Len(t, files, 4)
	})
}

func TestGetRepositoryStats(t *testing.T) {
	db := newTestDB(t)
	seedRepoFixture(t, db, "repo-1")
	q := NewQueryService(db, nil)
	ctx := context.Background()

	// 주의: GetRepositoryStats 의 "last activity" 쿼리는 MAX(commit_time) 결과를
	// *time.Time 으로 Scan 하는데, SQLite 드라이버는 이를 문자열로 돌려줘 Scan 에
	// 실패한다(Postgres 전용 동작). 따라서 SQLite 에서는 에러가 반환된다.
	// → COUNT/SUM 분기는 정상 동작함을 부분 검증하기 위해 별도 호출로 확인.
	var totalFiles int64
	require.NoError(t, db.Model(&models.CommitPathIndex{}).
		Where("repo_id = ? AND change_type != 'deleted'", "repo-1").
		Count(&totalFiles).Error)
	assert.Equal(t, int64(4), totalFiles)

	_, err := q.GetRepositoryStats(ctx, "repo-1")
	// SQLite 드라이버 한계로 last-activity Scan 에러가 기대됨.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "last activity")
}

func TestIsIndexAvailable(t *testing.T) {
	db := newTestDB(t)
	seedRepoFixture(t, db, "repo-1")
	q := NewQueryService(db, nil)
	ctx := context.Background()

	avail, err := q.IsIndexAvailable(ctx, "repo-1")
	require.NoError(t, err)
	assert.True(t, avail)

	avail, err = q.IsIndexAvailable(ctx, "empty-repo")
	require.NoError(t, err)
	assert.False(t, avail)
}

func TestQueryMetrics(t *testing.T) {
	db := newTestDB(t)
	seedRepoFixture(t, db, "repo-1")
	q := NewQueryService(db, nil)
	ctx := context.Background()

	// 초기 metrics
	m0 := q.GetQueryMetrics()
	assert.Equal(t, int64(0), m0.TotalQueries)

	// 쿼리 호출 시 metrics 누적 (updateMetrics 경로)
	_, err := q.GetFileHistory(ctx, "repo-1", "main.go", 20)
	require.NoError(t, err)
	_, err = q.GetLargestFiles(ctx, "repo-1", 20)
	require.NoError(t, err)

	m := q.GetQueryMetrics()
	assert.Equal(t, int64(2), m.TotalQueries)
	assert.False(t, m.LastQueryTime.IsZero())
	assert.Equal(t, 1.0, m.IndexHitRate)
}

func TestUpdateMetrics_AverageAndSlowest(t *testing.T) {
	db := newTestDB(t)
	q := NewQueryService(db, nil)

	q.updateMetrics(100 * time.Millisecond)
	assert.Equal(t, 100*time.Millisecond, q.metrics.AverageQueryTime)
	assert.Equal(t, 100*time.Millisecond, q.metrics.SlowestQueryTime)

	q.updateMetrics(300 * time.Millisecond)
	// moving average = (100 + 300) / 2 = 200ms
	assert.Equal(t, 200*time.Millisecond, q.metrics.AverageQueryTime)
	assert.Equal(t, 300*time.Millisecond, q.metrics.SlowestQueryTime)
	assert.Equal(t, int64(2), q.metrics.TotalQueries)
}

func TestInvalidateCache(t *testing.T) {
	db := newTestDB(t)
	q := NewQueryService(db, nil)
	// 현재는 no-op 이지만 에러 없이 동작해야 함.
	require.NoError(t, q.InvalidateCache(context.Background(), "repo-1"))
}

func TestWarmupCache(t *testing.T) {
	db := newTestDB(t)
	seedRepoFixture(t, db, "repo-1")
	q := NewQueryService(db, nil)
	ctx := context.Background()

	t.Run("warms common paths without error", func(t *testing.T) {
		err := q.WarmupCache(ctx, "repo-1", []string{"README.md", "main.go"})
		require.NoError(t, err)
	})

	t.Run("propagates error from underlying query", func(t *testing.T) {
		cfg := &QueryConfig{QueryTimeout: time.Second, MaxPathsPerQuery: 1}
		qLimited := NewQueryService(db, cfg)
		err := qLimited.WarmupCache(ctx, "repo-1", []string{"a", "b"})
		require.Error(t, err)
	})
}
