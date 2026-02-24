#!/bin/sh
# Redis backup script for CollabOps Git Executor
# Automated backup of L2 cache data

set -e

# Configuration from environment variables
BACKUP_INTERVAL=${BACKUP_INTERVAL:-3600}  # Default: backup every hour
BACKUP_RETENTION=${BACKUP_RETENTION:-168}  # Default: 7 days retention
REDIS_HOST=${REDIS_HOST:-redis-primary}
REDIS_PORT=${REDIS_PORT:-6379}

# Directories
SOURCE_DIR="/source-data"
BACKUP_DIR="/backup"
LOG_FILE="/var/log/backup.log"

# Ensure backup directory exists
mkdir -p "$BACKUP_DIR"

# Logging function
log() {
    echo "$(date '+%Y-%m-%d %H:%M:%S') $1" | tee -a "$LOG_FILE"
}

# Function to create backup
create_backup() {
    local timestamp=$(date '+%Y%m%d_%H%M%S')
    local backup_name="redis_backup_${timestamp}"
    local backup_path="${BACKUP_DIR}/${backup_name}"

    log "Starting backup: $backup_name"

    # Create backup directory
    mkdir -p "$backup_path"

    # Copy RDB file if it exists
    if [ -f "${SOURCE_DIR}/dump.rdb" ]; then
        cp "${SOURCE_DIR}/dump.rdb" "${backup_path}/dump.rdb"
        log "RDB file backed up"
    else
        log "No RDB file found"
    fi

    # Copy AOF file if it exists
    if [ -f "${SOURCE_DIR}/appendonly.aof" ]; then
        cp "${SOURCE_DIR}/appendonly.aof" "${backup_path}/appendonly.aof"
        log "AOF file backed up"
    else
        log "No AOF file found"
    fi

    # Create metadata file
    cat > "${backup_path}/metadata.txt" << EOF
Backup Created: $(date)
Redis Host: $REDIS_HOST:$REDIS_PORT
Backup Type: Automated
Source Directory: $SOURCE_DIR
EOF

    # Compress the backup
    cd "$BACKUP_DIR"
    tar -czf "${backup_name}.tar.gz" "$backup_name"
    rm -rf "$backup_name"

    log "Backup completed: ${backup_name}.tar.gz"
}

# Function to cleanup old backups
cleanup_old_backups() {
    log "Starting backup cleanup (retention: ${BACKUP_RETENTION} hours)"

    # Find and delete backups older than retention period
    find "$BACKUP_DIR" -name "redis_backup_*.tar.gz" -type f -mtime +"$((BACKUP_RETENTION / 24))" -delete

    # Count remaining backups
    local remaining=$(find "$BACKUP_DIR" -name "redis_backup_*.tar.gz" -type f | wc -l)
    log "Cleanup completed. Remaining backups: $remaining"
}

# Function to verify Redis connectivity
verify_redis() {
    if command -v redis-cli >/dev/null 2>&1; then
        if redis-cli -h "$REDIS_HOST" -p "$REDIS_PORT" ping >/dev/null 2>&1; then
            log "Redis connectivity verified"
            return 0
        else
            log "Redis connectivity failed"
            return 1
        fi
    else
        log "redis-cli not available, skipping connectivity check"
        return 0
    fi
}

# Function to get backup statistics
backup_stats() {
    local total_backups=$(find "$BACKUP_DIR" -name "redis_backup_*.tar.gz" -type f | wc -l)
    local total_size=$(du -sh "$BACKUP_DIR" | cut -f1)
    local oldest_backup=$(find "$BACKUP_DIR" -name "redis_backup_*.tar.gz" -type f -printf '%T@ %p\n' | sort -n | head -1 | cut -d' ' -f2- | xargs basename 2>/dev/null || echo "None")
    local newest_backup=$(find "$BACKUP_DIR" -name "redis_backup_*.tar.gz" -type f -printf '%T@ %p\n' | sort -n | tail -1 | cut -d' ' -f2- | xargs basename 2>/dev/null || echo "None")

    log "Backup Statistics:"
    log "  Total backups: $total_backups"
    log "  Total size: $total_size"
    log "  Oldest backup: $oldest_backup"
    log "  Newest backup: $newest_backup"
}

# Main backup loop
main() {
    log "Redis backup service starting"
    log "Configuration:"
    log "  Backup interval: ${BACKUP_INTERVAL}s"
    log "  Backup retention: ${BACKUP_RETENTION}h"
    log "  Redis host: ${REDIS_HOST}:${REDIS_PORT}"
    log "  Source directory: $SOURCE_DIR"
    log "  Backup directory: $BACKUP_DIR"

    # Initial connectivity check
    verify_redis

    # Initial stats
    backup_stats

    while true; do
        # Create backup
        if create_backup; then
            log "Backup cycle completed successfully"
        else
            log "Backup cycle failed"
        fi

        # Cleanup old backups
        cleanup_old_backups

        # Show current stats
        backup_stats

        # Wait for next backup interval
        log "Waiting ${BACKUP_INTERVAL}s for next backup cycle..."
        sleep "$BACKUP_INTERVAL"
    done
}

# Handle signals for graceful shutdown
trap 'log "Backup service shutting down"; exit 0' TERM INT

# Start main loop
main