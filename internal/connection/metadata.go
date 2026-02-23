package connection

import "sync"

// connectionMetadata implements ConnectionMetadata interface
type connectionMetadata struct {
	mu      sync.RWMutex
	owner   string
	repo    string
	userID  string
	protoV2 bool
}

// NewConnectionMetadata creates a new connection metadata instance
func NewConnectionMetadata() ConnectionMetadata {
	return &connectionMetadata{}
}

// GetConnectionInfo returns all connection information
func (cm *connectionMetadata) GetConnectionInfo() (owner, repo, userID string, protoV2 bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.owner, cm.repo, cm.userID, cm.protoV2
}

// SetConnectionInfo sets all connection information atomically
func (cm *connectionMetadata) SetConnectionInfo(owner, repo, userID string, protoV2 bool) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.owner = owner
	cm.repo = repo
	cm.userID = userID
	cm.protoV2 = protoV2
}

// GetOwner returns the repository owner
func (cm *connectionMetadata) GetOwner() string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.owner
}

// GetRepo returns the repository name
func (cm *connectionMetadata) GetRepo() string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.repo
}

// GetUserID returns the user ID
func (cm *connectionMetadata) GetUserID() string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.userID
}

// IsProtoV2 returns whether protocol v2 is being used
func (cm *connectionMetadata) IsProtoV2() bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.protoV2
}

// GetTenantID returns a tenant identifier for metrics and rate limiting
func (cm *connectionMetadata) GetTenantID() string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if cm.owner != "" && cm.repo != "" {
		return cm.owner + "/" + cm.repo
	}
	return "unknown"
}
