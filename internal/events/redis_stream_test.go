package events

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// getTestRedisURL returns Redis URL for testing
// Uses REDIS_URL environment variable or defaults to localhost:6379
func getTestRedisURL() string {
	url := os.Getenv("REDIS_URL")
	if url == "" {
		url = "redis://localhost:6379/0"
	}
	return url
}

func TestNewRedisStreamClient(t *testing.T) {
	redisURL := getTestRedisURL()
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))

	client, err := NewRedisStreamClient(redisURL, logger)
	require.NoError(t, err, "Should create Redis stream client successfully")
	require.NotNil(t, client, "Client should not be nil")

	defer func() {
		if err := client.Close(); err != nil {
			t.Logf("Failed to close client: %v", err)
		}
	}()
}

func TestPublishCommitEvent(t *testing.T) {
	redisURL := getTestRedisURL()
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))

	// Create Redis stream client
	client, err := NewRedisStreamClient(redisURL, logger)
	require.NoError(t, err, "Should create Redis stream client")
	defer func() {
		if err := client.Close(); err != nil {
			t.Logf("Failed to close client: %v", err)
		}
	}()

	// Create test event
	now := time.Now()
	event := &CommitEvent{
		RepoPath:   "/test/repo.git",
		EventType:  EventTypeCommit,
		CommitHash: "abc123def456",
		RefName:    "refs/heads/main",
		Pusher:     "test-user",
		Timestamp:  now,
	}

	// Publish event
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.PublishCommitEvent(ctx, event)
	require.NoError(t, err, "Should publish event successfully")

	// Verify event was published by reading from Redis Stream
	opt, err := redis.ParseURL(redisURL)
	require.NoError(t, err)

	redisClient := redis.NewClient(opt)
	defer redisClient.Close()

	streamKey := "git:events:commit"

	// Read the last message from the stream
	messages, err := redisClient.XRevRange(ctx, streamKey, "+", "-").Result()
	require.NoError(t, err, "Should read from stream")
	require.NotEmpty(t, messages, "Stream should contain at least one message")

	// Find our message (check the most recent ones)
	found := false
	for _, msg := range messages {
		if msg.Values["repo_path"] == event.RepoPath &&
			msg.Values["new_rev"] == event.CommitHash &&
			msg.Values["ref"] == event.RefName {
			found = true

			// Verify all fields
			assert.Equal(t, event.RepoPath, msg.Values["repo_path"])
			assert.Equal(t, event.CommitHash, msg.Values["new_rev"])
			assert.Equal(t, event.RefName, msg.Values["ref"])
			assert.Equal(t, event.Pusher, msg.Values["pusher"])

			// Verify timestamp (allow small difference due to conversion)
			timestampStr, ok := msg.Values["timestamp"].(string)
			require.True(t, ok, "Timestamp should be string")
			timestamp, err := strconv.ParseInt(timestampStr, 10, 64)
			require.NoError(t, err)
			expectedTimestamp := event.Timestamp.Unix()
			assert.Equal(t, expectedTimestamp, timestamp, "Timestamp should match")

			break
		}
	}

	assert.True(t, found, "Should find the published event in stream")
}

func TestPublishPushEvent(t *testing.T) {
	redisURL := getTestRedisURL()
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))

	client, err := NewRedisStreamClient(redisURL, logger)
	require.NoError(t, err)
	defer func() {
		if err := client.Close(); err != nil {
			t.Logf("Failed to close client: %v", err)
		}
	}()

	event := &CommitEvent{
		RepoPath:   "/test/repo.git",
		EventType:  EventTypePush,
		CommitHash: "push123hash",
		RefName:    "refs/heads/feature",
		Pusher:     "git",
		Timestamp:  time.Now(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.PublishCommitEvent(ctx, event)
	require.NoError(t, err)

	// Verify push event in correct stream
	opt, err := redis.ParseURL(redisURL)
	require.NoError(t, err)

	redisClient := redis.NewClient(opt)
	defer redisClient.Close()

	streamKey := "git:events:push"
	messages, err := redisClient.XRevRange(ctx, streamKey, "+", "-").Result()
	require.NoError(t, err)
	require.NotEmpty(t, messages)

	found := false
	for _, msg := range messages {
		if msg.Values["new_rev"] == event.CommitHash {
			found = true
			assert.Equal(t, event.EventType, EventTypePush)
			assert.Equal(t, event.CommitHash, msg.Values["new_rev"])
			break
		}
	}

	assert.True(t, found, "Should find push event in push stream")
}

func TestPublishMergeEvent(t *testing.T) {
	redisURL := getTestRedisURL()
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))

	client, err := NewRedisStreamClient(redisURL, logger)
	require.NoError(t, err)
	defer func() {
		if err := client.Close(); err != nil {
			t.Logf("Failed to close client: %v", err)
		}
	}()

	event := &CommitEvent{
		RepoPath:   "/test/repo.git",
		EventType:  EventTypeMerge,
		CommitHash: "merge456hash",
		RefName:    "refs/heads/main",
		Pusher:     "git",
		Timestamp:  time.Now(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.PublishCommitEvent(ctx, event)
	require.NoError(t, err)

	// Verify merge event in correct stream
	opt, err := redis.ParseURL(redisURL)
	require.NoError(t, err)

	redisClient := redis.NewClient(opt)
	defer redisClient.Close()

	streamKey := "git:events:merge"
	messages, err := redisClient.XRevRange(ctx, streamKey, "+", "-").Result()
	require.NoError(t, err)
	require.NotEmpty(t, messages)

	found := false
	for _, msg := range messages {
		if msg.Values["new_rev"] == event.CommitHash {
			found = true
			assert.Equal(t, event.EventType, EventTypeMerge)
			assert.Equal(t, event.CommitHash, msg.Values["new_rev"])
			break
		}
	}

	assert.True(t, found, "Should find merge event in merge stream")
}

func TestPublishCommitEvent_StreamLength(t *testing.T) {
	redisURL := getTestRedisURL()
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))

	client, err := NewRedisStreamClient(redisURL, logger)
	require.NoError(t, err)
	defer func() {
		if err := client.Close(); err != nil {
			t.Logf("Failed to close client: %v", err)
		}
	}()

	opt, err := redis.ParseURL(redisURL)
	require.NoError(t, err)

	redisClient := redis.NewClient(opt)
	defer redisClient.Close()

	streamKey := "git:events:commit"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Get initial stream length
	initialLength, err := redisClient.XLen(ctx, streamKey).Result()
	require.NoError(t, err)

	// Publish event
	event := &CommitEvent{
		RepoPath:   "/test/repo.git",
		EventType:  EventTypeCommit,
		CommitHash: "length-test-hash",
		RefName:    "refs/heads/test",
		Pusher:     "test",
		Timestamp:  time.Now(),
	}

	err = client.PublishCommitEvent(ctx, event)
	require.NoError(t, err)

	// Wait a bit for the event to be processed
	time.Sleep(100 * time.Millisecond)

	// Verify stream length increased
	finalLength, err := redisClient.XLen(ctx, streamKey).Result()
	require.NoError(t, err)
	assert.Greater(t, finalLength, initialLength, "Stream length should increase after publishing event")
}

func TestPublishCommitEvent_MultipleEvents(t *testing.T) {
	redisURL := getTestRedisURL()
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))

	client, err := NewRedisStreamClient(redisURL, logger)
	require.NoError(t, err)
	defer func() {
		if err := client.Close(); err != nil {
			t.Logf("Failed to close client: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Publish multiple events
	events := []*CommitEvent{
		{
			RepoPath:   "/test/repo1.git",
			EventType:  EventTypeCommit,
			CommitHash: "hash1",
			RefName:    "refs/heads/branch1",
			Pusher:     "user1",
			Timestamp:  time.Now(),
		},
		{
			RepoPath:   "/test/repo2.git",
			EventType:  EventTypeCommit,
			CommitHash: "hash2",
			RefName:    "refs/heads/branch2",
			Pusher:     "user2",
			Timestamp:  time.Now(),
		},
		{
			RepoPath:   "/test/repo3.git",
			EventType:  EventTypeCommit,
			CommitHash: "hash3",
			RefName:    "refs/heads/branch3",
			Pusher:     "user3",
			Timestamp:  time.Now(),
		},
	}

	for _, event := range events {
		err := client.PublishCommitEvent(ctx, event)
		require.NoError(t, err, "Should publish each event successfully")
	}

	// Verify all events were published
	opt, err := redis.ParseURL(redisURL)
	require.NoError(t, err)

	redisClient := redis.NewClient(opt)
	defer redisClient.Close()

	streamKey := "git:events:commit"
	messages, err := redisClient.XRevRange(ctx, streamKey, "+", "-").Result()
	require.NoError(t, err)

	// Check that all events are in the stream
	foundHashes := make(map[string]bool)
	for _, msg := range messages {
		if hash, ok := msg.Values["new_rev"].(string); ok {
			foundHashes[hash] = true
		}
	}

	for _, event := range events {
		assert.True(t, foundHashes[event.CommitHash],
			"Should find commit hash %s in stream", event.CommitHash)
	}
}
