package connection

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.uber.org/zap"
)

// ConcurrencyController manages concurrent Git operations with resource limits
type ConcurrencyController struct {
	maxConcurrentClones int
	maxConcurrentPushes int
	perTenantLimit      int
	perIPLimit          int

	// Current state tracking
	activeClonesCount int32
	activePushesCount int32
	perTenantCounts   map[string]int
	perIPCounts       map[string]int

	// Synchronization
	mu sync.RWMutex

	// Queuing
	cloneQueue chan *queueRequest
	pushQueue  chan *queueRequest

	// Metrics
	metrics *MetricsCollector
	logger  *zap.Logger

	// Graceful shutdown
	stopChan chan struct{}
	wg       sync.WaitGroup
}

type queueRequest struct {
	ctx        context.Context
	operation  OperationType
	tenantID   string
	clientIP   string
	responseCh chan *queueResponse
}

type queueResponse struct {
	granted bool
	err     error
}

type OperationType int

const (
	OperationClone OperationType = iota
	OperationPush
)

func (ot OperationType) String() string {
	switch ot {
	case OperationClone:
		return "clone"
	case OperationPush:
		return "push"
	default:
		return "unknown"
	}
}

// MetricsCollector collects operational metrics for Git operations
type MetricsCollector struct {
	operationDuration    prometheus.HistogramVec
	timeoutEvents        prometheus.CounterVec
	progressRate         prometheus.GaugeVec
	concurrentOperations prometheus.GaugeVec
	stallDetections      prometheus.CounterVec
	queueWaitTime        prometheus.HistogramVec
	operationsTotal      prometheus.CounterVec
	operationsInProgress prometheus.GaugeVec
	connectionErrors     prometheus.CounterVec
}

var (
	// Global metrics to avoid duplicate registration
	operationDurationVec    *prometheus.HistogramVec
	timeoutEventsVec        *prometheus.CounterVec
	progressRateVec         *prometheus.GaugeVec
	concurrentOperationsVec *prometheus.GaugeVec
	stallDetectionsVec      *prometheus.CounterVec
	queueWaitTimeVec        *prometheus.HistogramVec
	operationsTotalVec      *prometheus.CounterVec
	operationsInProgressVec *prometheus.GaugeVec
	connectionErrorsVec     *prometheus.CounterVec
	metricsInitOnce         sync.Once
)

func initMetrics(namespace string) {
	operationDurationVec = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "git_operation_duration_seconds",
		Help:      "Duration of Git operations in seconds",
		Buckets:   prometheus.ExponentialBuckets(0.1, 2, 10),
	}, []string{"operation", "status"})

	timeoutEventsVec = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "git_timeout_events_total",
		Help:      "Total number of timeout events",
	}, []string{"type", "operation"})

	progressRateVec = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "git_transfer_rate_bytes_per_second",
		Help:      "Current transfer rate in bytes per second",
	}, []string{"operation", "tenant"})

	concurrentOperationsVec = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "git_concurrent_operations",
		Help:      "Number of concurrent Git operations",
	}, []string{"operation"})

	stallDetectionsVec = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "git_stall_detections_total",
		Help:      "Total number of stall detections",
	}, []string{"operation", "tenant"})

	queueWaitTimeVec = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "git_queue_wait_time_seconds",
		Help:      "Time spent waiting in queue",
		Buckets:   prometheus.ExponentialBuckets(0.01, 2, 8),
	}, []string{"operation"})

	operationsTotalVec = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "git_operations_total",
		Help:      "Total number of Git operations",
	}, []string{"operation", "status"})

	operationsInProgressVec = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "git_operations_in_progress",
		Help:      "Number of Git operations currently in progress",
	}, []string{"operation"})

	connectionErrorsVec = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "git_connection_errors_total",
		Help:      "Total number of connection errors",
	}, []string{"type", "operation"})
}

// NewMetricsCollector creates a new metrics collector
func NewMetricsCollector(namespace string) *MetricsCollector {
	metricsInitOnce.Do(func() {
		initMetrics(namespace)
	})

	return &MetricsCollector{
		operationDuration:    *operationDurationVec,
		timeoutEvents:        *timeoutEventsVec,
		progressRate:         *progressRateVec,
		concurrentOperations: *concurrentOperationsVec,
		stallDetections:      *stallDetectionsVec,
		queueWaitTime:        *queueWaitTimeVec,
		operationsTotal:      *operationsTotalVec,
		operationsInProgress: *operationsInProgressVec,
		connectionErrors:     *connectionErrorsVec,
	}
}

// RecordOperationStart records the start of a Git operation
func (mc *MetricsCollector) RecordOperationStart(operation OperationType, tenantID string) {
	opStr := operation.String()
	mc.operationsTotal.WithLabelValues(opStr, "started").Inc()
	mc.operationsInProgress.WithLabelValues(opStr).Inc()
	mc.concurrentOperations.WithLabelValues(opStr).Inc()
}

// RecordOperationComplete records the completion of a Git operation
func (mc *MetricsCollector) RecordOperationComplete(operation OperationType, duration time.Duration, success bool, tenantID string) {
	opStr := operation.String()
	status := "success"
	if !success {
		status = "failure"
	}

	mc.operationDuration.WithLabelValues(opStr, status).Observe(duration.Seconds())
	mc.operationsTotal.WithLabelValues(opStr, status).Inc()
	mc.operationsInProgress.WithLabelValues(opStr).Dec()
	mc.concurrentOperations.WithLabelValues(opStr).Dec()
}

// RecordTimeout records a timeout event
func (mc *MetricsCollector) RecordTimeout(timeoutType string, operation OperationType) {
	mc.timeoutEvents.WithLabelValues(timeoutType, operation.String()).Inc()
}

// RecordTransferRate records current transfer rate
func (mc *MetricsCollector) RecordTransferRate(operation OperationType, tenantID string, bytesPerSecond float64) {
	mc.progressRate.WithLabelValues(operation.String(), tenantID).Set(bytesPerSecond)
}

// RecordStallDetection records a stall detection event
func (mc *MetricsCollector) RecordStallDetection(operation OperationType, tenantID string) {
	mc.stallDetections.WithLabelValues(operation.String(), tenantID).Inc()
}

// RecordQueueWait records time spent waiting in queue
func (mc *MetricsCollector) RecordQueueWait(operation OperationType, duration time.Duration) {
	mc.queueWaitTime.WithLabelValues(operation.String()).Observe(duration.Seconds())
}

// RecordConnectionError records a connection error
func (mc *MetricsCollector) RecordConnectionError(errorType string, operation OperationType) {
	mc.connectionErrors.WithLabelValues(errorType, operation.String()).Inc()
}

// ConcurrencyConfig holds configuration for concurrency control
type ConcurrencyConfig struct {
	MaxConcurrentClones int
	MaxConcurrentPushes int
	PerTenantLimit      int
	PerIPLimit          int
	QueueSize           int
}

// DefaultConcurrencyConfig returns default concurrency configuration
// Uses centralized configuration manager
func DefaultConcurrencyConfig() *ConcurrencyConfig {
	configManager := NewConfigManager()
	return configManager.ToConcurrencyConfig()
}

// ConcurrencyConfigFromConfig creates ConcurrencyConfig from ConfigManager
func ConcurrencyConfigFromConfig(config *ConfigManager) *ConcurrencyConfig {
	if config == nil {
		return DefaultConcurrencyConfig()
	}
	return config.ToConcurrencyConfig()
}

// NewConcurrencyController creates a new concurrency controller
func NewConcurrencyController(config *ConcurrencyConfig, metrics *MetricsCollector, logger *zap.Logger) *ConcurrencyController {
	if config == nil {
		config = DefaultConcurrencyConfig()
	}

	cc := &ConcurrencyController{
		maxConcurrentClones: config.MaxConcurrentClones,
		maxConcurrentPushes: config.MaxConcurrentPushes,
		perTenantLimit:      config.PerTenantLimit,
		perIPLimit:          config.PerIPLimit,
		perTenantCounts:     make(map[string]int),
		perIPCounts:         make(map[string]int),
		cloneQueue:          make(chan *queueRequest, config.QueueSize),
		pushQueue:           make(chan *queueRequest, config.QueueSize),
		metrics:             metrics,
		logger:              logger,
		stopChan:            make(chan struct{}),
	}

	// Start queue processors
	cc.wg.Add(2)
	go cc.processQueue(OperationClone)
	go cc.processQueue(OperationPush)

	return cc
}

// RequestOperation requests permission to start an operation
func (cc *ConcurrencyController) RequestOperation(ctx context.Context, operation OperationType, tenantID, clientIP string) error {
	startTime := time.Now()
	defer func() {
		cc.metrics.RecordQueueWait(operation, time.Since(startTime))
	}()

	req := &queueRequest{
		ctx:        ctx,
		operation:  operation,
		tenantID:   tenantID,
		clientIP:   clientIP,
		responseCh: make(chan *queueResponse, 1),
	}

	var queue chan *queueRequest
	switch operation {
	case OperationClone:
		queue = cc.cloneQueue
	case OperationPush:
		queue = cc.pushQueue
	default:
		return NewValidationError("operation", operation, "UNKNOWN_TYPE", fmt.Sprintf("unknown operation type: %v", operation))
	}

	select {
	case queue <- req:
		// Request queued successfully
	case <-ctx.Done():
		return ctx.Err()
	default:
		// Queue is full
		queueSize := cap(queue)
		return NewQueueFullError(operation, queueSize)
	}

	// Wait for response
	select {
	case resp := <-req.responseCh:
		if resp.err != nil {
			return resp.err
		}
		if !resp.granted {
			return fmt.Errorf("operation denied due to rate limiting")
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ReleaseOperation releases resources after operation completion
func (cc *ConcurrencyController) ReleaseOperation(operation OperationType, tenantID, clientIP string) {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	switch operation {
	case OperationClone:
		cc.activeClonesCount--
	case OperationPush:
		cc.activePushesCount--
	}

	if tenantID != "" {
		if count, exists := cc.perTenantCounts[tenantID]; exists && count > 0 {
			cc.perTenantCounts[tenantID]--
			if cc.perTenantCounts[tenantID] == 0 {
				delete(cc.perTenantCounts, tenantID)
			}
		}
	}

	if clientIP != "" {
		if count, exists := cc.perIPCounts[clientIP]; exists && count > 0 {
			cc.perIPCounts[clientIP]--
			if cc.perIPCounts[clientIP] == 0 {
				delete(cc.perIPCounts, clientIP)
			}
		}
	}
}

// processQueue processes requests from the operation queue
func (cc *ConcurrencyController) processQueue(operation OperationType) {
	defer cc.wg.Done()

	for {
		select {
		case <-cc.stopChan:
			return
		case req := <-cc.getQueueForOperation(operation):
			granted := cc.checkLimitsAndGrant(req)

			response := &queueResponse{
				granted: granted,
			}

			select {
			case req.responseCh <- response:
			case <-req.ctx.Done():
				// Client context cancelled, release resources if we granted
				if granted {
					cc.ReleaseOperation(req.operation, req.tenantID, req.clientIP)
				}
			}
		}
	}
}

// getQueueForOperation returns the appropriate queue for the operation
func (cc *ConcurrencyController) getQueueForOperation(operation OperationType) chan *queueRequest {
	switch operation {
	case OperationClone:
		return cc.cloneQueue
	case OperationPush:
		return cc.pushQueue
	default:
		return nil
	}
}

// checkLimitsAndGrant checks all limits and grants the operation if possible
func (cc *ConcurrencyController) checkLimitsAndGrant(req *queueRequest) bool {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	// Check global limits
	switch req.operation {
	case OperationClone:
		if int(cc.activeClonesCount) >= cc.maxConcurrentClones {
			return false
		}
	case OperationPush:
		if int(cc.activePushesCount) >= cc.maxConcurrentPushes {
			return false
		}
	}

	// Check per-tenant limit
	if req.tenantID != "" {
		if cc.perTenantCounts[req.tenantID] >= cc.perTenantLimit {
			return false
		}
	}

	// Check per-IP limit
	if req.clientIP != "" {
		if cc.perIPCounts[req.clientIP] >= cc.perIPLimit {
			return false
		}
	}

	// All checks passed, grant the operation
	switch req.operation {
	case OperationClone:
		cc.activeClonesCount++
	case OperationPush:
		cc.activePushesCount++
	}

	if req.tenantID != "" {
		cc.perTenantCounts[req.tenantID]++
	}

	if req.clientIP != "" {
		cc.perIPCounts[req.clientIP]++
	}

	// Record metrics
	cc.metrics.RecordOperationStart(req.operation, req.tenantID)

	return true
}

// GetClientIP extracts client IP from connection or address
func GetClientIP(conn interface{}) string {
	var addr net.Addr

	switch v := conn.(type) {
	case net.Conn:
		if v == nil {
			return ""
		}
		addr = v.RemoteAddr()
	case net.Addr:
		addr = v
	default:
		return ""
	}

	if addr == nil {
		return ""
	}

	if tcpAddr, ok := addr.(*net.TCPAddr); ok {
		return tcpAddr.IP.String()
	}

	// Parse address string for other formats
	addrStr := addr.String()
	if host, _, err := net.SplitHostPort(addrStr); err == nil {
		return host
	}

	return addrStr
}

// Stop gracefully stops the concurrency controller
func (cc *ConcurrencyController) Stop() {
	close(cc.stopChan)
	cc.wg.Wait()
}
