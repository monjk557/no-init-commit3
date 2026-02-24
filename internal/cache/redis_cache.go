package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// RedisCache implements L2 caching using Redis with JSON serialization
type RedisCache[T any] struct {
	client  *redis.Client
	prefix  string
	logger  *zap.Logger
	metrics *RedisCacheMetrics
}

// RedisCacheConfig contains Redis cache configuration
type RedisCacheConfig struct {
	Address  string `json:"address" yaml:"address"`
	Password string `json:"password,omitempty" yaml:"password,omitempty"`
	DB       int    `json:"db" yaml:"db"`
	PoolSize int    `json:"pool_size" yaml:"pool_size"`
	Prefix   string `json:"prefix" yaml:"prefix"`

	// Connection settings
	DialTimeout  time.Duration `json:"dial_timeout" yaml:"dial_timeout"`
	ReadTimeout  time.Duration `json:"read_timeout" yaml:"read_timeout"`
	WriteTimeout time.Duration `json:"write_timeout" yaml:"write_timeout"`
	PoolTimeout  time.Duration `json:"pool_timeout" yaml:"pool_timeout"`

	// Performance settings
	MaxRetries      int           `json:"max_retries" yaml:"max_retries"`
	MinRetryBackoff time.Duration `json:"min_retry_backoff" yaml:"min_retry_backoff"`
	MaxRetryBackoff time.Duration `json:"max_retry_backoff" yaml:"max_retry_backoff"`
}

// RedisCacheMetrics tracks Redis cache performance
type RedisCacheMetrics struct {
	Hits                int64         `json:"hits"`
	Misses              int64         `json:"misses"`
	Errors              int64         `json:"errors"`
	TotalRequests       int64         `json:"total_requests"`
	AverageResponseTime time.Duration `json:"average_response_time"`
	SerializationErrors int64         `json:"serialization_errors"`
	ConnectionErrors    int64         `json:"connection_errors"`
}

// NewRedisCache creates a new Redis cache instance
func NewRedisCache[T any](config *RedisCacheConfig, logger *zap.Logger) (*RedisCache[T], error) {
	if config == nil {
		return nil, fmt.Errorf("redis config is required")
	}

	// Apply defaults
	if config.Address == "" {
		config.Address = "localhost:6379"
	}
	if config.Prefix == "" {
		config.Prefix = "collabops:"
	}
	if config.PoolSize <= 0 {
		config.PoolSize = 10
	}
	if config.DialTimeout <= 0 {
		config.DialTimeout = 5 * time.Second
	}
	if config.ReadTimeout <= 0 {
		config.ReadTimeout = 3 * time.Second
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = 3 * time.Second
	}
	if config.PoolTimeout <= 0 {
		config.PoolTimeout = 4 * time.Second
	}
	if config.MaxRetries <= 0 {
		config.MaxRetries = 3
	}
	if config.MinRetryBackoff <= 0 {
		config.MinRetryBackoff = 8 * time.Millisecond
	}
	if config.MaxRetryBackoff <= 0 {
		config.MaxRetryBackoff = 512 * time.Millisecond
	}

	// Create Redis client with optimized settings
	rdb := redis.NewClient(&redis.Options{
		Addr:         config.Address,
		Password:     config.Password,
		DB:           config.DB,
		PoolSize:     config.PoolSize,
		DialTimeout:  config.DialTimeout,
		ReadTimeout:  config.ReadTimeout,
		WriteTimeout: config.WriteTimeout,
		PoolTimeout:  config.PoolTimeout,

		MaxRetries:      config.MaxRetries,
		MinRetryBackoff: config.MinRetryBackoff,
		MaxRetryBackoff: config.MaxRetryBackoff,

		// Connection pool settings for high performance
		MinIdleConns: 5,
		MaxIdleConns: config.PoolSize / 2,
	})

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	logger.Info("Redis cache initialized",
		zap.String("address", config.Address),
		zap.Int("db", config.DB),
		zap.Int("pool_size", config.PoolSize),
		zap.String("prefix", config.Prefix))

	return &RedisCache[T]{
		client:  rdb,
		prefix:  config.Prefix,
		logger:  logger,
		metrics: &RedisCacheMetrics{},
	}, nil
}

// Get retrieves an item from Redis cache
func (r *RedisCache[T]) Get(ctx context.Context, key string) (T, bool, error) {
	var zero T
	startTime := time.Now()

	fullKey := r.prefix + key
	result, err := r.client.Get(ctx, fullKey).Result()

	r.updateRequestMetrics(time.Since(startTime))

	if err != nil {
		if err == redis.Nil {
			r.metrics.Misses++
			return zero, false, nil
		}
		r.metrics.Errors++
		r.logger.Error("Redis GET error", zap.String("key", key), zap.Error(err))
		return zero, false, err
	}

	var value T
	if err := json.Unmarshal([]byte(result), &value); err != nil {
		r.metrics.SerializationErrors++
		r.logger.Error("Redis deserialization error", zap.String("key", key), zap.Error(err))
		return zero, false, err
	}

	r.metrics.Hits++
	return value, true, nil
}

// Set stores an item in Redis cache with TTL
func (r *RedisCache[T]) Set(ctx context.Context, key string, value T, ttl time.Duration) error {
	startTime := time.Now()

	data, err := json.Marshal(value)
	if err != nil {
		r.metrics.SerializationErrors++
		return fmt.Errorf("failed to serialize value for key %s: %w", key, err)
	}

	fullKey := r.prefix + key
	err = r.client.Set(ctx, fullKey, data, ttl).Err()

	r.updateRequestMetrics(time.Since(startTime))

	if err != nil {
		r.metrics.Errors++
		r.logger.Error("Redis SET error", zap.String("key", key), zap.Error(err))
		return err
	}

	return nil
}

// Delete removes an item from Redis cache
func (r *RedisCache[T]) Delete(ctx context.Context, key string) error {
	startTime := time.Now()
	fullKey := r.prefix + key

	err := r.client.Del(ctx, fullKey).Err()
	r.updateRequestMetrics(time.Since(startTime))

	if err != nil {
		r.metrics.Errors++
		r.logger.Error("Redis DEL error", zap.String("key", key), zap.Error(err))
	}

	return err
}

// DeleteByPattern removes all keys matching a pattern
func (r *RedisCache[T]) DeleteByPattern(ctx context.Context, pattern string) error {
	startTime := time.Now()
	fullPattern := r.prefix + pattern

	keys, err := r.client.Keys(ctx, fullPattern).Result()
	if err != nil {
		r.metrics.Errors++
		return err
	}

	if len(keys) == 0 {
		return nil
	}

	err = r.client.Del(ctx, keys...).Err()
	r.updateRequestMetrics(time.Since(startTime))

	if err != nil {
		r.metrics.Errors++
		r.logger.Error("Redis pattern delete error", zap.String("pattern", pattern), zap.Error(err))
		return err
	}

	r.logger.Debug("Deleted keys by pattern",
		zap.String("pattern", pattern),
		zap.Int("count", len(keys)))

	return nil
}

// Exists checks if a key exists in Redis
func (r *RedisCache[T]) Exists(ctx context.Context, key string) (bool, error) {
	startTime := time.Now()
	fullKey := r.prefix + key

	count, err := r.client.Exists(ctx, fullKey).Result()
	r.updateRequestMetrics(time.Since(startTime))

	if err != nil {
		r.metrics.Errors++
		return false, err
	}

	return count > 0, nil
}

// SetTTL updates the TTL for an existing key
func (r *RedisCache[T]) SetTTL(ctx context.Context, key string, ttl time.Duration) error {
	startTime := time.Now()
	fullKey := r.prefix + key

	err := r.client.Expire(ctx, fullKey, ttl).Err()
	r.updateRequestMetrics(time.Since(startTime))

	if err != nil {
		r.metrics.Errors++
		return err
	}

	return nil
}

// GetTTL retrieves the remaining TTL for a key
func (r *RedisCache[T]) GetTTL(ctx context.Context, key string) (time.Duration, error) {
	startTime := time.Now()
	fullKey := r.prefix + key

	ttl, err := r.client.TTL(ctx, fullKey).Result()
	r.updateRequestMetrics(time.Since(startTime))

	return ttl, err
}

// Ping tests the Redis connection
func (r *RedisCache[T]) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

// GetMetrics returns current cache metrics
func (r *RedisCache[T]) GetMetrics() RedisCacheMetrics {
	return *r.metrics
}

// GetHitRatio returns the cache hit ratio (0.0 to 1.0)
func (r *RedisCache[T]) GetHitRatio() float64 {
	total := r.metrics.Hits + r.metrics.Misses
	if total == 0 {
		return 0.0
	}
	return float64(r.metrics.Hits) / float64(total)
}

// ResetMetrics clears all performance metrics
func (r *RedisCache[T]) ResetMetrics() {
	r.metrics = &RedisCacheMetrics{}
}

// Close closes the Redis connection
func (r *RedisCache[T]) Close() error {
	return r.client.Close()
}

// GetInfo returns Redis server information
func (r *RedisCache[T]) GetInfo(ctx context.Context) (map[string]string, error) {
	info, err := r.client.Info(ctx).Result()
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)
	result["redis_info"] = info
	result["prefix"] = r.prefix
	result["hit_ratio"] = fmt.Sprintf("%.2f", r.GetHitRatio())

	return result, nil
}

// updateRequestMetrics updates performance metrics
func (r *RedisCache[T]) updateRequestMetrics(duration time.Duration) {
	r.metrics.TotalRequests++

	// Update average response time using moving average
	if r.metrics.TotalRequests == 1 {
		r.metrics.AverageResponseTime = duration
	} else {
		// Simple moving average approximation
		r.metrics.AverageResponseTime = time.Duration(
			(int64(r.metrics.AverageResponseTime)*9 + int64(duration)) / 10,
		)
	}
}
