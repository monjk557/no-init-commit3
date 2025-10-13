package permissions

import (
	"context"
	"sync"
	"time"
)

// CircuitBreakerState는 서킷 브레이커의 상태를 나타냅니다.
type CircuitBreakerState int

const (
	// StateClosed indicates the circuit is closed (normal operation).
	StateClosed CircuitBreakerState = iota
	// StateOpen indicates the circuit is open (blocking requests).
	StateOpen
	// StateHalfOpen indicates the circuit is half-open (testing if service is back).
	StateHalfOpen
)

func (s CircuitBreakerState) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CircuitBreakerConfig는 서킷 브레이커의 설정 정보를 담는 구조체입니다.
type CircuitBreakerConfig struct {
	// FailureThreshold is the number of failures required to open the circuit.
	FailureThreshold int

	// RecoveryTimeout is the time to wait before attempting to close the circuit.
	RecoveryTimeout time.Duration

	// MaxHalfOpenRequests is the maximum number of requests allowed in half-open state.
	MaxHalfOpenRequests int
}

// DefaultCircuitBreakerConfig returns a configuration with sensible defaults.
func DefaultCircuitBreakerConfig() *CircuitBreakerConfig {
	return &CircuitBreakerConfig{
		FailureThreshold:    5,
		RecoveryTimeout:     30 * time.Second,
		MaxHalfOpenRequests: 1,
	}
}

// CircuitBreaker는 서킷 브레이커 패턴을 구현하는 구조체입니다.
// 외부 서비스 장애 시 자동으로 요청을 차단합니다.
type CircuitBreaker struct {
	config          *CircuitBreakerConfig
	state           CircuitBreakerState
	failureCount    int
	halfOpenCount   int
	lastFailureTime time.Time
	lastSuccessTime time.Time
	mutex           sync.RWMutex
}

// NewCircuitBreaker는 주어진 설정으로 새로운 서킷 브레이커를 생성합니다.
func NewCircuitBreaker(config *CircuitBreakerConfig) *CircuitBreaker {
	if config == nil {
		config = DefaultCircuitBreakerConfig()
	}

	return &CircuitBreaker{
		config: config,
		state:  StateClosed,
	}
}

// Execute는 서킷이 허용할 때 주어진 함수를 실행합니다.
func (cb *CircuitBreaker) Execute(ctx context.Context, fn func(context.Context) error) error {
	if !cb.canExecute() {
		return &CircuitBreakerError{
			State:   cb.getState(),
			Message: "circuit breaker is open",
		}
	}

	err := fn(ctx)
	cb.recordResult(err)
	return err
}

// canExecute determines if a request can be executed based on circuit state.
func (cb *CircuitBreaker) canExecute() bool {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()

	switch cb.state {
	case StateClosed:
		return true
	case StateOpen:
		// Check if recovery timeout has passed
		return time.Since(cb.lastFailureTime) >= cb.config.RecoveryTimeout
	case StateHalfOpen:
		// Allow limited requests in half-open state
		return cb.halfOpenCount < cb.config.MaxHalfOpenRequests
	default:
		return false
	}
}

// recordResult updates the circuit breaker state based on the operation result.
func (cb *CircuitBreaker) recordResult(err error) {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	if err != nil {
		cb.onFailure()
	} else {
		cb.onSuccess()
	}
}

// onFailure handles a failed operation.
func (cb *CircuitBreaker) onFailure() {
	cb.failureCount++
	cb.lastFailureTime = time.Now()

	switch cb.state {
	case StateClosed:
		if cb.failureCount >= cb.config.FailureThreshold {
			cb.setState(StateOpen)
		}
	case StateHalfOpen:
		cb.setState(StateOpen)
		cb.halfOpenCount = 0
	}
}

// onSuccess handles a successful operation.
func (cb *CircuitBreaker) onSuccess() {
	cb.lastSuccessTime = time.Now()

	switch cb.state {
	case StateClosed:
		cb.failureCount = 0
	case StateHalfOpen:
		cb.setState(StateClosed)
		cb.failureCount = 0
		cb.halfOpenCount = 0
	case StateOpen:
		// This shouldn't happen, but reset if it does
		cb.setState(StateClosed)
		cb.failureCount = 0
	}
}

// setState updates the circuit breaker state.
func (cb *CircuitBreaker) setState(newState CircuitBreakerState) {
	if cb.state != newState {
		cb.state = newState

		if newState == StateHalfOpen {
			cb.halfOpenCount = 0
		}
	}
}

// getState returns the current state of the circuit breaker.
func (cb *CircuitBreaker) getState() CircuitBreakerState {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()

	// Transition from open to half-open if recovery timeout has passed
	if cb.state == StateOpen && time.Since(cb.lastFailureTime) >= cb.config.RecoveryTimeout {
		cb.mutex.RUnlock()
		cb.mutex.Lock()
		// Double-check after acquiring write lock
		if cb.state == StateOpen && time.Since(cb.lastFailureTime) >= cb.config.RecoveryTimeout {
			cb.setState(StateHalfOpen)
		}
		cb.mutex.Unlock()
		cb.mutex.RLock()
	}

	return cb.state
}

// GetMetrics returns metrics about the circuit breaker.
func (cb *CircuitBreaker) GetMetrics() CircuitBreakerMetrics {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()

	return CircuitBreakerMetrics{
		State:             cb.state,
		FailureCount:      cb.failureCount,
		HalfOpenCount:     cb.halfOpenCount,
		LastFailureTime:   cb.lastFailureTime,
		LastSuccessTime:   cb.lastSuccessTime,
		TimeUntilHalfOpen: cb.timeUntilHalfOpen(),
	}
}

// timeUntilHalfOpen calculates time remaining until half-open state.
func (cb *CircuitBreaker) timeUntilHalfOpen() time.Duration {
	if cb.state != StateOpen {
		return 0
	}

	elapsed := time.Since(cb.lastFailureTime)
	if elapsed >= cb.config.RecoveryTimeout {
		return 0
	}

	return cb.config.RecoveryTimeout - elapsed
}

// CircuitBreakerMetrics holds metrics about the circuit breaker.
type CircuitBreakerMetrics struct {
	State             CircuitBreakerState
	FailureCount      int
	HalfOpenCount     int
	LastFailureTime   time.Time
	LastSuccessTime   time.Time
	TimeUntilHalfOpen time.Duration
}

// CircuitBreakerError represents an error when the circuit breaker is open.
type CircuitBreakerError struct {
	State   CircuitBreakerState
	Message string
}

func (e *CircuitBreakerError) Error() string {
	return e.Message
}

// FallbackPolicy는 API 서비스를 사용할 수 없을 때의 동작을 정의합니다.
type FallbackPolicy int

const (
	// FallbackDeny denies all requests when the service is unavailable.
	FallbackDeny FallbackPolicy = iota
	// FallbackAllow allows all requests when the service is unavailable.
	FallbackAllow
	// FallbackReadOnly allows only read requests when the service is unavailable.
	FallbackReadOnly
	// FallbackUseCache uses cached results only, denying requests without cache entries.
	FallbackUseCache
)

func (p FallbackPolicy) String() string {
	switch p {
	case FallbackDeny:
		return "deny"
	case FallbackAllow:
		return "allow"
	case FallbackReadOnly:
		return "read-only"
	case FallbackUseCache:
		return "use-cache"
	default:
		return "unknown"
	}
}

// ShouldAllowAccess determines if access should be allowed based on the fallback policy.
func (p FallbackPolicy) ShouldAllowAccess(operation Operation, hasCachedResult bool) bool {
	switch p {
	case FallbackDeny:
		return false
	case FallbackAllow:
		return true
	case FallbackReadOnly:
		return operation == OperationRead
	case FallbackUseCache:
		return hasCachedResult
	default:
		return false
	}
}
