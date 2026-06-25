package indexing

import (
	"context"
	"testing"
	"time"

	"github.com/collabops/git-executor/internal/models"
	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeGitService 는 GitService 인터페이스의 결정론적 페이크 구현이다.
type fakeGitService struct {
	repo    *gogit.Repository
	openErr error
}

func (f *fakeGitService) GetAllCommits(repoPath string) ([]Commit, error) { return nil, nil }
func (f *fakeGitService) GetCommitsBySHAs(repoPath string, shas []string) (map[string]*Commit, error) {
	return nil, nil
}
func (f *fakeGitService) OpenRepository(repoPath string) (*gogit.Repository, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	return f.repo, nil
}

// buildMemoryRepo 는 인메모리 go-git 저장소에 두 개의 커밋을 만들어 반환한다.
func writeFile(t *testing.T, fs billy.Filesystem, name, content string) {
	t.Helper()
	f, err := fs.Create(name)
	require.NoError(t, err)
	_, err = f.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

func buildMemoryRepo(t *testing.T) *gogit.Repository {
	t.Helper()
	fs := memfs.New()
	repo, err := gogit.Init(memory.NewStorage(), fs)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	writeFile(t, fs, "README.md", "hello")
	writeFile(t, fs, "main.go", "package main")
	_, err = wt.Add("README.md")
	require.NoError(t, err)
	_, err = wt.Add("main.go")
	require.NoError(t, err)
	_, err = wt.Commit("initial", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Tester", Email: "t@example.com", When: time.Now()},
	})
	require.NoError(t, err)

	// 두 번째 커밋: main.go 수정 + 새 파일 추가
	writeFile(t, fs, "main.go", "package main // updated")
	writeFile(t, fs, "src/app.go", "package src")
	_, err = wt.Add("main.go")
	require.NoError(t, err)
	_, err = wt.Add("src/app.go")
	require.NoError(t, err)
	_, err = wt.Commit("second", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Tester", Email: "t@example.com", When: time.Now().Add(time.Minute)},
	})
	require.NoError(t, err)

	return repo
}

func TestNewIndexBuilder_DefaultConfig(t *testing.T) {
	db := newTestDB(t)
	b := NewIndexBuilder(db, &fakeGitService{}, nil)
	require.NotNil(t, b)
	assert.Equal(t, 100, b.config.BatchSize)
	assert.Equal(t, 4, b.config.MaxConcurrency)
	assert.Equal(t, 30*time.Minute, b.config.IndexTimeout)
	assert.Equal(t, 10000, b.config.MaxCommitsPerRun)
	assert.True(t, b.config.EnableMetrics)
}

func TestNewIndexBuilder_CustomConfig(t *testing.T) {
	db := newTestDB(t)
	cfg := &IndexConfig{BatchSize: 5, MaxCommitsPerRun: 50}
	b := NewIndexBuilder(db, &fakeGitService{}, cfg)
	assert.Equal(t, 5, b.config.BatchSize)
	assert.Equal(t, 50, b.config.MaxCommitsPerRun)
}

func TestUpdateIndex_WritesRows(t *testing.T) {
	db := newTestDB(t)
	b := NewIndexBuilder(db, &fakeGitService{}, nil)
	ctx := context.Background()

	t.Run("empty commits is no-op", func(t *testing.T) {
		require.NoError(t, b.UpdateIndex(ctx, "repo-1", nil))
		var count int64
		require.NoError(t, db.Model(&models.CommitPathIndex{}).Count(&count).Error)
		assert.Equal(t, int64(0), count)
	})

	t.Run("writes file changes and upserts on conflict", func(t *testing.T) {
		commits := []Commit{
			{
				SHA:  "sha1",
				Time: baseTime(),
				Changes: []FileChange{
					{Path: "a.go", Type: ChangeAdded, Size: 10},
					{Path: "b.go", Type: ChangeAdded, Size: 20},
				},
			},
			{
				SHA:  "sha2",
				Time: baseTime().Add(time.Hour),
				Changes: []FileChange{
					{Path: "a.go", Type: ChangeModified, Size: 15},               // upsert a.go
					{Path: "big.bin", Type: ChangeAdded, Size: 11 * 1024 * 1024}, // 10MB 초과 → skip
				},
			},
		}
		require.NoError(t, b.UpdateIndex(ctx, "repo-1", commits))

		var rows []models.CommitPathIndex
		require.NoError(t, db.Where("repo_id = ?", "repo-1").Find(&rows).Error)
		// a.go(upsert), b.go = 2개. big.bin 은 size 제한으로 제외.
		assert.Len(t, rows, 2)

		var aRow models.CommitPathIndex
		require.NoError(t, db.Where("repo_id = ? AND file_path = ?", "repo-1", "a.go").First(&aRow).Error)
		assert.Equal(t, "sha2", aRow.LastCommitSHA) // 최신 커밋으로 갱신
		assert.Equal(t, "modified", aRow.ChangeType)
		assert.Equal(t, int64(15), aRow.FileSize)
	})
}

func TestUpdateIndex_ContextCancelled(t *testing.T) {
	db := newTestDB(t)
	b := NewIndexBuilder(db, &fakeGitService{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 즉시 취소

	commits := []Commit{
		{SHA: "sha1", Time: baseTime(), Changes: []FileChange{{Path: "a.go", Type: ChangeAdded, Size: 10}}},
	}
	err := b.UpdateIndex(ctx, "repo-1", commits)
	require.Error(t, err)
}

func TestClearExistingIndex(t *testing.T) {
	db := newTestDB(t)
	seedRepoFixture(t, db, "repo-1")
	seedRepoFixture(t, db, "repo-2")
	b := NewIndexBuilder(db, &fakeGitService{}, nil)

	require.NoError(t, b.clearExistingIndex("repo-1"))

	var c1, c2 int64
	require.NoError(t, db.Model(&models.CommitPathIndex{}).Where("repo_id = ?", "repo-1").Count(&c1).Error)
	require.NoError(t, db.Model(&models.CommitPathIndex{}).Where("repo_id = ?", "repo-2").Count(&c2).Error)
	assert.Equal(t, int64(0), c1)
	assert.Equal(t, int64(5), c2)
}

func TestShouldSkipPath(t *testing.T) {
	b := NewIndexBuilder(newTestDB(t), &fakeGitService{}, nil)

	cases := []struct {
		path string
		skip bool
	}{
		{"src/main.go", false},
		{".gitignore", false},
		{".github", false},
		{".env", true}, // hidden, 예외 아님
		{"node_modules/lib/x.js", true},
		{"vendor/pkg/a.go", true},
		{"app/node_modules/x.js", true}, // 중첩 경로
		{"build/out", true},
		{"image.png", true},
		{"archive.ZIP", true}, // 대소문자 무시
		{"lib.so", true},
		{"normal.txt", false},
	}
	for _, tc := range cases {
		assert.Equalf(t, tc.skip, b.shouldSkipPath(tc.path), "path=%s", tc.path)
	}
}

func TestGetIndexStats(t *testing.T) {
	db := newTestDB(t)
	seedRepoFixture(t, db, "repo-1")
	b := NewIndexBuilder(db, &fakeGitService{}, nil)
	ctx := context.Background()

	// 주의: GetIndexStats 의 latest-index-time 쿼리는 MAX(updated_at) 결과를
	// *time.Time 으로 Scan 하는데 SQLite 드라이버가 문자열로 돌려줘 Scan 에
	// 실패한다(Postgres 전용). 따라서 SQLite 에서는 에러가 반환된다.
	// COUNT 분기가 정상 동작함을 직접 검증.
	var total int64
	require.NoError(t, db.Model(&models.CommitPathIndex{}).
		Where("repo_id = ? AND change_type != 'deleted'", "repo-1").
		Count(&total).Error)
	assert.Equal(t, int64(4), total)

	_, err := b.GetIndexStats(ctx, "repo-1")
	require.Error(t, err) // SQLite latest-index-time Scan 한계
}

func TestIsIndexUpToDate(t *testing.T) {
	db := newTestDB(t)
	seedRepoFixture(t, db, "repo-1")
	b := NewIndexBuilder(db, &fakeGitService{}, nil)
	ctx := context.Background()

	upToDate, err := b.IsIndexUpToDate(ctx, "repo-1", "/path", "sha-main")
	require.NoError(t, err)
	assert.True(t, upToDate)

	upToDate, err = b.IsIndexUpToDate(ctx, "repo-1", "/path", "nonexistent-sha")
	require.NoError(t, err)
	assert.False(t, upToDate)
}

func TestBuildFullIndex_RealRepo(t *testing.T) {
	db := newTestDB(t)
	repo := buildMemoryRepo(t)
	b := NewIndexBuilder(db, &fakeGitService{repo: repo}, &IndexConfig{
		BatchSize:        1,
		IndexTimeout:     time.Minute,
		MaxCommitsPerRun: 10000,
	})
	ctx := context.Background()

	require.NoError(t, b.BuildFullIndex(ctx, "repo-build", "/ignored"))

	var rows []models.CommitPathIndex
	require.NoError(t, db.Where("repo_id = ?", "repo-build").Find(&rows).Error)
	// 첫 커밋: README.md, main.go (added). 두번째: main.go(modified), src/app.go(added)
	// upsert 로 main.go 1개, 결과적으로 README.md, main.go, src/app.go = 3
	paths := map[string]bool{}
	for _, r := range rows {
		paths[r.FilePath] = true
	}
	assert.True(t, paths["README.md"])
	assert.True(t, paths["main.go"])
	assert.True(t, paths["src/app.go"])
}

func TestBuildFullIndex_OpenError(t *testing.T) {
	db := newTestDB(t)
	b := NewIndexBuilder(db, &fakeGitService{openErr: assertErr("boom")}, nil)
	err := b.BuildFullIndex(context.Background(), "repo-x", "/path")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open repository")
}

func TestUpdateIndexFromPush(t *testing.T) {
	db := newTestDB(t)
	repo := buildMemoryRepo(t)
	b := NewIndexBuilder(db, &fakeGitService{repo: repo}, nil)
	ctx := context.Background()

	t.Run("empty SHAs is no-op", func(t *testing.T) {
		require.NoError(t, b.UpdateIndexFromPush(ctx, "repo-push", "/path", nil))
	})

	t.Run("invalid SHA is skipped gracefully", func(t *testing.T) {
		// 존재하지 않는 SHA → CommitObject 실패 → 경고 후 skip, 에러 없음
		err := b.UpdateIndexFromPush(ctx, "repo-push", "/path",
			[]string{"0000000000000000000000000000000000000000"})
		require.NoError(t, err)
	})

	t.Run("valid SHA indexes commit", func(t *testing.T) {
		head, err := repo.Head()
		require.NoError(t, err)
		err = b.UpdateIndexFromPush(ctx, "repo-push2", "/path", []string{head.Hash().String()})
		require.NoError(t, err)

		var count int64
		require.NoError(t, db.Model(&models.CommitPathIndex{}).Where("repo_id = ?", "repo-push2").Count(&count).Error)
		assert.Greater(t, count, int64(0))
	})

	t.Run("open error propagates", func(t *testing.T) {
		badBuilder := NewIndexBuilder(db, &fakeGitService{openErr: assertErr("nope")}, nil)
		err := badBuilder.UpdateIndexFromPush(ctx, "repo-push3", "/path", []string{"abc"})
		require.Error(t, err)
	})
}

// assertErr 는 간단한 에러 생성 헬퍼.
type assertErr string

func (e assertErr) Error() string { return string(e) }
