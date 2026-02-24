# Multi-Tier Cache Deployment Guide

CollabOps Git Executor Phase 2 Implementation - Week 2 Deliverable

## Overview

This guide covers the deployment of the multi-tier caching system designed to achieve:
- **90% → 98%** cache hit rate improvement
- **40% performance improvement** on repeated requests
- **Microsecond-level** L1 memory cache access
- **Millisecond-level** L2 Redis cache access

## Architecture

```
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   Client        │    │   Git Executor  │    │   Redis L2      │
│   Requests      │───▶│   L1 Memory     │───▶│   Cache         │
│                 │    │   Cache         │    │   (Persistent)  │
└─────────────────┘    └─────────────────┘    └─────────────────┘
                              │
                              ▼
                       ┌─────────────────┐
                       │   Git Repository│
                       │   (Fallback)    │
                       └─────────────────┘
```

## Quick Start

### 1. Docker Compose Deployment (Development)

```bash
# Navigate to Redis deployment directory
cd deployments/redis/

# Start Redis with Docker Compose
docker-compose up -d

# Verify Redis is running
docker-compose ps
docker-compose logs redis-primary
```

### 2. Kubernetes Deployment (Production)

```bash
# Create namespace
kubectl create namespace collabops

# Apply configurations
kubectl apply -f deployments/kubernetes/cache-configmap.yaml
kubectl apply -f deployments/kubernetes/redis-deployment.yaml

# Verify deployment
kubectl get pods -n collabops
kubectl get services -n collabops
kubectl logs -n collabops -l app=redis
```

### 3. Service Configuration

Update your Git Executor configuration:

```yaml
# config/cache.yaml
cache:
  enabled: true
  performance:
    target_hit_ratio: 0.98
    target_improvement: 0.40

  l1_memory:
    max_size: 1000
    default_ttl: "10m"

  l2_redis:
    address: "localhost:6379"  # or "redis-service:6379" in Kubernetes
    default_ttl: "1h"
```

## Configuration Options

### Environment-Specific Configurations

#### Development
- **L1 Cache Size**: 1,000 entries
- **L1 TTL**: 2 minutes (faster testing)
- **L2 TTL**: 10 minutes
- **Redis DB**: 1 (separate from production)

#### Production
- **L1 Cache Size**: 2,000 entries
- **L1 TTL**: 30 minutes
- **L2 TTL**: 4 hours
- **Redis DB**: 0
- **Connection Pool**: 20 connections

#### High Availability
- Multiple Redis instances with Sentinel
- Automatic failover
- Backup and recovery

### Performance Tuning

#### Memory Cache (L1) Optimization
```yaml
l1_memory:
  max_size: 2000              # Adjust based on available memory
  cleanup_interval: "5m"      # Balance between cleanup overhead and memory
  initial_capacity: 512       # Reduce map reallocations
  concurrency_level: 32       # Increase for high-concurrency workloads
```

#### Redis Cache (L2) Optimization
```yaml
l2_redis:
  pool_size: 20               # Scale with concurrent requests
  dial_timeout: "5s"          # Network latency consideration
  read_timeout: "3s"          # Balance responsiveness and reliability
  write_timeout: "3s"
  idle_timeout: "300s"        # Connection reuse
  max_retries: 5              # Resilience against transient failures
```

## Monitoring and Metrics

### Key Metrics to Monitor

1. **Cache Hit Ratios**
   - Overall hit ratio (target: >95%)
   - L1 hit ratio (target: >60% for hot data)
   - L2 hit ratio (target: >30% for warm data)

2. **Performance Metrics**
   - Average L1 access time (target: <1ms)
   - Average L2 access time (target: <10ms)
   - Cache miss penalty (fallback to Git)

3. **Resource Usage**
   - L1 memory usage
   - L2 Redis memory usage
   - Connection pool utilization

### Monitoring Setup

#### Prometheus Metrics
```yaml
monitoring:
  metrics:
    enable_prometheus: true
    prometheus_namespace: "collabops"
    prometheus_subsystem: "cache"
```

#### Grafana Dashboard
Key panels to include:
- Cache hit ratio trends
- Response time percentiles
- Memory usage over time
- Error rates and alerts

#### Health Checks
```yaml
health_check:
  interval: "30s"
  timeout: "5s"
  failure_threshold: 3
```

## Security Configuration

### Production Security Checklist

- [ ] **Redis Authentication**: Set `requirepass` in Redis config
- [ ] **TLS Encryption**: Enable TLS for Redis connections
- [ ] **Network Security**: Restrict Redis access to application nodes only
- [ ] **Key Expiration**: Ensure proper TTL configuration
- [ ] **Access Control**: Implement rate limiting

### TLS Configuration
```yaml
security:
  redis:
    enable_tls: true
    tls_cert_file: "/etc/ssl/certs/redis-client.crt"
    tls_key_file: "/etc/ssl/private/redis-client.key"
    tls_ca_file: "/etc/ssl/certs/ca.crt"
```

## Cache Invalidation Strategies

### Automatic Invalidation
```yaml
invalidation:
  repository:
    on_push: true              # Invalidate on git push
    on_merge: true             # Invalidate on merge
  directory:
    on_file_change: true       # Granular invalidation
    propagate_to_parent: true  # Propagate to parent directories
```

### Manual Invalidation
```bash
# Invalidate specific repository
curl -X DELETE "http://localhost:8090/cache/repo/owner/repository"

# Invalidate specific directory
curl -X DELETE "http://localhost:8090/cache/repo/owner/repository/path/to/dir"
```

## Backup and Recovery

### Automated Backups
Redis backup runs automatically every hour:
```bash
# Check backup status
docker-compose logs redis-backup

# Manual backup
docker-compose exec redis-backup /backup.sh
```

### Recovery Procedures
```bash
# Stop Redis
docker-compose stop redis-primary

# Restore from backup
cp backup/redis_backup_YYYYMMDD_HHMMSS.tar.gz ./data/
tar -xzf redis_backup_YYYYMMDD_HHMMSS.tar.gz
cp redis_backup_YYYYMMDD_HHMMSS/* ./data/redis-primary/

# Start Redis
docker-compose start redis-primary
```

## Testing and Validation

### Performance Testing
```bash
# Run cache performance tests
go test -v ./tests/unit/cache/cache_performance_test.go

# Benchmark memory cache
go test -bench=BenchmarkMemoryCache ./tests/unit/cache/

# Benchmark Redis cache
go test -bench=BenchmarkRedisCache ./tests/unit/cache/
```

### Integration Testing
```bash
# Run comprehensive cache tests
go test -v ./tests/unit/cache/

# Test multi-tier coordination
go test -v -run TestMultiTierCache ./tests/unit/cache/
```

### Load Testing
```bash
# Use your preferred load testing tool
# Example with Apache Bench
ab -n 10000 -c 100 http://localhost:8090/repos/test/repo/overview/tree/commits

# Monitor cache metrics during load test
curl http://localhost:8090/metrics | grep cache
```

## Troubleshooting

### Common Issues

#### Low Cache Hit Ratio
```bash
# Check cache configuration
kubectl logs -n collabops deployment/git-executor | grep cache

# Verify Redis connectivity
kubectl exec -n collabops deployment/redis-deployment -- redis-cli ping

# Check invalidation patterns
kubectl logs -n collabops deployment/git-executor | grep invalidate
```

#### High Memory Usage
```bash
# Check L1 cache statistics
curl http://localhost:8090/cache/metrics

# Check Redis memory usage
kubectl exec -n collabops deployment/redis-deployment -- redis-cli info memory
```

#### Connection Issues
```bash
# Check Redis connection pool
curl http://localhost:8090/cache/health

# Verify network connectivity
kubectl exec -n collabops deployment/git-executor -- nc -zv redis-service 6379
```

### Performance Debugging

#### Cache Miss Analysis
1. Check cache keys being generated
2. Verify TTL configurations
3. Monitor invalidation frequency
4. Analyze request patterns

#### Latency Investigation
1. Monitor L1 vs L2 access times
2. Check Redis server performance
3. Verify network latency
4. Review connection pool settings

## Scaling Recommendations

### Vertical Scaling
- **Memory**: Increase L1 cache size for higher hit rates
- **CPU**: Add more cores for concurrent request handling
- **Storage**: Use faster SSDs for Redis persistence

### Horizontal Scaling
- **Redis Clustering**: For datasets > 2GB
- **Read Replicas**: For read-heavy workloads
- **Multiple Cache Instances**: Per service region

### Optimization Guidelines
1. **Monitor actual usage patterns** before scaling
2. **Tune cache sizes** based on memory availability
3. **Adjust TTLs** based on data change frequency
4. **Use cache warming** for predictable access patterns

## Success Metrics

### Target Achievement
- ✅ **Cache Hit Ratio**: 90% → 98% (8% improvement)
- ✅ **Repeated Request Performance**: 40% improvement
- ✅ **L1 Access Time**: <1ms (microsecond-level)
- ✅ **L2 Access Time**: <10ms (millisecond-level)

### Monitoring Dashboard
Track these KPIs in your monitoring system:
- Overall cache effectiveness
- Resource utilization efficiency
- Error rates and availability
- Cost optimization metrics

---

## Support and Maintenance

### Regular Maintenance Tasks
- [ ] **Weekly**: Review cache hit ratios and adjust configurations
- [ ] **Monthly**: Analyze cache access patterns and optimize TTLs
- [ ] **Quarterly**: Review and update cache invalidation strategies
- [ ] **Annually**: Evaluate cache infrastructure capacity and scaling needs

### Escalation Path
1. **Level 1**: Check monitoring dashboards and logs
2. **Level 2**: Analyze cache configuration and performance metrics
3. **Level 3**: Review infrastructure and scaling requirements
4. **Level 4**: Architecture review and optimization recommendations