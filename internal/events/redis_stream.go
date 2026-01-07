package events

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// CommitEventType represents the type of commit event
type CommitEventType string

const (
	EventTypePush   CommitEventType = "push"
	EventTypeMerge  CommitEventType = "merge"
	EventTypeCommit CommitEventType = "commit"
)

// CommitEvent represents a commit creation event
type CommitEvent struct {
	RepoPath   string          // Relative path (e.g., work_space_name/uuid.git)
	EventType  CommitEventType // push, merge, commit
	CommitHash string          // Commit hash (e.g., 51f2da8...)
	OldRev     string          // Old commit hash (e.g., e2d382...) - empty for new refs
	RefName    string          // Branch or ref name (e.g., refs/heads/main)
	Pusher     string          // Pusher identifier (e.g., "git")
	Timestamp  time.Time       // Event timestamp
}

// RedisStreamClient provides an interface for publishing commit events to Redis Stream
type RedisStreamClient interface {
	AddCommitEventToStream(ctx context.Context, event *CommitEvent) error
	Close() error
}

// redisStreamClient implements RedisStreamClient using go-redis
// NOTE: use redis.UniversalClient so we can support both single-node and cluster.
type redisStreamClient struct {
	client redis.UniversalClient
	logger *zap.Logger
}

// GetRelativeRepoPath converts an absolute repository path to a relative path
// e.g., /var/lib/git/repos/owner/repo.git -> owner/repo.git
func GetRelativeRepoPath(fullPath, repoRoot string) string {
	// Normalize paths
	fullPath = filepath.Clean(fullPath)
	repoRoot = filepath.Clean(repoRoot)

	// Remove repoRoot prefix
	if strings.HasPrefix(fullPath, repoRoot) {
		relative := strings.TrimPrefix(fullPath, repoRoot)
		relative = strings.TrimPrefix(relative, "/")
		return relative
	}

	// If prefix doesn't match, return as-is (might already be relative)
	return fullPath
}

// NewRedisStreamClient creates a new Redis (single or cluster) stream client
//
// redisURL examples:
//   - single:  "redis://redis-piper-1:6379/0"
//   - cluster: "redis://redis-piper-1:6379/0,redis://redis-piper-2:6379/0,redis://redis-piper-3:6379/0"
func NewRedisStreamClient(redisURL string, logger *zap.Logger) (RedisStreamClient, error) {
	if logger == nil {
		var err error
		logger, err = zap.NewProduction()
		if err != nil {
			return nil, fmt.Errorf("failed to create default logger: %w", err)
		}
	}

	if strings.TrimSpace(redisURL) == "" {
		return nil, fmt.Errorf("empty redisURL")
	}

	// Cluster 여부 판단 (콤마 포함 여부로 간단 판별)
	if strings.Contains(redisURL, ",") {
		return newClusterRedisStreamClient(redisURL, logger)
	}

	return newSingleRedisStreamClient(redisURL, logger)
}

// 싱글 노드 전용 클라이언트 생성
func newSingleRedisStreamClient(redisURL string, logger *zap.Logger) (RedisStreamClient, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Redis URL: %w", err)
	}

	client := redis.NewClient(opt)

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		logger.Error("Failed to connect to Redis",
			zap.String("redis_url", redisURL),
			zap.String("address", opt.Addr),
			zap.Error(err))
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	return &redisStreamClient{
		client: client,
		logger: logger,
	}, nil
}

// 클러스터 전용 클라이언트 생성
//
// redisURLs 예:
//
//	"redis://redis-piper-1:6379/0,redis://redis-piper-2:6379/0,redis://redis-piper-3:6379/0"
func newClusterRedisStreamClient(redisURLs string, logger *zap.Logger) (RedisStreamClient, error) {
	rawURLs := strings.Split(redisURLs, ",")
	if len(rawURLs) == 0 {
		return nil, fmt.Errorf("no Redis cluster URLs provided")
	}

	var (
		addrs    []string
		username string
		password string
		// TLSConfig도 필요하면 첫 노드 기준으로 복사해서 넣어주면 됨.
		// var tlsConfig *tls.Config
	)

	for i, u := range rawURLs {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}

		opt, err := redis.ParseURL(u)
		if err != nil {
			return nil, fmt.Errorf("failed to parse Redis cluster URL %q: %w", u, err)
		}

		if i == 0 {
			username = opt.Username
			password = opt.Password
			// tlsConfig = opt.TLSConfig
		}

		// Cluster에서는 DB index는 무시되고 항상 0이므로 opt.DB는 신경 안 써도 됨.
		addrs = append(addrs, opt.Addr)
	}

	if len(addrs) == 0 {
		return nil, fmt.Errorf("no valid Redis cluster addresses parsed")
	}

	clusterOpts := &redis.ClusterOptions{
		Addrs:    addrs,
		Username: username,
		Password: password,
		// TLSConfig: tlsConfig,
	}

	client := redis.NewClusterClient(clusterOpts)

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		logger.Error("Failed to connect to Redis Cluster",
			zap.String("redis_urls", redisURLs),
			zap.Strings("addrs", addrs),
			zap.Error(err))
		return nil, fmt.Errorf("failed to connect to Redis Cluster: %w", err)
	}

	return &redisStreamClient{
		client: client,
		logger: logger,
	}, nil
}

// AddCommitEventToStream adds a commit event to Redis Stream
// Stream key format: git:events
// Message format: field-value pairs (event_type, repo_path, ref, old_rev, new_rev, pusher, timestamp)
func (r *redisStreamClient) AddCommitEventToStream(ctx context.Context, event *CommitEvent) error {
	// Build stream key: git:events (unified stream for all event types)
	streamKey := "git:events"

	// Convert timestamp to Unix timestamp
	timestamp := event.Timestamp.Unix()

	// Prepare message fields as key-value pairs
	values := map[string]interface{}{
		"event_type": string(event.EventType),
		"repo_path":  event.RepoPath,
		"ref":        event.RefName,
		"new_rev":    event.CommitHash,
		"pusher":     event.Pusher,
		"timestamp":  strconv.FormatInt(timestamp, 10),
	}

	// Add old_rev only if it's not empty
	if event.OldRev != "" {
		values["old_rev"] = event.OldRev
	}

	_, err := r.client.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		ID:     "*", // Auto-generate ID
		Values: values,
	}).Result()

	if err != nil {
		return fmt.Errorf("failed to add to Redis Stream: %w", err)
	}

	return nil
}

// Close closes the Redis client connection
func (r *redisStreamClient) Close() error {
	return r.client.Close()
}
