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
type redisStreamClient struct {
	client *redis.Client
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

// NewRedisStreamClient creates a new Redis stream client
// logger can be *zap.Logger
func NewRedisStreamClient(redisURL string, logger *zap.Logger) (RedisStreamClient, error) {
	if logger == nil {
		// Create a default logger if none provided
		var err error
		logger, err = zap.NewProduction()
		if err != nil {
			return nil, fmt.Errorf("failed to create default logger: %w", err)
		}
	}
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

	logger.Debug("Redis events client initialized",
		zap.String("address", opt.Addr),
		zap.Int("db", opt.DB))

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

	// Debug: Log event data before publishing
	r.logger.Debug("Publishing commit event to Redis Stream",
		zap.String("stream_key", streamKey),
		zap.String("event_type", string(event.EventType)),
		zap.String("repo_path", event.RepoPath),
		zap.String("commit_hash", event.CommitHash),
		zap.String("ref_name", event.RefName),
		zap.String("pusher", event.Pusher),
		zap.Time("timestamp", event.Timestamp),
		zap.Int64("unix_timestamp", timestamp),
		zap.Any("values", values))

	// Add to Redis Stream using XADD
	// Use * for auto-generated ID
	r.logger.Debug("Executing XADD command",
		zap.String("stream_key", streamKey),
		zap.String("id", "*"))

	streamID, err := r.client.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		ID:     "*", // Auto-generate ID
		Values: values,
	}).Result()

	if err != nil {
		r.logger.Error("Failed to publish commit event to Redis Stream",
			zap.String("stream_key", streamKey),
			zap.String("event_type", string(event.EventType)),
			zap.String("commit_hash", event.CommitHash),
			zap.String("ref_name", event.RefName),
			zap.String("repo_path", event.RepoPath),
			zap.Any("values", values),
			zap.Error(err))
		return fmt.Errorf("failed to add to Redis Stream: %w", err)
	}

	r.logger.Info("Successfully published commit event to Redis Stream",
		zap.String("stream_key", streamKey),
		zap.String("stream_id", streamID),
		zap.String("repo_path", event.RepoPath),
		zap.String("event_type", string(event.EventType)),
		zap.String("commit_hash", event.CommitHash),
		zap.String("ref_name", event.RefName),
		zap.String("pusher", event.Pusher),
		zap.Int64("timestamp", timestamp),
		zap.Time("timestamp_time", event.Timestamp))

	// Debug: Verify the event was added by checking stream length
	streamLen, err := r.client.XLen(ctx, streamKey).Result()
	if err != nil {
		r.logger.Warn("Failed to get stream length after publishing",
			zap.String("stream_key", streamKey),
			zap.Error(err))
	} else {
		r.logger.Debug("Redis Stream length after publishing",
			zap.String("stream_key", streamKey),
			zap.Int64("length", streamLen),
			zap.String("new_stream_id", streamID))
	}

	return nil
}

// Close closes the Redis client connection
func (r *redisStreamClient) Close() error {
	return r.client.Close()
}
