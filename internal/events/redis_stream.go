package events

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
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
	EventID       string          // Unique event ID (e.g., evt-git-1768797509975-0)
	RepoPath      string          // Relative path (e.g., work_space_name/uuid.git)
	EventType     CommitEventType // push, merge, commit
	CommitHash    string          // Commit hash (e.g., 51f2da8...)
	OldRev        string          // Old commit hash (e.g., e2d382...) - empty for new refs
	RefName       string          // Branch or ref name (e.g., refs/heads/main)
	Pusher        string          // Pusher identifier (e.g., "git")
	UserID        string          // User ID from X-USER-ID header
	CommitMessage string          // Commit message
	Timestamp     time.Time       // Event timestamp
}

// eventData represents the JSON structure for Redis Stream data field
type eventData struct {
	EventID       string `json:"event_id"`
	Timestamp     string `json:"timestamp"`
	RepoPath      string `json:"repo_path"`
	NewRev        string `json:"new_rev"`
	Ref           string `json:"ref"`
	EventType     string `json:"event_type"`
	CommitMessage string `json:"commit_message"` // Always include, even if empty
	OldRev        string `json:"old_rev,omitempty"`
	UserID        string `json:"user_id,omitempty"`
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

// generateEventID generates a unique event ID
func generateEventID() string {
	// Format: evt-git-{timestamp}-{random}
	now := time.Now()
	timestamp := now.UnixNano() / int64(time.Millisecond)
	// Simple random suffix (in production, use crypto/rand)
	randomSuffix := now.Nanosecond() % 1000
	return fmt.Sprintf("evt-git-%d-%d", timestamp, randomSuffix)
}

// AddCommitEventToStream adds a commit event to Redis Stream
// Stream key format: git:events
// Message format: JSON string in data field
func (r *redisStreamClient) AddCommitEventToStream(ctx context.Context, event *CommitEvent) error {
	if event == nil {
		return fmt.Errorf("event cannot be nil")
	}
	// Build stream key: git:events (unified stream for all event types)
	streamKey := "git:events"

	// Generate event ID if not provided
	eventID := event.EventID
	if eventID == "" {
		eventID = generateEventID()
	}

	// Convert timestamp to RFC3339 format
	timestamp := event.Timestamp.Format(time.RFC3339)

	// Build event data structure
	data := eventData{
		EventID:       eventID,
		Timestamp:     timestamp,
		RepoPath:      event.RepoPath,
		NewRev:        event.CommitHash,
		Ref:           event.RefName,
		EventType:     string(event.EventType),
		CommitMessage: event.CommitMessage,
	}

	// Add optional fields
	if event.OldRev != "" {
		data.OldRev = event.OldRev
	}
	if event.UserID != "" {
		data.UserID = event.UserID
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(data)
	if err != nil {
		r.logger.Error("Failed to marshal event data to JSON",
			zap.String("event_id", eventID),
			zap.Error(err))
		return fmt.Errorf("failed to marshal event data: %w", err)
	}

	// Prepare message with data field as JSON string
	values := map[string]interface{}{
		"data": string(jsonData),
	}

	// Add to Redis Stream using XADD
	// Use * for auto-generated ID
	streamID, err := r.client.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		ID:     "*", // Auto-generate ID
		Values: values,
	}).Result()

	if err != nil {
		r.logger.Error("Failed to publish commit event to Redis Stream",
			zap.String("stream_key", streamKey),
			zap.String("event_id", eventID),
			zap.String("event_type", string(event.EventType)),
			zap.String("commit_hash", event.CommitHash),
			zap.String("ref_name", event.RefName),
			zap.String("repo_path", event.RepoPath),
			zap.Error(err))
		return fmt.Errorf("failed to add to Redis Stream: %w", err)
	}

	r.logger.Info("Successfully published commit event to Redis Stream",
		zap.String("stream_key", streamKey),
		zap.String("stream_id", streamID),
		zap.String("event_id", eventID),
		zap.String("repo_path", event.RepoPath),
		zap.String("event_type", string(event.EventType)),
		zap.String("commit_hash", event.CommitHash),
		zap.String("ref_name", event.RefName),
		zap.String("user_id", event.UserID),
		zap.String("timestamp", timestamp))

	// Debug log for user_id tracking
	r.logger.Debug("Commit event published with user context",
		zap.String("event_id", eventID),
		zap.String("user_id", event.UserID),
		zap.String("event_type", string(event.EventType)),
		zap.String("commit_hash", event.CommitHash),
		zap.String("ref_name", event.RefName),
		zap.String("commit_message", event.CommitMessage))

	return nil
}

// Close closes the Redis client connection
func (r *redisStreamClient) Close() error {
	return r.client.Close()
}
