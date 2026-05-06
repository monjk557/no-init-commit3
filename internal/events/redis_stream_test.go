package events

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// setupTestRedis는 테스트가 사용할 Redis URL을 반환합니다.
// 기본은 in-process miniredis 인스턴스 — docker/외부 인프라 의존 없음, t.Cleanup
// 으로 자동 종료. REDIS_URL 환경변수가 있으면 그 진짜 Redis 를 그대로 사용
// (로컬에서 진짜 인스턴스 대상 디버깅용 escape hatch).
//
// Issue-2197: 이전 구현이 항상 localhost:6379 를 찔러 Redis 없는 CI/dev 환경에서
// 16 건 모두 connect refused 로 실패. miniredis 는 본 패키지가 쓰는 stream/
// XADD/XLEN/XRANGE 등을 모두 지원하므로 prod 코드 변경 없이 그대로 통과.
func setupTestRedis(t *testing.T) string {
	t.Helper()
	if url := os.Getenv("REDIS_URL"); url != "" {
		return url
	}
	mr := miniredis.RunT(t)
	return "redis://" + mr.Addr() + "/0"
}

func TestNewRedisStreamClient(t *testing.T) {
	redisURL := setupTestRedis(t)
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

func TestAddCommitEventToStream(t *testing.T) {
	redisURL := setupTestRedis(t)
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
		RepoPath:      "/test/repo.git",
		EventType:     EventTypeCommit,
		CommitHash:    "abc123def456",
		RefName:       "refs/heads/main",
		Pusher:        "test-user",
		UserID:        "test-user-id-123",
		CommitMessage: "Test commit message",
		Timestamp:     now,
	}

	// Publish event
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.AddCommitEventToStream(ctx, event)
	require.NoError(t, err, "Should publish event successfully")

	// Verify event was published by reading from Redis Stream
	opt, err := redis.ParseURL(redisURL)
	require.NoError(t, err)

	redisClient := redis.NewClient(opt)
	defer func() { _ = redisClient.Close() }()

	streamKey := "git:events"

	// Read the last message from the stream
	messages, err := redisClient.XRevRange(ctx, streamKey, "+", "-").Result()
	require.NoError(t, err, "Should read from stream")
	require.NotEmpty(t, messages, "Stream should contain at least one message")

	// Find our message (check the most recent ones)
	found := false
	for _, msg := range messages {
		// Check if data field exists and contains JSON
		dataField, ok := msg.Values["data"].(string)
		if !ok {
			continue
		}

		// Parse JSON data
		var eventData map[string]interface{}
		err := json.Unmarshal([]byte(dataField), &eventData)
		if err != nil {
			continue
		}

		// Check if this is our event
		if eventData["repo_path"] == event.RepoPath &&
			eventData["new_rev"] == event.CommitHash &&
			eventData["ref"] == event.RefName {
			found = true

			// Verify all fields
			assert.Equal(t, event.RepoPath, eventData["repo_path"])
			assert.Equal(t, event.CommitHash, eventData["new_rev"])
			assert.Equal(t, event.RefName, eventData["ref"])
			assert.Equal(t, string(event.EventType), eventData["event_type"])
			assert.Equal(t, event.UserID, eventData["user_id"])
			assert.Equal(t, event.CommitMessage, eventData["commit_message"])

			// Verify event_id exists
			_, hasEventID := eventData["event_id"]
			assert.True(t, hasEventID, "Event ID should be present")

			// Verify timestamp format (RFC3339)
			timestampStr, ok := eventData["timestamp"].(string)
			require.True(t, ok, "Timestamp should be string")
			_, err = time.Parse(time.RFC3339, timestampStr)
			require.NoError(t, err, "Timestamp should be in RFC3339 format")

			break
		}
	}

	assert.True(t, found, "Should find the published event in stream")
}

func TestAddCommitEventToStream_WithUserID(t *testing.T) {
	redisURL := setupTestRedis(t)
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))

	client, err := NewRedisStreamClient(redisURL, logger)
	require.NoError(t, err)
	defer func() {
		if err := client.Close(); err != nil {
			t.Logf("Failed to close client: %v", err)
		}
	}()

	// Test event with user ID
	event := &CommitEvent{
		RepoPath:      "/test/repo.git",
		EventType:     EventTypeCommit,
		CommitHash:    "user-test-hash",
		RefName:       "refs/heads/main",
		Pusher:        "git",
		UserID:        "user-12345",
		CommitMessage: "Test commit with user",
		Timestamp:     time.Now(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.AddCommitEventToStream(ctx, event)
	require.NoError(t, err)

	// Verify user_id is in the stream
	opt, err := redis.ParseURL(redisURL)
	require.NoError(t, err)

	redisClient := redis.NewClient(opt)
	defer func() { _ = redisClient.Close() }()

	streamKey := "git:events"
	messages, err := redisClient.XRevRange(ctx, streamKey, "+", "-").Result()
	require.NoError(t, err)

	found := false
	for _, msg := range messages {
		dataField, ok := msg.Values["data"].(string)
		if !ok {
			continue
		}

		var eventData map[string]interface{}
		err := json.Unmarshal([]byte(dataField), &eventData)
		if err != nil {
			continue
		}

		if eventData["new_rev"] == event.CommitHash {
			found = true
			assert.Equal(t, event.UserID, eventData["user_id"], "User ID should be present in stream")
			break
		}
	}

	assert.True(t, found, "Should find event with user_id in stream")
}

func TestAddCommitEventToStream_WithoutUserID(t *testing.T) {
	redisURL := setupTestRedis(t)
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))

	client, err := NewRedisStreamClient(redisURL, logger)
	require.NoError(t, err)
	defer func() {
		if err := client.Close(); err != nil {
			t.Logf("Failed to close client: %v", err)
		}
	}()

	// Test event without user ID (empty string)
	event := &CommitEvent{
		RepoPath:      "/test/repo.git",
		EventType:     EventTypeCommit,
		CommitHash:    "no-user-hash",
		RefName:       "refs/heads/main",
		Pusher:        "git",
		UserID:        "", // Empty user ID
		CommitMessage: "Test commit without user",
		Timestamp:     time.Now(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.AddCommitEventToStream(ctx, event)
	require.NoError(t, err)

	// Verify user_id is not in the stream (empty string means field is not added)
	opt, err := redis.ParseURL(redisURL)
	require.NoError(t, err)

	redisClient := redis.NewClient(opt)
	defer func() { _ = redisClient.Close() }()

	streamKey := "git:events"
	messages, err := redisClient.XRevRange(ctx, streamKey, "+", "-").Result()
	require.NoError(t, err)

	found := false
	for _, msg := range messages {
		dataField, ok := msg.Values["data"].(string)
		if !ok {
			continue
		}

		var eventData map[string]interface{}
		err := json.Unmarshal([]byte(dataField), &eventData)
		if err != nil {
			continue
		}

		if eventData["new_rev"] == event.CommitHash {
			found = true
			// user_id should not be present if empty
			_, exists := eventData["user_id"]
			assert.False(t, exists, "user_id should not be present when empty")
			break
		}
	}

	assert.True(t, found, "Should find event in stream")
}

func TestAddPushEventToStream(t *testing.T) {
	redisURL := setupTestRedis(t)
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))

	client, err := NewRedisStreamClient(redisURL, logger)
	require.NoError(t, err)
	defer func() {
		if err := client.Close(); err != nil {
			t.Logf("Failed to close client: %v", err)
		}
	}()

	event := &CommitEvent{
		RepoPath:      "/test/repo.git",
		EventType:     EventTypePush,
		CommitHash:    "push123hash",
		RefName:       "refs/heads/feature",
		Pusher:        "git",
		UserID:        "test-user-id-push",
		CommitMessage: "Test push commit",
		Timestamp:     time.Now(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.AddCommitEventToStream(ctx, event)
	require.NoError(t, err)

	// Verify push event in correct stream
	opt, err := redis.ParseURL(redisURL)
	require.NoError(t, err)

	redisClient := redis.NewClient(opt)
	defer func() { _ = redisClient.Close() }()

	streamKey := "git:events"
	messages, err := redisClient.XRevRange(ctx, streamKey, "+", "-").Result()
	require.NoError(t, err)
	require.NotEmpty(t, messages)

	found := false
	for _, msg := range messages {
		dataField, ok := msg.Values["data"].(string)
		if !ok {
			continue
		}

		var eventData map[string]interface{}
		err := json.Unmarshal([]byte(dataField), &eventData)
		if err != nil {
			continue
		}

		if eventData["new_rev"] == event.CommitHash {
			found = true
			assert.Equal(t, string(event.EventType), eventData["event_type"])
			assert.Equal(t, event.CommitHash, eventData["new_rev"])
			assert.Equal(t, event.UserID, eventData["user_id"])
			assert.Equal(t, event.CommitMessage, eventData["commit_message"])
			break
		}
	}

	assert.True(t, found, "Should find push event in stream")
}

func TestAddMergeEventToStream(t *testing.T) {
	redisURL := setupTestRedis(t)
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))

	client, err := NewRedisStreamClient(redisURL, logger)
	require.NoError(t, err)
	defer func() {
		if err := client.Close(); err != nil {
			t.Logf("Failed to close client: %v", err)
		}
	}()

	event := &CommitEvent{
		RepoPath:      "/test/repo.git",
		EventType:     EventTypeMerge,
		CommitHash:    "merge456hash",
		RefName:       "refs/heads/main",
		Pusher:        "git",
		UserID:        "test-user-id-merge",
		CommitMessage: "Test merge commit",
		Timestamp:     time.Now(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.AddCommitEventToStream(ctx, event)
	require.NoError(t, err)

	// Verify merge event in correct stream
	opt, err := redis.ParseURL(redisURL)
	require.NoError(t, err)

	redisClient := redis.NewClient(opt)
	defer func() { _ = redisClient.Close() }()

	streamKey := "git:events"
	messages, err := redisClient.XRevRange(ctx, streamKey, "+", "-").Result()
	require.NoError(t, err)
	require.NotEmpty(t, messages)

	found := false
	for _, msg := range messages {
		dataField, ok := msg.Values["data"].(string)
		if !ok {
			continue
		}

		var eventData map[string]interface{}
		err := json.Unmarshal([]byte(dataField), &eventData)
		if err != nil {
			continue
		}

		if eventData["new_rev"] == event.CommitHash {
			found = true
			assert.Equal(t, string(event.EventType), eventData["event_type"])
			assert.Equal(t, event.CommitHash, eventData["new_rev"])
			assert.Equal(t, event.UserID, eventData["user_id"])
			assert.Equal(t, event.CommitMessage, eventData["commit_message"])
			break
		}
	}

	assert.True(t, found, "Should find merge event in stream")
}

func TestAddDeleteEventToStream(t *testing.T) {
	redisURL := setupTestRedis(t)
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))

	client, err := NewRedisStreamClient(redisURL, logger)
	require.NoError(t, err)
	defer func() {
		if err := client.Close(); err != nil {
			t.Logf("Failed to close client: %v", err)
		}
	}()

	event := &CommitEvent{
		RepoPath:      "/test/repo.git",
		EventType:     EventTypeBranchDelete,
		CommitHash:    "delete123hash",
		RefName:       "refs/heads/removed-branch",
		Pusher:        "git",
		UserID:        "test-user-id-delete",
		CommitMessage: "",
		Timestamp:     time.Now(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.AddCommitEventToStream(ctx, event)
	require.NoError(t, err)

	opt, err := redis.ParseURL(redisURL)
	require.NoError(t, err)

	redisClient := redis.NewClient(opt)
	defer func() { _ = redisClient.Close() }()

	streamKey := "git:events"
	messages, err := redisClient.XRevRange(ctx, streamKey, "+", "-").Result()
	require.NoError(t, err)
	require.NotEmpty(t, messages)

	found := false
	for _, msg := range messages {
		dataField, ok := msg.Values["data"].(string)
		if !ok {
			continue
		}

		var eventData map[string]interface{}
		err := json.Unmarshal([]byte(dataField), &eventData)
		if err != nil {
			continue
		}

		if eventData["old_rev"] == event.CommitHash && eventData["event_type"] == string(EventTypeBranchDelete) {
			found = true
			assert.Equal(t, string(EventTypeBranchDelete), eventData["event_type"])
			assert.Equal(t, event.CommitHash, eventData["old_rev"])
			assert.Equal(t, "", eventData["new_rev"])
			assert.Equal(t, event.RefName, eventData["ref"])
			assert.Equal(t, event.UserID, eventData["user_id"])
			break
		}
	}

	assert.True(t, found, "Should find delete event in stream")
}

func TestAddRenameEventToStream(t *testing.T) {
	redisURL := setupTestRedis(t)
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))

	client, err := NewRedisStreamClient(redisURL, logger)
	require.NoError(t, err)
	defer func() {
		if err := client.Close(); err != nil {
			t.Logf("Failed to close client: %v", err)
		}
	}()

	event := &CommitEvent{
		RepoPath:      "owner/repo.git",
		EventType:     EventTypeRename,
		CommitHash:    "rename456hash",
		RefName:       "refs/heads/new-branch",
		OldRefName:    "refs/heads/old-branch",
		Pusher:        "git",
		UserID:        "test-user-id-rename",
		CommitMessage: "",
		Timestamp:     time.Now(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.AddCommitEventToStream(ctx, event)
	require.NoError(t, err)

	opt, err := redis.ParseURL(redisURL)
	require.NoError(t, err)

	redisClient := redis.NewClient(opt)
	defer func() { _ = redisClient.Close() }()

	streamKey := "git:events"
	messages, err := redisClient.XRevRange(ctx, streamKey, "+", "-").Result()
	require.NoError(t, err)
	require.NotEmpty(t, messages)

	found := false
	for _, msg := range messages {
		dataField, ok := msg.Values["data"].(string)
		if !ok {
			continue
		}

		var eventData map[string]interface{}
		err := json.Unmarshal([]byte(dataField), &eventData)
		if err != nil {
			continue
		}

		if eventData["new_rev"] == event.CommitHash && eventData["event_type"] == string(EventTypeRename) {
			found = true
			assert.Equal(t, string(EventTypeRename), eventData["event_type"])
			assert.Equal(t, event.CommitHash, eventData["new_rev"])
			assert.Equal(t, event.CommitHash, eventData["old_rev"])
			assert.Equal(t, event.RefName, eventData["ref"])
			assert.Equal(t, event.OldRefName, eventData["old_ref"])
			assert.Equal(t, event.UserID, eventData["user_id"])
			break
		}
	}

	assert.True(t, found, "Should find rename event in stream")
}

func TestAddRenameEventToStream_WithoutOldRefName(t *testing.T) {
	redisURL := setupTestRedis(t)
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))

	client, err := NewRedisStreamClient(redisURL, logger)
	require.NoError(t, err)
	defer func() {
		if err := client.Close(); err != nil {
			t.Logf("Failed to close client: %v", err)
		}
	}()

	event := &CommitEvent{
		RepoPath:      "owner/repo.git",
		EventType:     EventTypeRename,
		CommitHash:    "rename-no-old-ref-hash",
		RefName:       "refs/heads/new-only",
		OldRefName:    "",
		Pusher:        "git",
		UserID:        "test-user-id",
		CommitMessage: "",
		Timestamp:     time.Now(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.AddCommitEventToStream(ctx, event)
	require.NoError(t, err)

	opt, err := redis.ParseURL(redisURL)
	require.NoError(t, err)

	redisClient := redis.NewClient(opt)
	defer func() { _ = redisClient.Close() }()

	streamKey := "git:events"
	messages, err := redisClient.XRevRange(ctx, streamKey, "+", "-").Result()
	require.NoError(t, err)
	require.NotEmpty(t, messages)

	found := false
	for _, msg := range messages {
		dataField, ok := msg.Values["data"].(string)
		if !ok {
			continue
		}

		var eventData map[string]interface{}
		err := json.Unmarshal([]byte(dataField), &eventData)
		if err != nil {
			continue
		}

		if eventData["new_rev"] == event.CommitHash {
			found = true
			assert.Equal(t, event.RefName, eventData["ref"])
			_, hasOldRef := eventData["old_ref"]
			assert.False(t, hasOldRef, "old_ref should be omitted when OldRefName is empty")
			break
		}
	}

	assert.True(t, found, "Should find rename event without old_ref in stream")
}

func TestAddCommitEventToStream_StreamLength(t *testing.T) {
	redisURL := setupTestRedis(t)
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
	defer func() { _ = redisClient.Close() }()

	streamKey := "git:events"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Get initial stream length
	initialLength, err := redisClient.XLen(ctx, streamKey).Result()
	require.NoError(t, err)

	// Publish event
	event := &CommitEvent{
		RepoPath:      "/test/repo.git",
		EventType:     EventTypeCommit,
		CommitHash:    "length-test-hash",
		RefName:       "refs/heads/test",
		Pusher:        "test",
		UserID:        "test-user-id-length",
		CommitMessage: "Test length commit",
		Timestamp:     time.Now(),
	}

	err = client.AddCommitEventToStream(ctx, event)
	require.NoError(t, err)

	// Wait a bit for the event to be processed
	time.Sleep(100 * time.Millisecond)

	// Verify stream length increased
	finalLength, err := redisClient.XLen(ctx, streamKey).Result()
	require.NoError(t, err)
	assert.Greater(t, finalLength, initialLength, "Stream length should increase after publishing event")
}

func TestAddCommitEventToStream_MultipleEvents(t *testing.T) {
	redisURL := setupTestRedis(t)
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
			RepoPath:      "/test/repo1.git",
			EventType:     EventTypeCommit,
			CommitHash:    "hash1",
			RefName:       "refs/heads/branch1",
			Pusher:        "user1",
			UserID:        "test-user-id-1",
			CommitMessage: "Commit 1",
			Timestamp:     time.Now(),
		},
		{
			RepoPath:      "/test/repo2.git",
			EventType:     EventTypeCommit,
			CommitHash:    "hash2",
			RefName:       "refs/heads/branch2",
			Pusher:        "user2",
			UserID:        "test-user-id-2",
			CommitMessage: "Commit 2",
			Timestamp:     time.Now(),
		},
		{
			RepoPath:      "/test/repo3.git",
			EventType:     EventTypeCommit,
			CommitHash:    "hash3",
			RefName:       "refs/heads/branch3",
			Pusher:        "user3",
			UserID:        "test-user-id-3",
			CommitMessage: "Commit 3",
			Timestamp:     time.Now(),
		},
	}

	for _, event := range events {
		err := client.AddCommitEventToStream(ctx, event)
		require.NoError(t, err, "Should publish each event successfully")
	}

	// Verify all events were published
	opt, err := redis.ParseURL(redisURL)
	require.NoError(t, err)

	redisClient := redis.NewClient(opt)
	defer func() { _ = redisClient.Close() }()

	streamKey := "git:events"
	messages, err := redisClient.XRevRange(ctx, streamKey, "+", "-").Result()
	require.NoError(t, err)

	// Check that all events are in the stream
	foundHashes := make(map[string]bool)
	for _, msg := range messages {
		dataField, ok := msg.Values["data"].(string)
		if !ok {
			continue
		}

		var eventData map[string]interface{}
		err := json.Unmarshal([]byte(dataField), &eventData)
		if err != nil {
			continue
		}

		if hash, ok := eventData["new_rev"].(string); ok {
			foundHashes[hash] = true
		}
	}

	for _, event := range events {
		assert.True(t, foundHashes[event.CommitHash],
			"Should find commit hash %s in stream", event.CommitHash)
	}
}

// --- 11.1 Redis Stream Events ---

// Test_PublishEvent_ValidEvent_Publishes 정상 발행
func Test_PublishEvent_ValidEvent_Publishes(t *testing.T) {
	redisURL := setupTestRedis(t)
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))
	client, err := NewRedisStreamClient(redisURL, logger)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	event := &CommitEvent{
		RepoPath:      "owner/repo.git",
		EventType:     EventTypePush,
		CommitHash:    "abc123",
		RefName:       "refs/heads/main",
		Timestamp:     time.Now(),
		CommitMessage: "valid event",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.AddCommitEventToStream(ctx, event)
	require.NoError(t, err)
}

// Test_PublishEvent_RedisDown_ReturnsError Redis 다운 시 에러 반환
func Test_PublishEvent_RedisDown_ReturnsError(t *testing.T) {
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))
	_, err := NewRedisStreamClient("redis://nonexistent-host-invalid:6379/0", logger)
	require.Error(t, err)
}

// Test_PublishEvent_InvalidPayload_Returns400 잘못된 페이로드(nil event) 시 에러 반환
func Test_PublishEvent_InvalidPayload_Returns400(t *testing.T) {
	redisURL := setupTestRedis(t)
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))
	client, err := NewRedisStreamClient(redisURL, logger)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.AddCommitEventToStream(ctx, nil)
	require.Error(t, err)
}

// --- 11.1A At-least-once / Ordering / Dedupe ---

// Test_PublishEvent_DuplicateDelivery_IsIdempotentConsumerSide 동일 이벤트 재발행 시에도 성공(컨슈머 측 idempotent)
func Test_PublishEvent_DuplicateDelivery_IsIdempotentConsumerSide(t *testing.T) {
	redisURL := setupTestRedis(t)
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))
	client, err := NewRedisStreamClient(redisURL, logger)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	event := &CommitEvent{
		EventID:       "evt-dedup-test-1",
		RepoPath:      "owner/repo.git",
		EventType:     EventTypeCommit,
		CommitHash:    "samehash",
		RefName:       "refs/heads/main",
		Timestamp:     time.Now(),
		CommitMessage: "duplicate",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.AddCommitEventToStream(ctx, event)
	require.NoError(t, err)
	err = client.AddCommitEventToStream(ctx, event)
	require.NoError(t, err)
}

// Test_PublishEvent_OrderPreserved_PerRepoPartition 발행 순서가 스트림에서 유지됨
func Test_PublishEvent_OrderPreserved_PerRepoPartition(t *testing.T) {
	redisURL := setupTestRedis(t)
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))
	opt, err := redis.ParseURL(redisURL)
	require.NoError(t, err)
	rdb := redis.NewClient(opt)
	defer func() { _ = rdb.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	streamKey := "git:events"
	lenBefore, _ := rdb.XLen(ctx, streamKey).Result()

	client, err := NewRedisStreamClient(redisURL, logger)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	first := &CommitEvent{RepoPath: "a/repo.git", EventType: EventTypePush, CommitHash: "order-first", RefName: "refs/heads/main", Timestamp: time.Now()}
	second := &CommitEvent{RepoPath: "a/repo.git", EventType: EventTypePush, CommitHash: "order-second", RefName: "refs/heads/main", Timestamp: time.Now()}
	require.NoError(t, client.AddCommitEventToStream(ctx, first))
	require.NoError(t, client.AddCommitEventToStream(ctx, second))

	messages, err := rdb.XRange(ctx, streamKey, "-", "+").Result()
	require.NoError(t, err)
	require.GreaterOrEqual(t, int64(len(messages)), lenBefore+2)
	// 마지막 두 메시지가 우리가 넣은 순서대로여야 함
	start := len(messages) - 2
	var hashes []string
	for i := start; i < len(messages); i++ {
		data, _ := messages[i].Values["data"].(string)
		var m map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(data), &m))
		rev, _ := m["new_rev"].(string)
		hashes = append(hashes, rev)
	}
	require.Equal(t, []string{"order-first", "order-second"}, hashes)
}
