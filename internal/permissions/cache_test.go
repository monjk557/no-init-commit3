package permissions

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPermissionCache(t *testing.T) {
	ttl := 5 * time.Minute
	cache := NewPermissionCache(ttl)

	require.NotNil(t, cache)
	assert.Equal(t, ttl, cache.ttl)
}

func TestPermissionCache_SetAndGet(t *testing.T) {
	cache := NewPermissionCache(5 * time.Minute)
	defer cache.Close()

	now := time.Now()
	result := &PermissionResult{
		UserID:    "user123",
		RepoOwner: "owner",
		RepoName:  "repo",
		Operation: OperationRead,
		HasAccess: true,
		CachedAt:  now,
		ExpiresAt: now.Add(5 * time.Minute),
	}

	key := result.CacheKey()

	// Test Set and Get
	cache.Set(key, result)
	retrieved := cache.Get(key)

	require.NotNil(t, retrieved)
	assert.Equal(t, result.UserID, retrieved.UserID)
	assert.Equal(t, result.RepoOwner, retrieved.RepoOwner)
	assert.Equal(t, result.RepoName, retrieved.RepoName)
	assert.Equal(t, result.Operation, retrieved.Operation)
	assert.Equal(t, result.HasAccess, retrieved.HasAccess)
	assert.Equal(t, result.CachedAt, retrieved.CachedAt)
	assert.Equal(t, result.ExpiresAt, retrieved.ExpiresAt)
}

func TestPermissionCache_GetNonExistent(t *testing.T) {
	cache := NewPermissionCache(5 * time.Minute)
	defer cache.Close()

	result := cache.Get("non-existent-key")
	assert.Nil(t, result)
}

func TestPermissionCache_Delete(t *testing.T) {
	cache := NewPermissionCache(5 * time.Minute)
	defer cache.Close()

	now := time.Now()
	result := &PermissionResult{
		UserID:    "user123",
		RepoOwner: "owner",
		RepoName:  "repo",
		Operation: OperationRead,
		HasAccess: true,
		CachedAt:  now,
		ExpiresAt: now.Add(5 * time.Minute),
	}

	key := result.CacheKey()

	// Set, verify it exists, delete, verify it's gone
	cache.Set(key, result)
	assert.NotNil(t, cache.Get(key))

	cache.Delete(key)
	assert.Nil(t, cache.Get(key))
}

func TestPermissionCache_Clear(t *testing.T) {
	cache := NewPermissionCache(5 * time.Minute)
	defer cache.Close()

	now := time.Now()

	// Add multiple entries
	results := []*PermissionResult{
		{
			UserID:    "user1",
			RepoOwner: "owner1",
			RepoName:  "repo1",
			Operation: OperationRead,
			HasAccess: true,
			CachedAt:  now,
			ExpiresAt: now.Add(5 * time.Minute),
		},
		{
			UserID:    "user2",
			RepoOwner: "owner2",
			RepoName:  "repo2",
			Operation: OperationWrite,
			HasAccess: false,
			CachedAt:  now,
			ExpiresAt: now.Add(5 * time.Minute),
		},
	}

	for _, result := range results {
		cache.Set(result.CacheKey(), result)
	}

	// Verify entries exist
	for _, result := range results {
		assert.NotNil(t, cache.Get(result.CacheKey()))
	}

	// Clear cache
	cache.Clear()

	// Verify all entries are gone
	for _, result := range results {
		assert.Nil(t, cache.Get(result.CacheKey()))
	}
}

func TestPermissionCache_CleanupExpired(t *testing.T) {
	cache := NewPermissionCache(100 * time.Millisecond)
	defer cache.Close()

	now := time.Now()

	// Add expired entry
	expiredResult := &PermissionResult{
		UserID:    "user1",
		RepoOwner: "owner1",
		RepoName:  "repo1",
		Operation: OperationRead,
		HasAccess: true,
		CachedAt:  now.Add(-2 * time.Hour),
		ExpiresAt: now.Add(-1 * time.Hour), // Expired
	}

	// Add valid entry
	validResult := &PermissionResult{
		UserID:    "user2",
		RepoOwner: "owner2",
		RepoName:  "repo2",
		Operation: OperationWrite,
		HasAccess: false,
		CachedAt:  now,
		ExpiresAt: now.Add(1 * time.Hour), // Not expired
	}

	cache.Set(expiredResult.CacheKey(), expiredResult)
	cache.Set(validResult.CacheKey(), validResult)

	// Verify both entries exist before cleanup
	assert.NotNil(t, cache.Get(expiredResult.CacheKey()))
	assert.NotNil(t, cache.Get(validResult.CacheKey()))

	// Run cleanup
	cache.CleanupExpired()

	// Verify expired entry is gone and valid entry remains
	assert.Nil(t, cache.Get(expiredResult.CacheKey()))
	assert.NotNil(t, cache.Get(validResult.CacheKey()))
}

func TestPermissionCache_Size(t *testing.T) {
	cache := NewPermissionCache(5 * time.Minute)
	defer cache.Close()

	// Initially empty
	assert.Equal(t, 0, cache.Size())

	now := time.Now()

	// Add entries
	for i := 0; i < 3; i++ {
		result := &PermissionResult{
			UserID:    "user" + string(rune('1'+i)),
			RepoOwner: "owner",
			RepoName:  "repo",
			Operation: OperationRead,
			HasAccess: true,
			CachedAt:  now,
			ExpiresAt: now.Add(5 * time.Minute),
		}
		cache.Set(result.CacheKey(), result)
	}

	assert.Equal(t, 3, cache.Size())

	// Clear and verify size
	cache.Clear()
	assert.Equal(t, 0, cache.Size())
}

func TestPermissionCache_ConcurrentAccess(t *testing.T) {
	cache := NewPermissionCache(5 * time.Minute)
	defer cache.Close()

	const numGoroutines = 10
	const numOperationsPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	now := time.Now()

	// Run concurrent operations
	for i := 0; i < numGoroutines; i++ {
		go func(goroutineID int) {
			defer wg.Done()

			for j := 0; j < numOperationsPerGoroutine; j++ {
				result := &PermissionResult{
					UserID:    "user" + string(rune('0'+goroutineID)),
					RepoOwner: "owner",
					RepoName:  "repo" + string(rune('0'+j%10)),
					Operation: OperationRead,
					HasAccess: true,
					CachedAt:  now,
					ExpiresAt: now.Add(5 * time.Minute),
				}

				key := result.CacheKey()

				// Perform operations in sequence: set, get, delete
				cache.Set(key, result)
				retrieved := cache.Get(key)
				assert.NotNil(t, retrieved)
				cache.Delete(key)
				deleted := cache.Get(key)
				assert.Nil(t, deleted)
			}
		}(i)
	}

	wg.Wait()

	// Cache should be empty after all operations
	assert.Equal(t, 0, cache.Size())
}

func TestPermissionCache_InvalidEntry(t *testing.T) {
	cache := NewPermissionCache(5 * time.Minute)
	defer cache.Close()

	// Manually store an invalid entry type
	cache.cache.Store("invalid-key", "not a PermissionResult")

	// Get should handle this gracefully
	result := cache.Get("invalid-key")
	assert.Nil(t, result)

	// The invalid entry should be removed
	_, exists := cache.cache.Load("invalid-key")
	assert.False(t, exists)
}

func TestPermissionCache_CleanupExpiredWithInvalidEntries(t *testing.T) {
	cache := NewPermissionCache(5 * time.Minute)
	defer cache.Close()

	now := time.Now()

	// Add a valid entry
	validResult := &PermissionResult{
		UserID:    "user1",
		RepoOwner: "owner1",
		RepoName:  "repo1",
		Operation: OperationRead,
		HasAccess: true,
		CachedAt:  now,
		ExpiresAt: now.Add(1 * time.Hour),
	}
	cache.Set(validResult.CacheKey(), validResult)

	// Manually add an invalid entry
	cache.cache.Store("invalid-key", "not a PermissionResult")

	// Run cleanup - should handle invalid entries gracefully
	cache.CleanupExpired()

	// Valid entry should remain
	assert.NotNil(t, cache.Get(validResult.CacheKey()))

	// Invalid entry should be removed
	_, exists := cache.cache.Load("invalid-key")
	assert.False(t, exists)
}
